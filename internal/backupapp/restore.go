package backupapp

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DatabaseValidationResult struct {
	Database string `json:"database"`
	Valid    bool   `json:"valid"`
	Error    string `json:"error,omitempty"`
}

type LogicalValidationResult struct {
	RunID     string                     `json:"run_id"`
	Valid     bool                       `json:"valid"`
	Databases []DatabaseValidationResult `json:"databases"`
	Error     string                     `json:"error,omitempty"`
}

func validationFailureSummary(result LogicalValidationResult) string {
	if strings.TrimSpace(result.Error) != "" {
		return result.Error
	}
	failed := make([]string, 0, len(result.Databases))
	for _, db := range result.Databases {
		if db.Valid {
			continue
		}
		name := db.Database
		if strings.TrimSpace(db.Error) != "" {
			name += ": " + db.Error
		}
		failed = append(failed, name)
	}
	if len(failed) == 0 {
		return ""
	}
	return "validation failed for " + strings.Join(failed, ", ")
}

func persistValidationResult(cfg Config, mode string, result LogicalValidationResult) {
	if strings.TrimSpace(result.RunID) == "" {
		return
	}
	_, err := updateRunRecord(cfg.RunLogPath, result.RunID, func(record *runRecord) {
		record.ValidationCheckedAt = time.Now().UTC()
		record.ValidationMode = mode
		if result.Valid {
			record.ValidationStatus = "success"
			record.ValidationError = ""
		} else {
			record.ValidationStatus = "failed"
			record.ValidationError = validationFailureSummary(result)
		}
		record.ValidationDatabases = append([]DatabaseValidationResult(nil), result.Databases...)
	})
	if err != nil {
		log.Printf("warning: failed to persist %s result for run=%s: %v", mode, result.RunID, err)
	}
}

func finalizeValidationResult(cfg Config, mode string, result LogicalValidationResult) {
	status := 0
	if result.Valid {
		status = 1
	}
	emitValidationMetrics(cfg, status)
	persistValidationResult(cfg, mode, result)
}

type tailWriter struct {
	buf []byte
}

func (tw *tailWriter) Write(p []byte) (n int, err error) {
	const keep = 256
	tw.buf = append(tw.buf, p...)
	if len(tw.buf) > keep {
		tw.buf = tw.buf[len(tw.buf)-keep:]
	}
	return len(p), nil
}

func ValidateLogicalRun(cfg Config, runID string) (LogicalValidationResult, error) {
	res := LogicalValidationResult{RunID: runID}

	run, err := ReadRunByID(cfg.RunLogPath, runID)
	if err != nil {
		return res, fmt.Errorf("read run error: %v", err)
	}
	if run == nil {
		return res, fmt.Errorf("run %q not found", runID)
	}
	if run.RunFolder == "" {
		res.Error = "No local run folder recorded for this backup"
		finalizeValidationResult(cfg, "logical validation", res)
		return res, nil
	}

	if _, err := os.Stat(run.RunFolder); os.IsNotExist(err) {
		res.Error = "Local backup files have been removed (retention period exceeded)"
		finalizeValidationResult(cfg, "logical validation", res)
		return res, nil
	}
	if len(run.Results) == 0 {
		res.Error = "No logical dump results recorded for this backup run"
		finalizeValidationResult(cfg, "logical validation", res)
		return res, nil
	}

	allValid := true
	for _, dbRes := range run.Results {
		dbPath := logicalArtifactPath(run.RunFolder, dbRes)
		dbValid, dbErr := validateLogicalArtifactForEngineWithEncryption(cfg.DBEngine, dbPath, dbRes.ArtifactSHA256, dbRes.SQLSHA256, cfg.EncryptionKey)
		errMsg := ""
		if dbErr != nil {
			errMsg = dbErr.Error()
		}
		res.Databases = append(res.Databases, DatabaseValidationResult{
			Database: dbRes.Name,
			Valid:    dbValid,
			Error:    errMsg,
		})
		if !dbValid {
			allValid = false
		}
	}

	res.Valid = allValid && len(run.Results) > 0

	finalizeValidationResult(cfg, "logical validation", res)
	return res, nil
}

