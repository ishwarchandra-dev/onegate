package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// Verification failures use the domain sentinels (domain.ErrNoCredential,
// etc.) so the ingest layer can map them onto 401/403 envelopes without
// importing this package. Lookup/storage failures wrap opaquely.

// Verifier authenticates virtual keys against the store. It is the
// "internal/auth verifies the virtual key" step of the request
// lifecycle (docs/architecture.md).
type Verifier struct {
	store  *storage.Store
	pepper []byte
	now    func() time.Time
}

// NewVerifier builds a Verifier. The pepper must be derived once per
// process (deriveKey on master.key) — hashes in storage are only
// comparable under the same pepper.
func NewVerifier(store *storage.Store, pepper []byte) *Verifier {
	return &Verifier{store: store, pepper: pepper, now: time.Now}
}

// Verify resolves a raw client key to its VirtualKey record.
//
// The lookup is GetByHash on the deterministic peppered hash; lookup
// equality is the authentication. Status and expiry are enforced here
// so every caller gets the same semantics. LastUsedMS is refreshed
// off the critical path (fire-and-forget write; WAL makes the tiny
// UPDATE cheap, and losing one touch on shutdown is acceptable).
func (v *Verifier) Verify(ctx context.Context, rawKey string) (domain.VirtualKey, error) {
	if rawKey == "" {
		return domain.VirtualKey{}, domain.ErrNoCredential
	}
	if !looksLikeVirtualKey(rawKey) {
		return domain.VirtualKey{}, domain.ErrMalformedKey
	}

	hash := HashVirtualKey(v.pepper, rawKey)
	key, err := v.store.VirtualKeys().GetByHash(hash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.VirtualKey{}, domain.ErrUnknownKey
		}
		return domain.VirtualKey{}, fmt.Errorf("auth: key lookup: %w", err)
	}

	nowMS := v.now().UnixMilli()
	switch {
	case key.Status == domain.KeyRevoked:
		return domain.VirtualKey{}, domain.ErrKeyRevoked
	case key.ExpiredAt(nowMS):
		return domain.VirtualKey{}, domain.ErrKeyExpired
	}

	if ctx.Err() == nil {
		go func(id string, atMS int64) {
			defer func() { _ = recover() }() // never propagate on the hot path
			_ = v.store.VirtualKeys().TouchLastUsed(id, atMS)
		}(key.ID, nowMS)
	}
	return key, nil
}
