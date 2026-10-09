// Package fallback implements the provider attempt loop and fallback
// execution engine for OneGate (ADR 003, Phase 3).
//
// Retries and fallback happen strictly before the first byte reaches the
// client. Once bytes are transmitted, fallback is forbidden.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/observability"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/ingest"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/nonstream"
	"github.com/ishwarchandra-dev/onegate/internal/stream"
)

// Target represents one candidate upstream target.
type Target struct {
	Provider client.Provider
	Model    string
	// CostMultiplier is the target's pricing modifier in percent
	// (100 = nominal). Flows into usage cost accounting.
	CostMultiplier int
}

// TargetResolver decides the ordered list of provider targets to try for a call.
type TargetResolver interface {
	ResolveTargets(ctx context.Context, call ingest.Call) ([]Target, error)
}

// StaticResolver returns a fixed list of candidate targets.
type StaticResolver struct {
	Targets []Target
}

// ResolveTargets returns the static target list.
func (s StaticResolver) ResolveTargets(_ context.Context, _ ingest.Call) ([]Target, error) {
	return s.Targets, nil
}

// Attempt records the outcome and timing of one candidate target execution.
type Attempt struct {
	Index        int
	ProviderID   string
	Model        string
	StatusCode   int
	Error        *domain.GatewayError
	Duration     time.Duration
	BytesWritten int64
	Retried      bool
}

// Trace records the complete attempt chain for a proxied request.
type Trace struct {
	CallID    string
	Attempts  []Attempt
	Duration  time.Duration
	Completed bool
}

// UsageEvent captures token usage and request lifecycle outcome for
// billing and analytics (Phase 5 integration).
type UsageEvent struct {
	CallID         string
	Protocol       domain.ProviderProtocol
	VirtualKeyID   string
	ModelRequested string
	ModelServed    string
	ProviderID     string
	Status         domain.RequestStatus
	ErrorCode      string
	Stream         bool
	Usage          domain.TokenUsage
	Duration       time.Duration
	TTFT           time.Duration
	Attempts       int
	Completed      bool
	Cancelled      bool
	CostMultiplier int
}

// ToObservabilityEvent maps UsageEvent to observability.Event for the usage pipeline.
func (u UsageEvent) ToObservabilityEvent() observability.Event {
	return observability.Event{
		CallID:           u.CallID,
		Protocol:         string(u.Protocol),
		VirtualKeyID:     u.VirtualKeyID,
		ModelRequested:   u.ModelRequested,
		ModelServed:      u.ModelServed,
		ProviderID:       u.ProviderID,
		Status:           u.Status,
		ErrorCode:        u.ErrorCode,
		Stream:           u.Stream,
		PromptTokens:     u.Usage.InputTokens,
		CompletionTokens: u.Usage.OutputTokens,
		TotalTokens:      u.Usage.TotalTokens,
		Duration:         u.Duration,
		TTFT:             u.TTFT,
		Attempts:         u.Attempts,
		CostMultiplier:   u.CostMultiplier,
	}
}

// Engine executes calls against an ordered list of candidate targets with
// retry-before-first-byte semantics.
type Engine struct {
	resolver  TargetResolver
	client    *client.Client
	nonstream *nonstream.Executor
	logger    *slog.Logger
	onTrace   func(Trace)
	onUsage   func(UsageEvent)
}

// Config configures the fallback engine.
type Config struct {
	Resolver             TargetResolver
	Client               *client.Client
	MaxRequestBodyBytes  int64
	MaxResponseBodyBytes int64
	Logger               *slog.Logger
	OnTrace              func(Trace)
	OnUsage              func(UsageEvent)
}

// NewEngine creates a new fallback execution engine.
func NewEngine(cfg Config) *Engine {
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}
	ns := nonstream.NewExecutor(nonstream.Config{
		Client:               cfg.Client,
		MaxRequestBodyBytes:  cfg.MaxRequestBodyBytes,
		MaxResponseBodyBytes: cfg.MaxResponseBodyBytes,
	})
	return &Engine{
		resolver:  cfg.Resolver,
		client:    cfg.Client,
		nonstream: ns,
		logger:    l,
		onTrace:   cfg.OnTrace,
		onUsage:   cfg.OnUsage,
	}
}

