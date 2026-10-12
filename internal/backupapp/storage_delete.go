package backupapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func canonicalStorageRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if abs != resolved || abs == "/" {
		return "", fmt.Errorf("storage root must be an explicit non-symlink backup directory")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("storage root is not a directory")
	}
	return abs, nil
}
func storageFileIdentity(path string) (StorageDecision, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return StorageDecision{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 {
		return StorageDecision{}, fmt.Errorf("non-regular or hard-linked storage artifact")
	}
	return StorageDecision{Path: path, Name: info.Name(), Size: info.Size(), Device: uint64(stat.Dev), Inode: stat.Ino, Modified: info.ModTime().UnixNano(), Changed: stat.Ctim.Nano()}, nil
}
func scanStorageRuns(root, current string) ([]storageRun, []StorageDecision, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	var runs []storageRun
	var held []StorageDecision
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		at, err := time.Parse(runTimestampLayout, entry.Name())
		if err != nil {
			continue
		}
		folder := filepath.Join(root, entry.Name())
		record, err := readManifest(folder)
		if err != nil || record.RunID != entry.Name() || record.FormatVersion < 2 {
			held = append(held, StorageDecision{RunID: entry.Name(), Path: folder, Action: "HOLD", Reason: "missing, mismatched or legacy manifest"})
			continue
		}
		active := false
		if record.PID > 0 {
			stale, _ := isLockStale(fmt.Sprintf("pid=%d", record.PID))
			active = !stale
		}
		if entry.Name() == filepath.Base(current) || record.Status == "running" || record.RecoveryState == "RUNNING" || active {
			held = append(held, StorageDecision{RunID: entry.Name(), Path: folder, Action: "KEEP", Reason: "current or in-progress run"})
			continue
		}
		children, e := os.ReadDir(folder)
		if e != nil {
			return nil, nil, e
		}
		r := storageRun{r: record, folder: folder, at: at}
		for _, child := range children {
			if !isStoragePayload(child.Name()) && !storageTemporaryName(child.Name()) {
				continue
			}
			file, e := storageFileIdentity(filepath.Join(folder, child.Name()))
			if e != nil {
				held = append(held, StorageDecision{RunID: entry.Name(), Path: filepath.Join(folder, child.Name()), Action: "HOLD", Reason: e.Error()})
				continue
			}
			file.RunID = entry.Name()
			file.Type = record.BackupType
			for _, d := range record.Results {
				if filepath.Base(d.OutputPath) == file.Name {
					file.SHA256 = d.ArtifactSHA256
				}
			}
			r.files = append(r.files, file)
		}
		runs = append(runs, r)
	}
	slices.SortFunc(runs, func(a, b storageRun) int { return b.at.Compare(a.at) })
	return runs, held, nil
}

// All operations are anchored to O_NOFOLLOW directory descriptors. Manifests,
// keys, physical streams, directories and files outside the exact run are not
// candidates, even if a path is swapped for a symlink before restart.
func unlinkStoragePayload(root string, expected StorageDecision) (int64, error) {
	if _, err := time.Parse(runTimestampLayout, expected.RunID); err != nil {
		return 0, err
	}
	if (!isStoragePayload(expected.Name) && !storageTemporaryName(expected.Name)) || expected.Path != filepath.Join(root, expected.RunID, expected.Name) {
		return 0, fmt.Errorf("unsafe storage deletion path")
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(rootFD)
	var rootStat unix.Stat_t
	if err = unix.Fstat(rootFD, &rootStat); err != nil {
		return 0, err
	}
	dirFD, err := unix.Openat(rootFD, expected.RunID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(dirFD)
	var pin unix.Stat_t
	if e := unix.Fstatat(dirFD, ".recovery-pin", &pin, unix.AT_SYMLINK_NOFOLLOW); e == nil || e != unix.ENOENT {
		return 0, fmt.Errorf("recovery pin present or unreadable")
	}
	fileFD, err := unix.Openat(dirFD, expected.Name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fileFD), expected.Name)
	defer file.Close()
	var stat unix.Stat_t
	if err = unix.Fstat(fileFD, &stat); err != nil {
		return 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || uint64(stat.Dev) != expected.Device || uint64(stat.Dev) != uint64(rootStat.Dev) || stat.Ino != expected.Inode || stat.Size != expected.Size || stat.Mtim.Nano() != expected.Modified || stat.Ctim.Nano() != expected.Changed {
		return 0, fmt.Errorf("storage candidate replaced, changed or on another filesystem")
	}
	if expected.SHA256 != "" {
		h := sha256.New()
		if _, err = io.Copy(h, file); err != nil {
			return 0, err
		}
		if hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
			return 0, fmt.Errorf("local checksum changed before deletion")
		}
	}
	var named unix.Stat_t
	if err = unix.Fstatat(dirFD, expected.Name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, err
	}
	if named.Ino != stat.Ino || named.Dev != stat.Dev || named.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0, fmt.Errorf("candidate name changed before unlink")
	}
	if err = unix.Unlinkat(dirFD, expected.Name, 0); err != nil {
		return 0, err
	}
	if err = unix.Fsync(dirFD); err != nil {
		return stat.Size, err
	}
	return stat.Size, nil
}

