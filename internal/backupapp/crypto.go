package backupapp

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	encryptedArtifactMagic     = "SDBE2"
	encryptedArtifactChunkSize = 64 * 1024
	maxEncryptedFrameSize      = encryptedArtifactChunkSize + 16
)

// decodeEncryptionKey accepts exactly 32 raw bytes or a 64-character
// hexadecimal representation. It never silently pads or truncates a key.
func decodeEncryptionKey(keyString string) ([]byte, error) {
	if len(keyString) == 32 {
		return []byte(keyString), nil
	}
	if len(keyString) == 64 {
		key, err := hex.DecodeString(keyString)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, fmt.Errorf("BACKUP_ENCRYPTION_KEY must be exactly 32 raw bytes or 64 hexadecimal characters")
}

// EncryptWriter writes versioned, chunked AES-256-GCM. Each frame is
// independently authenticated, so dumps can be streamed without buffering the
// full artifact and corruption is detected during validation or restore.
func EncryptWriter(w io.Writer, keyString string) (io.WriteCloser, error) {
	key, err := decodeEncryptionKey(keyString)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, encryptedArtifactMagic); err != nil {
		return nil, err
	}
	if _, err := w.Write(nonce); err != nil {
		return nil, err
	}
	return &encryptedArtifactWriter{writer: w, gcm: gcm, baseNonce: nonce}, nil
}

type encryptedArtifactWriter struct {
	writer    io.Writer
	gcm       cipher.AEAD
	baseNonce []byte
	counter   uint64
	closed    bool
}

func nonceForCounter(base []byte, counter uint64) []byte {
	nonce := append([]byte(nil), base...)
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], counter)
	return nonce
}

func frameAAD(counter uint64) []byte {
	value := make([]byte, len(encryptedArtifactMagic)+8)
	copy(value, encryptedArtifactMagic)
	binary.BigEndian.PutUint64(value[len(encryptedArtifactMagic):], counter)
	return value
}

func (w *encryptedArtifactWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("write to closed encrypted artifact")
	}
	written := 0
	for len(p) > 0 {
		chunkLen := min(len(p), encryptedArtifactChunkSize)
		chunk := p[:chunkLen]
		sealed := w.gcm.Seal(nil, nonceForCounter(w.baseNonce, w.counter), chunk, frameAAD(w.counter))
		if err := binary.Write(w.writer, binary.BigEndian, uint32(len(sealed))); err != nil {
			return written, err
		}
		if _, err := w.writer.Write(sealed); err != nil {
			return written, err
		}
		w.counter++
		written += chunkLen
		p = p[chunkLen:]
	}
	return written, nil
}

func (w *encryptedArtifactWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return binary.Write(w.writer, binary.BigEndian, uint32(0))
}

// DecryptReader opens the current authenticated format and supports the prior
// CFB format for migration. New artifacts are always authenticated.
func DecryptReader(r io.Reader, keyString string) (io.Reader, error) {
	reader := bufio.NewReader(r)
	header, err := reader.Peek(len(encryptedArtifactMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read encrypted artifact header: %w", err)
	}
	if bytes.Equal(header, []byte(encryptedArtifactMagic)) {
		if _, err := reader.Discard(len(encryptedArtifactMagic)); err != nil {
			return nil, err
		}
		key, err := decodeEncryptionKey(keyString)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		nonce := make([]byte, gcm.NonceSize())
		if _, err := io.ReadFull(reader, nonce); err != nil {
			return nil, fmt.Errorf("read encrypted artifact nonce: %w", err)
		}
		return &encryptedArtifactReader{reader: reader, gcm: gcm, baseNonce: nonce}, nil
	}

	// Read compatibility for older CFB artifacts. This path intentionally keeps
	// legacy key behavior only for decryption; new writes reject invalid keys.
	legacyKey := []byte(keyString)
	if len(legacyKey) != 32 {
		fixedKey := make([]byte, 32)
		copy(fixedKey, legacyKey)
		legacyKey = fixedKey
	}
	block, err := aes.NewCipher(legacyKey)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(reader, iv); err != nil {
		return nil, errors.New("failed to read legacy encryption IV: " + err.Error())
	}
	stream := cipher.NewCFBDecrypter(block, iv)
	return &cipher.StreamReader{S: stream, R: reader}, nil
}

type encryptedArtifactReader struct {
	reader    io.Reader
	gcm       cipher.AEAD
	baseNonce []byte
	counter   uint64
	current   *bytes.Reader
	done      bool
}

func (r *encryptedArtifactReader) Read(p []byte) (int, error) {
	for r.current == nil || r.current.Len() == 0 {
		if r.done {
			return 0, io.EOF
		}
		var frameLen uint32
		if err := binary.Read(r.reader, binary.BigEndian, &frameLen); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, errors.New("encrypted artifact is truncated before its final frame")
			}
			return 0, err
		}
		if frameLen == 0 {
			r.done = true
			return 0, io.EOF
		}
		if frameLen > maxEncryptedFrameSize {
			return 0, fmt.Errorf("encrypted artifact frame is too large: %d bytes", frameLen)
		}
		sealed := make([]byte, frameLen)
		if _, err := io.ReadFull(r.reader, sealed); err != nil {
			return 0, fmt.Errorf("encrypted artifact frame is truncated: %w", err)
		}
		plain, err := r.gcm.Open(nil, nonceForCounter(r.baseNonce, r.counter), sealed, frameAAD(r.counter))
		if err != nil {
			return 0, fmt.Errorf("encrypted artifact authentication failed: %w", err)
		}
		r.counter++
		r.current = bytes.NewReader(plain)
	}
	return r.current.Read(p)
}

func encryptedArtifactPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".enc")
}

// DecryptArtifactTo streams an encrypted artifact to w. It is used by the
// command-line recovery path and deliberately never exposes the key in logs.
func DecryptArtifactTo(r io.Reader, w io.Writer, keyString string) error {
	reader, err := DecryptReader(r, keyString)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, reader)
	return err
}
