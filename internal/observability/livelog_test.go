package observability_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

func TestRingBuffer_CapacityAndWrapping(t *testing.T) {
	ring := observability.NewRingBuffer(3)

	for i := 1; i <= 5; i++ {
		ring.Push(observability.LogEntry{
			Message: fmt.Sprintf("msg-%d", i),
		})
	}

	if ring.Total() != 3 {
		t.Fatalf("expected ring total 3, got %d", ring.Total())
	}

	recent := ring.Recent(10, observability.LogFilter{})
	if len(recent) != 3 {
		t.Fatalf("expected 3 recent entries, got %d", len(recent))
	}

	// Should contain msg-3, msg-4, msg-5
	expected := []string{"msg-3", "msg-4", "msg-5"}
	for i, exp := range expected {
		if recent[i].Message != exp {
			t.Errorf("idx %d: expected %s, got %s", i, exp, recent[i].Message)
		}
	}
}

func TestLogFilter_Matching(t *testing.T) {
	cases := []struct {
		name    string
		filter  observability.LogFilter
		entry   observability.LogEntry
		matches bool
	}{
		{
			name:    "level_exact_match",
			filter:  observability.LogFilter{MinLevel: "info"},
			entry:   observability.LogEntry{Level: "info"},
			matches: true,
		},
		{
			name:    "level_higher_passes",
			filter:  observability.LogFilter{MinLevel: "info"},
			entry:   observability.LogEntry{Level: "error"},
			matches: true,
		},
		{
			name:    "level_lower_rejected",
			filter:  observability.LogFilter{MinLevel: "warn"},
			entry:   observability.LogEntry{Level: "info"},
			matches: false,
		},
		{
			name:    "trace_id_matches",
			filter:  observability.LogFilter{TraceID: "req-123"},
			entry:   observability.LogEntry{TraceID: "req-123"},
			matches: true,
		},
		{
			name:    "trace_id_mismatch",
			filter:  observability.LogFilter{TraceID: "req-123"},
			entry:   observability.LogEntry{TraceID: "req-456"},
			matches: false,
		},
		{
			name:    "provider_matches",
			filter:  observability.LogFilter{Provider: "openai"},
			entry:   observability.LogEntry{Provider: "openai"},
			matches: true,
		},
		{
			name:    "provider_mismatch",
			filter:  observability.LogFilter{Provider: "openai"},
			entry:   observability.LogEntry{Provider: "anthropic"},
			matches: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.filter.Matches(tc.entry)
			if got != tc.matches {
				t.Fatalf("expected matches=%v, got %v", tc.matches, got)
			}
		})
	}
}

func TestLogHub_RedactionInheritedByFeed(t *testing.T) {
	hub := observability.NewLogHub(100)
	logger := observability.NewLoggerWithHub("debug", io.Discard, hub)

	// Acceptance: Redaction applies at the logging layer, inherited by the feed
	ctx := observability.WithTraceID(context.Background(), "req-sec-01")
	logger.InfoContext(ctx, "proxy attempt started",
		"provider", "openai-prod",
		"api_key", "sk-live-secret-should-never-leak-12345",
		"authorization", "Bearer super-secret-jwt",
		"password", "p@ssword",
		"safe_field", "user_123",
	)

	recent := hub.Ring().Recent(10, observability.LogFilter{})
	if len(recent) != 1 {
		t.Fatalf("expected 1 entry in ring, got %d", len(recent))
	}

	entry := recent[0]
	if entry.TraceID != "req-sec-01" {
		t.Errorf("expected trace ID req-sec-01, got %s", entry.TraceID)
	}
	if entry.Provider != "openai-prod" {
		t.Errorf("expected provider openai-prod, got %s", entry.Provider)
	}

	// Verify all sensitive attributes are masked
	attrs := entry.Attrs
	if attrs["api_key"] != "***REDACTED***" {
		t.Errorf("api_key not redacted in feed: %v", attrs["api_key"])
	}
	if attrs["authorization"] != "***REDACTED***" {
		t.Errorf("authorization not redacted in feed: %v", attrs["authorization"])
	}
	if attrs["password"] != "***REDACTED***" {
		t.Errorf("password not redacted in feed: %v", attrs["password"])
	}
	if attrs["safe_field"] != "user_123" {
		t.Errorf("safe_field corrupted: %v", attrs["safe_field"])
	}
}

