package routing_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/routing"
)

type mockClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMockClock() *mockClock {
	return &mockClock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
}

func (m *mockClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *mockClock) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}

func TestHealthTracker_CircuitOpen(t *testing.T) {
	clock := newMockClock()
	cfg := routing.HealthConfig{
		ConsecutiveFailures: 3,
		Cooldown:            10 * time.Second,
		SuccessThreshold:    2,
	}
	tracker := routing.NewHealthTracker(cfg)
	tracker.SetClock(clock.Now)

	providerID := "prov-1"
	model := "gpt-4o"

	// 1. Initial state is closed (healthy)
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected target to be initially available")
	}
	if st := tracker.GetState(providerID, model); st != routing.StateClosed {
		t.Fatalf("want StateClosed, got %v", st)
	}

	// 2. Failure 1 and 2: still closed
	tracker.RecordFailure(providerID, model, "connection reset")
	if st := tracker.GetState(providerID, model); st != routing.StateClosed {
		t.Fatalf("want StateClosed after 1 failure, got %v", st)
	}
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected target to still be available after 1 failure")
	}

	tracker.RecordFailure(providerID, model, "timeout")
	if st := tracker.GetState(providerID, model); st != routing.StateClosed {
		t.Fatalf("want StateClosed after 2 failures, got %v", st)
	}

	// 3. Failure 3: threshold reached -> circuit opens
	tracker.RecordFailure(providerID, model, "503 service unavailable")
	if st := tracker.GetState(providerID, model); st != routing.StateOpen {
		t.Fatalf("want StateOpen after 3 consecutive failures, got %v", st)
	}
	if tracker.IsAvailable(providerID, model) {
		t.Fatal("expected target to NOT be available when circuit is open")
	}
}

func TestHealthTracker_CooldownAndHalfOpenRecovery(t *testing.T) {
	clock := newMockClock()
	cfg := routing.HealthConfig{
		ConsecutiveFailures: 3,
		Cooldown:            10 * time.Second,
		SuccessThreshold:    2,
	}
	tracker := routing.NewHealthTracker(cfg)
	tracker.SetClock(clock.Now)

	providerID := "prov-openai"
	model := "gpt-4o"

	// Trip circuit to open
	for i := 0; i < 3; i++ {
		tracker.RecordFailure(providerID, model, "error")
	}
	if tracker.IsAvailable(providerID, model) {
		t.Fatal("circuit should be open")
	}

	// Advance time by 5s: cooldown not elapsed yet (cooldown is 10s)
	clock.Advance(5 * time.Second)
	if tracker.IsAvailable(providerID, model) {
		t.Fatal("circuit should still be blocked at 5s")
	}

	// Advance time past cooldown (total 11s)
	clock.Advance(6 * time.Second)

	// Now IsAvailable should admit a probe and transition to half-open
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected probe to be admitted after cooldown")
	}
	if st := tracker.GetState(providerID, model); st != routing.StateHalfOpen {
		t.Fatalf("want StateHalfOpen, got %v", st)
	}

	// While in half-open and probe is in-flight, second concurrent request should be denied
	if tracker.IsAvailable(providerID, model) {
		t.Fatal("expected second probe to be blocked while first probe is in-flight")
	}

	// Probe 1 succeeds
	tracker.RecordSuccess(providerID, model)
	// Still in half-open because SuccessThreshold is 2
	if st := tracker.GetState(providerID, model); st != routing.StateHalfOpen {
		t.Fatalf("want StateHalfOpen after 1 success, got %v", st)
	}

	// Probe 2 admitted
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected second probe to be admitted")
	}

	// Probe 2 succeeds -> circuit closes!
	tracker.RecordSuccess(providerID, model)
	if st := tracker.GetState(providerID, model); st != routing.StateClosed {
		t.Fatalf("want StateClosed after 2 consecutive successes, got %v", st)
	}
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected target to be fully available after recovery")
	}
}

func TestHealthTracker_HalfOpenProbeFailure(t *testing.T) {
	clock := newMockClock()
	cfg := routing.HealthConfig{
		ConsecutiveFailures: 2,
		Cooldown:            10 * time.Second,
		MaxCooldown:         60 * time.Second,
		SuccessThreshold:    2,
	}
	tracker := routing.NewHealthTracker(cfg)
	tracker.SetClock(clock.Now)

	providerID := "prov-unstable"
	model := "gpt-4o"

	// Trip circuit
	tracker.RecordFailure(providerID, model, "err 1")
	tracker.RecordFailure(providerID, model, "err 2")
	if tracker.GetState(providerID, model) != routing.StateOpen {
		t.Fatal("expected circuit to be open")
	}

	// Advance past 10s cooldown
	clock.Advance(11 * time.Second)
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected probe to be admitted")
	}
	if tracker.GetState(providerID, model) != routing.StateHalfOpen {
		t.Fatal("expected StateHalfOpen")
	}

	// Probe fails -> circuit re-opens with backoff cooldown (10s * 2 = 20s)
	tracker.RecordFailure(providerID, model, "probe failed")
	if st := tracker.GetState(providerID, model); st != routing.StateOpen {
		t.Fatalf("want StateOpen after probe failure, got %v", st)
	}

	// Advance 15s: still blocked because new cooldown is 20s!
	clock.Advance(15 * time.Second)
	if tracker.IsAvailable(providerID, model) {
		t.Fatal("expected circuit to remain open under exponential backoff (20s)")
	}

	// Advance another 6s (total 21s past second trip): now eligible for next probe
	clock.Advance(6 * time.Second)
	if !tracker.IsAvailable(providerID, model) {
		t.Fatal("expected second half-open probe after backoff cooldown")
	}
}

