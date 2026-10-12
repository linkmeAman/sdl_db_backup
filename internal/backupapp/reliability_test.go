package backupapp

import (
	"compress/gzip"
	"context"
	"crypto/md5"
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
	"syscall"
	"testing"
	"time"
)

func completeFixture(t *testing.T, root, id string, n int) runRecord {
	t.Helper()
	folder := filepath.Join(root, id)
	if e := os.MkdirAll(folder, 0700); e != nil {
		t.Fatal(e)
	}
	r := runRecord{RunID: id, Status: "success", RunFolder: folder, DatabasesTotal: n, DatabasesSucceeded: n}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("db%d", i)
		path := filepath.Join(folder, name+".sql.gz")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		gz := gzip.NewWriter(f)
		fmt.Fprintln(gz, "CREATE TABLE t (id int);\n-- Dump completed on fixture")
		gz.Close()
		f.Close()
		h, _ := fileSHA256(path)
		sql, _ := gzipPayloadSHA256(path)
		info, _ := os.Stat(path)
		r.ExpectedDatabases = append(r.ExpectedDatabases, name)
		r.Results = append(r.Results, databaseResult{Name: name, Status: "success", OutputPath: path, SizeBytes: info.Size(), ArtifactSHA256: h, SQLSHA256: sql})
	}
	if e := writeManifest(folder, r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestCompleteGateSixAndFiveDatabases(t *testing.T) {
	r := completeFixture(t, t.TempDir(), "2026-10-10_12-00-00", 6)
	cfg := config{DBEngine: "mysql"}
	if e := verifyLogicalCapture(cfg, r, r.RunFolder); e != nil {
		t.Fatal(e)
	}
	r.Results[5].Status = "failed"
	r.Status = "partial"
	r.DatabasesSucceeded = 5
	r.DatabasesFailed = 1
	if e := verifyLogicalCapture(cfg, r, r.RunFolder); e == nil {
		t.Fatal("partial run passed complete gate")
	}
	r.Status = "success"
	r.DatabasesFailed = 0
	r.DatabasesSucceeded = 6
	if e := verifyLogicalCapture(cfg, r, r.RunFolder); e == nil {
		t.Fatal("lying counters passed complete gate")
	}
}
func TestMissingCorruptAndLegacyArtifacts(t *testing.T) {
	for _, mode := range []string{"missing", "checksum", "legacy-no-hash", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			r := completeFixture(t, t.TempDir(), "2026-10-10_12-00-00", 2)
			switch mode {
			case "missing":
				os.Remove(r.Results[0].OutputPath)
			case "checksum":
				r.Results[0].ArtifactSHA256 = strings.Repeat("0", 64)
			case "legacy-no-hash":
				r.Results[0].ArtifactSHA256 = ""
			case "duplicate":
				r.Results[1].Name = r.Results[0].Name
			}
			if e := verifyLogicalCapture(config{DBEngine: "mysql"}, r, r.RunFolder); e == nil {
				t.Fatal("unproven inventory passed")
			}
		})
	}
}
func TestStoragePreflightAndReserveDuringWrite(t *testing.T) {
	root := t.TempDir()
	r, e := inspectCapacity(root, 1, ^uint64(0))
	if e == nil || r.Sufficient {
		t.Fatal("overflow estimate accepted")
	}
	r, e = inspectCapacity(root, 1, 0)
	if e != nil || !r.Sufficient {
		t.Fatalf("normal capacity: %+v %v", r, e)
	}
	f, _ := os.Create(filepath.Join(root, "capture"))
	defer f.Close()
	w := reserveWriter{file: f, reserve: ^uint64(0) - 1}
	if _, e = w.Write([]byte("payload")); e == nil {
		t.Fatal("reserve not enforced")
	}
}
func TestRetentionCompleteSurvivesNewerFailuresAndCrash(t *testing.T) {
	root := t.TempDir()
	old := completeFixture(t, root, "2026-10-01_12-00-00", 2)
	newer := completeFixture(t, root, "2026-10-02_12-00-00", 2)
	partial := completeFixture(t, root, "2026-10-10_12-00-00", 2)
	partial.Status = "partial"
	writeManifest(partial.RunFolder, partial)
	cfg := config{DBEngine: "mysql", BackupDir: root, LogDir: filepath.Join(root, "logs"), RetentionMinComplete: 1, RetentionDeleteEnabled: true}
	plan, e := PlanRetention(cfg, "")
	if e != nil {
		t.Fatal(e)
	}
	byID := map[string]RetentionDecision{}
	for _, d := range plan.Decisions {
		byID[d.RunID] = d
	}
	if byID[newer.RunID].Action != "KEEP" || byID[partial.RunID].Action != "HOLD" || byID[old.RunID].Action != "DELETE_CANDIDATE" {
		t.Fatalf("unsafe decisions %+v", byID)
	}
	if e = applyRetentionPlan(cfg, plan); e != nil {
		t.Fatal(e)
	}
	q := filepath.Join(root, ".retention-quarantine", old.RunID)
	if _, e = os.Stat(q); e != nil {
		t.Fatal("quarantined point not recoverable")
	}
	if _, e = PlanRetention(cfg, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(q); e != nil {
		t.Fatal("restart deleted quarantine")
	}
}
func TestConcurrentLockAndCrashMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.lock")
	release, e := acquireLock(path)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := acquireLock(path); e == nil {
		other()
		t.Fatal("overlapping runner accepted")
	}
	release()
	release()
	next, e := acquireLock(path)
	if e != nil {
		t.Fatal(e)
	}
	next()
	os.WriteFile(path, []byte("pid=999999999 started=old"), 0600)
	next, e = acquireLock(path)
	if e != nil {
		t.Fatal(e)
	}
	next()
}
func TestInterruptedOutputIsPreservedAndLabelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.sql.gz")
	os.WriteFile(path, []byte("partial"), 0600)
	removePartialOutput(path)
	found, _ := filepath.Glob(path + ".incomplete-*")
	if len(found) != 1 {
		t.Fatal("incomplete capture lost")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("incomplete file kept under success name")
	}
}
func TestPartialCannotPublishCompletedMarker(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			r := completeFixture(t, t.TempDir(), "2026-10-10_12-00-00", 6)
			if partial {
				r.Status = "partial"
				r.DatabasesFailed = 1
				r.DatabasesSucceeded = 5
				r.Results[5].Status = "failed"
				writeManifest(r.RunFolder, r)
			}
			objects := map[string][]byte{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				key := q.URL.Path
				switch q.Method {
				case "PUT":
					b, _ := io.ReadAll(q.Body)
					objects[key] = b
					w.Header().Set("ETag", "opaque")
				case "HEAD":
					b, ok := objects[key]
					if !ok {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("Content-Length", fmt.Sprint(len(b)))
				case "GET":
					if q.URL.Query().Get("list-type") == "2" {
						fmt.Fprint(w, "<ListBucketResult>")
						for path, b := range objects {
							fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", strings.TrimPrefix(path, "/bucket/"), len(b))
						}
						fmt.Fprint(w, "</ListBucketResult>")
					} else {
						w.Write(objects[key])
					}
				default:
					t.Errorf("unexpected %s", q.Method)
				}
			}))
			defer server.Close()
			cfg := testS3Config()
			cfg.S3Endpoint = server.URL
			cfg.DBEngine = "mysql"
			cfg.S3UploadTimeout = time.Minute
			if e := uploadBackupDirectToS3(cfg, r.RunFolder); e != nil {
				t.Fatal(e)
			}
			committed, err := os.ReadFile(filepath.Join(r.RunFolder, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			r.Duration = "later execution duration"
			if err := persistFinalRunMetadata(r.RunFolder, r); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(r.RunFolder, "manifest.json"))
			if string(committed) != string(after) {
				t.Fatal("post-upload finalization changed manifest")
			}
			for key, payload := range objects {
				if strings.HasSuffix(key, "/manifest.json") && string(payload) != string(after) {
					t.Fatal("local and remote manifests differ")
				}
			}
			complete, part := false, false
			for key := range objects {
				complete = complete || strings.HasSuffix(key, "/COMPLETED.json")
				part = part || strings.HasSuffix(key, "/PARTIAL.json")
			}
			if complete == partial || part != partial {
				t.Fatalf("false completion: complete=%t partial=%t", complete, part)
			}
		})
	}
}
func TestS3GetChecksumMismatch(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "corrupt") }))
	defer s.Close()
	cfg := testS3Config()
	cfg.S3Endpoint = s.URL
	h := sha256.Sum256([]byte("correct"))
	if e := verifyRemotePayload(context.Background(), cfg, "key", 7, hex.EncodeToString(h[:])); e == nil {
		t.Fatal("remote corruption accepted")
	}
}
func TestScheduleDriftAndSystemScope(t *testing.T) {
	cfg := config{SystemdScope: "system", ServiceUnitName: "sdl-db-backup.service", TimerUnitName: "sdl-db-backup.timer", LogicalSchedule: "daily@02:00,06:00,12:00,16:00,18:00", PhysicalSchedule: "weekly@sun,02:00"}
	if !calendarDrift(cfg.LogicalSchedule, "02:00:00 06:00:00 12:00:00 18:00:00") {
		t.Fatal("missing 16 detected as matching")
	}
	render, e := RenderTimerFromConfig(cfg)
	if e != nil || !strings.Contains(render, "16:00:00") {
		t.Fatal(render, e)
	}
	if calendarConfigDrift(cfg, render) {
		t.Fatal("render drift")
	}
	scope, e := resolveSystemdScope(cfg)
	if e != nil || scope != "system" {
		t.Fatal(scope, e)
	}
	for _, a := range systemdArgs(scope, "show", cfg.ServiceUnitName) {
		if a == "--user" {
			t.Fatal("system deployment routed to user manager")
		}
	}
	cfg.SystemdScope = "garbage"
	if _, e = resolveSystemdScope(cfg); e == nil {
		t.Fatal("invalid scope silently accepted")
	}
}
func TestGlobalDumpUsesOneTransactionAndAllDatabases(t *testing.T) {
	args := buildGlobalMySQLDumpArgs([]string{"a", "b", "c", "d", "e", "f"}, false)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--single-transaction") || !strings.Contains(joined, "--databases a b c d e f") || strings.Contains(joined, "--force") {
		t.Fatal(joined)
	}
}
func TestManifestCrashRunningIsNotComplete(t *testing.T) {
	root := t.TempDir()
	r := runRecord{Status: "running", RunID: "test"}
	setManifestMetadata(config{}, &r)
	writeManifest(root, r)
	var loaded runRecord
	b, _ := os.ReadFile(filepath.Join(root, "manifest.json"))
	json.Unmarshal(b, &loaded)
	if loaded.RecoveryState != "RUNNING" {
		t.Fatal(loaded.RecoveryState)
	}
	if e := verifyLogicalCapture(config{}, loaded, root); e == nil {
		t.Fatal("interrupted capture complete")
	}
}
func TestInvalidViewRefusesBeforeDump(t *testing.T) {
	root := t.TempDir()
	client := filepath.Join(root, "mysql")
	script := `#!/bin/sh
case "$*" in
 *"SHOW FIELDS"*) echo "ERROR 1356 (HY000): View 'd.broken' references invalid table(s)" >&2; exit 1;;
 *) echo broken;;
esac
`
	os.WriteFile(client, []byte(script), 0700)
	r := dumpWithRetry(config{DBEngine: "mysql", MySQLBin: client, PreflightTimeout: time.Second, RetryCount: 3}, "d", filepath.Join(root, "d.sql.gz"), nil)
	if r.Status != "failed" || r.Attempts != 0 || r.ErrorCategory != "schema" {
		t.Fatalf("view bypassed: %+v", r)
	}
}

