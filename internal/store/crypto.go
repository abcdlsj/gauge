package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// encPrefix marks a value that went through AES-GCM before hitting the DB.
const encPrefix = "enc1:"

// LoadKey resolves the at-rest encryption key. Precedence:
//  1. GAUGE_SECRET_KEY (hashed, so any passphrase length works)
//  2. an existing key file next to the database
//  3. a freshly generated 32-byte key written to that file with mode 0600
func LoadKey(path string) ([]byte, error) {
	if env := os.Getenv("GAUGE_SECRET_KEY"); env != "" {
		sum := sha256.Sum256([]byte(env))
		return sum[:], nil
	}
	if raw, err := os.ReadFile(path); err == nil && len(raw) >= 32 {
		key := make([]byte, 32)
		copy(key, raw)
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	return key, nil
}

// box encrypts and decrypts small strings with AES-256-GCM.
type box struct{ aead cipher.AEAD }

func newBox(key []byte) (*box, error) {
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &box{aead: aead}, nil
}

func (b *box) seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(out), nil
}

func (b *box) open(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// sensitive reports whether a settings key must be encrypted at rest.
func sensitive(key string) bool {
	k := strings.ToLower(key)
	for _, needle := range []string{"token", "cookie", "secret", "key", "json", "password", "webhook"} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}
