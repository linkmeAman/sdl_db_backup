package backupapp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func runXtrabackupCmd(bin string, args []string, runAsUser string) error {
	var cmd *exec.Cmd
	if runAsUser != "" {
		sudoArgs := append([]string{"-n", "-u", runAsUser, bin}, args...)
		cmd = exec.Command("sudo", sudoArgs...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	var lastLine, diskErrLine string
	scanner := bufio.NewScanner(stderrPipe)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line != "" {
			lastLine = line
		}
		// Capture the most recent disk-full message so it surfaces in the error.
		lower := strings.ToLower(line)
		if strings.Contains(lower, "no space left") || strings.Contains(lower, "errno 28") {
			diskErrLine = line
		}
		if shouldLogPhysicalLine("xtrabackup", line) {
			log.Printf("xtrabackup: %s", raw)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return fmt.Errorf("read xtrabackup stderr: %w", scanErr)
	}
	if err := cmd.Wait(); err != nil {
		summary := lastLine
		if diskErrLine != "" {
			summary = diskErrLine
		}
		if summary != "" {
			return fmt.Errorf("%w (%s)", err, summary)
		}
		return err
	}
	return nil
}

// writeXtrabackupCredsFile writes a temporary MySQL defaults file containing
// the xtrabackup credentials. The caller must delete the file when done.
// When xtrabackup runs as another OS user (for example mysql via sudo),
// the file must be readable by that user.
func writeXtrabackupCredsFile(cfg config, runAsUser string) (string, error) {
	f, err := os.CreateTemp("", "xtrabackup-creds-*.cnf")
	if err != nil {
		return "", fmt.Errorf("create xtrabackup creds file: %w", err)
	}
	defer f.Close()
	mode := os.FileMode(0o600)
	if runAsUser != "" {
		// xtrabackup executes as another user (usually mysql), so the temporary
		// defaults file must be readable by that process.
		mode = 0o644
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("chmod xtrabackup creds file: %w", err)
	}
	_, err = fmt.Fprintf(f, "[xtrabackup]\npassword=%s\n", cfg.XtrabackupPass)
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write xtrabackup creds file: %w", err)
	}
	return f.Name(), nil
}

// checkXtrabackupPrivileges verifies the xtrabackup user has the BACKUP_ADMIN
// privilege required by xtrabackup 8.0. Returns a descriptive error with the
// exact GRANT statement if the privilege is missing.
func checkXtrabackupPrivileges(cfg config) error {
	runSQL := func(query string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
		defer cancel()

		args := []string{"-u", cfg.XtrabackupUser, "-N", "-s", "-e", query}
		if cfg.XtrabackupSocket != "" {
			args = append([]string{"-S", cfg.XtrabackupSocket}, args...)
		} else {
			args = append([]string{"-h", cfg.DBHost, "-P", cfg.DBPort}, args...)
		}
		cmd := exec.CommandContext(ctx, cfg.MySQLBin, args...)
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.XtrabackupPass)
		out, err := cmd.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(out))
			return "", fmt.Errorf("%s", chooseFailureMessage(err, message))
		}
		return strings.TrimSpace(string(out)), nil
	}

	backupAdminCount, err := runSQL("SELECT COUNT(*) FROM information_schema.USER_PRIVILEGES WHERE PRIVILEGE_TYPE='BACKUP_ADMIN'")
	if err != nil {
		return fmt.Errorf("xtrabackup privilege check: cannot connect as %q: %v", cfg.XtrabackupUser, err)
	}
	if backupAdminCount == "0" || backupAdminCount == "" {
		return fmt.Errorf(
			"xtrabackup user %q lacks BACKUP_ADMIN privilege; grant it with:\n"+
				"  GRANT BACKUP_ADMIN, PROCESS, RELOAD, LOCK TABLES, REPLICATION CLIENT ON *.* TO '%s'@'localhost';\n"+
				"  FLUSH PRIVILEGES;",
			cfg.XtrabackupUser, cfg.XtrabackupUser)
	}

	perfChecks := []struct {
		table string
		query string
	}{
		{table: "replication_group_members", query: "SELECT 1 FROM performance_schema.replication_group_members LIMIT 1"},
		{table: "keyring_component_status", query: "SELECT 1 FROM performance_schema.keyring_component_status LIMIT 1"},
	}
	for _, check := range perfChecks {
		_, err := runSQL(check.query)
		if err == nil {
			continue
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "doesn't exist") || strings.Contains(msg, "unknown table") || strings.Contains(msg, "1146") {
			// Table not present on this server flavor/version; ignore.
			continue
		}
		if strings.Contains(msg, "1142") || strings.Contains(msg, "select command denied") {
			return fmt.Errorf(
				"xtrabackup user %q lacks SELECT on performance_schema.%s; grant it with:\n"+
					"  GRANT SELECT ON performance_schema.%s TO '%s'@'localhost';\n"+
					"  FLUSH PRIVILEGES;",
				cfg.XtrabackupUser, check.table, check.table, cfg.XtrabackupUser)
		}
		return fmt.Errorf("xtrabackup privilege check failed on performance_schema.%s: %v", check.table, err)
	}

	return nil
}

