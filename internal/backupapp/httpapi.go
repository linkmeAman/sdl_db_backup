package backupapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type apiEnvelope struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error *apiError   `json:"error,omitempty"`
}

type apiError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

type apiServer struct {
	envPath string
}

func RunAPIServer(ctx context.Context, envPath string) error {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return err
	}
	if !cfg.APIEnabled {
		return errors.New("BACKUP_API_ENABLED=false; enable the API explicitly before starting the API server")
	}
	srv := &http.Server{
		Addr:    cfg.APIListenAddr,
		Handler: (&apiServer{envPath: envPath}).routes(),
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *apiServer) routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg, err := loadConfigWithOverrides(s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "config_load_failed", err.Error(), nil)
			return
		}
		if !authorizeAPIRequest(cfg, r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token", nil)
			return
		}
		basePath := normalizeAPIBasePath(cfg.APIBasePath)
		if r.URL.Path == "/" || r.URL.Path == "" {
			writeAPIData(w, http.StatusOK, map[string]interface{}{
				"name":      "sdl-db-backup-api",
				"base_path": basePath,
				"auth": map[string]bool{
					"enabled": cfg.APIAuthEnabled,
				},
			})
			return
		}
		if !strings.HasPrefix(r.URL.Path, basePath) {
			writeAPIError(w, http.StatusNotFound, "not_found", "route not found", nil)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, basePath)
		path = strings.TrimPrefix(path, "/")
		s.routePath(w, r, cfg, path)
	})
}

func (s *apiServer) routePath(w http.ResponseWriter, r *http.Request, cfg config, path string) {
	switch {
	case path == "backups/health" && r.Method == http.MethodGet:
		report, err := GetHealthReport(r.Context(), s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "health_failed", err.Error(), nil)
			return
		}
		profile, _ := GetRuntimeProfile(s.envPath)
		writeAPIData(w, http.StatusOK, map[string]interface{}{"health": report, "runtime": profile})
	case path == "backups/runs" && r.Method == http.MethodGet:
		runs, err := ReadRunHistory(cfg.RunLogPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "history_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, runs)
	case path == "backups/runs" && r.Method == http.MethodPost:
		var opts ManualRunOptions
		if err := decodeJSON(r, &opts); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		if opts.Mode == "" {
			opts.Mode = ManualRunBoth
		}
		if opts.UploadMode == "" {
			opts.UploadMode = ManualUploadNormal
		}
		result, err := ManualRunWithSource(r.Context(), s.envPath, opts, "api", RunSinks{Console: os.Stdout})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "backup_run_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, result)
	case strings.HasPrefix(path, "backups/runs/") && r.Method == http.MethodGet:
		runID := strings.TrimPrefix(path, "backups/runs/")
		run, err := ReadRunByID(cfg.RunLogPath, runID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "run_read_failed", err.Error(), nil)
			return
		}
		if run == nil {
			writeAPIError(w, http.StatusNotFound, "run_not_found", "run not found", nil)
			return
		}
		writeAPIData(w, http.StatusOK, run)
	case path == "config" && r.Method == http.MethodGet:
		base, err := LoadConfig(s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "config_read_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]interface{}{"env_path": ResolveEnvFilePath(s.envPath), "config": MaskedConfigView(base)})
	case path == "config/effective" && r.Method == http.MethodGet:
		effective, err := LoadEffectiveConfig(s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "config_effective_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]interface{}{"env_path": ResolveEnvFilePath(s.envPath), "config": MaskedConfigView(effective)})
	case path == "config" && r.Method == http.MethodPut:
		updated, err := s.updateConfigFromRequest(r)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "config_update_invalid", err.Error(), nil)
			return
		}
		if err := SaveConfig(s.envPath, updated); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "config_save_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]interface{}{"saved": true, "config": MaskedConfigView(updated)})
	case path == "schedules/temporary-overrides" && r.Method == http.MethodGet:
		overrides, active, err := LoadTemporaryOverrides(s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "temporary_override_read_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]interface{}{"active": active, "overrides": overrides})
	case path == "schedules/temporary-overrides" && r.Method == http.MethodPut:
		var overrides TemporaryOverrides
		if err := decodeJSON(r, &overrides); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		if err := SaveTemporaryOverrides(s.envPath, overrides); err != nil {
			writeAPIError(w, http.StatusBadRequest, "temporary_override_invalid", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]bool{"saved": true})
	case path == "schedules/temporary-overrides" && r.Method == http.MethodDelete:
		if err := ClearTemporaryOverrides(s.envPath); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "temporary_override_clear_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]bool{"cleared": true})
	case path == "systemd" && r.Method == http.MethodGet:
		status, err := GetSystemdStatus(r.Context(), s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "systemd_status_failed", err.Error(), nil)
			return
		}
		rendered, _ := RenderUserSystemdUnits(s.envPath)
		writeAPIData(w, http.StatusOK, map[string]interface{}{"status": status, "rendered_units": rendered})
	case strings.HasPrefix(path, "systemd/actions/") && r.Method == http.MethodPost:
		actionName := strings.TrimPrefix(path, "systemd/actions/")
		action, ok := parseSystemdAction(actionName)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "invalid_systemd_action", "unsupported systemd action", nil)
			return
		}
		if err := RunSystemdAction(r.Context(), s.envPath, action); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "systemd_action_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]string{"action": actionName, "status": "ok"})
	case path == "logs/daily" && r.Method == http.MethodGet:
		targetDate := strings.TrimSpace(r.URL.Query().Get("date"))
		if targetDate == "" {
			targetDate = time.Now().Format("2006-01-02")
		}
		lines, err := ReadLogLines(filepath.Join(cfg.LogDir, targetDate+".log"))
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "daily_log_read_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]interface{}{"date": targetDate, "lines": lines})
	case path == "logs/runs" && r.Method == http.MethodGet:
		lines, err := ReadLogLines(cfg.RunLogPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "run_log_read_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, map[string]interface{}{"path": cfg.RunLogPath, "lines": lines})
	case path == "storage" && r.Method == http.MethodGet:
		storage, err := GetStorageSummary(s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "storage_read_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, storage)
	case path == "runtime" && r.Method == http.MethodGet:
		profile, err := GetRuntimeProfile(s.envPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "runtime_read_failed", err.Error(), nil)
			return
		}
		writeAPIData(w, http.StatusOK, profile)
	case path == "restore" && r.Method == http.MethodGet:
		writeAPIError(w, http.StatusNotImplemented, "restore_not_implemented", "restore operations are not implemented in this version", nil)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "route not found", nil)
	}
}

