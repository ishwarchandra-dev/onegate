package ratelimit_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
)

func TestParseRetryAfter_Headers(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		headers   map[string]string
		body      string
		wantDelay time.Duration
		wantFound bool
	}{
		{
			name:      "Retry-After seconds integer",
			headers:   map[string]string{"Retry-After": "3"},
			wantDelay: 3 * time.Second,
			wantFound: true,
		},
		{
			name:      "Retry-After fractional seconds",
			headers:   map[string]string{"Retry-After": "1.5"},
			wantDelay: 1500 * time.Millisecond,
			wantFound: true,
		},
		{
			name:      "retry-after-ms header",
			headers:   map[string]string{"retry-after-ms": "750"},
			wantDelay: 750 * time.Millisecond,
			wantFound: true,
		},
		{
			name:      "x-ratelimit-reset-requests duration string",
			headers:   map[string]string{"x-ratelimit-reset-requests": "250ms"},
			wantDelay: 250 * time.Millisecond,
			wantFound: true,
		},
		{
			name:      "x-ratelimit-reset-tokens relative seconds",
			headers:   map[string]string{"x-ratelimit-reset-tokens": "5"},
			wantDelay: 5 * time.Second,
			wantFound: true,
		},
		{
			name: "Retry-After HTTP-Date format",
			headers: map[string]string{
				"Retry-After": "Sat, 03 Oct 2026 12:00:10 GMT",
			},
			wantDelay: 10 * time.Second,
			wantFound: true,
		},
		{
			name:      "Error body text fallback seconds",
			body:      `{"error": "Rate limit exceeded. Please try again in 2.5s."}`,
			wantDelay: 2500 * time.Millisecond,
			wantFound: true,
		},
		{
			name:      "Error body text fallback milliseconds",
			body:      `{"message": "Rate limit reached. Retry after 400ms"}`,
			wantDelay: 400 * time.Millisecond,
			wantFound: true,
		},
		{
			name:      "No retry hint",
			headers:   map[string]string{"Content-Type": "application/json"},
			body:      `{"error": "Internal Server Error"}`,
			wantDelay: 0,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			d, found := ratelimit.ParseRetryAfter(h, []byte(tt.body), now)
			if found != tt.wantFound {
				t.Fatalf("wantFound %v, got %v", tt.wantFound, found)
			}
			if found && d != tt.wantDelay {
				t.Fatalf("want delay %v, got %v", tt.wantDelay, d)
			}
		})
	}
}

func TestComputeBackoff_HonorsHeaderAndCapped(t *testing.T) {
	now := time.Now()
	cfg := ratelimit.BackoffConfig{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  3 * time.Second,
		Jitter:    true,
	}

	// 1. Explicit Retry-After header within max delay
	h1 := make(http.Header)
	h1.Set("Retry-After", "2")
	delay, fromHeader := ratelimit.ComputeBackoff(0, h1, nil, cfg, 42, now)
	if !fromHeader || delay != 2*time.Second {
		t.Fatalf("want 2s from header, got %v (fromHeader: %v)", delay, fromHeader)
	}

	// 2. Explicit Retry-After exceeding max delay is capped
	h2 := make(http.Header)
	h2.Set("Retry-After", "10")
	delay, fromHeader = ratelimit.ComputeBackoff(0, h2, nil, cfg, 42, now)
	if !fromHeader || delay != 3*time.Second {
		t.Fatalf("want capped 3s from header, got %v", delay)
	}

	// 3. No header -> exponential jittered backoff capped at MaxDelay
	cfgNoJitter := ratelimit.BackoffConfig{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  1 * time.Second,
		Jitter:    false,
	}
	d0, fromHeader := ratelimit.ComputeBackoff(0, nil, nil, cfgNoJitter, 0, now)
	if fromHeader || d0 != 100*time.Millisecond {
		t.Fatalf("attempt 0 without jitter: want 100ms, got %v", d0)
	}

	d1, _ := ratelimit.ComputeBackoff(1, nil, nil, cfgNoJitter, 0, now)
	if d1 != 200*time.Millisecond {
		t.Fatalf("attempt 1 without jitter: want 200ms, got %v", d1)
	}

	d5, _ := ratelimit.ComputeBackoff(5, nil, nil, cfgNoJitter, 0, now)
	// 100ms * 32 = 3.2s, capped at 1s
	if d5 != 1*time.Second {
		t.Fatalf("attempt 5 capped: want 1s, got %v", d5)
	}
}

func TestRespectsDeadline(t *testing.T) {
	// 1. Context without deadline always respects
	ctxNoDeadline := context.Background()
	if !ratelimit.RespectsDeadline(ctxNoDeadline, 10*time.Second) {
		t.Error("expected context without deadline to return true")
	}

	// 2. Context with sufficient deadline
	ctxLong, cancelLong := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLong()
	if !ratelimit.RespectsDeadline(ctxLong, 100*time.Millisecond) {
		t.Error("expected 100ms to respect 5s deadline")
	}

	// 3. Context with insufficient deadline
	ctxShort, cancelShort := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelShort()
	if ratelimit.RespectsDeadline(ctxShort, 500*time.Millisecond) {
		t.Error("expected 500ms delay to NOT respect 200ms deadline")
	}
}

func TestWait(t *testing.T) {
	// Cancel early
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ratelimit.Wait(ctx, 1*time.Second)
	if err == nil {
		t.Fatal("expected context cancelled error from Wait")
	}

	// Normal wait
	err = ratelimit.Wait(context.Background(), 5*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error from Wait: %v", err)
	}
}
