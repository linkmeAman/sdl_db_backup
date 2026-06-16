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
		RunTimestamp:              1760000000,
		SuccessTimestamp:          1759999900,
		DurationSeconds:           12.345,
		SizeBytes:                 4096,
		Status:                    1,
		UploadSuccess:             1,
		CleanupTimestamp:          1759999800,
		LogicalTotalDatabases:     2,
		LogicalSucceededDatabases: 2,
		LogicalFailedDatabases:    0,
		PhysicalDurationSeconds:   90.5,
	}

	if err := writeBackupMetricsFile(path, snapshot, `job="sdl_db_backup",service="mysql"`); err != nil {
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
		`backup_last_run_timestamp{job="sdl_db_backup",service="mysql"} 1760000000`,
		`backup_last_success_timestamp{job="sdl_db_backup",service="mysql"} 1759999900`,
		`backup_last_duration_seconds{job="sdl_db_backup",service="mysql"} 12.345`,
		`backup_last_size_bytes{job="sdl_db_backup",service="mysql"} 4096`,
		`backup_last_status{job="sdl_db_backup",service="mysql"} 1`,
		`backup_upload_success{job="sdl_db_backup",service="mysql"} 1`,
		`backup_metrics_write_success{job="sdl_db_backup",service="mysql"} 0`,
		`backup_cleanup_success{job="sdl_db_backup",service="mysql"} 0`,
		`backup_last_cleanup_timestamp{job="sdl_db_backup",service="mysql"} 1759999800`,
		`backup_logical_last_attempted{job="sdl_db_backup",service="mysql"} 0`,
		`backup_logical_last_status{job="sdl_db_backup",service="mysql"} 0`,
		`backup_logical_last_total_databases{job="sdl_db_backup",service="mysql"} 2`,
		`backup_logical_last_succeeded_databases{job="sdl_db_backup",service="mysql"} 2`,
		`backup_logical_last_failed_databases{job="sdl_db_backup",service="mysql"} 0`,
		`backup_physical_last_attempted{job="sdl_db_backup",service="mysql"} 0`,
		`backup_physical_last_status{job="sdl_db_backup",service="mysql"} 0`,
		`backup_physical_last_duration_seconds{job="sdl_db_backup",service="mysql"} 90.500`,
		`backup_run_in_progress{job="sdl_db_backup",service="mysql"} 0`,
		`backup_current_run_start_timestamp{job="sdl_db_backup",service="mysql"} 0`,
		`backup_current_run_duration_seconds{job="sdl_db_backup",service="mysql"} 0.000`,
		`backup_metrics_last_update_timestamp{job="sdl_db_backup",service="mysql"} 0`,
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
		`backup_last_success_timestamp{job="sdl_db_backup",service="mysql"} 1700000000`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o640); err != nil {
		t.Fatalf("write existing metrics: %v", err)
	}

	cfg := config{MetricsFile: path}
	record := runRecord{
		Status:    "failed",
		RunFolder: "/tmp/run-1",
		Results: []databaseResult{
			{Name: "db1", SizeBytes: 2048},
		},
		DatabasesTotal:  1,
		DatabasesFailed: 1,
	}
	snapshot := buildFinalBackupMetricsSnapshot(cfg, record, time.Now().Add(-3*time.Second), true, false, true, false)
	if snapshot.SuccessTimestamp != 1700000000 {
		t.Fatalf("expected success timestamp preserved, got %d", snapshot.SuccessTimestamp)
	}
	if snapshot.Status != 0 {
		t.Fatalf("expected failed status metric, got %d", snapshot.Status)
	}
	if snapshot.UploadSuccess != 0 {
		t.Fatalf("expected upload metric 0, got %d", snapshot.UploadSuccess)
	}
	if snapshot.MetricsWriteSuccess != 1 {
		t.Fatalf("expected metrics write success preset to 1, got %d", snapshot.MetricsWriteSuccess)
	}
	if snapshot.CleanupSuccess != 1 {
		t.Fatalf("expected cleanup success 1, got %d", snapshot.CleanupSuccess)
	}
	if snapshot.CleanupTimestamp == 0 {
		t.Fatalf("expected cleanup timestamp to be set")
	}
	if snapshot.SizeBytes != 2048 {
		t.Fatalf("expected artifact size preserved, got %d", snapshot.SizeBytes)
	}
	if snapshot.LogicalAttempted != 1 || snapshot.LogicalStatus != 0 {
		t.Fatalf("expected logical backup attempted/failed, got attempted=%d status=%d", snapshot.LogicalAttempted, snapshot.LogicalStatus)
	}
	if snapshot.LogicalTotalDatabases != 1 || snapshot.LogicalSucceededDatabases != 0 || snapshot.LogicalFailedDatabases != 1 {
		t.Fatalf("unexpected logical database counters: total=%d success=%d failed=%d", snapshot.LogicalTotalDatabases, snapshot.LogicalSucceededDatabases, snapshot.LogicalFailedDatabases)
	}
	if snapshot.PhysicalAttempted != 0 || snapshot.PhysicalStatus != 0 {
		t.Fatalf("expected physical backup skipped metrics, got attempted=%d status=%d", snapshot.PhysicalAttempted, snapshot.PhysicalStatus)
	}
}

