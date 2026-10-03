// Package routing implements the OneGate model registry, capability filtering,
// and pure target decision functions (ADR 004, Phase 4).
//
// The routing layer resolves incoming requests to ordered lists of candidate
// upstream provider targets. Design rules:
//   - Pure decision functions: (req, snapshot, health) -> targets without I/O.
//   - Thread-safe, lock-free routing: hot path reads atomic snapshot pointers.
//   - Capability-aware: filters targets by tools, vision, json mode, and stream.
//   - Zero storage imports: persistence uses dependency inversion (StorageSource).
package routing

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

var (
	// ErrModelNotFound indicates the requested model ID or alias does not exist.
	ErrModelNotFound = errors.New("routing: model not found")

	// ErrNoTargets indicates the model has no provider targets configured.
	ErrNoTargets = errors.New("routing: no targets configured for model")

	// ErrNoHealthyTargets indicates all candidate targets failed health checks.
	ErrNoHealthyTargets = errors.New("routing: no healthy targets available")

	// ErrCapabilityMismatch indicates no targets satisfy the capabilities required by the request.
	ErrCapabilityMismatch = errors.New("routing: no targets satisfy required capabilities")

	// ErrScopeModelDenied indicates the virtual key's scopes forbid access to this model.
	ErrScopeModelDenied = errors.New("routing: model not allowed by virtual key scope")

	// ErrScopeProviderDenied indicates all provider targets were filtered out by virtual key provider scopes.
	ErrScopeProviderDenied = errors.New("routing: all provider targets denied by virtual key scope")

	// ErrNilRegistry indicates a nil registry snapshot was provided to the decision function.
	ErrNilRegistry = errors.New("routing: nil registry snapshot")
)

// Target represents an evaluated, eligible candidate provider target for an attempt.
type Target struct {
	ProviderID     string
	ProviderModel  string
	Protocol       domain.ProviderProtocol
	BaseURL        string
	Weight         int
	Position       int
	CostMultiplier int
	Capabilities   domain.ModelCapabilities
}

// HealthView provides read-only inspection of target health for the decision function.
type HealthView interface {
	// IsAvailable reports whether the target (providerID, model) is eligible to serve traffic.
	IsAvailable(providerID, model string) bool
}

// MapHealthView is an in-memory map implementation of HealthView useful for testing and static wiring.
type MapHealthView struct {
	Available map[string]bool // key: "providerID:model" or "providerID"
}

// IsAvailable checks whether the target is marked available. Defaults to true if not explicitly set false.
func (m MapHealthView) IsAvailable(providerID, model string) bool {
	if m.Available == nil {
		return true
	}
	if v, ok := m.Available[providerID+":"+model]; ok {
		return v
	}
	if v, ok := m.Available[providerID]; ok {
		return v
	}
	return true
}

// RouteRequest contains parameters for resolving candidate targets.
type RouteRequest struct {
	// Model is the requested model name (canonical ID or alias).
	Model string

	// Capabilities required for this request (tools, vision, json, stream).
	Capabilities domain.ModelCapabilities

	// Key is the optional authenticated virtual key (for scopes and overrides).
	Key *domain.VirtualKey

	// PolicyOverride optionally overrides the model's configured fallback policy.
	PolicyOverride domain.FallbackPolicy

	// Seed is an optional seed for deterministic weighted target selection.
	Seed int64

	// Latency is an optional latency view used for PolicyLatency ordering.
	Latency LatencyView
}

// RequestCapabilities inspects a canonical domain.Request to detect required capabilities.
func RequestCapabilities(req domain.Request) domain.ModelCapabilities {
	var caps domain.ModelCapabilities
	if len(req.Tools) > 0 || req.ToolChoice != nil {
		caps.Tools = true
	}
	if req.Stream {
		caps.Stream = true
	}
	if req.Sampling.ResponseFormat != nil &&
		req.Sampling.ResponseFormat.Type != "" &&
		req.Sampling.ResponseFormat.Type != "text" {
		caps.JSONMode = true
	}
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.Type == domain.BlockImage {
				caps.Vision = true
				break
			}
		}
	}
	return caps
}

// Snapshot is an immutable point-in-time view of the model registry.
// Decision functions operate on Snapshots with zero lock contention.
type Snapshot struct {
	Models    map[string]domain.Model       // canonical ID -> Model
	Aliases   map[string]string             // alias -> canonical ID
	Providers map[string]domain.Provider    // provider ID -> Provider
	Rules     map[string]domain.RoutingRule // model ID -> RoutingRule
}

// NewSnapshot constructs a Snapshot from the given slices, copying all entries.
func NewSnapshot(
	models []domain.Model,
	providers []domain.Provider,
	rules []domain.RoutingRule,
) *Snapshot {
	s := &Snapshot{
		Models:    make(map[string]domain.Model, len(models)),
		Aliases:   make(map[string]string),
		Providers: make(map[string]domain.Provider, len(providers)),
		Rules:     make(map[string]domain.RoutingRule, len(rules)),
	}

	for _, p := range providers {
		s.Providers[p.ID] = p
	}

	for _, m := range models {
		s.Models[m.ID] = m
		for _, alias := range m.Aliases {
			s.Aliases[alias] = m.ID
		}
	}

	for _, r := range rules {
		s.Rules[r.ModelID] = r
	}

	return s
}

