package backupapp

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultMetricsFilePath = "/var/lib/node_exporter/textfile_collector/sdl_db_backup.prom"
const backupMetricsFileMode = 0o644
const backupMetricsUpdateInterval = 5 * time.Second

type backupMetricsSnapshot struct {
	RunTimestamp               int64
	SuccessTimestamp           int64
	DurationSeconds            float64
	SizeBytes                  int64
	Status                     int
	UploadSuccess              int
	RunInProgress              int
	CurrentRunStartTimestamp   int64
	CurrentRunDurationSeconds  float64
	MetricsLastUpdateTimestamp int64
}

func buildFinalBackupMetricsSnapshot(cfg config, record runRecord, startedAt time.Time, uploadRequired bool, uploadSucceeded bool) backupMetricsSnapshot {
	runEndedAt := time.Now().UTC()
	successTimestamp := readLastSuccessTimestamp(cfg.MetricsFile)
	status := 0
	if record.Status == "success" && (!uploadRequired || uploadSucceeded) {
		status = 1
		successTimestamp = runEndedAt.Unix()
	}

	uploadSuccess := 0
	if uploadRequired && uploadSucceeded {
		uploadSuccess = 1
	}

	return backupMetricsSnapshot{
		RunTimestamp:               runEndedAt.Unix(),
		SuccessTimestamp:           successTimestamp,
		DurationSeconds:            time.Since(startedAt).Seconds(),
		SizeBytes:                  totalLogicalArtifactSize(record.Results),
		Status:                     status,
		UploadSuccess:              uploadSuccess,
		RunInProgress:              0,
		CurrentRunStartTimestamp:   0,
		CurrentRunDurationSeconds:  0,
		MetricsLastUpdateTimestamp: runEndedAt.Unix(),
	}
}

func buildInProgressBackupMetricsSnapshot(cfg config, startedAt time.Time) backupMetricsSnapshot {
	now := time.Now().UTC()
	previous := readExistingBackupMetricsSnapshot(cfg.MetricsFile)
	previous.RunInProgress = 1
	previous.CurrentRunStartTimestamp = startedAt.UTC().Unix()
	previous.CurrentRunDurationSeconds = time.Since(startedAt).Seconds()
	previous.MetricsLastUpdateTimestamp = now.Unix()
	return previous
}

func totalLogicalArtifactSize(results []databaseResult) int64 {
	var total int64
	for _, result := range results {
		if result.SizeBytes > 0 {
			total += result.SizeBytes
		}
	}
	return total
}

func readLastSuccessTimestamp(path string) int64 {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "backup_last_success_timestamp") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err == nil {
			return value
		}
	}
	return 0
}