func (s *apiServer) updateConfigFromRequest(r *http.Request) (Config, error) {
	current, err := LoadConfig(s.envPath)
	if err != nil {
		return Config{}, err
	}
	values := envMapFromConfig(current)
	var payload map[string]string
	if err := decodeJSON(r, &payload); err != nil {
		return Config{}, err
	}
	for key, value := range payload {
		if !slices.Contains(managedEnvKeys(), key) {
			return Config{}, fmt.Errorf("unsupported config key %s", key)
		}
		values[key] = value
	}
	return loadConfigFromValues(values), nil
}

func authorizeAPIRequest(cfg config, r *http.Request) bool {
	if !cfg.APIAuthEnabled {
		return true
	}
	token := strings.TrimSpace(cfg.APIBearerToken)
	if token == "" {
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == token
}

func normalizeAPIBasePath(path string) string {
	value := "/" + strings.Trim(strings.TrimSpace(path), "/")
	if value == "/" {
		return "/api/v1"
	}
	return value
}

func parseSystemdAction(name string) (SystemdAction, bool) {
	switch name {
	case "daemon-reload":
		return SystemdDaemonReload, true
	case "enable-timer":
		return SystemdEnableTimer, true
	case "disable-timer":
		return SystemdDisableTimer, true
	case "start-timer":
		return SystemdStartTimer, true
	case "stop-timer":
		return SystemdStopTimer, true
	case "restart-service":
		return SystemdRestartSvc, true
	case "start-service":
		return SystemdStartSvc, true
	case "stop-service":
		return SystemdStopSvc, true
	default:
		return "", false
	}
}

func decodeJSON(r *http.Request, dst interface{}) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

func writeAPIData(w http.ResponseWriter, status int, data interface{}) {
	writeAPI(w, status, apiEnvelope{OK: true, Data: data})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string, details interface{}) {
	writeAPI(w, status, apiEnvelope{OK: false, Error: &apiError{Code: code, Message: message, Details: details}})
}

func writeAPI(w http.ResponseWriter, status int, payload apiEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