type fixtureDDLGuard struct{}

func (fixtureDDLGuard) Check() error { return nil }
func (fixtureDDLGuard) Close()       {}
func TestGlobalCaptureAllSixInOneProcess(t *testing.T) {
	original := acquireGlobalDDLGuard
	acquireGlobalDDLGuard = func(config) (ddlGuard, error) { return fixtureDDLGuard{}, nil }
	defer func() { acquireGlobalDDLGuard = original }()
	root := t.TempDir()
	mysql := filepath.Join(root, "mysql")
	dump := filepath.Join(root, "mysqldump")
	calls := filepath.Join(root, "calls")
	os.WriteFile(mysql, []byte(`#!/bin/sh
case "$*" in
 *"SELECT TABLE_NAME FROM information_schema.VIEWS"*) exit 0;;
 *"COUNT("*) echo 0;;
 *"information_schema.ROUTINES"*|*"information_schema.TRIGGERS"*|*"information_schema.EVENTS"*|*"information_schema.VIEWS"*) exit 0;;
 *) echo t;;
esac
`), 0700)
	var sqlFixture strings.Builder
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		fmt.Fprintf(&sqlFixture, "USE `%s`;\nCREATE TABLE `t` (id int);\n", name)
	}
	sqlFixture.WriteString("-- Dump completed on fixture\n")
	os.WriteFile(dump, []byte("#!/bin/sh\nprintf 'call\n' >> '"+calls+"'\ncat <<'FIXTURE'\n"+sqlFixture.String()+"FIXTURE\n"), 0700)

	cfg := config{DBEngine: "mysql", MySQLBin: mysql, MySQLDumpBin: dump, PreflightTimeout: time.Second, LogicalTimeoutPerDB: time.Second, RetryCount: 1, LogicalConsistency: "global", LogicalDatabases: []string{"a", "b", "c", "d", "e", "f"}}
	got, e := captureGlobalLogical(cfg, root, cfg.LogicalDatabases)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(calls)
	if strings.Count(string(b), "call") != 1 {
		t.Fatal("multiple snapshot processes")
	}
	if len(got) != 6 {
		t.Fatal("missing required databases")
	}
	for _, d := range got {
		if d.Status != "success" || d.OutputPath != got[0].OutputPath {
			t.Fatalf("bad global result %+v", d)
		}
	}
	record := runRecord{Status: "success", DatabasesTotal: 6, DatabasesSucceeded: 6, ExpectedDatabases: cfg.LogicalDatabases, Results: got, ConsistencyMethod: "single mysqldump transaction across all selected InnoDB databases; DDL excluded by policy"}
	if e := verifyLogicalCapture(cfg, record, root); e != nil {
		t.Fatal(e)
	}
}
func TestDumpInterruptionDoesNotPublishArtifact(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "mysqldump")
	os.WriteFile(bin, []byte("#!/bin/sh\necho partial\nexit 1\n"), 0700)
	path := filepath.Join(root, "d.sql.gz")
	_, e := dumpDatabase(config{DBEngine: "mysql", MySQLDumpBin: bin, LogicalTimeoutPerDB: time.Second}, "d", path, nil, nil)
	if e == nil {
		t.Fatal("interrupted dump successful")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("false final artifact")
	}
	files, _ := filepath.Glob(path + ".incomplete-*")
	if len(files) != 1 {
		t.Fatal("partial preservation failed")
	}
}
func TestPhysicalInventoryUsesSourceChecksums(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			prefix := "backup/run/physical.xbstream"
			names := []string{"xtrabackup_checkpoints.00000000000000000000", "xtrabackup_info.00000000000000000000", "backup-my.cnf.00000000000000000000", "xtrabackup_tablespaces.00000000000000000000"}
			body := []byte("source chunk")
			sum := md5.Sum(body)
			var md5body strings.Builder
			inventory := s3PrefixInfo{}
			for _, name := range names {
				fmt.Fprintf(&md5body, "%x  %s\n", sum, name)
				inventory.Objects = append(inventory.Objects, remoteArtifact{Key: prefix + "/" + name, SizeBytes: int64(len(body))})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, ".md5") {
					io.WriteString(w, md5body.String())
				} else if corrupt {
					io.WriteString(w, "bad contents")
				} else {
					w.Write(body)
				}
			}))
			defer server.Close()
			cfg := testS3Config()
			cfg.S3Endpoint = server.URL
			got, e := verifyPhysicalInventory(context.Background(), cfg, prefix, inventory)
			if corrupt {
				if e == nil {
					t.Fatal("corrupt physical chunks verified")
				}
			} else {
				if e != nil || len(got) != 4 || got[0].SHA256 == "" {
					t.Fatal(got, e)
				}
			}
		})
	}
}
func TestS3PaginationBeyondThousand(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, "<ListBucketResult>")
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprint(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>second</NextContinuationToken>")
			for i := 0; i < 1000; i++ {
				fmt.Fprintf(w, "<Contents><Key>p/%d</Key><Size>1</Size></Contents>", i)
			}
		} else {
			fmt.Fprint(w, "<Contents><Key>p/1000</Key><Size>1</Size></Contents>")
		}
		fmt.Fprint(w, "</ListBucketResult>")
	}))
	defer server.Close()
	cfg := testS3Config()
	cfg.S3Endpoint = server.URL
	r, e := listS3Prefix(context.Background(), cfg, "p")
	if e != nil || r.ObjectCount != 1001 || calls != 2 {
		t.Fatal(r.ObjectCount, calls, e)
	}
}
func TestScheduleApplyRollsBackOnReloadFailure(t *testing.T) {
	root := t.TempDir()
	env := filepath.Join(root, ".env")
	timer := filepath.Join(root, "timer.d", "managed.conf")
	os.MkdirAll(filepath.Dir(timer), 0700)
	oldEnv := []byte("BACKUP_LOGICAL_SCHEDULE=daily@02:00\nCUSTOM=keep\n")
	oldTimer := []byte("[Timer]\nOnCalendar=*-*-* 02:00:00\n")
	os.WriteFile(env, oldEnv, 0600)
	os.WriteFile(timer, oldTimer, 0644)
	calls := 0
	run := func(args ...string) (string, error) {
		calls++
		if args[0] == "show" {
			return "ActiveState=active", nil
		}
		if args[0] == "daemon-reload" && calls == 2 {
			return "", fmt.Errorf("authorization denied")
		}
		return "", nil
	}
	cfg := config{LogicalSchedule: "daily@02:00,16:00", TimerUnitName: "sdl-db-backup.timer"}
	e := applyScheduleTransaction(env, cfg, timer, "[Timer]\nOnCalendar=*-*-* 16:00:00\n", run, func(string) error { return nil })
	if e == nil {
		t.Fatal("failed apply reported success")
	}
	a, _ := os.ReadFile(env)
	b, _ := os.ReadFile(timer)
	if string(a) != string(oldEnv) || string(b) != string(oldTimer) {
		t.Fatal("rollback did not restore files")
	}
}
func TestScheduleApplyVerifiesEffectiveState(t *testing.T) {
	root := t.TempDir()
	env := filepath.Join(root, ".env")
	path := filepath.Join(root, "dropin")
	os.WriteFile(env, []byte("CUSTOM=keep\n"), 0600)
	cfg := config{LogicalSchedule: "daily@02:00,16:00", TimerUnitName: "sdl-db-backup.timer"}
	calls := 0
	run := func(args ...string) (string, error) {
		calls++
		if args[0] == "show" && calls == 1 {
			return "ActiveState=inactive", nil
		}
		return "TimersCalendar={OnCalendar=*-*-* 02:00:00} {OnCalendar=*-*-* 16:00:00}", nil
	}
	if e := applyScheduleTransaction(env, cfg, path, "[Timer]\nOnCalendar=*-*-* 02:00:00\nOnCalendar=*-*-* 16:00:00\n", run, func(string) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if !fileExists(env + ".schedule-audit") {
		t.Fatal("applied schedule lacks audit")
	}
}
func TestEncryptionKeyMasked(t *testing.T) {
	v := MaskedConfigView(config{EncryptionKey: "sensitive-test-key"})
	if v["BACKUP_ENCRYPTION_KEY"] != "********" {
		t.Fatal("encryption key exposed")
	}
}
func TestManifestRequiredInventoryCannotShrink(t *testing.T) {
	r := completeFixture(t, t.TempDir(), "2026-10-10_12-00-00", 2)
	cfg := config{DBEngine: "mysql", LogicalDatabases: []string{"db0", "db1", "db2"}}
	if e := verifyLogicalCapture(cfg, r, r.RunFolder); e == nil {
		t.Fatal("shrinking required inventory allowed")
	}
}

func TestMissingSchemaObjectsCannotComplete(t *testing.T) {
	r := completeFixture(t, t.TempDir(), "2026-10-10_12-00-00", 1)
	r.Results[0].ExpectedObjects = map[string][]string{"view": {"required_view"}}
	if e := verifyLogicalCapture(config{DBEngine: "mysql"}, r, r.RunFolder); e == nil {
		t.Fatal("missing required view accepted")
	}
}
func TestSchemaInventoryRecognizesVersionedTriggers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "d.sql.gz")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	io.WriteString(gz, "CREATE TABLE `t` (id int);\n/*!50003 CREATE*/ /*!50017 DEFINER=`owner`@`%`*/ /*!50003 TRIGGER t_BU BEFORE UPDATE ON t FOR EACH ROW SET NEW.id=OLD.id */;;\n-- Dump completed on fixture\n")
	gz.Close()
	f.Close()
	got, e := artifactObjectInventory(path, "d", "")
	if e != nil || len(got["d"]["trigger"]) != 1 {
		t.Fatal(got, e)
	}
}

