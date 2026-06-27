package backupapp

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

func mysqlCmdContext(ctx context.Context, cfg config, bin string, args ...string) *exec.Cmd {
	base := []string{"-h", cfg.DBHost, "-P", cfg.DBPort, "-u", cfg.DBUser}
	all := append(base, args...)
	cmd := exec.CommandContext(ctx, bin, all...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPass)
	return cmd
}

func postgresCmdContext(ctx context.Context, cfg config, bin string, args ...string) *exec.Cmd {
	// e.g. psql -h host -p port -U user
	base := []string{"-h", cfg.DBHost, "-p", cfg.DBPort, "-U", cfg.DBUser}
	all := append(base, args...)
	cmd := exec.CommandContext(ctx, bin, all...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.DBPass)
	return cmd
}

func dbCmdContext(ctx context.Context, cfg config, bin string, args ...string) *exec.Cmd {
	if cfg.DBEngine == "postgres" {
		return postgresCmdContext(ctx, cfg, bin, args...)
	}
	return mysqlCmdContext(ctx, cfg, bin, args...)
}

func listDatabases(cfg config) ([]string, error) {
	log.Printf("discovering databases for %s", cfg.DBEngine)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DiscoveryTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		cmd = dbCmdContext(ctx, cfg, "psql", "-A", "-t", "-c", "SELECT datname FROM pg_database WHERE datistemplate = false")
	} else {
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", "SHOW DATABASES")
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("list databases failed (%s): %s", classifyFailure(err, message), chooseFailureMessage(err, message))
	}

	var databases []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		dbName := strings.TrimSpace(scanner.Text())
		if dbName == "" || slices.Contains(systemDBs, dbName) {
			continue
		}
		databases = append(databases, dbName)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return databases, nil
}

func filterDatabases(cfg config, discovered []string) []string {
	if len(cfg.LogicalDatabases) == 0 {
		filtered := make([]string, 0, len(discovered))
		for _, db := range discovered {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(db)), "bk_") {
				continue
			}
			filtered = append(filtered, db)
		}
		return filtered
	}
	allowed := map[string]bool{}
	for _, db := range cfg.LogicalDatabases {
		allowed[db] = true
	}
	filtered := []string{}
	for _, db := range discovered {
		if allowed[db] {
			filtered = append(filtered, db)
		}
	}
	return filtered
}

func listTables(cfg config, dbName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.DiscoveryTimeout)
	defer cancel()
	query := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=%q AND TABLE_TYPE IN ('BASE TABLE','VIEW') ORDER BY TABLE_NAME", dbName)
	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("mysql table discovery failed database=%s (%s): %s", dbName, classifyFailure(err, message), chooseFailureMessage(err, message))
	}
	tables := []string{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		tableName := strings.TrimSpace(scanner.Text())
		if tableName != "" {
			tables = append(tables, tableName)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	brokenViews, err := discoverBrokenViews(cfg, dbName)
	if err != nil {
		log.Printf("warning: could not precheck views for database=%s while listing scope objects: %v", dbName, err)
		return tables, nil
	}
	if len(brokenViews) == 0 {
		return tables, nil
	}

	filtered := filterBrokenViewsFromObjects(tables, brokenViews)
	log.Printf("database=%s: excluded %d broken view(s) from discovery: %s", dbName, len(brokenViews), strings.Join(brokenViews, ", "))
	return filtered, nil
}

func selectedTablesForDatabase(cfg config, dbName string) []string {
	if len(cfg.LogicalTables) == 0 {
		return nil
	}
	return cfg.LogicalTables[dbName]
}

func filterRequestedTables(requested, discovered []string) ([]string, []string) {
	if len(requested) == 0 {
		return nil, nil
	}
	available := make([]string, 0, len(requested))
	missing := []string{}
	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, table := range discovered {
		discoveredSet[table] = struct{}{}
	}
	for _, table := range requested {
		if _, ok := discoveredSet[table]; ok {
			available = append(available, table)
			continue
		}
		missing = append(missing, table)
	}
	return available, missing
}

func resolveSelectedTablesForDatabase(cfg config, dbName string) ([]string, []string, error) {
	requested := selectedTablesForDatabase(cfg, dbName)
	if len(requested) == 0 {
		return nil, nil, nil
	}

	discovered, err := listTables(cfg, dbName)
	if err != nil {
		return requested, nil, err
	}

	available, missing := filterRequestedTables(requested, discovered)
	return available, missing, nil
}

func removePartialOutput(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("warning: could not remove partial backup %s: %v", path, err)
	}
}