func logicalArtifactPath(runFolder string, dbRes databaseResult) string {
	if base := filepath.Base(strings.TrimSpace(dbRes.OutputPath)); base != "" && base != "." {
		return filepath.Join(runFolder, base)
	}
	encrypted := filepath.Join(runFolder, dbRes.Name+".sql.gz.enc")
	if _, err := os.Stat(encrypted); err == nil {
		return encrypted
	}
	return filepath.Join(runFolder, dbRes.Name+".sql.gz")
}

func validateGzipSQLFile(path string, expectedSHA256 string) (bool, error) {
	return validateGzipSQLFileWithEncryption(path, expectedSHA256, "")
}

func validateGzipSQLFileWithEncryption(path string, expectedSHA256 string, encryptionKey string) (bool, error) {
	return validateGzipSQLFileForEngineWithEncryption("mysql", path, expectedSHA256, encryptionKey)
}

func dumpCompletionSignature(engine string) string {
	if strings.EqualFold(strings.TrimSpace(engine), "postgres") {
		return "-- PostgreSQL database dump complete"
	}
	return "-- Dump completed"
}

func validateGzipSQLFileForEngineWithEncryption(engine string, path string, expectedSHA256 string, encryptionKey string) (bool, error) {
	if strings.TrimSpace(expectedSHA256) != "" {
		hash, err := fileSHA256(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, fmt.Errorf("file missing")
			}
			return false, fmt.Errorf("artifact hash read error: %v", err)
		}
		if hash != expectedSHA256 {
			return false, fmt.Errorf("artifact sha256 mismatch: manifest=%s actual=%s", expectedSHA256, hash)
		}
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("file missing")
		}
		return false, err
	}
	defer file.Close()

	var source io.Reader = file
	if encryptedArtifactPath(path) {
		if strings.TrimSpace(encryptionKey) == "" {
			return false, fmt.Errorf("encrypted artifact requires BACKUP_ENCRYPTION_KEY")
		}
		decrypted, err := DecryptReader(file, encryptionKey)
		if err != nil {
			return false, err
		}
		source = decrypted
	}

	gz, err := gzip.NewReader(source)
	if err != nil {
		return false, fmt.Errorf("invalid gzip header: %v", err)
	}
	defer gz.Close()

	tw := &tailWriter{}
	if _, err := io.Copy(tw, gz); err != nil {
		return false, fmt.Errorf("gzip checksum or read error: %v", err)
	}

	tailStr := string(tw.buf)
	signature := dumpCompletionSignature(engine)
	if !strings.Contains(tailStr, signature) {
		return false, fmt.Errorf("missing %q signature at end of file", signature)
	}

	return true, nil
}

func validateLogicalArtifact(path string, expectedArtifactSHA256 string, expectedSQLSHA256 string) (bool, error) {
	return validateLogicalArtifactWithEncryption(path, expectedArtifactSHA256, expectedSQLSHA256, "")
}

func validateLogicalArtifactWithEncryption(path string, expectedArtifactSHA256 string, expectedSQLSHA256 string, encryptionKey string) (bool, error) {
	return validateLogicalArtifactForEngineWithEncryption("mysql", path, expectedArtifactSHA256, expectedSQLSHA256, encryptionKey)
}

func validateLogicalArtifactForEngineWithEncryption(engine string, path string, expectedArtifactSHA256 string, expectedSQLSHA256 string, encryptionKey string) (bool, error) {
	if strings.TrimSpace(expectedArtifactSHA256) != "" {
		hash, err := fileSHA256(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, fmt.Errorf("file missing")
			}
			return false, fmt.Errorf("artifact hash read error: %v", err)
		}
		if hash != expectedArtifactSHA256 {
			return false, fmt.Errorf("artifact sha256 mismatch: manifest=%s actual=%s", expectedArtifactSHA256, hash)
		}
	}
	if strings.TrimSpace(expectedSQLSHA256) != "" {
		hash, err := gzipPayloadSHA256WithEncryption(path, encryptionKey)
		if err != nil {
			if os.IsNotExist(err) {
				return false, fmt.Errorf("file missing")
			}
			return false, fmt.Errorf("sql payload hash read error: %v", err)
		}
		if hash != expectedSQLSHA256 {
			return false, fmt.Errorf("sql sha256 mismatch: manifest=%s actual=%s", expectedSQLSHA256, hash)
		}
	}
	return validateGzipSQLFileForEngineWithEncryption(engine, path, "", encryptionKey)
}

