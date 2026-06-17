package backupapp

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DatabaseValidationResult struct {
	Database string `json:"database"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
}

type LogicalValidationResult struct {
	RunID     string                     `json:"run_id"`
	Valid     bool                       `json:"valid"`
	Databases []DatabaseValidationResult `json:"databases"`
	Error     string                     `json:"error,omitempty"`
}

type tailWriter struct {
	buf []byte
}

func (tw *tailWriter) Write(p []byte) (n int, err error) {
	const keep = 256
	tw.buf = append(tw.buf, p...)
	if len(tw.buf) > keep {
		tw.buf = tw.buf[len(tw.buf)-keep:]
	}
	return len(p), nil
}

func ValidateLogicalRun(cfg Config, runID string) (LogicalValidationResult, error) {
	res := LogicalValidationResult{RunID: runID}

	run, err := ReadRunByID(cfg.RunLogPath, runID)
	if err != nil {
		return res, fmt.Errorf("read run error: %v", err)
	}
	if run == nil {
		return res, fmt.Errorf("run %q not found", runID)
	}
	if run.RunFolder == "" {
		res.Error = "No local run folder recorded for this backup"
		return res, nil
	}

	if _, err := os.Stat(run.RunFolder); os.IsNotExist(err) {
		res.Error = "Local backup files have been removed (retention period exceeded)"
		return res, nil
	}

	allValid := true
	for _, dbRes := range run.Results {
		dbPath := filepath.Join(run.RunFolder, dbRes.Name+".sql.gz")
		dbValid, dbErr := validateGzipSQLFile(dbPath)
		errMsg := ""
		if dbErr != nil {
			errMsg = dbErr.Error()
		}
		res.Databases = append(res.Databases, DatabaseValidationResult{
			Database: dbRes.Name,
			Valid:    dbValid,
			Error:    errMsg,
		})
		if !dbValid {
			allValid = false
		}
	}

	res.Valid = allValid && len(run.Results) > 0

	// Emit metrics based on this validation.
	status := 0
	if res.Valid {
		status = 1
	}
	emitValidationMetrics(cfg, status)

	return res, nil
}

func validateGzipSQLFile(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("file missing")
		}
		return false, err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return false, fmt.Errorf("invalid gzip header: %v", err)
	}
	defer gz.Close()

	tw := &tailWriter{}
	if _, err := io.Copy(tw, gz); err != nil {
		return false, fmt.Errorf("gzip checksum or read error: %v", err)
	}

	tailStr := string(tw.buf)
	if !strings.Contains(tailStr, "-- Dump completed") {
		return false, fmt.Errorf("missing '-- Dump completed' signature at end of file")
	}

	return true, nil
}

func FullRestoreValidation(cfg Config, runID string, progress func(string)) (LogicalValidationResult, error) {
	res := LogicalValidationResult{RunID: runID}

	if !cfg.RestoreTestEnabled {
		return res, fmt.Errorf("restore validation is disabled in configuration")
	}
	if cfg.RestoreTestHost == "" {
		return res, fmt.Errorf("RESTORE_TEST_HOST is not configured")
	}

	run, err := ReadRunByID(cfg.RunLogPath, runID)
	if err != nil {
		return res, fmt.Errorf("read run error: %v", err)
	}
	if run == nil {
		return res, fmt.Errorf("run %q not found", runID)
	}
	if run.RunFolder == "" {
		res.Error = "No local run folder recorded for this backup"
		return res, nil
	}

	if _, err := os.Stat(run.RunFolder); os.IsNotExist(err) {
		res.Error = "Local backup files have been removed (retention period exceeded)"
		return res, nil
	}

	allValid := true
	for _, dbRes := range run.Results {
		if progress != nil {
			progress(fmt.Sprintf("Testing restore for %s...", dbRes.Name))
		}

		dbValid, dbErr := testRestoreDatabase(cfg, run.RunFolder, dbRes)
		errMsg := ""
		if dbErr != nil {
			errMsg = dbErr.Error()
		}

		res.Databases = append(res.Databases, DatabaseValidationResult{
			Database: dbRes.Name,
			Valid:    dbValid,
			Error:    errMsg,
		})
		if !dbValid {
			allValid = false
		}
	}

	res.Valid = allValid && len(run.Results) > 0

	status := 0
	if res.Valid {
		status = 1
	}
	emitValidationMetrics(cfg, status)

	return res, nil
}

func testRestoreDatabase(cfg Config, runFolder string, dbRes DatabaseResult) (bool, error) {
	gzPath := filepath.Join(runFolder, dbRes.Name+".sql.gz")
	if _, err := os.Stat(gzPath); os.IsNotExist(err) {
		return false, fmt.Errorf("dump file missing")
	}

	tempDB := fmt.Sprintf("test_restore_%s", dbRes.Name)

	testCmd := func(args ...string) *exec.Cmd {
		ctx, _ := context.WithTimeout(context.Background(), 30*time.Minute)
		cmdArgs := []string{
			"-h", cfg.RestoreTestHost,
			"-P", cfg.RestoreTestPort,
			"-u", cfg.RestoreTestUser,
		}
		if cfg.RestoreTestPass != "" {
			cmdArgs = append(cmdArgs, "-p"+cfg.RestoreTestPass)
		}
		cmdArgs = append(cmdArgs, args...)
		return exec.CommandContext(ctx, cfg.MySQLBin, cmdArgs...)
	}

	dropCmd := testCmd("-e", fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", tempDB))
	if out, err := dropCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("failed to drop temp db: %s", string(out))
	}

	createCmd := testCmd("-e", fmt.Sprintf("CREATE DATABASE `%s`", tempDB))
	if out, err := createCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("failed to create temp db: %s", string(out))
	}

	defer func() {
		cleanupCmd := testCmd("-e", fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", tempDB))
		_ = cleanupCmd.Run()
	}()

	importCmd := testCmd(tempDB)

	gzFile, err := os.Open(gzPath)
	if err != nil {
		return false, err
	}
	defer gzFile.Close()

	gzReader, err := gzip.NewReader(gzFile)
	if err != nil {
		return false, err
	}
	defer gzReader.Close()

	importCmd.Stdin = gzReader
	if out, err := importCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("import failed: %s", string(out))
	}

	countCmd := testCmd("-N", "-e", fmt.Sprintf("SELECT COALESCE(SUM(TABLE_ROWS), 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE'", tempDB))
	out, err := countCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to count rows: %s", string(out))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		valStr = "0"
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid row count from temp db: %v", err)
	}

	if count != dbRes.RowCounts {
		return false, fmt.Errorf("row count mismatch: manifest=%d, restored=%d", dbRes.RowCounts, count)
	}

	return true, nil
}
