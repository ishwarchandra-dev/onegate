package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Metrics is the observation seam between the middleware chain and the
// metrics subsystem. Phase 3 ships miniRegistry: tiny counters that make
// /metrics real end-to-end. Phase 5 (p5.metrics) injects the full
// Prometheus registry — histograms, TTFT, per-provider counters — as an
// Options.Metrics implementation; the middleware code does not change.
//
// Implementations must be safe for concurrent use.
type Metrics interface {
	// ObserveRequest counts one completed request. method is the HTTP
	// verb (bounded set); code is the response status.
	ObserveRequest(method string, code int)

	// ObservePanic counts recovered panics.
	ObservePanic()

	// Handler serves the exposition format (Prometheus text).
	Handler() http.Handler
}

// miniRegistry is the default Metrics: two counter families with
// strictly bounded label cardinality.
//
//   - onegate_http_requests_total{method,code}: method is one of a
//     handful of verbs; code is a standard HTTP status — dozens of
//     series at most.
//   - onegate_http_panics_total: scalar.
//
// p5.metrics owns the production registry (onereq_total and friends per
// the OmniRoute naming); this default keeps /metrics honest until then
// and gives the middleware something testable to assert against.
type miniRegistry struct {
	mu       sync.Mutex
	requests map[string]uint64 // key: "METHOD|CODE"
	panics   uint64
}

// NewMiniRegistry builds the default registry.
func NewMiniRegistry() Metrics {
	return &miniRegistry{requests: map[string]uint64{}}
}

func (m *miniRegistry) ObserveRequest(method string, code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[fmt.Sprintf("%s|%d", method, code)]++
}

func (m *miniRegistry) ObservePanic() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.panics++
}

// Handler renders the Prometheus text exposition format.
func (m *miniRegistry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		keys := make([]string, 0, len(m.requests))
		for k := range m.requests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		panics := m.panics
		requests := make(map[string]uint64, len(m.requests))
		for k, v := range m.requests {
			requests[k] = v
		}
		m.mu.Unlock()

		var b strings.Builder
		b.WriteString("# HELP onegate_http_requests_total Completed HTTP requests.\n")
		b.WriteString("# TYPE onegate_http_requests_total counter\n")
		for _, k := range keys {
			parts := strings.SplitN(k, "|", 2)
			fmt.Fprintf(&b, "onegate_http_requests_total{method=%q,code=%q} %d\n",
				parts[0], parts[1], requests[k])
		}
		b.WriteString("# HELP onegate_http_panics_total Handler panics recovered by the gateway.\n")
		b.WriteString("# TYPE onegate_http_panics_total counter\n")
		fmt.Fprintf(&b, "onegate_http_panics_total %d\n", panics)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}
