package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeError converts an OpenAI error body (plus HTTP status) into a
// canonical gateway error. Status is the primary classifier; explicit
// type/code strings refine it (mapping doc §1.4).
func DecodeError(body []byte, status int) domain.GatewayError {
	var eb errorBody
	if err := json.Unmarshal(body, &eb); err != nil || eb.Error.Message == "" {
		// Non-JSON body (HTML error page, empty body): classify by status.
		return statusError(body, status)
	}
	we := eb.Error

	ge := domain.GatewayError{
		Status:  status,
		Message: we.Message,
		Param:   we.Param,
	}
	if s, ok := we.Code.(string); ok {
		ge.Code = s
	}

	// Explicit type string, when it names a canonical type, wins — except
	// that HTTP 429 always classifies as a rate limit (checklist C-17:
	// status beats a generic body type; Azure subtypes handled below).
	if t, ok := errorTypeFromString(we.Type); ok && status != http.StatusTooManyRequests {
		ge.Type = t
	} else {
		ge.Type = errorTypeFromStatus(status)
	}
	// Azure quota subtypes ("tokens"/"requests") and rate-limit codes
	// [quirk:openai-azure-quota-types].
	if strings.Contains(strings.ToLower(we.Type+fmt.Sprint(we.Code)), "rate_limit") ||
		(we.Type == "tokens" || we.Type == "requests") {
		ge.Type = domain.ErrRateLimit
	}
	switch ge.Type {
	case domain.ErrInvalidRequest:
		if strings.Contains(ge.Code, "context_length") || strings.Contains(ge.Message, "maximum context length") {
			ge.Code = "context_length_exceeded"
		}
	}
	ge.Retryable = retryableType(ge.Type)
	return ge
}

func statusError(body []byte, status int) domain.GatewayError {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg == "" {
		msg = fmt.Sprintf("upstream returned HTTP %d", status)
	}
	t := errorTypeFromStatus(status)
	return domain.GatewayError{Status: status, Type: t, Message: msg, Retryable: retryableType(t)}
}

func errorTypeFromString(s string) (domain.ErrorType, bool) {
	switch s {
	case "invalid_request_error":
		return domain.ErrInvalidRequest, true
	case "authentication_error":
		return domain.ErrAuthentication, true
	case "permission_error":
		return domain.ErrPermission, true
	case "not_found_error":
		return domain.ErrNotFound, true
	case "rate_limit_error":
		return domain.ErrRateLimit, true
	case "api_error":
		return domain.ErrAPI, true
	case "request_too_large":
		return domain.ErrTooLarge, true
	}
	return "", false
}

func errorTypeFromStatus(status int) domain.ErrorType {
	switch {
	case status == http.StatusUnauthorized:
		return domain.ErrAuthentication
	case status == http.StatusForbidden:
		return domain.ErrPermission
	case status == http.StatusNotFound:
		return domain.ErrNotFound
	case status == http.StatusRequestEntityTooLarge:
		return domain.ErrTooLarge
	case status == http.StatusTooManyRequests:
		return domain.ErrRateLimit
	case status >= 500:
		return domain.ErrAPI
	case status >= 400:
		return domain.ErrInvalidRequest
	default:
		return domain.ErrAPI
	}
}

func retryableType(t domain.ErrorType) bool {
	switch t {
	case domain.ErrRateLimit, domain.ErrOverloaded, domain.ErrAPI, domain.ErrTimeout:
		return true
	case domain.ErrAuthentication:
		// The PROVIDER rejected the gateway's credential: hopeless for this
		// target, but the next target (a different provider, different
		// credential) may serve — retry across targets (checklist C-16).
		return true
	default:
		return false
	}
}

// EncodeError renders a canonical error in the OpenAI envelope. The status
// is returned separately for the HTTP response.
func EncodeError(ge domain.GatewayError) (body []byte, status int) {
	t := string(ge.Type)
	if t == "" {
		t = string(domain.ErrAPI)
	}
	we := wireError{Message: ge.Message, Type: t, Param: ge.Param}
	if ge.Code != "" {
		we.Code = ge.Code
	}
	body, _ = json.Marshal(errorBody{Error: we})
	return body, ge.Status
}
