package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// modelInput is the POST/PUT /api/models body.
type modelInput struct {
	ID           string                    `json:"id"`
	Aliases      []string                  `json:"aliases"`
	Targets      []domain.ModelTarget      `json:"targets"`
	Capabilities *domain.ModelCapabilities `json:"capabilities"`
}

// validateModelInput checks the create/replace invariants.
func (a *API) validateModelInput(body modelInput, requireID bool) *domain.GatewayError {
	if requireID && strings.TrimSpace(body.ID) == "" {
		e := errInvalid("id is required", "id")
		return &e
	}
	if len(body.Targets) == 0 {
		e := errInvalid("at least one target is required", "targets")
		return &e
	}
	// Every target must reference an existing provider (server-side
	// validation; the dashboard mirrors it client-side).
	for i, t := range body.Targets {
		if t.ProviderID == "" {
			e := errInvalid("target.provider_id is required", "targets")
			return &e
		}
		if strings.TrimSpace(t.ProviderModel) == "" {
			e := errInvalid("target.provider_model is required", "targets")
			return &e
		}
		if _, err := a.opts.Store.Providers().Get(t.ProviderID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				e := errInvalid("target references unknown provider "+t.ProviderID, "targets")
				return &e
			}
			e := errInvalid("provider lookup failed", "targets")
			return &e
		}
		if t.Weight < 0 {
			e := errInvalid("target.weight must be >= 0", "targets")
			return &e
		}
		if t.CostMultiplier < 0 || t.CostMultiplier > 10000 {
			e := errInvalid("target.cost_multiplier must be between 0 and 10000", "targets")
			return &e
		}
		_ = i
	}
	return nil
}

// handleListModels GET /api/models
func (a *API) handleListModels(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	models, err := a.opts.Store.Models().List()
	if err != nil {
		a.mapStorageError(w, err, "models")
		return
	}
	sortByID(models, func(m domain.Model) string { return m.ID })
	page, next, ge := paginateByID(models, limit, r.URL.Query().Get("cursor"),
		func(m domain.Model) string { return m.ID })
	if ge != nil {
		writeError(w, *ge)
		return
	}
	if page == nil {
		page = []domain.Model{}
	}
	writePage(w, page, next)
}

// handleCreateModel POST /api/models (idempotent when id supplied)
func (a *API) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var body modelInput
	if !decodeJSON(w, r, &body) {
		return
	}
	if verr := a.validateModelInput(body, false); verr != nil {
		writeError(w, *verr)
		return
	}
	if body.ID == "" {
		writeError(w, errInvalid("id is required", "id"))
		return
	}
	// Idempotent replay: existing id returns the stored model (200).
	if existing, err := a.opts.Store.Models().Get(body.ID); err == nil {
		writeJSON(w, http.StatusOK, existing)
		return
	} else if !errors.Is(err, storage.ErrNotFound) {
		a.mapStorageError(w, err, "model")
		return
	}
	m := body.toModel()
	if err := a.opts.Store.Models().Upsert(m); err != nil {
		a.mapStorageError(w, err, "model")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// handleGetModel GET /api/models/{id}
func (a *API) handleGetModel(w http.ResponseWriter, r *http.Request) {
	m, err := a.opts.Store.Models().Get(pathID(r))
	if err != nil {
		a.mapStorageError(w, err, "model")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleReplaceModel PUT /api/models/{id} (full replace, idempotent)
func (a *API) handleReplaceModel(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body modelInput
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.ID == "" {
		body.ID = id
	}
	if body.ID != id {
		writeError(w, errInvalid("body id must match the path", "id"))
		return
	}
	if verr := a.validateModelInput(body, true); verr != nil {
		writeError(w, *verr)
		return
	}
	if _, err := a.opts.Store.Models().Get(id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			a.mapStorageError(w, err, "model")
			return
		}
		a.mapStorageError(w, err, "model")
		return
	}
	m := body.toModel()
	if err := a.opts.Store.Models().Upsert(m); err != nil {
		a.mapStorageError(w, err, "model")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleDeleteModel DELETE /api/models/{id}
func (a *API) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if err := a.opts.Store.Models().Delete(pathID(r)); err != nil {
		a.mapStorageError(w, err, "model")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toModel normalizes the input into the stored shape (weight and cost
// defaults mirror the repository's own normalization).
func (b modelInput) toModel() domain.Model {
	m := domain.Model{
		ID:      strings.TrimSpace(b.ID),
		Aliases: b.Aliases,
		Targets: b.Targets,
	}
	if b.Capabilities != nil {
		m.Capabilities = *b.Capabilities
	}
	for i := range m.Targets {
		if m.Targets[i].Position == 0 {
			m.Targets[i].Position = i
		}
		if m.Targets[i].Weight <= 0 {
			m.Targets[i].Weight = 1
		}
		if m.Targets[i].CostMultiplier == 0 {
			m.Targets[i].CostMultiplier = 100
		}
	}
	return m
}
