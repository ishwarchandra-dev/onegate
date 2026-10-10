package auth

// p8.perf-fixes: the Verifier caches verified keys; these tests pin the
// correctness property that makes the cache safe — every semantic
// mutation observed by the repo must drop cached state so the very
// next Verify reflects it (revocation, scopes, limits, expiry).
import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// newVerifierWorld opens a migrated temp store with one active key and
// a Verifier over it (same-package access lets tests steer the clock).
func newVerifierWorld(t *testing.T) (v *Verifier, keyID string, raw string) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "verify.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	pepper := []byte("test-pepper")
	raw, prefix := GenerateVirtualKey()
	key, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name: "cache-test", Prefix: prefix,
		KeyHash: HashVirtualKey(pepper, raw),
		Status:  domain.KeyActive,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return NewVerifier(store, pepper), key.ID, raw
}

func TestVerifierCache_RevocationImmediate(t *testing.T) {
	v, keyID, raw := newVerifierWorld(t)
	ctx := context.Background()

	// Prime the cache.
	if _, err := v.Verify(ctx, raw); err != nil {
		t.Fatalf("prime: %v", err)
	}
	// Revoke through the same process's repo (the only writer).
	if err := v.store.VirtualKeys().UpdateStatus(keyID, domain.KeyRevoked); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// The very next Verify must see the revocation — no TTL, no window.
	if _, err := v.Verify(ctx, raw); !errors.Is(err, domain.ErrKeyRevoked) {
		t.Fatalf("want ErrKeyRevoked, got %v", err)
	}
}

func TestVerifierCache_ScopesUpdateImmediate(t *testing.T) {
	v, keyID, raw := newVerifierWorld(t)
	ctx := context.Background()

	got, err := v.Verify(ctx, raw)
	if err != nil {
		t.Fatalf("prime: %v", err)
	}
	if len(got.Scopes.AllowedModels) != 0 {
		t.Fatalf("unexpected initial scopes: %+v", got.Scopes)
	}

	newScopes := domain.KeyScopes{AllowedModels: []string{"gpt-4o"}}
	if err := v.store.VirtualKeys().UpdateScopes(keyID, newScopes); err != nil {
		t.Fatalf("update scopes: %v", err)
	}
	got, err = v.Verify(ctx, raw)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if len(got.Scopes.AllowedModels) != 1 || got.Scopes.AllowedModels[0] != "gpt-4o" {
		t.Fatalf("cached scopes stale after update: %+v", got.Scopes)
	}
}

func TestVerifierCache_LimitsUpdateImmediate(t *testing.T) {
	v, keyID, raw := newVerifierWorld(t)
	ctx := context.Background()

	if _, err := v.Verify(ctx, raw); err != nil {
		t.Fatalf("prime: %v", err)
	}
	limits := domain.KeyLimits{RPM: 42}
	if err := v.store.VirtualKeys().UpdateLimits(keyID, limits); err != nil {
		t.Fatalf("update limits: %v", err)
	}
	got, err := v.Verify(ctx, raw)
	if err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if got.Limits.RPM != 42 {
		t.Fatalf("cached limits stale after update: %+v", got.Limits)
	}
}

func TestVerifierCache_ExpiryCheckedPerRequest(t *testing.T) {
	// Expiry is time-based; the cached record must be re-checked against
	// "now" on every Verify — a cache hit must never outlive a key.
	v, keyID, raw := newVerifierWorld(t)
	ctx := context.Background()

	if _, err := v.Verify(ctx, raw); err != nil {
		t.Fatalf("prime: %v", err)
	}
	// Give the key a near-term expiry, then advance the clock past it.
	if err := v.store.VirtualKeys().UpdateMeta(keyID, "cache-test", time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	v.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := v.Verify(ctx, raw); !errors.Is(err, domain.ErrKeyExpired) {
		t.Fatalf("want ErrKeyExpired on expired cached key, got %v", err)
	}
}

func TestVerifierCache_DeletionImmediate(t *testing.T) {
	v, keyID, raw := newVerifierWorld(t)
	ctx := context.Background()

	if _, err := v.Verify(ctx, raw); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if err := v.store.VirtualKeys().Delete(keyID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := v.Verify(ctx, raw); !errors.Is(err, domain.ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey after delete, got %v", err)
	}
}