// Execute implements ingest.Proxy. It tries targets in order until success,
// context cancellation, or a fatal non-retryable failure occurs.
func (e *Engine) Execute(ctx context.Context, w http.ResponseWriter, call ingest.Call) {
	start := time.Now()

	callID := observability.TraceID(ctx)
	if callID == "" {
		callID = fmt.Sprintf("req-%d", time.Now().UnixNano())
	}

	usageEv := UsageEvent{
		CallID:         callID,
		Protocol:       call.Protocol,
		VirtualKeyID:   call.Key.ID,
		ModelRequested: call.Request.Model,
		Stream:         call.Stream,
	}

	trace := Trace{
		CallID: usageEv.CallID,
	}

	defer func() {
		trace.Duration = time.Since(start)
		usageEv.Duration = trace.Duration
		usageEv.Attempts = len(trace.Attempts)
		if ctx.Err() != nil || usageEv.Cancelled {
			usageEv.Status = domain.RequestCancelled
			usageEv.Cancelled = true
			if usageEv.ErrorCode == "" {
				usageEv.ErrorCode = "context_canceled"
			}
		} else if usageEv.Completed {
			usageEv.Status = domain.RequestSuccess
		} else {
			usageEv.Status = domain.RequestError
			if usageEv.ErrorCode == "" && len(trace.Attempts) > 0 {
				lastAtt := trace.Attempts[len(trace.Attempts)-1]
				if lastAtt.Error != nil {
					if lastAtt.Error.Code != "" {
						usageEv.ErrorCode = lastAtt.Error.Code
					} else {
						usageEv.ErrorCode = string(lastAtt.Error.Type)
					}
				}
			}
		}

		if e.onUsage != nil {
			e.onUsage(usageEv)
		}
		if e.onTrace != nil {
			e.onTrace(trace)
		}
	}()

	targets, err := e.resolver.ResolveTargets(ctx, call)
	if err != nil || len(targets) == 0 {
		// Routing failures render their own protocol-correct envelopes
		// (checklist C-10..C-12, C-23) — never a generic 503 blob.
		ge := resolveError(call, err)
		usageEv.ErrorCode = string(ge.Type)
		nonstream.RenderError(w, call.Protocol, ge)
		return
	}

	for idx, target := range targets {
		if ctx.Err() != nil {
			usageEv.Cancelled = true
			e.logger.InfoContext(ctx, "proxy call cancelled before attempt",
				observability.TraceAttr(ctx),
				slog.Int("attempt", idx),
				slog.String("provider", target.Provider.ID),
			)
			return
		}

		attemptStart := time.Now()
		attempt := Attempt{
			Index:      idx,
			ProviderID: target.Provider.ID,
			Model:      target.Model,
		}

		if call.Stream {
			sw := stream.NewWriter(w)
			summary, _ := e.executeStreamAttempt(ctx, w, sw, call, target, idx, len(targets), &attempt)
			attempt.Duration = time.Since(attemptStart)
			attempt.BytesWritten = sw.BytesWritten()
			trace.Attempts = append(trace.Attempts, attempt)

			usageEv.ModelServed = target.Model
			usageEv.ProviderID = target.Provider.ID
			usageEv.CostMultiplier = target.CostMultiplier
			if summary != nil {
				usageEv.Usage = summary.Usage
				usageEv.TTFT = summary.FirstTokenAt
				if summary.Cancelled {
					usageEv.Cancelled = true
				}
			}

			if attempt.Error == nil {
				trace.Completed = true
				usageEv.Completed = true
				return
			}
			if ctx.Err() != nil {
				usageEv.Cancelled = true
				return
			}
			if !attempt.Retried {
				// Fatal or client already received bytes; stop chain
				return
			}
		} else {
			rec := httptest.NewRecorder()
			res, _ := e.executeBufferedAttempt(ctx, rec, call, target, idx, len(targets), &attempt)
			attempt.Duration = time.Since(attemptStart)
			attempt.BytesWritten = int64(rec.Body.Len())
			trace.Attempts = append(trace.Attempts, attempt)

			usageEv.ModelServed = target.Model
			usageEv.ProviderID = target.Provider.ID
			usageEv.CostMultiplier = target.CostMultiplier
			if res != nil {
				usageEv.Usage = res.Usage
			}

			if attempt.Error == nil {
				trace.Completed = true
				usageEv.Completed = true
				commitRecorder(w, rec)
				return
			}
			if ctx.Err() != nil {
				usageEv.Cancelled = true
				return
			}
			if !attempt.Retried {
				// Fatal or last target: write the error response to the
				// client. The recorded provider-shaped body is discarded in
				// favor of the terminal remap (checklist C-16/C-18: upstream
				// auth -> 502 upstream_authentication, 5xx -> 502).
				if attempt.Error != nil {
					nonstream.RenderError(w, call.Protocol, terminalError(*attempt.Error))
				} else {
					commitRecorder(w, rec)
				}
				return
			}
		}
	}
}

