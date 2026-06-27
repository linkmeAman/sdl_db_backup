package backupapp

import (
	"bytes"
	"compress/gzip"
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

func writeTestGzipSQL(path string) error {
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
