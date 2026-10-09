package api

import (
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/auth"
	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// keyCreate is the POST /api/keys body.
type keyCreate struct {
	Name        string            `json:"name"`
	Scopes      *domain.KeyScopes `json:"scopes"`
	Limits      *domain.KeyLimits `json:"limits"`
	ExpiresAtMS int64             `json:"expires_at_ms"`
}

// keyUpdate is the PATCH /api/keys/{id} body. Nil fields keep values.
type keyUpdate struct {
	Name        *string           `json:"name"`
	Scopes      *domain.KeyScopes `json:"scopes"`
	Limits      *domain.KeyLimits `json:"limits"`
	ExpiresAtMS *int64            `json:"expires_at_ms"`
}

// handleListKeys GET /api/keys — metadata only, never raw material.
func (a *API) handleListKeys(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	keys, err := a.opts.Keys.ListKeys(r.Context())
	if err != nil {
		a.mapStorageError(w, err, "keys")
		return
	}
	// Newest first (created_ms DESC, id DESC) — keyset composite cursor.
	page, next, ge := paginateByTimeID(keys, limit, r.URL.Query().Get("cursor"),
		func(k domain.VirtualKey) (int64, string) { return k.CreatedMS, k.ID })
	if ge != nil {
		writeError(w, *ge)
		return
	}
	if page == nil {
		page = []domain.VirtualKey{}
	}
	writePage(w, page, next)
}

// handleCreateKey POST /api/keys — the ONLY response that ever carries
// the raw key (show-once semantics).
func (a *API) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body keyCreate
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, errInvalid("name is required", "name"))
		return
	}
	params := auth.CreateKeyParams{
		Name:      name,
		ExpiresMS: body.ExpiresAtMS,
	}
	if body.Scopes != nil {
		params.Scopes = *body.Scopes
	}
	if body.Limits != nil {
		params.Limits = *body.Limits
	}
	raw, rec, err := a.opts.Keys.CreateKey(r.Context(), params)
	if err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Key    domain.VirtualKey `json:"key"`
		RawKey string            `json:"raw_key"`
	}{Key: rec, RawKey: raw})
}

// handleGetKey GET /api/keys/{id}
func (a *API) handleGetKey(w http.ResponseWriter, r *http.Request) {
	k, err := a.opts.Keys.GetKey(r.Context(), pathID(r))
	if err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	writeJSON(w, http.StatusOK, k)
}

// handleUpdateKey PATCH /api/keys/{id}
func (a *API) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body keyUpdate
	if !decodeJSON(w, r, &body) {
		return
	}
	k, err := a.opts.Keys.GetKey(r.Context(), id)
	if err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			writeError(w, errInvalid("name cannot be empty", "name"))
			return
		}
		k.Name = name
	}
	if body.Scopes != nil {
		k.Scopes = *body.Scopes
	}
	if body.Limits != nil {
		k.Limits = *body.Limits
	}
	if body.ExpiresAtMS != nil {
		k.ExpiresMS = *body.ExpiresAtMS
	}
	// Revoked keys stay revoked — PATCH cannot resurrect.
	if err := a.opts.Store.VirtualKeys().UpdateMeta(id, k.Name, k.ExpiresMS); err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	if body.Scopes != nil {
		if err := a.opts.Store.VirtualKeys().UpdateScopes(id, k.Scopes); err != nil {
			a.mapStorageError(w, err, "key")
			return
		}
	}
	if body.Limits != nil {
		if err := a.opts.Store.VirtualKeys().UpdateLimits(id, k.Limits); err != nil {
			a.mapStorageError(w, err, "key")
			return
		}
	}
	updated, err := a.opts.Keys.GetKey(r.Context(), id)
	if err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteKey DELETE /api/keys/{id}
func (a *API) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := a.opts.Keys.DeleteKey(r.Context(), pathID(r)); err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeKey POST /api/keys/{id}/revoke (idempotent)
func (a *API) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := a.opts.Keys.GetKey(r.Context(), id); err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	if err := a.opts.Keys.RevokeKey(r.Context(), id); err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	k, err := a.opts.Keys.GetKey(r.Context(), id)
	if err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	writeJSON(w, http.StatusOK, k)
}

// usageTotals is the summary shape (GET /api/keys/{id}/usage, /api/usage/summary).
type usageTotals struct {
	Requests         int64 `json:"requests"`
	Errors           int64 `json:"errors"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CostUSDMicros    int64 `json:"cost_usd_micros"`
}

// handleGetKeyUsage GET /api/keys/{id}/usage
func (a *API) handleGetKeyUsage(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := a.opts.Keys.GetKey(r.Context(), id); err != nil {
		a.mapStorageError(w, err, "key")
		return
	}
	startMS, ok := queryInt64(r, "start_ms")
	if !ok {
		writeError(w, errInvalid("start_ms must be a non-negative integer", "start_ms"))
		return
	}
	endMS, ok := queryInt64(r, "end_ms")
	if !ok {
		writeError(w, errInvalid("end_ms must be a non-negative integer", "end_ms"))
		return
	}
	u, err := a.opts.Store.Rollups().QuerySummary(r.Context(), storage.RollupQueryParams{
		VirtualKeyID: id,
		StartMS:      startMS,
		EndMS:        endMS,
	})
	if err != nil {
		a.mapStorageError(w, err, "usage")
		return
	}
	writeJSON(w, http.StatusOK, usageTotalsFrom(u))
}

// usageTotalsFrom converts the domain aggregate to the API shape.
func usageTotalsFrom(u domain.Usage) usageTotals {
	return usageTotals{
		Requests:         u.Requests,
		Errors:           u.Errors,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CostUSDMicros:    u.CostUSDMicros,
	}
}