// runPhysicalBackup streams xtrabackup directly to S3 via xbcloud.
// No local physical directory is created.
func runPhysicalBackup(cfg config, runDir string) physicalBackupResult {
	started := time.Now()
	result := physicalBackupResult{Status: "failed"}
	runID := filepath.Base(runDir)
	objectKey := strings.Trim(strings.TrimSpace(cfg.S3PhysicalPrefix), "/") + "/" + runID + "/physical.xbstream"
	result.TargetDir = fmt.Sprintf("s3://%s/%s", cfg.S3Bucket, objectKey)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PhysicalTimeout)
	defer cancel()

	if cfg.S3Bucket == "" {
		result.Error = "physical backup requires BACKUP_S3_BUCKET"
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	if cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
		result.Error = "physical backup requires S3 credentials (set BACKUP_S3_KEY_ID and BACKUP_S3_KEY_SECRET, or AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY)"
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	xtrabackupBin, err := exec.LookPath(cfg.XtrabackupBin)
	if err != nil {
		result.Error = fmt.Sprintf("xtrabackup binary %q not found in PATH: %v (install Percona XtraBackup or set BACKUP_XTRABACKUP_BIN)", cfg.XtrabackupBin, err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	xbcloudBin, err := exec.LookPath(cfg.XbcloudBin)
	if err != nil {
		result.Error = fmt.Sprintf("xbcloud binary %q not found in PATH: %v", cfg.XbcloudBin, err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	if err := checkXtrabackupPrivileges(cfg); err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		log.Printf("physical backup: privilege check failed: %s", result.Error)
		return result
	}

	credsFile, err := writeXtrabackupCredsFile(cfg, cfg.XtrabackupRunAsUser)
	if err != nil {
		result.Error = err.Error()
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	defer func() { _ = os.Remove(credsFile) }()

	backupArgs := []string{
		"--defaults-extra-file=" + credsFile,
		"--no-version-check",
		"--backup",
		"--stream=xbstream",
		"--user=" + cfg.XtrabackupUser,
		"--parallel=" + strconv.Itoa(cfg.XtrabackupParallel),
	}
	if cfg.XtrabackupSocket != "" {
		backupArgs = append(backupArgs, "--socket="+cfg.XtrabackupSocket)
	} else {
		backupArgs = append(backupArgs, "--host="+cfg.DBHost, "--port="+cfg.DBPort)
	}

	var xtrabackupCmd *exec.Cmd
	if cfg.XtrabackupRunAsUser != "" {
		sudoArgs := append([]string{"-n", "-u", cfg.XtrabackupRunAsUser, xtrabackupBin}, backupArgs...)
		xtrabackupCmd = exec.CommandContext(ctx, "sudo", sudoArgs...)
		log.Printf("physical backup: running xtrabackup as OS user=%s", cfg.XtrabackupRunAsUser)
	} else {
		xtrabackupCmd = exec.CommandContext(ctx, xtrabackupBin, backupArgs...)
	}
	workDir := strings.TrimSpace(cfg.XtrabackupWorkDir)
	if workDir == "" {
		workDir = "/tmp"
	}
	xtrabackupCmd.Dir = workDir

	streamOut, err := xtrabackupCmd.StdoutPipe()
	if err != nil {
		result.Error = fmt.Sprintf("xtrabackup stdout pipe: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	xtrabackupErr, err := xtrabackupCmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Sprintf("xtrabackup stderr pipe: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	xbcloudArgs := []string{
		"put",
		"--storage=s3",
		"--s3-bucket=" + cfg.S3Bucket,
		"--s3-region=" + cfg.S3Region,
		objectKey,
	}
	xbcloudCmd := exec.CommandContext(ctx, xbcloudBin, xbcloudArgs...)
	xbcloudCmd.Dir = workDir
	xbcloudCmd.Env = append(os.Environ(),
		"AWS_ACCESS_KEY_ID="+cfg.S3KeyID,
		"AWS_SECRET_ACCESS_KEY="+cfg.S3KeySecret,
		"AWS_DEFAULT_REGION="+cfg.S3Region,
	)
	xbcloudCmd.Stdin = streamOut
	xbcloudErr, err := xbcloudCmd.StderrPipe()
	if err != nil {
		result.Error = fmt.Sprintf("xbcloud stderr pipe: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	log.Printf("physical backup: using working directory %s", workDir)
	log.Printf("physical backup: streaming directly to s3://%s/%s", cfg.S3Bucket, objectKey)

	if err := xtrabackupCmd.Start(); err != nil {
		result.Error = fmt.Sprintf("start xtrabackup: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}
	if err := xbcloudCmd.Start(); err != nil {
		_ = xtrabackupCmd.Process.Kill()
		_ = xtrabackupCmd.Wait()
		result.Error = fmt.Sprintf("start xbcloud: %v", err)
		result.Duration = time.Since(started).Round(time.Millisecond).String()
		return result
	}

	type loggedResult struct {
		lastLine string
		diskLine string
		err      error
	}
	logPipe := func(prefix string, reader io.Reader) <-chan loggedResult {
		ch := make(chan loggedResult, 1)
		go func() {
			defer close(ch)
			var res loggedResult
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				raw := scanner.Text()
				line := strings.TrimSpace(raw)
				if line != "" {
					res.lastLine = line
				}
				lower := strings.ToLower(line)
				if strings.Contains(lower, "no space left") || strings.Contains(lower, "errno 28") {
					res.diskLine = line
				}
				if shouldLogPhysicalLine(prefix, line) {
					log.Printf("%s: %s", prefix, raw)
				}
			}
			res.err = scanner.Err()
			ch <- res
		}()
		return ch
	}

	xtrabackupLogs := logPipe("xtrabackup", xtrabackupErr)
	xbcloudLogs := logPipe("xbcloud", xbcloudErr)

	xtrabackupWaitErr := xtrabackupCmd.Wait()
	xbcloudWaitErr := xbcloudCmd.Wait()
	xtrabackupScan := <-xtrabackupLogs
	xbcloudScan := <-xbcloudLogs

	result.Duration = time.Since(started).Round(time.Millisecond).String()

	if xtrabackupScan.err != nil && !isIgnorablePipeReadError(xtrabackupScan.err) {
		result.Error = fmt.Sprintf("read xtrabackup stderr: %v", xtrabackupScan.err)
		return result
	}
	if xbcloudScan.err != nil && !isIgnorablePipeReadError(xbcloudScan.err) {
		result.Error = fmt.Sprintf("read xbcloud stderr: %v", xbcloudScan.err)
		return result
	}
	if xtrabackupWaitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("physical backup timed out after %s", cfg.PhysicalTimeout)
			log.Printf("physical backup: %s", result.Error)
			return result
		}
		summary := xtrabackupScan.lastLine
		if xtrabackupScan.diskLine != "" {
			summary = xtrabackupScan.diskLine
		}
		if summary != "" {
			result.Error = fmt.Sprintf("physical backup stream failed: %v (%s)", xtrabackupWaitErr, summary)
		} else {
			result.Error = fmt.Sprintf("physical backup stream failed: %v", xtrabackupWaitErr)
		}
		log.Printf("physical backup: %s", result.Error)
		return result
	}
	if xbcloudWaitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("physical backup timed out after %s", cfg.PhysicalTimeout)
			log.Printf("physical backup: %s", result.Error)
			return result
		}
		summary := xbcloudScan.lastLine
		if summary != "" {
			result.Error = fmt.Sprintf("physical backup upload failed: %v (%s)", xbcloudWaitErr, summary)
		} else {
			result.Error = fmt.Sprintf("physical backup upload failed: %v", xbcloudWaitErr)
		}
		log.Printf("physical backup: %s", result.Error)
		return result
	}

	result.Status = "success"
	log.Printf("physical backup: completed successfully target=%s duration=%s", result.TargetDir, result.Duration)
	return result
}

func shouldLogPhysicalLine(prefix, line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}

	switch {
	case strings.Contains(lower, "error"),
		strings.Contains(lower, "warning"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "fatal"),
		strings.Contains(lower, "no space left"),
		strings.Contains(lower, "errno 28"),
		strings.Contains(lower, "completed ok"),
		strings.Contains(lower, "upload completed"),
		strings.Contains(lower, "transaction log of lsn"),
		strings.Contains(lower, "backup created in directory"),
		strings.Contains(lower, "mysql binlog position"):
		return true
	}

	if prefix == "xbcloud" {
		return strings.Contains(lower, "upload completed")
	}

	if prefix == "xtrabackup" {
		return strings.Contains(lower, "mysql binlog position") ||
			strings.Contains(lower, "backup created in directory") ||
			strings.Contains(lower, "completed ok") ||
			strings.Contains(lower, "transaction log of lsn")
	}

	return false
}
