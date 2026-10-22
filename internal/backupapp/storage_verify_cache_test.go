package backupapp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countLogicalArtifactChecks counts expensive full artifact reads (hash, gzip
// decode and object inventory parse) performed by local capture verification.
func countLogicalArtifactChecks(t *testing.T) *int {
	t.Helper()
	calls := 0
	original := logicalArtifactCheck
	logicalArtifactCheck = func(cfg config, path string, d databaseResult) (map[string]map[string][]string, error) {
		calls++
		return original(cfg, path, d)
	}
	t.Cleanup(func() { logicalArtifactCheck = original })
	return &calls
}

func shiftStorageVerifyClock(t *testing.T, offset time.Duration) {
	t.Helper()
	original := storageVerifyNow
	storageVerifyNow = func() time.Time { return time.Now().Add(offset) }
	t.Cleanup(func() { storageVerifyNow = original })
}

func verifyCacheFixture(t *testing.T) (config, runRecord) {
	t.Helper()
	root := t.TempDir()
	cfg := storageTestConfig(t, root)
	cfg.StorageVerifyCacheTTL = 24 * time.Hour
	return cfg, storageFixture(t, root, "2026-10-01_12-00-00", 6)
}

func TestStorageVerifyCacheSkipsUnchangedArtifacts(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if *calls != 6 {
		t.Fatalf("first verification must read every artifact: %d", *calls)
	}
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if *calls != 6 {
		t.Fatalf("unchanged artifacts re-read: %d", *calls)
	}
}

func TestStorageVerifyCacheSeededByFullVerification(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCapture(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if *calls != 6 {
		t.Fatalf("post-capture verification did not seed the cache: %d", *calls)
	}
}

func TestStorageVerifyCacheNeverShortcutsFullVerification(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	for i := 0; i < 2; i++ {
		if e := verifyLogicalCapture(cfg, r, r.RunFolder); e != nil {
			t.Fatal(e)
		}
	}
	if *calls != 12 {
		t.Fatalf("upload/retention/post-capture checks must stay full: %d", *calls)
	}
}

func TestStorageVerifyCacheDetectsRewrittenArtifactWithRestoredMtime(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	path := r.Results[0].OutputPath
	info, _ := os.Stat(path)
	time.Sleep(10 * time.Millisecond)
	if e := os.WriteFile(path, []byte(strings.Repeat("x", int(info.Size()))), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := os.Chtimes(path, info.ModTime(), info.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e == nil {
		t.Fatal("corrupted artifact accepted from cache")
	}
	if *calls <= 6 {
		t.Fatal("changed artifact was not re-read")
	}
}

func TestStorageVerifyCacheDetectsReplacedFile(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	path := r.Results[0].OutputPath
	data, _ := os.ReadFile(path)
	replacement := path + ".replacement"
	if e := os.WriteFile(replacement, data, 0o600); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(replacement, path); e != nil {
		t.Fatal(e)
	}
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if *calls != 7 {
		t.Fatalf("replaced inode must be re-read exactly once: %d", *calls)
	}
}

func TestStorageVerifyCacheMissesWhenManifestClaimsChange(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	r.Results[0].SQLSHA256 = strings.Repeat("0", 64)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e == nil {
		t.Fatal("cache satisfied a different manifest hash claim")
	}
}

func TestStorageVerifyCacheExpires(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	shiftStorageVerifyClock(t, 25*time.Hour)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if *calls != 12 {
		t.Fatalf("expired entries must be fully re-verified: %d", *calls)
	}
}

func TestStorageVerifyCacheDisabledWithZeroTTL(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	cfg.StorageVerifyCacheTTL = 0
	calls := countLogicalArtifactChecks(t)
	for i := 0; i < 2; i++ {
		if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
			t.Fatal(e)
		}
	}
	if *calls != 12 {
		t.Fatalf("disabled cache skipped verification: %d", *calls)
	}
	if fileExists(storageVerifyCachePath(cfg)) {
		t.Fatal("disabled cache wrote state")
	}
}

func TestStorageVerifyCacheDoesNotRecordFailures(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	// Same size, so the cheap size check passes and the full read must run.
	if e := os.WriteFile(r.Results[0].OutputPath, []byte(strings.Repeat("x", int(r.Results[0].SizeBytes))), 0o600); e != nil {
		t.Fatal(e)
	}
	calls := countLogicalArtifactChecks(t)
	for i := 0; i < 2; i++ {
		if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e == nil {
			t.Fatal("corrupt artifact accepted")
		}
	}
	if *calls < 2 {
		t.Fatalf("failed verification was cached: %d", *calls)
	}
}

func TestStorageVerifyCacheIgnoresCorruptStateFile(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	if e := os.WriteFile(storageVerifyCachePath(cfg), []byte("{not json"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil || *calls != 0 {
		t.Fatalf("cache not rebuilt after corruption: calls=%d err=%v", *calls, e)
	}
}

func TestStorageVerifyCacheKeyChangeInvalidatesAndKeyNeverStored(t *testing.T) {
	cfg, r := verifyCacheFixture(t)
	calls := countLogicalArtifactChecks(t)
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	cfg.EncryptionKey = testEncryptionKey
	if e := verifyLogicalCaptureCached(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	if *calls != 12 {
		t.Fatalf("encryption key change must invalidate cached proof: %d", *calls)
	}
	info, e := os.Stat(storageVerifyCachePath(cfg))
	if e != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache state must be private: %v %v", info, e)
	}
	raw, _ := os.ReadFile(storageVerifyCachePath(cfg))
	if strings.Contains(string(raw), testEncryptionKey) {
		t.Fatal("encryption key persisted in cache state")
	}
}

func TestStorageManagerReusesVerificationAcrossRuns(t *testing.T) {
	cfg, v, m, points, _ := storagePoints(t, 6)
	cfg.StorageMaxLocalBackups = 4
	cfg.StorageVerifyCacheTTL = 24 * time.Hour
	m.cfg = cfg
	v.available = 9000
	calls := countLogicalArtifactChecks(t)
	if _, e := m.ensure(context.Background(), "", true, false); e != nil {
		t.Fatal(e)
	}
	first := *calls
	if first == 0 {
		t.Fatal("first preflight performed no verification")
	}
	// A new capture arrives; the next preflight must not re-read unchanged points.
	next := storageFixture(t, cfg.BackupDir, "2026-10-07_12-00-00", 6)
	points[next.RunID] = next
	m2 := storageVirtualManager(cfg, v, points)
	if _, e := m2.ensure(context.Background(), "", true, false); e != nil {
		t.Fatal(e)
	}
	if got := *calls - first; got > 6 {
		t.Fatalf("second preflight re-read unchanged points: %d full checks", got)
	}
	count, _ := localPayloadRunCount(cfg.BackupDir)
	if count != 4 {
		t.Fatalf("retention outcome changed: %d", count)
	}
}

// countStoragePayloadGets counts payload downloads served by the S3 fixture.
func countStoragePayloadGets(s3 *storageS3Fixture) *atomic.Int64 {
	var n atomic.Int64
	inner := s3.server.Config.Handler
	s3.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "" && isStoragePayload(filepath.Base(r.URL.Path)) {
			n.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	return &n
}

func remoteCacheFixture(t *testing.T) (config, string, *storageS3Fixture, *atomic.Int64) {
	t.Helper()
	cfg, _, _, _, s3 := storagePoints(t, 1)
	cfg.StorageVerifyCacheTTL = 24 * time.Hour
	return cfg, "2026-10-01_12-00-00", s3, countStoragePayloadGets(s3)
}

func TestStorageRemoteVerifyCacheSkipsUnchangedDownload(t *testing.T) {
	cfg, id, _, gets := remoteCacheFixture(t)
	first, e := verifyStorageRemote(context.Background(), cfg, id)
	if e != nil || first.State != "VERIFIED" {
		t.Fatal(first, e)
	}
	downloaded := gets.Load()
	if downloaded != 6 {
		t.Fatalf("first proof must download every payload: %d", downloaded)
	}
	second, e := verifyStorageRemote(context.Background(), cfg, id)
	if e != nil || second.State != "VERIFIED" || len(second.Objects) != len(first.Objects) {
		t.Fatal(second, e)
	}
	if gets.Load() != downloaded {
		t.Fatalf("unchanged remote payload downloaded again: %d", gets.Load())
	}
	if e = checkStorageRemoteIdentity(context.Background(), cfg, second); e != nil {
		t.Fatal(e)
	}
}

func TestStorageRemoteVerifyCacheDetectsReplacedObject(t *testing.T) {
	cfg, id, s3, _ := remoteCacheFixture(t)
	if _, e := verifyStorageRemote(context.Background(), cfg, id); e != nil {
		t.Fatal(e)
	}
	key := "logical/" + id + "/db0.sql.gz"
	s3.mu.Lock()
	s3.objects[key] = []byte(strings.Repeat("x", len(s3.objects[key])))
	s3.mu.Unlock()
	if _, e := verifyStorageRemote(context.Background(), cfg, id); e == nil {
		t.Fatal("replaced remote payload accepted from cache")
	}
}

func TestStorageRemoteVerifyCacheStillChecksMarker(t *testing.T) {
	cfg, id, s3, _ := remoteCacheFixture(t)
	if _, e := verifyStorageRemote(context.Background(), cfg, id); e != nil {
		t.Fatal(e)
	}
	s3.mu.Lock()
	delete(s3.objects, "logical/"+id+"/COMPLETED.json")
	s3.mu.Unlock()
	if _, e := verifyStorageRemote(context.Background(), cfg, id); e == nil {
		t.Fatal("missing completion marker accepted from cache")
	}
}

func TestStorageRemoteVerifyCacheExpiresAndCanBeDisabled(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		cfg, id, _, gets := remoteCacheFixture(t)
		verifyStorageRemote(context.Background(), cfg, id)
		shiftStorageVerifyClock(t, 25*time.Hour)
		if _, e := verifyStorageRemote(context.Background(), cfg, id); e != nil {
			t.Fatal(e)
		}
		if gets.Load() != 12 {
			t.Fatalf("expired remote proof not re-downloaded: %d", gets.Load())
		}
	})
	t.Run("disabled", func(t *testing.T) {
		cfg, id, _, gets := remoteCacheFixture(t)
		cfg.StorageVerifyCacheTTL = 0
		verifyStorageRemote(context.Background(), cfg, id)
		if _, e := verifyStorageRemote(context.Background(), cfg, id); e != nil {
			t.Fatal(e)
		}
		if gets.Load() != 12 {
			t.Fatalf("disabled cache skipped download: %d", gets.Load())
		}
	})
}

func TestStorageVerifyCacheTTLConfig(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(env, []byte("DB_USER=u\nDB_PASS=p\n"), 0o600)
	cfg, e := LoadConfig(env)
	if e != nil || cfg.StorageVerifyCacheTTL != 24*time.Hour {
		t.Fatalf("default TTL: %v %v", cfg.StorageVerifyCacheTTL, e)
	}
	os.WriteFile(env, []byte("DB_USER=u\nDB_PASS=p\nBACKUP_STORAGE_VERIFY_CACHE_TTL=0\n"), 0o600)
	if cfg, e = LoadConfig(env); e != nil || cfg.StorageVerifyCacheTTL != 0 {
		t.Fatalf("disable TTL: %v %v", cfg.StorageVerifyCacheTTL, e)
	}
	cfg.StorageVerifyCacheTTL = 6 * time.Hour
	if e = SaveConfig(env, cfg); e != nil {
		t.Fatal(e)
	}
	if cfg, e = LoadConfig(env); e != nil || cfg.StorageVerifyCacheTTL != 6*time.Hour {
		t.Fatalf("TTL not persisted: %v %v", cfg.StorageVerifyCacheTTL, e)
	}
	cfg.StorageVerifyCacheTTL = -time.Hour
	if e = SaveConfig(env, cfg); e == nil {
		t.Fatal("negative TTL saved")
	}
}
