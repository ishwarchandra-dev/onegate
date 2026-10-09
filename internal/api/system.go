package api

import (
	"net/http"

	"github.com/ishwarchandra-dev/onegate/internal/storage"
	"github.com/ishwarchandra-dev/onegate/internal/version"
)

// systemStatus is GET /api/system/status's body.
type systemStatus struct {
	Version       string         `json:"version"`
	GitCommit     string         `json:"git_commit,omitempty"`
	SchemaVersion int            `json:"schema_version"`
	UptimeMS      int64          `json:"uptime_ms"`
	Counts        statusCounts   `json:"counts"`
	Storage       *statusStorage `json:"storage,omitempty"`
}

type statusCounts struct {
	Providers int   `json:"providers"`
	Models    int   `json:"models"`
	Keys      int   `json:"keys"`
	Requests  int64 `json:"requests"`
}

type statusStorage struct {
	DBSizeBytes int64 `json:"db_size_bytes,omitempty"`
}

// handleSetupStatus GET /api/auth/setup-status
//
// While p6.auth-sessions is pending, first-run setup is not yet
// available; the endpoint truthfully reports that setup is required
// through the session node's own store once it lands. For now: setup is
// reported required (the dashboard will route to setup when that flow
// ships; token-authenticated installs bypass the wizard).
func (a *API) handleSetupStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		SetupRequired bool `json:"setup_required"`
	}{SetupRequired: true})
}

// handleSystemStatus GET /api/system/status
func (a *API) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	st := systemStatus{
		Version:       version.Version,
		GitCommit:     version.GitCommit,
		SchemaVersion: a.opts.SchemaVersion,
		UptimeMS:      a.opts.NowMS() - a.opts.StartedMS,
	}
	if st.UptimeMS < 0 {
		st.UptimeMS = 0
	}

	if providers, err := a.opts.Store.Providers().List(); err == nil {
		st.Counts.Providers = len(providers)
	}
	if models, err := a.opts.Store.Models().List(); err == nil {
		st.Counts.Models = len(models)
	}
	if keys, err := a.opts.Store.VirtualKeys().List(); err == nil {
		st.Counts.Keys = len(keys)
	}
	// Total requests comes from the rollup summary (all time).
	if u, err := a.opts.Store.Rollups().QuerySummary(r.Context(), storage.RollupQueryParams{}); err == nil {
		st.Counts.Requests = u.Requests
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSystemConfig GET /api/system/config — read-only effective config.
func (a *API) handleSystemConfig(w http.ResponseWriter, _ *http.Request) {
	if a.opts.Config == nil {
		writeError(w, errNotFound("config snapshot"))
		return
	}
	writeJSON(w, http.StatusOK, a.opts.Config())
}
