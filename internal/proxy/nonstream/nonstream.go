// Package nonstream implements the buffered request/response proxy path
// with configurable payload size caps, cross-protocol translation, and
// fail-fast 413 error handling.
package nonstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/anthropic"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/gemini"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/openai"
	"github.com/ishwarchandra-dev/onegate/internal/proxy/client"
)

// DefaultMaxRequestBodyBytes is the default inbound request body cap (32 MiB).
const DefaultMaxRequestBodyBytes = 32 << 20

// DefaultMaxResponseBodyBytes is the default upstream response body cap (32 MiB).
const DefaultMaxResponseBodyBytes = 32 << 20

// Error is returned by Execute on proxy failures, carrying the canonical
// GatewayError envelope.
type Error struct {
	GErr  domain.GatewayError
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("nonstream: %s (%d): %v", e.GErr.Message, e.GErr.Status, e.Cause)
	}
	return fmt.Sprintf("nonstream: %s (%d)", e.GErr.Message, e.GErr.Status)
}

func (e *Error) Unwrap() error { return e.Cause }

// GatewayError returns the canonical gateway error payload.
func (e *Error) GatewayError() domain.GatewayError { return e.GErr }

// Config configures the non-streaming executor.
type Config struct {
	MaxRequestBodyBytes  int64
	MaxResponseBodyBytes int64
	Client               *client.Client
}

// Call holds parameters for a single non-streaming proxy execution.
type Call struct {
	// ClientProto is the client wire protocol.
	ClientProto domain.ProviderProtocol

	// Provider is the resolved upstream target.
	Provider client.Provider

	// Request is the canonical request.
	Request domain.Request

	// RawBody is the raw client request payload.
	RawBody []byte

	// OverrideModel optionally replaces the model name in responses.
	OverrideModel string
}

// Result captures the outcome of a successful non-streaming execution.
type Result struct {
	Response    domain.Response
	RawResponse []byte
	Status      int
	Usage       domain.TokenUsage
	Duration    time.Duration
}

// Executor coordinates buffered request translation, provider dispatch,
// and response re-encoding.
type Executor struct {
	client               *client.Client
	maxRequestBodyBytes  int64
	maxResponseBodyBytes int64
}

// NewExecutor creates a new non-streaming executor.
func NewExecutor(cfg Config) *Executor {
	maxReq := cfg.MaxRequestBodyBytes
	if maxReq <= 0 {
		maxReq = DefaultMaxRequestBodyBytes
	}
	maxResp := cfg.MaxResponseBodyBytes
	if maxResp <= 0 {
		maxResp = DefaultMaxResponseBodyBytes
	}
	return &Executor{
		client:               cfg.Client,
		maxRequestBodyBytes:  maxReq,
		maxResponseBodyBytes: maxResp,
	}
}

// Execute performs the non-streaming proxy call:
//  1. Validates request body against size caps (fails fast with 413).
//  2. Encodes canonical request into target provider wire format.
//  3. Sends request via pooled HTTP client.
//  4. Reads response with bounded LimitReader (fails fast with 413 if oversized).
//  5. Decodes provider response to canonical domain.Response.
//  6. Encodes canonical response to client wire format.
//  7. Writes response to client.
func (e *Executor) Execute(ctx context.Context, w http.ResponseWriter, call Call) (*Result, error) {
	start := time.Now()

	// 1. Inbound request body size check.
	if int64(len(call.RawBody)) > e.maxRequestBodyBytes {
		ge := domain.GatewayError{
			Status:  http.StatusRequestEntityTooLarge,
			Type:    domain.ErrTooLarge,
			Code:    "request_too_large",
			Message: fmt.Sprintf("request body of %d bytes exceeds maximum allowed size of %d bytes", len(call.RawBody), e.maxRequestBodyBytes),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge}
	}

	// 2. Prepare canonical request & encode for provider.
	req := call.Request
	if call.OverrideModel != "" {
		req.Model = call.OverrideModel
	}

	provBody, err := EncodeRequest(call.Provider.Protocol, req)
	if err != nil {
		ge := domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Message: fmt.Sprintf("encode provider request: %v", err),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge, Cause: err}
	}

	path, query := ProviderPath(call.Provider.Protocol, req.Model, false)

	// 3. Dispatch to upstream provider.
	upstreamReq := client.Request{
		Path:     path,
		RawQuery: query,
		Body:     provBody,
		Stream:   false,
	}

	resp, err := e.client.Do(ctx, call.Provider, upstreamReq)
	if err != nil {
		var clientErr *client.Error
		if errors.As(err, &clientErr) {
			RenderError(w, call.ClientProto, clientErr.GErr)
			return nil, &Error{GErr: clientErr.GErr, Cause: clientErr.Cause}
		}
		ge := domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrAPI,
			Message: err.Error(),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge, Cause: err}
	}
	defer resp.Body.Close()

	// 4. Read response with LimitReader to enforce response size cap.
	limited := io.LimitReader(resp.Body, e.maxResponseBodyBytes+1)
	respBytes, err := io.ReadAll(limited)
	if err != nil {
		ge := domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrAPI,
			Message: fmt.Sprintf("read upstream response: %v", err),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge, Cause: err}
	}

	if int64(len(respBytes)) > e.maxResponseBodyBytes {
		ge := domain.GatewayError{
			Status:  http.StatusRequestEntityTooLarge,
			Type:    domain.ErrTooLarge,
			Code:    "request_too_large",
			Message: fmt.Sprintf("upstream response body exceeds maximum allowed size of %d bytes", e.maxResponseBodyBytes),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge}
	}

	// 5. Handle upstream HTTP error responses.
	if resp.StatusCode >= 400 {
		ge := DecodeError(call.Provider.Protocol, respBytes, resp.StatusCode)
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge}
	}

	// 6. Decode provider response to canonical domain.Response.
	canonResp, err := DecodeResponse(call.Provider.Protocol, respBytes)
	if err != nil {
		ge := domain.GatewayError{
			Status:  http.StatusBadGateway,
			Type:    domain.ErrAPI,
			Message: fmt.Sprintf("decode upstream response: %v", err),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge, Cause: err}
	}

	if call.OverrideModel != "" {
		canonResp.Model = call.OverrideModel
	}

	// 7. Encode canonical response into client wire format.
	clientBytes, err := EncodeResponse(call.ClientProto, canonResp)
	if err != nil {
		ge := domain.GatewayError{
			Status:  http.StatusInternalServerError,
			Type:    domain.ErrInternal,
			Message: fmt.Sprintf("encode client response: %v", err),
		}
		RenderError(w, call.ClientProto, ge)
		return nil, &Error{GErr: ge, Cause: err}
	}

	if w != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(clientBytes)
	}

	return &Result{
		Response:    canonResp,
		RawResponse: clientBytes,
		Status:      http.StatusOK,
		Usage:       canonResp.Usage,
		Duration:    time.Since(start),
	}, nil
}