func getDatabaseRowCount(cfg config, dbName string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		query := "SELECT COALESCE(sum(reltuples::bigint), 0) FROM pg_class WHERE relkind='r' AND relnamespace=(SELECT oid FROM pg_namespace WHERE nspname='public')"
		cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
	} else {
		query := fmt.Sprintf("SELECT COALESCE(SUM(TABLE_ROWS), 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE'", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return 0, fmt.Errorf("row count query failed: %s", chooseFailureMessage(err, message))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		return 0, nil
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid row count %q: %v", valStr, err)
	}
	return count, nil
}

func quoteIdentifier(engine string, ident string) string {
	if engine == "postgres" {
		return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func getDatabaseBaseTableNames(cfg config, dbName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		query := "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename"
		cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
	} else {
		query := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}
	return readNamesQuery(cmd, "base table names")
}

func getDatabaseExactRowCount(cfg config, dbName string) (int64, error) {
	tableNames, err := getDatabaseBaseTableNames(cfg, dbName)
	if err != nil {
		return 0, err
	}
	if len(tableNames) == 0 {
		return 0, nil
	}

	timeout := cfg.LogicalTimeoutPerDB
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var total int64
	for _, tableName := range tableNames {
		var query string
		if cfg.DBEngine == "postgres" {
			query = "SELECT COUNT(*) FROM " + quoteIdentifier(cfg.DBEngine, tableName)
			out, err := dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query).CombinedOutput()
			if err != nil {
				return 0, fmt.Errorf("exact row count query failed table=%s: %s", tableName, chooseFailureMessage(err, strings.TrimSpace(string(out))))
			}
			count, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid exact row count for table=%s: %v", tableName, err)
			}
			total += count
			continue
		}

		query = "SELECT COUNT(*) FROM " + quoteIdentifier(cfg.DBEngine, dbName) + "." + quoteIdentifier(cfg.DBEngine, tableName)
		out, err := dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query).CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("exact row count query failed table=%s: %s", tableName, chooseFailureMessage(err, strings.TrimSpace(string(out))))
		}
		count, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid exact row count for table=%s: %v", tableName, err)
		}
		total += count
	}
	return total, nil
}

func getDatabaseExactTableRowCounts(cfg config, dbName string) (map[string]int64, int64, error) {
	tableNames, err := getDatabaseBaseTableNames(cfg, dbName)
	if err != nil {
		return nil, 0, err
	}
	if len(tableNames) == 0 {
		return map[string]int64{}, 0, nil
	}

	timeout := cfg.LogicalTimeoutPerDB
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	counts := make(map[string]int64, len(tableNames))
	var total int64
	for _, tableName := range tableNames {
		var (
			query string
			out   []byte
			err   error
		)
		if cfg.DBEngine == "postgres" {
			query = "SELECT COUNT(*) FROM " + quoteIdentifier(cfg.DBEngine, tableName)
			out, err = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query).CombinedOutput()
		} else {
			query = "SELECT COUNT(*) FROM " + quoteIdentifier(cfg.DBEngine, dbName) + "." + quoteIdentifier(cfg.DBEngine, tableName)
			out, err = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query).CombinedOutput()
		}
		if err != nil {
			return nil, 0, fmt.Errorf("exact row count query failed table=%s: %s", tableName, chooseFailureMessage(err, strings.TrimSpace(string(out))))
		}
		count, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid exact row count for table=%s: %v", tableName, err)
		}
		counts[tableName] = count
		total += count
	}
	return counts, total, nil
}

func getPrimaryKeyColumns(cfg config, dbName string, tableName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	if cfg.DBEngine == "postgres" {
		query := `
SELECT a.attname
FROM pg_index i
JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
WHERE i.indrelid = '` + strings.ReplaceAll(tableName, "'", "''") + `'::regclass
  AND i.indisprimary
ORDER BY array_position(i.indkey, a.attnum)
`
		return readNamesQuery(dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query), "primary key columns")
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
	return readNamesQuery(dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query), "primary key columns")
}

