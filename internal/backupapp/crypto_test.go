package backupapp

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testEncryptionKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAuthenticatedEncryptionRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("recovery-critical-data\n"), 5000)
	var artifact bytes.Buffer
	writer, err := EncryptWriter(&artifact, testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptWriter: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write encrypted payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close encrypted payload: %v", err)
	}

	reader, err := DecryptReader(bytes.NewReader(artifact.Bytes()), testEncryptionKey)
	if err != nil {
		t.Fatalf("DecryptReader: %v", err)
	}
	actual, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read decrypted payload: %v", err)
	}
	if !bytes.Equal(actual, payload) {
		t.Fatalf("round trip mismatch")
	}
}

func TestAuthenticatedEncryptionDetectsTampering(t *testing.T) {
	var artifact bytes.Buffer
	writer, err := EncryptWriter(&artifact, testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptWriter: %v", err)
	}
	if _, err := writer.Write([]byte("important dump")); err != nil {
		t.Fatalf("write encrypted payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close encrypted payload: %v", err)
	}

	data := artifact.Bytes()
	data[len(data)-6] ^= 0x01
	reader, err := DecryptReader(bytes.NewReader(data), testEncryptionKey)
	if err != nil {
		t.Fatalf("DecryptReader: %v", err)
	}
	if _, err := io.ReadAll(reader); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected authentication failure, got %v", err)
	}
}

func TestEncryptionRejectsInvalidKey(t *testing.T) {
	if _, err := EncryptWriter(io.Discard, "too-short"); err == nil {
		t.Fatal("expected invalid key to be rejected")
	}
}

// The recorded artifact size must match the bytes on disk, which for encrypted
// artifacts include the encryption framing; verification compares the two.
func TestEncryptedDumpRecordsOnDiskSize(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"", testEncryptionKey} {
		out := filepath.Join(root, "d.sql.gz")
		if key != "" {
			out += ".enc"
		}
		cfg := config{DBEngine: "mysql", MySQLDumpBin: fakeMySQLDump(t, root, 1<<20), LogicalTimeoutPerDB: time.Minute, EncryptionKey: key}
		outcome, err := dumpDatabase(cfg, "d", out, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(out)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.sizeBytes != info.Size() {
			t.Fatalf("encrypted=%v recorded size %d, file has %d bytes", key != "", outcome.sizeBytes, info.Size())
		}
	}
}