func TestStatementTimeoutIsDeterministic(t *testing.T) {
	e := fmt.Errorf("mysqldump: Error 3024: Query execution was interrupted, maximum statement execution time exceeded")
	if category := classifyFailure(e, e.Error()); category != "config" || shouldRetry(category) {
		t.Fatal(category, "server limit must not be retried")
	}
}

func TestVersionedDropStatementsDoNotInventObjects(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "d.sql.gz")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	io.WriteString(gz, "/*!50001 DROP VIEW IF EXISTS `v`*/;\n/*!50003 DROP TRIGGER IF EXISTS `tr` */;\nCREATE TABLE `t` (id int);\n/*!50001 VIEW `v` AS SELECT id FROM t */;\n-- Dump completed on fixture\n")
	gz.Close()
	f.Close()
	got, e := artifactObjectInventory(path, "d", "")
	if e != nil || len(got["d"]["view"]) != 1 || len(got["d"]["trigger"]) != 0 || slices.Contains(got["d"]["view"], "IF") {
		t.Fatal(got, e)
	}
}

func TestFailedCaptureCannotBeReconciled(t *testing.T) {
	root := t.TempDir()
	r := completeFixture(t, root, "2026-10-10_12-00-00", 2)
	r.Status = "failed"
	r.FailureReason = "captured inventory mismatch"
	r.Results[1].Status = "failed"
	r.DatabasesFailed = 1
	r.DatabasesSucceeded = 1
	writeManifest(r.RunFolder, r)
	cfg := config{DBEngine: "mysql", BackupDir: root, LockFile: filepath.Join(root, "lock")}
	if _, e := ReconcileCapturedLogicalRun(cfg, r.RunID); e == nil {
		t.Fatal("failed database manufactured complete capture")
	}
}
func TestLogicalValidationIsNotRestoreEvidence(t *testing.T) {
	r := runRecord{Status: "success", ValidationMode: "logical validation", ValidationStatus: "success"}
	setManifestMetadata(config{}, &r)
	if r.RestoreTestState != "UNVERIFIED" {
		t.Fatal("file validation represented as restore")
	}
}
func TestSharedArtifactHashesCannotDisagree(t *testing.T) {
	r := completeFixture(t, t.TempDir(), "2026-10-10_12-00-00", 2)
	r.Results[1].OutputPath = r.Results[0].OutputPath
	r.Results[1].SizeBytes = r.Results[0].SizeBytes
	r.Results[1].SQLSHA256 = r.Results[0].SQLSHA256
	r.Results[1].ArtifactSHA256 = strings.Repeat("0", 64)
	if e := verifyLogicalCapture(config{DBEngine: "mysql"}, r, r.RunFolder); e == nil {
		t.Fatal("inconsistent shared hashes accepted")
	}
}

