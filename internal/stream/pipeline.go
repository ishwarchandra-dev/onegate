package stream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// Summary captures the outcome of a stream execution, including usage,
// token metrics, and time-to-first-token (TTFT).
type Summary struct {
	ID           string
	Model        string
	Usage        domain.TokenUsage
	FinishReason domain.FinishReason
	EventsCount  int
	FramesIn     int
	FramesOut    int
	BytesIn      int64
	BytesOut     int64
	FirstTokenAt time.Duration // time between pipeline start and first client frame flushed
	Duration     time.Duration // total pipeline wall time
	Cancelled    bool
}

// RecordUsage merges token usage into the summary, taking maximums across
// cumulative updates or summing split accounting.
func (s *Summary) RecordUsage(u domain.TokenUsage) {
	if u.InputTokens > s.Usage.InputTokens {
		s.Usage.InputTokens = u.InputTokens
	}
	if u.OutputTokens > s.Usage.OutputTokens {
		s.Usage.OutputTokens = u.OutputTokens
	}
	if u.TotalTokens > s.Usage.TotalTokens {
		s.Usage.TotalTokens = u.TotalTokens
	}
	if u.ReasoningTokens > s.Usage.ReasoningTokens {
		s.Usage.ReasoningTokens = u.ReasoningTokens
	}
	if u.CacheReadTokens > s.Usage.CacheReadTokens {
		s.Usage.CacheReadTokens = u.CacheReadTokens
	}
	if u.CacheWriteTokens > s.Usage.CacheWriteTokens {
		s.Usage.CacheWriteTokens = u.CacheWriteTokens
	}
	s.Usage = s.Usage.WithTotalDerivation()
}

// Config specifies the parameters for a stream pipeline execution.
type Config struct {
	// ProviderProto is the upstream provider's wire protocol.
	ProviderProto domain.ProviderProtocol

	// ClientProto is the client's expected wire protocol.
	ClientProto domain.ProviderProtocol

	// Upstream is the source SSE stream (provider response body).
	Upstream io.Reader

	// Destination receives formatted client SSE frames.
	Destination *Writer

	// OnEvent is an optional observer called on every canonical event.
	OnEvent func(ev domain.StreamEvent)

	// OverrideModel optionally replaces the model name in emitted events.
	OverrideModel string

	// CreatedMS is an optional timestamp for chunk creation (defaults to now).
	CreatedMS int64
}