// ResolveModel resolves a model name (canonical or alias) to its canonical Model.
func (s *Snapshot) ResolveModel(name string) (domain.Model, bool) {
	if s == nil {
		return domain.Model{}, false
	}
	if m, ok := s.Models[name]; ok {
		return m, true
	}
	if id, ok := s.Aliases[name]; ok {
		if m, ok := s.Models[id]; ok {
			return m, true
		}
	}
	return domain.Model{}, false
}

// StorageSource provides access to persisted routing entities.
// This allows storage-backed persistence without importing storage directly.
type StorageSource interface {
	ListModels() ([]domain.Model, error)
	ListProviders() ([]domain.Provider, error)
	ListRules() ([]domain.RoutingRule, error)
}

// Registry is a thread-safe container for routing models, providers, and rules.
// Reads take atomic snapshots; writes swap the snapshot pointer under a mutex.
type Registry struct {
	snap atomic.Pointer[Snapshot]
	mu   sync.Mutex
}

// NewRegistry initializes an empty Registry.
func NewRegistry() *Registry {
	r := &Registry{}
	r.snap.Store(NewSnapshot(nil, nil, nil))
	return r
}

// Snapshot returns the current immutable snapshot.
func (r *Registry) Snapshot() *Snapshot {
	return r.snap.Load()
}

// Load atomically replaces the current snapshot with the provided entities.
func (r *Registry) Load(models []domain.Model, providers []domain.Provider, rules []domain.RoutingRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Store(NewSnapshot(models, providers, rules))
}

// LoadFrom populates the registry from a storage source.
func (r *Registry) LoadFrom(src StorageSource) error {
	models, err := src.ListModels()
	if err != nil {
		return fmt.Errorf("routing: load models: %w", err)
	}
	providers, err := src.ListProviders()
	if err != nil {
		return fmt.Errorf("routing: load providers: %w", err)
	}
	rules, err := src.ListRules()
	if err != nil {
		return fmt.Errorf("routing: load rules: %w", err)
	}
	r.Load(models, providers, rules)
	return nil
}

// RegisterModel adds or updates a canonical model.
func (r *Registry) RegisterModel(m domain.Model) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.Snapshot()
	models := make([]domain.Model, 0, len(old.Models)+1)
	for _, existing := range old.Models {
		if existing.ID != m.ID {
			models = append(models, existing)
		}
	}
	models = append(models, m)

	providers := make([]domain.Provider, 0, len(old.Providers))
	for _, p := range old.Providers {
		providers = append(providers, p)
	}

	rules := make([]domain.RoutingRule, 0, len(old.Rules))
	for _, rule := range old.Rules {
		rules = append(rules, rule)
	}

	r.snap.Store(NewSnapshot(models, providers, rules))
}

// UnregisterModel removes a model by canonical ID.
func (r *Registry) UnregisterModel(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.Snapshot()
	models := make([]domain.Model, 0, len(old.Models))
	for _, existing := range old.Models {
		if existing.ID != id {
			models = append(models, existing)
		}
	}

	providers := make([]domain.Provider, 0, len(old.Providers))
	for _, p := range old.Providers {
		providers = append(providers, p)
	}

	rules := make([]domain.RoutingRule, 0, len(old.Rules))
	for _, rule := range old.Rules {
		if rule.ModelID != id {
			rules = append(rules, rule)
		}
	}

	r.snap.Store(NewSnapshot(models, providers, rules))
}

// RegisterProvider adds or updates a provider.
func (r *Registry) RegisterProvider(p domain.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.Snapshot()

	models := make([]domain.Model, 0, len(old.Models))
	for _, m := range old.Models {
		models = append(models, m)
	}

	providers := make([]domain.Provider, 0, len(old.Providers)+1)
	for _, existing := range old.Providers {
		if existing.ID != p.ID {
			providers = append(providers, existing)
		}
	}
	providers = append(providers, p)

	rules := make([]domain.RoutingRule, 0, len(old.Rules))
	for _, rule := range old.Rules {
		rules = append(rules, rule)
	}

	r.snap.Store(NewSnapshot(models, providers, rules))
}

// UnregisterProvider removes a provider by ID.
func (r *Registry) UnregisterProvider(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.Snapshot()

	models := make([]domain.Model, 0, len(old.Models))
	for _, m := range old.Models {
		models = append(models, m)
	}

	providers := make([]domain.Provider, 0, len(old.Providers))
	for _, existing := range old.Providers {
		if existing.ID != id {
			providers = append(providers, existing)
		}
	}

	rules := make([]domain.RoutingRule, 0, len(old.Rules))
	for _, rule := range old.Rules {
		rules = append(rules, rule)
	}

	r.snap.Store(NewSnapshot(models, providers, rules))
}

