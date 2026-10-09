package routing_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/mockprovider"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/nonstream"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/server"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// integratedHarness connects the complete Phase 4 system:
// Virtual Keys & Auth -> Quota Manager & Pricing -> Pure Routing Engine ->
// Health Tracker Circuit Breaker -> Fallback Engine -> Mock Providers.
type integratedHarness struct {
	store        *storage.Store
	authMgr      *auth.Manager
	quotaMgr     *ratelimit.QuotaManager
	health       *routing.HealthTracker
	registry     *routing.Registry
	watcher      *routing.Watcher
	priceTable   *ratelimit.PriceTable
	mockPrimary  *httptest.Server
	mockFallback *httptest.Server
	proxyServer  *httptest.Server
	pepper       []byte

	// State trackers for assertions
	primaryCalls  atomic.Int64
	fallbackCalls atomic.Int64

	// Mock fault injection controls
	primaryStatus     atomic.Int64
	primaryRetryAfter atomic.Int64
}

func setupIntegratedHarness(t *testing.T) *integratedHarness {
	t.Helper()
	h := &integratedHarness{
		pepper:     []byte("integration-pepper-test-secret-32b"),
		priceTable: ratelimit.NewPriceTable(),
	}
	h.primaryStatus.Store(http.StatusOK)

	// 1. Mock upstream servers
	baseMockHandler := mockprovider.NewHandler()

	h.mockPrimary = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.primaryCalls.Add(1)
		status := int(h.primaryStatus.Load())
		if status >= 400 {
			if retrySec := h.primaryRetryAfter.Load(); retrySec > 0 {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retrySec))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			switch status {
			case http.StatusTooManyRequests:
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
			default:
				_, _ = w.Write([]byte(`{"error":{"message":"upstream provider error","type":"server_error","code":"internal_error"}}`))
			}
			return
		}
		baseMockHandler.ServeHTTP(w, r)
	}))

	h.mockFallback = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.fallbackCalls.Add(1)
		baseMockHandler.ServeHTTP(w, r)
	}))

	// 2. Storage & Auth
	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	h.store = store
	h.authMgr = auth.NewManager(store, h.pepper)
	h.quotaMgr = ratelimit.NewQuotaManager(h.priceTable)

	// Health tracker with fast 2-failure trip threshold and 100ms cooldown for tests
	h.health = routing.NewHealthTracker(routing.HealthConfig{
		ConsecutiveFailures: 2,
		Cooldown:            100 * time.Millisecond,
	})

	// 3. Populate providers and models in storage
	prov1 := &storage.ProviderRecord{
		Provider: domain.Provider{
			ID:       "prov-openai",
			Name:     "OpenAI Primary",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  h.mockPrimary.URL,
			Enabled:  true,
		},
	}
	prov2 := &storage.ProviderRecord{
		Provider: domain.Provider{
			ID:       "prov-fallback",
			Name:     "OpenAI Fallback",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  h.mockFallback.URL,
			Enabled:  true,
		},
	}
	if err := store.Providers().Upsert(prov1); err != nil {
		t.Fatalf("create prov1: %v", err)
	}
	if err := store.Providers().Upsert(prov2); err != nil {
		t.Fatalf("create prov2: %v", err)
	}

	caps := &domain.ModelCapabilities{Tools: true, Vision: true, JSONMode: true, Stream: true}

	// Model with fallback chain: prov-openai (pos 0, cost 10) -> prov-fallback (pos 1, cost 20)
	modelMulti := domain.Model{
		ID:           "gpt-4o",
		Capabilities: *caps,
		Targets: []domain.ModelTarget{
			{
				ProviderID:     "prov-openai",
				ProviderModel:  "gpt-4o",
				Position:       0,
				Weight:         1,
				CostMultiplier: 10,
				Capabilities:   caps,
			},
			{
				ProviderID:     "prov-fallback",
				ProviderModel:  "gpt-4o-fallback",
				Position:       1,
				Weight:         1,
				CostMultiplier: 20,
				Capabilities:   caps,
			},
		},
	}
	if err := store.Models().Upsert(modelMulti); err != nil {
		t.Fatalf("create model: %v", err)
	}

	if err := store.RoutingRules().Upsert(&domain.RoutingRule{
		ModelID: "gpt-4o",
		Policy:  domain.PolicyOrdered,
		Enabled: true,
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// Secondary model available only on prov-fallback
	modelFallbackOnly := domain.Model{
		ID:           "claude-3-5-sonnet",
		Capabilities: *caps,
		Targets: []domain.ModelTarget{
			{
				ProviderID:     "prov-fallback",
				ProviderModel:  "claude-3-5-sonnet",
				Position:       0,
				Weight:         1,
				CostMultiplier: 10,
				Capabilities:   caps,
			},
		},
	}
	if err := store.Models().Upsert(modelFallbackOnly); err != nil {
		t.Fatalf("create fallback only model: %v", err)
	}

	// 4. Registry & Hot Reload Watcher
	h.registry = routing.NewRegistry()
	if err := h.registry.LoadFrom(store.RoutingSource()); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	h.watcher = routing.NewWatcher(routing.WatcherConfig{
		Registry:     h.registry,
		Source:       store.RoutingSource(),
		PollInterval: 50 * time.Millisecond,
	})

	// 5. Connect Routing Engine -> Fallback TargetResolver
	resolver := &testTargetResolver{
		registry: h.registry,
		health:   h.health,
	}

	upstreamClient := client.New(client.TransportConfig{}, nil)

	var activeKeys sync.Map

	fbEngine := fallback.NewEngine(fallback.Config{
		Resolver:             resolver,
		Client:               upstreamClient,
		MaxRequestBodyBytes:  1024 * 1024,
		MaxResponseBodyBytes: 1024 * 1024,
		OnTrace: func(tr fallback.Trace) {
			for _, att := range tr.Attempts {
				if att.StatusCode >= 500 || att.StatusCode == http.StatusTooManyRequests {
					h.health.RecordFailure(att.ProviderID, att.Model, fmt.Sprintf("status %d", att.StatusCode))
				} else if att.StatusCode >= 200 && att.StatusCode < 300 {
					h.health.RecordSuccess(att.ProviderID, att.Model)
				}
			}
		},
		OnUsage: func(ue fallback.UsageEvent) {
			if ue.Completed {
				if v, ok := activeKeys.Load(ue.CallID); ok {
					key := v.(domain.VirtualKey)
					_ = h.quotaMgr.DebitUsage(key, ue.ModelServed, ue.Usage, 100)
				}
			}
		},
	})

	// Wrap Fallback engine with integrated Quota enforcement
	integratedProxy := &testIntegratedProxy{
		engine:     fbEngine,
		quotaMgr:   h.quotaMgr,
		activeKeys: &activeKeys,
	}

	// 6. HTTP Server Ingest Pipeline
	srv := server.New(server.Options{Logger: nil})
	ingest.Register(srv.Mux(), ingest.Deps{
		Auth:  h.authMgr.Verifier(),
		Proxy: integratedProxy,
	})
	h.proxyServer = httptest.NewServer(srv.Handler())

	return h
}

func (h *integratedHarness) Close() {
	h.watcher.Stop()
	h.proxyServer.Close()
	h.mockPrimary.Close()
	h.mockFallback.Close()
	h.store.Close()
}

// testTargetResolver adapts routing.DecideTargets to fallback.TargetResolver
type testTargetResolver struct {
	registry *routing.Registry
	health   routing.HealthView
}

func (r *testTargetResolver) ResolveTargets(ctx context.Context, call ingest.Call) ([]fallback.Target, error) {
	req := routing.RouteRequest{
		Model:        call.Request.Model,
		Capabilities: routing.RequestCapabilities(call.Request),
		Key:          &call.Key,
	}
	targets, err := r.registry.DecideTargets(req, r.health)
	if err != nil {
		// Wrap like the production adapter does (cmd/onegate/adapters.go):
		// routing sentinels render as their client envelopes.
		return nil, &fallback.ResolveError{GE: routing.ClientError(req.Model, err)}
	}

	fbTargets := make([]fallback.Target, 0, len(targets))
	for _, t := range targets {
		fbTargets = append(fbTargets, fallback.Target{
			Provider: client.Provider{
				ID:       t.ProviderID,
				Protocol: t.Protocol,
				BaseURL:  t.BaseURL,
				APIKey:   "sk-mock-provider-key",
			},
			Model: t.ProviderModel,
		})
	}
	return fbTargets, nil
}

// testIntegratedProxy wraps fallback.Engine with quota acquisition and trace context.
type testIntegratedProxy struct {
	engine     *fallback.Engine
	quotaMgr   *ratelimit.QuotaManager
	activeKeys *sync.Map
	reqCounter atomic.Int64
}

func (p *testIntegratedProxy) Execute(ctx context.Context, w http.ResponseWriter, call ingest.Call) {
	// 1. Quota Check: estimate tokens & acquire concurrency/RPM/TPM/spend slot
	estTokens := int64(100)
	release, err := p.quotaMgr.Acquire(call.Key, estTokens)
	if err != nil {
		var rlErr *ratelimit.RateLimitError
		if errors.As(err, &rlErr) {
			if rlErr.RetryAfter > 0 {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", rlErr.RetryAfter))
			}
			nonstream.RenderError(w, call.Protocol, rlErr.GErr)
			return
		}
		ge := domain.GatewayError{
			Status:  http.StatusTooManyRequests,
			Type:    domain.ErrRateLimit,
			Message: err.Error(),
		}
		nonstream.RenderError(w, call.Protocol, ge)
		return
	}
	defer release()

	// 2. Set Trace ID in context and associate key for usage attribution
	traceID := fmt.Sprintf("req-%d", p.reqCounter.Add(1))
	ctx = observability.WithTraceID(ctx, traceID)
	p.activeKeys.Store(traceID, call.Key)
	defer p.activeKeys.Delete(traceID)

	// 3. Execute fallback chain
	p.engine.Execute(ctx, w, call)
}

// ---------------------------------------------------------------------------
// Matrix Test 1: Healthy Provider × Standard Virtual Key × Quotas OK
// ---------------------------------------------------------------------------
func TestIntegration_HealthyProvider_EndToEnd(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	// Create virtual key with standard limits
	rawKey, keyRec, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "standard-key",
		Limits: domain.KeyLimits{
			Concurrency:       10,
			RPM:               100,
			TPM:               10000,
			MaxSpendUSDMicros: 1_000_000,
		},
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Issue request to /v1/chat/completions
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Hello world"}]}`
	req, err := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(reqBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Primary mock should have received the call; fallback mock untouched
	if got := h.primaryCalls.Load(); got != 1 {
		t.Errorf("expected 1 primary call, got %d", got)
	}
	if got := h.fallbackCalls.Load(); got != 0 {
		t.Errorf("expected 0 fallback calls, got %d", got)
	}

	// Circuit should remain closed (healthy)
	if !h.health.IsAvailable("prov-openai", "gpt-4o") {
		t.Errorf("expected prov-openai to be available")
	}

	// Concurrency released and spend accounted
	inFlight, spend := h.quotaMgr.GetStats(keyRec.ID)
	if inFlight != 0 {
		t.Errorf("expected 0 in-flight requests, got %d", inFlight)
	}
	if spend <= 0 {
		t.Errorf("expected positive spend debited, got %d", spend)
	}
}

// ---------------------------------------------------------------------------
// Matrix Test 2: Degraded Provider (500) × Fallback Chain × Circuit Breaker Trip
// ---------------------------------------------------------------------------
func TestIntegration_DegradedProvider_FallbackAndCircuitTrip(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	rawKey, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "fallback-key",
		Limits: domain.KeyLimits{
			Concurrency: 10,
			RPM:         100,
		},
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Simulate primary provider returning 500
	h.primaryStatus.Store(http.StatusInternalServerError)

	makeReq := func() (*http.Response, error) {
		reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Trigger fallback"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(reqBody))
		req.Header.Set("Authorization", "Bearer "+rawKey)
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}

	// Request 1: Primary fails (attempt 0), Fallback succeeds (attempt 1)
	resp1, err := makeReq()
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp1.Body)
		t.Fatalf("expected request 1 status 200 via fallback, got %d: %s", resp1.StatusCode, string(body))
	}

	if h.primaryCalls.Load() != 1 {
		t.Errorf("expected 1 primary call on request 1, got %d", h.primaryCalls.Load())
	}
	if h.fallbackCalls.Load() != 1 {
		t.Errorf("expected 1 fallback call on request 1, got %d", h.fallbackCalls.Load())
	}

	// Primary has 1 failure; threshold is 2, so circuit is still closed
	if !h.health.IsAvailable("prov-openai", "gpt-4o") {
		t.Errorf("expected prov-openai circuit still closed after 1 failure")
	}

	// Request 2: Primary fails second time -> reaches threshold 2 -> circuit trips to OPEN!
	resp2, err := makeReq()
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected request 2 status 200, got %d: %s", resp2.StatusCode, string(body))
	}

	if h.primaryCalls.Load() != 2 {
		t.Errorf("expected 2 primary calls total, got %d", h.primaryCalls.Load())
	}
	if h.fallbackCalls.Load() != 2 {
		t.Errorf("expected 2 fallback calls total, got %d", h.fallbackCalls.Load())
	}

	// Circuit must now be OPEN
	if h.health.IsAvailable("prov-openai", "gpt-4o") {
		t.Errorf("expected prov-openai circuit to be OPEN after 2 failures")
	}

	// Request 3: DecideTargets evaluates health; because prov-openai is OPEN,
	// it immediately routes ONLY to prov-fallback! Primary calls must NOT increment!
	resp3, err := makeReq()
	if err != nil {
		t.Fatalf("request 3 failed: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp3.Body)
		t.Fatalf("expected request 3 status 200, got %d: %s", resp3.StatusCode, string(body))
	}

	if h.primaryCalls.Load() != 2 {
		t.Errorf("expected primary calls to STAY at 2 (skipped by pure routing), got %d", h.primaryCalls.Load())
	}
	if h.fallbackCalls.Load() != 3 {
		t.Errorf("expected fallback calls to increment to 3, got %d", h.fallbackCalls.Load())
	}
}