func (e *Engine) executeBufferedAttempt(
	ctx context.Context,
	rec *httptest.ResponseRecorder,
	call ingest.Call,
	target Target,
	idx int,
	totalTargets int,
	attempt *Attempt,
) (*nonstream.Result, error) {
	callParams := nonstream.Call{
		ClientProto:   call.Protocol,
		Provider:      target.Provider,
		Request:       call.Request,
		RawBody:       call.Body,
		ProviderModel: target.Model,       // upstream sees the provider model (G-7)
		OverrideModel: call.Request.Model, // client sees the canonical model (parity)
	}

	res, err := e.nonstream.Execute(ctx, rec, callParams)
	if err == nil {
		attempt.StatusCode = http.StatusOK
		e.logger.InfoContext(ctx, "buffered attempt succeeded",
			observability.TraceAttr(ctx),
			slog.Int("attempt", idx),
			slog.String("provider", target.Provider.ID),
			slog.String("model", target.Model),
		)
		return res, nil
	}

	ge := extractGatewayError(err, rec.Code)
	attempt.StatusCode = ge.Status
	attempt.Error = &ge

	canRetry := ge.Retryable && (idx+1 < totalTargets)
	attempt.Retried = canRetry

	e.logger.WarnContext(ctx, "buffered attempt failed",
		observability.TraceAttr(ctx),
		slog.Int("attempt", idx),
		slog.String("provider", target.Provider.ID),
		slog.String("model", target.Model),
		slog.Int("status", ge.Status),
		slog.String("type", string(ge.Type)),
		slog.Bool("retryable", ge.Retryable),
		slog.Bool("will_retry", canRetry),
		slog.String("error", ge.Message),
	)
	return res, err
}

func (e *Engine) executeStreamAttempt(
	ctx context.Context,
	w http.ResponseWriter,
	sw *stream.Writer,
	call ingest.Call,
	target Target,
	idx int,
	totalTargets int,
	attempt *Attempt,
) (*stream.Summary, error) {
	req := call.Request
	if target.Model != "" {
		req.Model = target.Model
	}

	provBody, err := nonstream.EncodeRequest(target.Provider.Protocol, req)
	if err != nil {
		ge := domain.GatewayError{
			Status:    http.StatusBadRequest,
			Type:      domain.ErrInvalidRequest,
			Message:   fmt.Sprintf("encode provider request: %v", err),
			Retryable: false,
		}
		attempt.StatusCode = ge.Status
		attempt.Error = &ge
		nonstream.RenderError(w, call.Protocol, ge)
		return nil, err
	}

	path, query := nonstream.ProviderPath(target.Provider.Protocol, req.Model, true)
	upstreamReq := client.Request{
		Path:     path,
		RawQuery: query,
		Body:     provBody,
		Stream:   true,
	}

	resp, err := e.client.Do(ctx, target.Provider, upstreamReq)
	if err != nil {
		ge := extractGatewayError(err, http.StatusBadGateway)
		attempt.StatusCode = ge.Status
		attempt.Error = &ge

		canRetry := ge.Retryable && !sw.Written() && (idx+1 < totalTargets)
		attempt.Retried = canRetry

		e.logger.WarnContext(ctx, "stream attempt failed at connect/headers",
			observability.TraceAttr(ctx),
			slog.Int("attempt", idx),
			slog.String("provider", target.Provider.ID),
			slog.Int("status", ge.Status),
			slog.Bool("retryable", ge.Retryable),
			slog.Bool("written", sw.Written()),
			slog.Bool("will_retry", canRetry),
			slog.String("error", ge.Message),
		)

		if !canRetry {
			nonstream.RenderError(w, call.Protocol, terminalError(ge))
		}
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBytes, _ := io.ReadAll(resp.Body)
		ge := nonstream.DecodeError(target.Provider.Protocol, respBytes, resp.StatusCode)
		attempt.StatusCode = ge.Status
		attempt.Error = &ge

		canRetry := ge.Retryable && !sw.Written() && (idx+1 < totalTargets)
		attempt.Retried = canRetry

		e.logger.WarnContext(ctx, "stream attempt returned error status",
			observability.TraceAttr(ctx),
			slog.Int("attempt", idx),
			slog.String("provider", target.Provider.ID),
			slog.Int("status", ge.Status),
			slog.Bool("retryable", ge.Retryable),
			slog.Bool("written", sw.Written()),
			slog.Bool("will_retry", canRetry),
			slog.String("error", ge.Message),
		)

		if !canRetry {
			nonstream.RenderError(w, call.Protocol, terminalError(ge))
		}
		return nil, errors.New(ge.Message)
	}

	// 200 OK: pipe streaming SSE frames.
	summary, err := stream.Execute(ctx, stream.Config{
		ProviderProto: target.Provider.Protocol,
		ClientProto:   call.Protocol,
		Upstream:      resp.Body,
		Destination:   sw,
		OverrideModel: call.Request.Model, // echo the canonical model to the client (parity)
	})

	if err != nil {
		ge := extractGatewayError(err, http.StatusBadGateway)
		attempt.StatusCode = ge.Status
		attempt.Error = &ge

		// Fallback allowed ONLY if zero bytes have reached the client!
		canRetry := ge.Retryable && !sw.Written() && (idx+1 < totalTargets)
		attempt.Retried = canRetry

		e.logger.WarnContext(ctx, "stream pipeline execution failed",
			observability.TraceAttr(ctx),
			slog.Int("attempt", idx),
			slog.String("provider", target.Provider.ID),
			slog.Bool("written", sw.Written()),
			slog.Bool("will_retry", canRetry),
			slog.String("error", err.Error()),
		)

		if !canRetry && !sw.Written() {
			nonstream.RenderError(w, call.Protocol, terminalError(ge))
		}
		return summary, err
	}

	attempt.StatusCode = http.StatusOK
	e.logger.InfoContext(ctx, "stream attempt completed successfully",
		observability.TraceAttr(ctx),
		slog.Int("attempt", idx),
		slog.String("provider", target.Provider.ID),
		slog.Int64("bytes_out", summary.BytesOut),
		slog.Duration("ttft", summary.FirstTokenAt),
	)
	return summary, nil
}

