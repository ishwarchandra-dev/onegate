package observability_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

func TestMetricsRegistry_UnitsAndHelpDocumentation(t *testing.T) {
	reg := observability.NewRegistry(observability.MetricsConfig{})

	reg.ObserveRequest("POST", 200)
	reg.ObservePanic()
	reg.ObserveProxyRequest("openai", "mock-provider", "gpt-4o", "success", 120*time.Millisecond)
	reg.ObserveTTFT("mock-provider", "gpt-4o", 25*time.Millisecond)
	reg.IncInflight("openai")
	reg.ObserveQuotaRejection("rpm_limit")
	reg.SetUsagePipelineStats(observability.PipelineStats{
		Captured: 10,
		Dropped:  1,
		Written:  9,
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reg.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	body := w.Body.String()

	requiredMetrics := []struct {
		name        string
		hasUnit     bool
		hasConsumer bool
	}{
		{name: "onereq_total", hasUnit: true, hasConsumer: true},
		{name: "onereq_latency_seconds", hasUnit: true, hasConsumer: true},
		{name: "onereq_ttft_seconds", hasUnit: true, hasConsumer: true},
		{name: "onereq_inflight_requests", hasUnit: true, hasConsumer: true},
		{name: "onereq_quota_rejections_total", hasUnit: true, hasConsumer: true},
		{name: "onegate_http_requests_total", hasUnit: true, hasConsumer: true},
		{name: "onegate_http_panics_total", hasUnit: true, hasConsumer: true},
	}

	for _, req := range requiredMetrics {
		// Check that metric exists in body
		if !strings.Contains(body, req.name) {
			t.Errorf("missing metric %s in /metrics output", req.name)
		}

		// Acceptance: Each metric documents its dashboard consumer
		helpLinePrefix := fmt.Sprintf("# HELP %s", req.name)
		var helpLine string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, helpLinePrefix) {
				helpLine = line
				break
			}
		}
		if helpLine == "" {
			t.Errorf("missing # HELP for metric %s", req.name)
			continue
		}
		if !strings.Contains(helpLine, "Dashboard consumer:") {
			t.Errorf("metric %s does not document its dashboard consumer: %s", req.name, helpLine)
		}
	}
}

func TestMetricsRegistry_ExpositionFormat(t *testing.T) {
	reg := observability.NewRegistry(observability.MetricsConfig{})

	reg.ObserveProxyRequest("openai", "provider-a", "gpt-4o", "success", 200*time.Millisecond)
	reg.ObserveTTFT("provider-a", "gpt-4o", 50*time.Millisecond)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reg.Handler().ServeHTTP(w, r)

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") || !strings.Contains(ct, "version=0.0.4") {
		t.Fatalf("unexpected Content-Type: %s", ct)
	}

	body := w.Body.String()

	// Check onereq_total series
	expectedCounter := `onereq_total{protocol="openai",provider="provider-a",model="gpt-4o",status="success"} 1`
	if !strings.Contains(body, expectedCounter) {
		t.Errorf("expected counter line %q in:\n%s", expectedCounter, body)
	}

	// Check histogram lines
	expectedBucket := `onereq_latency_seconds_bucket{provider="provider-a",model="gpt-4o",status="success",le="0.25"} 1`
	if !strings.Contains(body, expectedBucket) {
		t.Errorf("expected histogram bucket %q in:\n%s", expectedBucket, body)
	}
	expectedInf := `onereq_latency_seconds_bucket{provider="provider-a",model="gpt-4o",status="success",le="+Inf"} 1`
	if !strings.Contains(body, expectedInf) {
		t.Errorf("expected histogram +Inf bucket %q in:\n%s", expectedInf, body)
	}
	expectedCount := `onereq_latency_seconds_count{provider="provider-a",model="gpt-4o",status="success"} 1`
	if !strings.Contains(body, expectedCount) {
		t.Errorf("expected histogram count %q in:\n%s", expectedCount, body)
	}
}

