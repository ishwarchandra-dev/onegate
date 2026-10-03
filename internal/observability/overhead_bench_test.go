package observability_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

// BenchmarkProxyHotPath_NoObservability simulates the proxy termination hooks
// without any observability processing (baseline).
func BenchmarkProxyHotPath_NoObservability(b *testing.B) {
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate minimal proxy request teardown
		_ = ctx
		duration := 15 * time.Millisecond
		_ = duration
	}
}

// BenchmarkProxyHotPath_WithObservability simulates full observability on request teardown:
// trace extraction, usage event queueing, metrics observation (counter + latency histogram),
// and pipeline stats gauge updates.
func BenchmarkProxyHotPath_WithObservability(b *testing.B) {
	reg := observability.NewRegistry(observability.MetricsConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipeline := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     100_000,
		BatchSize:     500,
		FlushInterval: 10 * time.Millisecond,
		Writer: observability.BatchWriterFunc(func(records []domain.RequestRecord) error {
			return nil
		}),
		Logger: slog.New(slog.DiscardHandler),
	})
	pipeline.Start(ctx)

	traceCtx := observability.WithTraceID(context.Background(), "req-bench-trace-12345")

	event := observability.Event{
		CallID:           observability.TraceID(traceCtx),
		Protocol:         "openai",
		VirtualKeyID:     "vkey-123",
		ModelRequested:   "gpt-4o",
		ModelServed:      "gpt-4o",
		ProviderID:       "openai-primary",
		Status:           domain.RequestSuccess,
		Stream:           true,
		PromptTokens:     120,
		CompletionTokens: 80,
		TotalTokens:      200,
		Duration:         25 * time.Millisecond,
		TTFT:             8 * time.Millisecond,
		Attempts:         1,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 1. Trace ID resolution
		_ = observability.TraceID(traceCtx)

		// 2. Usage pipeline enqueue (drop-and-count bounded queue)
		pipeline.EnqueueEvent(event)

		// 3. Prometheus metrics observation (onereq_total + onereq_latency_seconds)
		reg.ObserveProxyRequest("openai", "openai-primary", "gpt-4o", "success", event.Duration)

		// 4. TTFT observation for streaming
		reg.ObserveTTFT("openai-primary", "gpt-4o", event.TTFT)

		// 5. Usage pipeline stats sync
		reg.SetUsagePipelineStats(pipeline.Stats())
	}
}

// BenchmarkUsagePipeline_Enqueue measures the overhead of EnqueueEvent in isolation.
func BenchmarkUsagePipeline_Enqueue(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipeline := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     100_000,
		BatchSize:     500,
		FlushInterval: 10 * time.Millisecond,
		Writer: observability.BatchWriterFunc(func(records []domain.RequestRecord) error {
			return nil
		}),
		Logger: slog.New(slog.DiscardHandler),
	})
	pipeline.Start(ctx)

	ev := observability.Event{
		CallID:       "req-trace-1",
		Protocol:     "openai",
		VirtualKeyID: "vk-1",
		ProviderID:   "openai-primary",
		ModelServed:  "gpt-4o",
		Status:       domain.RequestSuccess,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pipeline.EnqueueEvent(ev)
	}
}

// BenchmarkMetrics_ObserveProxyRequest measures metrics observation overhead.
func BenchmarkMetrics_ObserveProxyRequest(b *testing.B) {
	reg := observability.NewRegistry(observability.MetricsConfig{})
	dur := 15 * time.Millisecond

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg.ObserveProxyRequest("openai", "openai-primary", "gpt-4o", "success", dur)
	}
}

// BenchmarkMetrics_ObserveTTFT measures TTFT histogram observation overhead.
func BenchmarkMetrics_ObserveTTFT(b *testing.B) {
	reg := observability.NewRegistry(observability.MetricsConfig{})
	ttft := 10 * time.Millisecond

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg.ObserveTTFT("openai-primary", "gpt-4o", ttft)
	}
}

// BenchmarkLogHub_Publish measures the broadcast overhead of the live log ring buffer.
func BenchmarkLogHub_Publish(b *testing.B) {
	hub := observability.NewLogHub(1000)
	entry := observability.LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "upstream response received",
		TraceID:   "req-trace-123",
		Provider:  "openai",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Publish(entry)
	}
}

// BenchmarkTraceContext_Propagation measures context trace injection and retrieval.
func BenchmarkTraceContext_Propagation(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := observability.WithTraceID(context.Background(), "req-trace-12345")
		_ = observability.TraceID(ctx)
	}
}

// BenchmarkConcurrentObservability evaluates contention under parallel proxy workers.
func BenchmarkConcurrentObservability(b *testing.B) {
	reg := observability.NewRegistry(observability.MetricsConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipeline := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     500_000,
		BatchSize:     500,
		FlushInterval: 10 * time.Millisecond,
		Writer: observability.BatchWriterFunc(func(records []domain.RequestRecord) error {
			return nil
		}),
		Logger: slog.New(slog.DiscardHandler),
	})
	pipeline.Start(ctx)

	event := observability.Event{
		CallID:       "req-bench",
		Protocol:     "openai",
		VirtualKeyID: "vkey-123",
		ModelServed:  "gpt-4o",
		ProviderID:   "openai-primary",
		Status:       domain.RequestSuccess,
		Duration:     20 * time.Millisecond,
		TTFT:         5 * time.Millisecond,
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		traceCtx := observability.WithTraceID(context.Background(), "req-trace-worker")
		for pb.Next() {
			_ = observability.TraceID(traceCtx)
			pipeline.EnqueueEvent(event)
			reg.ObserveProxyRequest("openai", "openai-primary", "gpt-4o", "success", event.Duration)
			reg.ObserveTTFT("openai-primary", "gpt-4o", event.TTFT)
		}
	})
}
