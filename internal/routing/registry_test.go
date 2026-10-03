package routing_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

func sampleSnapshot() *routing.Snapshot {
	providers := []domain.Provider{
		{
			ID:       "openai",
			Name:     "OpenAI Direct",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  "https://api.openai.com",
			Enabled:  true,
		},
		{
			ID:       "anthropic",
			Name:     "Anthropic Direct",
			Protocol: domain.ProtocolAnthropic,
			BaseURL:  "https://api.anthropic.com",
			Enabled:  true,
		},
		{
			ID:       "groq",
			Name:     "Groq Cloud",
			Protocol: domain.ProtocolOpenAIComp,
			BaseURL:  "https://api.groq.com/openai",
			Enabled:  true,
		},
		{
			ID:       "disabled-prov",
			Name:     "Disabled Provider",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  "https://disabled.example.com",
			Enabled:  false,
		},
	}

	models := []domain.Model{
		{
			ID:      "gpt-4o",
			Aliases: []string{"gpt-4o-latest", "chat-best"},
			Capabilities: domain.ModelCapabilities{
				Tools:    true,
				Vision:   true,
				JSONMode: true,
				Stream:   true,
			},
			Targets: []domain.ModelTarget{
				{
					ProviderID:     "openai",
					ProviderModel:  "gpt-4o-2024-08-06",
					Position:       0,
					Weight:         10,
					CostMultiplier: 100,
					// inherits model capabilities: tools, vision, json, stream
				},
				{
					ProviderID:     "anthropic",
					ProviderModel:  "claude-3-5-sonnet-20241022",
					Position:       1,
					Weight:         5,
					CostMultiplier: 110,
				},
			},
		},
		{
			ID: "hybrid-model",
			Capabilities: domain.ModelCapabilities{
				Tools:    true,
				Vision:   true,
				JSONMode: true,
				Stream:   true,
			},
			Targets: []domain.ModelTarget{
				{
					ProviderID:     "groq",
					ProviderModel:  "llama-3.1-70b-versatile",
					Position:       0,
					Weight:         20,
					CostMultiplier: 30, // cheap!
					Capabilities: &domain.ModelCapabilities{
						Tools:    true,
						Vision:   false, // groq target has no vision
						JSONMode: true,
						Stream:   true,
					},
				},
				{
					ProviderID:     "openai",
					ProviderModel:  "gpt-4o",
					Position:       1,
					Weight:         10,
					CostMultiplier: 100,
					Capabilities: &domain.ModelCapabilities{
						Tools:    true,
						Vision:   true, // openai has vision
						JSONMode: true,
						Stream:   true,
					},
				},
				{
					ProviderID:     "anthropic",
					ProviderModel:  "claude-3-5-haiku",
					Position:       2,
					Weight:         5,
					CostMultiplier: 50,
					Capabilities: &domain.ModelCapabilities{
						Tools:    false, // target has no tools
						Vision:   false,
						JSONMode: true,
						Stream:   true,
					},
				},
			},
		},
		{
			ID: "no-target-model",
			Capabilities: domain.ModelCapabilities{
				Stream: true,
			},
			Targets: nil,
		},
		{
			ID: "disabled-target-model",
			Capabilities: domain.ModelCapabilities{
				Stream: true,
			},
			Targets: []domain.ModelTarget{
				{
					ProviderID:    "disabled-prov",
					ProviderModel: "some-model",
				},
			},
		},
	}

	rules := []domain.RoutingRule{
		{
			ID:       "rule-gpt-4o",
			ModelID:  "gpt-4o",
			Policy:   domain.PolicyOrdered,
			Enabled:  true,
			Position: 0,
		},
		{
			ID:       "rule-hybrid",
			ModelID:  "hybrid-model",
			Policy:   domain.PolicyCost,
			Enabled:  true,
			Position: 1,
		},
	}

	return routing.NewSnapshot(models, providers, rules)
}