func readExistingBackupMetricsSnapshot(path string) backupMetricsSnapshot {
	file, err := os.Open(path)
	if err != nil {
		return backupMetricsSnapshot{}
	}
	defer file.Close()

	snapshot := backupMetricsSnapshot{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		if brace := strings.IndexByte(name, '{'); brace >= 0 {
			name = name[:brace]
		}
		value := fields[len(fields)-1]
		switch name {
		case "backup_last_run_timestamp":
			snapshot.RunTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_last_success_timestamp":
			snapshot.SuccessTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_last_duration_seconds":
			snapshot.DurationSeconds, _ = strconv.ParseFloat(value, 64)
		case "backup_last_size_bytes":
			snapshot.SizeBytes, _ = strconv.ParseInt(value, 10, 64)
		case "backup_last_status":
			snapshot.Status, _ = strconv.Atoi(value)
		case "backup_upload_success":
			snapshot.UploadSuccess, _ = strconv.Atoi(value)
		case "backup_run_in_progress":
			snapshot.RunInProgress, _ = strconv.Atoi(value)
		case "backup_current_run_start_timestamp":
			snapshot.CurrentRunStartTimestamp, _ = strconv.ParseInt(value, 10, 64)
		case "backup_current_run_duration_seconds":
			snapshot.CurrentRunDurationSeconds, _ = strconv.ParseFloat(value, 64)
		case "backup_metrics_last_update_timestamp":
			snapshot.MetricsLastUpdateTimestamp, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	return snapshot
}

func writeBackupMetricsFile(path string, snapshot backupMetricsSnapshot) error {
	if strings.TrimSpace(path) == "" {
		path = defaultMetricsFilePath
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create metrics directory %s: %w", dir, err)
	}

	var body bytes.Buffer
	for _, metric := range []struct {
		Name  string
		Help  string
		Value string
	}{
		{
			Name:  "backup_last_run_timestamp",
			Help:  "Unix timestamp when the most recent backup run ended.",
			Value: strconv.FormatInt(snapshot.RunTimestamp, 10),
		},
		{
			Name:  "backup_last_success_timestamp",
			Help:  "Unix timestamp when the most recent fully successful backup run ended.",
			Value: strconv.FormatInt(snapshot.SuccessTimestamp, 10),
		},
		{
			Name:  "backup_last_duration_seconds",
			Help:  "Total runtime of the most recent backup job in seconds.",
			Value: strconv.FormatFloat(snapshot.DurationSeconds, 'f', 3, 64),
		},
		{
			Name:  "backup_last_size_bytes",
			Help:  "Size in bytes of the backup artifact produced by the most recent run.",
			Value: strconv.FormatInt(snapshot.SizeBytes, 10),
		},
		{
			Name:  "backup_last_status",
			Help:  "Status of the most recent backup run: 1 for full success, 0 for failure.",
			Value: strconv.Itoa(snapshot.Status),
		},
		{
			Name:  "backup_upload_success",
			Help:  "Upload result of the most recent backup run: 1 for success, 0 for failure or not reached.",
			Value: strconv.Itoa(snapshot.UploadSuccess),
		},
		{
			Name:  "backup_run_in_progress",
			Help:  "Whether a backup run is currently in progress: 1 for running, 0 for idle.",
			Value: strconv.Itoa(snapshot.RunInProgress),
		},
		{
			Name:  "backup_current_run_start_timestamp",
			Help:  "Unix timestamp when the current in-progress backup run started, or 0 when idle.",
			Value: strconv.FormatInt(snapshot.CurrentRunStartTimestamp, 10),
		},
		{
			Name:  "backup_current_run_duration_seconds",
			Help:  "Elapsed seconds for the current in-progress backup run, or 0 when idle.",
			Value: strconv.FormatFloat(snapshot.CurrentRunDurationSeconds, 'f', 3, 64),
		},
		{
			Name:  "backup_metrics_last_update_timestamp",
			Help:  "Unix timestamp when the backup metrics file was last refreshed.",
			Value: strconv.FormatInt(snapshot.MetricsLastUpdateTimestamp, 10),
		},
	} {
		fmt.Fprintf(&body, "# HELP %s %s\n", metric.Name, metric.Help)
		fmt.Fprintf(&body, "# TYPE %s gauge\n", metric.Name)
		fmt.Fprintf(&body, "%s{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} %s\n", metric.Name, metric.Value)
	}

	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp metrics file in %s: %w", dir, err)
	}
	tempPath := tempFile.Name()
	cleanupTemp := func() {
		_ = os.Remove(tempPath)
	}

	if _, err := tempFile.Write(body.Bytes()); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return fmt.Errorf("write temp metrics file %s: %w", tempPath, err)
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return fmt.Errorf("sync temp metrics file %s: %w", tempPath, err)
	}
	if err := tempFile.Chmod(backupMetricsFileMode); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return fmt.Errorf("chmod temp metrics file %s: %w", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		cleanupTemp()
		return fmt.Errorf("close temp metrics file %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanupTemp()
		return fmt.Errorf("rename temp metrics file to %s: %w", path, err)
	}
	if err := os.Chmod(path, backupMetricsFileMode); err != nil {
		return fmt.Errorf("chmod final metrics file %s: %w", path, err)
	}
	return nil
}

func emitFinalBackupMetrics(cfg config, record runRecord, startedAt time.Time, uploadRequired bool, uploadSucceeded bool) {
	snapshot := buildFinalBackupMetricsSnapshot(cfg, record, startedAt, uploadRequired, uploadSucceeded)
	if err := writeBackupMetricsFile(cfg.MetricsFile, snapshot); err != nil {
		log.Printf("metrics write failed path=%s err=%v", cfg.MetricsFile, err)
		return
	}
	log.Printf(
		"metrics write succeeded path=%s status=%d upload_success=%d run_timestamp=%d success_timestamp=%d size_bytes=%d duration_seconds=%.3f",
		cfg.MetricsFile,
		snapshot.Status,
		snapshot.UploadSuccess,
		snapshot.RunTimestamp,
		snapshot.SuccessTimestamp,
		snapshot.SizeBytes,
		snapshot.DurationSeconds,
	)
}

func emitInProgressBackupMetrics(cfg config, startedAt time.Time) {
	snapshot := buildInProgressBackupMetricsSnapshot(cfg, startedAt)
	if err := writeBackupMetricsFile(cfg.MetricsFile, snapshot); err != nil {
		log.Printf("metrics write failed path=%s err=%v", cfg.MetricsFile, err)
		return
	}
	log.Printf(
		"metrics refresh succeeded path=%s in_progress=%d current_run_start=%d current_duration_seconds=%.3f last_update=%d",
		cfg.MetricsFile,
		snapshot.RunInProgress,
		snapshot.CurrentRunStartTimestamp,
		snapshot.CurrentRunDurationSeconds,
		snapshot.MetricsLastUpdateTimestamp,
	)
}

func startRealtimeBackupMetricsEmitter(cfg config, startedAt time.Time) func() {
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		emitInProgressBackupMetrics(cfg, startedAt)
		ticker := time.NewTicker(backupMetricsUpdateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				emitInProgressBackupMetrics(cfg, startedAt)
			case <-stopCh:
				return
			}
		}
	}()

	return func() {
		close(stopCh)
		<-doneCh
	}
}