type storageDeletionAudit struct {
	Decision        StorageDecision     `json:"decision"`
	At              time.Time           `json:"at"`
	Outcome         string              `json:"outcome"`
	RemovedBytes    int64               `json:"removed_payload_bytes"`
	ReclaimedBytes  uint64              `json:"measured_reclaimed_bytes"`
	ProtectedLocal  []string            `json:"protected_local"`
	ProtectedRemote []string            `json:"protected_remote"`
	RemoteProof     *StorageRemoteProof `json:"remote_verification,omitempty"`
	Error           string              `json:"error,omitempty"`
}

func auditPath(cfg config, d StorageDecision) string {
	h := sha256.Sum256([]byte(d.Path))
	return filepath.Join(cfg.LogDir, "storage-deletions", d.RunID+"-"+hex.EncodeToString(h[:8])+".json")
}
func writeStorageAudit(cfg config, a storageDeletionAudit) error {
	b, e := json.MarshalIndent(a, "", "  ")
	if e != nil {
		return e
	}
	return atomicWriteFile(auditPath(cfg, a.Decision), append(b, '\n'), 0600)
}
func (m *storageManager) checkProtected(r StorageReport) error {
	if len(r.ProtectedLocal) < 1 || len(r.ProtectedRemote) < 2 {
		return fmt.Errorf("recovery protection invariant failed")
	}
	for _, id := range r.ProtectedLocal {
		for _, original := range m.protectedFiles[id] {
			current, e := storageFileIdentity(original.Path)
			if e != nil {
				return fmt.Errorf("protected local point changed: %w", e)
			}
			if current.Inode != original.Inode || current.Device != original.Device || current.Size != original.Size || current.Modified != original.Modified || current.Changed != original.Changed {
				return fmt.Errorf("protected local point changed during cleanup")
			}
		}
		if len(m.protectedFiles[id]) == 0 {
			return fmt.Errorf("protected local point lacks inspected artifacts")
		}
	}
	return nil
}
func (m *storageManager) deleteCandidate(ctx context.Context, root string, d StorageDecision, proofs map[string]StorageRemoteProof, r *StorageReport, logical bool) error {
	if err := m.checkProtected(*r); err != nil {
		return err
	}
	if slices.Contains(r.ProtectedLocal, d.RunID) {
		return fmt.Errorf("cannot delete protected local recovery point")
	}
	record, e := readManifest(filepath.Join(root, d.RunID))
	if e != nil {
		return e
	}
	if record.RunID != d.RunID || record.Status == "running" || record.RecoveryState == "RUNNING" || runHasSuccessfulRestoreValidation(filepath.Join(root, d.RunID)) {
		return fmt.Errorf("candidate is now protected or in progress")
	}
	for _, id := range r.ProtectedRemote {
		if e = m.identity(ctx, m.cfg, proofs[id]); e != nil {
			return fmt.Errorf("protected offsite recovery point changed: %w", e)
		}
	}
	audit := storageDeletionAudit{Decision: d, At: m.now().UTC(), Outcome: "INTENT", ProtectedLocal: append([]string(nil), r.ProtectedLocal...), ProtectedRemote: append([]string(nil), r.ProtectedRemote...)}
	if d.RemoteRunID != "" {
		proof := proofs[d.RemoteRunID]
		if e = m.identity(ctx, m.cfg, proof); e != nil {
			return e
		}
		audit.RemoteProof = &proof
	} else if d.Type != "disposable_failed_temporary" {
		return fmt.Errorf("candidate has no verified offsite counterpart")
	}
	before, e := m.capacity(m.cfg, logical)
	_ = e
	if e = writeStorageAudit(m.cfg, audit); e != nil {
		return fmt.Errorf("cannot persist deletion intent: %w", e)
	}
	removed, e := m.remove(root, d)
	audit.RemovedBytes = removed
	after, _ := m.capacity(m.cfg, logical)
	r.Final = after
	if after.AvailableBytes > before.AvailableBytes {
		audit.ReclaimedBytes = after.AvailableBytes - before.AvailableBytes
	}
	if e != nil {
		audit.Outcome = "FAILED"
		audit.Error = e.Error()
	} else {
		audit.Outcome = "DELETED"
		r.RemovedBytes += uint64(max(0, removed))
		r.ReclaimedBytes += audit.ReclaimedBytes
	}
	if writeErr := writeStorageAudit(m.cfg, audit); writeErr != nil {
		return fmt.Errorf("deletion outcome persistence failed; retained intent permits restart audit: %w", writeErr)
	}
	if e != nil {
		return e
	}
	updateStorageDecision(r, d.Path, "DELETED", d.Reason)
	log.Printf("automatic local cleanup run=%s type=%s path=%s removed=%d reclaimed=%d reason=%s remote=%s", d.RunID, d.Type, d.Path, removed, audit.ReclaimedBytes, d.Reason, d.Remote)
	// Recovery metadata remains in the original run directory. Never rewrite the
	// immutable manifest or remove an entire backup folder.
	eviction := struct {
		At     time.Time `json:"at"`
		File   string    `json:"removed_file"`
		Remote string    `json:"remote"`
		Audit  string    `json:"audit"`
	}{m.now().UTC(), d.Name, d.Remote, auditPath(m.cfg, d)}
	b, _ := json.Marshal(eviction)
	return atomicWriteFile(filepath.Join(root, d.RunID, "local-storage-state.json"), append(b, '\n'), 0600)
}
func (m *storageManager) recoverIntents(ctx context.Context, root string, candidates []StorageDecision, proofs map[string]StorageRemoteProof, r *StorageReport, logical bool) error {
	paths, e := filepath.Glob(filepath.Join(m.cfg.LogDir, "storage-deletions", "*.json"))
	if e != nil {
		return e
	}
	for _, path := range paths {
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var audit storageDeletionAudit
		if e = json.Unmarshal(b, &audit); e != nil {
			return fmt.Errorf("invalid deletion journal preserved for review")
		}
		if audit.Outcome != "INTENT" {
			continue
		}
		d := audit.Decision
		if d.Path != filepath.Join(root, d.RunID, d.Name) || filepath.Base(d.Name) != d.Name {
			return fmt.Errorf("unsafe interrupted deletion journal")
		}
		if _, e = os.Lstat(d.Path); os.IsNotExist(e) {
			audit.Outcome = "RECOVERED_ALREADY_REMOVED"
			audit.At = m.now().UTC()
			if e = writeStorageAudit(m.cfg, audit); e != nil {
				return e
			}
			continue
		}
		eligible := false
		for _, candidate := range candidates {
			if candidate.Path == d.Path && candidate.Inode == d.Inode && candidate.Device == d.Device && candidate.Modified == d.Modified && candidate.Changed == d.Changed {
				eligible = true
				d = candidate
				break
			}
		}
		if !eligible {
			audit.Outcome = "HELD_AFTER_RESTART"
			audit.Error = "eligibility or inode changed; retained artifact"
			if e = writeStorageAudit(m.cfg, audit); e != nil {
				return e
			}
			continue
		}
		// Do not resume unnecessary deletions merely because an old intent exists.
		capacity, e := m.capacity(m.cfg, logical)
		if e == nil && capacity.AvailableBytes >= r.TargetBytes {
			audit.Outcome = "CANCELLED_TARGET_MET"
			if e = writeStorageAudit(m.cfg, audit); e != nil {
				return e
			}
			continue
		}
		if _, e = m.authorizeCandidate(ctx, d, proofs, r); e != nil {
			return e
		}
		if e = m.deleteCandidate(ctx, root, d, proofs, r, logical); e != nil {
			return e
		}
	}
	return nil
}
