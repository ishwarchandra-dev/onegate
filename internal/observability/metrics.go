package observability

import (
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"

	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Default latency histogram buckets in seconds (5ms to 60s).
var defaultLatencyBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0,
}

// Default TTFT histogram buckets in seconds (5ms to 5s).
var defaultTTFTBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 5.0,
}

// MetricsConfig configures the Prometheus metrics registry.
type MetricsConfig struct {
	// AdminToken enables admin-gating on the /metrics endpoint.
	// When non-empty, requests must provide matching Authorization: Bearer <token>
	// or X-Admin-Key: <token>. When empty, /metrics is ungated (loopback bind).
	AdminToken string

	// AdminAuth is an optional custom auth check. If set, it takes precedence
	// over AdminToken.
	AdminAuth func(r *http.Request) bool

	// LatencyBuckets overrides default latency histogram buckets.
	LatencyBuckets []float64

	// TTFTBuckets overrides default TTFT histogram buckets.
	TTFTBuckets []float64
}

// Histogram tracks observations into fixed buckets.
type Histogram struct {
	mu      sync.RWMutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

func newHistogram(buckets []float64) *Histogram {
	b := make([]float64, len(buckets))
	copy(b, buckets)
	sort.Float64s(b)
	return &Histogram{
		buckets: b,
		counts:  make([]uint64, len(b)),
	}
}

func (h *Histogram) Observe(val float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += val
	for i, limit := range h.buckets {
		if val <= limit {
			h.counts[i]++
		}
	}
}

// Registry is OneGate's Prometheus metrics registry. It collects request
// counters, latency/TTFT histograms, inflight gauges, and quota rejection
// counters, exposing them in Prometheus text format (0.0.4) at /metrics.
//
// All methods are thread-safe and non-blocking for proxy hot-path callers.
type Registry struct {
	adminToken string
	adminAuth  func(r *http.Request) bool

	latencyBuckets []float64
	ttftBuckets    []float64

	mu sync.RWMutex

	// onereq_total{protocol,provider,model,status}
	// Help: Total requests processed by the gateway.
	// Dashboard consumer: Overview & Providers views (RPS, traffic volume, outcome distribution).
	onereqTotal map[string]*atomic.Uint64

	// onereq_latency_seconds{provider,model,status}
	// Help: End-to-end request duration in seconds from receipt to response completion.
	// Dashboard consumer: Latency percentiles chart (p50, p90, p99) in Overview & Models views.
	onereqLatency map[string]*Histogram

	// onereq_ttft_seconds{provider,model}
	// Help: Time to first token in seconds for streaming responses.
	// Dashboard consumer: Models view streaming responsiveness card.
	onereqTTFT map[string]*Histogram

	// onereq_inflight_requests{protocol}
	// Help: Number of active requests currently in-flight across the gateway.
	// Dashboard consumer: System status badge, Live view active streams.
	inflightRequests map[string]*atomic.Int64

	// onereq_quota_rejections_total{reason}
	// Help: Total requests rejected due to rate limits or quota exhaustion.
	// Dashboard consumer: Virtual Keys view quota warning banner, Rate limits alert panel.
	quotaRejections map[string]*atomic.Uint64

	// onegate_http_requests_total{method,code}
	// Help: Completed HTTP requests served by the gateway.
	// Dashboard consumer: HTTP server health.
	httpRequests map[string]*atomic.Uint64

	// onegate_http_panics_total
	// Help: Handler panics recovered by the gateway.
	// Dashboard consumer: Gateway stability alert.
	httpPanics atomic.Uint64

	// Usage pipeline counters
	usageCaptured atomic.Int64
	usageDropped  atomic.Int64
	usageWritten  atomic.Int64
}

// NewRegistry constructs a new Prometheus metrics registry.
func NewRegistry(cfg MetricsConfig) *Registry {
	latBuckets := cfg.LatencyBuckets
	if len(latBuckets) == 0 {
		latBuckets = defaultLatencyBuckets
	}
	ttftBuckets := cfg.TTFTBuckets
	if len(ttftBuckets) == 0 {
		ttftBuckets = defaultTTFTBuckets
	}

	return &Registry{
		adminToken:       cfg.AdminToken,
		adminAuth:        cfg.AdminAuth,
		latencyBuckets:   latBuckets,
		ttftBuckets:      ttftBuckets,
		onereqTotal:      make(map[string]*atomic.Uint64),
		onereqLatency:    make(map[string]*Histogram),
		onereqTTFT:       make(map[string]*Histogram),
		inflightRequests: make(map[string]*atomic.Int64),
		quotaRejections:  make(map[string]*atomic.Uint64),
		httpRequests:     make(map[string]*atomic.Uint64),
	}
}

// ObserveRequest records completed HTTP requests from server middleware.
// Implements server.Metrics.
func (reg *Registry) ObserveRequest(method string, code int) {
	key := fmt.Sprintf("%s|%d", method, code)
	reg.getOrInitCounter(&reg.httpRequests, key).Add(1)
}

// ObservePanic records recovered panics from server middleware.
// Implements server.Metrics.
func (reg *Registry) ObservePanic() {
	reg.httpPanics.Add(1)
}

// ObserveProxyRequest records a completed proxied LLM request.
// Bounded label cardinality: protocol (openai|anthropic|gemini), status (success|error|cancelled).
func (reg *Registry) ObserveProxyRequest(protocol, provider, model, status string, duration time.Duration) {
	if protocol == "" {
		protocol = "unknown"
	}
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "unknown"
	}
	if status == "" {
		status = "unknown"
	}

	// Counter: onereq_total
	countKey := protocol + "|" + provider + "|" + model + "|" + status
	reg.getOrInitCounter(&reg.onereqTotal, countKey).Add(1)

	// Histogram: onereq_latency_seconds
	latKey := provider + "|" + model + "|" + status
	hist := reg.getOrInitHistogram(&reg.onereqLatency, latKey, reg.latencyBuckets)
	hist.Observe(duration.Seconds())
}

