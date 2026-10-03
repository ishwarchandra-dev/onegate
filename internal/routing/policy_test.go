package routing_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
)

func TestPolicyOrdered(t *testing.T) {
	targets := []routing.Target{
		{ProviderID: "prov-c", Position: 2},
		{ProviderID: "prov-a", Position: 0},
		{ProviderID: "prov-b", Position: 1},
		{ProviderID: "prov-z", Position: 1}, // tie with prov-b on position 1
	}

	routing.OrderTargets(targets, domain.PolicyOrdered, 0, nil)

	want := []string{"prov-a", "prov-b", "prov-z", "prov-c"}
	for i, tg := range targets {
		if tg.ProviderID != want[i] {
			t.Fatalf("position %d: want %s, got %s", i, want[i], tg.ProviderID)
		}
	}
}

func TestPolicyCost(t *testing.T) {
	targets := []routing.Target{
		{ProviderID: "expensive", CostMultiplier: 200, Position: 0},
		{ProviderID: "cheap-1", CostMultiplier: 50, Position: 2},
		{ProviderID: "cheap-0", CostMultiplier: 50, Position: 1}, // same cost as cheap-1, lower position
		{ProviderID: "nominal", CostMultiplier: 100, Position: 0},
	}

	routing.OrderTargets(targets, domain.PolicyCost, 0, nil)

	want := []string{"cheap-0", "cheap-1", "nominal", "expensive"}
	for i, tg := range targets {
		if tg.ProviderID != want[i] {
			t.Fatalf("cost position %d: want %s, got %s", i, want[i], tg.ProviderID)
		}
	}
}

func TestPolicyLatency(t *testing.T) {
	lat := routing.MapLatencyView{
		Latencies: map[string]time.Duration{
			"fast:model": 15 * time.Millisecond,
			"slow:model": 120 * time.Millisecond,
			"mid:model":  45 * time.Millisecond,
			// "untracked": 0 (not in map)
		},
	}

	targets := []routing.Target{
		{ProviderID: "slow", ProviderModel: "model", Position: 0},
		{ProviderID: "untracked", ProviderModel: "model", Position: 1},
		{ProviderID: "fast", ProviderModel: "model", Position: 2},
		{ProviderID: "mid", ProviderModel: "model", Position: 3},
	}

	routing.OrderTargets(targets, domain.PolicyLatency, 0, lat)

	// fast (15ms) -> mid (45ms) -> slow (120ms) -> untracked
	want := []string{"fast", "mid", "slow", "untracked"}
	for i, tg := range targets {
		if tg.ProviderID != want[i] {
			t.Fatalf("latency position %d: want %s, got %s", i, want[i], tg.ProviderID)
		}
	}
}

func TestPolicyWeighted_Determinism(t *testing.T) {
	targets := []routing.Target{
		{ProviderID: "a", Weight: 10},
		{ProviderID: "b", Weight: 50},
		{ProviderID: "c", Weight: 40},
	}

	t1 := make([]routing.Target, len(targets))
	copy(t1, targets)
	routing.OrderTargets(t1, domain.PolicyWeighted, 12345, nil)

	t2 := make([]routing.Target, len(targets))
	copy(t2, targets)
	routing.OrderTargets(t2, domain.PolicyWeighted, 12345, nil)

	if !reflect.DeepEqual(t1, t2) {
		t.Fatalf("weighted ordering not deterministic with same seed: %+v vs %+v", t1, t2)
	}

	// Verify that different seeds produce different permutations
	differentSeen := false
	for seed := int64(1); seed <= 20; seed++ {
		tSeed := make([]routing.Target, len(targets))
		copy(tSeed, targets)
		routing.OrderTargets(tSeed, domain.PolicyWeighted, seed, nil)
		if !reflect.DeepEqual(t1, tSeed) {
			differentSeen = true
			break
		}
	}
	if !differentSeen {
		t.Fatal("expected varying seeds to produce different permutations")
	}
}

func TestPolicyWeighted_Distribution(t *testing.T) {
	// Target A has 80% weight, Target B has 20% weight.
	// Over 10,000 trials, Target A should be picked first ~80% of the time (allow ±3% tolerance).
	baseTargets := []routing.Target{
		{ProviderID: "heavy", Weight: 80},
		{ProviderID: "light", Weight: 20},
	}

	const trials = 10000
	heavyFirstCount := 0

	for i := 0; i < trials; i++ {
		tCopy := make([]routing.Target, len(baseTargets))
		copy(tCopy, baseTargets)
		routing.OrderTargets(tCopy, domain.PolicyWeighted, int64(i*31+7), nil)
		if tCopy[0].ProviderID == "heavy" {
			heavyFirstCount++
		}
	}

	ratio := float64(heavyFirstCount) / float64(trials)
	expected := 0.80
	if math.Abs(ratio-expected) > 0.03 {
		t.Fatalf("weighted distribution out of bounds: want ~0.80, got %.4f (%d/%d)", ratio, heavyFirstCount, trials)
	}
}

