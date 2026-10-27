package backupapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type storageMetadataAudit struct {
	RunID   string                     `json:"run_id"`
	Root    string                     `json:"root"`
	Device  uint64                     `json:"device"`
	Inode   uint64                     `json:"inode"`
	At      time.Time                  `json:"at"`
	Outcome string                     `json:"outcome"`
	Files   map[string]StorageDecision `json:"metadata_files"`
}

func storageMetadataName(name string) bool {
	switch name {
	case "manifest.json", "manifest-commit.json", "execution-finalization.json", "manifest-finalization-audit.json", "local-storage-state.json":
		return true
	}
	return false
}

// Called only under the shared backup and retention locks. No recursive deletion
// is used: every remaining entry must be known, regular metadata on this volume.
func pruneStorageRunMetadata(cfg config, root, id string, dry bool) (bool, error) {
	if _, err := time.Parse(runTimestampLayout, id); err != nil {
		return false, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(rootFD)
	dirFD, err := unix.Openat(rootFD, id, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	directory := os.NewFile(uintptr(dirFD), id)
	defer directory.Close()
	var dirStat, rootStat unix.Stat_t
	if err = unix.Fstat(dirFD, &dirStat); err != nil {
		return false, err
	}
	if err = unix.Fstat(rootFD, &rootStat); err != nil || dirStat.Dev != rootStat.Dev {
		return false, fmt.Errorf("metadata directory on another filesystem")
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !storageMetadataName(entry.Name()) {
			return false, nil // Includes payloads, temporary dumps, pins and physical data.
		}
	}
	auditPath := filepath.Join(cfg.LogDir, "storage-deletions", id+"-metadata.json")
	var previous storageMetadataAudit
	data, auditErr := os.ReadFile(auditPath)
	if auditErr == nil {
		if json.Unmarshal(data, &previous) != nil || previous.Outcome != "METADATA_INTENT" || previous.Root != root || previous.RunID != id || previous.Device != uint64(dirStat.Dev) || previous.Inode != dirStat.Ino {
			return false, fmt.Errorf("metadata directory differs from its deletion journal")
		}
	} else if !os.IsNotExist(auditErr) {
		return false, auditErr
	}
	audit := storageMetadataAudit{RunID: id, Root: root, Device: uint64(dirStat.Dev), Inode: dirStat.Ino, At: time.Now().UTC(), Outcome: "METADATA_INTENT", Files: map[string]StorageDecision{}}
	var manifest []byte
	for _, entry := range entries {
		name := entry.Name()
		fd, e := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			return false, e
		}
		file := os.NewFile(uintptr(fd), name)
		var st unix.Stat_t
		e = unix.Fstat(fd, &st)
		if e != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Dev != dirStat.Dev || st.Size > 16<<20 {
			file.Close()
			return false, fmt.Errorf("unsafe metadata file")
		}
		contents, e := io.ReadAll(file)
		file.Close()
		if e != nil {
			return false, e
		}
		hash := sha256.Sum256(contents)
		identity := StorageDecision{Name: name, Device: uint64(st.Dev), Inode: st.Ino, Size: st.Size, Modified: st.Mtim.Nano(), Changed: st.Ctim.Nano(), SHA256: hex.EncodeToString(hash[:])}
		if auditErr == nil {
			old, exists := previous.Files[name]
			if !exists || old != identity {
				return false, fmt.Errorf("metadata changed after interrupted deletion")
			}
		}
		audit.Files[name] = identity
		if name == "manifest.json" {
			manifest = contents
		}
	}
	if manifest != nil {
		var record runRecord
		if json.Unmarshal(manifest, &record) != nil || record.RunID != id || record.Status == "running" || record.RecoveryState == "RUNNING" || record.MySQLPITR != nil || (record.ValidationStatus == "success" && record.ValidationMode == "sandbox restore test") {
			return false, nil
		}
		if record.PID > 0 {
			if stale, _ := isLockStale(fmt.Sprintf("pid=%d", record.PID)); !stale {
				return false, nil
			}
		}
	} else if auditErr != nil {
		return false, nil // Missing manifests require an existing matching deletion intent.
	}
	if dry {
		return true, nil
	}
	writeAudit := func() error {
		encoded, e := json.MarshalIndent(audit, "", "  ")
		if e != nil {
			return e
		}
		return atomicWriteFile(auditPath, append(encoded, '\n'), 0600)
	}
	if err = writeAudit(); err != nil {
		return false, err
	}
	for name, expected := range audit.Files {
		var named unix.Stat_t
		if err = unix.Fstatat(dirFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return false, err
		}
		if named.Mode&unix.S_IFMT != unix.S_IFREG || named.Nlink != 1 || uint64(named.Dev) != expected.Device || named.Ino != expected.Inode || named.Size != expected.Size || named.Mtim.Nano() != expected.Modified || named.Ctim.Nano() != expected.Changed {
			return false, fmt.Errorf("metadata name changed before deletion")
		}
		if err = unix.Unlinkat(dirFD, name, 0); err != nil {
			return false, err
		}
	}
	if err = unix.Fsync(dirFD); err != nil {
		return false, err
	}
	var named unix.Stat_t
	if err = unix.Fstatat(rootFD, id, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Ino != dirStat.Ino || named.Dev != dirStat.Dev {
		return false, fmt.Errorf("metadata directory name changed")
	}
	if err = unix.Unlinkat(rootFD, id, unix.AT_REMOVEDIR); err != nil {
		return false, err
	}
	if err = unix.Fsync(rootFD); err != nil {
		return true, err
	}
	audit.Outcome = "METADATA_REMOVED"
	audit.At = time.Now().UTC()
	return true, writeAudit()
}

func pruneStorageMetadata(cfg config, current string, dry bool) ([]string, error) {
	root, err := canonicalStorageRoot(cfg.BackupDir)
	if err != nil {
		return nil, err
	}
	release, err := acquireLock(filepath.Join(root, ".retention.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || entry.Name() == filepath.Base(current) {
			continue
		}
		if _, e := time.Parse(runTimestampLayout, entry.Name()); e != nil {
			continue
		}
		pruned, e := pruneStorageRunMetadata(cfg, root, entry.Name(), dry)
		if e != nil {
			log.Printf("obsolete metadata folder held run=%s reason=%v", entry.Name(), e)
			continue
		}
		if pruned {
			removed = append(removed, entry.Name())
		}
	}
	return removed, nil
}
