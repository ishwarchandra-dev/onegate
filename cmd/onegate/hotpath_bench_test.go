package main

// hotpath_bench_test.go — p8.perf-fixes: the full production data-plane
// composition, benchmarked end to end against the in-process mock
// provider: ingest auth (HMAC lookup) -> quota gate -> routing resolver
// (+ credential TTL cache) -> fallback engine -> OpenAI translation ->
// mock upstream -> response translation -> usage pipeline + metrics.
//
// This is the profile surface for the optimization cycle: run with
//
//      go test ./cmd/onegate -bench BenchmarkHotPath -benchmem -count 3
//      go test ./cmd/onegate -bench BenchmarkHotPathNonStream -cpuprofile cpu.out -memprofile mem.out
//
// Layering note: this file lives in the composition root because the
// layering contract (internal/proxy/proxy_test.go) forbids proxy tests
// from importing internal/routing or internal/ratelimit.
import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/mockprovider"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/server"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

type benchWorld struct {
	proxyURL string
	rawKey   string
	http     *http.Client
	srv      *httptest.Server
	mock     *httptest.Server
	upstream *client.Client
	store    *storage.Store
	pipeline *observability.UsagePipeline
}

func newBenchWorld(b *testing.B) *benchWorld {
	b.Helper()

	// Silence per-request logging: the engine falls back to slog.Default(),
	// and log I/O would otherwise dominate the profile.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	dir := b.TempDir()
	store, err := storage.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatalf("open storage: %v", err)
	}
	if err := store.Migrate(); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		b.Fatalf("rand master: %v", err)
	}
	cipher, err := auth.NewCipher(master, auth.PurposeProviderKeys)
	if err != nil {
		b.Fatalf("cipher: %v", err)
	}
	pepper, err := auth.Pepper(master)
	if err != nil {
		b.Fatalf("pepper: %v", err)
	}

	// Mock upstream.
	mock := httptest.NewServer(mockprovider.NewHandler())

	// Provider row with a sealed credential.
	enc, err := cipher.Encrypt([]byte("sk-bench"))
	if err != nil {
		b.Fatalf("seal: %v", err)
	}
	if err := store.Providers().Upsert(&storage.ProviderRecord{
		Provider: domain.Provider{
			ID: "bench-openai", Name: "Bench OpenAI",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  mock.URL, Enabled: true,
		},
		APIKeyEnc: enc,
	}); err != nil {
		b.Fatalf("provider upsert: %v", err)
	}

	// Virtual key (HMAC-peppered hash, as production).
	rawKey, prefix := auth.GenerateVirtualKey()
	if _, err := store.VirtualKeys().Create(domain.VirtualKey{
		Name: "bench-key", Prefix: prefix,
		KeyHash: auth.HashVirtualKey(pepper, rawKey),
		Status:  domain.KeyActive,
	}); err != nil {
		b.Fatalf("vkey create: %v", err)
	}

	// Routing registry: one canonical model -> one target.
	registry := routing.NewRegistry()
	registry.Load(
		[]domain.Model{{
			ID: "gpt-4o",
			Targets: []domain.ModelTarget{{
				ProviderID: "bench-openai", ProviderModel: "gpt-4o",
				Weight: 1, Position: 1, CostMultiplier: 100,
			}},
			Capabilities: domain.ModelCapabilities{Stream: true, Tools: true},
			CreatedMS:    time.Now().UnixMilli(),
		}},
		[]domain.Provider{{
			ID: "bench-openai", Name: "Bench OpenAI",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  mock.URL, Enabled: true,
		}},
		nil,
	)
	health := routing.NewHealthTracker(routing.DefaultHealthConfig())

	// Usage pipeline (bounded queue + batch writer into the store).
	prices := ratelimit.NewPriceTable()
	pipeline := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize: 10_000, BatchSize: 100, FlushInterval: 100 * time.Millisecond,
		Writer: store.Requests(), Prices: prices,
	})
	ctx, cancel := context.WithCancel(context.Background())
	pipeline.Start(ctx)

	metricsRegistry := observability.NewRegistry(observability.MetricsConfig{})

	upstream := client.New(client.TransportConfig{}, nil)
	credentials := newProviderCredentials(store, cipher, 15*time.Second)
	resolver := &routingResolver{registry: registry, health: health, credentials: credentials}
	quota := ratelimit.NewQuotaManager(prices)

	var qp *quotaProxy
	engine := fallback.NewEngine(fallback.Config{
		Resolver: resolver,
		Client:   upstream,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnUsage: func(ue fallback.UsageEvent) {
			pipeline.EnqueueEvent(ue.ToObservabilityEvent())
			if qp != nil {
				qp.onUsage(ue)
			}
			metricsRegistry.ObserveProxyRequest(
				string(ue.Protocol), ue.ProviderID, ue.ModelServed,
				string(ue.Status), ue.Duration)
			if ue.Stream && ue.TTFT > 0 {
				metricsRegistry.ObserveTTFT(ue.ProviderID, ue.ModelServed, ue.TTFT)
			}
		},
		OnTrace: func(tr fallback.Trace) {
			for _, at := range tr.Attempts {
				if at.Error != nil {
					health.RecordFailure(at.ProviderID, at.Model, string(at.Error.Type))
				} else if tr.Completed {
					health.RecordSuccess(at.ProviderID, at.Model)
				}
			}
		},
	})
	qp = newQuotaProxy(engine, quota, metricsRegistry)

	router := server.New(server.Options{Metrics: metricsRegistry})
	ingest.Register(router.Mux(), ingest.Deps{
		Auth:   auth.NewVerifier(store, pepper),
		Proxy:  qp,
		Models: &registryModelLister{registry: registry},
	})
	srv := httptest.NewServer(router.Handler())

	b.Cleanup(func() {
		srv.Close()
		mock.Close()
		upstream.CloseIdleConnections()
		cancel()
		_ = pipeline.Stop()
		store.Close()
	})

	return &benchWorld{
		proxyURL: srv.URL, rawKey: rawKey,
		http: &http.Client{Timeout: 30 * time.Second},
		srv:  srv, mock: mock, upstream: upstream,
		store: store, pipeline: pipeline,
	}
}

