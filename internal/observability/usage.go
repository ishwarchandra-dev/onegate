// Package observability owns logging, trace propagation, Prometheus metrics,
// and the usage event pipeline (Phase 5).
package observability

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// BatchWriter persists batches of request records to durable storage.
// *storage.RequestRepo satisfies this interface.
type BatchWriter interface {
	InsertBatch(records []domain.RequestRecord) error
}

// BatchWriterFunc allows a plain function to act as a BatchWriter.
type BatchWriterFunc func(records []domain.RequestRecord) error

// InsertBatch calls the underlying function.
func (f BatchWriterFunc) InsertBatch(records []domain.RequestRecord) error {
	return f(records)
}

// CostCalculator calculates micro-USD cost for token usage on a given model.
// *ratelimit.PriceTable satisfies this interface.
type CostCalculator interface {
	CalculateCost(model string, usage domain.TokenUsage, costMultiplier int) int64
}

// Event captures the raw attributes of a proxied request lifecycle event
// before enrichment into a domain.RequestRecord.
type Event struct {
	CallID           string
	Protocol         string
	VirtualKeyID     string
	ModelRequested   string
	ModelServed      string
	ProviderID       string
	Status           domain.RequestStatus
	ErrorCode        string
	Stream           bool
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostUSDMicros    int64
	Duration         time.Duration
	TTFT             time.Duration
	Attempts         int
	CreatedMS        int64
}

// UsagePipelineConfig configures the bounded usage event pipeline.
type UsagePipelineConfig struct {
	// QueueSize is the capacity of the bounded queue channel. Defaults to 10,000.
	QueueSize int

	// BatchSize is the maximum number of records accumulated before flushing. Defaults to 100.
	BatchSize int

	// FlushInterval is the maximum duration before partially-filled batches are flushed. Defaults to 100ms.
	FlushInterval time.Duration

	// Writer is the destination storage sink. Required.
	Writer BatchWriter

	// Prices estimates micro-USD costs from token counts. Optional.
	Prices CostCalculator

	// Logger for drop warnings and background write failures. Defaults to slog.Default().
	Logger *slog.Logger

	// Clock provides the current time (for deterministic testing). Defaults to time.Now.
	Clock func() time.Time
}

// PipelineStats captures counters for drop-and-count reconciliation.
type PipelineStats struct {
	Captured int64
	Enqueued int64
	Dropped  int64
	Written  int64
	Errors   int64
	QueueLen int
}

// UsagePipeline receives usage events off the proxy hot path, enriches them,
// buffers them in a bounded queue (drop-and-count on overflow), and batch-writes
// them to storage via a background worker goroutine.
type UsagePipeline struct {
	queue         chan domain.RequestRecord
	writer        BatchWriter
	prices        CostCalculator
	batchSize     int
	flushInterval time.Duration
	logger        *slog.Logger
	clock         func() time.Time

	captured atomic.Int64
	enqueued atomic.Int64
	dropped  atomic.Int64
	written  atomic.Int64
	errors   atomic.Int64

	flushReq chan chan struct{}
	stopOnce sync.Once
	stopped  atomic.Bool
	wg       sync.WaitGroup
}

// NewUsagePipeline creates a new bounded usage event pipeline. Call Start to
// launch the background writer goroutine.
func NewUsagePipeline(cfg UsagePipelineConfig) *UsagePipeline {
	qSize := cfg.QueueSize
	if qSize <= 0 {
		qSize = 10_000
	}
	bSize := cfg.BatchSize
	if bSize <= 0 {
		bSize = 100
	}
	interval := cfg.FlushInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}

	return &UsagePipeline{
		queue:         make(chan domain.RequestRecord, qSize),
		writer:        cfg.Writer,
		prices:        cfg.Prices,
		batchSize:     bSize,
		flushInterval: interval,
		logger:        l,
		clock:         clock,
		flushReq:      make(chan chan struct{}),
	}
}

// Start launches the background batch writer goroutine. It runs until ctx is
// cancelled or Stop is called.
func (p *UsagePipeline) Start(ctx context.Context) {
	p.wg.Add(1)
	go p.run(ctx)
}

// Stop gracefully shuts down the usage pipeline, draining all queued events
// and flushing them to storage before returning.
func (p *UsagePipeline) Stop() error {
	p.stopOnce.Do(func() {
		p.stopped.Store(true)
		close(p.queue)
		p.wg.Wait()
	})
	return nil
}

// Enqueue submits a pre-constructed RequestRecord to the pipeline.
// If the bounded queue is full, the record is dropped, the drop counter is
// incremented, a warning is logged, and false is returned.
// This call is strictly non-blocking and never pauses the proxy path.
func (p *UsagePipeline) Enqueue(rec domain.RequestRecord) bool {
	p.captured.Add(1)
	if p.stopped.Load() {
		p.dropped.Add(1)
		return false
	}

	select {
	case p.queue <- rec:
		p.enqueued.Add(1)
		return true
	default:
		dropped := p.dropped.Add(1)
		if p.logger != nil && (dropped == 1 || dropped%1000 == 0) {
			p.logger.Warn("usage pipeline: queue full, event dropped",
				slog.String("trace_id", rec.TraceID),
				slog.String("vkey_id", rec.VirtualKeyID),
				slog.Int64("dropped_total", dropped),
			)
		}
		return false
	}
}

