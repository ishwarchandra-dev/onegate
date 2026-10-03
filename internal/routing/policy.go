// Package routing fallback policies and target ordering implementations.
package routing

import (
	"math/rand"
	"sort"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// LatencyView provides read-only inspection of target latency for PolicyLatency.
type LatencyView interface {
	// TargetLatency returns the observed round-trip latency for (providerID, model).
	// Returns 0 or negative duration if untracked.
	TargetLatency(providerID, model string) time.Duration
}

// MapLatencyView is an in-memory map implementation of LatencyView.
type MapLatencyView struct {
	Latencies map[string]time.Duration // key: "providerID:model" or "providerID"
}

// TargetLatency looks up latency in the map.
func (m MapLatencyView) TargetLatency(providerID, model string) time.Duration {
	if m.Latencies == nil {
		return 0
	}
	if d, ok := m.Latencies[providerID+":"+model]; ok {
		return d
	}
	if d, ok := m.Latencies[providerID]; ok {
		return d
	}
	return 0
}

// ResolvePolicy determines the effective FallbackPolicy for a request.
//
// Precedence order:
//  1. Explicit request override (RouteRequest.PolicyOverride)
//  2. Virtual key model-specific override (Key.Scopes.ModelOverrides[modelID])
//  3. Virtual key global override (Key.Scopes.PolicyOverride)
//  4. Stored routing rule policy for model (RoutingRule.Policy)
//  5. Default: domain.PolicyOrdered
func ResolvePolicy(req RouteRequest, modelID string, reg *Snapshot) domain.FallbackPolicy {
	if req.PolicyOverride != "" {
		return req.PolicyOverride
	}
	if req.Key != nil {
		if req.Key.Scopes.ModelOverrides != nil {
			if p, ok := req.Key.Scopes.ModelOverrides[modelID]; ok && p != "" {
				return p
			}
		}
		if req.Key.Scopes.PolicyOverride != "" {
			return req.Key.Scopes.PolicyOverride
		}
	}
	if reg != nil {
		if rule, ok := reg.Rules[modelID]; ok && rule.Enabled && rule.Policy != "" {
			return rule.Policy
		}
	}
	return domain.PolicyOrdered
}

// OrderTargets orders eligible targets in-place according to the chosen policy,
// using seed for deterministic weighted ordering, and latencyView for latency ordering.
//
// OmniRoute v3.8.52 parity semantics:
//   - PolicyOrdered: position order (lower first), ties broken by ProviderID.
//   - PolicyCost: CostMultiplier order (cheaper first), ties broken by Position, then ProviderID.
//   - PolicyLatency: lowest observed latency first. Untracked targets (<=0) follow tracked ones,
//     ordered by Position. Ties broken by Position, then ProviderID.
//   - PolicyWeighted: random permutation weighted by relative shares without replacement.
//     Deterministic under fixed seed.
func OrderTargets(targets []Target, policy domain.FallbackPolicy, seed int64, latency LatencyView) {
	if len(targets) <= 1 {
		return
	}

	switch policy {
	case domain.PolicyCost:
		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i].CostMultiplier != targets[j].CostMultiplier {
				return targets[i].CostMultiplier < targets[j].CostMultiplier
			}
			if targets[i].Position != targets[j].Position {
				return targets[i].Position < targets[j].Position
			}
			return targets[i].ProviderID < targets[j].ProviderID
		})

	case domain.PolicyLatency:
		sort.SliceStable(targets, func(i, j int) bool {
			var latI, latJ time.Duration
			if latency != nil {
				latI = latency.TargetLatency(targets[i].ProviderID, targets[i].ProviderModel)
				latJ = latency.TargetLatency(targets[j].ProviderID, targets[j].ProviderModel)
			}

			// If both have tracked positive latency, compare directly
			if latI > 0 && latJ > 0 {
				if latI != latJ {
					return latI < latJ
				}
			} else if latI > 0 && latJ <= 0 {
				// Tracked beats untracked
				return true
			} else if latI <= 0 && latJ > 0 {
				// Untracked loses to tracked
				return false
			}

			// Ties or untracked fall back to Position, then ProviderID
			if targets[i].Position != targets[j].Position {
				return targets[i].Position < targets[j].Position
			}
			return targets[i].ProviderID < targets[j].ProviderID
		})

	case domain.PolicyWeighted:
		// Deterministic weighted selection using seed
		r := rand.New(rand.NewSource(seed))
		remaining := make([]Target, len(targets))
		copy(remaining, targets)
		ordered := make([]Target, 0, len(targets))

		for len(remaining) > 0 {
			totalWeight := 0
			for _, t := range remaining {
				if t.Weight > 0 {
					totalWeight += t.Weight
				}
			}

			if totalWeight <= 0 {
				// Remaining have zero or negative weight: append in position order
				sort.SliceStable(remaining, func(i, j int) bool {
					if remaining[i].Position != remaining[j].Position {
						return remaining[i].Position < remaining[j].Position
					}
					return remaining[i].ProviderID < remaining[j].ProviderID
				})
				ordered = append(ordered, remaining...)
				break
			}

			pick := r.Intn(totalWeight)
			acc := 0
			chosenIdx := 0
			for idx, t := range remaining {
				if t.Weight <= 0 {
					continue
				}
				acc += t.Weight
				if pick < acc {
					chosenIdx = idx
					break
				}
			}
			ordered = append(ordered, remaining[chosenIdx])
			remaining = append(remaining[:chosenIdx], remaining[chosenIdx+1:]...)
		}
		copy(targets, ordered)

	case domain.PolicyOrdered:
		fallthrough
	default:
		sort.SliceStable(targets, func(i, j int) bool {
			if targets[i].Position != targets[j].Position {
				return targets[i].Position < targets[j].Position
			}
			return targets[i].ProviderID < targets[j].ProviderID
		})
	}
}
