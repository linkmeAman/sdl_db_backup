package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupObservabilityDashboardIsValidJSON(t *testing.T) {
	path := filepath.Join("sdl-db-backup-observability.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dashboard json: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("dashboard json is invalid: %v", err)
	}

	if got := decoded["title"]; got != "SDL DB Backup Observability" {
		t.Fatalf("unexpected dashboard title: %v", got)
	}
}

func TestBackupObservabilityDashboardReferencesCoreMetrics(t *testing.T) {
	path := filepath.Join("sdl-db-backup-observability.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dashboard json: %v", err)
	}
	text := string(data)

	requiredMetrics := []string{
		"backup_run_in_progress",
		"backup_last_status",
		"backup_upload_success",
		"backup_metrics_write_success",
		"backup_cleanup_success",
		"backup_logical_validation_last_status",
		"backup_last_run_timestamp",
		"backup_last_success_timestamp",
		"backup_metrics_last_update_timestamp",
		"backup_current_run_duration_seconds",
		"backup_logical_last_status",
		"backup_logical_last_attempted",
		"backup_logical_last_total_databases",
		"backup_logical_last_succeeded_databases",
		"backup_logical_last_failed_databases",
		"backup_physical_last_status",
		"backup_last_duration_seconds",
		"backup_last_size_bytes",
		"backup_physical_last_duration_seconds",
		"backup_last_cleanup_timestamp",
		"backup_adaptive_logical_parallel",
		"backup_adaptive_xtrabackup_parallel",
		"backup_adaptive_xbcloud_parallel",
		"backup_adaptive_load_per_cpu",
		"backup_physical_retry_count",
		"backup_physical_rate_limit_retry_count",
		"backup_current_run_start_timestamp",
	}

	for _, metric := range requiredMetrics {
		if !strings.Contains(text, metric) {
			t.Fatalf("expected dashboard to reference metric %q", metric)
		}
	}
}

func TestBackupLogsDashboardIsValidJSON(t *testing.T) {
	path := filepath.Join("sdl-db-backup-logs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read logs dashboard json: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("logs dashboard json is invalid: %v", err)
	}

	if got := decoded["title"]; got != "SDL DB Backup Logs" {
		t.Fatalf("unexpected logs dashboard title: %v", got)
	}
}

func TestBackupLogsDashboardReferencesLokiLogQuery(t *testing.T) {
	path := filepath.Join("sdl-db-backup-logs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read logs dashboard json: %v", err)
	}
	text := string(data)

	for _, want := range []string{
		"\"type\": \"logs\"",
		"job=\\\"$job\\\"",
		"service=\\\"$service\\\"",
		"env=\\\"$env\\\"",
		"log_kind=\\\"run\\\"",
		"Backup Lifecycle Events",
		"Full Per-Run Backup Log",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected logs dashboard to contain %q", want)
		}
	}
}
