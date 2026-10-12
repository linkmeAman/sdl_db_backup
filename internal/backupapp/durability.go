package backupapp

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// atomicWriteFile makes metadata updates durable enough for interruption-safe
// scheduling and recovery metadata: readers see either the previous complete
// file or the newly synced complete file, never a partially written document.
var atomicFileChown = func(file *os.File, uid, gid int) error { return file.Chown(uid, gid) }

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	previous, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	if previous != nil {
		if owner, ok := previous.Sys().(*syscall.Stat_t); ok {
			if err := atomicFileChown(temp, int(owner.Uid), int(owner.Gid)); err != nil {
				_ = temp.Close()
				cleanup()
				return fmt.Errorf("preserve metadata ownership: %w", err)
			}
		}
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		cleanup()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanup()
		return err
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open metadata directory for sync: %w", err)
	}
	defer dirHandle.Close()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync metadata directory: %w", err)
	}
	return nil
}
