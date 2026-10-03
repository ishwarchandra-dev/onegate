package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

func setupTestManager(t *testing.T) (*auth.Manager, *storage.Store) {
	t.Helper()
	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	pepper := []byte("test-pepper-32-bytes-long-123456")
	return auth.NewManager(store, pepper), store
}

func TestVirtualKeys_Lifecycle(t *testing.T) {
	ctx := context.Background()
	mgr, store := setupTestManager(t)

	// 1. Create key with scopes and limits
	rawKey, rec, err := mgr.CreateKey(ctx, auth.CreateKeyParams{
		Name: "Production Backend",
		Scopes: domain.KeyScopes{
			AllowedModels:    []string{"gpt-4o", "claude-3-5-sonnet"},
			AllowedProviders: []string{"openai", "anthropic"},
			PolicyOverride:   domain.PolicyCost,
			ModelOverrides: map[string]domain.FallbackPolicy{
				"gpt-4o": domain.PolicyOrdered,
			},
		},
		Limits: domain.KeyLimits{
			RPM:         600,
			Concurrency: 10,
		},
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Verify raw key format and prefix
	if !strings.HasPrefix(rawKey, "ogk-") {
		t.Fatalf("expected ogk- prefix on raw key, got %s", rawKey)
	}
	if !strings.HasPrefix(rec.Prefix, "ogk-") || !strings.HasSuffix(rec.Prefix, "…") {
		t.Fatalf("expected prefix format e.g. ogk-..., got %s", rec.Prefix)
	}
	// Verify raw key is never returned with KeyHash populated
	if rec.KeyHash != "" {
		t.Fatalf("rec.KeyHash should be redacted, got %s", rec.KeyHash)
	}

	// Verify raw key is NOT stored in plain text in database
	dbKey, err := store.VirtualKeys().Get(rec.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if dbKey.KeyHash == "" || strings.Contains(dbKey.KeyHash, rawKey) {
		t.Fatalf("raw key leaked or hash missing in DB: %s", dbKey.KeyHash)
	}

	// 2. Constant-time verification
	verified, err := mgr.VerifyKey(ctx, rawKey)
	if err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if verified.ID != rec.ID || verified.Name != "Production Backend" {
		t.Fatalf("verified mismatch: %+v vs %+v", verified, rec)
	}

	// 3. Scopes inspection
	if !auth.CheckModelScope(verified, "gpt-4o") {
		t.Error("expected gpt-4o to be allowed")
	}
	if auth.CheckModelScope(verified, "disallowed-model") {
		t.Error("expected disallowed-model to be rejected")
	}
	if !auth.CheckProviderScope(verified, "openai") {
		t.Error("expected openai to be allowed")
	}
	if auth.CheckProviderScope(verified, "disallowed-provider") {
		t.Error("expected disallowed-provider to be rejected")
	}
	if verified.Scopes.PolicyOverride != domain.PolicyCost {
		t.Fatalf("want PolicyCost, got %v", verified.Scopes.PolicyOverride)
	}
	if verified.Scopes.ModelOverrides["gpt-4o"] != domain.PolicyOrdered {
		t.Fatalf("want PolicyOrdered for gpt-4o, got %v", verified.Scopes.ModelOverrides["gpt-4o"])
	}

	// 4. Update scopes and limits
	newScopes := domain.KeyScopes{
		AllowedModels: []string{"gpt-4o"},
	}
	if err := mgr.UpdateScopes(ctx, rec.ID, newScopes); err != nil {
		t.Fatalf("UpdateScopes: %v", err)
	}
	updated, err := mgr.GetKey(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if len(updated.Scopes.AllowedModels) != 1 || updated.Scopes.AllowedModels[0] != "gpt-4o" {
		t.Fatalf("updated scopes mismatch: %+v", updated.Scopes)
	}

	// 5. List keys
	keys, err := mgr.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != rec.ID {
		t.Fatalf("list mismatch: %+v", keys)
	}

	// 6. Revocation
	if err := mgr.RevokeKey(ctx, rec.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	_, err = mgr.VerifyKey(ctx, rawKey)
	if !errors.Is(err, domain.ErrKeyRevoked) {
		t.Fatalf("want ErrKeyRevoked, got %v", err)
	}

	// 7. Deletion
	if err := mgr.DeleteKey(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	_, err = mgr.GetKey(ctx, rec.ID)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestVirtualKeys_AuthErrors(t *testing.T) {
	ctx := context.Background()
	mgr, _ := setupTestManager(t)

	// Empty key
	_, err := mgr.VerifyKey(ctx, "")
	if !errors.Is(err, domain.ErrNoCredential) {
		t.Fatalf("want ErrNoCredential, got %v", err)
	}

	// Malformed key (missing ogk- prefix)
	_, err = mgr.VerifyKey(ctx, "sk-proj-12345")
	if !errors.Is(err, domain.ErrMalformedKey) {
		t.Fatalf("want ErrMalformedKey, got %v", err)
	}

	// Unknown key (well-formed ogk- but not in database)
	_, err = mgr.VerifyKey(ctx, "ogk-dGhpcy1pcy1hLWZha2Uta2V5LXRoYXQtZG9lcy1ub3QtZXhpc3Q")
	if !errors.Is(err, domain.ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}

	// Expired key
	rawKey, _, err := mgr.CreateKey(ctx, auth.CreateKeyParams{
		Name:      "Expired Key",
		ExpiresMS: time.Now().Add(-1 * time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	_, err = mgr.VerifyKey(ctx, rawKey)
	if !errors.Is(err, domain.ErrKeyExpired) {
		t.Fatalf("want ErrKeyExpired, got %v", err)
	}
}

func TestVirtualKeys_Concurrency(t *testing.T) {
	ctx := context.Background()
	mgr, _ := setupTestManager(t)

	rawKey, rec, err := mgr.CreateKey(ctx, auth.CreateKeyParams{Name: "Concurrent Test"})
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Verifiers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, _ = mgr.VerifyKey(ctx, rawKey)
			}
		}()
	}

	// Updaters / Readers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = mgr.UpdateLimits(ctx, rec.ID, domain.KeyLimits{RPM: int64(100 + j)})
				_, _ = mgr.GetKey(ctx, rec.ID)
			}
		}()
	}

	wg.Wait()
}
