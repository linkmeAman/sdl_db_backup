package backupapp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// One mysqldump process opens one consistent transaction for all selected InnoDB
// databases. DDL is not transactionally snapshotted: operators must exclude it.
func captureGlobalLogical(cfg config, folder string, databases []string) ([]databaseResult, error) {
	if cfg.DBEngine != "mysql" || len(databases) == 0 || len(cfg.LogicalTables) > 0 {
		return nil, fmt.Errorf("global consistency requires MySQL, an explicit database inventory and no table scope")
	}
	guard, e := acquireGlobalDDLGuard(cfg)
	if e != nil {
		return nil, e
	}
	defer guard.Close()
	cfg.LogicalDatabases = databases
	results := make([]databaseResult, len(databases))
	for i, name := range databases {
		broken, e := discoverBrokenViews(cfg, name)
		if e != nil {
			return nil, e
		}
		if len(broken) > 0 {
			return nil, fmt.Errorf("global capture refused: %s has invalid views: %s", name, strings.Join(broken, ","))
		}
		q := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=%q AND TABLE_TYPE='BASE TABLE' AND ENGINE <> 'InnoDB'", name)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.PreflightTimeout)
		names, e := readNamesQuery(dbCmdContext(ctx, cfg, cfg.MySQLBin, "-N", "-e", q), "transactional engine inventory")
		cancel()
		if e != nil {
			return nil, e
		}
		if len(names) != 1 || names[0] != "0" {
			return nil, fmt.Errorf("global transaction cannot guarantee consistency for non-InnoDB tables in %s", name)
		}
		r := databaseResult{Name: name, Status: "failed"}
		r.SchemaFingerprint, e = getDatabaseSchemaFingerprint(cfg, name)
		if e != nil {
			return nil, e
		}
		for target, f := range map[*int64]func(config, string) (int64, error){&r.TableCount: getDatabaseBaseTableCount, &r.ViewCount: getDatabaseViewCount, &r.TriggerCount: getDatabaseTriggerCount, &r.RoutineCount: getDatabaseRoutineCount, &r.EventCount: getDatabaseEventCount} {
			*target, e = f(cfg, name)
			if e != nil {
				return nil, e
			}
		}
		inventory, e := getDatabaseObjectInventory(cfg, name)
		if e != nil {
			return nil, e
		}
		r.ExpectedObjects = inventory
		results[i] = r
	}
	ext := ".sql.gz"
	if cfg.EncryptionKey != "" {
		ext += ".enc"
	}
	path := filepath.Join(folder, "all-databases"+ext)
	captured := dumpWithRetry(cfg, databases[0], path, nil)
	if e := guard.Check(); e != nil {
		return nil, e
	}
	for i, r := range results {
		r.Status = captured.Status
		r.Attempts = captured.Attempts
		r.Duration = captured.Duration
		r.Error = captured.Error
		r.ErrorCategory = captured.ErrorCategory
		r.ErrorCode = captured.ErrorCode
		r.OutputPath = captured.OutputPath
		r.SizeBytes = captured.SizeBytes
		r.ArtifactSHA256 = captured.ArtifactSHA256
		r.SQLSHA256 = captured.SQLSHA256
		r.MySQLPITRCoordinate = captured.MySQLPITRCoordinate
		after, e := getDatabaseSchemaFingerprint(cfg, r.Name)
		if e != nil || after != r.SchemaFingerprint {
			r.Status = "failed"
			r.Error = "schema inventory changed during global capture; DDL exclusion required"
			r.ErrorCategory = "schema"
		}
		results[i] = r
	}
	return results, nil
}
