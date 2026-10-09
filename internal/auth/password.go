// Admin account credentials for the dashboard (p6.auth-sessions).
//
// Passwords are hashed with PBKDF2-HMAC-SHA256 from the Go 1.24 standard
// library (crypto/pbkdf2) — no third-party crypto, per the
// security-engineer charter. The stored envelope is
//
//	pbkdf2-sha256$<iterations>$<salt-base64url>$<hash-base64url>
//
// with a 16-byte random salt and a 32-byte derived key. Verification is
// constant-time and parameter-aware (legacy rounds can be raised by
// re-hashing on the next successful login).
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Password hashing parameters. DefaultPasswordIterations follows the
// OWASP 2023 guidance for PBKDF2-HMAC-SHA256 (600k); verification honors
// the parameters stored in the envelope so hashes age gracefully.
const (
	DefaultPasswordIterations = 600_000
	passwordSaltBytes         = 16
	passwordKeyBytes          = 32
)

// HashPassword derives and seals a password into the storage envelope.
func HashPassword(password string, iterations int) (string, error) {
	if iterations <= 0 {
		iterations = DefaultPasswordIterations
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: password salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, passwordKeyBytes)
	if err != nil {
		return "", fmt.Errorf("auth: pbkdf2 derive: %w", err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		iterations,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a stored envelope in constant
// time. Malformed envelopes fail closed (false, error).
func VerifyPassword(stored, password string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false, fmt.Errorf("auth: malformed password envelope")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false, fmt.Errorf("auth: malformed password envelope")
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false, fmt.Errorf("auth: malformed password salt")
	}
	want, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("auth: malformed password hash")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false, fmt.Errorf("auth: pbkdf2 derive: %w", err)
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyEnvelopeSalt is a fixed, non-secret salt for timing equalization.
const dummyEnvelopeSalt = "onegate-timing-equalizer"

// BurnPasswordWork performs a PBKDF2 round of the given iteration count
// against a fixed dummy salt. Call it on the unknown-username login path
// (with the same iteration count the real hashes use) so response timing
// does not reveal which usernames exist.
func BurnPasswordWork(password string, iterations int) {
	if iterations <= 0 {
		iterations = DefaultPasswordIterations
	}
	_, _ = pbkdf2.Key(sha256.New, password, []byte(dummyEnvelopeSalt), iterations, passwordKeyBytes)
}
