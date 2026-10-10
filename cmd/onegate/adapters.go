// Composition-root adapters for the data plane (p7.parity-fixes): the
// routing-backed target resolver, the credential cache, the quota-aware
// proxy wrapper, and the model catalog adapter for GET /v1/models.
//
// These live in package main because the layering contract
// (internal/proxy/proxy_test.go) forbids internal/proxy from importing
// internal/routing or internal/ratelimit: composition happens ONLY here
// (cmd/onegate), exactly like the Phase 4 integration test proved.
package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/nonstream"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// ---------------------------------------------------------------------------
// Provider credentials (TTL cache over storage + master-key cipher)
// ---------------------------------------------------------------------------

// providerCredentials implements fallback.CredentialSource with a TTL
// cache: the hot path takes an RLock and a map read; storage + decryption
// happen at most once per provider per TTL window. Entries are bounded by
// the provider count and replaced atomically.
type providerCredentials struct {
	store   *storage.Store
	cipher  *auth.Cipher
	ttl     time.Duration
	now     func() time.Time
	mu      sync.RWMutex
	entries map[string]credEntry
}

type credEntry struct {
	provider client.Provider
	fetched  time.Time
}

// newProviderCredentials builds the cache. ttl <= 0 means 15s.
func newProviderCredentials(store *storage.Store, cipher *auth.Cipher, ttl time.Duration) *providerCredentials {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &providerCredentials{
		store:   store,
		cipher:  cipher,
		ttl:     ttl,
		now:     time.Now,
		entries: map[string]credEntry{},
	}
}

// ProviderCredentials resolves a provider's connection details, serving
// from cache when fresh. A rotation becomes visible within one TTL.
func (p *providerCredentials) ProviderCredentials(_ context.Context, providerID string) (client.Provider, error) {
	now := p.now()
	p.mu.RLock()
	if e, ok := p.entries[providerID]; ok && now.Sub(e.fetched) < p.ttl {
		p.mu.RUnlock()
		return e.provider, nil
	}
	p.mu.RUnlock()

	rec, err := p.store.Providers().Get(providerID)
	if err != nil {
		return client.Provider{}, err
	}
	prov := client.Provider{
		ID:       rec.Provider.ID,
		Protocol: rec.Provider.Protocol,
		BaseURL:  rec.Provider.BaseURL,
	}
	if len(rec.APIKeyEnc) > 0 && p.cipher != nil {
		plain, err := p.cipher.Decrypt(rec.APIKeyEnc)
		if err != nil {
			return client.Provider{}, err
		}
		prov.APIKey = string(plain)
	}

	p.mu.Lock()
	p.entries[providerID] = credEntry{provider: prov, fetched: now}
	p.mu.Unlock()
	return prov, nil
}

