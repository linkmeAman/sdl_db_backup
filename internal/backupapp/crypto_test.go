package backupapp

import (
	"bytes"
	"io"
	"strings"
	"testing"
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