// doRequest sends one chat completion through the gateway and drains it.
func (w *benchWorld) doRequest(stream bool) error {
	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","messages":[{"role":"user","content":"Say hello in five words."}]`)
	if stream {
		sb.WriteString(`,"stream":true`)
	}
	sb.WriteString(`}`)
	req, err := http.NewRequest(http.MethodPost, w.proxyURL+"/v1/chat/completions", strings.NewReader(sb.String()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.rawKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{code: resp.StatusCode, body: string(body)}
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return strings.TrimSpace(e.body) }

// warmup primes caches (credential TTL entry, connection pools, lazy
// registry state) so the benchmark measures steady state, like the soak.
func (w *benchWorld) warmup(b *testing.B) {
	b.Helper()
	for i := 0; i < 20; i++ {
		if err := w.doRequest(false); err != nil {
			b.Fatalf("warmup: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond) // let the usage pipeline settle
}

func BenchmarkHotPathNonStream(b *testing.B) {
	w := newBenchWorld(b)
	w.warmup(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.doRequest(false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHotPathStream(b *testing.B) {
	w := newBenchWorld(b)
	w.warmup(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.doRequest(true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHotPathParallel(b *testing.B) {
	w := newBenchWorld(b)
	w.warmup(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := w.doRequest(false); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkHotPathJSON sanity-checks that responses parse: kept separate
// so JSON decode cost never hides inside the transport benchmarks.
func BenchmarkHotPathJSON(b *testing.B) {
	payload := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there, how can I help?"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":7,"total_tokens":16}}`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var probe map[string]any
		if err := json.Unmarshal([]byte(payload), &probe); err != nil {
			b.Fatal(err)
		}
	}
}