func TestS3ErrorsRedactProviderPayload(t *testing.T) {
	e := &s3HTTPStatusError{operation: "upload", status: 403, body: `<Error><Code>AccessDenied</Code><AWSAccessKeyId>sensitive-key</AWSAccessKeyId><Message>private data</Message></Error>`}
	if strings.Contains(e.Error(), "sensitive-key") || strings.Contains(e.Error(), "private data") {
		t.Fatal("provider payload exposed")
	}
}
func TestUnverifiedRemoteCannotBecomeComplete(t *testing.T) {
	r := runRecord{Status: "success", DatabasesTotal: 6, DatabasesSucceeded: 6}
	setManifestMetadata(config{RequireRemoteCompletion: true, LogicalS3UploadEnabled: true, S3UploadMode: "direct"}, &r)
	if r.RecoveryState != "RUNNING" {
		t.Fatal("completion claimed before remote checks")
	}
	r.LogicalUploadStatus = "success"
	setManifestMetadata(config{RequireRemoteCompletion: true, LogicalS3UploadEnabled: true, S3UploadMode: "direct"}, &r)
	if r.RecoveryState != "COMPLETE" {
		t.Fatal("verified remote run not complete")
	}
}

func TestUnverifiedInventoryIsNotGreenLogicalHealth(t *testing.T) {
	root := t.TempDir()
	r := runRecord{FormatVersion: 2, Status: "failed", DatabasesTotal: 6, DatabasesSucceeded: 6, FailureReason: "captured schema mismatch"}
	s := buildFinalBackupMetricsSnapshot(config{MetricsFile: filepath.Join(root, "metrics")}, r, time.Now(), true, false, true, true)
	if s.LogicalStatus != 0 {
		t.Fatal("schema verification failure reported healthy")
	}
}

