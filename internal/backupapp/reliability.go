package backupapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RecoveryState versions the stronger contract while preserving legacy status.
// RUNNING -> FAILED/PARTIAL/COMPLETE. Restore evidence is always independent.
func recoveryState(r runRecord) string {
	if r.Status == "running" {
		return "RUNNING"
	}
	if r.Status == "success" && r.FailureReason == "" && r.DatabasesFailed == 0 && len(r.LogicalOmissions) == 0 && r.LogicalBackupScope == "" {
		return "COMPLETE"
	}
	if r.Status == "partial" {
		return "PARTIAL"
	}
	return "FAILED"
}

func readManifest(folder string) (runRecord, error) {
	var r runRecord
	b, e := os.ReadFile(filepath.Join(folder, "manifest.json"))
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}

// verifyLogicalCapture refuses counters without a matching expected inventory.
// Legacy successful manifests may be read but still must prove their artifacts.
func verifyLogicalCapture(cfg config, r runRecord, folder string) error {
	if r.Status != "success" || r.FailureReason != "" || r.DatabasesFailed != 0 || len(r.LogicalOmissions) > 0 || r.LogicalBackupScope != "" {
		return fmt.Errorf("logical capture is %s, not complete", r.Status)
	}
	if cfg.LogicalConsistency == "global" && !strings.HasPrefix(r.ConsistencyMethod, "single mysqldump transaction across all selected InnoDB") {
		return fmt.Errorf("capture lacks the required shared transaction snapshot")
	}
	expected := r.ExpectedDatabases
	if len(expected) == 0 {
		expected = cfg.LogicalDatabases
	}
	if len(expected) == 0 {
		for _, d := range r.Results {
			expected = append(expected, d.Name)
		}
	}
	if len(expected) == 0 || len(expected) != len(r.Results) || r.DatabasesTotal != len(expected) || r.DatabasesSucceeded != len(expected) {
		return fmt.Errorf("expected database inventory does not match capture results")
	}
	if len(cfg.LogicalDatabases) > 0 {
		if len(cfg.LogicalDatabases) != len(expected) {
			return fmt.Errorf("manifest differs from required production database inventory")
		}
		for _, name := range cfg.LogicalDatabases {
			if !slices.Contains(expected, name) {
				return fmt.Errorf("required database %q missing from manifest", name)
			}
		}
	}
	seen := map[string]bool{}
	verified := map[string]bool{}
	artifactHashes := map[string]string{}
	sqlHashes := map[string]string{}
	inventories := map[string]map[string]map[string][]string{}
	for _, d := range r.Results {
		if !slices.Contains(expected, d.Name) || seen[d.Name] || d.Status != "success" || len(d.OmittedObjects) > 0 || len(d.SelectedTables) > 0 {
			return fmt.Errorf("incomplete or unexpected database %q", d.Name)
		}
		seen[d.Name] = true
		if len(d.ArtifactSHA256) != 64 || d.SQLSHA256 == "" || d.SizeBytes <= 0 {
			return fmt.Errorf("database %q lacks integrity evidence", d.Name)
		}
		path := logicalArtifactPath(folder, d)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("database %q artifact missing: %w", d.Name, err)
		}
		if !info.Mode().IsRegular() || info.Size() != d.SizeBytes {
			return fmt.Errorf("database %q artifact size or type mismatch", d.Name)
		}
		if verified[path] && (artifactHashes[path] != d.ArtifactSHA256 || sqlHashes[path] != d.SQLSHA256) {
			return fmt.Errorf("shared artifact has inconsistent manifest hashes for database %s", d.Name)
		}
		if r.FormatVersion >= 2 && len(d.ExpectedObjects) == 0 {
			return fmt.Errorf("version 2 capture lacks required object inventory for database %s", d.Name)
		}
		if !verified[path] {
			ok, err := validateLogicalArtifactForEngineWithEncryption(cfg.DBEngine, path, d.ArtifactSHA256, d.SQLSHA256, cfg.EncryptionKey)
			if err != nil || !ok {
				return fmt.Errorf("database %q artifact invalid: %v", d.Name, err)
			}
			parsed, e := artifactObjectInventory(path, d.Name, cfg.EncryptionKey)
			if e != nil {
				return e
			}
			inventories[path] = parsed
			verified[path] = true
			artifactHashes[path] = d.ArtifactSHA256
			sqlHashes[path] = d.SQLSHA256
		}
		if (cfg.DBEngine == "mysql" || cfg.DBEngine == "") && len(d.ExpectedObjects) > 0 {
			if e := compareObjectInventory(d.Name, d.ExpectedObjects, inventories[path][d.Name]); e != nil {
				return e
			}
		}
	}
	return nil
}

