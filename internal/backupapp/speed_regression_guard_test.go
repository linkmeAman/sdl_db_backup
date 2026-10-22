package backupapp

// Regression guards for the backup speed work (verification cache and parallel
// compression). They use only pre-existing behaviour and must pass both before
// and after those changes.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMySQLDump writes a mysqldump stand-in emitting roughly size bytes of SQL
// followed by the completion marker. Like the real mysqldump it is a single
// process (exec awk), so killing it closes its output pipes.
func fakeMySQLDump(t *testing.T, root string, size int) string {
	t.Helper()
	bin := filepath.Join(root, "mysqldump")
	script := fmt.Sprintf("#!/bin/sh\nexec awk -v n=%d 'BEGIN { print \"CREATE TABLE `t` (id int);\"; s = \"INSERT INTO `t` VALUES (1),(2),(3),(4),(5),(6),(7),(8),(9);\"; while (c < n) { print s; c += length(s) + 1 }; print \"-- Dump completed on fixture\" }'\n", size)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

// assertDumpFailsCleanly requires a failed dump to return promptly, leave no
// final artifact name behind and preserve the partial output for inspection.
func assertDumpFailsCleanly(t *testing.T, cfg config, out string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := dumpDatabase(cfg, "d", out, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed dump reported success")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("dump pipeline hung after a failure")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("final artifact published after failure")
	}
	if files, _ := filepath.Glob(out + ".incomplete-*"); len(files) != 1 {
		t.Fatalf("partial output not preserved: %v", files)
	}
}

func TestGuardDumpRoundTripsSQL(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "d.sql.gz")
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 4<<20), LogicalTimeoutPerDB: time.Minute, LogicalGzipLevel: 4}
	if _, err := dumpDatabase(cfg, "d", out, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := validateGzipSQLFileForEngineWithEncryption("mysql", out, "", ""); !ok || err != nil {
		t.Fatalf("dump output invalid: %v", err)
	}
	info, _ := os.Stat(out)
	if info.Size() == 0 || info.Size() > 1<<20 {
		t.Fatalf("output not compressed: %d bytes", info.Size())
	}
}

func TestGuardEncryptedDumpRoundTrips(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "d.sql.gz.enc")
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 1<<20), LogicalTimeoutPerDB: time.Minute, EncryptionKey: testEncryptionKey}
	if _, err := dumpDatabase(cfg, "d", out, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := validateGzipSQLFileForEngineWithEncryption("mysql", out, "", testEncryptionKey); !ok || err != nil {
		t.Fatalf("encrypted dump invalid: %v", err)
	}
	raw, _ := os.ReadFile(out)
	if strings.Contains(string(raw), "CREATE TABLE") {
		t.Fatal("plaintext SQL in encrypted artifact")
	}
}

func TestGuardMySQLDumpFailureDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "mysqldump")
	os.WriteFile(bin, []byte("#!/bin/sh\necho partial\nexit 1\n"), 0o700)
	assertDumpFailsCleanly(t, config{DBEngine: "mysql", MySQLDumpBin: bin, LogicalTimeoutPerDB: 30 * time.Second}, filepath.Join(root, "d.sql.gz"))
}

func TestGuardDiskReserveFailureDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 8<<20), LogicalTimeoutPerDB: 30 * time.Second, StorageReserveBytes: 1 << 62}
	assertDumpFailsCleanly(t, cfg, filepath.Join(root, "d.sql.gz"))
}

func TestGuardDumpKeepsPITRCoordinate(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "mysqldump")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000001', SOURCE_LOG_POS=789;\"\necho 'CREATE TABLE t (id int);'\necho '-- Dump completed on fixture'\n"), 0o700)
	cfg := config{DBEngine: "mysql", MySQLDumpBin: bin, LogicalTimeoutPerDB: time.Minute, MySQLPITRMetadataEnabled: true}
	outcome, err := dumpDatabase(cfg, "d", filepath.Join(root, "d.sql.gz"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.pitrCoordinate == nil || outcome.pitrCoordinate.BinlogFile != "binlog.000001" || outcome.pitrCoordinate.BinlogPosition != 789 {
		t.Fatalf("binlog coordinate lost: %+v", outcome.pitrCoordinate)
	}
}

// Repeated preflights must reach the same retention outcome and keep rejecting
// corrupted protected points, whatever verification shortcuts exist.
func TestGuardRepeatedPreflightOutcomeAndCorruptionDetection(t *testing.T) {
	cfg, v, m, points, _ := storagePoints(t, 6)
	cfg.StorageMaxLocalBackups = 4
	m.cfg = cfg
	v.available = 9000
	if _, e := m.ensure(context.Background(), "", true, false); e != nil {
		t.Fatal(e)
	}
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 4 {
		t.Fatalf("first preflight count=%d", count)
	}
	next := storageFixture(t, cfg.BackupDir, "2026-10-07_12-00-00", 6)
	points[next.RunID] = next
	if _, e := storageVirtualManager(cfg, v, points).ensure(context.Background(), "", true, false); e != nil {
		t.Fatal(e)
	}
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 4 {
		t.Fatalf("second preflight count=%d", count)
	}
	for day := 4; day <= 7; day++ {
		assertPointPresent(t, points[fmt.Sprintf("2026-10-%02d_12-00-00", day)])
	}
	victim := points["2026-10-06_12-00-00"]
	if err := os.WriteFile(victim.Results[0].OutputPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := m.local(cfg, logicalStorageRecord(victim), victim.RunFolder); e == nil {
		t.Fatal("corrupted protected point still verifies")
	}
}