// Execute runs the streaming translation pipeline:
//  1. Ingests upstream provider SSE frames via stream.Reader.
//  2. Decodes provider chunks into canonical domain.StreamEvent objects.
//  3. Emits events to optional listener (capturing usage and metadata).
//  4. Encodes canonical events into client-facing SSE frames.
//  5. Writes frames to Destination with immediate per-frame flushing.
//
// Retry safety contract:
// If an error occurs BEFORE any byte reaches the client, Execute returns
// the error without writing to Destination (allowing the caller to invoke
// a fallback provider). Once Destination.Written() is true, mid-stream
// errors are formatted as client-native SSE error frames and the stream
// terminates.
func Execute(ctx context.Context, cfg Config) (*Summary, error) {
	if cfg.Upstream == nil {
		return nil, errors.New("stream: nil upstream reader")
	}
	if cfg.Destination == nil {
		return nil, errors.New("stream: nil destination writer")
	}

	start := time.Now()
	summary := &Summary{}

	decoder, err := NewDecoder(cfg.ProviderProto)
	if err != nil {
		return nil, err
	}

	createdMS := cfg.CreatedMS
	if createdMS <= 0 {
		createdMS = start.UnixMilli()
	}
	encoder, err := NewEncoder(cfg.ClientProto, createdMS)
	if err != nil {
		return nil, err
	}

	reader := NewReader(cfg.Upstream)
	defer func() {
		_ = reader.Close()
	}()

	var (
		streamDone bool
		seenDone   bool
	)

	for {
		// Check context cancellation before reading next chunk.
		select {
		case <-ctx.Done():
			summary.Cancelled = true
			summary.Duration = time.Since(start)
			summary.BytesOut = cfg.Destination.BytesWritten()
			return summary, ctx.Err()
		default:
		}

		frame, rerr := reader.ReadFrame()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			if ctx.Err() != nil || errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				summary.Cancelled = true
				summary.Duration = time.Since(start)
				summary.BytesOut = cfg.Destination.BytesWritten()
				err := ctx.Err()
				if err == nil {
					err = rerr
				}
				return summary, err
			}
			// Read error occurred.
			if !cfg.Destination.Written() {
				summary.Duration = time.Since(start)
				return summary, rerr
			}
			emitMidStreamError(cfg.Destination, encoder, rerr)
			summary.Duration = time.Since(start)
			summary.BytesOut = cfg.Destination.BytesWritten()
			return summary, rerr
		}

		summary.FramesIn++
		summary.BytesIn += int64(len(frame.Data) + len(frame.Event))

		// Check for OpenAI-style [DONE] frame.
		if sse.IsDone(frame) {
			seenDone = true
			break
		}

		events, decErr := decoder.Decode([]byte(frame.Data))
		if decErr != nil {
			if !cfg.Destination.Written() {
				summary.Duration = time.Since(start)
				return summary, decErr
			}
			emitMidStreamError(cfg.Destination, encoder, decErr)
			summary.Duration = time.Since(start)
			summary.BytesOut = cfg.Destination.BytesWritten()
			return summary, decErr
		}

		for _, ev := range events {
			if cfg.OverrideModel != "" && ev.Model != "" {
				ev.Model = cfg.OverrideModel
			}
			if ev.ID != "" && summary.ID == "" {
				summary.ID = ev.ID
			}
			if ev.Model != "" && summary.Model == "" {
				summary.Model = ev.Model
			}
			if ev.Usage != nil {
				summary.RecordUsage(*ev.Usage)
			}
			if ev.FinishReason != "" {
				summary.FinishReason = ev.FinishReason
			}
			summary.EventsCount++

			if cfg.OnEvent != nil {
				cfg.OnEvent(ev)
			}

			outFrames, done, encErr := encoder.Encode(ev)
			if encErr != nil {
				if !cfg.Destination.Written() {
					summary.Duration = time.Since(start)
					return summary, encErr
				}
				emitMidStreamError(cfg.Destination, encoder, encErr)
				summary.Duration = time.Since(start)
				summary.BytesOut = cfg.Destination.BytesWritten()
				return summary, encErr
			}

			for _, of := range outFrames {
				if err := cfg.Destination.WriteFrame(of); err != nil {
					summary.Duration = time.Since(start)
					summary.BytesOut = cfg.Destination.BytesWritten()
					if ctx.Err() != nil || errors.Is(err, context.Canceled) {
						summary.Cancelled = true
					}
					return summary, err
				}
				summary.FramesOut++
				if summary.FirstTokenAt == 0 {
					summary.FirstTokenAt = time.Since(start)
				}
				if sse.IsDone(of) {
					seenDone = true
				}
			}

			if done {
				streamDone = true
				break
			}
		}

		if streamDone {
			break
		}
	}

	if ctx.Err() != nil {
		summary.Cancelled = true
		summary.Duration = time.Since(start)
		summary.BytesOut = cfg.Destination.BytesWritten()
		return summary, ctx.Err()
	}

	// Emit trailing canonical events if decoder implements Finisher.
	if finisher, ok := decoder.(Finisher); ok && !streamDone {
		trailing := finisher.Finish()
		for _, ev := range trailing {
			if cfg.OverrideModel != "" && ev.Model != "" {
				ev.Model = cfg.OverrideModel
			}
			if ev.Usage != nil {
				summary.RecordUsage(*ev.Usage)
			}
			if ev.FinishReason != "" {
				summary.FinishReason = ev.FinishReason
			}
			summary.EventsCount++

			if cfg.OnEvent != nil {
				cfg.OnEvent(ev)
			}

			outFrames, done, encErr := encoder.Encode(ev)
			if encErr == nil {
				for _, of := range outFrames {
					_ = cfg.Destination.WriteFrame(of)
					summary.FramesOut++
					if sse.IsDone(of) {
						seenDone = true
					}
				}
			}
			if done {
				break
			}
		}
	}

	// If client protocol is OpenAI and [DONE] hasn't been emitted, emit it.
	if cfg.ClientProto == domain.ProtocolOpenAI && !seenDone {
		_ = cfg.Destination.WriteFrame(sse.Frame{Data: "[DONE]"})
		summary.FramesOut++
	}

	summary.Duration = time.Since(start)
	summary.BytesOut = cfg.Destination.BytesWritten()
	return summary, nil
}

// emitMidStreamError formats a fatal mid-stream failure into a client-facing
// native SSE error event, preserving the error taxonomy.
func emitMidStreamError(w *Writer, enc Encoder, streamErr error) {
	ev := domain.StreamEvent{
		Type: domain.EventError,
		Error: &domain.GatewayError{
			Status:  http.StatusInternalServerError,
			Type:    domain.ErrAPI,
			Message: streamErr.Error(),
		},
	}
	frames, _, err := enc.Encode(ev)
	if err == nil {
		for _, f := range frames {
			_ = w.WriteFrame(f)
		}
	}
}