// ObserveTTFT records time-to-first-token for streaming requests.
func (reg *Registry) ObserveTTFT(provider, model string, ttft time.Duration) {
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "unknown"
	}
	key := provider + "|" + model
	hist := reg.getOrInitHistogram(&reg.onereqTTFT, key, reg.ttftBuckets)
	hist.Observe(ttft.Seconds())
}

// IncInflight increments the active in-flight request gauge for protocol.
func (reg *Registry) IncInflight(protocol string) {
	if protocol == "" {
		protocol = "unknown"
	}
	reg.getOrInitGauge(&reg.inflightRequests, protocol).Add(1)
}

// DecInflight decrements the active in-flight request gauge for protocol.
func (reg *Registry) DecInflight(protocol string) {
	if protocol == "" {
		protocol = "unknown"
	}
	reg.getOrInitGauge(&reg.inflightRequests, protocol).Add(-1)
}

// ObserveQuotaRejection records a rate limit or spend limit rejection.
// reason is bounded: "rpm_limit" | "tpm_limit" | "concurrency_limit" | "spend_limit".
func (reg *Registry) ObserveQuotaRejection(reason string) {
	if reason == "" {
		reason = "quota_exceeded"
	}
	reg.getOrInitCounter(&reg.quotaRejections, reason).Add(1)
}

// SetUsagePipelineStats updates the usage pipeline counters for exposition.
func (reg *Registry) SetUsagePipelineStats(stats PipelineStats) {
	reg.usageCaptured.Store(stats.Captured)
	reg.usageDropped.Store(stats.Dropped)
	reg.usageWritten.Store(stats.Written)
}