func TestDecideTargets_Table(t *testing.T) {
	snap := sampleSnapshot()

	tests := []struct {
		name        string
		req         routing.RouteRequest
		health      routing.HealthView
		wantTargets []string // expected provider IDs in order
		wantErr     error
	}{
		{
			name: "Canonical model ordered",
			req: routing.RouteRequest{
				Model: "gpt-4o",
			},
			health:      nil,
			wantTargets: []string{"openai", "anthropic"},
			wantErr:     nil,
		},
		{
			name: "Alias model lookup",
			req: routing.RouteRequest{
				Model: "gpt-4o-latest",
			},
			health:      nil,
			wantTargets: []string{"openai", "anthropic"},
			wantErr:     nil,
		},
		{
			name: "Unknown model",
			req: routing.RouteRequest{
				Model: "non-existent-model",
			},
			wantErr: routing.ErrModelNotFound,
		},
		{
			name: "Model without targets",
			req: routing.RouteRequest{
				Model: "no-target-model",
			},
			wantErr: routing.ErrNoTargets,
		},
		{
			name: "Model with only disabled provider targets",
			req: routing.RouteRequest{
				Model: "disabled-target-model",
			},
			wantErr: routing.ErrNoTargets,
		},
		{
			name: "Virtual key scope allowed model",
			req: routing.RouteRequest{
				Model: "gpt-4o",
				Key: &domain.VirtualKey{
					Scopes: domain.KeyScopes{
						AllowedModels: []string{"gpt-4o"},
					},
				},
			},
			wantTargets: []string{"openai", "anthropic"},
			wantErr:     nil,
		},
		{
			name: "Virtual key scope denied model",
			req: routing.RouteRequest{
				Model: "gpt-4o",
				Key: &domain.VirtualKey{
					Scopes: domain.KeyScopes{
						AllowedModels: []string{"other-model"},
					},
				},
			},
			wantErr: routing.ErrScopeModelDenied,
		},
		{
			name: "Virtual key scope allowed providers filter",
			req: routing.RouteRequest{
				Model: "gpt-4o",
				Key: &domain.VirtualKey{
					Scopes: domain.KeyScopes{
						AllowedProviders: []string{"anthropic"},
					},
				},
			},
			wantTargets: []string{"anthropic"},
			wantErr:     nil,
		},
		{
			name: "Virtual key scope denied all providers",
			req: routing.RouteRequest{
				Model: "gpt-4o",
				Key: &domain.VirtualKey{
					Scopes: domain.KeyScopes{
						AllowedProviders: []string{"unknown-provider"},
					},
				},
			},
			wantErr: routing.ErrScopeProviderDenied,
		},
		{
			name: "Cost policy sorting on hybrid model",
			req: routing.RouteRequest{
				Model: "hybrid-model",
			},
			// Cost multipliers: groq (30), anthropic (50), openai (100)
			wantTargets: []string{"groq", "anthropic", "openai"},
			wantErr:     nil,
		},
		{
			name: "Policy override to ordered on hybrid model",
			req: routing.RouteRequest{
				Model:          "hybrid-model",
				PolicyOverride: domain.PolicyOrdered,
			},
			// Positions: groq (0), openai (1), anthropic (2)
			wantTargets: []string{"groq", "openai", "anthropic"},
			wantErr:     nil,
		},
		{
			name: "Capability filter: vision required excludes groq and anthropic",
			req: routing.RouteRequest{
				Model: "hybrid-model",
				Capabilities: domain.ModelCapabilities{
					Vision: true,
				},
			},
			// Only openai supports vision on hybrid-model
			wantTargets: []string{"openai"},
			wantErr:     nil,
		},
		{
			name: "Capability filter: tools required excludes anthropic",
			req: routing.RouteRequest{
				Model: "hybrid-model",
				Capabilities: domain.ModelCapabilities{
					Tools: true,
				},
			},
			// groq and openai have tools; sorted by cost (groq 30, openai 100)
			wantTargets: []string{"groq", "openai"},
			wantErr:     nil,
		},
		{
			name: "Capability filter: mismatch when no target supports capability",
			req: routing.RouteRequest{
				Model: "hybrid-model",
				Capabilities: domain.ModelCapabilities{
					Vision: true,
					Tools:  false,
				},
				Key: &domain.VirtualKey{
					Scopes: domain.KeyScopes{
						AllowedProviders: []string{"groq"}, // groq does not have vision
					},
				},
			},
			wantErr: routing.ErrCapabilityMismatch,
		},
		{
			name: "Health filter skips unavailable target",
			req: routing.RouteRequest{
				Model: "gpt-4o",
			},
			health: routing.MapHealthView{
				Available: map[string]bool{
					"openai": false,
				},
			},
			wantTargets: []string{"anthropic"},
			wantErr:     nil,
		},
		{
			name: "Health filter all targets down returns ErrNoHealthyTargets",
			req: routing.RouteRequest{
				Model: "gpt-4o",
			},
			health: routing.MapHealthView{
				Available: map[string]bool{
					"openai":    false,
					"anthropic": false,
				},
			},
			wantErr: routing.ErrNoHealthyTargets,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := routing.DecideTargets(tt.req, snap, tt.health)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("want error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			gotProvIDs := make([]string, len(got))
			for i, target := range got {
				gotProvIDs[i] = target.ProviderID
			}

			if !reflect.DeepEqual(gotProvIDs, tt.wantTargets) {
				t.Fatalf("targets mismatch: want %v, got %v", tt.wantTargets, gotProvIDs)
			}
		})
	}
}

func TestDecideTargets_PureDeterminism(t *testing.T) {
	snap := sampleSnapshot()
	req := routing.RouteRequest{
		Model: "hybrid-model",
		Capabilities: domain.ModelCapabilities{
			Tools: true,
		},
	}

	// 100 consecutive runs must yield identical results without side effects
	var firstResult []routing.Target
	for i := 0; i < 100; i++ {
		got, err := routing.DecideTargets(req, snap, nil)
		if err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
		if i == 0 {
			firstResult = got
			continue
		}
		if !reflect.DeepEqual(firstResult, got) {
			t.Fatalf("run %d deviated from run 0: %+v vs %+v", i, got, firstResult)
		}
	}
}