// RegisterRule adds or updates a fallback routing rule.
func (r *Registry) RegisterRule(rule domain.RoutingRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.Snapshot()

	models := make([]domain.Model, 0, len(old.Models))
	for _, m := range old.Models {
		models = append(models, m)
	}

	providers := make([]domain.Provider, 0, len(old.Providers))
	for _, p := range old.Providers {
		providers = append(providers, p)
	}

	rules := make([]domain.RoutingRule, 0, len(old.Rules)+1)
	for _, existing := range old.Rules {
		if existing.ModelID != rule.ModelID {
			rules = append(rules, existing)
		}
	}
	rules = append(rules, rule)

	r.snap.Store(NewSnapshot(models, providers, rules))
}

// ListModels returns a slice of all registered models in the current snapshot.
func (r *Registry) ListModels() []domain.Model {
	snap := r.Snapshot()
	out := make([]domain.Model, 0, len(snap.Models))
	for _, m := range snap.Models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ListProviders returns a slice of all registered providers in the current snapshot.
func (r *Registry) ListProviders() []domain.Provider {
	snap := r.Snapshot()
	out := make([]domain.Provider, 0, len(snap.Providers))
	for _, p := range snap.Providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DecideTargets resolves targets using the current snapshot.
func (r *Registry) DecideTargets(req RouteRequest, health HealthView) ([]Target, error) {
	return DecideTargets(req, r.Snapshot(), health)
}

// DecideTargets is a pure function that resolves the ordered candidate targets
// for a request given the registry snapshot and health view.
//
// Pipeline:
//  1. Model resolution (canonical ID or alias)
//  2. Virtual key model scope check
//  3. Target candidate extraction
//  4. Provider existence and enabled filter
//  5. Virtual key provider scope filter
//  6. Capability matching (tools, vision, json, stream)
//  7. Health availability filter
//  8. Fallback policy ordering (ordered, cost, weighted)
func DecideTargets(req RouteRequest, reg *Snapshot, health HealthView) ([]Target, error) {
	if reg == nil {
		return nil, ErrNilRegistry
	}

	if req.Model == "" {
		return nil, ErrModelNotFound
	}

	model, ok := reg.ResolveModel(req.Model)
	if !ok {
		return nil, ErrModelNotFound
	}

	// Virtual key model scope validation
	if req.Key != nil && len(req.Key.Scopes.AllowedModels) > 0 {
		allowed := false
		for _, m := range req.Key.Scopes.AllowedModels {
			if m == model.ID || m == req.Model {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrScopeModelDenied
		}
	}

	if len(model.Targets) == 0 {
		return nil, ErrNoTargets
	}

	// Tracking filter drop reasons to emit precise errors
	var (
		providerEnabledCount  int
		keyProviderScopeCount int
		capabilityPassCount   int
		healthyCount          int
	)

	var candidates []Target

	for _, mt := range model.Targets {
		provider, exists := reg.Providers[mt.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}
		providerEnabledCount++

		// Virtual key provider scope validation
		if req.Key != nil && len(req.Key.Scopes.AllowedProviders) > 0 {
			allowed := false
			for _, pid := range req.Key.Scopes.AllowedProviders {
				if pid == mt.ProviderID {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		keyProviderScopeCount++

		// Effective capabilities: target override if set, else model default
		effectiveCaps := model.Capabilities
		if mt.Capabilities != nil {
			effectiveCaps = *mt.Capabilities
		}

		// Capability filters
		if req.Capabilities.Tools && !effectiveCaps.Tools {
			continue
		}
		if req.Capabilities.Vision && !effectiveCaps.Vision {
			continue
		}
		if req.Capabilities.JSONMode && !effectiveCaps.JSONMode {
			continue
		}
		if req.Capabilities.Stream && !effectiveCaps.Stream {
			continue
		}
		capabilityPassCount++

		// Health check filter
		if health != nil && !health.IsAvailable(mt.ProviderID, mt.ProviderModel) {
			continue
		}
		healthyCount++

		costMultiplier := mt.CostMultiplier
		if costMultiplier <= 0 {
			costMultiplier = 100
		}
		weight := mt.Weight
		if weight <= 0 {
			weight = 1
		}

		candidates = append(candidates, Target{
			ProviderID:     mt.ProviderID,
			ProviderModel:  mt.ProviderModel,
			Protocol:       provider.Protocol,
			BaseURL:        provider.BaseURL,
			Weight:         weight,
			Position:       mt.Position,
			CostMultiplier: costMultiplier,
			Capabilities:   effectiveCaps,
		})
	}

	if len(candidates) == 0 {
		switch {
		case providerEnabledCount == 0:
			return nil, ErrNoTargets
		case keyProviderScopeCount == 0:
			return nil, ErrScopeProviderDenied
		case capabilityPassCount == 0:
			return nil, ErrCapabilityMismatch
		case healthyCount == 0:
			return nil, ErrNoHealthyTargets
		default:
			return nil, ErrNoTargets
		}
	}

	// Determine applicable fallback policy using precedence hierarchy
	policy := ResolvePolicy(req, model.ID, reg)
	OrderTargets(candidates, policy, req.Seed, req.Latency)
	return candidates, nil
}
