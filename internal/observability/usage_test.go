package observability_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/ratelimit"
	"github.com/ishwarchandra-dev/onegate/internal/storage"
)

// mockBatchWriter records batches written to it.
type mockBatchWriter struct {
	mu      sync.Mutex
	batches [][]domain.RequestRecord
	records []domain.RequestRecord
	err     error
	delay   time.Duration
}

func (m *mockBatchWriter) InsertBatch(records []domain.RequestRecord) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	cpy := make([]domain.RequestRecord, len(records))
	copy(cpy, records)
	m.batches = append(m.batches, cpy)
	m.records = append(m.records, cpy...)
	return nil
}

func (m *mockBatchWriter) totalRecords() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

func TestUsagePipeline_Enrichment(t *testing.T) {
	fixedTime := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	prices := ratelimit.NewPriceTable()

	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		Prices: prices,
		Clock:  func() time.Time { return fixedTime },
	})

	ev := observability.Event{
		CallID:           "trace-123",
		VirtualKeyID:     "vkey-abc",
		ModelRequested:   "gpt-4o",
		ModelServed:      "gpt-4o",
		ProviderID:       "openai",
		Status:           domain.RequestSuccess,
		Stream:           true,
		PromptTokens:     100,
		CompletionTokens: 50,
		Duration:         250 * time.Millisecond,
		TTFT:             45 * time.Millisecond,
		Attempts:         1,
	}

	rec := pipe.Enrich(ev)

	if rec.ID != "trace-123" || rec.TraceID != "trace-123" {
		t.Fatalf("unexpected IDs: id=%s trace_id=%s", rec.ID, rec.TraceID)
	}
	if rec.VirtualKeyID != "vkey-abc" {
		t.Fatalf("unexpected vkey: %s", rec.VirtualKeyID)
	}
	if rec.CreatedMS != fixedTime.UnixMilli() {
		t.Fatalf("unexpected created_ms: %d", rec.CreatedMS)
	}
	if rec.TotalTokens != 150 {
		t.Fatalf("expected total tokens 150, got %d", rec.TotalTokens)
	}
	if rec.LatencyMS != 250 {
		t.Fatalf("expected latency 250, got %d", rec.LatencyMS)
	}
	if rec.TTFTMS != 45 {
		t.Fatalf("expected ttft 45, got %d", rec.TTFTMS)
	}
	if rec.CostUSDMicros <= 0 {
		t.Fatalf("expected calculated cost > 0, got %d", rec.CostUSDMicros)
	}
	if rec.Status != domain.RequestSuccess {
		t.Fatalf("expected status success, got %s", rec.Status)
	}
}

func TestUsagePipeline_BatchWriteAndFlush(t *testing.T) {
	mockWriter := &mockBatchWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     100,
		BatchSize:     5,
		FlushInterval: 1 * time.Hour, // don't flush via timer in this test
		Writer:        mockWriter,
	})
	pipe.Start(ctx)
	defer func() { _ = pipe.Stop() }()

	// Enqueue 12 events
	for i := 0; i < 12; i++ {
		ok := pipe.EnqueueEvent(observability.Event{
			CallID:         fmt.Sprintf("req-%d", i),
			ModelRequested: "gpt-4o",
			Status:         domain.RequestSuccess,
		})
		if !ok {
			t.Fatalf("failed to enqueue event %d", i)
		}
	}

	// 10 items should have been written in 2 batches of 5
	// Give worker a moment to process the second batch
	pipe.Flush()

	if mockWriter.totalRecords() != 12 {
		t.Fatalf("expected 12 records written, got %d", mockWriter.totalRecords())
	}

	stats := pipe.Stats()
	if stats.Captured != 12 || stats.Enqueued != 12 || stats.Written != 12 || stats.Dropped != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestUsagePipeline_FlushInterval(t *testing.T) {
	mockWriter := &mockBatchWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     100,
		BatchSize:     100, // won't trigger batch size
		FlushInterval: 20 * time.Millisecond,
		Writer:        mockWriter,
	})
	pipe.Start(ctx)
	defer func() { _ = pipe.Stop() }()

	pipe.Enqueue(domain.RequestRecord{
		ID:             "flush-req-1",
		TraceID:        "flush-req-1",
		ModelRequested: "gpt-4o",
		Status:         domain.RequestSuccess,
	})

	// Wait for periodic ticker to flush
	time.Sleep(100 * time.Millisecond)

	if mockWriter.totalRecords() != 1 {
		t.Fatalf("expected 1 record flushed by interval, got %d", mockWriter.totalRecords())
	}
}