func FullRestoreValidation(cfg Config, runID string, progress func(string)) (LogicalValidationResult, error) {
	res := LogicalValidationResult{RunID: runID}

	if !cfg.RestoreTestEnabled {
		return res, fmt.Errorf("restore validation is disabled in configuration")
	}
	if cfg.RestoreTestHost == "" {
		return res, fmt.Errorf("RESTORE_TEST_HOST is not configured")
	}
	engine := strings.ToLower(strings.TrimSpace(cfg.DBEngine))
	if engine == "" {
		engine = "mysql"
	}
	restoreClientBin := cfg.MySQLBin
	if engine == "postgres" {
		restoreClientBin = "psql"
	}
	if _, err := exec.LookPath(restoreClientBin); err != nil {
		return res, fmt.Errorf("restore validation requires %s in PATH: %w", restoreClientBin, err)
	}

	run, err := ReadRunByID(cfg.RunLogPath, runID)
	if err != nil {
		return res, fmt.Errorf("read run error: %v", err)
	}
	if run == nil {
		return res, fmt.Errorf("run %q not found", runID)
	}
	if run.RunFolder == "" {
		res.Error = "No local run folder recorded for this backup"
		finalizeValidationResult(cfg, "sandbox restore test", res)
		return res, nil
	}

	if _, err := os.Stat(run.RunFolder); os.IsNotExist(err) {
		res.Error = "Local backup files have been removed (retention period exceeded)"
		finalizeValidationResult(cfg, "sandbox restore test", res)
		return res, nil
	}
	if len(run.Results) == 0 {
		res.Error = "No logical dump results recorded for this backup run"
		finalizeValidationResult(cfg, "sandbox restore test", res)
		return res, nil
	}

	allValid := true
	for _, dbRes := range run.Results {
		if progress != nil {
			progress(fmt.Sprintf("Testing restore for %s...", dbRes.Name))
		}

		dbValid, dbErr := testRestoreDatabase(cfg, run.RunFolder, dbRes)
		errMsg := ""
		if dbErr != nil {
			errMsg = dbErr.Error()
		}

		res.Databases = append(res.Databases, DatabaseValidationResult{
			Database: dbRes.Name,
			Valid:    dbValid,
			Error:    errMsg,
		})
		if !dbValid {
			allValid = false
		}
	}

	res.Valid = allValid && len(run.Results) > 0

	finalizeValidationResult(cfg, "sandbox restore test", res)
	return res, nil
}

type restoreTestClient struct {
	engine  string
	testCmd func(...string) *exec.Cmd
}

func (c restoreTestClient) query(dbName string, query string) *exec.Cmd {
	if c.engine == "postgres" {
		return c.testCmd("-d", dbName, "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", query)
	}
	return c.testCmd("-N", "-e", query)
}

func (c restoreTestClient) sampleQuery(dbName string, query string) *exec.Cmd {
	if c.engine == "postgres" {
		return c.testCmd("-d", dbName, "-X", "-A", "-t", "-F", "\t", "-v", "ON_ERROR_STOP=1", "-c", query)
	}
	return c.testCmd("--batch", "--raw", "--skip-column-names", "-e", query)
}

func (c restoreTestClient) importSQL(dbName string) *exec.Cmd {
	if c.engine == "postgres" {
		return c.testCmd("-d", dbName, "-X", "-v", "ON_ERROR_STOP=1")
	}
	return c.testCmd(dbName)
}

