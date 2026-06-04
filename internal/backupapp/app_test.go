package backupapp

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildMySQLDumpArgsOmitDatabasesFlag(t *testing.T) {
	args := buildMySQLDumpArgs("app_db", nil, []string{"broken_view"})
	required := []string{
		"--single-transaction",
		"--quick",
		"--routines",
		"--triggers",
		"--events",
		"--no-tablespaces",
		"--set-gtid-purged=OFF",
		"--ignore-error=1356,1449,1227",
		"app_db",
		"--ignore-table=app_db.broken_view",
	}
	for _, want := range required {
		if !slices.Contains(args, want) {
			t.Fatalf("expected mysqldump args to contain %q; args=%v", want, args)
		}
	}
	if slices.Contains(args, "--databases") {
		t.Fatalf("expected mysqldump args to omit --databases; args=%v", args)
	}
}

func TestBuildMySQLDumpArgsCanLimitTables(t *testing.T) {
	args := buildMySQLDumpArgs("app_db", []string{"users", "orders"}, nil)
	if !slices.Contains(args, "users") || !slices.Contains(args, "orders") {
		t.Fatalf("expected selected tables in mysqldump args: %v", args)
	}
	dbIndex := slices.Index(args, "app_db")
	usersIndex := slices.Index(args, "users")
	if dbIndex < 0 || usersIndex <= dbIndex {
		t.Fatalf("expected tables after database name: %v", args)
	}
}

func TestParseTableScope(t *testing.T) {
	scope := parseTableScope("db1:users,orders;db2:events")
	if !slices.Equal(scope["db1"], []string{"users", "orders"}) {
		t.Fatalf("unexpected db1 scope: %+v", scope["db1"])
	}
	if !slices.Equal(scope["db2"], []string{"events"}) {
		t.Fatalf("unexpected db2 scope: %+v", scope["db2"])
	}
}

func TestFilterRequestedTablesSkipsMissing(t *testing.T) {
	available, missing := filterRequestedTables(
		[]string{"action", "notification_delivery_rule", "backup"},
		[]string{"action", "backup"},
	)

	if !slices.Equal(available, []string{"action", "backup"}) {
		t.Fatalf("unexpected available tables: %+v", available)
	}
	if !slices.Equal(missing, []string{"notification_delivery_rule"}) {
		t.Fatalf("unexpected missing tables: %+v", missing)
	}
}

func TestShouldLogDumpLine(t *testing.T) {
	if !shouldLogDumpLine(`mysqldump: Couldn't find table: "notification_event"`) {
		t.Fatalf("expected missing-table dump line to be logged")
	}
	if shouldLogDumpLine("note: dumped 10 tables") {
		t.Fatalf("expected routine dump progress to stay quiet")
	}
}

func TestShouldLogPhysicalLine(t *testing.T) {
	if !shouldLogPhysicalLine("xtrabackup", "xtrabackup: Transaction log of lsn (1) to (2) was copied.") {
		t.Fatalf("expected xtrabackup summary line to be logged")
	}
	if shouldLogPhysicalLine("xtrabackup", "xtrabackup: Streaming ./pf_central/request.ibd") {
		t.Fatalf("expected xtrabackup streaming line to be suppressed")
	}
	if !shouldLogPhysicalLine("xbcloud", "xbcloud: Upload completed.") {
		t.Fatalf("expected xbcloud completion line to be logged")
	}
	if shouldLogPhysicalLine("xbcloud", "xbcloud: [0] successfully uploaded chunk: file") {
		t.Fatalf("expected xbcloud chunk line to be suppressed")
	}
}

