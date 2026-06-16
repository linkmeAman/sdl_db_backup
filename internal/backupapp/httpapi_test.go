package backupapp

import (
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

