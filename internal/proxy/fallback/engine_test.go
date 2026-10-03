package fallback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
)

func TestFallbackBufferedSafeRetry(t *testing.T) {
	var target1Hits, target2Hits atomic.Int32

	// Target 1 returns 503 (safe/retryable error)
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"overloaded_error"}}`))
	}))
	defer srv1.Close()

	// Target 2 succeeds with 200 OK
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c2","object":"chat.completion","created":1770000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"fallback success"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`))
	}))
	defer srv2.Close()

	cli := client.New(client.TransportConfig{}, nil)
	targets := []Target{
		{Provider: client.Provider{ID: "p1", Protocol: domain.ProtocolOpenAI, BaseURL: srv1.URL, APIKey: "k1"}, Model: "gpt-4o"},
		{Provider: client.Provider{ID: "p2", Protocol: domain.ProtocolOpenAI, BaseURL: srv2.URL, APIKey: "k2"}, Model: "gpt-4o"},
	}

	var recordedTrace Trace
	engine := NewEngine(Config{
		Resolver: StaticResolver{Targets: targets},
		Client:   cli,
		OnTrace: func(tr Trace) {
			recordedTrace = tr
		},
	})

	rec := httptest.NewRecorder()
	call := ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Request: domain.Request{
			Model:    "gpt-4o",
			Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "hi"}}}},
		},
		Stream: false,
	}

	engine.Execute(context.Background(), rec, call)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if target1Hits.Load() != 1 {
		t.Errorf("target 1 hits: got %d, want 1", target1Hits.Load())
	}
	if target2Hits.Load() != 1 {
		t.Errorf("target 2 hits: got %d, want 1", target2Hits.Load())
	}

	if !strings.Contains(rec.Body.String(), "fallback success") {
		t.Errorf("missing fallback response: %s", rec.Body.String())
	}

	// Verify trace
	if len(recordedTrace.Attempts) != 2 {
		t.Fatalf("expected 2 attempts in trace, got %d", len(recordedTrace.Attempts))
	}
	if !recordedTrace.Attempts[0].Retried {
		t.Errorf("attempt 0 should be marked retried")
	}
	if recordedTrace.Attempts[0].ProviderID != "p1" {
		t.Errorf("attempt 0 provider: %s", recordedTrace.Attempts[0].ProviderID)
	}
	if recordedTrace.Attempts[1].Retried {
		t.Errorf("attempt 1 should not be marked retried")
	}
	if recordedTrace.Attempts[1].StatusCode != http.StatusOK {
		t.Errorf("attempt 1 status: %d", recordedTrace.Attempts[1].StatusCode)
	}
	if !recordedTrace.Completed {
		t.Errorf("trace should be marked completed")
	}
}

func TestFallbackBufferedUnsafeNoRetry(t *testing.T) {
	var target1Hits, target2Hits atomic.Int32

	// Target 1 returns 400 Bad Request (non-retryable!)
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid prompt","type":"invalid_request_error"}}`))
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target2Hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	cli := client.New(client.TransportConfig{}, nil)
	targets := []Target{
		{Provider: client.Provider{ID: "p1", Protocol: domain.ProtocolOpenAI, BaseURL: srv1.URL, APIKey: "k1"}, Model: "gpt-4o"},
		{Provider: client.Provider{ID: "p2", Protocol: domain.ProtocolOpenAI, BaseURL: srv2.URL, APIKey: "k2"}, Model: "gpt-4o"},
	}

	var recordedTrace Trace
	engine := NewEngine(Config{
		Resolver: StaticResolver{Targets: targets},
		Client:   cli,
		OnTrace: func(tr Trace) {
			recordedTrace = tr
		},
	})

	rec := httptest.NewRecorder()
	call := ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Request: domain.Request{
			Model:    "gpt-4o",
			Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "hi"}}}},
		},
		Stream: false,
	}

	engine.Execute(context.Background(), rec, call)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	if target1Hits.Load() != 1 {
		t.Errorf("target 1 hits: got %d, want 1", target1Hits.Load())
	}
	// Critical guarantee: Target 2 must NEVER be called on a 400 error!
	if target2Hits.Load() != 0 {
		t.Fatalf("target 2 was called despite non-retryable 400 error! hits=%d", target2Hits.Load())
	}

	if len(recordedTrace.Attempts) != 1 {
		t.Fatalf("expected exactly 1 attempt in trace, got %d", len(recordedTrace.Attempts))
	}
	if recordedTrace.Attempts[0].Retried {
		t.Errorf("attempt 0 should NOT be marked retried")
	}
}