func TestBuildManualRunConfigLocalOnlyDisablesPhysical(t *testing.T) {
	cfg := Config{
		LogicalEnabled:          true,
		PhysicalEnabled:         true,
		LogicalS3UploadEnabled:  true,
		PhysicalS3UploadEnabled: true,
	}
	out, preview, err := BuildManualRunConfig(cfg, ManualRunOptions{
		Mode:       ManualRunBoth,
		UploadMode: ManualUploadLocalOnly,
		ForceNow:   true,
	})
	if err != nil {
		t.Fatalf("BuildManualRunConfig returned error: %v", err)
	}
	if out.LogicalS3UploadEnabled {
		t.Fatalf("expected logical upload disabled")
	}
	if out.PhysicalEnabled {
		t.Fatalf("expected physical backup disabled in local-only mode")
	}
	if len(preview.Warnings) == 0 {
		t.Fatalf("expected warning about physical backup disablement")
	}
}

func TestParseSystemdShow(t *testing.T) {
	state := parseSystemdShow("LoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\n", "svc")
	if state.LoadState != "loaded" || state.Active != "active" || state.SubState != "running" || state.Enabled != "enabled" {
		t.Fatalf("unexpected systemd state: %+v", state)
	}
}

func TestInitRunLoggerWritesRunAndDailyLogs(t *testing.T) {
	dir := t.TempDir()
	runID := "2026-05-27_20-00-00"
	var console bytes.Buffer

	origWriter := log.Writer()
	defer log.SetOutput(origWriter)

	closer, runPath, dailyPath, err := initRunLogger(dir, runID, &console)
	if err != nil {
		t.Fatalf("initRunLogger returned error: %v", err)
	}
	log.Print("hello daily logger")
	if err := closer.Close(); err != nil {
		t.Fatalf("closing logger failed: %v", err)
	}

	runData, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	dailyData, err := os.ReadFile(dailyPath)
	if err != nil {
		t.Fatalf("read daily log: %v", err)
	}
	if !strings.Contains(string(runData), "hello daily logger") {
		t.Fatalf("expected run log to contain message: %s", string(runData))
	}
	if !strings.Contains(string(dailyData), "hello daily logger") {
		t.Fatalf("expected daily log to contain message: %s", string(dailyData))
	}
	if !strings.Contains(console.String(), "hello daily logger") {
		t.Fatalf("expected console sink to contain message: %s", console.String())
	}
}

func TestSaveConfigPreservesUnknownAndAppendsManagedKeys(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	initial := "# comment\nUNKNOWN_KEY=keep-me\nDB_USER=old-user\n"
	if err := os.WriteFile(envPath, []byte(initial), 0o640); err != nil {
		t.Fatalf("write initial env: %v", err)
	}

	cfg := Config{
		DBUser:                  "backup_logical",
		DBPass:                  "p@ss word",
		DBHost:                  "127.0.0.1",
		DBPort:                  "3306",
		BackupDir:               "/backups",
		LogDir:                  "/backups/logs",
		LockFile:                "/backups/logs/backup.lock",
		MySQLBin:                "mysql",
		MySQLDumpBin:            "mysqldump",
		RetryCount:              3,
		RetentionDays:           5,
		LogicalEnabled:          true,
		LogicalTimeoutPerDB:     30 * time.Minute,
		LogicalS3UploadEnabled:  false,
		LogicalSchedule:         "always",
		PhysicalSchedule:        "weekly@sun,02:00",
		PhysicalTimeout:         6 * time.Hour,
		DiscoveryTimeout:        30 * time.Second,
		PreflightTimeout:        15 * time.Second,
		RetryBaseDelay:          2 * time.Second,
		RetryMaxDelay:           20 * time.Second,
		S3UploadTimeout:         2 * time.Hour,
		S3PHPBin:                "/usr/bin/php",
		S3PhysicalPrefix:        "prefix",
		S3Bucket:                "bucket",
		S3Region:                "ap-south-1",
		XbcloudBin:              "xbcloud",
		PhysicalEnabled:         true,
		PhysicalS3UploadEnabled: true,
		XtrabackupBin:           "xtrabackup",
		XtrabackupParallel:      4,
		XtrabackupUser:          "xtrabackup",
		XtrabackupPass:          "secret",
		XtrabackupWorkDir:       "/tmp",
	}
	if err := SaveConfig(envPath, cfg); err != nil {
		t.Fatalf("SaveConfig returned error: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "# comment") {
		t.Fatalf("expected comment preserved: %s", text)
	}
	if !strings.Contains(text, "UNKNOWN_KEY=keep-me") {
		t.Fatalf("expected unknown key preserved: %s", text)
	}
	if !strings.Contains(text, "DB_USER=backup_logical") {
		t.Fatalf("expected DB_USER updated: %s", text)
	}
	if !strings.Contains(text, "DB_PASS=\"p@ss word\"") {
		t.Fatalf("expected DB_PASS quoted and saved: %s", text)
	}
	if !strings.Contains(text, "BACKUP_LOGICAL_SCHEDULE=always") {
		t.Fatalf("expected missing managed key appended: %s", text)
	}
}

func TestLoadConfigWithOverridesPrefersProcessEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("BACKUP_DIR=/from-file\nDB_USER=file-user\n"), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	t.Setenv("BACKUP_DIR", "/from-env")
	t.Setenv("DB_USER", "env-user")

	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		t.Fatalf("loadConfigWithOverrides returned error: %v", err)
	}
	if cfg.BackupDir != "/from-env" {
		t.Fatalf("expected BACKUP_DIR override, got %q", cfg.BackupDir)
	}
	if cfg.DBUser != "env-user" {
		t.Fatalf("expected DB_USER override, got %q", cfg.DBUser)
	}
}