func queryNamesWithTestCmd(client restoreTestClient, dbName string, query string) ([]string, error) {
	cmd := client.query(dbName, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	names := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name != "" {
			names = append(names, name)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

func restoredSchemaFingerprint(client restoreTestClient, dbName string) (string, error) {
	queryMap := mysqlRestoreSchemaQueries(dbName)
	if client.engine == "postgres" {
		queryMap = postgresRestoreSchemaQueries()
	}
	sections := map[string][]string{}
	for key, query := range queryMap {
		names, err := queryNamesWithTestCmd(client, dbName, query)
		if err != nil {
			return "", fmt.Errorf("failed to read restored %s names: %v", key, err)
		}
		sections[key] = names
	}
	return buildSchemaFingerprint(sections), nil
}

func mysqlRestoreSchemaQueries(dbName string) map[string]string {
	return map[string]string{
		"table":   fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME", dbName),
		"view":    fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA='%s' ORDER BY TABLE_NAME", dbName),
		"trigger": fmt.Sprintf("SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='%s' ORDER BY TRIGGER_NAME", dbName),
		"routine": fmt.Sprintf("SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='%s' ORDER BY ROUTINE_NAME", dbName),
		"event":   fmt.Sprintf("SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA='%s' ORDER BY EVENT_NAME", dbName),
	}
}

func postgresRestoreSchemaQueries() map[string]string {
	return map[string]string{
		"table":   "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename",
		"view":    "SELECT viewname FROM pg_views WHERE schemaname='public' ORDER BY viewname",
		"trigger": "SELECT trigger_name FROM information_schema.triggers WHERE trigger_schema='public' ORDER BY trigger_name",
		"routine": "SELECT routine_name FROM information_schema.routines WHERE specific_schema='public' ORDER BY routine_name",
		"event":   "SELECT NULL WHERE false",
	}
}

func restoredBaseTableNamesQuery(engine string, dbName string) string {
	if engine == "postgres" {
		return "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"
	}
	return fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME", dbName)
}

func restoredExactRowCount(client restoreTestClient, dbName string) (int64, error) {
	tableNames, err := queryNamesWithTestCmd(client, dbName, restoredBaseTableNamesQuery(client.engine, dbName))
	if err != nil {
		return 0, fmt.Errorf("failed to read restored base table names: %v", err)
	}
	var total int64
	for _, tableName := range tableNames {
		query := restoredTableCountQuery(client.engine, dbName, tableName)
		cmd := client.query(dbName, query)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("failed to count restored rows for table=%s: %s", tableName, strings.TrimSpace(string(out)))
		}
		count, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid restored row count for table=%s: %v", tableName, err)
		}
		total += count
	}
	return total, nil
}

func restoredTableCountQuery(engine string, dbName string, tableName string) string {
	if engine == "postgres" {
		return "SELECT COUNT(*) FROM " + quoteIdentifier("postgres", "public") + "." + quoteIdentifier("postgres", tableName)
	}
	return "SELECT COUNT(*) FROM " + quoteIdentifier("mysql", dbName) + "." + quoteIdentifier("mysql", tableName)
}

func restoredExactTableRowCounts(client restoreTestClient, dbName string) (map[string]int64, int64, error) {
	tableNames, err := queryNamesWithTestCmd(client, dbName, restoredBaseTableNamesQuery(client.engine, dbName))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read restored base table names: %v", err)
	}
	counts := make(map[string]int64, len(tableNames))
	var total int64
	for _, tableName := range tableNames {
		query := restoredTableCountQuery(client.engine, dbName, tableName)
		cmd := client.query(dbName, query)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, 0, fmt.Errorf("failed to count restored rows for table=%s: %s", tableName, strings.TrimSpace(string(out)))
		}
		count, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid restored row count for table=%s: %v", tableName, err)
		}
		counts[tableName] = count
		total += count
	}
	return counts, total, nil
}

func restoredPrimaryKeyColumns(client restoreTestClient, dbName string, tableName string) ([]string, error) {
	if client.engine == "postgres" {
		query := `
SELECT a.attname
FROM pg_index i
JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
WHERE i.indrelid = 'public.` + strings.ReplaceAll(tableName, "'", "''") + `'::regclass
  AND i.indisprimary
ORDER BY array_position(i.indkey, a.attnum)
`
		return queryNamesWithTestCmd(client, dbName, query)
	}
	query := fmt.Sprintf(`
SELECT k.COLUMN_NAME
FROM information_schema.TABLE_CONSTRAINTS t
JOIN information_schema.KEY_COLUMN_USAGE k
  ON t.CONSTRAINT_NAME = k.CONSTRAINT_NAME
 AND t.TABLE_SCHEMA = k.TABLE_SCHEMA
 AND t.TABLE_NAME = k.TABLE_NAME
WHERE t.CONSTRAINT_TYPE = 'PRIMARY KEY'
  AND t.TABLE_SCHEMA = '%s'
  AND t.TABLE_NAME = '%s'
ORDER BY k.ORDINAL_POSITION
`, dbName, tableName)
	return queryNamesWithTestCmd(client, dbName, query)
}