func queryOutputSHA256(cmd *exec.Cmd, label string) (string, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return "", fmt.Errorf("%s query failed: %s", label, chooseFailureMessage(err, message))
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

func getDatabaseSampleRowHashes(cfg config, dbName string, limit int) (map[string]string, error) {
	if limit < 1 {
		return map[string]string{}, nil
	}
	tableNames, err := getDatabaseBaseTableNames(cfg, dbName)
	if err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	timeout := cfg.LogicalTimeoutPerDB
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for _, tableName := range tableNames {
		pkCols, err := getPrimaryKeyColumns(cfg, dbName, tableName)
		if err != nil {
			return nil, fmt.Errorf("primary key discovery failed table=%s: %v", tableName, err)
		}
		if len(pkCols) == 0 {
			continue
		}
		orderBy := make([]string, 0, len(pkCols))
		for _, col := range pkCols {
			orderBy = append(orderBy, quoteIdentifier(cfg.DBEngine, col))
		}
		var query string
		var cmd *exec.Cmd
		if cfg.DBEngine == "postgres" {
			query = "SELECT * FROM " + quoteIdentifier(cfg.DBEngine, tableName) + " ORDER BY " + strings.Join(orderBy, ", ") + fmt.Sprintf(" LIMIT %d", limit)
			cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-F", "\t", "-c", query)
		} else {
			query = "SELECT * FROM " + quoteIdentifier(cfg.DBEngine, dbName) + "." + quoteIdentifier(cfg.DBEngine, tableName) + " ORDER BY " + strings.Join(orderBy, ", ") + fmt.Sprintf(" LIMIT %d", limit)
			cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "--batch", "--raw", "--skip-column-names", "-e", query)
		}
		hash, err := queryOutputSHA256(cmd, "sample data hash")
		if err != nil {
			return nil, fmt.Errorf("sample data hash failed table=%s: %v", tableName, err)
		}
		hashes[tableName] = hash
	}
	return hashes, nil
}

func getDatabaseBaseTableCount(cfg config, dbName string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		query := "SELECT COUNT(*) FROM pg_tables WHERE schemaname='public'"
		cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
	} else {
		query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE'", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return 0, fmt.Errorf("base table count query failed: %s", chooseFailureMessage(err, message))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		return 0, nil
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid base table count %q: %v", valStr, err)
	}
	return count, nil
}

func getDatabaseViewCount(cfg config, dbName string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		query := "SELECT COUNT(*) FROM pg_views WHERE schemaname='public'"
		cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
	} else {
		query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA='%s'", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return 0, fmt.Errorf("view count query failed: %s", chooseFailureMessage(err, message))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		return 0, nil
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid view count %q: %v", valStr, err)
	}
	return count, nil
}

func getDatabaseTriggerCount(cfg config, dbName string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		query := "SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema='public'"
		cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
	} else {
		query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='%s'", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return 0, fmt.Errorf("trigger count query failed: %s", chooseFailureMessage(err, message))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		return 0, nil
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid trigger count %q: %v", valStr, err)
	}
	return count, nil
}

func getDatabaseRoutineCount(cfg config, dbName string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		query := "SELECT COUNT(*) FROM information_schema.routines WHERE specific_schema='public'"
		cmd = dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
	} else {
		query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='%s'", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return 0, fmt.Errorf("routine count query failed: %s", chooseFailureMessage(err, message))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		return 0, nil
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid routine count %q: %v", valStr, err)
	}
	return count, nil
}

func getDatabaseEventCount(cfg config, dbName string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		return 0, nil
	} else {
		query := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='%s'", dbName)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return 0, fmt.Errorf("event count query failed: %s", chooseFailureMessage(err, message))
	}

	valStr := strings.TrimSpace(string(out))
	if valStr == "NULL" || valStr == "" {
		return 0, nil
	}
	count, err := strconv.ParseInt(valStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid event count %q: %v", valStr, err)
	}
	return count, nil
}