func TestMetricsRegistry_InflightGauge(t *testing.T) {
	reg := observability.NewRegistry(observability.MetricsConfig{})

	reg.IncInflight("openai")
	reg.IncInflight("openai")
	reg.DecInflight("openai")
	reg.IncInflight("anthropic")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reg.Handler().ServeHTTP(w, r)

	body := w.Body.String()

	if !strings.Contains(body, `onereq_inflight_requests{protocol="openai"} 1`) {
		t.Errorf("expected inflight openai 1 in:\n%s", body)
	}
	if !strings.Contains(body, `onereq_inflight_requests{protocol="anthropic"} 1`) {
		t.Errorf("expected inflight anthropic 1 in:\n%s", body)
	}
}

func TestMetricsRegistry_QuotaRejections(t *testing.T) {
	reg := observability.NewRegistry(observability.MetricsConfig{})

	reg.ObserveQuotaRejection("rpm_limit")
	reg.ObserveQuotaRejection("rpm_limit")
	reg.ObserveQuotaRejection("spend_limit")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reg.Handler().ServeHTTP(w, r)

	body := w.Body.String()

	if !strings.Contains(body, `onereq_quota_rejections_total{reason="rpm_limit"} 2`) {
		t.Errorf("expected quota rejections rpm_limit 2 in:\n%s", body)
	}
	if !strings.Contains(body, `onereq_quota_rejections_total{reason="spend_limit"} 1`) {
		t.Errorf("expected quota rejections spend_limit 1 in:\n%s", body)
	}
}

func TestMetricsRegistry_AdminGating(t *testing.T) {
	const adminSecret = "super-admin-token-12345"

	// 1. Ungated when no token configured
	ungatedReg := observability.NewRegistry(observability.MetricsConfig{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	ungatedReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("ungated registry: expected 200, got %d", w.Code)
	}

	// 2. Gated registry
	gatedReg := observability.NewRegistry(observability.MetricsConfig{
		AdminToken: adminSecret,
	})

	// Missing auth -> 401
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	gatedReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", w.Code)
	}

	// Wrong token -> 401
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer wrong-token")
	gatedReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for wrong token, got %d", w.Code)
	}

	// Valid Bearer token -> 200 OK
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+adminSecret)
	gatedReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		body, _ := io.ReadAll(w.Body)
		t.Fatalf("expected 200 OK for valid Bearer token, got %d: %s", w.Code, string(body))
	}

	// Valid X-Admin-Key header -> 200 OK
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("X-Admin-Key", adminSecret)
	gatedReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for valid X-Admin-Key, got %d", w.Code)
	}

	// Custom AdminAuth function
	customReg := observability.NewRegistry(observability.MetricsConfig{
		AdminAuth: func(req *http.Request) bool {
			return req.Header.Get("X-Custom-Admin") == "allow"
		},
	})

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	customReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("custom auth: expected 401 without header, got %d", w.Code)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("X-Custom-Admin", "allow")
	customReg.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("custom auth: expected 200 with header, got %d", w.Code)
	}
}

func TestMetricsRegistry_ConcurrencyRace(t *testing.T) {
	reg := observability.NewRegistry(observability.MetricsConfig{})

	const numGoroutines = 10
	const numOps = 200
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			provider := fmt.Sprintf("p-%d", id%3)
			model := "gpt-4o"
			protocol := "openai"

			for j := 0; j < numOps; j++ {
				reg.ObserveProxyRequest(protocol, provider, model, "success", time.Duration(j)*time.Millisecond)
				reg.ObserveTTFT(provider, model, time.Duration(j%50)*time.Millisecond)
				reg.IncInflight(protocol)
				reg.DecInflight(protocol)
				reg.ObserveQuotaRejection("rpm_limit")
				reg.ObserveRequest("POST", 200)
			}
		}(i)
	}

	// Concurrently read from /metrics while writes are happening
	stopRead := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopRead:
				return
			default:
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
				reg.Handler().ServeHTTP(w, r)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	wg.Wait()
	close(stopRead)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reg.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after concurrent ops, got %d", w.Code)
	}
}