func TestBuildInProgressBackupMetricsSnapshotPreservesPreviousFinalMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sdl_db_backup.prom")
	existing := strings.Join([]string{
		`backup_last_run_timestamp{job="sdl_db_backup",service="mysql"} 1700000100`,
		`backup_last_success_timestamp{job="sdl_db_backup",service="mysql"} 1700000000`,
		`backup_last_duration_seconds{job="sdl_db_backup",service="mysql"} 8.500`,
		`backup_last_size_bytes{job="sdl_db_backup",service="mysql"} 1234`,
		`backup_last_status{job="sdl_db_backup",service="mysql"} 1`,
		`backup_upload_success{job="sdl_db_backup",service="mysql"} 1`,
		`backup_metrics_write_success{job="sdl_db_backup",service="mysql"} 1`,
		`backup_cleanup_success{job="sdl_db_backup",service="mysql"} 1`,
		`backup_last_cleanup_timestamp{job="sdl_db_backup",service="mysql"} 1699999999`,
		`backup_logical_last_attempted{job="sdl_db_backup",service="mysql"} 1`,
		`backup_logical_last_status{job="sdl_db_backup",service="mysql"} 1`,
		`backup_logical_last_total_databases{job="sdl_db_backup",service="mysql"} 2`,
		`backup_logical_last_succeeded_databases{job="sdl_db_backup",service="mysql"} 2`,
		`backup_logical_last_failed_databases{job="sdl_db_backup",service="mysql"} 0`,
		`backup_physical_last_attempted{job="sdl_db_backup",service="mysql"} 1`,
		`backup_physical_last_status{job="sdl_db_backup",service="mysql"} 0`,
		`backup_physical_last_duration_seconds{job="sdl_db_backup",service="mysql"} 321.000`,
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
	if snapshot.LogicalAttempted != 1 || snapshot.LogicalStatus != 1 || snapshot.PhysicalAttempted != 1 || snapshot.PhysicalStatus != 0 {
		t.Fatalf("expected previous split metrics preserved, got logical=(%d,%d) physical=(%d,%d)", snapshot.LogicalAttempted, snapshot.LogicalStatus, snapshot.PhysicalAttempted, snapshot.PhysicalStatus)
	}
	if snapshot.MetricsWriteSuccess != 1 || snapshot.CleanupSuccess != 1 {
		t.Fatalf("expected metrics write/cleanup preserved, got write=%d cleanup=%d", snapshot.MetricsWriteSuccess, snapshot.CleanupSuccess)
	}
	if snapshot.CleanupTimestamp != 1699999999 {
		t.Fatalf("expected cleanup timestamp preserved, got %d", snapshot.CleanupTimestamp)
	}
	if snapshot.LogicalTotalDatabases != 2 || snapshot.LogicalSucceededDatabases != 2 || snapshot.LogicalFailedDatabases != 0 {
		t.Fatalf("expected logical counters preserved, got total=%d success=%d failed=%d", snapshot.LogicalTotalDatabases, snapshot.LogicalSucceededDatabases, snapshot.LogicalFailedDatabases)
	}
	if snapshot.PhysicalDurationSeconds != 321 {
		t.Fatalf("expected physical duration preserved, got %.3f", snapshot.PhysicalDurationSeconds)
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

func TestValidateMetricsPathWritableCreatesMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "collector", "sdl_db_backup.prom")
	if err := validateMetricsPathWritable(path); err != nil {
		t.Fatalf("validateMetricsPathWritable returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("expected metrics directory to exist: %v", err)
	}
}

func TestGetObservabilityReportReadsMetricsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sdl_db_backup.prom")
	snapshot := backupMetricsSnapshot{
		RunTimestamp:               1760000000,
		SuccessTimestamp:           1759999900,
		DurationSeconds:            12.345,
		SizeBytes:                  4096,
		Status:                     1,
		UploadSuccess:              1,
		MetricsWriteSuccess:        1,
		CleanupSuccess:             1,
		CleanupTimestamp:           1759999800,
		LogicalAttempted:           1,
		LogicalStatus:              1,
		LogicalTotalDatabases:      3,
		LogicalSucceededDatabases:  3,
		LogicalFailedDatabases:     0,
		PhysicalAttempted:          0,
		PhysicalStatus:             0,
		PhysicalDurationSeconds:    0,
		MetricsLastUpdateTimestamp: 1760000001,
	}
	if err := writeBackupMetricsFile(path, snapshot, `job="sdl_db_backup",service="mysql"` ); err != nil {
		t.Fatalf("writeBackupMetricsFile returned error: %v", err)
	}

	report := GetObservabilityReport(config{MetricsFile: path})
	if !report.MetricsWritable || !report.MetricsFileExists {
		t.Fatalf("expected writable existing metrics file, got %+v", report)
	}
	if report.LastWriteResult != "success" {
		t.Fatalf("expected last write success, got %q", report.LastWriteResult)
	}
	if got := report.Snapshot["backup_cleanup_success"]; got != "1" {
		t.Fatalf("expected cleanup metric 1, got %q", got)
	}
	if got := report.Snapshot["backup_logical_last_total_databases"]; got != "3" {
		t.Fatalf("expected logical total metric 3, got %q", got)
	}
}