func setManifestMetadata(cfg config, r *runRecord) {
	r.FormatVersion = 2
	r.RecoveryState = recoveryState(*r)
	r.BackupType = "logical"
	if r.PhysicalBackup != nil {
		r.BackupType = "logical+physical"
	}
	if len(r.Results) == 0 && r.PhysicalBackup != nil {
		r.BackupType = "physical"
	}
	r.Compression = "gzip"
	r.Encrypted = cfg.EncryptionKey != ""
	r.S3Destination = "s3://" + cfg.S3Bucket + "/" + strings.Trim(cfg.S3LogicalPrefix, "/") + "/" + r.RunID
	r.RemoteIntegrityState = "UNVERIFIED"
	if r.LogicalUploadStatus == "success" && verifiedUploadMode(cfg) {
		r.RemoteIntegrityState = "VERIFIED"
	}
	if r.ConsistencyMethod == "" {
		r.ConsistencyMethod = "per-database single transaction; no cross-database snapshot; concurrent DDL excluded by policy"
	}
	r.RestoreTestState = "UNVERIFIED"
	if r.ValidationMode == "sandbox restore test" && r.ValidationStatus != "" {
		r.RestoreTestState = strings.ToUpper(r.ValidationStatus)
	}
	if cfg.RequireRemoteCompletion && cfg.LogicalS3UploadEnabled && r.DatabasesTotal > 0 && r.Status == "success" && r.RemoteIntegrityState != "VERIFIED" {
		r.RecoveryState = "RUNNING"
	}
	if r.Status != "running" {
		r.EndedAt = time.Now().UTC()
	}
}

// ReconcileCapturedLogicalRun rechecks a preserved capture after an orchestration
// failure. It never turns failed database results into success. The prior
// manifest is preserved, and publication still requires the complete gate.
func ReconcileCapturedLogicalRun(cfg Config, runID string) (RunResult, error) {
	release, e := acquireLock(cfg.LockFile)
	if e != nil {
		return RunResult{}, e
	}
	defer release()
	folder := filepath.Join(cfg.BackupDir, runID)
	if filepath.Base(runID) != runID {
		return RunResult{}, fmt.Errorf("invalid run ID")
	}
	r, e := readManifest(folder)
	if e != nil {
		return r, e
	}
	if r.RunID != runID {
		return r, fmt.Errorf("manifest identity mismatch")
	}
	if r.PhysicalBackup != nil {
		return r, fmt.Errorf("combined physical runs need separate policy review")
	}
	if r.Status != "failed" || !strings.HasPrefix(r.FailureReason, "captured ") {
		return r, fmt.Errorf("only preserved capture-inventory failures can be reconciled")
	}
	original, e := os.ReadFile(filepath.Join(folder, "manifest.json"))
	if e != nil {
		return r, e
	}
	probe := r
	probe.Status = "success"
	probe.FailureReason = ""
	if e = verifyLogicalCapture(cfg, probe, folder); e != nil {
		return r, e
	}
	if e = atomicWriteFile(filepath.Join(folder, "manifest-before-reconciliation.json"), original, 0600); e != nil {
		return r, e
	}
	r = probe
	r.LocalCaptureIntegrityState = "VERIFIED"
	r.Duration = time.Since(r.Timestamp).Round(time.Millisecond).String()
	r.LogicalUploadStatus = ""
	r.RemoteIntegrityState = "UNVERIFIED"
	setManifestMetadata(cfg, &r)
	if e = writeManifest(folder, r); e != nil {
		return r, e
	}
	if e = uploadBackupDirectToS3(cfg, folder); e != nil {
		markLogicalUploadFailure(&r, e)
		_ = writeManifest(folder, r)
		return r, e
	}
	markLogicalUploadSuccess(&r)
	r.Duration = time.Since(r.Timestamp).Round(time.Millisecond).String()
	setManifestMetadata(cfg, &r)
	if e = persistFinalRunMetadata(folder, r); e != nil {
		return r, e
	}
	emitFinalBackupMetrics(cfg, r, r.Timestamp, true, false, true, true)
	if e = appendRunRecord(cfg.RunLogPath, r); e != nil {
		return r, e
	}
	audit := struct {
		RunID  string    `json:"run_id"`
		At     time.Time `json:"at"`
		Reason string    `json:"reason"`
		State  string    `json:"state"`
	}{runID, time.Now().UTC(), "capture and remote inventory independently reverified after parser correction", "COMPLETE"}
	b, _ := json.MarshalIndent(audit, "", "  ")
	if e = atomicWriteFile(filepath.Join(folder, "reconciliation-audit.json"), b, 0600); e != nil {
		return r, e
	}
	return r, nil
}
