// Package server owns OneGate's public HTTP surface: the router, the
// middleware chain, and the listener timeout policy.
//
// Layering (docs/adr/005-http-server.md):
//   - This package depends only on the standard library, internal/domain
//     (error envelopes) and internal/observability (trace context). It
//     never imports internal/config — the caller resolves config into
//     Options, so the server stays testable and the layering audit clean.
//   - Proxy endpoints are registered by later nodes (p3.ingest-endpoints)
//     onto the exposed Mux; this package owns the chain, not the routes.
//
// Middleware order is fixed and tested (middleware_test.go):
//
//	RequestID  ->  AccessLog  ->  Recover  ->  handler
//
// RequestID is outermost so every downstream log line (including panic
// logs) carries the trace ID. AccessLog sits outside Recover so a
// recovered panic still yields exactly one access-log line with the
// rendered status. Recover is the innermost wrapper: it is the last
// line of defense before application code.
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/version"
)

// Timeouts is the listener timeout policy, resolved from config by the
// caller. Zero durations disable the corresponding timeout (the default
// for read/write so streaming responses are never cut mid-flight).
// ReadHeader must be > 0 — it is the slowloris guard; config validation
// enforces that before the server is constructed.
type Timeouts struct {
	ReadHeader time.Duration
	Read       time.Duration
	Write      time.Duration
	Idle       time.Duration
}

// Options configures a Router. Logger is required; a nil logger is
// replaced by a discarding one so tests can opt out of output.
type Options struct {
	Logger *slog.Logger

	// Timeouts is the listener policy applied by Server().
	Timeouts Timeouts

	// Version is reported by /healthz (defaults to version.Version).
	Version string

	// SchemaVersion is the storage schema reported by /healthz.
	SchemaVersion int

	// Metrics receives middleware observations. Nil selects the default
	// minimal registry, which also serves /metrics. Phase 5 injects the
	// full Prometheus registry here (p5.metrics).
	Metrics Metrics
}

// Router is OneGate's HTTP front door. Routes are registered on Mux
// (including the proxy endpoints added by p3.ingest-endpoints); Handler
// returns the mux wrapped in the middleware chain; Server materializes
// an *http.Server with the configured timeout policy.
type Router struct {
	opts    Options
	mux     *http.ServeMux
	handler http.Handler
	metrics Metrics
}

// New builds the router, registers the system routes (/healthz, /metrics,
// and the landing page), and wraps the mux in the middleware chain.
func New(opts Options) *Router {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Version == "" {
		opts.Version = version.Version
	}
	mx := Metrics(nil)
	if opts.Metrics != nil {
		mx = opts.Metrics
	} else {
		mx = NewMiniRegistry()
	}

	r := &Router{
		opts:    opts,
		mux:     http.NewServeMux(),
		handler: nil,
		metrics: mx,
	}

	// System routes. Proxy endpoints land in p3.ingest-endpoints via
	// r.Mux().Handle("POST /v1/chat/completions", ...).
	r.mux.HandleFunc("GET /healthz", r.handleHealth)
	r.mux.Handle("GET /metrics", mx.Handler())
	r.mux.HandleFunc("GET /{$}", r.handleLanding)

	r.handler = r.chain(r.mux)
	return r
}

// Mux exposes the route table for endpoint registration (p3.ingest).
// Patterns follow Go 1.22+ ServeMux syntax ("POST /v1/messages").
func (r *Router) Mux() *http.ServeMux { return r.mux }

// Handler returns the full middleware chain over the route table.
func (r *Router) Handler() http.Handler { return r.handler }

// Server builds an *http.Server on addr with the configured timeout
// policy. Graceful shutdown stays with the caller (cmd/onegate).
func (r *Router) Server(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           r.handler,
		ReadHeaderTimeout: r.opts.Timeouts.ReadHeader,
		ReadTimeout:       r.opts.Timeouts.Read,
		WriteTimeout:      r.opts.Timeouts.Write,
		IdleTimeout:       r.opts.Timeouts.Idle,
	}
}

// handleHealth reports liveness plus build identity. OmniRoute parity:
// same shape as the Phase 0 endpoint this replaces.
func (r *Router) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"version":        r.opts.Version,
		"schema_version": r.opts.SchemaVersion,
	})
}

// handleLanding is the human-facing root; the dashboard (Phase 6)
// replaces it.
func (r *Router) handleLanding(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "OneGate %s — proxy core online\n", r.opts.Version)
}

// writeJSON renders a JSON body with status. Errors from the encoder are
// unrecoverable (the client is gone); nothing to do but give up.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError renders a domain.GatewayError under the neutral
// {"error": {...}} envelope. Protocol adapters render protocol-specific
// shapes at the edge (p3.ingest-endpoints); this is the shape for
// gateway-internal failures before a protocol is known.
func writeError(w http.ResponseWriter, ge domain.GatewayError) {
	writeJSON(w, ge.Status, struct {
		Error domain.GatewayError `json:"error"`
	}{Error: ge})
}