// Invalidate drops the cached entry for one provider (called when the
// dashboard updates a provider, so rotations apply immediately).
func (p *providerCredentials) Invalidate(providerID string) {
	p.mu.Lock()
	delete(p.entries, providerID)
	p.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Routing-backed target resolver
// ---------------------------------------------------------------------------

// routingResolver adapts the Phase 4 routing engine (registry snapshot +
// health view) to fallback.TargetResolver. Routing sentinels map onto
// client-ready ResolveErrors (checklist C-10..C-12, C-23).
type routingResolver struct {
	registry    *routing.Registry
	health      routing.HealthView
	credentials *providerCredentials
}

// ResolveTargets implements fallback.TargetResolver.
func (r *routingResolver) ResolveTargets(ctx context.Context, call ingest.Call) ([]fallback.Target, error) {
	req := routing.RouteRequest{
		Model:        call.Request.Model,
		Capabilities: routing.RequestCapabilities(call.Request),
		Key:          &call.Key,
	}
	targets, err := r.registry.DecideTargets(req, r.health)
	if err != nil {
		return nil, &fallback.ResolveError{GE: routing.ClientError(call.Request.Model, err)}
	}

	out := make([]fallback.Target, 0, len(targets))
	for _, t := range targets {
		prov, err := r.credentials.ProviderCredentials(ctx, t.ProviderID)
		if err != nil {
			return nil, &fallback.ResolveError{GE: domain.GatewayError{
				Status:  http.StatusServiceUnavailable,
				Type:    domain.ErrOverloaded,
				Message: "provider credentials unavailable",
			}}
		}
		out = append(out, fallback.Target{
			Provider:       prov,
			Model:          t.ProviderModel,
			CostMultiplier: t.CostMultiplier,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Quota-aware proxy wrapper (checklist C-6..C-9)
// ---------------------------------------------------------------------------

// quotaProxy wraps the engine with quota enforcement: the gate renders
// 429 + Retry-After before any upstream work; actual usage debits after
// completion. In-flight keys are kept trace-ID-keyed for the debit.
//
// p8.perf-fixes: the trace->key map is a sync.Map — each request stores
// and deletes its own unique key (disjoint-access pattern), so the fast
// path runs lock-free instead of serializing every request through one
// mutex.
type quotaProxy struct {
	engine  *fallback.Engine
	quota   *ratelimit.QuotaManager
	metrics *observability.Registry // may be nil (tests)

	keys sync.Map // traceID -> domain.VirtualKey
}

// newQuotaProxy builds the wrapper. engine and quota are required;
// metrics (may be nil) drives the in-flight gauge.
func newQuotaProxy(engine *fallback.Engine, quota *ratelimit.QuotaManager,
	metrics *observability.Registry) *quotaProxy {
	if engine == nil || quota == nil {
		panic("onegate: quotaProxy requires an engine and a QuotaManager")
	}
	return &quotaProxy{engine: engine, quota: quota, metrics: metrics}
}

// Execute implements ingest.Proxy.
func (p *quotaProxy) Execute(ctx context.Context, w http.ResponseWriter, call ingest.Call) {
	if p.metrics != nil {
		p.metrics.IncInflight(string(call.Protocol))
		defer p.metrics.DecInflight(string(call.Protocol))
	}
	release, err := p.quota.Acquire(call.Key, estimateTokens(call.Request))
	if err != nil {
		var rlErr *ratelimit.RateLimitError
		if errors.As(err, &rlErr) {
			nonstream.RenderError(w, call.Protocol, rlErr.GErr)
			return
		}
		nonstream.RenderError(w, call.Protocol, domain.GatewayError{
			Status:  http.StatusTooManyRequests,
			Type:    domain.ErrRateLimit,
			Message: err.Error(),
		})
		return
	}
	defer release()

	traceID := observability.TraceID(ctx)
	if traceID != "" {
		p.keys.Store(traceID, call.Key)
		defer p.keys.Delete(traceID)
	}

	p.engine.Execute(ctx, w, call)
}

// onUsage debits actual usage against the key that issued the call.
// Pricing follows the canonical model (ModelRequested) — same rule as the
// usage pipeline's cost enrichment.
func (p *quotaProxy) onUsage(ue fallback.UsageEvent) {
	val, ok := p.keys.Load(ue.CallID)
	if !ok {
		return
	}
	key, _ := val.(domain.VirtualKey)
	mult := ue.CostMultiplier
	if mult <= 0 {
		mult = 100
	}
	model := ue.ModelRequested
	if model == "" {
		model = ue.ModelServed
	}
	p.quota.DebitUsage(key, model, ue.Usage, mult)
}

// estimateTokens produces a cheap, deterministic prompt-token estimate
// for TPM admission: ~4 characters per token over message text plus a
// per-message overhead constant. The post-response debit corrects any
// over/under-estimate with actual usage.
func estimateTokens(req domain.Request) int64 {
	const charsPerToken = 4
	const perMessageOverhead = 4
	var chars int64
	for _, m := range req.Messages {
		chars += perMessageOverhead
		for _, b := range m.Content {
			chars += int64(len(b.Text))
		}
	}
	est := chars/charsPerToken + 1
	if est < 1 {
		est = 1
	}
	return est
}

// ---------------------------------------------------------------------------
// Model catalog adapter (GET /v1/models, checklist A-7)
// ---------------------------------------------------------------------------

// registryModelLister adapts routing.Registry to ingest.ModelLister.
type registryModelLister struct {
	registry *routing.Registry
}

// ListModels returns the canonical catalog with unix-second created times.
func (l *registryModelLister) ListModels() []ingest.ModelInfo {
	models := l.registry.ListModels()
	out := make([]ingest.ModelInfo, 0, len(models))
	for _, m := range models {
		out = append(out, ingest.ModelInfo{ID: m.ID, Created: m.CreatedMS / 1000})
	}
	return out
}
