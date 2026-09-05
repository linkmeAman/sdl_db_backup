package backupapp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLogicalRunPersistsValidationResult(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runDir := filepath.Join(dir, "run-1")
	runLogPath := filepath.Join(logDir, "backup-runs.jsonl")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := writeTestGzipSQL(filepath.Join(runDir, "app.sql.gz")); err != nil {
		t.Fatalf("write test gzip: %v", err)
	}

	record := runRecord{
		RunID:     "run-1",
		Status:    "success",
		RunFolder: runDir,
		LogFile:   filepath.Join(logDir, "run-1.log"),
		Results: []databaseResult{
			{Name: "app", Status: "success", SizeBytes: 123},
		},
	}
	if err := appendRunRecord(runLogPath, record); err != nil {
		t.Fatalf("appendRunRecord: %v", err)
	}

	res, err := ValidateLogicalRun(Config{RunLogPath: runLogPath, MetricsFile: metricsPath}, "run-1")
	if err != nil {
		t.Fatalf("ValidateLogicalRun returned error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected validation success, got %+v", res)
	}

	updated, err := ReadRunByID(runLogPath, "run-1")
	if err != nil {
		t.Fatalf("ReadRunByID returned error: %v", err)
	}
	if updated == nil || updated.ValidationStatus != "success" || updated.ValidationMode != "logical validation" {
		t.Fatalf("expected persisted validation metadata, got %+v", updated)
	}
	if len(updated.ValidationDatabases) != 1 || updated.ValidationDatabases[0].Database != "app" || !updated.ValidationDatabases[0].Valid {
		t.Fatalf("expected persisted validation database details, got %+v", updated.ValidationDatabases)
	}

	manifestData, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest runRecord
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.ValidationStatus != "success" || manifest.ValidationMode != "logical validation" {
		t.Fatalf("expected manifest validation fields, got %+v", manifest)
	}

	metricsData, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if !strings.Contains(string(metricsData), `backup_logical_validation_last_status{job="sdl_db_backup",service="mysql",env="pilot"} 1`) {
		t.Fatalf("expected validation metric to be emitted, got:\n%s", string(metricsData))
	}
}

func TestValidateLogicalRunPersistsMissingFolderFailure(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runLogPath := filepath.Join(logDir, "backup-runs.jsonl")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}

	record := runRecord{
		RunID:     "run-2",
		Status:    "success",
		RunFolder: filepath.Join(dir, "missing-run-folder"),
		LogFile:   filepath.Join(logDir, "run-2.log"),
	}
	if err := appendRunRecord(runLogPath, record); err != nil {
		t.Fatalf("appendRunRecord: %v", err)
	}

	res, err := ValidateLogicalRun(Config{RunLogPath: runLogPath, MetricsFile: metricsPath}, "run-2")
	if err != nil {
		t.Fatalf("ValidateLogicalRun returned error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected validation failure for missing run folder, got %+v", res)
	}
	if !strings.Contains(res.Error, "retention period exceeded") {
		t.Fatalf("expected missing folder error, got %+v", res)
	}

	updated, err := ReadRunByID(runLogPath, "run-2")
	if err != nil {
		t.Fatalf("ReadRunByID returned error: %v", err)
	}
	if updated == nil || updated.ValidationStatus != "failed" {
		t.Fatalf("expected failed validation metadata, got %+v", updated)
	}
	if !strings.Contains(updated.ValidationError, "retention period exceeded") {
		t.Fatalf("expected persisted validation failure reason, got %+v", updated)
	}
}

func TestValidateLogicalRunFailsWhenNoResultsRecorded(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runDir := filepath.Join(dir, "run-empty")
	runLogPath := filepath.Join(logDir, "backup-runs.jsonl")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	record := runRecord{
		RunID:     "run-empty",
		Status:    "success",
		RunFolder: runDir,
		LogFile:   filepath.Join(logDir, "run-empty.log"),
	}
	if err := appendRunRecord(runLogPath, record); err != nil {
		t.Fatalf("appendRunRecord: %v", err)
	}

	res, err := ValidateLogicalRun(Config{RunLogPath: runLogPath, MetricsFile: metricsPath}, "run-empty")
	if err != nil {
		t.Fatalf("ValidateLogicalRun returned error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected validation failure for empty logical results, got %+v", res)
	}
	if !strings.Contains(res.Error, "No logical dump results recorded") {
		t.Fatalf("expected explicit empty-results failure, got %+v", res)
	}
}

func TestRestoreTempDatabaseNameIsBoundedAndStable(t *testing.T) {
	name := restoreTempDatabaseName("PF-TickleRight/Very-Long Database Name With Spaces And Symbols!!!!")
	if len(name) > 64 {
		t.Fatalf("expected MySQL-safe temp db name length, got %d (%s)", len(name), name)
	}
	if !strings.HasPrefix(name, "test_restore_") {
		t.Fatalf("expected restore prefix, got %s", name)
	}
	if name != restoreTempDatabaseName("PF-TickleRight/Very-Long Database Name With Spaces And Symbols!!!!") {
		t.Fatalf("expected deterministic temp db naming, got %s", name)
	}
}