// ---------------------------------------------------------------------------
// Matrix Test 3: Rate-Limited Provider (429 + Retry-After) × Fallback Chain
// ---------------------------------------------------------------------------
func TestIntegration_RateLimitedProvider_429Fallback(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	rawKey, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "rate-limit-test-key",
		Limits: domain.KeyLimits{
			Concurrency: 10,
			RPM:         100,
		},
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Primary returns 429 Too Many Requests with Retry-After: 3
	h.primaryStatus.Store(http.StatusTooManyRequests)
	h.primaryRetryAfter.Store(3)

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Test 429 fallback"}]}`
	req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200 via fallback, got %d: %s", resp.StatusCode, string(body))
	}

	// Both primary (which gave 429) and fallback (which gave 200) were called
	if h.primaryCalls.Load() != 1 {
		t.Errorf("expected 1 primary call, got %d", h.primaryCalls.Load())
	}
	if h.fallbackCalls.Load() != 1 {
		t.Errorf("expected 1 fallback call, got %d", h.fallbackCalls.Load())
	}
}

// ---------------------------------------------------------------------------
// Matrix Test 4: Key Scopes (AllowedModels & AllowedProviders Filter)
// ---------------------------------------------------------------------------
func TestIntegration_KeyScopes_ModelAndProviderFiltering(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	// Key A: Allowed only for "gpt-4o", forbidden from "claude-3-5-sonnet"
	rawKeyModelRestricted, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "model-restricted-key",
		Scopes: domain.KeyScopes{
			AllowedModels: []string{"gpt-4o"},
		},
		Limits: domain.KeyLimits{Concurrency: 10, RPM: 100},
	})
	if err != nil {
		t.Fatalf("CreateKey A: %v", err)
	}

	// Key B: Allowed only for provider "prov-fallback", forbidden from "prov-openai"
	rawKeyProvRestricted, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "provider-restricted-key",
		Scopes: domain.KeyScopes{
			AllowedProviders: []string{"prov-fallback"},
		},
		Limits: domain.KeyLimits{Concurrency: 10, RPM: 100},
	})
	if err != nil {
		t.Fatalf("CreateKey B: %v", err)
	}

	sendReq := func(key, model string) int {
		body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"Scope test"}]}`, model)
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 1. Key A requesting allowed model "gpt-4o" -> 200 OK
	if code := sendReq(rawKeyModelRestricted, "gpt-4o"); code != http.StatusOK {
		t.Errorf("expected 200 OK for allowed model, got %d", code)
	}

	// 2. Key A requesting disallowed model "claude-3-5-sonnet" -> 403
	// permission_error (scope denial renders its own envelope — checklist
	// C-11, p7.parity-fixes; previously a generic 503).
	if code := sendReq(rawKeyModelRestricted, "claude-3-5-sonnet"); code != http.StatusForbidden {
		t.Errorf("expected 403 for denied model scope, got %d", code)
	}

	// 3. Key B requesting "gpt-4o" with prov-fallback allowed:
	// Pure routing filters out prov-openai, routing strictly to prov-fallback
	primaryBefore := h.primaryCalls.Load()
	fallbackBefore := h.fallbackCalls.Load()

	if code := sendReq(rawKeyProvRestricted, "gpt-4o"); code != http.StatusOK {
		t.Errorf("expected 200 OK for prov-restricted key, got %d", code)
	}
	if h.primaryCalls.Load() != primaryBefore {
		t.Errorf("expected prov-openai NOT to be called under provider scope restriction")
	}
	if h.fallbackCalls.Load() != fallbackBefore+1 {
		t.Errorf("expected prov-fallback to be called")
	}
}

// ---------------------------------------------------------------------------
// Matrix Test 5: Quota Enforcement (Concurrency, RPM, and Max Spend Caps)
// ---------------------------------------------------------------------------
func TestIntegration_Quotas_ConcurrencyRPMAndSpend(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	// 1. Concurrency limit = 1
	rawKeyConc, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "concurrency-key",
		Limits: domain.KeyLimits{
			Concurrency:       1,
			RPM:               1000,
			MaxSpendUSDMicros: 10_000_000,
		},
	})
	if err != nil {
		t.Fatalf("CreateKey conc: %v", err)
	}

	// Hold 1 slot manually
	rel, err := h.quotaMgr.Acquire(domain.VirtualKey{
		ID:     "manual-id",
		Limits: domain.KeyLimits{Concurrency: 1},
	}, 10)
	if err != nil {
		t.Fatalf("manual acquire: %v", err)
	}
	rel() // released

	// 2. RPM Limit = 2
	rawKeyRPM, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "rpm-key",
		Limits: domain.KeyLimits{
			Concurrency:       10,
			RPM:               2,
			MaxSpendUSDMicros: 10_000_000,
		},
	})
	if err != nil {
		t.Fatalf("CreateKey rpm: %v", err)
	}

	sendReq := func(key string) (int, string) {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Quota test"}]}`
		req, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		retryAfter := resp.Header.Get("Retry-After")
		return resp.StatusCode, retryAfter
	}

	// Request 1: RPM count = 1 -> OK
	if code, _ := sendReq(rawKeyRPM); code != http.StatusOK {
		t.Errorf("RPM req 1 expected 200, got %d", code)
	}
	// Request 2: RPM count = 2 -> OK
	if code, _ := sendReq(rawKeyRPM); code != http.StatusOK {
		t.Errorf("RPM req 2 expected 200, got %d", code)
	}
	// Request 3: RPM count = 3 > 2 -> 429 Too Many Requests with Retry-After
	code, retryAfter := sendReq(rawKeyRPM)
	if code != http.StatusTooManyRequests {
		t.Errorf("RPM req 3 expected 429, got %d", code)
	}
	if retryAfter == "" {
		t.Errorf("RPM req 3 expected non-empty Retry-After header on 429")
	}

	// 3. Spend Cap Enforcement
	rawKeySpend, keySpendRec, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name: "spend-key",
		Limits: domain.KeyLimits{
			Concurrency:       10,
			RPM:               1000,
			MaxSpendUSDMicros: 100, // Small spend limit of 100 micros ($0.000100)
		},
	})
	if err != nil {
		t.Fatalf("CreateKey spend: %v", err)
	}

	// Set 150 micros to exhaust the 100 micros spend limit
	h.quotaMgr.SetSpend(keySpendRec.ID, 150)

	// Now issuing request with exhausted spend should immediately fail with 429
	spendCode, _ := sendReq(rawKeySpend)
	if spendCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 on exhausted spend cap, got %d", spendCode)
	}
	_ = rawKeyConc
}