func TestHealthTracker_EventsAndSubscription(t *testing.T) {
	clock := newMockClock()
	cfg := routing.HealthConfig{
		ConsecutiveFailures: 2,
		Cooldown:            5 * time.Second,
		SuccessThreshold:    1,
	}
	tracker := routing.NewHealthTracker(cfg)
	tracker.SetClock(clock.Now)

	var recordedEvents []routing.HealthEvent
	var mu sync.Mutex

	unsub := tracker.Subscribe(func(ev routing.HealthEvent) {
		mu.Lock()
		defer mu.Unlock()
		recordedEvents = append(recordedEvents, ev)
	})
	defer unsub()

	// 1. Trip circuit: Closed -> Open
	tracker.RecordFailure("p1", "m1", "fail 1")
	tracker.RecordFailure("p1", "m1", "fail 2")

	// 2. Cooldown elapsed: Open -> HalfOpen
	clock.Advance(6 * time.Second)
	_ = tracker.IsAvailable("p1", "m1")

	// 3. Probe succeeds: HalfOpen -> Closed
	tracker.RecordSuccess("p1", "m1")

	mu.Lock()
	eventsCount := len(recordedEvents)
	mu.Unlock()

	if eventsCount != 3 {
		t.Fatalf("want 3 events, got %d: %+v", eventsCount, recordedEvents)
	}

	// Verify transitions
	if recordedEvents[0].OldState != routing.StateClosed || recordedEvents[0].NewState != routing.StateOpen {
		t.Errorf("event 0 mismatch: %+v", recordedEvents[0])
	}
	if recordedEvents[1].OldState != routing.StateOpen || recordedEvents[1].NewState != routing.StateHalfOpen {
		t.Errorf("event 1 mismatch: %+v", recordedEvents[1])
	}
	if recordedEvents[2].OldState != routing.StateHalfOpen || recordedEvents[2].NewState != routing.StateClosed {
		t.Errorf("event 2 mismatch: %+v", recordedEvents[2])
	}

	// Verify Events query returns newest first
	history := tracker.Events(10)
	if len(history) != 3 {
		t.Fatalf("want 3 history events, got %d", len(history))
	}
	if history[0].NewState != routing.StateClosed {
		t.Fatalf("history[0] should be newest (StateClosed), got %v", history[0].NewState)
	}
}

func TestHealthTracker_DecisionFunctionIntegration(t *testing.T) {
	// Verify that HealthTracker cleanly integrates with DecideTargets as HealthView
	providers := []domain.Provider{
		{ID: "primary", Enabled: true, Protocol: domain.ProtocolOpenAI},
		{ID: "backup", Enabled: true, Protocol: domain.ProtocolOpenAI},
	}
	models := []domain.Model{
		{
			ID: "gpt-4o",
			Targets: []domain.ModelTarget{
				{ProviderID: "primary", ProviderModel: "gpt-4o", Position: 0},
				{ProviderID: "backup", ProviderModel: "gpt-4o", Position: 1},
			},
		},
	}
	snap := routing.NewSnapshot(models, providers, nil)

	tracker := routing.NewHealthTracker(routing.DefaultHealthConfig())

	// Both healthy: primary first
	targets1, err := routing.DecideTargets(routing.RouteRequest{Model: "gpt-4o"}, snap, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets1) != 2 || targets1[0].ProviderID != "primary" {
		t.Fatalf("want primary first, got %+v", targets1)
	}

	// Trip primary circuit
	tracker.RecordFailure("primary", "gpt-4o", "error 1")
	tracker.RecordFailure("primary", "gpt-4o", "error 2")
	tracker.RecordFailure("primary", "gpt-4o", "error 3")

	// DecideTargets should now skip primary and choose backup
	targets2, err := routing.DecideTargets(routing.RouteRequest{Model: "gpt-4o"}, snap, tracker)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets2) != 1 || targets2[0].ProviderID != "backup" {
		t.Fatalf("want backup only, got %+v", targets2)
	}
}

func TestHealthTracker_Concurrency(t *testing.T) {
	tracker := routing.NewHealthTracker(routing.DefaultHealthConfig())

	const goroutines = 20
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Checkers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = tracker.IsAvailable("prov", "model")
				_ = tracker.GetState("prov", "model")
			}
		}()
	}

	// Reporters
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if (id+j)%3 == 0 {
					tracker.RecordFailure("prov", "model", "transient error")
				} else {
					tracker.RecordSuccess("prov", "model")
				}
			}
		}(i)
	}

	// Event log readers
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = tracker.Events(10)
			}
		}()
	}

	wg.Wait()
}
