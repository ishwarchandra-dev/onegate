// Package auth owns virtual keys, their hashing and verification (Phase 4),
// and the at-rest encryption of provider API keys (this file).
//
// Secrets model:
//   - A master secret (32 random bytes) lives in <data_dir>/master.key,
//     created 0600 on first start.
//   - Per-purpose AES-256-GCM keys are derived from the master secret via
//     HKDF-SHA256 (crypto/hkdf, stdlib since Go 1.24) so compromising one
//     purpose never exposes another.
//   - Ciphertext envelope: version byte 0x01 || 12-byte nonce || AES-GCM
//     ciphertext+tag. Tampering fails authentication (GCM), never
//     decrypts to garbage.
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Purpose identifies a derivation purpose. New purposes are cheap: just a
// distinct string. Known: "provider-keys".
type Purpose string

const (
	// PurposeProviderKeys derives the key encrypting provider API keys.
	PurposeProviderKeys Purpose = "onegate/provider-keys/v1"

	// PurposeVirtualKeyHash derives the HMAC pepper for virtual-key
	// hashes (keyhash.go).
	PurposeVirtualKeyHash Purpose = "onegate/vkey-hash/v1"

	// envelopeVersion is the current ciphertext envelope version byte.
	envelopeVersion byte = 0x01
)

// MasterSecret loads the master secret from path, generating and saving a
// random 32-byte secret when the file does not exist. The file is created
// with 0600 permissions; an existing file with wrong permissions is an
// error (fail closed — a world-readable master key is a security incident).
func MasterSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(data) != 32 {
			return nil, fmt.Errorf("auth: master secret %s: want 32 bytes, got %d", path, len(data))
		}
		return data, nil
	case errors.Is(err, os.ErrNotExist):
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("auth: generate master secret: %w", err)
		}
		if err := os.WriteFile(path, secret, 0o600); err != nil {
			return nil, fmt.Errorf("auth: write master secret: %w", err)
		}
		return secret, nil
	default:
		return nil, fmt.Errorf("auth: read master secret: %w", err)
	}
}

// deriveKey derives a 32-byte AES-256 key for the given purpose.
func deriveKey(master []byte, purpose Purpose) ([]byte, error) {
	if len(master) != 32 {
		return nil, errors.New("auth: master secret must be 32 bytes")
	}
	k, err := hkdf.Key(sha256.New, master, nil, string(purpose), 32)
	if err != nil {
		return nil, fmt.Errorf("auth: hkdf derive: %w", err)
	}
	return k, nil
}

// Cipher encrypts and decrypts secrets for one purpose.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher derives a Cipher for the purpose from the master secret.
func NewCipher(master []byte, purpose Purpose) (*Cipher, error) {
	key, err := deriveKey(master, purpose)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("auth: aes init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: gcm init: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext into the versioned envelope:
// 0x01 || nonce(12) || ciphertext+tag. Empty plaintext is allowed (a
// provider configured without a key); the envelope round-trips to "".
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	out := make([]byte, 0, 1+c.aead.NonceSize()+len(plaintext)+c.aead.Overhead())
	out = append(out, envelopeVersion)
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("auth: nonce: %w", err)
	}
	out = append(out, nonce...)
	return c.aead.Seal(out, nonce, plaintext, nil), nil
}

// Decrypt opens an envelope produced by Encrypt. Errors on: unknown
// version, wrong key, truncated input, or tampering — with distinct,
// non-oracle-leaking messages.
func (c *Cipher) Decrypt(envelope []byte) ([]byte, error) {
	const hdr = 1 + 12 // version + nonce
	if len(envelope) == 0 {
		return nil, errors.New("auth: empty envelope")
	}
	if envelope[0] != envelopeVersion {
		return nil, fmt.Errorf("auth: unsupported envelope version %#x", envelope[0])
	}
	if len(envelope) < hdr+c.aead.Overhead() {
		return nil, errors.New("auth: envelope too short")
	}
	nonce := envelope[1:hdr]
	ct := envelope[hdr:]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("auth: decrypt failed (wrong key or tampered data)")
	}
	return plain, nil
}

// MaskKey renders a provider API key for display: first 4 and last 4
// characters around an ellipsis; keys of 8 characters or fewer collapse
// to "****". Never logs or returns the full key.
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "…" + key[len(key)-4:]
}

// Pepper derives the virtual-key hash pepper from the master secret.
// Callers derive once at startup and hand the pepper to NewVerifier;
// hashes in storage are only comparable under the same derivation.
func Pepper(master []byte) ([]byte, error) {
	return deriveKey(master, PurposeVirtualKeyHash)
}