func restoredSampleRowHashes(client restoreTestClient, dbName string, limit int) (map[string]string, error) {
	tableNames, err := queryNamesWithTestCmd(client, dbName, restoredBaseTableNamesQuery(client.engine, dbName))
	if err != nil {
		return nil, fmt.Errorf("failed to read restored base table names: %v", err)
	}
	hashes := map[string]string{}
	for _, tableName := range tableNames {
		pkCols, err := restoredPrimaryKeyColumns(client, dbName, tableName)
		if err != nil {
			return nil, fmt.Errorf("failed to read restored primary key columns table=%s: %v", tableName, err)
		}
		if len(pkCols) == 0 {
			continue
		}
		orderBy := make([]string, 0, len(pkCols))
		for _, col := range pkCols {
			orderBy = append(orderBy, quoteIdentifier(client.engine, col))
		}
		query := restoredSampleRowsQuery(client.engine, dbName, tableName, orderBy, limit)
		cmd := client.sampleQuery(dbName, query)
		hash, err := queryOutputSHA256(cmd, "restored sample data hash")
		if err != nil {
			return nil, fmt.Errorf("restored sample data hash failed table=%s: %v", tableName, err)
		}
		hashes[tableName] = hash
	}
	return hashes, nil
}

func restoredSampleRowsQuery(engine string, dbName string, tableName string, orderBy []string, limit int) string {
	if engine == "postgres" {
		return "SELECT * FROM " + quoteIdentifier("postgres", "public") + "." + quoteIdentifier("postgres", tableName) + " ORDER BY " + strings.Join(orderBy, ", ") + fmt.Sprintf(" LIMIT %d", limit)
	}
	return "SELECT * FROM " + quoteIdentifier("mysql", dbName) + "." + quoteIdentifier("mysql", tableName) + " ORDER BY " + strings.Join(orderBy, ", ") + fmt.Sprintf(" LIMIT %d", limit)
}

