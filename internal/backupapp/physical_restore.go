package backupapp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PhysicalRestoreValidationResult describes a non-destructive physical backup
// drill. The drill downloads the stream into a temporary directory, extracts
// it, and runs xtrabackup --prepare. It never stops or changes a MySQL server.
type PhysicalRestoreValidationResult struct {
	RunID     string `json:"run_id"`
	Valid     bool   `json:"valid"`
	ObjectKey string `json:"object_key,omitempty"`
	Error     string `json:"error,omitempty"`
}

func physicalObjectKey(cfg config, runID string) string {
	prefix := strings.Trim(strings.TrimSpace(cfg.S3PhysicalPrefix), "/")
	if prefix == "" {
		return runID + "/physical.xbstream"
	}
	return prefix + "/" + runID + "/physical.xbstream"
}

// ValidatePhysicalRun performs a prepare-only restore drill. Run it on a
// dedicated verification host with enough temporary disk for the full backup.
func ValidatePhysicalRun(cfg Config, runID string, progress func(string)) (PhysicalRestoreValidationResult, error) {
	result := PhysicalRestoreValidationResult{RunID: runID}
	if cfg.DBEngine != "mysql" {
		return result, fmt.Errorf("physical restore validation is supported only for DB_ENGINE=mysql")
	}
	run, err := ReadRunByID(cfg.RunLogPath, runID)
	if err != nil {
		return result, fmt.Errorf("read run history: %w", err)
	}
	if run == nil || run.PhysicalBackup == nil || run.PhysicalBackup.Status != "success" {
		return result, fmt.Errorf("run %q has no successful physical backup", runID)
	}
	if strings.TrimSpace(run.PhysicalBackup.CompletionObject) == "" {
		return result, fmt.Errorf("run %q has no verified physical completion marker", runID)
	}
	if cfg.S3Bucket == "" || cfg.S3Region == "" || cfg.S3KeyID == "" || cfg.S3KeySecret == "" {
		return result, fmt.Errorf("physical restore validation requires direct S3 bucket, region, and credentials")
	}
	xbcloudBin, err := exec.LookPath(cfg.XbcloudBin)
	if err != nil {
		return result, fmt.Errorf("xbcloud binary %q not found: %w", cfg.XbcloudBin, err)
	}
	xtrabackupBin, err := exec.LookPath(cfg.XtrabackupBin)
	if err != nil {
		return result, fmt.Errorf("xtrabackup binary %q not found: %w", cfg.XtrabackupBin, err)
	}
	xbstreamBin, err := exec.LookPath("xbstream")
	if err != nil {
		return result, fmt.Errorf("xbstream binary not found in PATH: %w", err)
	}

	workRoot := strings.TrimSpace(cfg.XtrabackupWorkDir)
	if workRoot == "" {
		return result, fmt.Errorf("BACKUP_XTRABACKUP_WORK_DIR is required for physical restore validation")
	}
	if err := validateWritableDir(workRoot); err != nil {
		return result, fmt.Errorf("physical restore work directory: %w", err)
	}
	workDir, err := os.MkdirTemp(workRoot, "physical-restore-validate-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(workDir)
	extractDir := filepath.Join(workDir, "prepared")
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return result, err
	}

	objectKey := physicalObjectKey(cfg, runID)
	result.ObjectKey = objectKey
	if progress != nil {
		progress("downloading physical backup stream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PhysicalTimeout)
	defer cancel()
	download := exec.CommandContext(ctx, xbcloudBin,
		"get", "--storage=s3", "--s3-bucket="+cfg.S3Bucket, "--s3-region="+cfg.S3Region, objectKey)
	download.Env = append(os.Environ(), "AWS_ACCESS_KEY_ID="+cfg.S3KeyID, "AWS_SECRET_ACCESS_KEY="+cfg.S3KeySecret, "AWS_DEFAULT_REGION="+cfg.S3Region)
	stream, err := download.StdoutPipe()
	if err != nil {
		return result, fmt.Errorf("open xbcloud output: %w", err)
	}
	var downloadErr, extractErr bytes.Buffer
	download.Stderr = &downloadErr
	extract := exec.CommandContext(ctx, xbstreamBin, "-x", "-C", extractDir)
	extract.Stdin = stream
	extract.Stderr = &extractErr
	if err := download.Start(); err != nil {
		return result, fmt.Errorf("start xbcloud download: %w", err)
	}
	if err := extract.Start(); err != nil {
		_ = download.Process.Kill()
		_ = download.Wait()
		return result, fmt.Errorf("start xbstream extraction: %w", err)
	}
	downloadWaitErr := download.Wait()
	extractWaitErr := extract.Wait()
	if ctx.Err() != nil {
		return result, fmt.Errorf("physical restore validation timed out after %s", cfg.PhysicalTimeout)
	}
	if downloadWaitErr != nil {
		return result, fmt.Errorf("download physical stream: %v: %s", downloadWaitErr, strings.TrimSpace(downloadErr.String()))
	}
	if extractWaitErr != nil {
		return result, fmt.Errorf("extract physical stream: %v: %s", extractWaitErr, strings.TrimSpace(extractErr.String()))
	}

	if progress != nil {
		progress("preparing physical backup")
	}
	prepareArgs := []string{"--prepare", "--target-dir=" + extractDir}
	prepare := exec.CommandContext(ctx, xtrabackupBin, prepareArgs...)
	if cfg.XtrabackupRunAsUser != "" {
		prepare = exec.CommandContext(ctx, "sudo", append([]string{"-n", "-u", cfg.XtrabackupRunAsUser, xtrabackupBin}, prepareArgs...)...)
	}
	prepare.Dir = workDir
	output, err := prepare.CombinedOutput()
	if ctx.Err() != nil {
		return result, fmt.Errorf("physical restore validation timed out after %s", cfg.PhysicalTimeout)
	}
	if err != nil {
		return result, fmt.Errorf("prepare physical backup: %v: %s", err, strings.TrimSpace(string(output)))
	}
	result.Valid = true
	if progress != nil {
		progress("physical backup prepared successfully")
	}
	return result, nil
}