func TestLogHub_SlowSubscriberDropAndNotify(t *testing.T) {
	hub := observability.NewLogHub(100)

	// Small buffer size of 2
	sub, unsubscribe := hub.Subscribe(observability.LogFilter{}, 2)
	defer unsubscribe()

	// Acceptance: Slow dashboard subscriber never backpressures the proxy (drops, notifies)
	// Publish 10 entries without reading from sub
	start := time.Now()
	for i := 0; i < 10; i++ {
		hub.Publish(observability.LogEntry{
			Message: fmt.Sprintf("burst-%d", i),
		})
	}
	dur := time.Since(start)
	if dur > 50*time.Millisecond {
		t.Errorf("Publish blocked on slow subscriber for %v (expected non-blocking)", dur)
	}

	// Sub should have 2 entries buffered, and 8 dropped
	if len(sub.Channel()) != 2 {
		t.Fatalf("expected 2 buffered items, got %d", len(sub.Channel()))
	}
	if sub.DroppedCount() != 8 {
		t.Fatalf("expected 8 drops recorded, got %d", sub.DroppedCount())
	}
}

func TestLogHub_SSEHandlerStreamAndHistory(t *testing.T) {
	hub := observability.NewLogHub(50)

	// Seed 5 historical entries in ring
	for i := 0; i < 5; i++ {
		hub.Publish(observability.LogEntry{
			Timestamp: time.Now(),
			Level:     "info",
			Message:   fmt.Sprintf("history-%d", i),
			TraceID:   "req-hist",
			Provider:  "openai",
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"?level=info&history=5", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET SSE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream Content-Type, got %s", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// Read initial history events
	receivedHistory := 0
	readDone := make(chan struct{})

	go func() {
		defer close(readDone)
		for receivedHistory < 5 {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "history-") {
				receivedHistory++
			}
		}
	}()

	select {
	case <-readDone:
		if receivedHistory != 5 {
			t.Fatalf("expected 5 history entries, got %d", receivedHistory)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout reading history events, read %d", receivedHistory)
	}

	// Now publish live log event and verify stream receives it
	liveDone := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "live-msg-42") {
				liveDone <- line
				return
			}
		}
	}()

	hub.Publish(observability.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "live-msg-42",
		TraceID:   "req-live",
	})

	select {
	case line := <-liveDone:
		if !strings.Contains(line, "live-msg-42") {
			t.Fatalf("unexpected line: %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for live log event")
	}
}

func TestLogHub_ConcurrentPublish(t *testing.T) {
	hub := observability.NewLogHub(500)
	sub, unsubscribe := hub.Subscribe(observability.LogFilter{}, 500)
	defer unsubscribe()

	var wg sync.WaitGroup
	const numWriters = 10
	const msgsPerWriter = 100

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for m := 0; m < msgsPerWriter; m++ {
				hub.Publish(observability.LogEntry{
					Message:  fmt.Sprintf("w-%d-%d", wid, m),
					Level:    "info",
					Provider: "openai",
				})
			}
		}(i)
	}

	wg.Wait()

	if hub.Ring().Total() != 500 {
		t.Fatalf("expected ring total 500, got %d", hub.Ring().Total())
	}
	// Drain subscription channel to verify no corruption under -race
	drained := 0
drain:
	for {
		select {
		case <-sub.Channel():
			drained++
		default:
			break drain
		}
	}
	if drained+int(sub.DroppedCount()) != numWriters*msgsPerWriter {
		t.Fatalf("total drained (%d) + dropped (%d) != %d", drained, sub.DroppedCount(), numWriters*msgsPerWriter)
	}
}