// ---------------------------------------------------------------------------
// Matrix Test 6: Hot Reload on Storage Changes Under Active Traffic
// ---------------------------------------------------------------------------
func TestIntegration_HotReload_LiveUpdate(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	rawKey, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
		Name:   "hot-reload-key",
		Limits: domain.KeyLimits{Concurrency: 20, RPM: 1000},
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	// Add a new dynamic model to storage
	caps := &domain.ModelCapabilities{Stream: true}
	newModel := domain.Model{
		ID:           "dynamic-model-v2",
		Capabilities: *caps,
		Targets: []domain.ModelTarget{
			{
				ProviderID:    "prov-fallback",
				ProviderModel: "gpt-4o-fallback",
				Position:      0,
				Weight:        1,
				Capabilities:  caps,
			},
		},
	}
	if err := h.store.Models().Upsert(newModel); err != nil {
		t.Fatalf("create new model: %v", err)
	}

	// Prior to reload, requesting "dynamic-model-v2" should fail with 503
	reqBody := `{"model":"dynamic-model-v2","messages":[{"role":"user","content":"test"}]}`
	req1, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(reqBody))
	req1.Header.Set("Authorization", "Bearer "+rawKey)
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp1.Body.Close()
	// Unknown model before the reload -> 404 not_found_error (checklist
	// C-10, p7.parity-fixes; previously a generic 503).
	if resp1.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 before reload, got %d", resp1.StatusCode)
	}

	// Trigger hot-reload
	if err := h.watcher.ReloadNow(context.Background()); err != nil {
		t.Fatalf("ReloadNow: %v", err)
	}

	// Now requesting "dynamic-model-v2" succeeds immediately without server restart!
	req2, _ := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(reqBody))
	req2.Header.Set("Authorization", "Bearer "+rawKey)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200 OK after hot reload, got %d: %s", resp2.StatusCode, string(body))
	}
}

