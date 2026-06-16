package backupapp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// EncryptWriter wraps an io.Writer with AES-256-CFB encryption.
// It generates a random IV, writes it to the underlying writer,
// and then encrypts all subsequent writes using the provided 32-byte key.
func EncryptWriter(w io.Writer, keyString string) (io.Writer, error) {
	key := []byte(keyString)
	if len(key) != 32 {
		// Pad or truncate to 32 bytes for AES-256
		fixedKey := make([]byte, 32)
		copy(fixedKey, key)
		key = fixedKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	// Write IV to the output first
	if _, err := w.Write(iv); err != nil {
		return nil, err
	}

	stream := cipher.NewCFBEncrypter(block, iv)
	return &cipher.StreamWriter{S: stream, W: w}, nil
}

// DecryptReader wraps an io.Reader with AES-256-CFB decryption.
// It reads the IV from the beginning of the stream and then decrypts
// all subsequent reads using the provided 32-byte key.
func DecryptReader(r io.Reader, keyString string) (io.Reader, error) {
	key := []byte(keyString)
	if len(key) != 32 {
		fixedKey := make([]byte, 32)
		copy(fixedKey, key)
		key = fixedKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(r, iv); err != nil {
		return nil, errors.New("failed to read IV: " + err.Error())
	}

	stream := cipher.NewCFBDecrypter(block, iv)
	return &cipher.StreamReader{S: stream, R: r}, nil
}