func TestDecideTargets_WeightedDeterminism(t *testing.T) {
	snap := sampleSnapshot()
	req1 := routing.RouteRequest{
		Model:          "gpt-4o",
		PolicyOverride: domain.PolicyWeighted,
		Seed:           42,
	}
	req2 := routing.RouteRequest{
		Model:          "gpt-4o",
		PolicyOverride: domain.PolicyWeighted,
		Seed:           42,
	}

	res1, err := routing.DecideTargets(req1, snap, nil)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := routing.DecideTargets(req2, snap, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(res1, res2) {
		t.Fatalf("weighted selection not deterministic with identical seed: %+v vs %+v", res1, res2)
	}
}

func TestRequestCapabilities(t *testing.T) {
	t.Run("tools detection", func(t *testing.T) {
		req := domain.Request{
			Tools: []domain.Tool{{Name: "calc"}},
		}
		caps := routing.RequestCapabilities(req)
		if !caps.Tools {
			t.Error("expected Tools: true")
		}
	})

	t.Run("vision detection", func(t *testing.T) {
		req := domain.Request{
			Messages: []domain.Message{
				{
					Role: domain.RoleUser,
					Content: []domain.ContentBlock{
						{Type: domain.BlockImage},
					},
				},
			},
		}
		caps := routing.RequestCapabilities(req)
		if !caps.Vision {
			t.Error("expected Vision: true")
		}
	})

	t.Run("json mode detection", func(t *testing.T) {
		req := domain.Request{
			Sampling: domain.SamplingParams{
				ResponseFormat: &domain.ResponseFormat{
					Type: "json_object",
				},
			},
		}
		caps := routing.RequestCapabilities(req)
		if !caps.JSONMode {
			t.Error("expected JSONMode: true")
		}
	})

	t.Run("stream detection", func(t *testing.T) {
		req := domain.Request{
			Stream: true,
		}
		caps := routing.RequestCapabilities(req)
		if !caps.Stream {
			t.Error("expected Stream: true")
		}
	})
}

func TestRegistry_Concurrency(t *testing.T) {
	reg := routing.NewRegistry()
	snap := sampleSnapshot()
	reg.Load(reg.ListModels(), reg.ListProviders(), nil)

	for _, p := range snap.Providers {
		reg.RegisterProvider(p)
	}
	for _, m := range snap.Models {
		reg.RegisterModel(m)
	}

	const goroutines = 20
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Reader goroutines
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, _ = reg.DecideTargets(routing.RouteRequest{
					Model: "gpt-4o",
				}, nil)
				_ = reg.ListModels()
			}
		}()
	}

	// Writer goroutines
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				reg.RegisterModel(domain.Model{
					ID: "dynamic-model",
					Capabilities: domain.ModelCapabilities{
						Stream: true,
					},
				})
			}
		}(i)
	}

	wg.Wait()
}

func TestRegistry_StoragePersistence(t *testing.T) {
	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	prov := storage.ProviderRecord{
		Provider: domain.Provider{
			ID:       "prov-stored",
			Name:     "Stored Provider",
			Protocol: domain.ProtocolOpenAI,
			BaseURL:  "https://stored.example.com",
			Enabled:  true,
		},
	}
	if err := store.Providers().Upsert(&prov); err != nil {
		t.Fatalf("upsert provider: %v", err)
	}

	m := domain.Model{
		ID:      "stored-model",
		Aliases: []string{"stored-alias"},
		Capabilities: domain.ModelCapabilities{
			Tools:  true,
			Stream: true,
		},
		Targets: []domain.ModelTarget{
			{
				ProviderID:    "prov-stored",
				ProviderModel: "provider-m1",
				Position:      0,
				Weight:        1,
			},
		},
	}
	if err := store.Models().Upsert(m); err != nil {
		t.Fatalf("upsert model: %v", err)
	}

	rule := domain.RoutingRule{
		ID:       "rule-stored",
		ModelID:  "stored-model",
		Policy:   domain.PolicyOrdered,
		Enabled:  true,
		Position: 0,
	}
	if err := store.RoutingRules().Upsert(&rule); err != nil {
		t.Fatalf("upsert rule: %v", err)
	}

	// Hydrate registry directly from storage via routing.StorageSource
	reg := routing.NewRegistry()
	if err := reg.LoadFrom(store.RoutingSource()); err != nil {
		t.Fatalf("LoadFrom storage: %v", err)
	}

	targets, err := reg.DecideTargets(routing.RouteRequest{
		Model: "stored-alias",
	}, nil)
	if err != nil {
		t.Fatalf("DecideTargets: %v", err)
	}

	if len(targets) != 1 || targets[0].ProviderID != "prov-stored" || targets[0].ProviderModel != "provider-m1" {
		t.Fatalf("unexpected targets: %+v", targets)
	}
}
