package backupapp

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyCommittedManifest(t *testing.T) {
	folder := t.TempDir()
	body := []byte(`{"run_id":"r1"}` + "\n")
	if err := os.WriteFile(filepath.Join(folder, "manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if err := persistManifestCommit(folder, "r1", hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommittedManifest(folder); err != nil {
		t.Fatalf("matching manifest rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folder, "manifest.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCommittedManifest(folder); err == nil {
		t.Fatal("changed manifest accepted")
	}
}
