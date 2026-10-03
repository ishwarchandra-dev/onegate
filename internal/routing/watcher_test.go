package routing_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

func TestWatcher_InFlightOldConfigAndNewRequests(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// 1. Initial configuration in storage: model-v1 pointing to prov-1
	prov1 := storage.ProviderRecord{
		Provider: domain.Provider{
			ID:       "prov-1",
			Name:     "Provider 1",
			Protocol: domain.ProtocolOpenAI,
			Enabled:  true,
		},
	}
	if err := store.Providers().Upsert(&prov1); err != nil {
		t.Fatal(err)
	}

	modelV1 := domain.Model{
		ID: "test-model",
		Targets: []domain.ModelTarget{
			{ProviderID: "prov-1", ProviderModel: "model-target-v1", Position: 0},
		},
	}
	if err := store.Models().Upsert(modelV1); err != nil {
		t.Fatal(err)
	}

	reg := routing.NewRegistry()
	var changeCount int
	var mu sync.Mutex

	watcher := routing.NewWatcher(routing.WatcherConfig{
		Registry: reg,
		Source:   store.RoutingSource(),
		OnChange: func(ev routing.ChangeEvent) {
			mu.Lock()
			changeCount++
			mu.Unlock()
		},
	})

	if err := watcher.ReloadNow(ctx); err != nil {
		t.Fatalf("initial ReloadNow: %v", err)
	}

	// 2. In-flight request captures snapshot BEFORE the update
	inFlightSnapshot := reg.Snapshot()

	// Verify in-flight snapshot sees prov-1
	targetsPre, err := routing.DecideTargets(routing.RouteRequest{Model: "test-model"}, inFlightSnapshot, nil)
	if err != nil || len(targetsPre) != 1 || targetsPre[0].ProviderID != "prov-1" {
		t.Fatalf("pre-reload targets mismatch: %+v", targetsPre)
	}

	// 3. Storage changes: add prov-2 and update model to point to prov-2 instead of prov-1
	prov2 := storage.ProviderRecord{
		Provider: domain.Provider{
			ID:       "prov-2",
			Name:     "Provider 2",
			Protocol: domain.ProtocolAnthropic,
			Enabled:  true,
		},
	}
	if err := store.Providers().Upsert(&prov2); err != nil {
		t.Fatal(err)
	}

	modelV2 := domain.Model{
		ID: "test-model",
		Targets: []domain.ModelTarget{
			{ProviderID: "prov-2", ProviderModel: "model-target-v2", Position: 0},
		},
	}
	if err := store.Models().Upsert(modelV2); err != nil {
		t.Fatal(err)
	}

	// 4. Trigger watcher reload
	if err := watcher.ReloadNow(ctx); err != nil {
		t.Fatalf("ReloadNow after storage update: %v", err)
	}

	mu.Lock()
	if changeCount != 2 {
		t.Fatalf("want 2 change events (initial + update), got %d", changeCount)
	}
	mu.Unlock()

	// 5. In-flight request still finishes on old config!
	targetsInFlight, err := routing.DecideTargets(routing.RouteRequest{Model: "test-model"}, inFlightSnapshot, nil)
	if err != nil || len(targetsInFlight) != 1 || targetsInFlight[0].ProviderID != "prov-1" {
		t.Fatalf("in-flight request on old snapshot deviated! got: %+v", targetsInFlight)
	}

	// 6. New request immediately sees new config!
	newSnapshot := reg.Snapshot()
	targetsNew, err := routing.DecideTargets(routing.RouteRequest{Model: "test-model"}, newSnapshot, nil)
	if err != nil || len(targetsNew) != 1 || targetsNew[0].ProviderID != "prov-2" {
		t.Fatalf("new request failed to see updated provider: %+v", targetsNew)
	}
}

func TestWatcher_BackgroundPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := storage.OpenTemp()
	if err != nil {
		t.Fatalf("OpenTemp: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	reg := routing.NewRegistry()
	changed := make(chan struct{}, 10)

	watcher := routing.NewWatcher(routing.WatcherConfig{
		Registry:     reg,
		Source:       store.RoutingSource(),
		PollInterval: 20 * time.Millisecond,
		OnChange: func(ev routing.ChangeEvent) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})

	watcher.Start(ctx)
	defer watcher.Stop()

	// Wait for initial load
	select {
	case <-changed:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for initial load")
	}

	// Insert new model into storage
	m := domain.Model{
		ID: "polled-model",
	}
	if err := store.Models().Upsert(m); err != nil {
		t.Fatal(err)
	}

	// Watcher background polling should detect it within 100ms
	select {
	case <-changed:
		// Succeeded! Verify registry sees polled-model
		if _, ok := reg.Snapshot().ResolveModel("polled-model"); !ok {
			t.Fatal("polled model not present in swapped snapshot")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background poller did not detect storage change within 500ms")
	}
}
