// Package ratelimit provider 429 and rate limit backoff handling.
package ratelimit

import (
	"bytes"
	"context"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// BackoffConfig tunes provider retry backoff policy.
type BackoffConfig struct {
	// BaseDelay is the starting backoff duration when no Retry-After is supplied (default 100ms).
	BaseDelay time.Duration
	// MaxDelay caps the backoff duration (default 5s).
	MaxDelay time.Duration
	// Jitter enables full randomized jitter to prevent thundering herds (default true).
	Jitter bool
}

// DefaultBackoffConfig provides production defaults for provider retry backoff.
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  5 * time.Second,
		Jitter:    true,
	}
}

var (
	// regex to capture "try again in 2.5s" or "try again in 250ms" from error bodies
	retryAfterBodyRegex = regexp.MustCompile(`(?i)(?:try again in|retry after)\s+([0-9]+(?:\.[0-9]+)?)\s*(s|sec|seconds|ms|millis)?`)
)

// ParseRetryAfter attempts to extract rate-limit backoff duration from provider HTTP headers
// or response body.
func ParseRetryAfter(header http.Header, body []byte, now time.Time) (time.Duration, bool) {
	if header != nil {
		// 1. Standard Retry-After header (seconds or HTTP-date)
		if val := header.Get("Retry-After"); val != "" {
			// Integer seconds
			if secs, err := strconv.ParseFloat(val, 64); err == nil && secs >= 0 {
				return time.Duration(secs * float64(time.Second)), true
			}
			// HTTP-Date (RFC1123, etc.)
			if t, err := http.ParseTime(val); err == nil {
				diff := t.Sub(now)
				if diff < 0 {
					diff = 0
				}
				return diff, true
			}
		}

		// 2. retry-after-ms header (used by Groq and various LLM gateways)
		if val := header.Get("retry-after-ms"); val != "" {
			if ms, err := strconv.ParseInt(val, 10, 64); err == nil && ms >= 0 {
				return time.Duration(ms) * time.Millisecond, true
			}
		}

		// 3. x-ratelimit-reset-requests / x-ratelimit-reset-tokens duration or timestamp
		for _, h := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
			if val := header.Get(h); val != "" {
				if d, err := time.ParseDuration(val); err == nil && d >= 0 {
					return d, true
				}
				if secs, err := strconv.ParseFloat(val, 64); err == nil && secs >= 0 {
					// Check if timestamp in seconds or relative seconds
					if secs > 1_000_000_000 {
						diff := time.Unix(int64(secs), 0).Sub(now)
						if diff < 0 {
							diff = 0
						}
						return diff, true
					}
					return time.Duration(secs * float64(time.Second)), true
				}
			}
		}
	}

	// 4. Fallback: inspect response body error message for text hints
	if len(body) > 0 {
		match := retryAfterBodyRegex.FindSubmatch(body)
		if len(match) >= 2 {
			valStr := string(match[1])
			unitStr := "s"
			if len(match) >= 3 && len(match[2]) > 0 {
				unitStr = string(bytes.ToLower(match[2]))
			}

			if val, err := strconv.ParseFloat(valStr, 64); err == nil && val >= 0 {
				switch unitStr {
				case "ms", "millis":
					return time.Duration(val * float64(time.Millisecond)), true
				default:
					return time.Duration(val * float64(time.Second)), true
				}
			}
		}
	}

	return 0, false
}

// ComputeBackoff determines the delay before a retry attempt.
// If provider headers/body specify Retry-After, it honors it directly (capped by MaxDelay).
// Otherwise, it computes capped exponential backoff with full jitter.
func ComputeBackoff(
	attempt int,
	header http.Header,
	body []byte,
	cfg BackoffConfig,
	seed int64,
	now time.Time,
) (delay time.Duration, fromHeader bool) {
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 100 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 5 * time.Second
	}

	// Check if provider supplied an explicit Retry-After
	if retryAfter, ok := ParseRetryAfter(header, body, now); ok {
		if retryAfter > cfg.MaxDelay {
			retryAfter = cfg.MaxDelay
		}
		return retryAfter, true
	}

	// Exponential backoff: BaseDelay * 2^attempt
	multiplier := 1 << attempt
	if multiplier <= 0 || multiplier > 1024 {
		multiplier = 1024
	}
	maxCap := cfg.BaseDelay * time.Duration(multiplier)
	if maxCap > cfg.MaxDelay {
		maxCap = cfg.MaxDelay
	}

	if !cfg.Jitter {
		return maxCap, false
	}

	// Full jitter: uniform random in [0, maxCap]
	r := rand.New(rand.NewSource(seed + int64(attempt*1009)))
	jittered := time.Duration(r.Float64() * float64(maxCap))
	if jittered < cfg.BaseDelay/2 {
		jittered = cfg.BaseDelay / 2
	}
	return jittered, false
}

// RespectsDeadline reports whether waiting for delay would finish before the context's deadline.
// If the context has no deadline, it returns true.
func RespectsDeadline(ctx context.Context, delay time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	// Leave at least 50ms buffer for outbound request handling
	return time.Now().Add(delay + 50*time.Millisecond).Before(deadline)
}

// Wait blocks for the given delay, returning early with ctx.Err() if context cancels.
func Wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
