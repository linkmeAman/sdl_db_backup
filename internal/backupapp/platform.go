package backupapp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

type RuntimeProfile struct {
	EnvPath                 string   `json:"env_path"`
	WorkingDirectory        string   `json:"working_directory"`
	ExecutablePath          string   `json:"executable_path"`
	CurrentUser             string   `json:"current_user"`
	Hostname                string   `json:"hostname"`
	NonRoot                 bool     `json:"non_root"`
	ServiceUnitName         string   `json:"service_unit_name"`
	TimerUnitName           string   `json:"timer_unit_name"`
	ExecutionSource         string   `json:"execution_source"`
	BackupDir               string   `json:"backup_dir"`
	LogDir                  string   `json:"log_dir"`
	APIServerEnabled        bool     `json:"api_server_enabled"`
	APIServerListenAddr     string   `json:"api_server_listen_addr"`
	APIAuthEnabled          bool     `json:"api_auth_enabled"`
	SchedulerGuidance       string   `json:"scheduler_guidance"`
	PotentialConflictReason string   `json:"potential_conflict_reason,omitempty"`
	AuditChecklist          []string `json:"audit_checklist"`
}

type StorageSummary struct {
	BackupDir              string `json:"backup_dir"`
	LogDir                 string `json:"log_dir"`
	LogicalUploadEnabled   bool   `json:"logical_upload_enabled"`
	PhysicalUploadEnabled  bool   `json:"physical_upload_enabled"`
	UploadMode             string `json:"upload_mode"`
	S3Bucket               string `json:"s3_bucket"`
	S3Region               string `json:"s3_region"`
	S3LogicalPrefix        string `json:"s3_logical_prefix"`
	S3PhysicalPrefix       string `json:"s3_physical_prefix"`
	UploadScriptConfigured bool   `json:"upload_script_configured"`
	UploadURLConfigured    bool   `json:"upload_url_configured"`
}

type ServiceRenderResult struct {
	ServiceUnitName string `json:"service_unit_name"`
	TimerUnitName   string `json:"timer_unit_name"`
	ServiceUnit     string `json:"service_unit"`
	TimerUnit       string `json:"timer_unit"`
}

func currentOSUser() string {
	if u, err := user.Current(); err == nil {
		if u.Username != "" {
			return u.Username
		}
	}
	return getenv("USER", "unknown")
}

func currentHostname() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "unknown"
	}
	return name
}

func normalizedExecutionSource(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "runner"
	}
	return value
}

func runtimeAuditChecklist() []string {
	return []string{
		"Inspect root crontab and /etc/cron.* for backup commands.",
		"Inspect system-level systemd units for duplicate backup triggers.",
		"Inspect deployment hooks or external supervisors invoking this repo.",
	}
}

func GetRuntimeProfile(envPath string) (RuntimeProfile, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return RuntimeProfile{}, err
	}
	wd, _ := os.Getwd()
	exe, _ := os.Executable()
	profile := RuntimeProfile{
		EnvPath:             ResolveEnvFilePath(envPath),
		WorkingDirectory:    wd,
		ExecutablePath:      exe,
		CurrentUser:         currentOSUser(),
		Hostname:            currentHostname(),
		NonRoot:             currentOSUser() != "root",
		ServiceUnitName:     cfg.ServiceUnitName,
		TimerUnitName:       cfg.TimerUnitName,
		ExecutionSource:     normalizedExecutionSource(cfg.ExecutionSource),
		BackupDir:           cfg.BackupDir,
		LogDir:              cfg.LogDir,
		APIServerEnabled:    cfg.APIEnabled,
		APIServerListenAddr: cfg.APIListenAddr,
		APIAuthEnabled:      cfg.APIAuthEnabled,
		SchedulerGuidance:   "Use only a user-level systemd timer/service for scheduled runs. Manual shell and alternate runner paths should not be scheduled.",
		AuditChecklist:      runtimeAuditChecklist(),
	}
	if !profile.NonRoot {
		profile.PotentialConflictReason = "current process is running as root; scheduled execution should be user-level only"
	}
	return profile, nil
}

