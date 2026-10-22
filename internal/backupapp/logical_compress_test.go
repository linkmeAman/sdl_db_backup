package backupapp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requirePigz(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("pigz")
	if err != nil {
		t.Skip("pigz not installed")
	}
	return path
}

func withPigzLookup(t *testing.T, lookup func() (string, error)) {
	t.Helper()
	original := lookPigz
	lookPigz = lookup
	t.Cleanup(func() { lookPigz = original })
}

func TestResolveLogicalCompressor(t *testing.T) {
	withPigzLookup(t, func() (string, error) { return "/usr/bin/pigz", nil })
	for value, want := range map[string]string{"": "pigz", "auto": "pigz", "pigz": "pigz", "gzip": "gzip", "bogus": "gzip"} {
		if got, _, err := resolveLogicalCompressor(config{LogicalCompressor: value}); err != nil || got != want {
			t.Fatalf("%q => %q %v, want %q", value, got, err, want)
		}
	}
	withPigzLookup(t, func() (string, error) { return "", errors.New("not found") })
	if got, _, err := resolveLogicalCompressor(config{LogicalCompressor: "auto"}); err != nil || got != "gzip" {
		t.Fatalf("auto without pigz must fall back to built-in gzip: %q %v", got, err)
	}
	if _, _, err := resolveLogicalCompressor(config{LogicalCompressor: "pigz"}); err == nil {
		t.Fatal("explicit pigz without the binary must fail before dumping")
	}
}

func TestLogicalCompressorsProduceIdenticalSQL(t *testing.T) {
	requirePigz(t)
	root := t.TempDir()
	dump := fakeMySQLDump(t, root, 8<<20)
	hashes := map[string]string{}
	for _, compressor := range []string{"gzip", "pigz"} {
		out := filepath.Join(root, compressor+".sql.gz")
		cfg := config{DBEngine: "mysql", MySQLDumpBin: dump, LogicalTimeoutPerDB: time.Minute, LogicalGzipLevel: 4, LogicalCompressor: compressor, LogicalCompressThreads: 4}
		if _, err := dumpDatabase(cfg, "d", out, nil, nil); err != nil {
			t.Fatalf("%s: %v", compressor, err)
		}
		sum, err := gzipPayloadSHA256(out)
		if err != nil {
			t.Fatalf("%s output not readable gzip: %v", compressor, err)
		}
		if ok, err := validateGzipSQLFileForEngineWithEncryption("mysql", out, "", ""); !ok || err != nil {
			t.Fatalf("%s output fails SQL validation: %v", compressor, err)
		}
		hashes[compressor] = sum
	}
	if hashes["gzip"] != hashes["pigz"] {
		t.Fatal("compressors produced different SQL payloads")
	}
}

func TestPigzReceivesLevelAndThreads(t *testing.T) {
	root := t.TempDir()
	argsFile := filepath.Join(root, "args")
	fake := filepath.Join(root, "pigz")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" > '"+argsFile+"'\nexec gzip -c\n"), 0o700)
	withPigzLookup(t, func() (string, error) { return fake, nil })
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 1024), LogicalTimeoutPerDB: time.Minute, LogicalGzipLevel: 4, LogicalCompressor: "pigz", LogicalCompressThreads: 3}
	if _, err := dumpDatabase(cfg, "d", filepath.Join(root, "d.sql.gz"), nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argsFile)
	args := " " + strings.TrimSpace(string(raw)) + " "
	if !strings.Contains(args, " -4 ") || !strings.Contains(args, " -p 3 ") || !strings.Contains(args, " -c ") {
		t.Fatalf("unexpected pigz arguments: %s", args)
	}
}

func TestPigzCrashFailsDumpWithoutPublishingArtifact(t *testing.T) {
	root := t.TempDir()
	fake := filepath.Join(root, "pigz")
	os.WriteFile(fake, []byte("#!/bin/sh\nhead -c 100 >/dev/null\necho broken >&2\nexit 3\n"), 0o700)
	withPigzLookup(t, func() (string, error) { return fake, nil })
	out := filepath.Join(root, "d.sql.gz")
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 4<<20), LogicalTimeoutPerDB: 30 * time.Second, LogicalCompressor: "pigz", LogicalCompressThreads: 2}
	assertDumpFailsCleanly(t, cfg, out)
}

func TestPigzPathMySQLDumpFailureDoesNotPublishArtifact(t *testing.T) {
	requirePigz(t)
	root := t.TempDir()
	bin := filepath.Join(root, "mysqldump")
	os.WriteFile(bin, []byte("#!/bin/sh\necho partial\nexit 1\n"), 0o700)
	cfg := config{DBEngine: "mysql", MySQLDumpBin: bin, LogicalTimeoutPerDB: 30 * time.Second, LogicalCompressor: "pigz", LogicalCompressThreads: 2}
	assertDumpFailsCleanly(t, cfg, filepath.Join(root, "d.sql.gz"))
}