func TestTemporaryOverridesAffectEffectiveConfigOnly(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	logDir := filepath.Join(dir, "logs")
	if err := os.WriteFile(envPath, []byte(strings.Join([]string{
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_LOGICAL_SCHEDULE=daily@02:00",
		"BACKUP_PHYSICAL_SCHEDULE=weekly@sun,02:00",
		"BACKUP_LOGICAL_S3_UPLOAD_ENABLED=true",
	}, "\n")+"\n"), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	err := SaveTemporaryOverrides(envPath, TemporaryOverrides{
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Values: map[string]string{
			"BACKUP_LOGICAL_SCHEDULE":          "daily@00:00,12:00",
			"BACKUP_PHYSICAL_SCHEDULE":         "daily@03:00",
			"BACKUP_LOGICAL_S3_UPLOAD_ENABLED": "false",
		},
	})
	if err != nil {
		t.Fatalf("SaveTemporaryOverrides returned error: %v", err)
	}

	base, err := LoadConfig(envPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if base.LogicalSchedule != "daily@02:00" || !base.LogicalS3UploadEnabled {
		t.Fatalf("base config should not include temporary overrides: %+v", base)
	}

	effective, err := LoadEffectiveConfig(envPath)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig returned error: %v", err)
	}
	if effective.LogicalSchedule != "daily@00:00,12:00" {
		t.Fatalf("expected temporary logical schedule, got %q", effective.LogicalSchedule)
	}
	if effective.PhysicalSchedule != "daily@03:00" {
		t.Fatalf("expected temporary physical schedule, got %q", effective.PhysicalSchedule)
	}
	if effective.LogicalS3UploadEnabled {
		t.Fatalf("expected temporary logical S3 upload override to false")
	}
}

func TestProcessEnvOverridesTemporaryOverrides(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	logDir := filepath.Join(dir, "logs")
	if err := os.WriteFile(envPath, []byte("BACKUP_DIR="+dir+"\nBACKUP_LOG_DIR="+logDir+"\nBACKUP_LOGICAL_SCHEDULE=daily@02:00\n"), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := SaveTemporaryOverrides(envPath, TemporaryOverrides{
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Values: map[string]string{
			"BACKUP_LOGICAL_SCHEDULE": "daily@00:00,12:00",
		},
	}); err != nil {
		t.Fatalf("SaveTemporaryOverrides returned error: %v", err)
	}

	t.Setenv("BACKUP_LOGICAL_SCHEDULE", "always")
	effective, err := LoadEffectiveConfig(envPath)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig returned error: %v", err)
	}
	if effective.LogicalSchedule != "always" {
		t.Fatalf("expected process env to win, got %q", effective.LogicalSchedule)
	}
}

func TestExpiredTemporaryOverridesAreIgnored(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	logDir := filepath.Join(dir, "logs")
	if err := os.WriteFile(envPath, []byte("BACKUP_DIR="+dir+"\nBACKUP_LOG_DIR="+logDir+"\nBACKUP_LOGICAL_SCHEDULE=daily@02:00\n"), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	data := []byte(`{"created_at":"2026-05-28T00:00:00Z","expires_at":"2000-01-01T00:00:00Z","values":{"BACKUP_LOGICAL_SCHEDULE":"always"}}`)
	if err := os.WriteFile(filepath.Join(logDir, "backup-temporary-overrides.json"), data, 0o640); err != nil {
		t.Fatalf("write temporary overrides: %v", err)
	}

	effective, err := LoadEffectiveConfig(envPath)
	if err != nil {
		t.Fatalf("LoadEffectiveConfig returned error: %v", err)
	}
	if effective.LogicalSchedule != "daily@02:00" {
		t.Fatalf("expected expired override ignored, got %q", effective.LogicalSchedule)
	}
}

func TestReadLatestRunInfoReturnsLastRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup-runs.jsonl")
	content := `{"timestamp":"2026-05-27T10:00:00Z","run_id":"old","status":"failed","duration":"1s","databases_total":1,"databases_succeeded":0,"databases_failed":1}
{"timestamp":"2026-05-28T10:00:00Z","run_id":"new","status":"success","run_folder":"/tmp/run","log_file":"/tmp/run.log","duration":"2s","databases_total":2,"databases_succeeded":2,"databases_failed":0}
`
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatalf("write runs file: %v", err)
	}
	info, err := readLatestRunInfo(path)
	if err != nil {
		t.Fatalf("readLatestRunInfo returned error: %v", err)
	}
	if info == nil || info.RunID != "new" || info.Status != "success" {
		t.Fatalf("unexpected latest run: %+v", info)
	}
}

