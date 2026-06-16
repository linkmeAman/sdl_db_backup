package backupapp

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateGzipSQLFile(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.sql.gz")
	
	// Create a valid gzip file
	f, err := os.Create(validPath)
	if err != nil {
		t.Fatalf("failed to create valid file: %v", err)
	}
	gw := gzip.NewWriter(f)
	_, _ = gw.Write([]byte("CREATE TABLE foo (id INT);\n-- Dump completed on 2026-06-16 10:00:00\n"))
	gw.Close()
	f.Close()

	valid, err := validateGzipSQLFile(validPath)
	if err != nil {
		t.Errorf("expected valid file to pass, got error: %v", err)
	}
	if !valid {
		t.Errorf("expected valid file to return true")
	}

	invalidPath := filepath.Join(dir, "invalid.sql.gz")
	
	// Create an invalid gzip file (missing dump completed)
	f2, _ := os.Create(invalidPath)
	gw2 := gzip.NewWriter(f2)
	_, _ = gw2.Write([]byte("CREATE TABLE foo (id INT);\n-- Some incomplete dump\n"))
	gw2.Close()
	f2.Close()

	valid, err = validateGzipSQLFile(invalidPath)
	if err == nil {
		t.Errorf("expected invalid file to fail")
	}
	if valid {
		t.Errorf("expected invalid file to return false")
	}
}
