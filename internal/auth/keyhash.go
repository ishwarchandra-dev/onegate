// Virtual key material: generation, hashing, and verification.
//
// Hash design: HMAC-SHA256 keyed by a gateway-local pepper (derived from
// master.key), rendered as hex. Rationale:
//
//   - Deterministic: storage looks keys up by hash (GetByHash), so the
//     hash must be computable without knowing the row. Salted-per-key or
//     memory-hard formats cannot serve as a lookup key.
//   - Fast: verification sits on the proxy hot path; the Phase 3 gate
//     budgets < 10ms TTFT overhead vs direct. HMAC-SHA256 costs
//     microseconds.
//   - Memory-hard hashing (argon2id, the original schema comment) buys
//     brute-force resistance — which only matters for low-entropy
//     secrets. Virtual keys are 256-bit random values; brute force is
//     infeasible against plain SHA-256, and the pepper additionally
//     prevents offline verification of leaked databases.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// virtualKeyPrefix namespaces gateway-issued keys ("ogk-" = OneGate key).
const virtualKeyPrefix = "ogk-"

// virtualKeyEntropy is the random body length in bytes (32 bytes ->
// 43 base64url chars; ~256 bits of entropy).
const virtualKeyEntropy = 32

// prefixDisplayLen is how much of a raw key the display prefix keeps
// (prefix + 8 chars + ellipsis), e.g. "ogk-Ab3dEf0h…".
const prefixDisplayLen = len(virtualKeyPrefix) + 8

// GenerateVirtualKey mints a new raw key. The raw value is returned
// exactly once at creation; callers show only Prefix afterwards.
func GenerateVirtualKey() (raw, prefix string) {
	b := make([]byte, virtualKeyEntropy)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	raw = virtualKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	return raw, KeyPrefix(raw)
}

// KeyPrefix derives the display prefix from a raw key ("ogk-Ab3dEf0h…").
func KeyPrefix(raw string) string {
	if len(raw) <= prefixDisplayLen {
		return raw
	}
	return raw[:prefixDisplayLen] + "…"
}

// HashVirtualKey computes the storage hash of a raw key under the given
// pepper. The pepper must be stable for the lifetime of stored hashes
// (derive it once from master.key; see NewVerifier).
func HashVirtualKey(pepper []byte, raw string) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyVirtualKey reports whether raw matches the stored hash, in
// constant time.
func VerifyVirtualKey(pepper []byte, raw, storedHash string) bool {
	if raw == "" || storedHash == "" {
		return false
	}
	computed := HashVirtualKey(pepper, raw)
	return hmac.Equal([]byte(computed), []byte(storedHash))
}

// looksLikeVirtualKey is a cheap structural check used before hashing:
// keys carry the gateway prefix. Avoids hashing arbitrary header junk.
func looksLikeVirtualKey(raw string) bool {
	return strings.HasPrefix(raw, virtualKeyPrefix) && len(raw) > len(virtualKeyPrefix)
}
