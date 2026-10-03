package ratelimit_test

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
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

func TestQuotaManager_ConcurrencyLimit(t *testing.T) {
	qm := ratelimit.NewQuotaManager(nil)
	key := domain.VirtualKey{
		ID: "vkey-concurrency",
		Limits: domain.KeyLimits{
			Concurrency: 2,
		},
	}

	// Request 1: succeeds
	rel1, err := qm.Acquire(key, 0)
	if err != nil {
		t.Fatalf("acquire 1 failed: %v", err)
	}

	// Request 2: succeeds
	rel2, err := qm.Acquire(key, 0)
	if err != nil {
		t.Fatalf("acquire 2 failed: %v", err)
	}

	// In-flight should be 2
	inFlight, _ := qm.GetStats(key.ID)
	if inFlight != 2 {
		t.Fatalf("want inFlight 2, got %d", inFlight)
	}

	// Request 3: rejected with 429
	_, err = qm.Acquire(key, 0)
	if err == nil {
		t.Fatal("expected 429 concurrency limit error, got nil")
	}
	var rle *ratelimit.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected *RateLimitError, got %T: %v", err, err)
	}
	if rle.LimitType != "concurrency" || rle.GErr.Status != http.StatusTooManyRequests {
		t.Fatalf("unexpected RateLimitError: %+v", rle)
	}

	// Release request 1
	rel1()
	// Duplicate release is a safe no-op
	rel1()

	inFlight, _ = qm.GetStats(key.ID)
	if inFlight != 1 {
		t.Fatalf("want inFlight 1 after release, got %d", inFlight)
	}

	// Request 4 can now acquire
	rel3, err := qm.Acquire(key, 0)
	if err != nil {
		t.Fatalf("acquire 3 failed after release: %v", err)
	}
	rel2()
	rel3()

	inFlight, _ = qm.GetStats(key.ID)
	if inFlight != 0 {
		t.Fatalf("want inFlight 0 after all released, got %d", inFlight)
	}
}

func TestQuotaManager_RPMLimit(t *testing.T) {
	clock := newMockClock()
	qm := ratelimit.NewQuotaManager(nil)
	qm.SetClock(clock.Now)

	key := domain.VirtualKey{
		ID: "vkey-rpm",
		Limits: domain.KeyLimits{
			RPM: 3, // 3 requests per minute
		},
	}

	// 3 requests within same window succeed
	for i := 0; i < 3; i++ {
		rel, err := qm.Acquire(key, 0)
		if err != nil {
			t.Fatalf("acquire %d failed: %v", i, err)
		}
		rel()
	}

	// 4th request trips RPM limit
	_, err := qm.Acquire(key, 0)
	if err == nil {
		t.Fatal("expected 429 RPM limit error")
	}
	var rle *ratelimit.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected RateLimitError, got %v", err)
	}
	if rle.LimitType != "rpm" || rle.RetryAfter <= 0 {
		t.Fatalf("unexpected rate limit error: %+v", rle)
	}

	// Advance clock by 25 seconds (tokens refill at 3 tokens / 60s = 0.05 tokens/s -> ~1.25 tokens)
	clock.Advance(25 * time.Second)

	// Now a request should succeed
	rel, err := qm.Acquire(key, 0)
	if err != nil {
		t.Fatalf("acquire failed after token refill: %v", err)
	}
	rel()
}

func TestQuotaManager_SpendCapAndDebit(t *testing.T) {
	prices := ratelimit.NewPriceTable()
	qm := ratelimit.NewQuotaManager(prices)

	key := domain.VirtualKey{
		ID: "vkey-budget",
		Limits: domain.KeyLimits{
			MaxSpendUSDMicros: 10_000, // $0.01 lifetime cap (10,000 micros)
		},
	}

	// Initial spend is 0
	_, spend := qm.GetStats(key.ID)
	if spend != 0 {
		t.Fatalf("want initial spend 0, got %d", spend)
	}

	// Request 1: acquires successfully
	rel, err := qm.Acquire(key, 100)
	if err != nil {
		t.Fatalf("acquire 1 failed: %v", err)
	}
	rel()

	// Finalized request 1: debits 6,000 micros on gpt-4o-mini
	// Input: 10,000 tokens * 150_000 / 1M = 1,500 micros
	// Output: 5,000 tokens * 600_000 / 1M = 3,000 micros
	// Cost = 4,500 micros
	cost1 := qm.DebitUsage(key, "gpt-4o-mini", domain.TokenUsage{
		InputTokens:  10000,
		OutputTokens: 5000,
		TotalTokens:  15000,
	}, 100)
	if cost1 != 4500 {
		t.Fatalf("want cost 4500, got %d", cost1)
	}

	_, spend = qm.GetStats(key.ID)
	if spend != 4500 {
		t.Fatalf("want spend 4500, got %d", spend)
	}

	// Request 2: acquires successfully (4500 < 10000)
	rel2, err := qm.Acquire(key, 100)
	if err != nil {
		t.Fatalf("acquire 2 failed: %v", err)
	}
	rel2()

	// Finalized request 2: debits another 6,000 micros
	// Total spend reaches 4,500 + 6,000 = 10,500 micros (exceeds 10,000)
	cost2 := qm.DebitUsage(key, "gpt-4o", domain.TokenUsage{
		InputTokens:  2000,
		OutputTokens: 100,
		TotalTokens:  2100,
	}, 100)
	if cost2 != 6000 {
		t.Fatalf("want cost 6000, got %d", cost2)
	}

	_, spend = qm.GetStats(key.ID)
	if spend != 10500 {
		t.Fatalf("want spend 10500, got %d", spend)
	}

	// Request 3: hard-stop rejected because spend (10,500) >= MaxSpendUSDMicros (10,000)
	_, err = qm.Acquire(key, 100)
	if err == nil {
		t.Fatal("expected 429 spend cap error, got nil")
	}
	var rle *ratelimit.RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected RateLimitError, got %v", err)
	}
	if rle.LimitType != "spend" || rle.GErr.Status != http.StatusTooManyRequests {
		t.Fatalf("unexpected rate limit error: %+v", rle)
	}
}

func TestQuotaManager_ConcurrencyAndRaces(t *testing.T) {
	qm := ratelimit.NewQuotaManager(nil)
	key := domain.VirtualKey{
		ID: "vkey-race",
		Limits: domain.KeyLimits{
			Concurrency:       5,
			RPM:               10000,
			TPM:               1000000,
			MaxSpendUSDMicros: 10_000_000,
		},
	}

	const workers = 20
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(workers)

	var successfulRequests sync.WaitGroup
	var completedDebits sync.WaitGroup

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				rel, err := qm.Acquire(key, 50)
				if err == nil {
					// Simulate small execution
					time.Sleep(100 * time.Microsecond)
					rel()
					_ = qm.DebitUsage(key, "gpt-4o", domain.TokenUsage{
						InputTokens:  10,
						OutputTokens: 5,
						TotalTokens:  15,
					}, 100)
				}
			}
		}(i)
	}

	wg.Wait()
	_ = successfulRequests
	_ = completedDebits

	inFlight, spend := qm.GetStats(key.ID)
	if inFlight != 0 {
		t.Fatalf("expected all in-flight requests released, got %d", inFlight)
	}
	if spend <= 0 {
		t.Fatalf("expected positive accumulated spend, got %d", spend)
	}
}
