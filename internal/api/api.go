// Package api implements OneGate's management API (/api/*): the control
// plane consumed by the embedded dashboard (Phase 6).
//
// The OpenAPI contract (docs/api/openapi.yaml) is the single source of
// truth: the route table is generated from it (spec_gen.go, via
// scripts/gen_api_routes.py), handlers are looked up by OperationID, and
// the conformance tests assert parity between spec, registrations, and
// runtime behavior.
//
// Layering: the management plane is a sibling of internal/proxy/ingest,
// registered on the same mux by the composition root. It may import
// storage, auth, observability, routing, and the upstream client (for
// SSRF-guarded provider probes) — but never internal/server or
// internal/config (callers resolve plain values, keeping the same seam
// discipline as the proxy).
//
// Security model:
//   - Every route carries an auth class (admin-session | admin-token |
//     public). This node implements admin-token verification against
//     ONEGATE_ADMIN_TOKEN; session auth (cookies + CSRF) lands in
//     p6.auth-sessions by wrapping the same Authenticator chain.
//   - Every route carries a rate-limit class (auth/read/write/stream/
//     probe), enforced per principal by a fixed-window limiter.
//   - Secrets never leave: provider API keys are write-only (encrypted at
//     rest, masked_key on reads); raw virtual keys appear exactly once,
//     in the mint response.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// routeSpec is one compiled route of the OpenAPI contract. Generated —
// see spec_gen.go.
type routeSpec struct {
	Method      string
	Path        string
	OperationID string
	Auth        string // admin-session | admin-token | public
	RateLimit   string // auth | read | write | stream | probe
}

// HealthView exposes routing health for the dashboard. Implemented by the
// composition root over routing.HealthTracker; kept as an interface so
// tests and future sources can substitute it.
type HealthView interface {
	// State reports the circuit state for one target.
	State(providerID, model string) routing.CircuitState
	// Events returns the most recent transition events (newest last).
	Events(limit int) []routing.HealthEvent
}

// SystemConfig is the read-only effective configuration (settings view).
// The composition root adapts internal/config into this DTO so the API
// package never imports config (same seam rule as internal/server).
type SystemConfig struct {
	Host     string       `json:"host"`
	Port     int          `json:"port"`
	DataDir  string       `json:"data_dir"`
	LogLevel string       `json:"log_level"`
	HTTP     HTTPTimeouts `json:"http"`
	Reload   ReloadInfo   `json:"reload"`
}

// HTTPTimeouts mirrors config.HTTPConfig (ms units; 0 = disabled).
type HTTPTimeouts struct {
	ReadHeaderTimeoutMS int `json:"read_header_timeout_ms"`
	ReadTimeoutMS       int `json:"read_timeout_ms"`
	WriteTimeoutMS      int `json:"write_timeout_ms"`
	IdleTimeoutMS       int `json:"idle_timeout_ms"`
}

// ReloadInfo mirrors config.ReloadConfig.
type ReloadInfo struct {
	Enabled bool `json:"enabled"`
	PollMS  int  `json:"poll_ms"`
}

// Options wires the management API. Everything is injectable; the
// composition root (cmd/onegate) is the only caller that constructs real
// implementations.
type Options struct {
	// Logger receives API lifecycle logs. Nil selects a discarding logger.
	Logger *slog.Logger

	// Store is the SQLite store backing all repositories.
	Store *storage.Store

	// Keys mints and manages virtual keys (raw key shown once).
	Keys *auth.Manager

	// ProviderCipher encrypts/decrypts provider API keys at rest.
	ProviderCipher *auth.Cipher

	// AdminToken enables admin-token auth (ONEGATE_ADMIN_TOKEN). Empty
	// disables token auth entirely — admin routes then require the
	// session authenticator (p6.auth-sessions).
	AdminToken string

	// LogHub backs /api/logs/live (SSE) and /api/logs/recent.
	LogHub *observability.LogHub

	// Health exposes routing circuit states for /api/routing/health.
	Health HealthView

	// Models lists canonical models (drives the health target list).
	Models func() ([]domain.Model, error)

	// Config resolves the effective runtime configuration snapshot.
	Config func() SystemConfig

	// StartedMS is the process start time (uptime in /api/system/status).
	StartedMS int64

	// SchemaVersion is reported by /api/system/status.
	SchemaVersion int

	// Prober performs SSRF-guarded outbound probes (test connection).
	// Nil disables the endpoint (503 instead of an unsafe direct call).
	Prober *client.Client

	// NowMS overrides the wall clock for deterministic tests.
	NowMS func() int64
}