func TestResolvePolicy_Hierarchy(t *testing.T) {
	snap := routing.NewSnapshot(
		nil,
		nil,
		[]domain.RoutingRule{
			{
				ModelID: "model-rule",
				Policy:  domain.PolicyOrdered,
				Enabled: true,
			},
		},
	)

	// 1. Explicit request override beats everything
	req1 := routing.RouteRequest{
		PolicyOverride: domain.PolicyCost,
		Key: &domain.VirtualKey{
			Scopes: domain.KeyScopes{
				PolicyOverride: domain.PolicyWeighted,
				ModelOverrides: map[string]domain.FallbackPolicy{
					"model-rule": domain.PolicyLatency,
				},
			},
		},
	}
	if got := routing.ResolvePolicy(req1, "model-rule", snap); got != domain.PolicyCost {
		t.Fatalf("want PolicyCost, got %v", got)
	}

	// 2. Key model-specific override beats key global and rule policy
	req2 := routing.RouteRequest{
		Key: &domain.VirtualKey{
			Scopes: domain.KeyScopes{
				PolicyOverride: domain.PolicyWeighted,
				ModelOverrides: map[string]domain.FallbackPolicy{
					"model-rule": domain.PolicyLatency,
				},
			},
		},
	}
	if got := routing.ResolvePolicy(req2, "model-rule", snap); got != domain.PolicyLatency {
		t.Fatalf("want PolicyLatency, got %v", got)
	}

	// 3. Key global override beats rule policy
	req3 := routing.RouteRequest{
		Key: &domain.VirtualKey{
			Scopes: domain.KeyScopes{
				PolicyOverride: domain.PolicyWeighted,
			},
		},
	}
	if got := routing.ResolvePolicy(req3, "model-rule", snap); got != domain.PolicyWeighted {
		t.Fatalf("want PolicyWeighted, got %v", got)
	}

	// 4. Stored rule policy used when no key override
	req4 := routing.RouteRequest{}
	if got := routing.ResolvePolicy(req4, "model-rule", snap); got != domain.PolicyOrdered {
		t.Fatalf("want PolicyOrdered, got %v", got)
	}

	// 5. Default PolicyOrdered when no rule exists
	if got := routing.ResolvePolicy(req4, "unknown-model", snap); got != domain.PolicyOrdered {
		t.Fatalf("want default PolicyOrdered, got %v", got)
	}
}

func TestOmniRouteChainSemantics(t *testing.T) {
	// OmniRoute v3.8.52 parity test:
	// A virtual key with model-specific policy override:
	// - "chat-heavy" routes using PolicyCost
	// - "chat-quick" routes using PolicyOrdered
	providers := []domain.Provider{
		{ID: "p-groq", BaseURL: "https://groq", Protocol: domain.ProtocolOpenAIComp, Enabled: true},
		{ID: "p-openai", BaseURL: "https://openai", Protocol: domain.ProtocolOpenAI, Enabled: true},
		{ID: "p-anthropic", BaseURL: "https://anthropic", Protocol: domain.ProtocolAnthropic, Enabled: true},
	}

	models := []domain.Model{
		{
			ID: "chat-heavy",
			Targets: []domain.ModelTarget{
				{ProviderID: "p-openai", ProviderModel: "gpt-4o", CostMultiplier: 100, Position: 0},
				{ProviderID: "p-groq", ProviderModel: "llama-70b", CostMultiplier: 25, Position: 1},
				{ProviderID: "p-anthropic", ProviderModel: "claude-3-5", CostMultiplier: 110, Position: 2},
			},
		},
		{
			ID: "chat-quick",
			Targets: []domain.ModelTarget{
				{ProviderID: "p-groq", ProviderModel: "llama-8b", CostMultiplier: 10, Position: 0},
				{ProviderID: "p-openai", ProviderModel: "gpt-4o-mini", CostMultiplier: 15, Position: 1},
			},
		},
	}

	rules := []domain.RoutingRule{
		{ModelID: "chat-heavy", Policy: domain.PolicyOrdered, Enabled: true},
		{ModelID: "chat-quick", Policy: domain.PolicyOrdered, Enabled: true},
	}

	snap := routing.NewSnapshot(models, providers, rules)

	// Virtual key overrides chat-heavy to PolicyCost
	vkey := &domain.VirtualKey{
		ID: "vkey-parity",
		Scopes: domain.KeyScopes{
			ModelOverrides: map[string]domain.FallbackPolicy{
				"chat-heavy": domain.PolicyCost,
			},
		},
	}

	// 1. chat-heavy should use PolicyCost: p-groq (25) -> p-openai (100) -> p-anthropic (110)
	targetsHeavy, err := routing.DecideTargets(routing.RouteRequest{
		Model: "chat-heavy",
		Key:   vkey,
	}, snap, nil)
	if err != nil {
		t.Fatalf("decide chat-heavy: %v", err)
	}

	wantHeavy := []string{"p-groq", "p-openai", "p-anthropic"}
	for i, tg := range targetsHeavy {
		if tg.ProviderID != wantHeavy[i] {
			t.Fatalf("chat-heavy index %d: want %s, got %s", i, wantHeavy[i], tg.ProviderID)
		}
	}

	// 2. chat-quick has no override; uses model rule PolicyOrdered: p-groq (0) -> p-openai (1)
	targetsQuick, err := routing.DecideTargets(routing.RouteRequest{
		Model: "chat-quick",
		Key:   vkey,
	}, snap, nil)
	if err != nil {
		t.Fatalf("decide chat-quick: %v", err)
	}

	wantQuick := []string{"p-groq", "p-openai"}
	for i, tg := range targetsQuick {
		if tg.ProviderID != wantQuick[i] {
			t.Fatalf("chat-quick index %d: want %s, got %s", i, wantQuick[i], tg.ProviderID)
		}
	}
}