func testRestoreDatabase(cfg Config, runFolder string, dbRes DatabaseResult) (bool, error) {
	gzPath := logicalArtifactPath(runFolder, dbRes)
	if _, err := os.Stat(gzPath); os.IsNotExist(err) {
		return false, fmt.Errorf("dump file missing")
	}

	engine := strings.ToLower(strings.TrimSpace(cfg.DBEngine))
	if engine == "" {
		engine = "mysql"
	}
	tempDB := restoreTempDatabaseNameForEngine(engine, dbRes.Name)
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer restoreCancel()

	testCmd := restoreTestCommand(restoreCtx, cfg, engine)
	client := restoreTestClient{engine: engine, testCmd: testCmd}

	adminDatabase := ""
	if engine == "postgres" {
		adminDatabase = "postgres"
	}
	dropCmd := client.query(adminDatabase, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdentifier(engine, tempDB)))
	if out, err := dropCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("failed to drop temp db: %s", strings.TrimSpace(string(out)))
	}

	createCmd := client.query(adminDatabase, fmt.Sprintf("CREATE DATABASE %s", quoteIdentifier(engine, tempDB)))
	if out, err := createCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("failed to create temp db: %s", strings.TrimSpace(string(out)))
	}

	defer func() {
		cleanupCmd := client.query(adminDatabase, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdentifier(engine, tempDB)))
		_ = cleanupCmd.Run()
	}()

	importCmd := client.importSQL(tempDB)

	gzFile, err := os.Open(gzPath)
	if err != nil {
		return false, err
	}
	defer gzFile.Close()

	var source io.Reader = gzFile
	if encryptedArtifactPath(gzPath) {
		if strings.TrimSpace(cfg.EncryptionKey) == "" {
			return false, fmt.Errorf("encrypted artifact requires BACKUP_ENCRYPTION_KEY")
		}
		decrypted, err := DecryptReader(gzFile, cfg.EncryptionKey)
		if err != nil {
			return false, fmt.Errorf("decrypt artifact: %w", err)
		}
		source = decrypted
	}

	gzReader, err := gzip.NewReader(source)
	if err != nil {
		return false, err
	}
	defer gzReader.Close()

	importCmd.Stdin = gzReader
	if out, err := importCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("import failed: %s", string(out))
	}

	if len(dbRes.SelectedTables) > 0 {
		for _, name := range dbRes.SelectedTables {
			query := restoredSelectedObjectQuery(engine, tempDB, name)
			out, err := client.query(tempDB, query).CombinedOutput()
			if err != nil {
				return false, fmt.Errorf("failed to verify restored selected object %s: %s", name, string(out))
			}
			count, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
			if err != nil || count != 1 {
				return false, fmt.Errorf("selected object missing after restore: %s", name)
			}
		}
		return true, nil
	}

	tableCountCmd := client.query(tempDB, restoredBaseTableCountQuery(engine, tempDB))
	out, err := tableCountCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to count restored base tables: %s", string(out))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		valStr = "0"
	}
	tableCount, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid restored base table count: %v", err)
	}

	if tableCount != dbRes.TableCount {
		return false, fmt.Errorf("base table count mismatch: manifest=%d, restored=%d", dbRes.TableCount, tableCount)
	}

	viewCountCmd := client.query(tempDB, restoredViewCountQuery(engine, tempDB))
	out, err = viewCountCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to count restored views: %s", string(out))
	}

	valStr = strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		valStr = "0"
	}
	viewCount, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid restored view count: %v", err)
	}

	if viewCount != dbRes.ViewCount {
		return false, fmt.Errorf("view count mismatch: manifest=%d, restored=%d", dbRes.ViewCount, viewCount)
	}

	triggerCountCmd := client.query(tempDB, restoredTriggerCountQuery(engine, tempDB))
	out, err = triggerCountCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to count restored triggers: %s", string(out))
	}

	valStr = strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		valStr = "0"
	}
	triggerCount, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid restored trigger count: %v", err)
	}

	if triggerCount != dbRes.TriggerCount {
		return false, fmt.Errorf("trigger count mismatch: manifest=%d, restored=%d", dbRes.TriggerCount, triggerCount)
	}

	routineCountCmd := client.query(tempDB, restoredRoutineCountQuery(engine, tempDB))
	out, err = routineCountCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to count restored routines: %s", string(out))
	}

	valStr = strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		valStr = "0"
	}
	routineCount, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid restored routine count: %v", err)
	}

	if routineCount != dbRes.RoutineCount {
		return false, fmt.Errorf("routine count mismatch: manifest=%d, restored=%d", dbRes.RoutineCount, routineCount)
	}

	eventCountCmd := client.query(tempDB, restoredEventCountQuery(engine, tempDB))
	out, err = eventCountCmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to count restored events: %s", string(out))
	}

	valStr = strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		valStr = "0"
	}
	eventCount, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid restored event count: %v", err)
	}

	if eventCount != dbRes.EventCount {
		return false, fmt.Errorf("event count mismatch: manifest=%d, restored=%d", dbRes.EventCount, eventCount)
	}

	fingerprint, err := restoredSchemaFingerprint(client, tempDB)
	if err != nil {
		return false, err
	}
	if dbRes.SchemaFingerprint != "" && fingerprint != dbRes.SchemaFingerprint {
		return false, fmt.Errorf("schema fingerprint mismatch: manifest=%s, restored=%s", dbRes.SchemaFingerprint, fingerprint)
	}

	if dbRes.RowCountMode == "exact" {
		restoredTableRows, restoredRows, err := restoredExactTableRowCounts(client, tempDB)
		if err != nil {
			return false, err
		}
		if len(dbRes.TableRowCounts) > 0 {
			if len(restoredTableRows) != len(dbRes.TableRowCounts) {
				return false, fmt.Errorf("exact table row count set mismatch: manifest_tables=%d restored_tables=%d", len(dbRes.TableRowCounts), len(restoredTableRows))
			}
			for tableName, expectedCount := range dbRes.TableRowCounts {
				actualCount, ok := restoredTableRows[tableName]
				if !ok {
					return false, fmt.Errorf("exact row count missing restored table: %s", tableName)
				}
				if actualCount != expectedCount {
					return false, fmt.Errorf("exact row count mismatch table=%s manifest=%d restored=%d", tableName, expectedCount, actualCount)
				}
			}
		}
		if restoredRows != dbRes.RowCounts {
			return false, fmt.Errorf("exact row count mismatch: manifest=%d, restored=%d", dbRes.RowCounts, restoredRows)
		}
	}

	if len(dbRes.SampleRowHashes) > 0 && dbRes.SampleRowCount > 0 {
		restoredHashes, err := restoredSampleRowHashes(client, tempDB, dbRes.SampleRowCount)
		if err != nil {
			return false, err
		}
		if len(restoredHashes) != len(dbRes.SampleRowHashes) {
			return false, fmt.Errorf("sample row hash table set mismatch: manifest_tables=%d restored_tables=%d", len(dbRes.SampleRowHashes), len(restoredHashes))
		}
		for tableName, expectedHash := range dbRes.SampleRowHashes {
			actualHash, ok := restoredHashes[tableName]
			if !ok {
				return false, fmt.Errorf("sample row hash missing restored table: %s", tableName)
			}
			if actualHash != expectedHash {
				return false, fmt.Errorf("sample row hash mismatch table=%s manifest=%s restored=%s", tableName, expectedHash, actualHash)
			}
		}
	}

	return true, nil
}

