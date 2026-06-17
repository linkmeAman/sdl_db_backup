package backupapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func readLatestRunInfo(path string) (*LatestRunInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var record runRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		return &LatestRunInfo{
			Timestamp:          record.Timestamp,
			RunID:              record.RunID,
			Status:             record.Status,
			RunFolder:          record.RunFolder,
			LogFile:            record.LogFile,
			FailureReason:      record.FailureReason,
			CleanupError:       record.CleanupError,
			Duration:           record.Duration,
			DatabasesTotal:     record.DatabasesTotal,
			DatabasesSucceeded: record.DatabasesSucceeded,
			DatabasesFailed:    record.DatabasesFailed,
			OSUser:             record.OSUser,
			ExecutionSource:    record.ExecutionSource,
			Hostname:           record.Hostname,
			PID:                record.PID,
		}, nil
	}
	return nil, nil
}

func checkLogicalHealth(cfg config) HealthCheck {
	check := HealthCheck{Name: "logical", Status: "ok", Message: "logical backup prerequisites look valid"}
	if !cfg.LogicalEnabled {
		check.Status = "disabled"
		check.Message = "logical backup is disabled in config"
		return check
	}
	if err := validatePrerequisites(cfg, true); err != nil {
		check.Status = "error"
		check.Message = err.Error()
	}
	return check
}

func checkPhysicalHealth(cfg config) HealthCheck {
	check := HealthCheck{Name: "physical", Status: "ok", Message: "physical backup prerequisites look valid"}
	if !cfg.PhysicalEnabled {
		check.Status = "disabled"
		check.Message = "physical backup is disabled in config"
		return check
	}
	if !cfg.PhysicalS3UploadEnabled {
		check.Status = "disabled"
		check.Message = "physical backup is skipped because BACKUP_PHYSICAL_S3_UPLOAD_ENABLED=false"
		return check
	}
	if err := validateWritableDir(cfg.BackupDir); err != nil {
		check.Status = "error"
		check.Message = err.Error()
		return check
	}
	if err := validateWritableDir(cfg.LogDir); err != nil {
		check.Status = "error"
		check.Message = err.Error()
		return check
	}
	if _, err := exec.LookPath(cfg.MySQLBin); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("mysql binary %q not found in PATH: %v", cfg.MySQLBin, err)
		return check
	}
	if _, err := exec.LookPath(cfg.XtrabackupBin); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("xtrabackup binary %q not found in PATH: %v", cfg.XtrabackupBin, err)
		return check
	}
	if _, err := exec.LookPath(cfg.XbcloudBin); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("xbcloud binary %q not found in PATH: %v", cfg.XbcloudBin, err)
		return check
	}
	if cfg.S3Bucket == "" {
		check.Status = "error"
		check.Message = "physical backup requires BACKUP_S3_BUCKET"
		return check
	}
	if cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
		check.Status = "error"
		check.Message = "physical backup requires S3 credentials"
		return check
	}
	if err := checkXtrabackupPrivileges(cfg); err != nil {
		check.Status = "error"
		check.Message = err.Error()
		return check
	}
	return check
}

func pathOwnershipSummary(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Sprintf("mode=%#o", info.Mode().Perm())
	}
	return fmt.Sprintf("uid=%d gid=%d mode=%#o", stat.Uid, stat.Gid, info.Mode().Perm())
}

func probeExistingWritableDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	tempFile, err := os.CreateTemp(path, ".sdl-backup-health-*")
	if err != nil {
		return err
	}
	name := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	return nil
}

func checkDirectoryHealth(name, path string) HealthCheck {
	check := HealthCheck{Name: name}
	info, err := os.Stat(path)
	if err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("%s missing or unreadable: %v", path, err)
		return check
	}
	if !info.IsDir() {
		check.Status = "error"
		check.Message = fmt.Sprintf("%s is not a directory (%s)", path, pathOwnershipSummary(info))
		return check
	}
	if err := probeExistingWritableDir(path); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("%s is not writable (%s): %v", path, pathOwnershipSummary(info), err)
		return check
	}
	check.Status = "ok"
	check.Message = fmt.Sprintf("%s writable (%s)", path, pathOwnershipSummary(info))
	return check
}

func checkMetricsHealth(cfg config) HealthCheck {
	path := resolvedMetricsFilePath(cfg.MetricsFile)
	dir := filepath.Dir(path)
	check := HealthCheck{Name: "metrics"}
	info, err := os.Stat(dir)
	if err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("metrics directory %s missing or unreadable: %v", dir, err)
		return check
	}
	if !info.IsDir() {
		check.Status = "error"
		check.Message = fmt.Sprintf("metrics directory %s is not a directory (%s)", dir, pathOwnershipSummary(info))
		return check
	}
	if err := validateMetricsPathWritable(path); err != nil {
		check.Status = "error"
		check.Message = fmt.Sprintf("metrics path %s is not writable (%s): %v", path, pathOwnershipSummary(info), err)
		return check
	}
	fileInfo, err := os.Stat(path)
	if err == nil {
		check.Status = "ok"
		check.Message = fmt.Sprintf("metrics path %s writable; file=%s dir=%s", path, pathOwnershipSummary(fileInfo), pathOwnershipSummary(info))
		return check
	}
	check.Status = "ok"
	check.Message = fmt.Sprintf("metrics path %s writable; file not created yet; dir=%s", path, pathOwnershipSummary(info))
	return check
}

func GetHealthReport(ctx context.Context, envPath string) (HealthReport, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return HealthReport{}, err
	}
	resolvedPath := ResolveEnvFilePath(envPath)
	report := HealthReport{
		ConfigPath:   resolvedPath,
		RunLogPath:   cfg.RunLogPath,
		DailyLogPath: filepath.Join(cfg.LogDir, time.Now().Format("2006-01-02")+".log"),
		Logical:      checkLogicalHealth(cfg),
		Physical:     checkPhysicalHealth(cfg),
		Metrics:      checkMetricsHealth(cfg),
		Directories: []HealthCheck{
			checkDirectoryHealth("backup_dir", cfg.BackupDir),
			checkDirectoryHealth("log_dir", cfg.LogDir),
			checkDirectoryHealth("metrics_dir", filepath.Dir(resolvedMetricsFilePath(cfg.MetricsFile))),
		},
		Observability: GetObservabilityReport(cfg),
	}
	runtime, runtimeErr := GetRuntimeProfile(envPath)
	if runtimeErr == nil {
		report.Runtime = runtime
	}
	latestRun, err := readLatestRunInfo(cfg.RunLogPath)
	if err != nil {
		return report, err
	}
	report.LatestRun = latestRun
	return report, nil
}

type multiCloser struct {
	closers []io.Closer
}
