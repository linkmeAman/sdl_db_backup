package backupapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStorageMetadataPrunesOnlyObsoleteFolders(t *testing.T) {
	for _, kind := range []string{"obsolete", "payload", "temporary", "pin", "physical", "unknown", "symlink", "hardlink", "active", "restore-tested", "pitr", "current"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			cfg := storageTestConfig(t, root)
			id := "2026-10-14_16-00-01"
			folder := filepath.Join(root, id)
			os.Mkdir(folder, 0700)
			r := runRecord{RunID: id, Status: "success"}
			if kind == "active" {
				r.PID = os.Getpid()
			}
			if kind == "restore-tested" {
				r.ValidationStatus = "success"
				r.ValidationMode = "sandbox restore test"
			}
			if kind == "pitr" {
				r.MySQLPITR = &mysqlPITRMetadata{}
			}
			if err := writeManifest(folder, r); err != nil {
				t.Fatal(err)
			}
			for label, name := range map[string]string{"payload": "all-databases.sql.gz", "temporary": "all-databases.sql.gz.incomplete-20261014T160001.000000000", "pin": ".recovery-pin", "unknown": "keep.txt"} {
				if kind == label {
					os.WriteFile(filepath.Join(folder, name), []byte("keep"), 0600)
				}
			}
			if kind == "physical" {
				os.Mkdir(filepath.Join(folder, "physical"), 0700)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			os.WriteFile(outside, []byte("protected outside data"), 0600)
			if kind == "symlink" {
				os.Symlink(outside, filepath.Join(folder, "local-storage-state.json"))
			}
			if kind == "hardlink" {
				os.Link(outside, filepath.Join(folder, "local-storage-state.json"))
			}
			current := ""
			if kind == "current" {
				current = id
			}
			eligible, err := pruneStorageMetadata(cfg, current, true)
			if err != nil {
				t.Fatal(err)
			}
			if !fileExists(folder) {
				t.Fatal("dry run removed a folder")
			}
			removed, err := pruneStorageMetadata(cfg, current, false)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "obsolete" {
				if len(eligible) != 1 || len(removed) != 1 || fileExists(folder) {
					t.Fatal("obsolete folder was not removed")
				}
				data, err := os.ReadFile(filepath.Join(cfg.LogDir, "storage-deletions", id+"-metadata.json"))
				var audit storageMetadataAudit
				if err != nil || json.Unmarshal(data, &audit) != nil || audit.Outcome != "METADATA_REMOVED" || audit.Files["manifest.json"].SHA256 == "" {
					t.Fatal("central metadata deletion audit missing")
				}
			} else if len(eligible) != 0 || len(removed) != 0 || !fileExists(filepath.Join(folder, "manifest.json")) {
				t.Fatal("protected folder metadata was removed")
			}
			if data, _ := os.ReadFile(outside); string(data) != "protected outside data" {
				t.Fatal("outside file changed")
			}
		})
	}
}

func TestStorageMetadataResumesInterruptedRemoval(t *testing.T) {
	root := t.TempDir()
	cfg := storageTestConfig(t, root)
	id := "2026-10-14_16-00-01"
	folder := filepath.Join(root, id)
	os.Mkdir(folder, 0700)
	file := filepath.Join(folder, "local-storage-state.json")
	os.WriteFile(file, []byte("{}"), 0600)
	identity, err := storageFileIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	identity.Path = ""
	identity.SHA256, _ = fileSHA256(file)
	info, err := os.Stat(folder)
	if err != nil {
		t.Fatal(err)
	}
	// The manifest was already unlinked after a durable metadata intent.
	stat := info.Sys().(*syscall.Stat_t)
	audit := storageMetadataAudit{RunID: id, Root: root, Device: uint64(stat.Dev), Inode: stat.Ino, At: time.Now(), Outcome: "METADATA_INTENT", Files: map[string]StorageDecision{"local-storage-state.json": identity}}
	path := filepath.Join(cfg.LogDir, "storage-deletions", id+"-metadata.json")
	data, _ := json.Marshal(audit)
	if err := atomicWriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	removed, err := pruneStorageRunMetadata(cfg, root, id, false)
	if err != nil || !removed || fileExists(folder) {
		t.Fatalf("interrupted removal not recovered: removed=%v err=%v", removed, err)
	}
}