func TestPigzPathDiskReserveFailureDoesNotHang(t *testing.T) {
	requirePigz(t)
	root := t.TempDir()
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 32<<20), LogicalTimeoutPerDB: 30 * time.Second, LogicalCompressor: "pigz", LogicalCompressThreads: 2, StorageReserveBytes: 1 << 62}
	assertDumpFailsCleanly(t, cfg, filepath.Join(root, "d.sql.gz"))
}

// A dump wrapper whose descendant keeps stderr open must not block cleanup
// forever after a failure (exec.Cmd.Wait otherwise waits for the pipe's EOF).
func TestDumpFailureWithLingeringDescendantDoesNotHang(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "mysqldump")
	os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60 &\nhead -c 2000000 /dev/zero | tr '\\0' 'x'\n"), 0o700)
	original := dumpPipeWaitDelay
	dumpPipeWaitDelay = time.Second
	t.Cleanup(func() { dumpPipeWaitDelay = original })
	for _, compressor := range []string{"gzip", "pigz"} {
		t.Run(compressor, func(t *testing.T) {
			if compressor == "pigz" {
				requirePigz(t)
			}
			cfg := config{DBEngine: "mysql", MySQLDumpBin: bin, LogicalTimeoutPerDB: 30 * time.Second, LogicalCompressor: compressor, LogicalCompressThreads: 2, StorageReserveBytes: 1 << 62}
			assertDumpFailsCleanly(t, cfg, filepath.Join(t.TempDir(), "d.sql.gz"))
		})
	}
}

func TestPigzPathEncryptedRoundTrip(t *testing.T) {
	requirePigz(t)
	root := t.TempDir()
	out := filepath.Join(root, "d.sql.gz.enc")
	cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 2<<20), LogicalTimeoutPerDB: time.Minute, LogicalCompressor: "pigz", LogicalCompressThreads: 2, EncryptionKey: testEncryptionKey}
	if _, err := dumpDatabase(cfg, "d", out, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := validateGzipSQLFileForEngineWithEncryption("mysql", out, "", testEncryptionKey); !ok || err != nil {
		t.Fatalf("encrypted pigz artifact invalid: %v", err)
	}
}

func TestPigzPathKeepsPITRCoordinate(t *testing.T) {
	requirePigz(t)
	root := t.TempDir()
	bin := filepath.Join(root, "mysqldump")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000001', SOURCE_LOG_POS=789;\"\necho 'CREATE TABLE t (id int);'\necho '-- Dump completed on fixture'\n"), 0o700)
	cfg := config{DBEngine: "mysql", MySQLDumpBin: bin, LogicalTimeoutPerDB: time.Minute, LogicalCompressor: "pigz", LogicalCompressThreads: 2, MySQLPITRMetadataEnabled: true}
	outcome, err := dumpDatabase(cfg, "d", filepath.Join(root, "d.sql.gz"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.pitrCoordinate == nil || outcome.pitrCoordinate.BinlogFile != "binlog.000001" || outcome.pitrCoordinate.BinlogPosition != 789 {
		t.Fatalf("binlog coordinate lost: %+v", outcome.pitrCoordinate)
	}
}

func TestLogicalCompressorConfig(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(env, []byte("DB_USER=u\nDB_PASS=p\n"), 0o600)
	cfg, e := LoadConfig(env)
	if e != nil || cfg.LogicalCompressor != "auto" || cfg.LogicalCompressThreads != 4 {
		t.Fatalf("defaults: %q %d %v", cfg.LogicalCompressor, cfg.LogicalCompressThreads, e)
	}
	cfg.LogicalCompressor = "gzip"
	cfg.LogicalCompressThreads = 2
	if e = SaveConfig(env, cfg); e != nil {
		t.Fatal(e)
	}
	if cfg, e = LoadConfig(env); e != nil || cfg.LogicalCompressor != "gzip" || cfg.LogicalCompressThreads != 2 {
		t.Fatalf("not persisted: %q %d %v", cfg.LogicalCompressor, cfg.LogicalCompressThreads, e)
	}
	for _, bad := range []func(*config){
		func(c *config) { c.LogicalCompressor = "zstd" },
		func(c *config) { c.LogicalCompressThreads = -1 },
		func(c *config) { c.LogicalCompressThreads = 65 },
	} {
		broken := cfg
		bad(&broken)
		if e = SaveConfig(env, broken); e == nil {
			t.Fatalf("invalid compressor settings saved: %q %d", broken.LogicalCompressor, broken.LogicalCompressThreads)
		}
	}
	// Callers predating these settings leave them zero: saved as the defaults.
	legacy := cfg
	legacy.LogicalCompressor, legacy.LogicalCompressThreads = "", 0
	if e = SaveConfig(env, legacy); e != nil {
		t.Fatal(e)
	}
	if cfg, e = LoadConfig(env); e != nil || cfg.LogicalCompressor != "auto" || cfg.LogicalCompressThreads != 4 {
		t.Fatalf("unset settings not saved as defaults: %q %d %v", cfg.LogicalCompressor, cfg.LogicalCompressThreads, e)
	}
}
