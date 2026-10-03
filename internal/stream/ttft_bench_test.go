package stream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// mockStreamServer returns an HTTP test server streaming chunked SSE tokens.
func mockStreamServer(token string, count int, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher != nil {
			flusher.Flush()
		}

		for i := 0; i < count; i++ {
			if delay > 0 {
				time.Sleep(delay)
			}
			_, _ = w.Write([]byte("data: {\"id\":\"cmpl\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + token + "\"}}]}\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

// TestTTFTBudgetVsDirect verifies the Phase 3 gate criterion:
// "Streaming passthrough with TTFT overhead < 10ms vs direct (mock harness)".
func TestTTFTBudgetVsDirect(t *testing.T) {
	srv := mockStreamServer("hello", 5, 2*time.Millisecond)
	defer srv.Close()

	client := srv.Client()

	// 1. Measure direct baseline TTFT.
	var directTTFTs []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("direct get: %v", err)
		}
		buf := make([]byte, 256)
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			t.Fatalf("direct read: %v", err)
		}
		ttft := time.Since(start)
		_ = resp.Body.Close()
		if n > 0 {
			directTTFTs = append(directTTFTs, ttft)
		}
	}

	// 2. Measure pipeline TTFT (through Execute).
	var pipelineTTFTs []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("pipeline get: %v", err)
		}

		var (
			firstTokenTTFT time.Duration
			out            bytes.Buffer
		)
		w := NewCustomWriter(&out, nil)

		summary, err := Execute(context.Background(), Config{
			ProviderProto: domain.ProtocolOpenAI,
			ClientProto:   domain.ProtocolOpenAI,
			Upstream:      resp.Body,
			Destination:   w,
			OnEvent: func(_ domain.StreamEvent) {
				if firstTokenTTFT == 0 {
					firstTokenTTFT = time.Since(start)
				}
			},
		})
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("pipeline execute: %v", err)
		}
		if firstTokenTTFT > 0 {
			pipelineTTFTs = append(pipelineTTFTs, firstTokenTTFT)
		} else if summary.FirstTokenAt > 0 {
			pipelineTTFTs = append(pipelineTTFTs, summary.FirstTokenAt)
		}
	}

	var directSum, pipelineSum time.Duration
	for _, d := range directTTFTs {
		directSum += d
	}
	for _, p := range pipelineTTFTs {
		pipelineSum += p
	}

	avgDirect := directSum / time.Duration(len(directTTFTs))
	avgPipeline := pipelineSum / time.Duration(len(pipelineTTFTs))

	overhead := avgPipeline - avgDirect
	if overhead < 0 {
		overhead = 0 // network/scheduling jitter in pipeline's favor
	}

	t.Logf("TTFT Baseline (Direct): %v, Pipeline: %v, Overhead: %v (budget: 10ms)",
		avgDirect, avgPipeline, overhead)

	// Gate budget: < 10ms overhead vs direct mock.
	const budget = 10 * time.Millisecond
	if overhead > budget {
		t.Fatalf("TTFT overhead %v exceeded budget %v", overhead, budget)
	}
}

func BenchmarkTTFTPipeline(b *testing.B) {
	fixture := []byte("data: {\"id\":\"cmpl\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: [DONE]\n\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out bytes.Buffer
		w := NewCustomWriter(&out, nil)

		s, err := Execute(context.Background(), Config{
			ProviderProto: domain.ProtocolOpenAI,
			ClientProto:   domain.ProtocolOpenAI,
			Upstream:      bytes.NewReader(fixture),
			Destination:   w,
		})
		if err != nil {
			b.Fatal(err)
		}
		if s.FirstTokenAt == 0 {
			b.Fatal("first token at 0")
		}
	}
}

func BenchmarkTTFTDirect(b *testing.B) {
	fixture := []byte("data: {\"id\":\"cmpl\",\"object\":\"chat.completion.chunk\",\"created\":1770000000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: [DONE]\n\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(fixture)
		var out bytes.Buffer
		buf := make([]byte, 256)
		n, _ := r.Read(buf)
		_, _ = out.Write(buf[:n])
	}
}
