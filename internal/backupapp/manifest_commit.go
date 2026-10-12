package backupapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type manifestCommit struct {
	RunID  string `json:"run_id"`
	SHA256 string `json:"sha256"`
}

func persistManifestCommit(folder, runID, hash string) error {
	data, err := json.Marshal(manifestCommit{RunID: runID, SHA256: hash})
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(folder, "manifest-commit.json"), append(data, '\n'), 0600)
}

// The manifest referenced by the remote completion marker is immutable.
// Later execution bookkeeping belongs in the run index and a separate audit.
func persistFinalRunMetadata(folder string, record runRecord) error {
	data, err := os.ReadFile(filepath.Join(folder, "manifest-commit.json"))
	if os.IsNotExist(err) {
		return writeManifest(folder, record)
	}
	if err != nil {
		return err
	}
	var commit manifestCommit
	if err = json.Unmarshal(data, &commit); err != nil {
		return err
	}
	manifest, err := os.ReadFile(filepath.Join(folder, "manifest.json"))
	if err != nil {
		return err
	}
	hash := sha256.Sum256(manifest)
	if commit.RunID != record.RunID || commit.SHA256 != hex.EncodeToString(hash[:]) {
		return fmt.Errorf("committed manifest checksum mismatch; refusing metadata replacement")
	}
	audit, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(folder, "execution-finalization.json"), append(audit, '\n'), 0600)
}
