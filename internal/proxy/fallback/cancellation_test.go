package fallback_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/fallback"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
)

func TestCancellation_StreamMidStreamDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var (
		providerCtxCancelled atomic.Bool
		providerDone         = make(chan struct{})
		firstChunkSent       = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("expected flusher")
			return
		}

		// Emit initial chunk
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n")
		flusher.Flush()
		close(firstChunkSent)

		// Wait for client cancellation or timeout
		select {
		case <-r.Context().Done():
			providerCtxCancelled.Store(true)
			close(providerDone)
		case <-time.After(2 * time.Second):
			t.Errorf("provider did not observe cancellation in time")
		}
	}))
	defer server.Close()

	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()

	var (
		usageCaptured  fallback.UsageEvent
		usageFinalized = make(chan struct{})
		traceCaptured  fallback.Trace
		traceFinalized = make(chan struct{})
	)

	engine := fallback.NewEngine(fallback.Config{
		Resolver: fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-openai",
						Protocol: domain.ProtocolOpenAI,
						BaseURL:  server.URL,
						APIKey:   "sk-test",
					},
					Model: "gpt-4o",
				},
			},
		},
		Client: cli,
		OnUsage: func(u fallback.UsageEvent) {
			usageCaptured = u
			close(usageFinalized)
		},
		OnTrace: func(tr fallback.Trace) {
			traceCaptured = tr
			close(traceFinalized)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := httptest.NewRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		engine.Execute(ctx, rec, ingest.Call{
			Protocol: domain.ProtocolOpenAI,
			Stream:   true,
			Request: domain.Request{
				Model: "gpt-4o",
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Hi"}}},
				},
			},
		})
	}()

	// Wait for the first chunk to be emitted by provider
	select {
	case <-firstChunkSent:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first chunk")
	}

	// Wait brief moment for proxy to flush to client, then disconnect client
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine.Execute did not return promptly upon cancel")
	}

	select {
	case <-usageFinalized:
	case <-time.After(time.Second):
		t.Fatal("usage was not finalized on cancellation")
	}

	select {
	case <-traceFinalized:
	case <-time.After(time.Second):
		t.Fatal("trace was not finalized on cancellation")
	}

	select {
	case <-providerDone:
	case <-time.After(time.Second):
		t.Error("expected provider request context to be cancelled when client disconnected")
	}

	if !usageCaptured.Cancelled {
		t.Errorf("expected usageCaptured.Cancelled == true, got false")
	}
	if usageCaptured.Status != domain.RequestCancelled {
		t.Errorf("expected usage status 'cancelled', got %s", usageCaptured.Status)
	}
	if usageCaptured.Usage.TotalTokens != 15 {
		t.Errorf("expected 15 tokens captured from partial stream, got %d", usageCaptured.Usage.TotalTokens)
	}
	if len(traceCaptured.Attempts) != 1 {
		t.Errorf("expected 1 attempt in trace, got %d", len(traceCaptured.Attempts))
	}
}

func TestCancellation_StreamPreFirstByteDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var (
		providerCtxCancelled atomic.Bool
		providerDone         = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			providerCtxCancelled.Store(true)
			close(providerDone)
		case <-time.After(2 * time.Second):
			t.Errorf("provider did not observe cancellation")
		}
	}))
	defer server.Close()

	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()

	var usageCaptured fallback.UsageEvent
	usageFinalized := make(chan struct{})

	engine := fallback.NewEngine(fallback.Config{
		Resolver: fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-openai",
						Protocol: domain.ProtocolOpenAI,
						BaseURL:  server.URL,
						APIKey:   "sk-test",
					},
					Model: "gpt-4o",
				},
			},
		},
		Client: cli,
		OnUsage: func(u fallback.UsageEvent) {
			usageCaptured = u
			close(usageFinalized)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		engine.Execute(ctx, rec, ingest.Call{
			Protocol: domain.ProtocolOpenAI,
			Stream:   true,
			Request: domain.Request{
				Model: "gpt-4o",
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Hi"}}},
				},
			},
		})
	}()

	// Disconnect client before provider sends headers
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine.Execute did not return promptly upon cancel")
	}

	select {
	case <-usageFinalized:
	case <-time.After(time.Second):
		t.Fatal("usage was not finalized on cancellation")
	}

	select {
	case <-providerDone:
	case <-time.After(time.Second):
		t.Error("expected provider request context to be cancelled")
	}

	if !usageCaptured.Cancelled {
		t.Errorf("expected usageCaptured.Cancelled == true")
	}
	if usageCaptured.Status != domain.RequestCancelled {
		t.Errorf("expected status 'cancelled', got %s", usageCaptured.Status)
	}
}

