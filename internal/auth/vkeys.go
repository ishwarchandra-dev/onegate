// Package auth virtual key lifecycle management.
package auth

import (
	"context"
	"fmt"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// CreateKeyParams holds configuration options for minting a new virtual key.
type CreateKeyParams struct {
	Name      string
	Scopes    domain.KeyScopes
	Limits    domain.KeyLimits
	ExpiresMS int64
}

// Manager orchestrates virtual key lifecycle operations: minting, verification,
// scope enforcement, updating, and revocation.
type Manager struct {
	store    *storage.Store
	pepper   []byte
	verifier *Verifier
}

// NewManager constructs a virtual key Manager.
func NewManager(store *storage.Store, pepper []byte) *Manager {
	return &Manager{
		store:    store,
		pepper:   pepper,
		verifier: NewVerifier(store, pepper),
	}
}

// Verifier returns the underlying Verifier used by proxy ingest.
func (m *Manager) Verifier() *Verifier {
	return m.verifier
}

// CreateKey mints a new virtual key. The raw key is returned exactly once
// at creation time; only its peppered HMAC-SHA256 hash is persisted.
func (m *Manager) CreateKey(ctx context.Context, params CreateKeyParams) (rawKey string, rec domain.VirtualKey, err error) {
	if ctx.Err() != nil {
		return "", domain.VirtualKey{}, ctx.Err()
	}

	raw, prefix := GenerateVirtualKey()
	hash := HashVirtualKey(m.pepper, raw)

	k := domain.VirtualKey{
		Name:      params.Name,
		Prefix:    prefix,
		KeyHash:   hash,
		Scopes:    params.Scopes,
		Limits:    params.Limits,
		Status:    domain.KeyActive,
		ExpiresMS: params.ExpiresMS,
	}

	created, err := m.store.VirtualKeys().Create(k)
	if err != nil {
		return "", domain.VirtualKey{}, fmt.Errorf("auth: persist virtual key: %w", err)
	}

	// Never leak stored hash to callers
	created.KeyHash = ""
	return raw, created, nil
}

// VerifyKey resolves and authenticates a raw key in constant time.
func (m *Manager) VerifyKey(ctx context.Context, rawKey string) (domain.VirtualKey, error) {
	return m.verifier.Verify(ctx, rawKey)
}

// RevokeKey marks an active virtual key as revoked. Revoked keys fail authentication.
func (m *Manager) RevokeKey(ctx context.Context, id string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return m.store.VirtualKeys().UpdateStatus(id, domain.KeyRevoked)
}

// GetKey retrieves a virtual key's metadata by ID.
func (m *Manager) GetKey(ctx context.Context, id string) (domain.VirtualKey, error) {
	if ctx.Err() != nil {
		return domain.VirtualKey{}, ctx.Err()
	}
	k, err := m.store.VirtualKeys().Get(id)
	if err != nil {
		return domain.VirtualKey{}, err
	}
	k.KeyHash = ""
	return k, nil
}

// ListKeys returns all virtual keys sorted newest-first.
func (m *Manager) ListKeys(ctx context.Context) ([]domain.VirtualKey, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	keys, err := m.store.VirtualKeys().List()
	if err != nil {
		return nil, err
	}
	for i := range keys {
		keys[i].KeyHash = ""
	}
	return keys, nil
}

// DeleteKey permanently deletes a virtual key row.
func (m *Manager) DeleteKey(ctx context.Context, id string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return m.store.VirtualKeys().Delete(id)
}

// UpdateScopes updates model and provider access scopes on a virtual key.
func (m *Manager) UpdateScopes(ctx context.Context, id string, scopes domain.KeyScopes) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return m.store.VirtualKeys().UpdateScopes(id, scopes)
}

// UpdateLimits updates quotas and spend caps on a virtual key.
func (m *Manager) UpdateLimits(ctx context.Context, id string, limits domain.KeyLimits) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return m.store.VirtualKeys().UpdateLimits(id, limits)
}

// CheckModelScope reports whether the given model is permitted under the key's scopes.
func CheckModelScope(key domain.VirtualKey, model string) bool {
	if len(key.Scopes.AllowedModels) == 0 {
		return true
	}
	for _, m := range key.Scopes.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

// CheckProviderScope reports whether the given provider is permitted under the key's scopes.
func CheckProviderScope(key domain.VirtualKey, providerID string) bool {
	if len(key.Scopes.AllowedProviders) == 0 {
		return true
	}
	for _, p := range key.Scopes.AllowedProviders {
		if p == providerID {
			return true
		}
	}
	return false
}