func TestInterruptedMultipartJournalAbortsOnlyOwnedUpload(t *testing.T) {
	var aborted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" || r.URL.Query().Get("uploadId") != "owned-upload" {
			t.Errorf("unsafe request %s", r.URL)
		}
		aborted = true
		w.WriteHeader(204)
	}))
	defer server.Close()
	cfg := testS3Config()
	cfg.S3Endpoint = server.URL
	path := filepath.Join(t.TempDir(), "upload.json")
	if e := saveMultipartJournal(path, cfg, "run/file", "owned-upload"); e != nil {
		t.Fatal(e)
	}
	if e := recoverMultipartJournal(context.Background(), cfg, path, "wrong-destination"); e == nil || aborted {
		t.Fatal("destination mismatch aborted upload")
	}
	if e := recoverMultipartJournal(context.Background(), cfg, path, "run/file"); e != nil || !aborted {
		t.Fatal(e)
	}
	if fileExists(path) {
		t.Fatal("resolved journal not cleared")
	}
}

func TestEffectiveCalendarPreservesAllEntriesAndDetectsExtraOrWrongZone(t *testing.T) {
	cfg := config{LogicalSchedule: "daily@02:00,16:00", PhysicalSchedule: "weekly@sun,02:00", ScheduleTimezone: "Europe/Berlin"}
	output := "TimersCalendar={ OnCalendar=*-*-* 02:00:00 Europe/Berlin ; next_elapse=x }\nTimersCalendar={ OnCalendar=*-*-* 16:00:00 Europe/Berlin ; next_elapse=x }\nTimersCalendar={ OnCalendar=Sun *-*-* 02:00:00 Europe/Berlin ; next_elapse=x }"
	state := parseSystemdShow(output, "test.timer")
	if calendarConfigDrift(cfg, state.Calendar) {
		t.Fatal("lost effective calendar rows")
	}
	if !calendarConfigDrift(cfg, strings.ReplaceAll(state.Calendar, "Europe/Berlin", "UTC")) {
		t.Fatal("missed timezone drift")
	}
	if !calendarConfigDrift(cfg, state.Calendar+" {OnCalendar=*-*-* 08:00:00 Europe/Berlin}") {
		t.Fatal("missed extra unwanted trigger")
	}
	if !calendarConfigDrift(cfg, strings.ReplaceAll(state.Calendar, "Sun *-*-* 02:00:00 Europe/Berlin", "Mon *-*-* 02:00:00 Europe/Berlin")) {
		t.Fatal("missed physical schedule drift")
	}
}

