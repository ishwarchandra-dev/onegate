package api

import (
	"net/http"
	"strconv"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

// logsRecentResponse is GET /api/logs/recent's body.
type logsRecentResponse struct {
	Entries []observability.LogEntry `json:"entries"`
}

// parseLogFilter extracts the shared log filter surface (min_level,
// trace_id, provider) with spec-compliant validation.
func parseLogFilter(w http.ResponseWriter, r *http.Request) (observability.LogFilter, bool) {
	q := r.URL.Query()
	f := observability.LogFilter{
		MinLevel: q.Get("min_level"),
		TraceID:  q.Get("trace_id"),
		Provider: q.Get("provider"),
	}
	switch f.MinLevel {
	case "", "debug", "info", "warn", "error":
		return f, true
	default:
		writeError(w, errInvalid("min_level must be one of debug, info, warn, error", "min_level"))
		return f, false
	}
}

// handleRecentLogs GET /api/logs/recent — ring buffer snapshot.
func (a *API) handleRecentLogs(w http.ResponseWriter, r *http.Request) {
	if a.opts.LogHub == nil {
		writeError(w, errNotFound("log hub"))
		return
	}
	filter, ok := parseLogFilter(w, r)
	if !ok {
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, errInvalid("limit must be between 1 and 1000", "limit"))
			return
		}
		limit = n
	}
	entries := a.opts.LogHub.Ring().Recent(limit, filter)
	if entries == nil {
		entries = []observability.LogEntry{}
	}
	writeJSON(w, http.StatusOK, logsRecentResponse{Entries: entries})
}

// handleLiveLogs GET /api/logs/live — SSE passthrough to the LogHub.
// Auth and rate limiting already ran in wrap(); the hub owns framing,
// backpressure (drop-and-count), and history replay.
func (a *API) handleLiveLogs(w http.ResponseWriter, r *http.Request) {
	if a.opts.LogHub == nil {
		writeError(w, errNotFound("log hub"))
		return
	}
	a.opts.LogHub.ServeHTTP(w, r)
}