func TestGetHealthReportWithDisabledBackups(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	logDir := filepath.Join(dir, "logs")
	content := strings.Join([]string{
		"DB_USER=tester",
		"DB_PASS=testpass",
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_LOGICAL_ENABLED=false",
		"BACKUP_PHYSICAL_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	runsPath := filepath.Join(logDir, "backup-runs.jsonl")
	runs := `{"timestamp":"2026-05-28T12:00:00Z","run_id":"run-1","status":"success","run_folder":"/tmp/run-1","log_file":"/tmp/run-1.log","duration":"4s","databases_total":0,"databases_succeeded":0,"databases_failed":0}
`
	if err := os.WriteFile(runsPath, []byte(runs), 0o640); err != nil {
		t.Fatalf("write run log: %v", err)
	}

	report, err := GetHealthReport(context.Background(), envPath)
	if err != nil {
		t.Fatalf("GetHealthReport returned error: %v", err)
	}
	if report.LatestRun == nil || report.LatestRun.RunID != "run-1" {
		t.Fatalf("unexpected latest run in report: %+v", report.LatestRun)
	}
	if report.Logical.Status != "disabled" {
		t.Fatalf("expected logical disabled, got %q", report.Logical.Status)
	}
	if report.Physical.Status != "disabled" {
		t.Fatalf("expected physical disabled, got %q", report.Physical.Status)
	}
	expectedDaily := filepath.Join(logDir, time.Now().Format("2006-01-02")+".log")
	if report.DailyLogPath != expectedDaily {
		t.Fatalf("expected daily log path %q, got %q", expectedDaily, report.DailyLogPath)
	}
}
