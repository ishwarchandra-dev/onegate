package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
	"github.com/ishwarchandra-dev/onegate/internal/protocol/profile"
)

// classifyTransportError maps http.Client failures onto *Error with a
// domain.GatewayError the fallback engine can act on:
//
//   - client cancellation (ctx canceled)      -> cancelled, not retryable
//     (the client is gone; retrying serves nobody)
//   - gateway-side deadline (ctx deadline)    -> timeout, retryable
//   - SSRF guard refusal                      -> permission, not retryable
//     (configuration error; every attempt fails identically)
//   - everything else (refused, reset, DNS,
//     TLS, header timeout, ...)               -> api_error, retryable
//     (transient network conditions; the fallback engine may try
//     another target)
func classifyTransportError(providerID string, err error) error {
	ge := domain.GatewayError{
		Status:    http.StatusBadGateway,
		Type:      domain.ErrAPI,
		Message:   transportMessage(err),
		Retryable: true,
	}
	switch {
	case errors.Is(err, context.Canceled):
		ge.Type = domain.ErrCancelled
		ge.Retryable = false
		ge.Status = 499 // client closed request (nginx convention)
		ge.Message = "client cancelled the request"
	case errors.Is(err, context.DeadlineExceeded):
		ge.Type = domain.ErrTimeout
		ge.Status = http.StatusGatewayTimeout
		ge.Message = "upstream attempt exceeded its deadline"
	case isSSRFRefusal(err):
		ge.Type = domain.ErrPermission
		ge.Retryable = false
		ge.Message = err.Error()
	}
	return &Error{Provider: providerID, GErr: ge, Cause: err}
}

// isSSRFRefusal detects dial-time guard failures by their error prefix
// (the guard's control hook errors surface wrapped inside url.Error).
func isSSRFRefusal(err error) bool {
	var inner error
	for e := err; e != nil; e = errors.Unwrap(e) {
		inner = e
	}
	return inner != nil && strings.HasPrefix(inner.Error(), ssrfRefusalPrefix)
}

// ssrfRefusalPrefix marks dial-time guard errors.
const ssrfRefusalPrefix = "client: ssrf guard:"

// transportMessage renders a log-safe transport error (no URLs with
// credentials — auth is header-based, so URL text is safe, but keep it
// short anyway).
func transportMessage(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "upstream network timeout"
	}
	if errors.Is(err, io.EOF) {
		return "upstream closed the connection prematurely"
	}
	msg := err.Error()
	// Strip the wrapped url.Error noise ("Post \"http://...\": ...").
	if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "Post ") {
		msg = msg[i+2:]
	}
	return msg
}

// EndpointPath returns the per-protocol chat endpoint path and query
// for a canonical model. The proxy core calls this once per attempt
// and hands the result to Request:
//
//	openai         profile path (e.g. /chat/completions)
//	openai-compat  profile path for the flavor
//	anthropic      /v1/messages
//	gemini         /v1beta/models/{model}:generateContent, or
//	               :streamGenerateContent?alt=sse when stream
func EndpointPath(p Provider, model string, stream bool) (path, rawQuery string, err error) {
	switch p.Protocol {
	case domain.ProtocolOpenAI, domain.ProtocolOpenAIComp:
		prof, rerr := profile.Resolve(openAIFlavor(p))
		if rerr != nil {
			return "", "", rerr
		}
		return prof.Path, "", nil
	case domain.ProtocolAnthropic:
		return "/v1/messages", "", nil
	case domain.ProtocolGemini:
		if model == "" {
			return "", "", fmt.Errorf("gemini endpoint requires a model in the path")
		}
		if stream {
			return "/v1beta/models/" + model + ":streamGenerateContent", "alt=sse", nil
		}
		return "/v1beta/models/" + model + ":generateContent", "", nil
	default:
		return "", "", fmt.Errorf("unknown provider protocol %q", p.Protocol)
	}
}
