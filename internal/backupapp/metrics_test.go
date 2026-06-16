package backupapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigWithOverridesReadsMetricsFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("BACKUP_METRICS_FILE="+filepath.Join(dir, "custom.prom")+"\n"), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		t.Fatalf("loadConfigWithOverrides returned error: %v", err)
	}
	if want := filepath.Join(dir, "custom.prom"); cfg.MetricsFile != want {
		t.Fatalf("expected metrics file %q, got %q", want, cfg.MetricsFile)
	}
}

func TestWriteBackupMetricsFileWritesPrometheusTextfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sdl_db_backup.prom")
	snapshot := backupMetricsSnapshot{
		RunTimestamp:     1760000000,
		SuccessTimestamp: 1759999900,
		DurationSeconds:  12.345,
		SizeBytes:        4096,
		Status:           1,
		UploadSuccess:    1,
	}

	if err := writeBackupMetricsFile(path, snapshot); err != nil {
		t.Fatalf("writeBackupMetricsFile returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat metrics file: %v", err)
	}
	text := string(data)
	required := []string{
		"# HELP backup_last_run_timestamp Unix timestamp when the most recent backup run ended.",
		"# TYPE backup_last_run_timestamp gauge",
		"backup_last_run_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1760000000",
		"backup_last_success_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1759999900",
		"backup_last_duration_seconds{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 12.345",
		"backup_last_size_bytes{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 4096",
		"backup_last_status{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1",
		"backup_upload_success{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1",
		"backup_run_in_progress{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 0",
		"backup_current_run_start_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 0",
		"backup_current_run_duration_seconds{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 0.000",
		"backup_metrics_last_update_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 0",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("expected metrics file to contain %q\nfull text:\n%s", want, text)
		}
	}
	if got := info.Mode().Perm(); got != backupMetricsFileMode {
		t.Fatalf("expected metrics file mode %o, got %o", backupMetricsFileMode, got)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected temp files to be cleaned up, found %v", matches)
	}
}

func TestBuildBackupMetricsSnapshotPreservesPreviousSuccessTimestampOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sdl_db_backup.prom")
	existing := strings.Join([]string{
		"# HELP backup_last_success_timestamp Unix timestamp when the most recent fully successful backup run ended.",
		"# TYPE backup_last_success_timestamp gauge",
		"backup_last_success_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1700000000",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o640); err != nil {
		t.Fatalf("write existing metrics: %v", err)
	}

	cfg := config{MetricsFile: path}
	record := runRecord{
		Status: "failed",
		Results: []databaseResult{
			{Name: "db1", SizeBytes: 2048},
		},
	}
	snapshot := buildFinalBackupMetricsSnapshot(cfg, record, time.Now().Add(-3*time.Second), true, false)
	if snapshot.SuccessTimestamp != 1700000000 {
		t.Fatalf("expected success timestamp preserved, got %d", snapshot.SuccessTimestamp)
	}
	if snapshot.Status != 0 {
		t.Fatalf("expected failed status metric, got %d", snapshot.Status)
	}
	if snapshot.UploadSuccess != 0 {
		t.Fatalf("expected upload metric 0, got %d", snapshot.UploadSuccess)
	}
	if snapshot.SizeBytes != 2048 {
		t.Fatalf("expected artifact size preserved, got %d", snapshot.SizeBytes)
	}
}

func TestBuildInProgressBackupMetricsSnapshotPreservesPreviousFinalMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sdl_db_backup.prom")
	existing := strings.Join([]string{
		"backup_last_run_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1700000100",
		"backup_last_success_timestamp{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1700000000",
		"backup_last_duration_seconds{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 8.500",
		"backup_last_size_bytes{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1234",
		"backup_last_status{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1",
		"backup_upload_success{job=\"sdl_db_backup\",service=\"mysql\",env=\"pilot\"} 1",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o640); err != nil {
		t.Fatalf("write existing metrics: %v", err)
	}

	cfg := config{MetricsFile: path}
	startedAt := time.Now().Add(-10 * time.Second)
	snapshot := buildInProgressBackupMetricsSnapshot(cfg, startedAt)
	if snapshot.RunTimestamp != 1700000100 {
		t.Fatalf("expected previous run timestamp preserved, got %d", snapshot.RunTimestamp)
	}
	if snapshot.SuccessTimestamp != 1700000000 {
		t.Fatalf("expected previous success timestamp preserved, got %d", snapshot.SuccessTimestamp)
	}
	if snapshot.RunInProgress != 1 {
		t.Fatalf("expected in-progress metric 1, got %d", snapshot.RunInProgress)
	}
	if snapshot.CurrentRunStartTimestamp != startedAt.UTC().Unix() {
		t.Fatalf("expected current start timestamp %d, got %d", startedAt.UTC().Unix(), snapshot.CurrentRunStartTimestamp)
	}
	if snapshot.CurrentRunDurationSeconds < 9 || snapshot.CurrentRunDurationSeconds > 11 {
		t.Fatalf("expected current duration near 10s, got %.3f", snapshot.CurrentRunDurationSeconds)
	}
}
