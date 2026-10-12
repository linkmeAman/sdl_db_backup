package backupapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupKeepsNewestRestoreVerifiedRun(t *testing.T) {
	root := t.TempDir()
	oldVerified := filepath.Join(root, "2025-01-01_00-00-00")
	newerUnverified := filepath.Join(root, "2025-01-02_00-00-00")
	for _, dir := range []string{oldVerified, newerUnverified} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	manifest := []byte(`{"validation_status":"success","validation_mode":"sandbox restore test"}`)
	if err := os.WriteFile(filepath.Join(oldVerified, "manifest.json"), manifest, 0o640); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := config{RetentionDaily: 0, RetentionWeekly: 0, RetentionMonthly: 0, RetentionMinVerified: 1}
	if err := cleanupOldBackups(root, "", cfg); err != nil {
		t.Fatalf("cleanupOldBackups: %v", err)
	}
	if _, err := os.Stat(oldVerified); err != nil {
		t.Fatalf("expected validated run to be retained: %v", err)
	}
	if _, err := os.Stat(newerUnverified); err != nil {
		t.Fatalf("expected unproven recovery value to be held, stat err=%v", err)
	}

}
