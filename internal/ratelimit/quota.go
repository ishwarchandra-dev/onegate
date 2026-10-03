// Package ratelimit quota management, concurrency limits, and 429 envelopes.
package ratelimit

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// RateLimitError represents a 429 rejection returned when a key quota is exceeded.
type RateLimitError struct {
	LimitType  string              // "rpm", "tpm", "concurrency", "spend"
	Message    string              // Human-readable rejection message
	RetryAfter int                 // Recommended backoff in seconds
	GErr       domain.GatewayError // Canonical error envelope
}

func (e *RateLimitError) Error() string {
	return e.Message
}

// NewRateLimitError constructs a RateLimitError and its canonical GatewayError envelope.
func NewRateLimitError(limitType string, message string, retryAfter int) *RateLimitError {
	if retryAfter < 1 {
		retryAfter = 1
	}
	return &RateLimitError{
		LimitType:  limitType,
		Message:    message,
		RetryAfter: retryAfter,
		GErr: domain.GatewayError{
			Status:    http.StatusTooManyRequests,
			Type:      domain.ErrRateLimit,
			Code:      limitType + "_limit_exceeded",
			Message:   message,
			Retryable: true,
		},
	}
}

// tokenBucket implements a thread-safe token bucket rate limiter over a 60-second window.
type tokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	fillRate   float64 // tokens per second
	lastRefill time.Time
}

func newTokenBucket(capacity int64, now time.Time) *tokenBucket {
	capF := float64(capacity)
	return &tokenBucket{
		capacity:   capF,
		tokens:     capF,
		fillRate:   capF / 60.0, // refill entire capacity over 1 minute
		lastRefill: now,
	}
}

func (b *tokenBucket) update(capacity int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	capF := float64(capacity)
	b.capacity = capF
	b.fillRate = capF / 60.0
	if b.tokens > capF {
		b.tokens = capF
	}
}

func (b *tokenBucket) tryConsume(n float64, now time.Time) (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+(elapsed*b.fillRate))
		b.lastRefill = now
	}

	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}

	// Calculate wait time until required tokens are replenished
	needed := n - b.tokens
	retryAfter := 1
	if b.fillRate > 0 {
		retryAfter = int(math.Ceil(needed / b.fillRate))
	}
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, retryAfter
}

func (b *tokenBucket) forceConsume(n float64, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+(elapsed*b.fillRate))
		b.lastRefill = now
	}

	b.tokens -= n
	if b.tokens < 0 {
		// allow temporary deficit, capped at negative capacity
		if b.tokens < -b.capacity {
			b.tokens = -b.capacity
		}
	}
}

// keyLimiter maintains rate limit and quota state for one virtual key.
type keyLimiter struct {
	inFlight    atomic.Int64
	spendMicros atomic.Int64

	mu         sync.Mutex
	rpmBucket  *tokenBucket
	tpmBucket  *tokenBucket
	lastRPMCap int64
	lastTPMCap int64
}

// QuotaManager coordinates rate limiting, concurrency gates, and spend caps across keys.
type QuotaManager struct {
	prices   *PriceTable
	limiters sync.Map // string -> *keyLimiter
	now      func() time.Time
}

// NewQuotaManager constructs a QuotaManager.
func NewQuotaManager(prices *PriceTable) *QuotaManager {
	if prices == nil {
		prices = NewPriceTable()
	}
	return &QuotaManager{
		prices: prices,
		now:    time.Now,
	}
}

// SetClock allows tests to inject a deterministic time provider.
func (qm *QuotaManager) SetClock(now func() time.Time) {
	qm.now = now
}

func (qm *QuotaManager) getOrCreateLimiter(keyID string) *keyLimiter {
	val, ok := qm.limiters.Load(keyID)
	if ok {
		return val.(*keyLimiter)
	}
	kl := &keyLimiter{}
	actual, _ := qm.limiters.LoadOrStore(keyID, kl)
	return actual.(*keyLimiter)
}