func TestAtomicReplacementPreservesOwnerAndRefusesOwnershipFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := atomicWriteFile(path, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	ownerBefore := before.Sys().(*syscall.Stat_t)
	ownerAfter := after.Sys().(*syscall.Stat_t)
	if ownerBefore.Uid != ownerAfter.Uid || ownerBefore.Gid != ownerAfter.Gid {
		t.Fatal("settings owner changed")
	}
	saved := atomicFileChown
	atomicFileChown = func(*os.File, int, int) error { return syscall.EPERM }
	defer func() { atomicFileChown = saved }()
	if err := atomicWriteFile(path, []byte("broken"), 0600); err == nil {
		t.Fatal("ownership failure was ignored")
	}
	contents, _ := os.ReadFile(path)
	if string(contents) != "after" {
		t.Fatal("failed ownership check replaced original")
	}
}
func TestInProgressCapacityReflectsLiveFilesystem(t *testing.T) {
	cfg := config{BackupDir: t.TempDir(), MetricsFile: filepath.Join(t.TempDir(), "empty.prom"), LogicalEnabled: true, StorageEstimateBytes: 1 << 20, StorageReserveBytes: 1 << 20}
	snapshot := buildInProgressBackupMetricsSnapshot(cfg, time.Now())
	if snapshot.CapacityAvailable == 0 || snapshot.CapacityRequired == 0 || snapshot.CapacitySufficient != 1 {
		t.Fatal("running metrics report zero capacity", snapshot)
	}
}

func TestFinalizationCannotMutateCommittedManifest(t *testing.T) {
	folder := t.TempDir()
	record := completeFixture(t, folder, "2026-10-12_17-26-25", 6)
	original, err := os.ReadFile(filepath.Join(record.RunFolder, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(original)
	if err := persistManifestCommit(record.RunFolder, record.RunID, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	record.Duration = "12m"
	if err := persistFinalRunMetadata(record.RunFolder, record); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(record.RunFolder, "manifest.json"))
	if string(original) != string(after) {
		t.Fatal("finalization changed committed manifest")
	}
	if _, err := os.Stat(filepath.Join(record.RunFolder, "execution-finalization.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(record.RunFolder, record); err == nil {
		t.Fatal("other mutation path bypassed immutability")
	}
	if err := os.WriteFile(filepath.Join(record.RunFolder, "manifest.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := persistFinalRunMetadata(record.RunFolder, record); err == nil {
		t.Fatal("corrupt committed metadata accepted")
	}
}
