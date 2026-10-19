package backupapp

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func storageFixture(t *testing.T, root, id string, n int) runRecord {
	t.Helper()
	r := completeFixture(t, root, id, n)
	r.FormatVersion = 2
	r.RecoveryState = "COMPLETE"
	r.BackupType = "logical"
	r.LocalCaptureIntegrityState = "VERIFIED"
	r.RemoteIntegrityState = "VERIFIED"
	r.Timestamp, _ = time.Parse(runTimestampLayout, id)
	for i := range r.Results {
		r.Results[i].ExpectedObjects = map[string][]string{"table": {"t"}}
	}
	if e := writeManifest(r.RunFolder, r); e != nil {
		t.Fatal(e)
	}
	return r
}
func storageTestConfig(t *testing.T, root string) config {
	t.Helper()
	env := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(env, []byte("DB_USER=fixture\nDB_PASS=fixture\n"), 0600)
	cfg, e := LoadConfig(env)
	if e != nil {
		t.Fatal(e)
	}
	cfg.BackupDir = root
	cfg.LogDir = t.TempDir()
	cfg.RunLogPath = filepath.Join(cfg.LogDir, "backup-runs.jsonl")
	cfg.LockFile = filepath.Join(cfg.LogDir, "backup.lock")
	cfg.StorageAutoCleanup = true
	cfg.StorageReserveBytes = 0
	cfg.StorageConcurrentBytes = 0
	cfg.StorageTemporaryBytes = 0
	cfg.LogicalConsistency = "per_database"
	cfg.DBEngine = "mysql"
	cfg.LogicalDatabases = []string{"db0", "db1", "db2", "db3", "db4", "db5"}
	cfg.LogicalSchedule = "daily@02:00,06:00,12:00,16:00,18:00"
	cfg.LogicalEnabled = true
	cfg.S3LogicalPrefix = "logical"
	cfg.S3Bucket = "bucket"
	cfg.S3Region = "region"
	cfg.S3KeyID = "fixture"
	cfg.S3KeySecret = "fixture"
	cfg.S3UploadTimeout = time.Minute
	return cfg
}

type storageVirtualFS struct {
	available, total, required uint64
	failedRemove               bool
}

func (v *storageVirtualFS) capacity(cfg config, logical bool) (CapacityReport, error) {
	r := CapacityReport{AvailableBytes: v.available, TotalBytes: v.total, UsableBytes: v.total, RequiredBytes: v.required, EstimatedBytes: v.required, FreeInodes: 1000, TotalInodes: 2000, RequiredInodes: 128, FilesystemID: 1, Sufficient: v.available >= v.required}
	r.UsedPercent = 100 * (1 - float64(v.available)/float64(v.total))
	if !r.Sufficient {
		return r, fmt.Errorf("fixture capacity exhausted")
	}
	return r, nil
}
func storageVirtualManager(cfg config, v *storageVirtualFS, points map[string]runRecord) *storageManager {
	m := storageManagerFactory(cfg)
	m.capacity = v.capacity
	m.remote = func(_ context.Context, _ config, id string) (StorageRemoteProof, error) {
		r, ok := points[id]
		if !ok {
			return StorageRemoteProof{}, fmt.Errorf("no verified remote counterpart")
		}
		return StorageRemoteProof{RunID: id, State: "VERIFIED", RestoreState: "UNVERIFIED", Destination: "s3://bucket/logical/" + id, VerifiedAt: time.Now(), Manifest: r}, nil
	}
	m.remoteLogical = m.remote
	m.identity = func(context.Context, config, StorageRemoteProof) error { return nil }
	m.remove = func(root string, d StorageDecision) (int64, error) {
		if v.failedRemove {
			return 0, fmt.Errorf("fixture unlink failure")
		}
		n, e := unlinkStoragePayload(root, d)
		if e == nil {
			v.available += uint64(n)
		}
		return n, e
	}
	return m
}

type storageS3Fixture struct {
	mu      sync.Mutex
	objects map[string][]byte
	fail    string
	server  *httptest.Server
}