// EnqueueEvent enriches a raw Event and enqueues the resulting RequestRecord.
// Strictly non-blocking.
func (p *UsagePipeline) EnqueueEvent(ev Event) bool {
	rec := p.Enrich(ev)
	return p.Enqueue(rec)
}

// Enrich converts a raw Event into a durable domain.RequestRecord, populating
// missing IDs, timestamps, token sums, and pricing if available.
func (p *UsagePipeline) Enrich(ev Event) domain.RequestRecord {
	rec := domain.RequestRecord{
		ID:               ev.CallID,
		TraceID:          ev.CallID,
		VirtualKeyID:     ev.VirtualKeyID,
		ModelRequested:   ev.ModelRequested,
		ModelServed:      ev.ModelServed,
		ProviderID:       ev.ProviderID,
		Status:           ev.Status,
		ErrorCode:        ev.ErrorCode,
		Stream:           ev.Stream,
		PromptTokens:     ev.PromptTokens,
		CompletionTokens: ev.CompletionTokens,
		TotalTokens:      ev.TotalTokens,
		CostUSDMicros:    ev.CostUSDMicros,
		LatencyMS:        ev.Duration.Milliseconds(),
		TTFTMS:           ev.TTFT.Milliseconds(),
		Attempts:         ev.Attempts,
		CreatedMS:        ev.CreatedMS,
	}

	if rec.ID == "" {
		rec.ID = rec.TraceID
	}
	if rec.CreatedMS <= 0 {
		rec.CreatedMS = p.clock().UnixMilli()
	}
	if rec.TotalTokens == 0 {
		rec.TotalTokens = rec.PromptTokens + rec.CompletionTokens
	}
	if rec.Status == "" {
		if rec.ErrorCode != "" {
			rec.Status = domain.RequestError
		} else {
			rec.Status = domain.RequestSuccess
		}
	}

	// Cost enrichment via price table
	if rec.CostUSDMicros == 0 && p.prices != nil {
		modelForPrice := rec.ModelServed
		if modelForPrice == "" {
			modelForPrice = rec.ModelRequested
		}
		if modelForPrice != "" {
			usage := domain.TokenUsage{
				InputTokens:  rec.PromptTokens,
				OutputTokens: rec.CompletionTokens,
				TotalTokens:  rec.TotalTokens,
			}
			rec.CostUSDMicros = p.prices.CalculateCost(modelForPrice, usage, 100)
		}
	}

	return rec
}

// Stats returns an atomic snapshot of pipeline metrics.
func (p *UsagePipeline) Stats() PipelineStats {
	return PipelineStats{
		Captured: p.captured.Load(),
		Enqueued: p.enqueued.Load(),
		Dropped:  p.dropped.Load(),
		Written:  p.written.Load(),
		Errors:   p.errors.Load(),
		QueueLen: len(p.queue),
	}
}

// Flush synchronously flushes any currently buffered records to storage.
// Useful for determinism in integration tests.
func (p *UsagePipeline) Flush() {
	if p.stopped.Load() {
		return
	}
	done := make(chan struct{})
	select {
	case p.flushReq <- done:
		<-done
	case <-time.After(5 * time.Second):
		// timeout fallback to avoid test deadlocks if stopped concurrently
	}
}

func (p *UsagePipeline) run(ctx context.Context) {
	defer p.wg.Done()

	ticker := time.NewTicker(p.flushInterval)
	defer ticker.Stop()

	batch := make([]domain.RequestRecord, 0, p.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if p.writer != nil {
			if err := p.writer.InsertBatch(batch); err != nil {
				p.errors.Add(int64(len(batch)))
				if p.logger != nil {
					p.logger.Error("usage pipeline: failed writing batch to storage",
						slog.Any("error", err),
						slog.Int("records_lost", len(batch)),
					)
				}
			} else {
				p.written.Add(int64(len(batch)))
			}
		} else {
			// No writer configured: drop and count as errors
			p.errors.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			p.drainAndFlush(&batch, flush)
			return

		case req := <-p.flushReq:
		drainLoop:
			for {
				select {
				case rec, ok := <-p.queue:
					if !ok {
						flush()
						close(req)
						return
					}
					batch = append(batch, rec)
					if len(batch) >= p.batchSize {
						flush()
					}
				default:
					break drainLoop
				}
			}
			flush()
			close(req)

		case <-ticker.C:
			flush()

		case rec, ok := <-p.queue:
			if !ok {
				// Queue channel closed by Stop()
				flush()
				return
			}
			batch = append(batch, rec)
			if len(batch) >= p.batchSize {
				flush()
			}
		}
	}
}

func (p *UsagePipeline) drainAndFlush(batch *[]domain.RequestRecord, flush func()) {
	for {
		select {
		case rec, ok := <-p.queue:
			if !ok {
				flush()
				return
			}
			*batch = append(*batch, rec)
			if len(*batch) >= p.batchSize {
				flush()
			}
		default:
			flush()
			return
		}
	}
}