// Acquire evaluates all local limits for the key (Spend, Concurrency, RPM, TPM estimate).
// If all limits pass, it increments the in-flight concurrency counter and returns a
// release callback that must be deferred by the caller.
// If any limit is exceeded, it returns a *RateLimitError.
func (qm *QuotaManager) Acquire(key domain.VirtualKey, estimatedTokens int64) (release func(), err error) {
	kl := qm.getOrCreateLimiter(key.ID)
	now := qm.now()

	// 1. Spend cap check (MaxSpendUSDMicros)
	if key.Limits.MaxSpendUSDMicros > 0 {
		currentSpend := kl.spendMicros.Load()
		if currentSpend >= key.Limits.MaxSpendUSDMicros {
			return nil, NewRateLimitError(
				"spend",
				fmt.Sprintf("lifetime spend cap of $%0.2f reached (current spend: $%0.2f)",
					float64(key.Limits.MaxSpendUSDMicros)/1e6, float64(currentSpend)/1e6),
				60,
			)
		}
	}

	// 2. Concurrency limit check
	if key.Limits.Concurrency > 0 {
		for {
			cur := kl.inFlight.Load()
			if cur >= int64(key.Limits.Concurrency) {
				return nil, NewRateLimitError(
					"concurrency",
					fmt.Sprintf("concurrency limit of %d in-flight requests reached", key.Limits.Concurrency),
					1,
				)
			}
			if kl.inFlight.CompareAndSwap(cur, cur+1) {
				break
			}
		}
	} else {
		kl.inFlight.Add(1)
	}

	// Build release function to decrement in-flight counter exactly once
	var once sync.Once
	rel := func() {
		once.Do(func() {
			kl.inFlight.Add(-1)
		})
	}

	// 3. Requests Per Minute (RPM) limit check
	if key.Limits.RPM > 0 {
		kl.mu.Lock()
		if kl.rpmBucket == nil || kl.lastRPMCap != key.Limits.RPM {
			kl.rpmBucket = newTokenBucket(key.Limits.RPM, now)
			kl.lastRPMCap = key.Limits.RPM
		}
		rpmB := kl.rpmBucket
		kl.mu.Unlock()

		if allowed, retryAfter := rpmB.tryConsume(1, now); !allowed {
			rel()
			return nil, NewRateLimitError(
				"rpm",
				fmt.Sprintf("rate limit of %d requests per minute exceeded", key.Limits.RPM),
				retryAfter,
			)
		}
	}

	// 4. Tokens Per Minute (TPM) limit check (estimated prompt tokens)
	if key.Limits.TPM > 0 {
		kl.mu.Lock()
		if kl.tpmBucket == nil || kl.lastTPMCap != key.Limits.TPM {
			kl.tpmBucket = newTokenBucket(key.Limits.TPM, now)
			kl.lastTPMCap = key.Limits.TPM
		}
		tpmB := kl.tpmBucket
		kl.mu.Unlock()

		est := float64(estimatedTokens)
		if est < 1 {
			est = 1
		}
		if allowed, retryAfter := tpmB.tryConsume(est, now); !allowed {
			rel()
			return nil, NewRateLimitError(
				"tpm",
				fmt.Sprintf("token limit of %d tokens per minute exceeded", key.Limits.TPM),
				retryAfter,
			)
		}
	}

	return rel, nil
}

// DebitUsage calculates actual cost in micro-USD from the finalized usage event,
// debits the virtual key's spend counter, adjusts TPM consumption, and returns actual cost.
// It is guaranteed to be called exactly once per request.
func (qm *QuotaManager) DebitUsage(
	key domain.VirtualKey,
	model string,
	usage domain.TokenUsage,
	costMultiplier int,
) int64 {
	costMicros := qm.prices.CalculateCost(model, usage, costMultiplier)
	kl := qm.getOrCreateLimiter(key.ID)

	// Debit lifetime spend counter
	if costMicros > 0 {
		kl.spendMicros.Add(costMicros)
	}

	// Adjust TPM bucket with actual token usage
	if key.Limits.TPM > 0 && usage.TotalTokens > 0 {
		kl.mu.Lock()
		if kl.tpmBucket != nil {
			kl.tpmBucket.forceConsume(float64(usage.TotalTokens), qm.now())
		}
		kl.mu.Unlock()
	}

	return costMicros
}

// GetStats returns current in-flight and lifetime spend metrics for a key.
func (qm *QuotaManager) GetStats(keyID string) (inFlight int64, spendMicros int64) {
	val, ok := qm.limiters.Load(keyID)
	if !ok {
		return 0, 0
	}
	kl := val.(*keyLimiter)
	return kl.inFlight.Load(), kl.spendMicros.Load()
}

// SetSpend sets the initial or restored spend counter for a key (e.g. on startup from storage).
func (qm *QuotaManager) SetSpend(keyID string, spendMicros int64) {
	kl := qm.getOrCreateLimiter(keyID)
	kl.spendMicros.Store(spendMicros)
}
