package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// ruleInput is the POST/PUT /api/routing-rules body.
type ruleInput struct {
	ID       string `json:"id"`
	ModelID  string `json:"model_id"`
	Policy   string `json:"policy"`
	Enabled  *bool  `json:"enabled"`
	Position int    `json:"position"`
}

// legalPolicies mirrors the spec enum (domain.FallbackPolicy values).
var legalPolicies = map[string]bool{
	"ordered":  true,
	"weighted": true,
	"cost":     true,
	"latency":  true,
}

func (a *API) validateRuleInput(body ruleInput) *domain.GatewayError {
	if body.ModelID == "" {
		e := errInvalid("model_id is required", "model_id")
		return &e
	}
	if !legalPolicies[body.Policy] {
		e := errInvalid("policy must be one of ordered, weighted, cost, latency", "policy")
		return &e
	}
	if _, err := a.opts.Store.Models().Get(body.ModelID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			e := errInvalid("model_id references an unknown model", "model_id")
			return &e
		}
		e := errInvalid("model lookup failed", "model_id")
		return &e
	}
	return nil
}

func (b ruleInput) toRule() domain.RoutingRule {
	return domain.RoutingRule{
		ID:       b.ID,
		ModelID:  b.ModelID,
		Policy:   domain.FallbackPolicy(b.Policy),
		Enabled:  b.Enabled == nil || *b.Enabled,
		Position: b.Position,
	}
}

// handleListRoutingRules GET /api/routing-rules
func (a *API) handleListRoutingRules(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	rules, err := a.opts.Store.RoutingRules().List()
	if err != nil {
		a.mapStorageError(w, err, "routing rules")
		return
	}
	sortByID(rules, func(rule domain.RoutingRule) string { return rule.ID })
	page, next, ge := paginateByID(rules, limit, r.URL.Query().Get("cursor"),
		func(rule domain.RoutingRule) string { return rule.ID })
	if ge != nil {
		writeError(w, *ge)
		return
	}
	if page == nil {
		page = []domain.RoutingRule{}
	}
	writePage(w, page, next)
}

// handleCreateRoutingRule POST /api/routing-rules
func (a *API) handleCreateRoutingRule(w http.ResponseWriter, r *http.Request) {
	var body ruleInput
	if !decodeJSON(w, r, &body) {
		return
	}
	if verr := a.validateRuleInput(body); verr != nil {
		writeError(w, *verr)
		return
	}
	if body.ID != "" {
		if existing, err := a.opts.Store.RoutingRules().Get(body.ID); err == nil {
			writeJSON(w, http.StatusOK, existing)
			return
		} else if !errors.Is(err, storage.ErrNotFound) {
			a.mapStorageError(w, err, "routing rule")
			return
		}
	}
	rule := body.toRule()
	if err := a.opts.Store.RoutingRules().Upsert(&rule); err != nil {
		a.mapStorageError(w, err, "routing rule")
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

// handleReplaceRoutingRule PUT /api/routing-rules/{id}
func (a *API) handleReplaceRoutingRule(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body ruleInput
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
	if verr := a.validateRuleInput(body); verr != nil {
		writeError(w, *verr)
		return
	}
	if _, err := a.opts.Store.RoutingRules().Get(id); err != nil {
		a.mapStorageError(w, err, "routing rule")
		return
	}
	rule := body.toRule()
	if err := a.opts.Store.RoutingRules().Upsert(&rule); err != nil {
		a.mapStorageError(w, err, "routing rule")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// handleDeleteRoutingRule DELETE /api/routing-rules/{id}
func (a *API) handleDeleteRoutingRule(w http.ResponseWriter, r *http.Request) {
	if err := a.opts.Store.RoutingRules().Delete(pathID(r)); err != nil {
		a.mapStorageError(w, err, "routing rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// targetHealthDTO is one target's circuit state for the health view.
type targetHealthDTO struct {
	ProviderID          string `json:"provider_id"`
	Model               string `json:"model"`
	State               string `json:"state"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
	CooldownUntilMS     int64  `json:"cooldown_until_ms,omitempty"`
}

// healthResponse is GET /api/routing/health's body.
type healthResponse struct {
	Targets []targetHealthDTO     `json:"targets"`
	Events  []routing.HealthEvent `json:"events"`
}

// handleRoutingHealth GET /api/routing/health
//
// States are reported for every (provider, provider_model) pair reachable
// from the model registry; pairs without recorded failures read as
// closed. Events come from the tracker's ring buffer (newest last).
func (a *API) handleRoutingHealth(w http.ResponseWriter, r *http.Request) {
	eventLimit := 50
	if raw := r.URL.Query().Get("event_limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, errInvalid("event_limit must be between 1 and 200", "event_limit"))
			return
		}
		eventLimit = n
	}

	resp := healthResponse{Targets: []targetHealthDTO{}, Events: []routing.HealthEvent{}}

	if a.opts.Health != nil {
		models, err := a.listModels()
		if err != nil {
			a.mapStorageError(w, err, "models")
			return
		}
		seen := map[string]bool{}
		for _, m := range models {
			for _, t := range m.Targets {
				key := t.ProviderID + "|" + t.ProviderModel
				if seen[key] {
					continue
				}
				seen[key] = true
				state := a.opts.Health.State(t.ProviderID, t.ProviderModel)
				resp.Targets = append(resp.Targets, targetHealthDTO{
					ProviderID: t.ProviderID,
					Model:      t.ProviderModel,
					State:      string(state),
				})
			}
		}
		resp.Events = a.opts.Health.Events(eventLimit)
		if resp.Events == nil {
			resp.Events = []routing.HealthEvent{}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// listModels resolves models via the injected model source (or the
// store's own repo when no override is present).
func (a *API) listModels() ([]domain.Model, error) {
	if a.opts.Models != nil {
		return a.opts.Models()
	}
	return a.opts.Store.Models().List()
}