func TestCancellation_BufferedDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var (
		providerCtxCancelled atomic.Bool
		providerDone         = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			providerCtxCancelled.Store(true)
			close(providerDone)
		case <-time.After(2 * time.Second):
			t.Errorf("provider did not observe cancellation")
		}
	}))
	defer server.Close()

	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()

	var usageCaptured fallback.UsageEvent
	usageFinalized := make(chan struct{})

	engine := fallback.NewEngine(fallback.Config{
		Resolver: fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-openai",
						Protocol: domain.ProtocolOpenAI,
						BaseURL:  server.URL,
						APIKey:   "sk-test",
					},
					Model: "gpt-4o",
				},
			},
		},
		Client: cli,
		OnUsage: func(u fallback.UsageEvent) {
			usageCaptured = u
			close(usageFinalized)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		engine.Execute(ctx, rec, ingest.Call{
			Protocol: domain.ProtocolOpenAI,
			Stream:   false,
			Request: domain.Request{
				Model: "gpt-4o",
				Messages: []domain.Message{
					{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Hi"}}},
				},
			},
		})
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("engine.Execute did not return promptly upon cancel")
	}

	select {
	case <-usageFinalized:
	case <-time.After(time.Second):
		t.Fatal("usage was not finalized on cancellation")
	}

	select {
	case <-providerDone:
	case <-time.After(time.Second):
		t.Error("expected provider request context to be cancelled")
	}

	if !usageCaptured.Cancelled {
		t.Errorf("expected usageCaptured.Cancelled == true")
	}
	if usageCaptured.Status != domain.RequestCancelled {
		t.Errorf("expected status 'cancelled', got %s", usageCaptured.Status)
	}
}

func TestCancellation_TimeoutScenario(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var (
		providerCtxCancelled atomic.Bool
		providerDone         = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			providerCtxCancelled.Store(true)
			close(providerDone)
		case <-time.After(2 * time.Second):
			t.Errorf("provider did not observe cancellation")
		}
	}))
	defer server.Close()

	cli := client.New(client.TransportConfig{}, nil)
	defer cli.CloseIdleConnections()

	var usageCaptured fallback.UsageEvent
	usageFinalized := make(chan struct{})

	engine := fallback.NewEngine(fallback.Config{
		Resolver: fallback.StaticResolver{
			Targets: []fallback.Target{
				{
					Provider: client.Provider{
						ID:       "mock-openai",
						Protocol: domain.ProtocolOpenAI,
						BaseURL:  server.URL,
						APIKey:   "sk-test",
					},
					Model: "gpt-4o",
				},
			},
		},
		Client: cli,
		OnUsage: func(u fallback.UsageEvent) {
			usageCaptured = u
			close(usageFinalized)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rec := httptest.NewRecorder()
	engine.Execute(ctx, rec, ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Stream:   false,
		Request: domain.Request{
			Model: "gpt-4o",
			Messages: []domain.Message{
				{Role: domain.RoleUser, Content: []domain.ContentBlock{{Type: domain.BlockText, Text: "Hi"}}},
			},
		},
	})

	select {
	case <-usageFinalized:
	case <-time.After(time.Second):
		t.Fatal("usage was not finalized on timeout")
	}

	select {
	case <-providerDone:
	case <-time.After(time.Second):
		t.Error("expected provider request context to be cancelled on timeout")
	}

	if !usageCaptured.Cancelled {
		t.Errorf("expected usageCaptured.Cancelled == true")
	}
	if usageCaptured.Status != domain.RequestCancelled {
		t.Errorf("expected status 'cancelled', got %s", usageCaptured.Status)
	}
}

type panickingResolver struct{}

func (panickingResolver) ResolveTargets(_ context.Context, _ ingest.Call) ([]fallback.Target, error) {
	panic("unexpected resolver crash")
}

func TestCancellation_PanicClean(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	engine := fallback.NewEngine(fallback.Config{
		Resolver: panickingResolver{},
	})

	rec := httptest.NewRecorder()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from panickingResolver")
		}
	}()

	engine.Execute(context.Background(), rec, ingest.Call{
		Protocol: domain.ProtocolOpenAI,
		Request:  domain.Request{Model: "gpt-4o"},
	})
}