// timeNowMS is the wall clock (swappable in tests via Options.NowMS).
func timeNowMS() int64 { return time.Now().UnixMilli() }

// API is the registered management plane. It owns the mux subtree /api/*
// (plus the legacy-free SSE feed) and the middleware chain: rate limit ->
// authenticate -> dispatch.
type API struct {
	opts     Options
	logger   *slog.Logger
	auth     Authenticator
	limits   *limiter
	handlers map[string]http.HandlerFunc // keyed by OperationID
}

// Register builds the API and registers every spec route on mux. It
// panics only on programmer error (a spec route without a handler), which
// fails tests before any deploy.
func Register(mux *http.ServeMux, opts Options) *API {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.NowMS == nil {
		opts.NowMS = func() int64 { return timeNowMS() }
	}
	a := &API{
		opts:     opts,
		logger:   opts.Logger,
		auth:     NewTokenAuthenticator(opts.AdminToken),
		limits:   newLimiter(opts.NowMS),
		handlers: map[string]http.HandlerFunc{},
	}
	a.bind()

	seen := map[string]bool{}
	for _, rt := range specRoutes {
		h, ok := a.handlers[rt.OperationID]
		if !ok {
			panic(fmt.Sprintf("api: no handler for spec operation %s (%s %s)", rt.OperationID, rt.Method, rt.Path))
		}
		mux.Handle(rt.Method+" "+rt.Path, a.wrap(rt, h))
		seen[rt.OperationID] = true
	}
	// Handler coverage must be exact: an implemented operation that left
	// the spec (or vice versa) is a contract violation.
	for op := range a.handlers {
		if !seen[op] {
			panic(fmt.Sprintf("api: handler %s is not in the spec route table", op))
		}
	}
	return a
}

// wrap applies the per-route middleware chain: rate limit -> auth ->
// handler. The SSE route additionally tracks concurrent streams.
func (a *API) wrap(rt routeSpec, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := "anonymous"
		if rt.Auth != "public" {
			p, ok := a.auth.Authenticate(r)
			if !ok {
				writeError(w, domain.GatewayError{
					Status:  http.StatusUnauthorized,
					Type:    domain.ErrAuthentication,
					Code:    "unauthenticated",
					Message: "authentication required",
				})
				return
			}
			principal = p
		}
		if !a.limits.allow(rt.RateLimit, principal) {
			writeError(w, domain.GatewayError{
				Status:    http.StatusTooManyRequests,
				Type:      domain.ErrRateLimit,
				Code:      "rate_limit_exceeded",
				Message:   "too many requests",
				Retryable: true,
			})
			return
		}
		h(w, r)
	}
}

// bind registers every handler by OperationID.
func (a *API) bind() {
	a.handlers["getSetupStatus"] = a.handleSetupStatus
	a.handlers["login"] = a.handleNotYetSessions // p6.auth-sessions
	a.handlers["createAdmin"] = a.handleNotYetSessions
	a.handlers["logout"] = a.handleNotYetSessions
	a.handlers["getSession"] = a.handleNotYetSessions

	a.handlers["listProviders"] = a.handleListProviders
	a.handlers["createProvider"] = a.handleCreateProvider
	a.handlers["getProvider"] = a.handleGetProvider
	a.handlers["updateProvider"] = a.handleUpdateProvider
	a.handlers["deleteProvider"] = a.handleDeleteProvider
	a.handlers["testProvider"] = a.handleTestProvider

	a.handlers["listModels"] = a.handleListModels
	a.handlers["createModel"] = a.handleCreateModel
	a.handlers["getModel"] = a.handleGetModel
	a.handlers["replaceModel"] = a.handleReplaceModel
	a.handlers["deleteModel"] = a.handleDeleteModel

	a.handlers["listRoutingRules"] = a.handleListRoutingRules
	a.handlers["createRoutingRule"] = a.handleCreateRoutingRule
	a.handlers["replaceRoutingRule"] = a.handleReplaceRoutingRule
	a.handlers["deleteRoutingRule"] = a.handleDeleteRoutingRule
	a.handlers["getRoutingHealth"] = a.handleRoutingHealth

	a.handlers["listKeys"] = a.handleListKeys
	a.handlers["createKey"] = a.handleCreateKey
	a.handlers["getKey"] = a.handleGetKey
	a.handlers["updateKey"] = a.handleUpdateKey
	a.handlers["deleteKey"] = a.handleDeleteKey
	a.handlers["revokeKey"] = a.handleRevokeKey
	a.handlers["getKeyUsage"] = a.handleGetKeyUsage

	a.handlers["queryUsageRange"] = a.handleUsageRange
	a.handlers["queryUsageSummary"] = a.handleUsageSummary
	a.handlers["listRequests"] = a.handleListRequests

	a.handlers["recentLogs"] = a.handleRecentLogs
	a.handlers["streamLiveLogs"] = a.handleLiveLogs

	a.handlers["getSystemStatus"] = a.handleSystemStatus
	a.handlers["getEffectiveConfig"] = a.handleSystemConfig
}

