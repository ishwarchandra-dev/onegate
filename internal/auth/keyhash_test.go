package auth

import (
	"context"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

func TestGenerateVirtualKeyShape(t *testing.T) {
	raw, prefix := GenerateVirtualKey()
	if len(raw) <= len("ogk-")+8 {
		t.Fatalf("key too short: %q", raw)
	}
	if raw[:4] != "ogk-" {
		t.Fatalf("prefix missing: %q", raw)
	}
	if prefix != raw[:12]+"…" {
		t.Fatalf("display prefix: %q", prefix)
	}
	raw2, _ := GenerateVirtualKey()
	if raw == raw2 {
		t.Fatal("keys must be unique")
	}
}

func TestHashDeterministicAndPeppered(t *testing.T) {
	raw, _ := GenerateVirtualKey()
	h1 := HashVirtualKey([]byte("pepper-a"), raw)
	h2 := HashVirtualKey([]byte("pepper-a"), raw)
	h3 := HashVirtualKey([]byte("pepper-b"), raw)
	if h1 != h2 {
		t.Fatal("hash must be deterministic per pepper")
	}
	if h1 == h3 {
		t.Fatal("different peppers must yield different hashes")
	}
	if len(h1) != 64 {
		t.Fatalf("sha256 hex length: %d", len(h1))
	}
}

func TestVerifyVirtualKeyConstantTime(t *testing.T) {
	raw, _ := GenerateVirtualKey()
	stored := HashVirtualKey([]byte("p"), raw)
	if !VerifyVirtualKey([]byte("p"), raw, stored) {
		t.Fatal("valid key must verify")
	}
	if VerifyVirtualKey([]byte("p"), raw+"x", stored) {
		t.Fatal("tampered key must fail")
	}
	if VerifyVirtualKey([]byte("q"), raw, stored) {
		t.Fatal("wrong pepper must fail")
	}
	if VerifyVirtualKey([]byte("p"), "", stored) || VerifyVirtualKey([]byte("p"), raw, "") {
		t.Fatal("empty inputs must fail")
	}
}

func TestVerifierLifecycle(t *testing.T) {
	store, err := storage.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}

	pepper := []byte("test-pepper")
	v := NewVerifier(store, pepper)

	// No credential.
	if _, err := v.Verify(context.Background(), ""); err != domain.ErrNoCredential {
		t.Fatalf("empty: %v", err)
	}
	// Malformed (not ogk-).
	if _, err := v.Verify(context.Background(), "sk-foreign"); err != domain.ErrMalformedKey {
		t.Fatalf("foreign: %v", err)
	}

	// Unknown but well-formed.
	raw, _ := GenerateVirtualKey()
	if _, err := v.Verify(context.Background(), raw); err != domain.ErrUnknownKey {
		t.Fatalf("unknown: %v", err)
	}

	// Active key verifies.
	key, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name:    "test",
		Prefix:  KeyPrefix(raw),
		KeyHash: HashVirtualKey(pepper, raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != key.ID {
		t.Fatalf("resolved %q, want %q", got.ID, key.ID)
	}

	// Revoked -> 403-class error.
	if err := store.VirtualKeys().UpdateStatus(key.ID, domain.KeyRevoked); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), raw); err != domain.ErrKeyRevoked {
		t.Fatalf("revoked: %v", err)
	}

	// Expired -> 403-class error.
	raw2, _ := GenerateVirtualKey()
	k2, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name:      "expired",
		Prefix:    KeyPrefix(raw2),
		KeyHash:   HashVirtualKey(pepper, raw2),
		ExpiresMS: 1, // long past
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = k2
	if _, err := v.Verify(context.Background(), raw2); err != domain.ErrKeyExpired {
		t.Fatalf("expired: %v", err)
	}

	// LastUsed is touched (best effort — poll briefly).
	deadline := 50
	for i := 0; i < deadline; i++ {
		re, err := store.VirtualKeys().Get(key.ID)
		if err == nil && re.LastUsedMS > 0 {
			break
		}
		if i == deadline-1 {
			t.Fatal("last_used_ms never touched")
		}
		sleepMS(5)
	}
}

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
