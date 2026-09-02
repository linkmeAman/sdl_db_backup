package backupapp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileReplacesCompleteContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "schedule.json")
	if err := atomicWriteFile(path, []byte("first"), 0o640); err != nil {
		t.Fatalf("first atomic write: %v", err)
	}
	if err := atomicWriteFile(path, []byte("second"), 0o640); err != nil {
		t.Fatalf("second atomic write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read atomic result: %v", err)
	}
	if string(data) != "second" {
		t.Fatalf("unexpected atomic content %q", data)
	}
}