func buildSchemaFingerprint(sections map[string][]string) string {
	lines := make([]string, 0)
	keys := make([]string, 0, len(sections))
	for key := range sections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := append([]string(nil), sections[key]...)
		sort.Strings(values)
		for _, value := range values {
			lines = append(lines, key+":"+value)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func gzipPayloadSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, gz); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readNamesQuery(cmd *exec.Cmd, label string) ([]string, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("%s query failed: %s", label, chooseFailureMessage(err, message))
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

func getDatabaseSchemaFingerprint(cfg config, dbName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	sections := map[string][]string{}
	if cfg.DBEngine == "postgres" {
		queryMap := map[string]string{
			"table":   "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename",
			"view":    "SELECT viewname FROM pg_views WHERE schemaname='public' ORDER BY viewname",
			"trigger": "SELECT trigger_name FROM information_schema.triggers WHERE trigger_schema='public' ORDER BY trigger_name",
			"routine": "SELECT routine_name FROM information_schema.routines WHERE specific_schema='public' ORDER BY routine_name",
		}
		for key, query := range queryMap {
			cmd := dbCmdContext(ctx, cfg, "psql", "-d", dbName, "-A", "-t", "-c", query)
			names, err := readNamesQuery(cmd, key+" fingerprint")
			if err != nil {
				return "", err
			}
			sections[key] = names
		}
		sections["event"] = nil
		return buildSchemaFingerprint(sections), nil
	}

	queryMap := map[string]string{
		"table":   fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='%s' AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME", dbName),
		"view":    fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA='%s' ORDER BY TABLE_NAME", dbName),
		"trigger": fmt.Sprintf("SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='%s' ORDER BY TRIGGER_NAME", dbName),
		"routine": fmt.Sprintf("SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='%s' ORDER BY ROUTINE_NAME", dbName),
		"event":   fmt.Sprintf("SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA='%s' ORDER BY EVENT_NAME", dbName),
	}
	for key, query := range queryMap {
		cmd := dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
		names, err := readNamesQuery(cmd, key+" fingerprint")
		if err != nil {
			return "", err
		}
		sections[key] = names
	}
	return buildSchemaFingerprint(sections), nil
}

func discoverBrokenViews(cfg config, dbName string) ([]string, error) {
	if cfg.DBEngine == "postgres" {
		// PostgreSQL handles views safely in pg_dump, skipping broken view check
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
	defer cancel()

	query := fmt.Sprintf("SELECT TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA=%q ORDER BY TABLE_NAME", dbName)
	cmd := dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("list views failed (%s): %s", classifyFailure(err, message), chooseFailureMessage(err, message))
	}

	var broken []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		viewName := strings.TrimSpace(scanner.Text())
		if viewName == "" {
			continue
		}

		vCtx, vCancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
		check := dbCmdContext(vCtx, cfg, cfg.MySQLBin, "-D", dbName, "-N", "-e", fmt.Sprintf("SHOW FIELDS FROM `%s`", viewName))
		checkOut, checkErr := check.CombinedOutput()
		vCancel()

		if checkErr != nil {
			msg := strings.TrimSpace(string(checkOut))
			category := classifyFailure(checkErr, msg)
			if category == "" {
				category = "view"
			}
			if strings.Contains(msg, "View") && strings.Contains(msg, "references invalid table(s)") {
				broken = append(broken, viewName)
			}
			log.Printf("warning: skipping view database=%s view=%s category=%s error=%s", dbName, viewName, category, chooseFailureMessage(checkErr, msg))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan views output: %w", err)
	}
	return broken, nil
}

func filterBrokenViewsFromObjects(objects []string, brokenViews []string) []string {
	if len(objects) == 0 || len(brokenViews) == 0 {
		return objects
	}
	broken := make(map[string]struct{}, len(brokenViews))
	for _, viewName := range brokenViews {
		broken[viewName] = struct{}{}
	}
	filtered := make([]string, 0, len(objects))
	for _, objectName := range objects {
		if _, ok := broken[objectName]; ok {
			continue
		}
		filtered = append(filtered, objectName)
	}
	return filtered
}

func buildMySQLDumpArgs(dbName string, tables []string, ignoreTables []string) []string {
	args := []string{
		"--single-transaction",
		"--quick",
		"--routines",
		"--triggers",
		"--events",
		"--no-tablespaces",
		"--set-gtid-purged=OFF",
		"--force",                       // continue on SQL errors (required for --ignore-error to take effect)
		"--ignore-error=1356,1449,1227", // skip broken views, missing definers, definer-privilege errors
		dbName,
	}
	args = append(args, tables...)
	for _, table := range ignoreTables {
		args = append(args, "--ignore-table="+dbName+"."+table)
	}
	return args
}

func buildPostgresDumpArgs(dbName string, tables []string, ignoreTables []string) []string {
	args := []string{
		"-d", dbName,
		"--clean",
		"--if-exists",
	}
	for _, table := range tables {
		args = append(args, "-t", table)
	}
	for _, table := range ignoreTables {
		args = append(args, "-T", table)
	}
	return args
}

func logLogicalTableSummary(dbName string, tables []string) {
	if len(tables) == 0 {
		return
	}
	const maxPreview = 8
	if len(tables) <= maxPreview {
		log.Printf("database=%s: dumping selected tables only (%d): %s", dbName, len(tables), strings.Join(tables, ", "))
		return
	}
	log.Printf("database=%s: dumping selected tables only (%d tables)", dbName, len(tables))
}

func normalizedLogicalParallelism(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

func normalizedLogicalGzipLevel(value int) int {
	if value < gzip.NoCompression || value > gzip.BestCompression {
		return gzip.BestSpeed
	}
	return value
}

func dumpDatabase(cfg config, dbName, outFile string, tables []string, ignoreTables []string) (int64, error) {
	log.Printf("starting dump for database=%s", dbName)
	logLogicalTableSummary(dbName, tables)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.LogicalTimeoutPerDB)
	defer cancel()

	file, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return 0, err
	}

	closeFile := func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Printf("warning: could not close output file %s: %v", outFile, closeErr)
		}
	}

	var baseWriter io.Writer = file
	if cfg.EncryptionKey != "" {
		encWriter, encErr := EncryptWriter(file, cfg.EncryptionKey)
		if encErr != nil {
			closeFile()
			return 0, fmt.Errorf("setup encryption: %w", encErr)
		}
		baseWriter = encWriter
	}

	counter := &countingWriter{writer: baseWriter}
	gz, err := gzip.NewWriterLevel(counter, normalizedLogicalGzipLevel(cfg.LogicalGzipLevel))
	if err != nil {
		closeFile()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("setup gzip writer: %w", err)
	}

	var cmd *exec.Cmd
	if cfg.DBEngine == "postgres" {
		args := buildPostgresDumpArgs(dbName, tables, ignoreTables)
		cmd = dbCmdContext(ctx, cfg, "pg_dump", args...)
	} else {
		args := buildMySQLDumpArgs(dbName, tables, ignoreTables)
		cmd = dbCmdContext(ctx, cfg, cfg.MySQLDumpBin, args...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closeFile()
		removePartialOutput(outFile)
		return 0, err
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		closeFile()
		removePartialOutput(outFile)
		return 0, err
	}

	if _, err := io.Copy(gz, stdout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = gz.Close()
		closeFile()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("stream mysqldump output: %w", err)
	}

	if err := gz.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		closeFile()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("finalize gzip: %w", err)
	}

	if err := file.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		removePartialOutput(outFile)
		return 0, fmt.Errorf("close gzip file: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		removePartialOutput(outFile)
		message := strings.TrimSpace(stderr.String())
		return 0, fmt.Errorf("mysqldump failed (%s): %s", classifyFailure(err, message), chooseFailureMessage(err, message))
	}

	// Log any warnings mysqldump emitted (e.g. objects skipped via --ignore-error).
	if warnings := strings.TrimSpace(stderr.String()); warnings != "" {
		for _, line := range strings.Split(warnings, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				if shouldLogDumpLine(line) {
					log.Printf("mysqldump warning database=%s: %s", dbName, line)
				}
			}
		}
	}

	if counter.bytes == 0 {
		removePartialOutput(outFile)
		return 0, fmt.Errorf("mysqldump produced empty output for database=%s", dbName)
	}

	log.Printf("completed dump for database=%s output=%s size_bytes=%d", dbName, outFile, counter.bytes)
	return counter.bytes, nil
}

func shouldLogDumpLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	switch {
	case strings.Contains(lower, "error"),
		strings.Contains(lower, "warning"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "couldn't find table"),
		strings.Contains(lower, "could not"):
		return true
	default:
		return false
	}
}

func dumpWithRetry(cfg config, dbName, outFile string, tables []string) databaseResult {
	started := time.Now()
	result := databaseResult{Name: dbName, Status: "failed"}
	brokenViews, precheckErr := discoverBrokenViews(cfg, dbName)
	if precheckErr != nil {
		log.Printf("warning: could not precheck views for database=%s: %v", dbName, precheckErr)
	}
	if len(brokenViews) > 0 {
		log.Printf("database=%s: precheck found %d broken view(s), excluding from dump: %s", dbName, len(brokenViews), strings.Join(brokenViews, ", "))
	}

	for attempt := 1; attempt <= cfg.RetryCount; attempt++ {
		result.Attempts = attempt
		if attempt == 1 {
			if count, err := getDatabaseBaseTableCount(cfg, dbName); err == nil {
				result.TableCount = count
			} else {
				log.Printf("warning: could not fetch base table count for database=%s: %v", dbName, err)
			}
			if count, err := getDatabaseViewCount(cfg, dbName); err == nil {
				result.ViewCount = count
			} else {
				log.Printf("warning: could not fetch view count for database=%s: %v", dbName, err)
			}
			if count, err := getDatabaseTriggerCount(cfg, dbName); err == nil {
				result.TriggerCount = count
			} else {
				log.Printf("warning: could not fetch trigger count for database=%s: %v", dbName, err)
			}
			if count, err := getDatabaseRoutineCount(cfg, dbName); err == nil {
				result.RoutineCount = count
			} else {
				log.Printf("warning: could not fetch routine count for database=%s: %v", dbName, err)
			}
			if count, err := getDatabaseEventCount(cfg, dbName); err == nil {
				result.EventCount = count
			} else {
				log.Printf("warning: could not fetch event count for database=%s: %v", dbName, err)
			}
			if fingerprint, err := getDatabaseSchemaFingerprint(cfg, dbName); err == nil {
				result.SchemaFingerprint = fingerprint
			} else {
				log.Printf("warning: could not fetch schema fingerprint for database=%s: %v", dbName, err)
			}
			if cfg.ExactRowCounts {
				if tableCounts, total, err := getDatabaseExactTableRowCounts(cfg, dbName); err == nil {
					result.TableRowCounts = tableCounts
					result.RowCounts = total
					result.RowCountMode = "exact"
				} else {
					log.Printf("warning: could not fetch exact row count for database=%s: %v; falling back to estimate", dbName, err)
					if count, fallbackErr := getDatabaseRowCount(cfg, dbName); fallbackErr == nil {
						result.RowCounts = count
						result.RowCountMode = "estimate"
					} else {
						log.Printf("warning: could not fetch estimated row count for database=%s: %v", dbName, fallbackErr)
					}
				}
			} else if count, err := getDatabaseRowCount(cfg, dbName); err == nil {
				result.RowCounts = count
				result.RowCountMode = "estimate"
			} else {
				log.Printf("warning: could not fetch row count for database=%s: %v", dbName, err)
			}
			if cfg.SampleDataChecks {
				if hashes, err := getDatabaseSampleRowHashes(cfg, dbName, cfg.SampleDataRows); err == nil {
					result.SampleRowHashes = hashes
					result.SampleRowCount = cfg.SampleDataRows
				} else {
					log.Printf("warning: could not capture sample data hashes for database=%s: %v", dbName, err)
				}
			}
		}

		sizeBytes, err := dumpDatabase(cfg, dbName, outFile, tables, brokenViews)
		if err == nil {
			artifactHash, hashErr := fileSHA256(outFile)
			if hashErr != nil {
				result.Error = fmt.Sprintf("compute artifact hash: %v", hashErr)
				result.ErrorCategory = "output"
				log.Printf("attempt %d/%d failed for database=%s category=%s err=%v", attempt, cfg.RetryCount, dbName, result.ErrorCategory, hashErr)
				if attempt == cfg.RetryCount {
					break
				}
				delay := retryDelay(cfg, attempt)
				log.Printf("retrying database=%s in %s (attempt %d/%d)", dbName, delay, attempt+1, cfg.RetryCount)
				time.Sleep(delay)
				continue
			}
			sqlHash, sqlHashErr := gzipPayloadSHA256(outFile)
			if sqlHashErr != nil {
				result.Error = fmt.Sprintf("compute sql payload hash: %v", sqlHashErr)
				result.ErrorCategory = "output"
				log.Printf("attempt %d/%d failed for database=%s category=%s err=%v", attempt, cfg.RetryCount, dbName, result.ErrorCategory, sqlHashErr)
				if attempt == cfg.RetryCount {
					break
				}
				delay := retryDelay(cfg, attempt)
				log.Printf("retrying database=%s in %s (attempt %d/%d)", dbName, delay, attempt+1, cfg.RetryCount)
				time.Sleep(delay)
				continue
			}
			result.Status = "success"
			result.OutputPath = outFile
			result.SizeBytes = sizeBytes
			result.ArtifactSHA256 = artifactHash
			result.SQLSHA256 = sqlHash
			result.Duration = time.Since(started).Round(time.Millisecond).String()
			return result
		}

		result.Error = err.Error()
		result.ErrorCategory = classifyFailure(err, err.Error())
		log.Printf("attempt %d/%d failed for database=%s category=%s err=%v", attempt, cfg.RetryCount, dbName, result.ErrorCategory, err)

		if attempt == cfg.RetryCount || !shouldRetry(result.ErrorCategory) {
			break
		}

		delay := retryDelay(cfg, attempt)
		log.Printf("retrying database=%s in %s (attempt %d/%d)", dbName, delay, attempt+1, cfg.RetryCount)
		time.Sleep(delay)
	}

	result.Duration = time.Since(started).Round(time.Millisecond).String()
	return result
}

