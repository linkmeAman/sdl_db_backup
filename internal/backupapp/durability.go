package backupapp

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile makes metadata updates durable enough for interruption-safe
// scheduling and recovery metadata: readers see either the previous complete
// file or the newly synced complete file, never a partially written document.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
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
