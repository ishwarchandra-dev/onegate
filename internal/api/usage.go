package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// usageRangeResponse is GET /api/usage/range's body.
type usageRangeResponse struct {
	Buckets []domain.Usage `json:"buckets"`
}

// parseWindowAndFilters extracts the shared query surface of the usage
// endpoints (start_ms, end_ms, vkey_id, model_id, provider_id).
func parseWindowAndFilters(w http.ResponseWriter, r *http.Request) (storage.RollupQueryParams, bool) {
	p := storage.RollupQueryParams{
		VirtualKeyID: r.URL.Query().Get("vkey_id"),
		ModelID:      r.URL.Query().Get("model_id"),
		ProviderID:   r.URL.Query().Get("provider_id"),
	}
	var ok bool
	if p.StartMS, ok = queryInt64(r, "start_ms"); !ok {
		writeError(w, errInvalid("start_ms must be a non-negative integer", "start_ms"))
		return p, false
	}
	if p.EndMS, ok = queryInt64(r, "end_ms"); !ok {
		writeError(w, errInvalid("end_ms must be a non-negative integer", "end_ms"))
		return p, false
	}
	if p.StartMS > 0 && p.EndMS > 0 && p.StartMS >= p.EndMS {
		writeError(w, errInvalid("start_ms must be before end_ms", "start_ms"))
		return p, false
	}
	return p, true
}

// handleUsageRange GET /api/usage/range
func (a *API) handleUsageRange(w http.ResponseWriter, r *http.Request) {
	params, ok := parseWindowAndFilters(w, r)
	if !ok {
		return
	}
	switch r.URL.Query().Get("step") {
	case "", "hour":
		params.StepMS = storage.HourBucketMS
	case "day":
		params.StepMS = storage.DayBucketMS
	default:
		writeError(w, errInvalid("step must be hour or day", "step"))
		return
	}
	buckets, err := a.opts.Store.Rollups().QueryRange(r.Context(), params)
	if err != nil {
		a.mapStorageError(w, err, "usage")
		return
	}
	if buckets == nil {
		buckets = []domain.Usage{}
	}
	writeJSON(w, http.StatusOK, usageRangeResponse{Buckets: buckets})
}

// handleUsageSummary GET /api/usage/summary
func (a *API) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	params, ok := parseWindowAndFilters(w, r)
	if !ok {
		return
	}
	u, err := a.opts.Store.Rollups().QuerySummary(r.Context(), params)
	if err != nil {
		a.mapStorageError(w, err, "usage")
		return
	}
	writeJSON(w, http.StatusOK, usageTotalsFrom(u))
}

// handleListRequests GET /api/requests — cursor pagination is native in
// the request repository (ListByTime), including the vkey filter.
func (a *API) handleListRequests(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	// Storage treats undecodable cursors as "start from newest" (lenient
	// by design); the API contract requires a 400 instead, so the shape
	// ("<created_ms>:<id>") is validated up front.
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !validStorageCursor(cursor) {
		writeError(w, errInvalid("cursor is malformed", "cursor"))
		return
	}
	page, err := a.opts.Store.Requests().ListByTime(
		r.URL.Query().Get("vkey_id"), limit, cursor)
	if err != nil {
		writeError(w, errInvalid("cursor is malformed", "cursor"))
		return
	}
	items := page.Items
	if items == nil {
		items = []domain.RequestRecord{}
	}
	writePage(w, items, page.NextCur)
}

// validStorageCursor checks the repository cursor shape: int64 ":" id.
func validStorageCursor(s string) bool {
	ms, id, found := strings.Cut(s, ":")
	if !found || id == "" {
		return false
	}
	if _, err := strconv.ParseInt(ms, 10, 64); err != nil {
		return false
	}
	return true
}