func retryDelay(cfg config, attempt int) time.Duration {
	delay := cfg.RetryBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= cfg.RetryMaxDelay {
			return cfg.RetryMaxDelay
		}
	}
	if delay > cfg.RetryMaxDelay {
		return cfg.RetryMaxDelay
	}
	return delay
}

func shouldRetry(category string) bool {
	switch category {
	case "auth", "permission", "disk", "view", "definer", "schema", "config", "binary":
		return false
	default:
		return true
	}
}

func classifyFailure(err error, detail string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	text := strings.ToLower(strings.TrimSpace(detail + " " + err.Error()))
	switch {
	case strings.Contains(text, "access denied"):
		return "auth"
	case strings.Contains(text, "permission denied"):
		return "permission"
	case strings.Contains(text, "no space left on device"), strings.Contains(text, "disk full"):
		return "disk"
	case strings.Contains(text, "references invalid table"), strings.Contains(text, "1356"):
		return "view"
	case strings.Contains(text, "can't connect"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "server has gone away"), strings.Contains(text, "connection reset"),
		strings.Contains(text, "no route to host"):
		return "connect"
	case strings.Contains(text, "deadline exceeded"), strings.Contains(text, "timed out"),
		strings.Contains(text, "error 3024"), strings.Contains(text, "max_execution_time"):
		return "timeout"
	case strings.Contains(text, "unknown variable"), strings.Contains(text, "unknown option"):
		return "config"
	case strings.Contains(text, "executable file not found"):
		return "binary"
	case strings.Contains(text, "user specified as a definer"), strings.Contains(text, "1449"):
		return "definer"
	case strings.Contains(text, "doesn't exist"), strings.Contains(text, "1146"):
		return "schema"
	case strings.Contains(text, "max_allowed_packet"), strings.Contains(text, "got a packet bigger"), strings.Contains(text, "1153"):
		return "config"
	default:
		return "command"
	}
}

