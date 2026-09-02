package backupapp

import "testing"

func TestBuildMySQLDumpArgsWithPITRAddsSourceDataComment(t *testing.T) {
	args := buildMySQLDumpArgsWithPITR("app_db", nil, nil, true)
	if !containsString(args, "--source-data=2") {
		t.Fatalf("expected source-data option, got %v", args)
	}
	without := buildMySQLDumpArgs("app_db", nil, nil)
	if containsString(without, "--source-data=2") {
		t.Fatalf("did not expect source-data without PITR metadata enabled, got %v", without)
	}
}

func TestParseMySQLSourceDataCoordinate(t *testing.T) {
	for _, input := range []string{
		"-- CHANGE MASTER TO MASTER_LOG_FILE='mysql-bin.000123', MASTER_LOG_POS=456;\n",
		"-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000001', SOURCE_LOG_POS=789;\n",
	} {
		coordinate := parseMySQLSourceDataCoordinate(input)
		if coordinate == nil || coordinate.BinlogFile == "" || coordinate.BinlogPosition == 0 {
			t.Fatalf("expected coordinate from %q, got %#v", input, coordinate)
		}
		if coordinate.Method != "mysqldump_source_data" {
			t.Fatalf("unexpected coordinate method: %#v", coordinate)
		}
	}
	if coordinate := parseMySQLSourceDataCoordinate("-- no source coordinate\n"); coordinate != nil {
		t.Fatalf("expected no coordinate, got %#v", coordinate)
	}
}

func TestParseMySQLBinaryLogStatus(t *testing.T) {
	file, position, gtidSet, err := parseMySQLBinaryLogStatus("mysql-bin.000042\t12345\t\t\tuuid:1-99\n")
	if err != nil {
		t.Fatal(err)
	}
	if file != "mysql-bin.000042" || position != 12345 || gtidSet != "uuid:1-99" {
		t.Fatalf("unexpected status: file=%q position=%d gtid=%q", file, position, gtidSet)
	}
	if _, _, _, err := parseMySQLBinaryLogStatus("mysql-bin.000042\tnot-a-number\n"); err == nil {
		t.Fatal("expected invalid position error")
	}
}

func TestParseMySQLServerIdentity(t *testing.T) {
	uuid, version, gtidSet, err := parseMySQLServerIdentity("server-uuid\t8.0.42\tserver-uuid:1-10\n")
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "server-uuid" || version != "8.0.42" || gtidSet != "server-uuid:1-10" {
		t.Fatalf("unexpected identity: uuid=%q version=%q gtid=%q", uuid, version, gtidSet)
	}
}

func TestParseMySQLServerIdentityAllowsEmptyGTIDSet(t *testing.T) {
	uuid, version, gtidSet, err := parseMySQLServerIdentity("server-uuid\t8.0.42\t\n")
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "server-uuid" || version != "8.0.42" || gtidSet != "" {
		t.Fatalf("unexpected identity: uuid=%q version=%q gtid=%q", uuid, version, gtidSet)
	}
}

func TestPITRRequiredEnablesMetadataCapture(t *testing.T) {
	cfg := loadConfigFromValues(map[string]string{
		"BACKUP_MYSQL_PITR_METADATA_REQUIRED": "true",
	})
	if !cfg.MySQLPITRMetadataRequired || !cfg.MySQLPITRMetadataEnabled {
		t.Fatalf("required PITR metadata must enable capture, got %+v", cfg)
	}
}

func TestRequiredMySQLPITRMetadataRejectsPostgres(t *testing.T) {
	cfg := config{DBUser: "backup", DBEngine: "postgres", MySQLPITRMetadataRequired: true}
	if err := validatePrerequisites(cfg, false); err == nil {
		t.Fatal("expected required MySQL PITR metadata to be rejected for postgres")
	}
}

func TestSafeRemoteAndPhysicalDefaultsRequireExplicitOptIn(t *testing.T) {
	cfg := loadConfigFromValues(map[string]string{})
	if cfg.LogicalS3UploadEnabled || cfg.PhysicalEnabled || cfg.PhysicalS3UploadEnabled {
		t.Fatalf("remote and physical backups must default to disabled, got logical_s3=%t physical=%t physical_s3=%t", cfg.LogicalS3UploadEnabled, cfg.PhysicalEnabled, cfg.PhysicalS3UploadEnabled)
	}
	if !cfg.RequireRemoteCompletion {
		t.Fatal("recovery-grade remote completion must default to required")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