func TestUsagePipeline_DropAndCountUnderLoad(t *testing.T) {
	// Artificially slow writer to simulate persistent backlog
	mockWriter := &mockBatchWriter{delay: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Small queue size to trigger overflows
	const queueSize = 20
	const batchSize = 10
	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     queueSize,
		BatchSize:     batchSize,
		FlushInterval: 10 * time.Millisecond,
		Writer:        mockWriter,
		Logger:        slog.New(slog.DiscardHandler),
	})
	pipe.Start(ctx)

	const totalRequests = 1000
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	// Fire 10 concurrent goroutines spamming requests
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			<-startCh
			for i := 0; i < totalRequests/10; i++ {
				// Proxy path calls Enqueue — verify it never blocks!
				start := time.Now()
				pipe.EnqueueEvent(observability.Event{
					CallID:         fmt.Sprintf("load-%d-%d", gid, i),
					ModelRequested: "gpt-4o",
					Status:         domain.RequestSuccess,
				})
				dur := time.Since(start)
				if dur > 50*time.Millisecond {
					t.Errorf("Enqueue blocked for %v (expected non-blocking)", dur)
				}
			}
		}(g)
	}

	close(startCh)
	wg.Wait()

	// Stop pipeline to drain all enqueued items
	if err := pipe.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	stats := pipe.Stats()

	// Reconciliation checks:
	// 1. Total captured must equal Enqueued + Dropped
	if stats.Captured != totalRequests {
		t.Fatalf("expected %d captured, got %d", totalRequests, stats.Captured)
	}
	if stats.Captured != stats.Enqueued+stats.Dropped {
		t.Fatalf("count mismatch: captured (%d) != enqueued (%d) + dropped (%d)",
			stats.Captured, stats.Enqueued, stats.Dropped)
	}

	// 2. All enqueued items must be written
	if stats.Enqueued != stats.Written {
		t.Fatalf("enqueued (%d) != written (%d)", stats.Enqueued, stats.Written)
	}

	// 3. Since queue was 20 and writer was delayed, drops MUST have occurred
	if stats.Dropped <= 0 {
		t.Fatalf("expected drops under load with slow writer, got %d", stats.Dropped)
	}

	// 4. Total written in mockWriter must equal stats.Written
	if int64(mockWriter.totalRecords()) != stats.Written {
		t.Fatalf("mock writer records (%d) != stats.Written (%d)",
			mockWriter.totalRecords(), stats.Written)
	}
}

func TestUsagePipeline_StorageFailureResilience(t *testing.T) {
	mockWriter := &mockBatchWriter{err: errors.New("database disk full / locked")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     50,
		BatchSize:     5,
		FlushInterval: 10 * time.Millisecond,
		Writer:        mockWriter,
		Logger:        slog.New(slog.DiscardHandler),
	})
	pipe.Start(ctx)
	defer func() { _ = pipe.Stop() }()

	for i := 0; i < 10; i++ {
		pipe.Enqueue(domain.RequestRecord{
			ID:             fmt.Sprintf("err-%d", i),
			TraceID:        fmt.Sprintf("err-%d", i),
			ModelRequested: "gpt-4o",
			Status:         domain.RequestSuccess,
		})
	}

	pipe.Flush()

	stats := pipe.Stats()
	if stats.Written != 0 {
		t.Fatalf("expected 0 written on error, got %d", stats.Written)
	}
	if stats.Errors != 10 {
		t.Fatalf("expected 10 errors counted, got %d", stats.Errors)
	}
}

func TestUsagePipeline_IntegrationWithSQLite(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	repo := db.Requests()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		QueueSize:     100,
		BatchSize:     10,
		FlushInterval: 50 * time.Millisecond,
		Writer:        repo,
	})
	pipe.Start(ctx)

	for i := 0; i < 25; i++ {
		pipe.EnqueueEvent(observability.Event{
			CallID:           fmt.Sprintf("sqlite-req-%02d", i),
			VirtualKeyID:     "vkey-sqlite",
			ModelRequested:   "gpt-4o",
			ModelServed:      "gpt-4o-2024-08-06",
			ProviderID:       "openai-main",
			Status:           domain.RequestSuccess,
			PromptTokens:     10 + int64(i),
			CompletionTokens: 20 + int64(i),
			Duration:         100 * time.Millisecond,
			CreatedMS:        int64(10_000 + i),
		})
	}

	// Flush and stop
	if err := pipe.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	page, err := repo.ListByTime("vkey-sqlite", 100, "")
	if err != nil {
		t.Fatalf("ListByTime: %v", err)
	}
	if len(page.Items) != 25 {
		t.Fatalf("expected 25 rows in SQLite, got %d", len(page.Items))
	}

	stats := pipe.Stats()
	if stats.Written != 25 || stats.Dropped != 0 || stats.Errors != 0 {
		t.Fatalf("unexpected pipeline stats: %+v", stats)
	}
}

func TestUsagePipeline_EmitExactlyOncePerRequest(t *testing.T) {
	// Tests that across success, cancellation, and errors, each request emits exactly once
	var emitted atomic.Int64
	writer := observability.BatchWriterFunc(func(records []domain.RequestRecord) error {
		emitted.Add(int64(len(records)))
		return nil
	})

	pipe := observability.NewUsagePipeline(observability.UsagePipelineConfig{
		Writer: writer,
	})
	pipe.Start(context.Background())
	defer func() { _ = pipe.Stop() }()

	cases := []struct {
		name   string
		status domain.RequestStatus
		err    string
	}{
		{name: "success", status: domain.RequestSuccess},
		{name: "cancelled", status: domain.RequestCancelled, err: "context_canceled"},
		{name: "error", status: domain.RequestError, err: "internal_error"},
		{name: "quota_exceeded", status: domain.RequestError, err: "quota_exceeded"},
	}

	for _, tc := range cases {
		ok := pipe.EnqueueEvent(observability.Event{
			CallID:         tc.name,
			Status:         tc.status,
			ErrorCode:      tc.err,
			ModelRequested: "gpt-4o",
		})
		if !ok {
			t.Fatalf("failed to enqueue: %s", tc.name)
		}
	}

	pipe.Flush()

	if emitted.Load() != int64(len(cases)) {
		t.Fatalf("expected %d emitted records, got %d", len(cases), emitted.Load())
	}
}