// RenderError serializes a canonical gateway error into the client protocol's
// native envelope and writes it to the http.ResponseWriter.
func RenderError(w http.ResponseWriter, protocol domain.ProviderProtocol, ge domain.GatewayError) {
	if w == nil {
		return
	}
	var (
		body   []byte
		status int
	)
	switch protocol {
	case domain.ProtocolAnthropic:
		body, status = anthropic.EncodeError(ge)
	case domain.ProtocolGemini:
		body, status = gemini.EncodeError(ge)
	default:
		body, status = openai.EncodeError(ge)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// ProviderPath returns the standard endpoint path and query parameters for
// the target provider protocol.
func ProviderPath(protocol domain.ProviderProtocol, model string, stream bool) (path string, rawQuery string) {
	switch protocol {
	case domain.ProtocolAnthropic:
		return "/v1/messages", ""
	case domain.ProtocolGemini:
		action := ":generateContent"
		if stream {
			action = ":streamGenerateContent"
			return "/v1beta/models/" + model + action, "alt=sse"
		}
		return "/v1beta/models/" + model + action, ""
	default:
		return "/chat/completions", ""
	}
}

// EncodeRequest translates a canonical request to the wire format of the provider protocol.
func EncodeRequest(protocol domain.ProviderProtocol, req domain.Request) ([]byte, error) {
	switch protocol {
	case domain.ProtocolAnthropic:
		return anthropic.EncodeRequest(req)
	case domain.ProtocolGemini:
		return gemini.EncodeRequest(req)
	default:
		return openai.EncodeRequest(req)
	}
}

// DecodeResponse translates provider response wire bytes into a canonical domain.Response.
func DecodeResponse(protocol domain.ProviderProtocol, body []byte) (domain.Response, error) {
	switch protocol {
	case domain.ProtocolAnthropic:
		return anthropic.DecodeResponse(body)
	case domain.ProtocolGemini:
		return gemini.DecodeResponse(body)
	default:
		return openai.DecodeResponse(body)
	}
}

// EncodeResponse translates a canonical response into the client's wire format.
func EncodeResponse(protocol domain.ProviderProtocol, resp domain.Response) ([]byte, error) {
	switch protocol {
	case domain.ProtocolAnthropic:
		return anthropic.EncodeResponse(resp)
	case domain.ProtocolGemini:
		return gemini.EncodeResponse(resp)
	default:
		return openai.EncodeResponse(resp)
	}
}

// DecodeError translates provider error wire bytes and status into a canonical domain.GatewayError.
func DecodeError(protocol domain.ProviderProtocol, body []byte, status int) domain.GatewayError {
	switch protocol {
	case domain.ProtocolAnthropic:
		return anthropic.DecodeError(body, status)
	case domain.ProtocolGemini:
		return gemini.DecodeError(body, status)
	default:
		return openai.DecodeError(body, status)
	}
}
