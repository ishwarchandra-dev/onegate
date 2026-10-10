package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// Verification failures use the domain sentinels (domain.ErrNoCredential,
// etc.) so the ingest layer can map them onto 401/403 envelopes without
// importing this package. Lookup/storage failures wrap opaquely.

// verifierCacheMax bounds the verified-key cache. The natural bound is
// the operator's key count (hundreds); a hard cap keeps memory finite
// even under key-spray traffic. Overshoot clears the whole map — the
// cost is one storage roundtrip per live key, once.
const verifierCacheMax = 4096

// touchInterval coalesces last_used_ms writes: at most one
// fire-and-forget UPDATE per key per window. The field is display
// metadata for the dashboard; the previous per-request write cadence
// bought nothing observable (p8.perf-fixes).
const touchInterval = time.Second

// cachedKey is one verified-key cache entry. lastTouch tracks when the
// last_used_ms fire-and-forget write was spawned (coalescing knob).
type cachedKey struct {
	key       domain.VirtualKey
	lastTouch time.Time
}

// Verifier authenticates virtual keys against the store. It is the
// "internal/auth verifies the virtual key" step of the request
// lifecycle (docs/architecture.md).
//
// Hot path (p8.perf-fixes): every request used to pay a SQLite lookup
// (15.5% of proxy CPU in the hot-path profile). Verified records are
// now cached by key hash; the storage layer fires the change hook on
// every semantic mutation (create / status / scopes / limits / meta /
// delete), which drops the cache. Correctness is unchanged — the
// gateway process is the only writer, so a revocation observed by the
// repo clears cached state before the next Verify reads it. Expiry
// stays a per-request time check against the cached record. Callers
// treat the returned record (and its Scopes/Limits maps) as read-only.
type Verifier struct {
	store  *storage.Store
	pepper []byte
	now    func() time.Time

	mu    sync.RWMutex
	cache map[string]cachedKey
}

// NewVerifier builds a Verifier. The pepper must be derived once per
// process (deriveKey on master.key) — hashes in storage are only
// comparable under the same pepper.
func NewVerifier(store *storage.Store, pepper []byte) *Verifier {
	v := &Verifier{
		store:  store,
		pepper: pepper,
		now:    time.Now,
		cache:  map[string]cachedKey{},
	}
	// Register before serving: every in-process vkey mutation (mgmt API,
	// CLI, importer) flows through the repo and drops the cache, so a
	// revoked key fails on its very next request — same as uncached.
	store.VirtualKeys().SetChangeHook(v.invalidate)
	return v
}

// invalidate drops all cached verifications (called by the storage
// change hook after any vkey mutation).
func (v *Verifier) invalidate() {
	v.mu.Lock()
	v.cache = make(map[string]cachedKey)
	v.mu.Unlock()
}

// Verify resolves a raw client key to its VirtualKey record.
//
// The lookup is the deterministic peppered hash (cache hit) falling
// back to GetByHash on storage; lookup equality is the authentication.
// Status and expiry are enforced here so every caller gets the same
// semantics. LastUsedMS is refreshed off the critical path
// (fire-and-forget write, coalesced to one per key per second; WAL
// makes the tiny UPDATE cheap, and losing touches on shutdown is
// acceptable — they are display metadata).
func (v *Verifier) Verify(ctx context.Context, rawKey string) (domain.VirtualKey, error) {
	if rawKey == "" {
		return domain.VirtualKey{}, domain.ErrNoCredential
	}
	if !looksLikeVirtualKey(rawKey) {
		return domain.VirtualKey{}, domain.ErrMalformedKey
	}

	hash := HashVirtualKey(v.pepper, rawKey)

	now := v.now()
	nowMS := now.UnixMilli()

	// Fast path: cached, still active, not expired.
	v.mu.RLock()
	entry, ok := v.cache[hash]
	v.mu.RUnlock()
	if ok {
		if err := checkKeyState(entry.key, nowMS); err != nil {
			return domain.VirtualKey{}, err
		}
		v.maybeTouch(ctx, hash, entry, now)
		return entry.key, nil
	}

	key, err := v.store.VirtualKeys().GetByHash(hash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return domain.VirtualKey{}, domain.ErrUnknownKey
		}
		return domain.VirtualKey{}, fmt.Errorf("auth: key lookup: %w", err)
	}

	if err := checkKeyState(key, nowMS); err != nil {
		return domain.VirtualKey{}, err
	}

	// Cache only usable records (active, unexpired): rejected states
	// keep paying the storage lookup, which is the conservative side.
	v.mu.Lock()
	if len(v.cache) >= verifierCacheMax {
		v.cache = make(map[string]cachedKey)
	}
	v.cache[hash] = cachedKey{key: key, lastTouch: now}
	v.mu.Unlock()

	v.spawnTouch(ctx, key.ID, now)
	return key, nil
}

// checkKeyState enforces status and expiry against a record snapshot.
func checkKeyState(key domain.VirtualKey, nowMS int64) error {
	switch {
	case key.Status == domain.KeyRevoked:
		return domain.ErrKeyRevoked
	case key.ExpiredAt(nowMS):
		return domain.ErrKeyExpired
	}
	return nil
}

// maybeTouch coalesces the last_used_ms write: spawn the
// fire-and-forget UPDATE only when the window has elapsed. The cache
// entry is updated under the write lock so concurrent requests for the
// same key do not double-spawn within the window.
func (v *Verifier) maybeTouch(ctx context.Context, hash string, entry cachedKey, now time.Time) {
	if now.Sub(entry.lastTouch) < touchInterval {
		return
	}
	v.mu.Lock()
	// Re-check under the write lock: another goroutine may have won the
	// race and already refreshed the timestamp.
	if cur, ok := v.cache[hash]; ok && now.Sub(cur.lastTouch) < touchInterval {
		v.mu.Unlock()
		return
	} else if ok {
		v.cache[hash] = cachedKey{key: cur.key, lastTouch: now}
	} else {
		v.cache[hash] = cachedKey{key: entry.key, lastTouch: now}
	}
	v.mu.Unlock()
	v.spawnTouch(ctx, entry.key.ID, now)
}

// spawnTouch fires the best-effort last_used_ms write. It never blocks
// or propagates on the hot path.
func (v *Verifier) spawnTouch(ctx context.Context, id string, at time.Time) {
	if ctx.Err() != nil {
		return
	}
	atMS := at.UnixMilli()
	go func(id string, atMS int64) {
		defer func() { _ = recover() }() // never propagate on the hot path
		_ = v.store.VirtualKeys().TouchLastUsed(id, atMS)
	}(id, atMS)
}