// ResolveError carries a client-ready GatewayError out of a
// TargetResolver (checklist C-10..C-12, C-23). Resolvers at the
// composition layer wrap their domain-specific failures (routing
// sentinels, credential outages) in this type; the engine stays free of
// routing imports (layering contract, internal/proxy/proxy_test.go).
type ResolveError struct {
	GE domain.GatewayError
}

// Error implements error.
func (e *ResolveError) Error() string { return e.GE.Message }

// resolveError classifies a resolver failure for client rendering.
// ResolveError renders as-is; unknown failures degrade to 503
// overloaded_error, matching OmniRoute's posture for routing outages.
func resolveError(_ ingest.Call, err error) domain.GatewayError {
	if err == nil {
		return domain.GatewayError{
			Status:  http.StatusServiceUnavailable,
			Type:    domain.ErrOverloaded,
			Message: "no available upstream provider targets for model",
		}
	}
	var re *ResolveError
	if errors.As(err, &re) {
		return re.GE
	}
	return domain.GatewayError{
		Status:  http.StatusServiceUnavailable,
		Type:    domain.ErrOverloaded,
		Message: fmt.Sprintf("resolve targets: %v", err),
	}
}

// terminalError remaps the last upstream error at fallback exhaustion
// (checklist C-16/C-18): the client's key is fine, the provider failed —
// upstream auth problems surface as gateway 502 upstream_authentication,
// provider 5xx as 502 api_error (overload keeps its 503), and everything
// else (rate limits, 4xx passthrough) renders as classified.
func terminalError(ge domain.GatewayError) domain.GatewayError {
	switch {
	case ge.Status == http.StatusUnauthorized || ge.Status == http.StatusForbidden:
		return domain.GatewayError{
			Status:        http.StatusBadGateway,
			Type:          domain.ErrAPI,
			Code:          "upstream_authentication",
			Message:       ge.Message,
			RetryAfterSec: ge.RetryAfterSec,
		}
	case ge.Status >= 500 && ge.Type != domain.ErrOverloaded:
		ge.Status = http.StatusBadGateway
		return ge
	default:
		return ge
	}
}

func extractGatewayError(err error, defaultStatus int) domain.GatewayError {
	var nsErr *nonstream.Error
	if errors.As(err, &nsErr) {
		return nsErr.GErr
	}
	var cliErr *client.Error
	if errors.As(err, &cliErr) {
		return cliErr.GErr
	}
	status := defaultStatus
	if status == 0 {
		status = http.StatusBadGateway
	}
	return domain.GatewayError{
		Status:    status,
		Type:      domain.ErrAPI,
		Message:   err.Error(),
		Retryable: true,
	}
}

func commitRecorder(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	if w == nil || rec == nil {
		return
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}