func TestFallbackStreamSafeRetryBeforeFirstByte(t *testing.T) {
	var target1Hits, target2Hits atomic.Int32

	// Target 1 returns 502 Bad Gateway before any SSE frame is written
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway","type":"api_error"}}`))
	}))
	defer srv1.Close()

	// Target 2 succeeds with SSE stream
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target2Hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream fallback success\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv2.Close()

	cli := client.New(client.TransportConfig{}, nil)
	targets := []Target{
		{Provider: client.Provider{ID: "p1", Protocol: domain.ProtocolOpenAI, BaseURL: srv1.URL, APIKey: "k1"}, Model: "gpt-4o"},
		{Provider: client.Provider{ID: "p2", Protocol: domain.ProtocolOpenAI, BaseURL: srv2.URL, APIKey: "k2"}, Model: "gpt-4o"},
	}

	var recordedTrace Trace
	engine := NewEngine(Config{
		Resolver: StaticResolver{Targets: targets},
		Client:   cli,
		OnTrace: func(tr Trace) {
			recordedTrace = tr
		},
	})

	rec := httptest.NewRecorder()
	call := ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Request: domain.Request{
			Model:    "gpt-4o",
			Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "hi"}}}},
		},
		Stream: true,
	}

	engine.Execute(context.Background(), rec, call)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if target1Hits.Load() != 1 {
		t.Errorf("target 1 hits: got %d, want 1", target1Hits.Load())
	}
	if target2Hits.Load() != 1 {
		t.Errorf("target 2 hits: got %d, want 1", target2Hits.Load())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "stream fallback success") {
		t.Fatalf("missing stream fallback content in body: %s", body)
	}

	if len(recordedTrace.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(recordedTrace.Attempts))
	}
	if !recordedTrace.Attempts[0].Retried {
		t.Errorf("attempt 0 should be marked retried")
	}
}

func TestFallbackStreamNeverRetryAfterFirstByte(t *testing.T) {
	// ADR 003 acceptance criterion: "never after bytes emitted to client"
	var target1Hits, target2Hits atomic.Int32

	// Target 1 writes one token frame to client, then abruptly dies mid-stream
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target1Hits.Add(1)
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first token\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		// Abruptly terminate connection
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target2Hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	cli := client.New(client.TransportConfig{}, nil)
	targets := []Target{
		{Provider: client.Provider{ID: "p1", Protocol: domain.ProtocolOpenAI, BaseURL: srv1.URL, APIKey: "k1"}, Model: "gpt-4o"},
		{Provider: client.Provider{ID: "p2", Protocol: domain.ProtocolOpenAI, BaseURL: srv2.URL, APIKey: "k2"}, Model: "gpt-4o"},
	}

	var recordedTrace Trace
	engine := NewEngine(Config{
		Resolver: StaticResolver{Targets: targets},
		Client:   cli,
		OnTrace: func(tr Trace) {
			recordedTrace = tr
		},
	})

	rec := httptest.NewRecorder()
	call := ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Request: domain.Request{
			Model:    "gpt-4o",
			Messages: []domain.Message{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "hi"}}}},
		},
		Stream: true,
	}

	engine.Execute(context.Background(), rec, call)

	if target1Hits.Load() != 1 {
		t.Errorf("target 1 hits: got %d, want 1", target1Hits.Load())
	}
	// CRITICAL ADR 003 CHECK: Target 2 must NOT be tried because bytes already reached client!
	if target2Hits.Load() != 0 {
		t.Fatalf("target 2 was called after bytes were already emitted to client! hits=%d", target2Hits.Load())
	}

	if len(recordedTrace.Attempts) != 1 {
		t.Fatalf("expected exactly 1 attempt in trace, got %d", len(recordedTrace.Attempts))
	}
	if recordedTrace.Attempts[0].Retried {
		t.Errorf("attempt 0 should NOT be marked retried after first byte")
	}
}

func TestFallbackContextCancellation(t *testing.T) {
	var target1Hits atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target1Hits.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()

	cli := client.New(client.TransportConfig{}, nil)
	targets := []Target{
		{Provider: client.Provider{ID: "p1", Protocol: domain.ProtocolOpenAI, BaseURL: srv1.URL, APIKey: "k1"}, Model: "gpt-4o"},
	}

	engine := NewEngine(Config{
		Resolver: StaticResolver{Targets: targets},
		Client:   cli,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before execute

	rec := httptest.NewRecorder()
	engine.Execute(ctx, rec, ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Request:  domain.Request{Model: "gpt-4o"},
	})

	if target1Hits.Load() != 0 {
		t.Fatalf("target was hit despite canceled context")
	}
}