func TestRestoreTempDatabaseNameForPostgresIsBoundedAndStable(t *testing.T) {
	name := restoreTempDatabaseNameForEngine("postgres", "PF-TickleRight/Very-Long Database Name With Spaces And Symbols!!!!")
	if len(name) > 63 {
		t.Fatalf("expected PostgreSQL-safe temp db name length, got %d (%s)", len(name), name)
	}
	if !strings.HasPrefix(name, "test_restore_") {
		t.Fatalf("expected restore prefix, got %s", name)
	}
	if name != restoreTempDatabaseNameForEngine("postgres", "PF-TickleRight/Very-Long Database Name With Spaces And Symbols!!!!") {
		t.Fatalf("expected deterministic temp db naming, got %s", name)
	}
}

func TestPostgresDumpValidationRequiresPostgresCompletionMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sql.gz")
	if err := writeTestGzipSQLWithContents(path, "CREATE TABLE test (id int);\n-- PostgreSQL database dump complete\n"); err != nil {
		t.Fatalf("write PostgreSQL gzip: %v", err)
	}

	valid, err := validateLogicalArtifactForEngineWithEncryption("postgres", path, "", "", "")
	if err != nil || !valid {
		t.Fatalf("expected PostgreSQL validation success, valid=%t err=%v", valid, err)
	}

	valid, err = validateLogicalArtifactForEngineWithEncryption("mysql", path, "", "", "")
	if err == nil || valid {
		t.Fatalf("expected MySQL validation to reject PostgreSQL dump marker, valid=%t err=%v", valid, err)
	}
}

func TestPostgresRestoreClientUsesPsqlSafeArguments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := restoreTestCommand(ctx, Config{
		RestoreTestHost: "restore.example.internal",
		RestoreTestPort: "5432",
		RestoreTestUser: "restore_user",
		RestoreTestPass: "restore_secret",
	}, "postgres")
	client := restoreTestClient{engine: "postgres", testCmd: command}

	cmd := client.query("test_restore_app", "SELECT 1")
	if cmd.Path != "psql" && !strings.HasSuffix(cmd.Path, "/psql") {
		t.Fatalf("expected psql command, got %q", cmd.Path)
	}
	for _, want := range []string{"-h", "restore.example.internal", "-p", "5432", "-U", "restore_user", "-d", "test_restore_app", "-X", "-A", "-t", "ON_ERROR_STOP=1", "SELECT 1"} {
		if !containsString(cmd.Args, want) {
			t.Fatalf("expected psql args to contain %q, got %v", want, cmd.Args)
		}
	}
	if !containsString(cmd.Env, "PGPASSWORD=restore_secret") {
		t.Fatalf("expected PGPASSWORD in command environment")
	}

	if got := restoredBaseTableCountQuery("postgres", "ignored"); got != "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public'" {
		t.Fatalf("unexpected PostgreSQL table count query: %s", got)
	}
	if got := restoredEventCountQuery("postgres", "ignored"); got != "SELECT 0" {
		t.Fatalf("unexpected PostgreSQL event count query: %s", got)
	}
}

func TestPostgresConfigDefaultsUsePostgresRestoreEndpoint(t *testing.T) {
	cfg := loadConfigFromValues(map[string]string{"DB_ENGINE": "postgres"})
	if cfg.DBPort != "5432" || cfg.RestoreTestPort != "5432" || cfg.RestoreTestUser != "postgres" {
		t.Fatalf("expected PostgreSQL defaults, got db_port=%q restore_port=%q restore_user=%q", cfg.DBPort, cfg.RestoreTestPort, cfg.RestoreTestUser)
	}
}

func TestValidateLogicalRunFailsOnArtifactHashMismatch(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runDir := filepath.Join(dir, "run-hash")
	runLogPath := filepath.Join(logDir, "backup-runs.jsonl")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	dumpPath := filepath.Join(runDir, "app.sql.gz")
	if err := writeTestGzipSQL(dumpPath); err != nil {
		t.Fatalf("write test gzip: %v", err)
	}

	record := runRecord{
		RunID:     "run-hash",
		Status:    "success",
		RunFolder: runDir,
		LogFile:   filepath.Join(logDir, "run-hash.log"),
		Results: []databaseResult{
			{Name: "app", Status: "success", SizeBytes: 123, ArtifactSHA256: "deadbeef"},
		},
	}
	if err := appendRunRecord(runLogPath, record); err != nil {
		t.Fatalf("appendRunRecord: %v", err)
	}

	res, err := ValidateLogicalRun(Config{RunLogPath: runLogPath, MetricsFile: metricsPath}, "run-hash")
	if err != nil {
		t.Fatalf("ValidateLogicalRun returned error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected validation failure for artifact hash mismatch, got %+v", res)
	}
	if len(res.Databases) != 1 || !strings.Contains(res.Databases[0].Error, "artifact sha256 mismatch") {
		t.Fatalf("expected hash mismatch error, got %+v", res.Databases)
	}
}

