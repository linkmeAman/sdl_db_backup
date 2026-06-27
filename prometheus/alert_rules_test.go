package prometheus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlertRulesFileExistsAndLooksStructured(t *testing.T) {
	path := filepath.Join("sdl-db-backup-alerts.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read alert rule file: %v", err)
	}
	text := string(data)

	for _, want := range []string{
		"groups:",
		"- name: sdl-db-backup",
		"rules:",
		"alert: SDLDBBackupFailedOverall",
		"alert: SDLDBLogicalBackupFailed",
		"alert: SDLDBPhysicalBackupFailed",
		"alert: SDLDBBackupUploadFailed",
		"alert: SDLDBBackupMetricsWriteFailed",
		"alert: SDLDBBackupCleanupFailed",
		"alert: SDLDBLogicalValidationFailed",
		"alert: SDLDBBackupStuckRunning",
		"alert: SDLDBBackupMetricsStaleDuringRun",
		"alert: SDLDBNoSuccessfulBackupRecently",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected alert rule file to contain %q", want)
		}
	}
}

func TestAlertRulesFileReferencesCoreMetrics(t *testing.T) {
	path := filepath.Join("sdl-db-backup-alerts.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read alert rule file: %v", err)
	}
	text := string(data)

	requiredMetrics := []string{
		"backup_last_status",
		"backup_logical_last_attempted",
		"backup_logical_last_status",
		"backup_physical_last_attempted",
		"backup_physical_last_status",
		"backup_upload_success",
		"backup_metrics_write_success",
		"backup_cleanup_success",
		"backup_logical_validation_last_status",
		"backup_run_in_progress",
		"backup_current_run_duration_seconds",
		"backup_metrics_last_update_timestamp",
		"backup_last_success_timestamp",
	}

	for _, metric := range requiredMetrics {
		if !strings.Contains(text, metric) {
			t.Fatalf("expected alert rule file to reference metric %q", metric)
		}
	}
}
