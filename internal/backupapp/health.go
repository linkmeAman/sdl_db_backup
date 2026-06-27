package backupapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
			Timestamp:                record.Timestamp,
			RunID:                    record.RunID,
			Status:                   record.Status,
			RunFolder:                record.RunFolder,
			LogFile:                  record.LogFile,
			FinalOutcome:             DeriveFinalOutcome(record.Status, record.DatabasesTotal, record.DatabasesFailed, record.FailureReason, record.CleanupError, record.LogicalUploadError),
			FailureReason:            record.FailureReason,
			CleanupError:             record.CleanupError,
			Duration:                 record.Duration,
			DatabasesTotal:           record.DatabasesTotal,
			DatabasesSucceeded:       record.DatabasesSucceeded,
			DatabasesFailed:          record.DatabasesFailed,
			LogicalUploadRun:         record.LogicalUploadRun,
			LogicalUploadStatus:      record.LogicalUploadStatus,
			LogicalUploadNote:        record.LogicalUploadNote,
			LogicalUploadError:       record.LogicalUploadError,
			AdaptiveLoadPerCPU:       record.AdaptiveLoadPerCPU,
			AdaptiveLogicalParallel:  record.AdaptiveLogicalParallel,
			AdaptivePhysicalParallel: record.AdaptivePhysicalParallel,
			AdaptiveXbcloudParallel:  record.AdaptiveXbcloudParallel,
			AdaptiveTuningReason:     record.AdaptiveTuningReason,
			ValidationCheckedAt:      record.ValidationCheckedAt,
			ValidationMode:           record.ValidationMode,
			ValidationStatus:         record.ValidationStatus,
			ValidationError:          record.ValidationError,
			OSUser:                   record.OSUser,
			ExecutionSource:          record.ExecutionSource,
			Hostname:                 record.Hostname,
			PID:                      record.PID,
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

type ownershipDriftFinding struct {
	RelativePath string
	UID          uint32
	GID          uint32
	Mode         os.FileMode
}

func scanOwnershipDrift(path string, expectedUID uint32, maxDepth int, maxFindings int, lstat func(string) (os.FileInfo, error)) ([]ownershipDriftFinding, error) {
	findings := []ownershipDriftFinding{}
	walkErr := filepath.WalkDir(path, func(current string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == path {
			return nil
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		depth := strings.Count(rel, string(os.PathSeparator))
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if stat.Uid == expectedUID {
			return nil
		}
		findings = append(findings, ownershipDriftFinding{
			RelativePath: rel,
			UID:          stat.Uid,
			GID:          stat.Gid,
			Mode:         info.Mode().Perm(),
		})
		if len(findings) >= maxFindings {
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(walkErr, fs.SkipAll) {
		return findings, nil
	}
	if walkErr != nil {
		return nil, walkErr
	}
	return findings, nil
}

func formatOwnershipDrift(findings []ownershipDriftFinding) string {
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		parts = append(parts, fmt.Sprintf("%s(uid=%d gid=%d mode=%#o)", finding.RelativePath, finding.UID, finding.GID, finding.Mode.Perm()))
	}
	return strings.Join(parts, ", ")
}

func checkDirectoryHealth(name, path string, detectDrift bool) HealthCheck {
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
	if detectDrift {
		findings, err := scanOwnershipDrift(path, uint32(os.Geteuid()), 2, 5, os.Lstat)
		if err != nil {
			check.Status = "warn"
			check.Message = fmt.Sprintf("%s writable but ownership drift scan failed: %v", path, err)
			return check
		}
		if len(findings) > 0 {
			check.Status = "warn"
			check.Message = fmt.Sprintf("%s writable but ownership drift detected relative to uid=%d: %s", path, os.Geteuid(), formatOwnershipDrift(findings))
		}
	}
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
			checkDirectoryHealth("backup_dir", cfg.BackupDir, true),
			checkDirectoryHealth("log_dir", cfg.LogDir, true),
			checkDirectoryHealth("metrics_dir", filepath.Dir(resolvedMetricsFilePath(cfg.MetricsFile)), false),
		},
		Observability: GetObservabilityReport(cfg),
		RestoreVerification: RestoreVerificationProfile{
			RestoreTestEnabled: cfg.RestoreTestEnabled,
			ExactRowCounts:     cfg.ExactRowCounts,
			SampleDataChecks:   cfg.SampleDataChecks,
			SampleDataRows:     cfg.SampleDataRows,
		},
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
