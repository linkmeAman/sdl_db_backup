package backupapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func expireFixtureS3(t *testing.T, s3 *storageS3Fixture, through int) {
	t.Helper()
	s3.mu.Lock()
	defer s3.mu.Unlock()
	for key := range s3.objects {
		for day := 1; day <= through; day++ {
			if strings.HasPrefix(key, fmt.Sprintf("logical/2026-10-%02d_12-00-00/", day)) {
				delete(s3.objects, key)
			}
		}
	}
}

func TestStorageCountExpiryAfterIndependentS3Retention(t *testing.T) {
	cfg, v, m, points, s3 := storagePoints(t, 8)
	cfg.StorageMaxLocalBackups = 5
	m.cfg = cfg
	v.available = 9000
	expireFixtureS3(t, s3, 6)
	m.remote = verifyStorageRemote
	m.remoteLogical = verifyStorageRemoteLogical
	m.identity = checkStorageRemoteIdentity

	plan, err := m.ensure(context.Background(), "", true, true)
	if err != nil {
		t.Fatal(err)
	}
	eligible := 0
	for _, d := range plan.Decisions {
		if d.Action == "ELIGIBLE" && d.Type == "expired_complete_logical" {
			eligible++
		}
	}
	if eligible != 18 { // Three old runs, six separate database artifacts each.
		t.Fatalf("dry-run expiry candidates=%d want=18", eligible)
	}
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 8 {
		t.Fatal("dry run deleted data")
	}
	report, err := m.ensure(context.Background(), "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 5 || report.LocalBackupCount != 5 || report.State != "CLEANED" {
		t.Fatalf("configured count not enforced: count=%d report=%+v", count, report)
	}
	for day := 1; day <= 8; day++ {
		id := fmt.Sprintf("2026-10-%02d_12-00-00", day)
		if day <= 3 {
			if fileExists(points[id].RunFolder) {
				t.Fatalf("expired folder remains: %s", id)
			}
		} else {
			assertPointPresent(t, points[id])
		}
	}
	if len(report.LocalWithoutOffsite) != 3 { // The retained fourth, fifth and sixth.
		t.Fatalf("offsite gaps include deleted runs: %v", report.LocalWithoutOffsite)
	}
	audits, _ := filepath.Glob(filepath.Join(cfg.LogDir, "storage-deletions", "*.json"))
	deletions := 0
	for _, path := range audits {
		data, _ := os.ReadFile(path)
		var audit storageDeletionAudit
		if json.Unmarshal(data, &audit) == nil && audit.Outcome == "DELETED" {
			deletions++
			if audit.Decision.Type != "expired_complete_logical" || len(audit.SupersedingRemoteProofs) != 2 {
				t.Fatal("count-expiry audit lacks newer offsite protection")
			}
		}
	}
	if deletions != 18 {
		t.Fatalf("payload deletion audit count=%d", deletions)
	}
	again, err := m.ensure(context.Background(), "", true, false)
	if err != nil || again.RemovedBytes != 0 || len(again.LocalWithoutOffsite) != 3 {
		t.Fatalf("repeat cleanup lost gaps or removed more data: %+v %v", again, err)
	}

	// The next capture makes room first, then finishes at the configured count.
	next := "2026-10-09_12-00-00"
	if _, err := m.ensure(context.Background(), next, true, false); err != nil {
		t.Fatal(err)
	}
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 4 {
		t.Fatalf("next capture did not reserve one slot: %d", count)
	}
	point := storageFixture(t, cfg.BackupDir, next, 6)
	s3.add(t, point)
	if count, _ := localPayloadRunCount(cfg.BackupDir); count != 5 {
		t.Fatalf("completed next capture count=%d", count)
	}
}

func TestStorageCountExpiryPreservesProtectedAndIncompleteRuns(t *testing.T) {
	for _, kind := range []string{"pin", "restore-tested", "partial", "database-scope", "pitr", "active", "corrupt-commit"} {
		t.Run(kind, func(t *testing.T) {
			cfg, v, m, points, s3 := storagePoints(t, 6)
			cfg.StorageMaxLocalBackups = 5
			m.cfg = cfg
			v.available = 9000
			expireFixtureS3(t, s3, 4)
			old := points["2026-10-01_12-00-00"]
			switch kind {
			case "pin":
				os.WriteFile(filepath.Join(old.RunFolder, ".recovery-pin"), []byte("keep"), 0600)
			case "restore-tested":
				old.ValidationStatus = "success"
				old.ValidationMode = "sandbox restore test"
			case "partial":
				old.Status = "partial"
				old.RecoveryState = "PARTIAL"
				// A uniquely useful partial must not match a newer fixture payload.
				for i := range old.Results {
					old.Results[i].SQLSHA256 = strings.Repeat("a", 64)
				}
			case "database-scope":
				old.LogicalBackupScope = "selected tables"
			case "pitr":
				old.MySQLPITR = &mysqlPITRMetadata{}
			case "active":
				old.PID = os.Getpid()
			}
			if err := writeManifest(old.RunFolder, old); err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt-commit" {
				if err := persistManifestCommit(old.RunFolder, old.RunID, strings.Repeat("0", 64)); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = m.ensure(context.Background(), "", true, false)
			assertPointPresent(t, old)
		})
	}
}

func TestStorageCountExpiryRequiresNewerValidRecoveryWindow(t *testing.T) {
	for _, kind := range []string{"older-offsite", "different-database-offsite", "partial-offsite", "changed-offsite", "damaged-newer-local"} {
		t.Run(kind, func(t *testing.T) {
			cfg, v, m, points, s3 := storagePoints(t, 6)
			cfg.StorageMaxLocalBackups = 5
			m.cfg = cfg
			v.available = 9000
			expireFixtureS3(t, s3, 4)
			old := points["2026-10-01_12-00-00"]
			switch kind {
			case "older-offsite":
				expireFixtureS3(t, s3, 6)
				for _, id := range []string{"2026-09-29_12-00-00", "2026-09-30_12-00-00"} {
					point := storageFixture(t, t.TempDir(), id, 6)
					points[id] = point
					s3.add(t, point)
				}
			case "different-database-offsite":
				verify := m.remote
				m.remote = func(ctx context.Context, cfg config, id string) (StorageRemoteProof, error) {
					proof, err := verify(ctx, cfg, id)
					proof.Manifest.Results[0].Name = "different_database"
					return proof, err
				}
			case "partial-offsite":
				verify := m.remote
				m.remote = func(ctx context.Context, cfg config, id string) (StorageRemoteProof, error) {
					proof, err := verify(ctx, cfg, id)
					proof.LogicalOnly = true
					return proof, err
				}
			case "changed-offsite":
				m.identity = func(context.Context, config, StorageRemoteProof) error { return fmt.Errorf("offsite object replaced") }
			case "damaged-newer-local":
				os.WriteFile(points["2026-10-06_12-00-00"].Results[0].OutputPath, []byte("corrupt"), 0600)
			}
			_, _ = m.ensure(context.Background(), "", true, false)
			assertPointPresent(t, old)
		})
	}
}