func newStorageS3(t *testing.T) *storageS3Fixture {
	t.Helper()
	f := &storageS3Fixture{objects: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != "" && strings.Contains(key, f.fail) {
			w.WriteHeader(403)
			return
		}
		if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
			io.WriteString(w, "<ListBucketResult>")
			for name, data := range f.objects {
				if strings.HasPrefix(name, r.URL.Query().Get("prefix")) {
					fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", name, len(data))
				}
			}
			io.WriteString(w, "</ListBucketResult>")
			return
		}
		if r.Method == "PUT" {
			data, _ := io.ReadAll(r.Body)
			f.objects[key] = data
			w.Header().Set("ETag", "\"uploaded\"")
			return
		}
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		h := sha256.Sum256(data)
		w.Header().Set("ETag", "\""+hex.EncodeToString(h[:])+"\"")
		w.Header().Set("Last-Modified", "Mon, 12 Oct 2026 00:00:00 GMT")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		if r.Method == "GET" {
			w.Write(data)
		} else if r.Method != "HEAD" {
			t.Errorf("unexpected remote mutation %s", r.Method)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *storageS3Fixture) add(t *testing.T, r runRecord) {
	t.Helper()
	prefix := "logical/" + r.RunID + "/"
	artifacts := []remoteArtifact{}
	seen := map[string]bool{}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range r.Results {
		data, e := os.ReadFile(d.OutputPath)
		if e != nil {
			t.Fatal(e)
		}
		name := filepath.Base(d.OutputPath)
		if seen[name] {
			continue
		}
		seen[name] = true
		f.objects[prefix+name] = data
		artifacts = append(artifacts, remoteArtifact{Key: name, SizeBytes: int64(len(data)), SHA256: d.ArtifactSHA256})
	}
	meta, e := os.ReadFile(filepath.Join(r.RunFolder, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256(meta)
	f.objects[prefix+"manifest.json"] = meta
	artifacts = append(artifacts, remoteArtifact{Key: "manifest.json", SizeBytes: int64(len(meta)), SHA256: hex.EncodeToString(h[:])})
	marker := logicalCompletionManifest{FormatVersion: 2, Status: "COMPLETE", RunID: r.RunID, Bucket: "bucket", Prefix: strings.TrimSuffix(prefix, "/"), ExpectedDatabases: r.ExpectedDatabases, Artifacts: artifacts}
	data, _ := json.Marshal(marker)
	f.objects[prefix+"COMPLETED.json"] = data
}
func storagePoints(t *testing.T, n int) (config, *storageVirtualFS, *storageManager, map[string]runRecord, *storageS3Fixture) {
	t.Helper()
	root := t.TempDir()
	cfg := storageTestConfig(t, root)
	points := map[string]runRecord{}
	s3 := newStorageS3(t)
	cfg.S3Endpoint = s3.server.URL
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("2026-10-%02d_12-00-00", i)
		r := storageFixture(t, root, id, 6)
		points[id] = r
		s3.add(t, r)
	}
	v := &storageVirtualFS{available: 100, total: 10000, required: 1000}
	return cfg, v, storageVirtualManager(cfg, v, points), points, s3
}
func assertPointPresent(t *testing.T, r runRecord) {
	t.Helper()
	for _, d := range r.Results {
		if _, e := os.Stat(d.OutputPath); e != nil {
			t.Fatalf("protected point removed %s: %v", r.RunID, e)
		}
	}
}
func TestStoragePressureDeletesOldestStopsAndKeepsLatestTwo(t *testing.T) {
	cfg, v, m, points, _ := storagePoints(t, 5)
	r, e := m.ensure(context.Background(), "new", true, false)
	if e != nil {
		t.Fatal(e)
	}
	if r.State != "CLEANED" || v.available < 1000 || r.RemovedBytes == 0 {
		t.Fatalf("cleanup did not make room: %+v", r)
	}
	assertPointPresent(t, points["2026-10-05_12-00-00"])
	assertPointPresent(t, points["2026-10-04_12-00-00"])
	if !fileExists(filepath.Join(cfg.BackupDir, "2026-10-01_12-00-00", "manifest.json")) {
		t.Fatal("recovery manifest deleted")
	}
	if len(r.ProtectedRemote) != 2 || len(r.ProtectedLocal) != 2 {
		t.Fatal("coverage invariant")
	}
	audits, _ := filepath.Glob(filepath.Join(cfg.LogDir, "storage-deletions", "*.json"))
	if len(audits) == 0 {
		t.Fatal("no deletion audit")
	}
}
func TestStorageOnlyProtectedInsufficientDoesNotSacrificePoint(t *testing.T) {
	_, _, m, points, _ := storagePoints(t, 2)
	r, e := m.ensure(context.Background(), "new", true, false)
	if e == nil || r.State != "CRITICAL" {
		t.Fatal("must fail honestly")
	}
	for _, point := range points {
		assertPointPresent(t, point)
	}
}
func TestStorageMayUseSecondLatestOnlyWhenItCanFitNextCapture(t *testing.T) {
	_, v, m, points, _ := storagePoints(t, 2)
	v.available = 700
	r, e := m.ensure(context.Background(), "new", true, false)
	if e != nil {
		t.Fatal(e)
	}
	assertPointPresent(t, points["2026-10-02_12-00-00"])
	if len(r.ProtectedLocal) != 1 || len(r.ProtectedRemote) != 2 {
		t.Fatal("emergency protection invariant")
	}
}
func TestStorageRemoteFailureBlocksDeletion(t *testing.T) {
	_, _, m, points, _ := storagePoints(t, 5)
	m.remote = func(context.Context, config, string) (StorageRemoteProof, error) {
		return StorageRemoteProof{}, fmt.Errorf("S3 outage")
	}
	r, e := m.ensure(context.Background(), "new", true, false)
	if e == nil || r.State != "CRITICAL" {
		t.Fatal("remote uncertainty accepted")
	}
	for _, point := range points {
		assertPointPresent(t, point)
	}
}
func TestStorageRemoteOutageWithAdequateHeadroomDoesNotBlockCapture(t *testing.T) {
	_, v, m, points, _ := storagePoints(t, 3)
	v.available = 1000
	m.remote = func(context.Context, config, string) (StorageRemoteProof, error) {
		return StorageRemoteProof{}, fmt.Errorf("S3 outage")
	}
	r, e := m.ensure(context.Background(), "new", true, false)
	if e != nil || r.State != "CLEANUP_DEFERRED" {
		t.Fatal(r, e)
	}
	for _, point := range points {
		assertPointPresent(t, point)
	}
}
func TestStorageTemporaryAndPartialDuplicatesPrecedeComplete(t *testing.T) {
	cfg, v, m, points, _ := storagePoints(t, 5)
	folder := filepath.Join(cfg.BackupDir, "2026-09-01_12-00-00")
	os.MkdirAll(folder, 0700)
	failed := runRecord{RunID: filepath.Base(folder), FormatVersion: 2, Status: "failed", RecoveryState: "FAILED", BackupType: "logical"}
	writeManifest(folder, failed)
	tmp := filepath.Join(folder, "db.sql.gz.incomplete-20260901T120000.000000000")
	os.WriteFile(tmp, []byte(strings.Repeat("x", 200)), 0600)
	os.Chtimes(tmp, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	partial := storageFixture(t, cfg.BackupDir, "2026-10-06_12-00-00", 6)
	partial.Status = "partial"
	partial.RecoveryState = "PARTIAL"
	partial.DatabasesSucceeded = 5
	partial.DatabasesFailed = 1
	partial.Results[5].Status = "failed"
	writeManifest(partial.RunFolder, partial)
	v.required = 300
	report, e := m.ensure(context.Background(), "new", true, false)
	if e != nil {
		t.Fatal(e)
	}
	if fileExists(tmp) {
		t.Fatal("expired disposable temporary retained")
	}
	assertPointPresent(t, points["2026-10-05_12-00-00"])
	audits, _ := filepath.Glob(filepath.Join(cfg.LogDir, "storage-deletions", "*.json"))
	first := false
	for _, a := range audits {
		b, _ := os.ReadFile(a)
		var record storageDeletionAudit
		json.Unmarshal(b, &record)
		if record.Decision.Type == "disposable_failed_temporary" {
			first = true
		}
	}
	if !first || report.ReclaimedBytes == 0 {
		t.Fatal("temporary cleanup was not audited")
	}
}
func TestStorageUniquePartialAndRestoreTestedAndDependenciesHeld(t *testing.T) {
	cfg, _, m, points, _ := storagePoints(t, 5)
	r := points["2026-10-01_12-00-00"]
	r.ValidationMode = "sandbox restore test"
	r.ValidationStatus = "success"
	writeManifest(r.RunFolder, r)
	r = points["2026-10-02_12-00-00"]
	r.MySQLPITR = &mysqlPITRMetadata{}
	writeManifest(r.RunFolder, r)
	unique := storageFixture(t, cfg.BackupDir, "2026-10-08_12-00-00", 6)
	unique.Status = "partial"
	unique.RecoveryState = "PARTIAL"
	unique.DatabasesFailed = 1
	unique.DatabasesSucceeded = 5
	unique.Results[0].ArtifactSHA256 = strings.Repeat("a", 64)
	writeManifest(unique.RunFolder, unique)
	m.ensure(context.Background(), "new", true, false)
	assertPointPresent(t, points["2026-10-01_12-00-00"])
	assertPointPresent(t, points["2026-10-02_12-00-00"])
	if !fileExists(unique.Results[0].OutputPath) {
		t.Fatal("uniquely useful partial artifact removed")
	}
}
func TestStoragePhysicalComponentsAndCurrentRunNeverDeleted(t *testing.T) {
	cfg, _, m, points, _ := storagePoints(t, 5)
	r := points["2026-10-01_12-00-00"]
	r.PhysicalBackup = &physicalBackupResult{Status: "success"}
	r.BackupType = "logical+physical"
	writeManifest(r.RunFolder, r)
	component := filepath.Join(r.RunFolder, "physical.xbstream")
	os.WriteFile(component, []byte("physical component"), 0600)
	m.ensure(context.Background(), "2026-10-03_12-00-00", false, false)
	if !fileExists(component) || !fileExists(filepath.Join(r.RunFolder, "manifest.json")) {
		t.Fatal("physical component or metadata deleted")
	}
	assertPointPresent(t, points["2026-10-03_12-00-00"])
	_ = cfg
}
func TestStorageUnlinkRejectsSymlinksHardlinksAndChangedFiles(t *testing.T) {
	root := t.TempDir()
	r := storageFixture(t, root, "2026-10-01_12-00-00", 1)
	d, e := storageFileIdentity(r.Results[0].OutputPath)
	if e != nil {
		t.Fatal(e)
	}
	d.RunID = r.RunID
	d.SHA256 = r.Results[0].ArtifactSHA256
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Remove(d.Path)
	os.Symlink(outside, d.Path)
	if _, e = unlinkStoragePayload(root, d); e == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "outside" {
		t.Fatal("outside data changed")
	}
	os.Remove(d.Path)
	os.Link(outside, d.Path)
	d, _ = storageFileIdentity(d.Path)
	if d.Path != "" {
		t.Fatal("hardlink accepted")
	}
}
func TestStorageCrashIntentReconcilesWithoutDeletingReplacement(t *testing.T) {
	cfg, _, m, points, _ := storagePoints(t, 5)
	point := points["2026-10-01_12-00-00"]
	d, _ := storageFileIdentity(point.Results[0].OutputPath)
	d.RunID = point.RunID
	audit := storageDeletionAudit{Decision: d, At: time.Now(), Outcome: "INTENT"}
	if e := writeStorageAudit(cfg, audit); e != nil {
		t.Fatal(e)
	}
	os.Remove(d.Path)
	os.WriteFile(d.Path, []byte("replacement"), 0600)
	m.ensure(context.Background(), "new", true, false)
	bytes, _ := os.ReadFile(d.Path)
	if string(bytes) != "replacement" {
		t.Fatal("replacement deleted on recovery")
	}
	data, _ := os.ReadFile(auditPath(cfg, d))
	var outcome storageDeletionAudit
	json.Unmarshal(data, &outcome)
	if outcome.Outcome != "HELD_AFTER_RESTART" {
		t.Fatalf("journal not recovered: %s", outcome.Outcome)
	}
}
func TestStorageCrashAfterUnlinkAcknowledgesMissingFile(t *testing.T) {
	cfg, _, m, points, _ := storagePoints(t, 5)
	point := points["2026-10-01_12-00-00"]
	d, _ := storageFileIdentity(point.Results[0].OutputPath)
	d.RunID = point.RunID
	writeStorageAudit(cfg, storageDeletionAudit{Decision: d, At: time.Now(), Outcome: "INTENT"})
	os.Remove(d.Path)
	m.ensure(context.Background(), "new", true, false)
	data, _ := os.ReadFile(auditPath(cfg, d))
	var outcome storageDeletionAudit
	json.Unmarshal(data, &outcome)
	if outcome.Outcome != "RECOVERED_ALREADY_REMOVED" {
		t.Fatalf("interrupted unlink not acknowledged: %s", outcome.Outcome)
	}
}
func TestStorageS3FullIntegrityAndMissingObjects(t *testing.T) {
	cfg, _, _, points, s3 := storagePoints(t, 3)
	id := "2026-10-01_12-00-00"
	proof, e := verifyStorageRemote(context.Background(), cfg, id)
	if e != nil || proof.State != "VERIFIED" || proof.RestoreState != "UNVERIFIED" {
		t.Fatal(proof, e)
	}
	if e = checkStorageRemoteIdentity(context.Background(), cfg, proof); e != nil {
		t.Fatal(e)
	}
	key := "logical/" + id + "/" + filepath.Base(points[id].Results[0].OutputPath)
	s3.mu.Lock()
	s3.objects[key] = []byte("corrupt")
	s3.mu.Unlock()
	if _, e = verifyStorageRemote(context.Background(), cfg, id); e == nil {
		t.Fatal("corrupt remote payload accepted")
	}
	if e = checkStorageRemoteIdentity(context.Background(), cfg, proof); e == nil {
		t.Fatal("changed remote identity accepted")
	}
}
func TestStorageConcurrentSchedulerAndCleanup(t *testing.T) {
	cfg, _, _, _, _ := storagePoints(t, 3)
	release, e := acquireLock(cfg.LockFile)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = PlanStorageRecovery(context.Background(), cfg); e == nil {
		t.Fatal("cleanup overlapped scheduler")
	}
}
func TestStorageThirtyDaysFiveDailyCapturesPreserveCoverage(t *testing.T) {
	cfg, v, m, points, s3 := storagePoints(t, 3)
	v.available = 2500
	v.total = 10000
	v.required = 1000
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	deleted := uint64(0)
	for n := 0; n < 150; n++ {
		id := start.AddDate(0, 0, n/5).Add(time.Duration([]int{2, 6, 12, 16, 18}[n%5]) * time.Hour).Format(runTimestampLayout)
		report, e := m.ensure(context.Background(), id, true, false)
		if e != nil {
			t.Fatalf("day %d slot %d: %v", n/5, n%5, e)
		}
		if len(report.ProtectedLocal) < 1 || len(report.ProtectedRemote) < 2 {
			t.Fatalf("coverage lost at run %d", n)
		}
		deleted += report.RemovedBytes
		point := storageFixture(t, cfg.BackupDir, id, 6)
		points[id] = point
		s3.add(t, point)
		cost := uint64(totalLogicalArtifactSize(point.Results))
		if v.available < cost {
			t.Fatal("avoidable disk exhaustion")
		}
		v.available -= cost
	}
	if deleted == 0 {
		t.Fatal("simulation never exercised cleanup")
	}
	latest := start.AddDate(0, 0, 29).Add(18 * time.Hour).Format(runTimestampLayout)
	assertPointPresent(t, points[latest])
}
func TestStorageEstimateGrowthAndOverflow(t *testing.T) {
	cfg := storageTestConfig(t, t.TempDir())
	cfg.StorageEstimateBytes = 2 << 30
	cfg.StorageGrowthPercent = 75
	if got := estimateCaptureBytes(cfg); got != uint64(3584<<20) {
		t.Fatal("growth margin missing", got)
	}
	cfg.StorageEstimateBytes = int(^uint(0) >> 1)
	if estimateCaptureBytes(cfg) != ^uint64(0) {
		t.Fatal("overflow did not fail closed")
	}
}
func TestStorageNoCandidatesNoFallbackCritical(t *testing.T) {
	_, _, m, points, _ := storagePoints(t, 2)
	r, e := m.ensure(context.Background(), "new", true, false)
	if e == nil || r.State != "CRITICAL" || r.Fallback != "DISABLED" {
		t.Fatal(r, e)
	}
	for _, p := range points {
		assertPointPresent(t, p)
	}
}
func TestStorageDryRunDoesNotDelete(t *testing.T) {
	_, _, m, points, _ := storagePoints(t, 5)
	r, e := m.ensure(context.Background(), "", true, true)
	if e != nil || r.State != "DRY_RUN" {
		t.Fatal(r, e)
	}
	for _, p := range points {
		assertPointPresent(t, p)
	}
	if !slices.Contains(r.ProtectedLocal, "2026-10-05_12-00-00") {
		t.Fatal("latest point unprotected")
	}
}

func globalStorageFixture(t *testing.T, root, id string) runRecord {
	t.Helper()
	folder := filepath.Join(root, id)
	os.MkdirAll(folder, 0700)
	var sql strings.Builder
	names := []string{"db0", "db1", "db2", "db3", "db4", "db5"}
	for _, name := range names {
		fmt.Fprintf(&sql, "USE `%s`;\nCREATE TABLE `t` (id int);\nINSERT INTO `t` VALUES (1);\n", name)
	}
	sql.WriteString("-- Dump completed on fixture\n")
	path := filepath.Join(folder, "all-databases.sql.gz")
	file, _ := os.Create(path)
	gz := gzip.NewWriter(file)
	gz.Write([]byte(sql.String()))
	gz.Close()
	file.Close()
	hash, _ := fileSHA256(path)
	plain, _ := gzipPayloadSHA256(path)
	info, _ := os.Stat(path)
	r := runRecord{RunID: id, FormatVersion: 2, Status: "success", RecoveryState: "COMPLETE", BackupType: "logical", LocalCaptureIntegrityState: "VERIFIED", RemoteIntegrityState: "VERIFIED", RunFolder: folder, DatabasesTotal: 6, DatabasesSucceeded: 6, ExpectedDatabases: names, ConsistencyMethod: "single mysqldump transaction across all selected InnoDB databases; DDL excluded by policy"}
	r.Timestamp, _ = time.Parse(runTimestampLayout, id)
	for _, name := range names {
		r.Results = append(r.Results, databaseResult{Name: name, Status: "success", OutputPath: path, SizeBytes: info.Size(), ArtifactSHA256: hash, SQLSHA256: plain, ExpectedObjects: map[string][]string{"table": {"t"}}})
	}
	if e := writeManifest(folder, r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestStorageScheduledLowSpaceCleanupThenCompleteSixDatabaseS3Backup(t *testing.T) {
	root := t.TempDir()
	cfg := storageTestConfig(t, root)
	cfg.LogicalConsistency = "global"
	s3 := newStorageS3(t)
	cfg.S3Endpoint = s3.server.URL
	points := map[string]runRecord{}
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("2026-09-%02d_12-00-00", i)
		r := globalStorageFixture(t, root, id)
		points[id] = r
		s3.add(t, r)
	}
	v := &storageVirtualFS{available: 20, total: 2000, required: 400}
	mysql := filepath.Join(t.TempDir(), "mysql")
	dump := filepath.Join(t.TempDir(), "mysqldump")
	script := `#!/bin/sh
case "$*" in
 *"SHOW DATABASES"*) printf 'db0\ndb1\ndb2\ndb3\ndb4\ndb5\n';;
 *"ENGINE <>"*) echo 0;;
 *"information_schema.VIEWS"*|*"information_schema.TRIGGERS"*|*"information_schema.ROUTINES"*|*"information_schema.EVENTS"*) case "$*" in *"COUNT"*) echo 0;; *) exit 0;; esac;;
 *"COUNT"*) echo 1;;
 *"SELECT 1"*) echo 1;;
 *) echo t;;
esac
`
	os.WriteFile(mysql, []byte(script), 0700)
	var sql strings.Builder
	for _, name := range cfg.LogicalDatabases {
		fmt.Fprintf(&sql, "USE `%s`;\nCREATE TABLE `t` (id int);\nINSERT INTO `t` VALUES (2);\n", name)
	}
	sql.WriteString("-- Dump completed on fixture\n")
	os.WriteFile(dump, []byte("#!/bin/sh\ncat <<'FIXTURE'\n"+sql.String()+"FIXTURE\n"), 0700)
	cfg.MySQLBin = mysql
	cfg.MySQLDumpBin = dump
	cfg.MySQLDumpSessionTimeoutOverride = false
	cfg.DBHost = "fixture"
	cfg.DBPort = "3306"
	cfg.LogicalTimeoutPerDB = time.Minute
	cfg.LogicalSchedule = "always"
	cfg.LogicalS3UploadEnabled = true
	cfg.RequireRemoteCompletion = true
	cfg.S3UploadMode = "direct"
	cfg.PhysicalEnabled = false
	cfg.ExecutionSource = "runner"
	cfg.MetricsFile = filepath.Join(cfg.LogDir, "metrics.prom")
	cfg.LogicalParallel = 1
	oldFactory := storageManagerFactory
	storageManagerFactory = func(c config) *storageManager {
		m := oldFactory(c)
		m.capacity = v.capacity
		m.remove = func(root string, d StorageDecision) (int64, error) {
			n, e := unlinkStoragePayload(root, d)
			if e == nil {
				v.available += uint64(n)
			}
			return n, e
		}
		return m
	}
	defer func() { storageManagerFactory = oldFactory }()
	originalGuard := acquireGlobalDDLGuard
	acquireGlobalDDLGuard = func(config) (ddlGuard, error) { return fixtureDDLGuard{}, nil }
	defer func() { acquireGlobalDDLGuard = originalGuard }()
	result, e := RunBackup(context.Background(), cfg, RunSinks{Console: io.Discard})
	if e != nil {
		t.Fatal(e)
	}
	if result.Status != "success" || result.RecoveryState != "COMPLETE" || result.DatabasesSucceeded != 6 || result.DatabasesFailed != 0 || result.StorageManagement == nil || result.StorageManagement.ReclaimedBytes == 0 {
		t.Fatalf("next scheduled backup failed: %+v", result)
	}
	assertPointPresent(t, points["2026-09-06_12-00-00"])
	assertPointPresent(t, points["2026-09-05_12-00-00"])
	proof, e := verifyStorageRemote(context.Background(), cfg, result.RunID)
	if e != nil || proof.State != "VERIFIED" {
		t.Fatal(proof, e)
	}
}
func TestStorageS3OutageOnApprovedFallbackFailsWithoutRemoteCompletion(t *testing.T) {
	cfg, _, m, points, _ := storagePoints(t, 2)
	secondary := t.TempDir()
	cfg.StorageSecondaryDir = secondary
	m.cfg = cfg
	original := m.capacity
	m.capacity = func(c config, l bool) (CapacityReport, error) {
		if c.BackupDir == secondary {
			return CapacityReport{AvailableBytes: 5000, RequiredBytes: 1000, Sufficient: true, FilesystemID: 2}, nil
		}
		return original(c, l)
	}
	m.remote = func(context.Context, config, string) (StorageRemoteProof, error) {
		return StorageRemoteProof{}, fmt.Errorf("S3 unavailable")
	}
	r, e := m.ensure(context.Background(), "new", true, false)
	if e != nil || r.State != "FALLBACK" || r.Fallback != "APPROVED_SECONDARY" {
		t.Fatal(r, e)
	}
	for _, point := range points {
		assertPointPresent(t, point)
	}
	cfg.S3Endpoint = "http://127.0.0.1:1"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, e = verifyStorageRemote(ctx, cfg, "2026-10-01_12-00-00"); e == nil {
		t.Fatal("outage manufactured completion")
	}
}
func TestStorageDiskExhaustionDuringDumpPreservesPreviousPoints(t *testing.T) {
	cfg, _, _, points, _ := storagePoints(t, 2)
	dump := filepath.Join(t.TempDir(), "dump")
	os.WriteFile(dump, []byte("#!/bin/sh\necho 'CREATE TABLE t (id int);'\necho '-- Dump completed on fixture'\n"), 0700)
	cfg.MySQLDumpBin = dump
	mysql := filepath.Join(t.TempDir(), "mysql")
	os.WriteFile(mysql, []byte("#!/bin/sh\ncase \"$*\" in *\"COUNT\"*) echo 0;; *\"information_schema.VIEWS\"*) exit 0;; *) echo 0;; esac\n"), 0700)
	cfg.MySQLBin = mysql
	cfg.MySQLDumpSessionTimeoutOverride = false
	cfg.StorageReserveBytes = int(^uint(0) >> 1)
	cfg.RetryCount = 1
	cfg.LogicalTimeoutPerDB = time.Second
	destination := filepath.Join(t.TempDir(), "new.sql.gz")
	r := dumpWithRetry(cfg, "db0", destination, nil)
	if r.Status == "success" || fileExists(destination) || !strings.Contains(r.Error, "reserve") {
		t.Fatalf("disk failure was not exercised: status=%s category=%s error=%s exists=%t", r.Status, r.ErrorCategory, r.Error, fileExists(destination))
	}
	for _, p := range points {
		assertPointPresent(t, p)
	}
}

func TestStorageReserveRechecksDuringLongWrite(t *testing.T) {
	_, _, _, points, _ := storagePoints(t, 2)
	path := filepath.Join(t.TempDir(), "active.sql.gz")
	file, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	writer := reserveWriter{file: file, reserve: 1, inspect: func(string, uint64, uint64) (CapacityReport, error) {
		calls++
		return CapacityReport{Sufficient: calls == 1}, nil
	}}
	if n, e := writer.Write(make([]byte, 17<<20)); e != nil || n != 17<<20 {
		t.Fatal("first write failed", n, e)
	}
	if n, e := writer.Write([]byte("later")); e == nil || n != 0 {
		t.Fatal("mid-run exhaustion ignored", n, e)
	}
	file.Close()
	removePartialOutput(path)
	if fileExists(path) {
		t.Fatal("interrupted write retained final artifact name")
	}
	for _, point := range points {
		assertPointPresent(t, point)
	}
}
func TestStorageLargerRecentCaptureRaisesNextEstimate(t *testing.T) {
	cfg := storageTestConfig(t, t.TempDir())
	cfg.StorageEstimateBytes = 0
	cfg.StorageGrowthPercent = 50
	cfg.StorageHistoryRuns = 12
	for i := 0; i < 16; i++ {
		size := int64(1 << 30)
		if i == 0 {
			size = 40 << 30
		}
		if i == 15 {
			size = 2560 << 20
		}
		record := runRecord{RunID: fmt.Sprint(i), Status: "success", DatabasesTotal: 1, Results: []databaseResult{{OutputPath: "dump", SizeBytes: size}}}
		if e := appendRunRecord(cfg.RunLogPath, record); e != nil {
			t.Fatal(e)
		}
	}
	if got := estimateCaptureBytes(cfg); got != uint64(3840<<20) {
		t.Fatalf("recent growth not reflected or obsolete max retained: %d", got)
	}
}
func TestStorageInvalidWatermarksRejectedBeforeConfigSave(t *testing.T) {
	cfg := storageTestConfig(t, t.TempDir())
	cfg.StorageHighWatermark = 90
	cfg.StorageLowWatermark = 95
	if e := SaveConfig(filepath.Join(t.TempDir(), ".env"), cfg); e == nil {
		t.Fatal("invalid cleanup thresholds saved")
	}
}

func TestStorageCriticalPreflightFinalizesFailedRunAndAlertMetric(t *testing.T) {
	cfg, _, manager, points, _ := storagePoints(t, 2)
	cfg.LogicalSchedule = "always"
	cfg.PhysicalEnabled = false
	cfg.AllowRootRunner = true
	cfg.ExecutionSource = "runner"
	cfg.MetricsFile = filepath.Join(cfg.LogDir, "metrics.prom")
	before := storageManagerFactory
	storageManagerFactory = func(config) *storageManager { return manager }
	defer func() { storageManagerFactory = before }()
	record, err := RunBackup(context.Background(), cfg, RunSinks{Console: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "failed" || record.RecoveryState != "FAILED" || record.ExitCode == 0 || !strings.Contains(record.FailureReason, "CRITICAL storage recovery") {
		t.Fatalf("capacity refusal was not an honest failed run: %+v", record)
	}
	if record.StorageManagement == nil || record.StorageManagement.State != "CRITICAL" {
		t.Fatal("critical storage evidence missing")
	}
	metrics, err := os.ReadFile(cfg.MetricsFile)
	if err != nil {
		t.Fatal(err)
	}
	criticalMetric := false
	for _, line := range strings.Split(string(metrics), "\n") {
		if strings.HasPrefix(line, "backup_storage_manager_critical{") && strings.HasSuffix(line, " 1") {
			criticalMetric = true
		}
	}
	if !criticalMetric {
		t.Fatal("critical alert metric was not emitted")
	}
	for _, point := range points {
		assertPointPresent(t, point)
	}
}

func TestStorageCountRetentionWithoutDiskPressure(t *testing.T) {
	for _, reserveCapture := range []bool{false, true} {
		t.Run(fmt.Sprint(reserveCapture), func(t *testing.T) {
			cfg, v, m, points, _ := storagePoints(t, 6)
			cfg.StorageMaxLocalBackups = 4
			m.cfg = cfg
			v.available = 9000
			current := ""
			want := 4
			if reserveCapture {
				current = "2026-10-07_12-00-00"
				want = 3
			}
			report, err := m.ensure(context.Background(), current, true, false)
			if err != nil {
				t.Fatal(err)
			}
			count, err := localPayloadRunCount(cfg.BackupDir)
			if err != nil || count != want || report.RemovedBytes == 0 {
				t.Fatalf("count=%d want=%d report=%+v error=%v", count, want, report, err)
			}
			for day := 7 - want; day <= 6; day++ {
				assertPointPresent(t, points[fmt.Sprintf("2026-10-%02d_12-00-00", day)])
			}
		})
	}
}

func TestStorageCountRetentionRefusesUnverifiedCounterpart(t *testing.T) {
	cfg, v, m, points, _ := storagePoints(t, 5)
	cfg.StorageMaxLocalBackups = 4
	m.cfg = cfg
	v.available = 9000
	delete(points, "2026-10-01_12-00-00")
	report, err := m.ensure(context.Background(), "", true, false)
	if err == nil || report.State != "CRITICAL" {
		t.Fatal("unverifiable deletion must block an unmet count cap")
	}
	count, _ := localPayloadRunCount(cfg.BackupDir)
	if count != 5 {
		t.Fatalf("unexpected deletion: count=%d", count)
	}
}

func TestStoragePhysicalFailureDoesNotEraseLogicalEvidence(t *testing.T) {
	cfg, _, _, points, s3 := storagePoints(t, 1)
	r := points["2026-10-01_12-00-00"]
	r.Status = "partial"
	r.RecoveryState = "PARTIAL"
	r.PhysicalBackup = &physicalBackupResult{Status: "failed", Error: "physical disk full"}
	r.FailureReason = r.PhysicalBackup.Error
	if err := writeManifest(r.RunFolder, r); err != nil {
		t.Fatal(err)
	}
	s3.add(t, r)
	s3.mu.Lock()
	prefix := "logical/" + r.RunID + "/"
	var marker logicalCompletionManifest
	if err := json.Unmarshal(s3.objects[prefix+"COMPLETED.json"], &marker); err != nil {
		t.Fatal(err)
	}
	marker.Status = "PARTIAL"
	raw, _ := json.Marshal(marker)
	s3.objects[prefix+"PARTIAL.json"] = raw
	delete(s3.objects, prefix+"COMPLETED.json")
	s3.mu.Unlock()
	if _, err := verifyStorageRemote(context.Background(), cfg, r.RunID); err == nil {
		t.Fatal("PARTIAL marker passed complete recovery gate")
	}
	proof, err := verifyStorageRemoteLogical(context.Background(), cfg, r.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.LogicalOnly || proof.Manifest.RecoveryState != "PARTIAL" {
		t.Fatal("physical failure evidence lost")
	}
	if err := checkStorageRemoteIdentity(context.Background(), cfg, proof); err != nil {
		t.Fatal(err)
	}
	s3.mu.Lock()
	delete(s3.objects, prefix+"PARTIAL.json")
	s3.mu.Unlock()
	if err := checkStorageRemoteIdentity(context.Background(), cfg, proof); err == nil {
		t.Fatal("removed partial commitment accepted")
	}
	copy := logicalStorageRecord(r)
	if err := verifyLogicalCapture(cfg, copy, r.RunFolder); err != nil {
		t.Fatal(err)
	}
	original, _ := readManifest(r.RunFolder)
	if original.Status != "partial" || original.RecoveryState != "PARTIAL" {
		t.Fatal("original manifest mutated")
	}
	r.DatabasesFailed = 1
	if logicalStorageRecord(r).Status != "partial" {
		t.Fatal("logical failure promoted")
	}
}

func TestStorageLocalCountConfigRoundTrip(t *testing.T) {
	cfg := storageTestConfig(t, t.TempDir())
	cfg.StorageMaxLocalBackups = 4
	path := filepath.Join(t.TempDir(), ".env")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.StorageMaxLocalBackups != 4 {
		t.Fatalf("count setting lost: %v %+v", err, loaded.StorageMaxLocalBackups)
	}
	cfg.StorageMaxLocalBackups = 2
	if err := SaveConfig(path, cfg); err == nil {
		t.Fatal("unsafe two-point cap accepted")
	}
}

func TestStorageCountRetentionWithRealPartialIdentityRecheck(t *testing.T) {
	cfg, v, _, points, s3 := storagePoints(t, 6)
	cfg.StorageMaxLocalBackups = 4
	v.available = 9000
	old := points["2026-10-01_12-00-00"]
	old.Status = "partial"
	old.RecoveryState = "PARTIAL"
	old.PhysicalBackup = &physicalBackupResult{Status: "failed", Error: "physical capacity unavailable"}
	old.FailureReason = old.PhysicalBackup.Error
	if err := writeManifest(old.RunFolder, old); err != nil {
		t.Fatal(err)
	}
	s3.add(t, old)
	prefix := "logical/" + old.RunID + "/"
	s3.mu.Lock()
	var marker logicalCompletionManifest
	if err := json.Unmarshal(s3.objects[prefix+"COMPLETED.json"], &marker); err != nil {
		t.Fatal(err)
	}
	marker.Status = "PARTIAL"
	raw, _ := json.Marshal(marker)
	s3.objects[prefix+"PARTIAL.json"] = raw
	delete(s3.objects, prefix+"COMPLETED.json")
	s3.mu.Unlock()
	m := storageManagerFactory(cfg)
	m.capacity = v.capacity
	report, err := m.ensure(context.Background(), "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	count, err := localPayloadRunCount(cfg.BackupDir)
	if err != nil || count != 4 || report.LocalBackupCount != 4 {
		t.Fatalf("count=%d report=%+v err=%v", count, report, err)
	}
	for day := 3; day <= 6; day++ {
		assertPointPresent(t, points[fmt.Sprintf("2026-10-%02d_12-00-00", day)])
	}
	original, err := readManifest(old.RunFolder)
	if err != nil || original.RecoveryState != "PARTIAL" {
		t.Fatal("historical manifest was changed")
	}
}
