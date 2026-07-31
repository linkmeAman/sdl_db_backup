package backupapp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

func TestNormalizedLogicalParallelism(t *testing.T) {
	if got := normalizedLogicalParallelism(0); got != 1 {
		t.Fatalf("expected parallelism fallback 1, got %d", got)
	}
	if got := normalizedLogicalParallelism(3); got != 3 {
		t.Fatalf("expected parallelism 3, got %d", got)
	}
}

func TestNormalizedLogicalGzipLevel(t *testing.T) {
	if got := normalizedLogicalGzipLevel(99); got != gzip.BestSpeed {
		t.Fatalf("expected invalid gzip level to fall back to BestSpeed, got %d", got)
	}
	if got := normalizedLogicalGzipLevel(0); got != 0 {
		t.Fatalf("expected gzip level 0 preserved, got %d", got)
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

func TestFilterBrokenViewsFromObjectsSkipsOnlyBrokenViews(t *testing.T) {
	objects := []string{"accounts", "active_users", "audit_log", "broken_view"}
	filtered := filterBrokenViewsFromObjects(objects, []string{"broken_view"})
	if !slices.Equal(filtered, []string{"accounts", "active_users", "audit_log"}) {
		t.Fatalf("unexpected filtered objects: %+v", filtered)
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

func TestBuildSchemaFingerprintDeterministic(t *testing.T) {
	a := buildSchemaFingerprint(map[string][]string{
		"view":    {"v_users"},
		"table":   {"orders", "users"},
		"trigger": {"trg_orders_after_insert"},
		"routine": {"sp_rebuild_cache"},
		"event":   {"ev_cleanup"},
	})
	b := buildSchemaFingerprint(map[string][]string{
		"event":   {"ev_cleanup"},
		"routine": {"sp_rebuild_cache"},
		"trigger": {"trg_orders_after_insert"},
		"table":   {"users", "orders"},
		"view":    {"v_users"},
	})
	if a == "" || b == "" {
		t.Fatalf("expected non-empty schema fingerprints")
	}
	if a != b {
		t.Fatalf("expected deterministic fingerprint, got %q and %q", a, b)
	}
}

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.bin")
	content := []byte("hello backup")
	if err := os.WriteFile(path, content, 0o640); err != nil {
		t.Fatalf("write file: %v", err)
	}
	hash, err := fileSHA256(path)
	if err != nil {
		t.Fatalf("fileSHA256 returned error: %v", err)
	}
	expected := sha256.Sum256(content)
	if hash != hex.EncodeToString(expected[:]) {
		t.Fatalf("unexpected sha256: %s", hash)
	}
}

func TestGzipPayloadSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.sql.gz")
	if err := writeTestGzipSQL(path); err != nil {
		t.Fatalf("writeTestGzipSQL: %v", err)
	}
	hash, err := gzipPayloadSHA256(path)
	if err != nil {
		t.Fatalf("gzipPayloadSHA256 returned error: %v", err)
	}
	expected := sha256.Sum256([]byte("CREATE TABLE test (id int);\n-- Dump completed\n"))
	if hash != hex.EncodeToString(expected[:]) {
		t.Fatalf("unexpected payload sha256: %s", hash)
	}
}

func TestIsIgnorablePipeReadError(t *testing.T) {
	if !isIgnorablePipeReadError(errors.New("read |0: file already closed")) {
		t.Fatalf("expected closed pipe read error to be ignored")
	}
	if !isIgnorablePipeReadError(errors.New("read /proc/self/fd/3: file already closed")) {
		t.Fatalf("expected generic file already closed error to be ignored")
	}
	if isIgnorablePipeReadError(errors.New("permission denied")) {
		t.Fatalf("expected unrelated read error to remain fatal")
	}
}

func TestShouldBlockScheduledRootRun(t *testing.T) {
	if !shouldBlockScheduledRootRun("root", "runner", false) {
		t.Fatalf("expected scheduled runner to be blocked for root when allowRoot is false")
	}
	if shouldBlockScheduledRootRun("root", "runner", true) {
		t.Fatalf("expected scheduled runner to be allowed for root when allowRoot is true")
	}
	if shouldBlockScheduledRootRun("root", "api", false) {
		t.Fatalf("expected api-triggered run to remain allowed for root")
	}
	if shouldBlockScheduledRootRun("developer", "runner", false) {
		t.Fatalf("expected runner to remain allowed for non-root user")
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

func TestBuildManualRunConfigPreflightOnlyMarksConfigAndPreview(t *testing.T) {
	cfg := Config{LogicalEnabled: true}
	out, preview, err := BuildManualRunConfig(cfg, ManualRunOptions{
		Mode:          ManualRunLogicalOnly,
		UploadMode:    ManualUploadNormal,
		ForceNow:      true,
		PreflightOnly: true,
	})
	if err != nil {
		t.Fatalf("BuildManualRunConfig returned error: %v", err)
	}
	if !out.PreflightOnly {
		t.Fatalf("expected preflight-only flag enabled")
	}
	if !strings.Contains(strings.Join(preview.Lines, "\n"), "Run type: preflight only") {
		t.Fatalf("expected preview to describe preflight-only mode: %+v", preview)
	}
	if !strings.Contains(strings.Join(preview.Lines, "\n"), "Invocation context:") {
		t.Fatalf("expected preview to describe invocation context: %+v", preview)
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
		RetentionDaily:          5,
		LogicalEnabled:          true,
		LogicalTimeoutPerDB:     30 * time.Minute,
		LogicalParallel:         2,
		LogicalGzipLevel:        1,
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
	if !strings.Contains(text, "BACKUP_LOGICAL_PARALLEL=2") {
		t.Fatalf("expected logical parallel saved: %s", text)
	}
	if !strings.Contains(text, "BACKUP_LOGICAL_GZIP_LEVEL=1") {
		t.Fatalf("expected logical gzip level saved: %s", text)
	}
}

func TestLoadConfigWithOverridesReadsLogicalPerformanceSettings(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	content := "BACKUP_LOGICAL_PARALLEL=3\nBACKUP_LOGICAL_GZIP_LEVEL=5\nBACKUP_XBCLOUD_PARALLEL=2\nBACKUP_XBCLOUD_FIFO_STREAMS=1\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}

	cfg, err := loadConfigWithOverrides(envPath)
	if err != nil {
		t.Fatalf("loadConfigWithOverrides returned error: %v", err)
	}
	if cfg.LogicalParallel != 3 {
		t.Fatalf("expected logical parallel 3, got %d", cfg.LogicalParallel)
	}
	if cfg.LogicalGzipLevel != 5 {
		t.Fatalf("expected logical gzip level 5, got %d", cfg.LogicalGzipLevel)
	}
	if cfg.XbcloudParallel != 2 {
		t.Fatalf("expected xbcloud parallel 2, got %d", cfg.XbcloudParallel)
	}
	if cfg.XbcloudFIFOStreams != 1 {
		t.Fatalf("expected xbcloud fifo streams 1, got %d", cfg.XbcloudFIFOStreams)
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
{"timestamp":"2026-05-28T10:00:00Z","run_id":"new","status":"success","run_folder":"/tmp/run","log_file":"/tmp/run.log","duration":"2s","databases_total":2,"databases_succeeded":2,"databases_failed":0,"logical_upload_status":"success","adaptive_xbcloud_parallel":2}
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
	if info.LogicalUploadStatus != "success" {
		t.Fatalf("expected logical upload status preserved, got %+v", info)
	}
	if info.AdaptiveXbcloudParallel != 2 {
		t.Fatalf("expected xbcloud adaptive parallel preserved, got %+v", info)
	}
	if info.FinalOutcome != "" {
		t.Fatalf("expected no final outcome for clean success, got %+v", info)
	}
}

func TestDeriveFinalOutcome(t *testing.T) {
	cases := []struct {
		name               string
		status             string
		total              int
		failed             int
		failureReason      string
		cleanupError       string
		logicalUploadError string
		want               string
	}{
		{name: "explicit failure reason", status: "failed", failureReason: "logical backup upload failed: s3 unavailable", want: "logical backup upload failed: s3 unavailable"},
		{name: "upload error fallback", status: "failed", logicalUploadError: "s3 unavailable", want: "logical upload failed: s3 unavailable"},
		{name: "cleanup issue success", status: "success", cleanupError: "permission denied", want: "backup completed with cleanup issue: permission denied"},
		{name: "partial derived", status: "partial", total: 4, failed: 1, want: "1 of 4 database backups failed"},
		{name: "failed before db finish", status: "failed", want: "backup failed before any database finished"},
	}
	for _, tc := range cases {
		if got := DeriveFinalOutcome(tc.status, tc.total, tc.failed, tc.failureReason, tc.cleanupError, tc.logicalUploadError); got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestBuildAPIRunResultIncludesFinalOutcome(t *testing.T) {
	run := RunResult{
		RunID:              "run-1",
		Status:             "partial",
		DatabasesTotal:     4,
		DatabasesFailed:    1,
		CleanupError:       "",
		LogicalUploadError: "",
	}
	apiRun := BuildAPIRunResult(run)
	if apiRun.RunID != "run-1" {
		t.Fatalf("expected run id preserved, got %+v", apiRun)
	}
	if apiRun.FinalOutcome != "1 of 4 database backups failed" {
		t.Fatalf("expected derived final outcome, got %+v", apiRun)
	}
}

func TestMarkLogicalUploadFailureUpdatesRunStatus(t *testing.T) {
	record := runRecord{Status: "success"}

	markLogicalUploadFailure(&record, errors.New("s3 unavailable"))

	if record.Status != "failed" {
		t.Fatalf("expected upload failure to mark successful run failed, got %q", record.Status)
	}
	if record.LogicalUploadStatus != "failed" || record.LogicalUploadError != "s3 unavailable" {
		t.Fatalf("unexpected logical upload fields: %+v", record)
	}
	if !strings.Contains(record.FailureReason, "logical backup upload failed") {
		t.Fatalf("expected upload failure reason, got %q", record.FailureReason)
	}
}

func TestMarkLogicalUploadFailurePreservesPartialStatus(t *testing.T) {
	record := runRecord{Status: "partial", FailureReason: "1 of 2 database backups failed"}

	markLogicalUploadFailure(&record, errors.New("s3 unavailable"))

	if record.Status != "partial" {
		t.Fatalf("expected partial status preserved, got %q", record.Status)
	}
	if record.FailureReason != "1 of 2 database backups failed" {
		t.Fatalf("expected original failure reason preserved, got %q", record.FailureReason)
	}
}

func TestGetHealthReportWithDisabledBackups(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	logDir := filepath.Join(dir, "logs")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")
	content := strings.Join([]string{
		"DB_USER=tester",
		"DB_PASS=testpass",
		"BACKUP_DIR=" + dir,
		"BACKUP_LOG_DIR=" + logDir,
		"BACKUP_METRICS_FILE=" + metricsPath,
		"BACKUP_LOGICAL_ENABLED=false",
		"BACKUP_PHYSICAL_ENABLED=false",
	}, "\n") + "\n"
	if err := os.WriteFile(envPath, []byte(content), 0o640); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(metricsPath), 0o755); err != nil {
		t.Fatalf("create metrics dir: %v", err)
	}
	runsPath := filepath.Join(logDir, "backup-runs.jsonl")
	runs := `{"timestamp":"2026-05-28T12:00:00Z","run_id":"run-1","status":"success","run_folder":"/tmp/run-1","log_file":"/tmp/run-1.log","duration":"4s","databases_total":0,"databases_succeeded":0,"databases_failed":0,"adaptive_xbcloud_parallel":2}
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
	if report.LatestRun.AdaptiveXbcloudParallel != 2 {
		t.Fatalf("expected latest run xbcloud adaptive parallel, got %+v", report.LatestRun)
	}
	if report.Logical.Status != "disabled" {
		t.Fatalf("expected logical disabled, got %q", report.Logical.Status)
	}
	if report.Physical.Status != "disabled" {
		t.Fatalf("expected physical disabled, got %q", report.Physical.Status)
	}
	if report.Metrics.Status != "ok" {
		t.Fatalf("expected metrics health ok, got %q (%s)", report.Metrics.Status, report.Metrics.Message)
	}
	if len(report.Directories) != 3 {
		t.Fatalf("expected 3 directory checks, got %d", len(report.Directories))
	}
	expectedDaily := filepath.Join(logDir, time.Now().Format("2006-01-02")+".log")
	if report.DailyLogPath != expectedDaily {
		t.Fatalf("expected daily log path %q, got %q", expectedDaily, report.DailyLogPath)
	}
}

func TestFilterDatabasesExcludesBackupPrefixedDatabasesByDefault(t *testing.T) {
	discovered := []string{"pf_main", "bk_pf_main", "pf_central"}
	filtered := filterDatabases(config{}, discovered)
	if strings.Join(filtered, ",") != "pf_main,pf_central" {
		t.Fatalf("unexpected filtered databases: %v", filtered)
	}
}

func TestFilterDatabasesAllowsExplicitBackupPrefixedDatabases(t *testing.T) {
	cfg := config{LogicalDatabases: []string{"bk_pf_main"}}
	discovered := []string{"pf_main", "bk_pf_main", "pf_central"}
	filtered := filterDatabases(cfg, discovered)
	if strings.Join(filtered, ",") != "bk_pf_main" {
		t.Fatalf("unexpected filtered databases: %v", filtered)
	}
}

type fakeOwnershipInfo struct {
	name  string
	mode  os.FileMode
	isDir bool
	sys   any
}

func (f fakeOwnershipInfo) Name() string       { return f.name }
func (f fakeOwnershipInfo) Size() int64        { return 0 }
func (f fakeOwnershipInfo) Mode() os.FileMode  { return f.mode }
func (f fakeOwnershipInfo) ModTime() time.Time { return time.Time{} }
func (f fakeOwnershipInfo) IsDir() bool        { return f.isDir }
func (f fakeOwnershipInfo) Sys() any           { return f.sys }

func TestScanOwnershipDriftFindsMismatchedNestedEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "old-run"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old-run", "bk.sql.gz"), []byte("x"), 0o640); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owned.log"), []byte("ok"), 0o640); err != nil {
		t.Fatalf("write file: %v", err)
	}

	expectedUID := uint32(1000)
	lstat := func(path string) (os.FileInfo, error) {
		base := filepath.Base(path)
		uid := expectedUID
		if strings.Contains(path, "old-run") {
			uid = 0
		}
		mode := os.FileMode(0o640)
		isDir := false
		if base == "old-run" {
			mode = os.ModeDir | 0o755
			isDir = true
		}
		return fakeOwnershipInfo{
			name:  base,
			mode:  mode,
			isDir: isDir,
			sys:   &syscall.Stat_t{Uid: uid, Gid: 0},
		}, nil
	}

	findings, err := scanOwnershipDrift(dir, expectedUID, 2, 5, lstat)
	if err != nil {
		t.Fatalf("scanOwnershipDrift returned error: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 drift findings, got %+v", findings)
	}
	if findings[0].RelativePath != "old-run" || findings[1].RelativePath != filepath.Join("old-run", "bk.sql.gz") {
		t.Fatalf("unexpected drift findings: %+v", findings)
	}
	formatted := formatOwnershipDrift(findings)
	if !strings.Contains(formatted, "old-run(uid=0") || !strings.Contains(formatted, filepath.Join("old-run", "bk.sql.gz")+"(uid=0") {
		t.Fatalf("unexpected formatted drift summary: %s", formatted)
	}
}