func restoreTestCommand(ctx context.Context, cfg Config, engine string) func(...string) *exec.Cmd {
	if engine == "postgres" {
		return func(args ...string) *exec.Cmd {
			cmdArgs := []string{"-h", cfg.RestoreTestHost, "-p", cfg.RestoreTestPort, "-U", cfg.RestoreTestUser}
			cmdArgs = append(cmdArgs, args...)
			cmd := exec.CommandContext(ctx, "psql", cmdArgs...)
			cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.RestoreTestPass)
			return cmd
		}
	}

	return func(args ...string) *exec.Cmd {
		cmdArgs := []string{"-h", cfg.RestoreTestHost, "-P", cfg.RestoreTestPort, "-u", cfg.RestoreTestUser}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.CommandContext(ctx, cfg.MySQLBin, cmdArgs...)
		// Match the backup client: command-line passwords emit a warning that
		// pollutes CombinedOutput used for counts and sampled row hashes.
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.RestoreTestPass)
		return cmd
	}
}

func restoredSelectedObjectQuery(engine string, dbName string, objectName string) string {
	if engine == "postgres" {
		return "SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='" + strings.ReplaceAll(objectName, "'", "''") + "' AND c.relkind IN ('r','p','v','m','f')"
	}
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_NAME='%s'",
		strings.ReplaceAll(dbName, "'", "''"),
		strings.ReplaceAll(objectName, "'", "''"),
	)
}

func restoredBaseTableCountQuery(engine string, dbName string) string {
	if engine == "postgres" {
		return "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public'"
	}
	return fmt.Sprintf("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE'", dbName)
}

func restoredViewCountQuery(engine string, dbName string) string {
	if engine == "postgres" {
		return "SELECT COUNT(*) FROM pg_views WHERE schemaname='public'"
	}
	return fmt.Sprintf("SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA='%s'", dbName)
}

func restoredTriggerCountQuery(engine string, dbName string) string {
	if engine == "postgres" {
		return "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema='public'"
	}
	return fmt.Sprintf("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='%s'", dbName)
}

func restoredRoutineCountQuery(engine string, dbName string) string {
	if engine == "postgres" {
		return "SELECT COUNT(*) FROM information_schema.routines WHERE specific_schema='public'"
	}
	return fmt.Sprintf("SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='%s'", dbName)
}

func restoredEventCountQuery(engine string, dbName string) string {
	if engine == "postgres" {
		return "SELECT 0"
	}
	return fmt.Sprintf("SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='%s'", dbName)
}

func restoreTempDatabaseName(dbName string) string {
	return restoreTempDatabaseNameForEngine("mysql", dbName)
}

func restoreTempDatabaseNameForEngine(engine string, dbName string) string {
	const maxLen = 64
	maxNameLen := maxLen
	if engine == "postgres" {
		// PostgreSQL identifiers are limited to 63 bytes by default (NAMEDATALEN-1).
		maxNameLen = 63
	}
	base := strings.ToLower(strings.TrimSpace(dbName))
	if base == "" {
		base = "db"
	}
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, base)
	sanitized = strings.Trim(sanitized, "_")
	if sanitized == "" {
		sanitized = "db"
	}

	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(dbName))
	suffix := fmt.Sprintf("%08x", hasher.Sum32())
	prefix := "test_restore_"
	maxBaseLen := maxNameLen - len(prefix) - 1 - len(suffix)
	if maxBaseLen < 1 {
		maxBaseLen = 1
	}
	if len(sanitized) > maxBaseLen {
		sanitized = sanitized[:maxBaseLen]
	}
	return prefix + sanitized + "_" + suffix
}
