// Package storage routing adapter.
package storage

import "github.com/ishwarchandra-dev/onegate/internal/domain"

// RoutingSource adapts a storage.Store to supply entities to routing.Registry.
type RoutingSource struct {
	Store *Store
}

// RoutingSource returns a RoutingSource backed by s.
func (s *Store) RoutingSource() *RoutingSource {
	return &RoutingSource{Store: s}
}

// ListModels retrieves all canonical models and their targets.
func (rs *RoutingSource) ListModels() ([]domain.Model, error) {
	return rs.Store.Models().List()
}

// ListProviders retrieves all configured upstream providers.
func (rs *RoutingSource) ListProviders() ([]domain.Provider, error) {
	recs, err := rs.Store.Providers().List()
	if err != nil {
		return nil, err
	}
	out := make([]domain.Provider, len(recs))
	for i, r := range recs {
		out[i] = r.Provider
	}
	return out, nil
}

// ListRules retrieves all fallback routing rules.
func (rs *RoutingSource) ListRules() ([]domain.RoutingRule, error) {
	return rs.Store.RoutingRules().List()
}