func chooseFailureMessage(err error, detail string) string {
	trimmed := strings.TrimSpace(detail)
	if trimmed != "" {
		return trimmed
	}
	return err.Error()
}

func appendRunRecord(path string, record runRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer file.Close()

	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}

func rewriteRunRecords(path string, runs []runRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	cleanupTemp := func() {
		_ = os.Remove(tempPath)
	}

	for _, run := range runs {
		encoded, err := json.Marshal(run)
		if err != nil {
			_ = tempFile.Close()
			cleanupTemp()
			return err
		}
		if _, err := tempFile.Write(append(encoded, '\n')); err != nil {
			_ = tempFile.Close()
			cleanupTemp()
			return err
		}
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		cleanupTemp()
		return err
	}
	if err := tempFile.Close(); err != nil {
		cleanupTemp()
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanupTemp()
		return err
	}
	return os.Chmod(path, 0o640)
}

func updateRunRecord(path, runID string, apply func(*runRecord)) (runRecord, error) {
	runs, err := ReadRunHistory(path)
	if err != nil {
		return runRecord{}, err
	}
	if len(runs) == 0 {
		return runRecord{}, fmt.Errorf("run %q not found", runID)
	}

	found := -1
	records := make([]runRecord, len(runs))
	for i, run := range runs {
		records[i] = run
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].RunID == runID {
			found = i
			break
		}
	}
	if found < 0 {
		return runRecord{}, fmt.Errorf("run %q not found", runID)
	}

	apply(&records[found])
	if err := rewriteRunRecords(path, records); err != nil {
		return runRecord{}, err
	}
	if strings.TrimSpace(records[found].RunFolder) != "" {
		if _, err := os.Stat(records[found].RunFolder); err == nil {
			if err := writeManifest(records[found].RunFolder, records[found]); err != nil {
				return records[found], err
			}
		}
	}
	return records[found], nil
}

