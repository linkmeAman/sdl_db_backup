package backupapp

import (
	"encoding/json"
	"log"
	"path/filepath"
)

func cleanupOldBackups(backupDir, currentRun string, cfg config) error {
	cfg.BackupDir = backupDir
	if !cfg.RetentionDeleteEnabled {
		log.Print("retention deletion HOLD: BACKUP_RETENTION_DELETE_ENABLED=false; recovery points preserved")
		return nil
	}
	release, e := acquireLock(filepath.Join(backupDir, ".retention.lock"))
	if e != nil {
		return e
	}
	defer release()
	plan, e := PlanRetention(cfg, currentRun)
	if e != nil {
		return e
	}
	b, e := json.MarshalIndent(plan, "", "  ")
	if e != nil {
		return e
	}
	if e = atomicWriteFile(filepath.Join(cfg.LogDir, "retention-plan.json"), append(b, '\n'), 0600); e != nil {
		return e
	}
	return applyRetentionPlan(cfg, plan)
}
func runHasSuccessfulRestoreValidation(folder string) bool {
	r, e := readManifest(folder)
	return e == nil && r.Status == "success" && r.ValidationStatus == "success" && r.ValidationMode == "sandbox restore test"
}
