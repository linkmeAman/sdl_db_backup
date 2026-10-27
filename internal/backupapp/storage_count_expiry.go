package backupapp

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Explicit count retention can expire complete historical snapshots even when
// an independent S3 policy has already expired their exact remote counterparts.
// This never authorizes pressure-only deletion, incomplete captures, or losing
// the configured newer local window and two newer complete offsite points.
func storageCompleteLogicalRecord(cfg config, r runRecord) bool {
	if r.FormatVersion < 2 || r.Status != "success" || r.RecoveryState != "COMPLETE" || r.FailureReason != "" || r.DatabasesFailed != 0 || r.MySQLPITR != nil || r.LogicalBackupScope != "" || len(r.LogicalOmissions) != 0 || len(cfg.LogicalDatabases) == 0 {
		return false
	}
	if cfg.LogicalConsistency == "global" && !strings.HasPrefix(r.ConsistencyMethod, "single mysqldump transaction across all selected InnoDB") {
		return false
	}
	n := len(cfg.LogicalDatabases)
	if len(r.Results) != n || r.DatabasesTotal != n || r.DatabasesSucceeded != n {
		return false
	}
	seen := map[string]bool{}
	for _, d := range r.Results {
		if !slices.Contains(cfg.LogicalDatabases, d.Name) || seen[d.Name] || d.Status != "success" || len(d.OmittedObjects) != 0 || len(d.SelectedTables) != 0 || len(d.ExpectedObjects) == 0 || len(d.ArtifactSHA256) != 64 || len(d.SQLSHA256) != 64 || d.SizeBytes <= 0 || !isStoragePayload(filepath.Base(d.OutputPath)) {
			return false
		}
		seen[d.Name] = true
	}
	return true
}

func (m *storageManager) authorizeCountExpiry(d StorageDecision, proofs map[string]StorageRemoteProof, report StorageReport) error {
	limit := report.LocalBackupLimit
	if m.cfg.StorageMaxLocalBackups < 3 || limit < 2 || limit > m.cfg.StorageMaxLocalBackups || slices.Contains(report.ProtectedLocal, d.RunID) {
		return fmt.Errorf("count-expiry policy is disabled or candidate is protected")
	}
	count, err := localPayloadRunCount(m.cfg.BackupDir)
	if err != nil {
		return err
	}
	if count <= limit {
		return fmt.Errorf("configured local count already satisfied")
	}
	folder := filepath.Join(m.cfg.BackupDir, d.RunID)
	record, err := readManifest(folder)
	if err != nil {
		return err
	}
	if record.RunID != d.RunID || !storageCompleteLogicalRecord(m.cfg, record) || !referencedStoragePayload(record, d) || fileExists(filepath.Join(folder, ".recovery-pin")) || runHasSuccessfulRestoreValidation(folder) {
		return fmt.Errorf("count-expiry candidate is incomplete, pinned, scoped or a recovery dependency")
	}
	if record.PID > 0 {
		if stale, _ := isLockStale(fmt.Sprintf("pid=%d", record.PID)); !stale {
			return fmt.Errorf("count-expiry candidate is still active")
		}
	}
	if _, err := os.Stat(filepath.Join(folder, "manifest-commit.json")); err == nil {
		if err = verifyCommittedManifest(folder); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := m.checkProtected(report); err != nil {
		return err
	}
	newerLocal := 0
	for _, id := range report.ProtectedLocal {
		if id > d.RunID {
			newerLocal++
		}
	}
	if newerLocal < limit {
		return fmt.Errorf("configured newer verified local window is not preserved")
	}
	newerRemote := 0
	seen := map[string]bool{}
	for _, id := range report.ProtectedRemote {
		proof, ok := proofs[id]
		if ok && !seen[id] && id > d.RunID && proof.RunID == id && proof.State == "VERIFIED" && !proof.LogicalOnly && proof.Manifest.RunID == id && storageCompleteLogicalRecord(m.cfg, proof.Manifest) {
			newerRemote++
			seen[id] = true
		}
	}
	if newerRemote < 2 {
		return fmt.Errorf("count expiry requires two newer verified complete offsite points")
	}
	return nil
}