func writeManifest(runFolder string, record runRecord) error {
	manifestPath := filepath.Join(runFolder, "manifest.json")
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(encoded, '\n'), 0o640)
}

func finalizeRun(cfg config, record *runRecord, startedAt time.Time) int {
	if record.Duration == "" {
		record.Duration = time.Since(startedAt).Round(time.Millisecond).String()
	}

	if record.Status == "skipped" {
		log.Printf("backup summary status=skipped duration=%s", record.Duration)
		return 0
	}

	if record.RunFolder != "" {
		if err := writeManifest(record.RunFolder, *record); err != nil {
			log.Printf("warning: failed to write manifest for run=%s: %v", record.RunID, err)
		}
	}
	if err := appendRunRecord(cfg.RunLogPath, *record); err != nil {
		log.Printf("warning: failed to append run record %s: %v", cfg.RunLogPath, err)
	}

	log.Printf(
		"backup summary status=%s total=%d success=%d failed=%d duration=%s",
		record.Status,
		record.DatabasesTotal,
		record.DatabasesSucceeded,
		record.DatabasesFailed,
		record.Duration,
	)
	if record.FailureReason != "" {
		log.Printf("backup failure reason: %s", record.FailureReason)
	}
	if record.CleanupError != "" {
		log.Printf("backup cleanup issue: %s", record.CleanupError)
	}

	if record.Status == "success" {
		return 0
	}
	return 1
}

// tryDisableMaxExecutionTime attempts to set @@GLOBAL.max_execution_time=0 so
// mysqldump SELECT queries are not killed mid-table. Returns a restore function.
// If the user lacks SYSTEM_VARIABLES_ADMIN, logs a warning and returns a no-op.
func tryDisableMaxExecutionTime(cfg config) func() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := mysqlCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-s", "-e", "SELECT @@GLOBAL.max_execution_time")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("max_execution_time: could not read current value: %v", strings.TrimSpace(string(out)))
		return func() {}
	}
	currentVal := strings.TrimSpace(string(out))
	if currentVal == "0" || currentVal == "" {
		log.Printf("max_execution_time: already 0, dumps will not be interrupted")
		return func() {}
	}

	log.Printf("max_execution_time: server value is %s ms — attempting to disable for this backup run", currentVal)

	setCtx, setCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer setCancel()
	if setErr := mysqlCmdContext(setCtx, cfg, cfg.MySQLBin, "-N", "-e", "SET GLOBAL max_execution_time=0").Run(); setErr != nil {
		log.Printf("warning: cannot disable max_execution_time (user lacks SYSTEM_VARIABLES_ADMIN?): %v", setErr)
		log.Printf("warning: large-table dumps may still hit the %s ms server timeout", currentVal)
		return func() {}
	}
	log.Printf("max_execution_time: disabled (0) for backup run; will restore to %s ms afterward", currentVal)

	return func() {
		rCtx, rCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rCancel()
		restoreSQL := fmt.Sprintf("SET GLOBAL max_execution_time=%s", currentVal)
		if rErr := mysqlCmdContext(rCtx, cfg, cfg.MySQLBin, "-N", "-e", restoreSQL).Run(); rErr != nil {
			log.Printf("warning: could not restore max_execution_time to %s ms: %v", currentVal, rErr)
		} else {
			log.Printf("max_execution_time: restored to %s ms", currentVal)
		}
	}
}
