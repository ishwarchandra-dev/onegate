package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/server"
)

type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeLogBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeLogBuffer) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []string
	raw := s.buf.String()
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestTraceContext_EndToEndLogCorrelation(t *testing.T) {
	logBuf := &safeLogBuffer{}
	logger := observability.NewLogger("debug", logBuf)

	router := server.New(server.Options{Logger: logger})

	// Register simulated gateway endpoint traversing multiple lifecycle steps
	router.Mux().HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		// 1. Ingest / Routing step
		logger.InfoContext(ctx, "routing decided", "model", "gpt-4o", "provider", "prov-openai")

		// 2. Upstream provider attempt
		logger.InfoContext(ctx, "upstream attempt", "attempt", 0, "status", 200)

		// 3. Quota / Usage debit step
		logger.InfoContext(ctx, "usage debited", "prompt_tokens", 10, "completion_tokens", 20)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	})

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	const clientTraceID = "client-trace-uuid-12345"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{}`))
	req.Header.Set("X-Request-Id", clientTraceID)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// 1. Header echo check
	if got := resp.Header.Get("X-Request-Id"); got != clientTraceID {
		t.Fatalf("expected echoed X-Request-Id %q, got %q", clientTraceID, got)
	}

	lines := logBuf.Lines()
	if len(lines) == 0 {
		t.Fatal("expected log entries, got none")
	}

	// 2. Verify EVERY log line emitted during this request carries the exact same trace_id
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not valid JSON: %s", i, line)
		}

		traceID, ok := rec["trace_id"].(string)
		if !ok || traceID != clientTraceID {
			t.Errorf("line %d: expected trace_id %q, got %v (log: %s)", i, clientTraceID, rec["trace_id"], line)
		}
	}
}

func TestTraceContext_GeneratedTraceIDSpansLogs(t *testing.T) {
	logBuf := &safeLogBuffer{}
	logger := observability.NewLogger("debug", logBuf)

	router := server.New(server.Options{Logger: logger})

	router.Mux().HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger.InfoContext(ctx, "processing call")
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	// Request without X-Request-Id header -> gateway must generate req-<hex>
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	genID := resp.Header.Get("X-Request-Id")
	if !strings.HasPrefix(genID, "req-") || len(genID) < 30 {
		t.Fatalf("expected valid generated req-* ID, got %q", genID)
	}

	lines := logBuf.Lines()
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 log lines (handler + access log), got %d", len(lines))
	}

	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d is not valid JSON: %s", i, line)
		}
		if rec["trace_id"] != genID {
			t.Errorf("line %d: expected generated trace_id %q, got %v", i, genID, rec["trace_id"])
		}
	}
}

func TestTraceContext_PanicRecoveryMaintainsTraceID(t *testing.T) {
	logBuf := &safeLogBuffer{}
	logger := observability.NewLogger("info", logBuf)

	router := server.New(server.Options{Logger: logger})

	router.Mux().HandleFunc("GET /panic", func(w http.ResponseWriter, req *http.Request) {
		panic("simulated test panic")
	})

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	const traceID = "panic-trace-67890"
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/panic", nil)
	req.Header.Set("X-Request-Id", traceID)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 status on panic, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Request-Id"); got != traceID {
		t.Fatalf("expected echoed X-Request-Id %q, got %q", traceID, got)
	}

	lines := logBuf.Lines()
	if len(lines) < 2 {
		t.Fatalf("expected panic error log + access log, got %d lines", len(lines))
	}

	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d invalid JSON: %s", i, line)
		}
		if rec["trace_id"] != traceID {
			t.Errorf("line %d: expected trace_id %q, got %v (log: %s)", i, traceID, rec["trace_id"], line)
		}
	}
}

func TestTraceContext_ConcurrentRequestsMaintainIsolatedTraceIDs(t *testing.T) {
	logBuf := &safeLogBuffer{}
	logger := observability.NewLogger("info", logBuf)

	router := server.New(server.Options{Logger: logger})

	router.Mux().HandleFunc("GET /work", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		logger.InfoContext(ctx, "worker processing")
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(router.Handler())
	defer srv.Close()

	const workers = 10
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func(workerID int) {
			defer wg.Done()
			reqTraceID := fmt.Sprintf("worker-trace-%02d", workerID)
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/work", nil)
			req.Header.Set("X-Request-Id", reqTraceID)

			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Errorf("worker %d failed: %v", workerID, err)
				return
			}
			defer resp.Body.Close()
			if got := resp.Header.Get("X-Request-Id"); got != reqTraceID {
				t.Errorf("worker %d expected header %q, got %q", workerID, reqTraceID, got)
			}
		}(i)
	}

	wg.Wait()

	// Every line in logBuf must have a valid worker-trace-XX ID
	lines := logBuf.Lines()
	if len(lines) < workers*2 {
		t.Fatalf("expected at least %d log lines, got %d", workers*2, len(lines))
	}

	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid json: %s", line)
		}
		tid, ok := rec["trace_id"].(string)
		if !ok || !strings.HasPrefix(tid, "worker-trace-") {
			t.Errorf("expected worker-trace-XX prefix, got %v in %s", rec["trace_id"], line)
		}
	}
}