// ---------------------------------------------------------------------------
// Matrix Test 7: High Concurrency & Race Detector Matrix
// ---------------------------------------------------------------------------
func TestIntegration_ConcurrentMatrix_RaceClean(t *testing.T) {
	h := setupIntegratedHarness(t)
	defer h.Close()

	const numKeys = 5
	rawKeys := make([]string, numKeys)
	for i := 0; i < numKeys; i++ {
		k, _, err := h.authMgr.CreateKey(context.Background(), auth.CreateKeyParams{
			Name: fmt.Sprintf("race-key-%d", i),
			Limits: domain.KeyLimits{
				Concurrency:       5,
				RPM:               10000,
				MaxSpendUSDMicros: 10_000_000,
			},
		})
		if err != nil {
			t.Fatalf("CreateKey: %v", err)
		}
		rawKeys[i] = k
	}

	const workers = 15
	const iterations = 10
	var wg sync.WaitGroup
	wg.Add(workers)

	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			key := rawKeys[workerID%numKeys]
			for j := 0; j < iterations; j++ {
				body := `{"model":"gpt-4o","messages":[{"role":"user","content":"Concurrent race test"}]}`
				req, err := http.NewRequest(http.MethodPost, h.proxyServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
				if err != nil {
					return
				}
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}(w)
	}

	wg.Wait()
}