// handleNotYetSessions stands in for the four session operations while
// p6.auth-sessions is pending: the contract is reserved in the spec, the
// routes answer 503 so clients can feature-detect.
func (a *API) handleNotYetSessions(w http.ResponseWriter, _ *http.Request) {
	writeError(w, domain.GatewayError{
		Status:    http.StatusServiceUnavailable,
		Type:      domain.ErrAPI,
		Code:      "not_implemented",
		Message:   "dashboard session auth arrives in p6.auth-sessions",
		Retryable: true,
	})
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

// maxBodyBytes caps management payloads. The dashboard never sends
// anything close to this; provider configs and key metadata are tiny.
const maxBodyBytes = 1 << 20 // 1 MiB

// decodeJSON reads a JSON body into dst with size capping and a friendly
// 400 on malformed input.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeError(w, domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Code:    "read_body_failed",
			Message: "could not read request body",
		})
		return false
	}
	if len(body) > maxBodyBytes {
		writeError(w, domain.GatewayError{
			Status:  http.StatusRequestEntityTooLarge,
			Type:    domain.ErrTooLarge,
			Code:    "body_too_large",
			Message: "request body exceeds 1 MiB",
		})
		return false
	}
	if len(body) == 0 {
		writeError(w, domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Code:    "empty_body",
			Message: "request body required",
		})
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		writeError(w, domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Code:    "invalid_json",
			Message: "request body is not valid JSON",
		})
		return false
	}
	return true
}

// pathID extracts the {id} path segment registered via ServeMux patterns.
func pathID(r *http.Request) string { return r.PathValue("id") }

// queryLimit parses the limit query parameter (1-200, default 50).
func queryLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 50, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		writeError(w, domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Code:    "invalid_parameter",
			Message: "limit must be between 1 and 200",
			Param:   "limit",
		})
		return 0, false
	}
	return n, true
}

// queryInt64 parses an optional int64 query parameter.
func queryInt64(r *http.Request, name string) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// errNotFound renders the canonical 404 for a resource kind.
func errNotFound(kind string) domain.GatewayError {
	return domain.GatewayError{
		Status:  http.StatusNotFound,
		Type:    domain.ErrNotFound,
		Code:    "resource_not_found",
		Message: kind + " not found",
	}
}

// errInvalid renders a 400 with an offending parameter.
func errInvalid(message, param string) domain.GatewayError {
	return domain.GatewayError{
		Status:  http.StatusBadRequest,
		Type:    domain.ErrInvalidRequest,
		Code:    "invalid_parameter",
		Message: message,
		Param:   param,
	}
}

// mapStorageError converts repository errors onto the envelope. The
// storage detail is logged server-side (it may contain SQL), never sent
// to the client.
func (a *API) mapStorageError(w http.ResponseWriter, err error, kind string) {
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, errNotFound(kind))
		return
	}
	a.logger.Error("management api storage error", "error", err.Error(), "kind", kind)
	writeError(w, domain.GatewayError{
		Status:  http.StatusInternalServerError,
		Type:    domain.ErrInternal,
		Code:    "storage_error",
		Message: "storage operation failed",
	})
}

// pageMeta is the shared pagination envelope.
type pageMeta struct {
	Items      any    `json:"items"`
	NextCursor string `json:"next_cursor"`
}