// Handler returns an http.Handler that renders the Prometheus text exposition
// format (0.0.4) at /metrics. If configured with AdminToken or AdminAuth, it
// enforces admin authorization. Implements server.Metrics.
func (reg *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reg.isAuthorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="OneGate Metrics"`)
			http.Error(w, "Unauthorized: admin credentials required for /metrics", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		var b strings.Builder
		reg.render(&b)
		_, _ = w.Write([]byte(b.String()))
	})
}

func (reg *Registry) isAuthorized(r *http.Request) bool {
	if reg.adminAuth != nil {
		return reg.adminAuth(r)
	}
	if reg.adminToken == "" {
		return true // ungated when no token configured
	}

	// Check Authorization: Bearer <token>
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if subtle.ConstantTimeCompare([]byte(token), []byte(reg.adminToken)) == 1 {
			return true
		}
	}

	// Check X-Admin-Key: <token>
	if adminKey := r.Header.Get("X-Admin-Key"); adminKey != "" {
		if subtle.ConstantTimeCompare([]byte(adminKey), []byte(reg.adminToken)) == 1 {
			return true
		}
	}

	return false
}

func (reg *Registry) render(b *strings.Builder) {
	// 1. onereq_total
	b.WriteString("# HELP onereq_total Total requests processed by the gateway. Dashboard consumer: Overview & Providers views (RPS, traffic volume, outcome distribution).\n")
	b.WriteString("# TYPE onereq_total counter\n")
	reg.mu.RLock()
	onereqKeys := make([]string, 0, len(reg.onereqTotal))
	for k := range reg.onereqTotal {
		onereqKeys = append(onereqKeys, k)
	}
	sort.Strings(onereqKeys)
	for _, k := range onereqKeys {
		val := reg.onereqTotal[k].Load()
		parts := strings.Split(k, "|")
		if len(parts) == 4 {
			fmt.Fprintf(b, "onereq_total{protocol=%q,provider=%q,model=%q,status=%q} %d\n",
				parts[0], parts[1], parts[2], parts[3], val)
		}
	}

	// 2. onereq_latency_seconds
	b.WriteString("# HELP onereq_latency_seconds End-to-end request duration in seconds from receipt to response completion. Dashboard consumer: Latency percentiles chart (p50, p90, p99) in Overview & Models views.\n")
	b.WriteString("# TYPE onereq_latency_seconds histogram\n")
	latKeys := make([]string, 0, len(reg.onereqLatency))
	for k := range reg.onereqLatency {
		latKeys = append(latKeys, k)
	}
	sort.Strings(latKeys)
	for _, k := range latKeys {
		h := reg.onereqLatency[k]
		parts := strings.Split(k, "|")
		if len(parts) == 3 {
			renderHistogram(b, "onereq_latency_seconds",
				fmt.Sprintf("provider=%q,model=%q,status=%q", parts[0], parts[1], parts[2]),
				h)
		}
	}

	// 3. onereq_ttft_seconds
	b.WriteString("# HELP onereq_ttft_seconds Time to first token in seconds for streaming responses. Dashboard consumer: Models view streaming responsiveness card.\n")
	b.WriteString("# TYPE onereq_ttft_seconds histogram\n")
	ttftKeys := make([]string, 0, len(reg.onereqTTFT))
	for k := range reg.onereqTTFT {
		ttftKeys = append(ttftKeys, k)
	}
	sort.Strings(ttftKeys)
	for _, k := range ttftKeys {
		h := reg.onereqTTFT[k]
		parts := strings.Split(k, "|")
		if len(parts) == 2 {
			renderHistogram(b, "onereq_ttft_seconds",
				fmt.Sprintf("provider=%q,model=%q", parts[0], parts[1]),
				h)
		}
	}

	// 4. onereq_inflight_requests
	b.WriteString("# HELP onereq_inflight_requests Number of active requests currently in-flight across the gateway. Dashboard consumer: System status badge, Live view active streams.\n")
	b.WriteString("# TYPE onereq_inflight_requests gauge\n")
	inflightKeys := make([]string, 0, len(reg.inflightRequests))
	for k := range reg.inflightRequests {
		inflightKeys = append(inflightKeys, k)
	}
	sort.Strings(inflightKeys)
	for _, k := range inflightKeys {
		val := reg.inflightRequests[k].Load()
		fmt.Fprintf(b, "onereq_inflight_requests{protocol=%q} %d\n", k, val)
	}

	// 4b. Runtime health gauges (p8 hardening: soak monitors goroutine
	// and fd stability directly from /metrics).
	b.WriteString("# HELP onereq_go_goroutines Current goroutine count (leak detection for soak tests).\n")
	b.WriteString("# TYPE onereq_go_goroutines gauge\n")
	fmt.Fprintf(b, "onereq_go_goroutines %d\n", runtime.NumGoroutine())

	b.WriteString("# HELP onereq_go_os_threads Current OS thread count.\n")
	b.WriteString("# TYPE onereq_go_os_threads gauge\n")
	fmt.Fprintf(b, "onereq_go_os_threads %d\n", osThreads())

	b.WriteString("# HELP onereq_process_open_fds Open file descriptors (Linux; connection-leak detection).\n")
	b.WriteString("# TYPE onereq_process_open_fds gauge\n")
	fmt.Fprintf(b, "onereq_process_open_fds %d\n", countOpenFDs())

	// 5. onereq_quota_rejections_total
	b.WriteString("# HELP onereq_quota_rejections_total Total requests rejected due to rate limits or quota exhaustion. Dashboard consumer: Virtual Keys view quota warning banner, Rate limits alert panel.\n")
	b.WriteString("# TYPE onereq_quota_rejections_total counter\n")
	quotaKeys := make([]string, 0, len(reg.quotaRejections))
	for k := range reg.quotaRejections {
		quotaKeys = append(quotaKeys, k)
	}
	sort.Strings(quotaKeys)
	for _, k := range quotaKeys {
		val := reg.quotaRejections[k].Load()
		fmt.Fprintf(b, "onereq_quota_rejections_total{reason=%q} %d\n", k, val)
	}

	// 6. onegate_http_requests_total
	b.WriteString("# HELP onegate_http_requests_total Completed HTTP requests served by the gateway. Dashboard consumer: HTTP server health.\n")
	b.WriteString("# TYPE onegate_http_requests_total counter\n")
	httpKeys := make([]string, 0, len(reg.httpRequests))
	for k := range reg.httpRequests {
		httpKeys = append(httpKeys, k)
	}
	sort.Strings(httpKeys)
	for _, k := range httpKeys {
		val := reg.httpRequests[k].Load()
		parts := strings.Split(k, "|")
		if len(parts) == 2 {
			fmt.Fprintf(b, "onegate_http_requests_total{method=%q,code=%q} %d\n", parts[0], parts[1], val)
		}
	}

	// 7. onegate_http_panics_total
	b.WriteString("# HELP onegate_http_panics_total Handler panics recovered by the gateway. Dashboard consumer: Gateway stability alert.\n")
	b.WriteString("# TYPE onegate_http_panics_total counter\n")
	fmt.Fprintf(b, "onegate_http_panics_total %d\n", reg.httpPanics.Load())

	// 8. Usage pipeline counters
	b.WriteString("# HELP onereq_usage_events_captured_total Total usage events captured by the usage pipeline. Dashboard consumer: Observability health.\n")
	b.WriteString("# TYPE onereq_usage_events_captured_total counter\n")
	fmt.Fprintf(b, "onereq_usage_events_captured_total %d\n", reg.usageCaptured.Load())

	b.WriteString("# HELP onereq_usage_events_dropped_total Total usage events dropped due to bounded queue overflow. Dashboard consumer: Queue overflow alert.\n")
	b.WriteString("# TYPE onereq_usage_events_dropped_total counter\n")
	fmt.Fprintf(b, "onereq_usage_events_dropped_total %d\n", reg.usageDropped.Load())

	b.WriteString("# HELP onereq_usage_events_written_total Total usage events written to persistent storage. Dashboard consumer: Usage pipeline throughput.\n")
	b.WriteString("# TYPE onereq_usage_events_written_total counter\n")
	fmt.Fprintf(b, "onereq_usage_events_written_total %d\n", reg.usageWritten.Load())

	reg.mu.RUnlock()
}

func renderHistogram(b *strings.Builder, name, labelPrefix string, h *Histogram) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for i, limit := range h.buckets {
		fmt.Fprintf(b, "%s_bucket{%s,le=\"%g\"} %d\n", name, labelPrefix, limit, h.counts[i])
	}
	fmt.Fprintf(b, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, labelPrefix, h.count)
	fmt.Fprintf(b, "%s_sum{%s} %g\n", name, labelPrefix, h.sum)
	fmt.Fprintf(b, "%s_count{%s} %d\n", name, labelPrefix, h.count)
}

func (reg *Registry) getOrInitCounter(m *map[string]*atomic.Uint64, key string) *atomic.Uint64 {
	reg.mu.RLock()
	c, ok := (*m)[key]
	reg.mu.RUnlock()
	if ok {
		return c
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()
	if c, ok = (*m)[key]; ok {
		return c
	}
	c = &atomic.Uint64{}
	(*m)[key] = c
	return c
}

func (reg *Registry) getOrInitGauge(m *map[string]*atomic.Int64, key string) *atomic.Int64 {
	reg.mu.RLock()
	g, ok := (*m)[key]
	reg.mu.RUnlock()
	if ok {
		return g
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()
	if g, ok = (*m)[key]; ok {
		return g
	}
	g = &atomic.Int64{}
	(*m)[key] = g
	return g
}

func (reg *Registry) getOrInitHistogram(m *map[string]*Histogram, key string, buckets []float64) *Histogram {
	reg.mu.RLock()
	h, ok := (*m)[key]
	reg.mu.RUnlock()
	if ok {
		return h
	}

	reg.mu.Lock()
	defer reg.mu.Unlock()
	if h, ok = (*m)[key]; ok {
		return h
	}
	h = newHistogram(buckets)
	(*m)[key] = h
	return h
}

// osThreads reports the current OS thread count from the runtime's
// thread-create profile (no allocation beyond the count).
func osThreads() int {
	return pprof.Lookup("threadcreate").Count()
}

// countOpenFDs counts the process's open file descriptors. On Linux it
// reads /proc/self/fd; elsewhere it reports 0 (gauge absent in practice
// on non-Linux dev boxes, which is acceptable for soak monitoring).
func countOpenFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(entries)
}

var _ = strconv.Itoa // keep strconv if future edits use it