func GetStorageSummary(envPath string) (StorageSummary, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return StorageSummary{}, err
	}
	return StorageSummary{
		BackupDir:              cfg.BackupDir,
		LogDir:                 cfg.LogDir,
		LogicalUploadEnabled:   cfg.LogicalS3UploadEnabled,
		PhysicalUploadEnabled:  cfg.PhysicalS3UploadEnabled,
		UploadMode:             cfg.S3UploadMode,
		S3Bucket:               cfg.S3Bucket,
		S3Region:               cfg.S3Region,
		S3LogicalPrefix:        cfg.S3LogicalPrefix,
		S3PhysicalPrefix:       cfg.S3PhysicalPrefix,
		UploadScriptConfigured: strings.TrimSpace(cfg.S3UploadScript) != "",
		UploadURLConfigured:    strings.TrimSpace(cfg.S3UploadURL) != "",
	}, nil
}

func ReadRunHistory(path string) ([]RunResult, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	runs := []RunResult{}
	decoder := json.NewDecoder(file)
	for {
		var run RunResult
		if err := decoder.Decode(&run); err != nil {
			if err == io.EOF {
				break
			}
			return runs, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func ReadRunByID(path, runID string) (*RunResult, error) {
	runs, err := ReadRunHistory(path)
	if err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].RunID == runID {
			run := runs[i]
			return &run, nil
		}
	}
	return nil, nil
}

func ReadLogLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

func GenerateBearerToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func MaskedConfigView(cfg Config) map[string]interface{} {
	values := envMapFromConfig(cfg)
	masked := make(map[string]interface{}, len(values))
	for key, value := range values {
		switch key {
		case "DB_PASS", "BACKUP_S3_KEY_ID", "BACKUP_S3_KEY_SECRET", "BACKUP_XTRABACKUP_PASS", "BACKUP_API_BEARER_TOKEN":
			if strings.TrimSpace(value) == "" {
				masked[key] = ""
			} else {
				masked[key] = "********"
			}
		default:
			masked[key] = value
		}
	}
	return masked
}

func RenderUserSystemdUnits(envPath string) (ServiceRenderResult, error) {
	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		return ServiceRenderResult{}, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return ServiceRenderResult{}, err
	}
	resolvedEnv := ResolveEnvFilePath(envPath)
	service := strings.Join([]string{
		"[Unit]",
		"Description=SDL DB Backup Runner",
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=oneshot",
		"WorkingDirectory=" + wd,
		"Environment=BACKUP_ENV_FILE=" + resolvedEnv,
		"ExecStart=/usr/bin/env go run ./main.go",
		"TimeoutStartSec=0",
		"StandardOutput=journal",
		"StandardError=journal",
		"",
	}, "\n")
	timer := strings.Join([]string{
		"[Unit]",
		"Description=Run SDL DB backup scheduler",
		"",
		"[Timer]",
		"OnCalendar=*-*-* 00:00:00",
		"OnCalendar=*-*-* 06:00:00",
		"OnCalendar=*-*-* 12:00:00",
		"OnCalendar=*-*-* 18:00:00",
		"Persistent=true",
		"AccuracySec=1min",
		"Unit=" + cfg.ServiceUnitName,
		"",
		"[Install]",
		"WantedBy=timers.target",
		"",
	}, "\n")
	return ServiceRenderResult{
		ServiceUnitName: cfg.ServiceUnitName,
		TimerUnitName:   cfg.TimerUnitName,
		ServiceUnit:     service,
		TimerUnit:       timer,
	}, nil
}

func ManualRunWithSource(ctx context.Context, envPath string, opts ManualRunOptions, source string, sinks RunSinks) (RunResult, error) {
	base, err := LoadEffectiveConfig(envPath)
	if err != nil {
		return RunResult{}, err
	}
	cfg, _, err := BuildManualRunConfig(base, opts)
	if err != nil {
		return RunResult{}, err
	}
	cfg.ExecutionSource = normalizedExecutionSource(source)
	return RunBackup(ctx, cfg, sinks)
}

func DailyLogPath(cfg Config) string {
	return filepath.Join(cfg.LogDir, time.Now().Format("2006-01-02")+".log")
}
