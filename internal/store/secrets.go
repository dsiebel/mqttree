package store

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// Key seals saved broker passwords. It is a separate file from the database so
// a database copy alone does not disclose credentials.
type Key struct {
	aead cipher.AEAD
}

// LoadKey reads a 32 byte key from path. Accepts raw bytes, base64 (std or
// URL-safe, with or without padding) or hex. When the file does not exist the
// key is generated and written with mode 0600, which keeps a first manual start
// working; Ansible always pre-creates it so deploys never rotate it by accident.
func LoadKey(path string) (*Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read secret key: %w", err)
		}
		generated := make([]byte, chacha20poly1305.KeySize)
		if _, err := rand.Read(generated); err != nil {
			return nil, fmt.Errorf("generate secret key: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create secret key directory: %w", err)
		}
		if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(generated)), 0o600); err != nil {
			return nil, fmt.Errorf("write secret key: %w", err)
		}
		return newKey(generated)
	}
	material, err := decodeKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode secret key %s: %w", path, err)
	}
	return newKey(material)
}

func decodeKey(value string) ([]byte, error) {
	if len(value) == chacha20poly1305.KeySize*2 {
		if decoded, err := hex.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == chacha20poly1305.KeySize {
			return decoded, nil
		}
	}
	if len(value) == chacha20poly1305.KeySize {
		return []byte(value), nil
	}
	return nil, fmt.Errorf("expected %d bytes of key material", chacha20poly1305.KeySize)
}

func newKey(material []byte) (*Key, error) {
	aead, err := chacha20poly1305.NewX(material)
	if err != nil {
		return nil, fmt.Errorf("initialise cipher: %w", err)
	}
	return &Key{aead: aead}, nil
}

// Seal returns nonce||ciphertext.
func (k *Key) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return k.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal.
func (k *Key) Open(sealed []byte) ([]byte, error) {
	if len(sealed) < k.aead.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := sealed[:k.aead.NonceSize()], sealed[k.aead.NonceSize():]
	plaintext, err := k.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}
