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

type backupMetricsSnapshot struct {
	RunTimestamp     time.Time
	SuccessTimestamp int64
	DurationSeconds  float64
	SizeBytes        int64
	Status           int
	UploadSuccess    int
}

func buildBackupMetricsSnapshot(cfg config, record runRecord, startedAt time.Time, uploadRequired bool, uploadSucceeded bool) backupMetricsSnapshot {
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
		RunTimestamp:     runEndedAt,
		SuccessTimestamp: successTimestamp,
		DurationSeconds:  time.Since(startedAt).Seconds(),
		SizeBytes:        totalLogicalArtifactSize(record.Results),
		Status:           status,
		UploadSuccess:    uploadSuccess,
	}
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
			Value: strconv.FormatInt(snapshot.RunTimestamp.Unix(), 10),
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

func emitBackupMetrics(cfg config, record runRecord, startedAt time.Time, uploadRequired bool, uploadSucceeded bool) {
	snapshot := buildBackupMetricsSnapshot(cfg, record, startedAt, uploadRequired, uploadSucceeded)
	if err := writeBackupMetricsFile(cfg.MetricsFile, snapshot); err != nil {
		log.Printf("metrics write failed path=%s err=%v", cfg.MetricsFile, err)
	}
}
