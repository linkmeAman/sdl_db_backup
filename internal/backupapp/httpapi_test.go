package backupapp

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigReadsAPIAndSystemdSettings(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + filepath.Join(dir, "logs"),
		"BACKUP_SYSTEMD_SERVICE_NAME=custom-backup.service",
		"BACKUP_SYSTEMD_TIMER_NAME=custom-backup.timer",
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_LISTEN_ADDR=127.0.0.1:9090",
		"BACKUP_API_BASE_PATH=/internal/api",
		"BACKUP_API_AUTH_ENABLED=true",
		"BACKUP_API_BEARER_TOKEN=secret-token",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	cfg, err := LoadConfig(envPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.ServiceUnitName != "custom-backup.service" || cfg.TimerUnitName != "custom-backup.timer" {
		t.Fatalf("unexpected unit names: %+v", cfg)
	}
	if !cfg.APIEnabled || !cfg.APIAuthEnabled || cfg.APIListenAddr != "127.0.0.1:9090" || cfg.APIBasePath != "/internal/api" || cfg.APIBearerToken != "secret-token" {
		t.Fatalf("unexpected API config: %+v", cfg)
	}
}

func TestRenderUserSystemdUnitsUsesConfigNames(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := "BACKUP_SYSTEMD_SERVICE_NAME=custom-backup.service\nBACKUP_SYSTEMD_TIMER_NAME=custom-backup.timer\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	rendered, err := RenderUserSystemdUnits(envPath)
	if err != nil {
		t.Fatalf("RenderUserSystemdUnits returned error: %v", err)
	}
	if !strings.Contains(rendered.TimerUnit, "Unit=custom-backup.service") {
		t.Fatalf("expected rendered timer to use custom service name: %s", rendered.TimerUnit)
	}
	if !strings.Contains(rendered.ServiceUnit, "Environment=BACKUP_ENV_FILE="+envPath) {
		t.Fatalf("expected rendered service to use env path: %s", rendered.ServiceUnit)
	}
}

func TestAPIReturnsMaskedConfig(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"DB_PASS=super-secret",
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + filepath.Join(dir, "logs"),
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatalf("expected secret to be masked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "********") {
		t.Fatalf("expected masked marker: %s", rec.Body.String())
	}
}

func TestAPIAuthEnabledRejectsMissingToken(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + filepath.Join(dir, "logs"),
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=true",
		"BACKUP_API_BEARER_TOKEN=top-secret",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/storage", nil)
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPIRestoreStatusReportsCapabilities(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + filepath.Join(dir, "logs"),
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
		"RESTORE_TEST_ENABLED=true",
		"BACKUP_EXACT_ROW_COUNTS=true",
		"BACKUP_SAMPLE_DATA_CHECKS=true",
		"BACKUP_SAMPLE_DATA_ROWS=25",
		"RESTORE_TEST_HOST=127.0.0.1",
		"RESTORE_TEST_PORT=3307",
		"RESTORE_TEST_USER=restore_user",
		"RESTORE_TEST_PASS=secret",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/restore", nil)
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"logical_validation_available":true`) {
		t.Fatalf("expected logical validation flag in response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"exact_row_counts":true`) || !strings.Contains(rec.Body.String(), `"sample_data_checks":true`) || !strings.Contains(rec.Body.String(), `"sample_data_rows":25`) {
		t.Fatalf("expected restore verification depth in response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `/api/v1/restore/test`) {
		t.Fatalf("expected restore test endpoint in response: %s", rec.Body.String())
	}
}

func TestAPIRestoreValidateReturnsRunValidation(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runDir := filepath.Join(dir, "run-1")
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := writeValidGzipSQL(filepath.Join(runDir, "app.sql.gz")); err != nil {
		t.Fatalf("write gzip file: %v", err)
	}
	runLog := `{"timestamp":"2026-06-22T10:00:00Z","run_id":"run-1","status":"success","run_folder":"` + runDir + `","log_file":"` + filepath.Join(logDir, "run-1.log") + `","duration":"2s","databases_total":1,"databases_succeeded":1,"databases_failed":0,"results":[{"name":"app","status":"success","attempts":1,"duration":"1s","output_path":"` + filepath.Join(runDir, "app.sql.gz") + `","size_bytes":123}]}
`
	if err := os.WriteFile(filepath.Join(logDir, "backup-runs.jsonl"), []byte(runLog), 0o640); err != nil {
		t.Fatalf("write run log: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore/validate", strings.NewReader(`{"run_id":"run-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"valid":true`) || !strings.Contains(rec.Body.String(), `"database":"app"`) {
		t.Fatalf("expected validation result in response: %s", rec.Body.String())
	}
}

func TestAPIBackupsHealthIncludesLatestRunFinalOutcome(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
		"BACKUP_METRICS_FILE=" + filepath.Join(dir, "collector", "sdl_db_backup.prom"),
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	runLog := `{"timestamp":"2026-06-22T10:00:00Z","run_id":"run-1","status":"failed","duration":"2s","databases_total":0,"databases_succeeded":0,"databases_failed":0,"logical_upload_error":"s3 unavailable"}
`
	if err := os.WriteFile(filepath.Join(logDir, "backup-runs.jsonl"), []byte(runLog), 0o640); err != nil {
		t.Fatalf("write run log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/health", nil)
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"FinalOutcome":"logical upload failed: s3 unavailable"`) {
		t.Fatalf("expected health API to include latest run final outcome, got: %s", rec.Body.String())
	}
}

func TestAPIBackupsRunsIncludesFinalOutcome(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	runLog := `{"timestamp":"2026-06-22T10:00:00Z","run_id":"run-1","status":"partial","duration":"2s","databases_total":4,"databases_succeeded":3,"databases_failed":1}
`
	if err := os.WriteFile(filepath.Join(logDir, "backup-runs.jsonl"), []byte(runLog), 0o640); err != nil {
		t.Fatalf("write run log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/runs", nil)
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"final_outcome":"1 of 4 database backups failed"`) {
		t.Fatalf("expected runs API to include final outcome, got: %s", rec.Body.String())
	}
}

func TestAPIBackupRunByIDIncludesFinalOutcome(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	runLog := `{"timestamp":"2026-06-22T10:00:00Z","run_id":"run-9","status":"failed","duration":"2s","databases_total":0,"databases_succeeded":0,"databases_failed":0,"logical_upload_error":"s3 unavailable"}
`
	if err := os.WriteFile(filepath.Join(logDir, "backup-runs.jsonl"), []byte(runLog), 0o640); err != nil {
		t.Fatalf("write run log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/backups/runs/run-9", nil)
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"final_outcome":"logical upload failed: s3 unavailable"`) {
		t.Fatalf("expected single run API to include final outcome, got: %s", rec.Body.String())
	}
}

func TestAPIRestoreTestDisabledReturnsBadRequest(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + filepath.Join(dir, "logs"),
		"BACKUP_API_ENABLED=true",
		"BACKUP_API_AUTH_ENABLED=false",
		"RESTORE_TEST_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restore/test", strings.NewReader(`{"run_id":"run-1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	(&apiServer{envPath: envPath}).routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "restore_test_disabled") {
		t.Fatalf("expected restore_test_disabled error: %s", rec.Body.String())
	}
}

func writeValidGzipSQL(path string) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte("CREATE TABLE test (id int);\n-- Dump completed\n")); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o640)
}