func TestValidateLogicalRunFailsOnSQLHashMismatch(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runDir := filepath.Join(dir, "run-sql-hash")
	runLogPath := filepath.Join(logDir, "backup-runs.jsonl")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	dumpPath := filepath.Join(runDir, "app.sql.gz")
	if err := writeTestGzipSQL(dumpPath); err != nil {
		t.Fatalf("write test gzip: %v", err)
	}
	artifactHash, err := fileSHA256(dumpPath)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}

	record := runRecord{
		RunID:     "run-sql-hash",
		Status:    "success",
		RunFolder: runDir,
		LogFile:   filepath.Join(logDir, "run-sql-hash.log"),
		Results: []databaseResult{
			{Name: "app", Status: "success", SizeBytes: 123, ArtifactSHA256: artifactHash, SQLSHA256: "deadbeef"},
		},
	}
	if err := appendRunRecord(runLogPath, record); err != nil {
		t.Fatalf("appendRunRecord: %v", err)
	}

	res, err := ValidateLogicalRun(Config{RunLogPath: runLogPath, MetricsFile: metricsPath}, "run-sql-hash")
	if err != nil {
		t.Fatalf("ValidateLogicalRun returned error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected validation failure for sql hash mismatch, got %+v", res)
	}
	if len(res.Databases) != 1 || !strings.Contains(res.Databases[0].Error, "sql sha256 mismatch") {
		t.Fatalf("expected sql hash mismatch error, got %+v", res.Databases)
	}
}

func TestValidateLogicalRunSupportsAuthenticatedEncryptedArtifact(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	runDir := filepath.Join(dir, "run-encrypted")
	runLogPath := filepath.Join(logDir, "backup-runs.jsonl")
	metricsPath := filepath.Join(dir, "collector", "sdl_db_backup.prom")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	plainPath := filepath.Join(dir, "app.sql.gz")
	if err := writeTestGzipSQL(plainPath); err != nil {
		t.Fatalf("write test gzip: %v", err)
	}
	plain, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatalf("read test gzip: %v", err)
	}
	encryptedPath := filepath.Join(runDir, "app.sql.gz.enc")
	encryptedFile, err := os.OpenFile(encryptedPath, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatalf("create encrypted artifact: %v", err)
	}
	writer, err := EncryptWriter(encryptedFile, testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptWriter: %v", err)
	}
	if _, err := writer.Write(plain); err != nil {
		t.Fatalf("encrypt gzip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("finalize encrypted artifact: %v", err)
	}
	if err := encryptedFile.Close(); err != nil {
		t.Fatalf("close encrypted artifact: %v", err)
	}
	artifactHash, err := fileSHA256(encryptedPath)
	if err != nil {
		t.Fatalf("artifact hash: %v", err)
	}
	sqlHash, err := gzipPayloadSHA256(plainPath)
	if err != nil {
		t.Fatalf("sql hash: %v", err)
	}

	record := runRecord{
		RunID:     "run-encrypted",
		Status:    "success",
		RunFolder: runDir,
		Results: []databaseResult{{
			Name: "app", Status: "success", OutputPath: encryptedPath,
			ArtifactSHA256: artifactHash, SQLSHA256: sqlHash,
		}},
	}
	if err := appendRunRecord(runLogPath, record); err != nil {
		t.Fatalf("append run record: %v", err)
	}
	res, err := ValidateLogicalRun(Config{RunLogPath: runLogPath, MetricsFile: metricsPath, EncryptionKey: testEncryptionKey}, "run-encrypted")
	if err != nil {
		t.Fatalf("ValidateLogicalRun: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected encrypted validation success, got %+v", res)
	}
}

func writeTestGzipSQL(path string) error {
	return writeTestGzipSQLWithContents(path, "CREATE TABLE test (id int);\n-- Dump completed\n")
}

func writeTestGzipSQLWithContents(path string, contents string) error {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(contents)); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o640)
}

func TestMySQLRestorePasswordDoesNotPolluteQueryOutput(t *testing.T) {
	// MySQL warns on stderr for -p<password>. Restore queries use CombinedOutput,
	// so that warning breaks numeric parsing and differs from source row hashes.
	t.Setenv("MYSQL_PWD", "unrelated-source-password")
	for _, password := range []string{"restore_secret", ""} {
		command := restoreTestCommand(context.Background(), Config{
			MySQLBin: "mysql", RestoreTestHost: "restore.internal",
			RestoreTestPort: "3306", RestoreTestUser: "restore_user",
			RestoreTestPass: password,
		}, "mysql")
		cmd := command("-N", "-e", "SELECT 1")
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "-p") || strings.Contains(arg, "restore_secret") {
				t.Fatalf("password leaked into arguments: %v", cmd.Args)
			}
		}
		if !containsString(cmd.Environ(), "MYSQL_PWD="+password) {
			t.Fatal("restore password must override the inherited source password")
		}
	}
}
