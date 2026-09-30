package anthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeError converts an Anthropic error body (plus HTTP status) into a
// canonical gateway error. The Anthropic type names match the canonical
// taxonomy by design (mapping doc §2.4).
func DecodeError(body []byte, status int) domain.GatewayError {
	var eb errorEnvelope
	if err := json.Unmarshal(body, &eb); err != nil || eb.Error.Message == "" {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		if msg == "" {
			msg = fmt.Sprintf("upstream returned HTTP %d", status)
		}
		t := statusType(status)
		return domain.GatewayError{Status: status, Type: t, Message: msg, Retryable: retryable(t)}
	}

	ge := domain.GatewayError{
		Status:  status,
		Message: eb.Error.Message,
	}
	if t, ok := typeFromString(eb.Error.Type); ok {
		ge.Type = t
	} else {
		ge.Type = statusType(status)
	}
	ge.Retryable = retryable(ge.Type)
	return ge
}

func typeFromString(s string) (domain.ErrorType, bool) {
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
	case "overloaded_error":
		return domain.ErrOverloaded, true
	case "api_error":
		return domain.ErrAPI, true
	case "request_too_large":
		return domain.ErrTooLarge, true
	}
	return "", false
}

func statusType(status int) domain.ErrorType {
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
	case status == http.StatusServiceUnavailable:
		return domain.ErrOverloaded
	case status >= 500:
		return domain.ErrAPI
	case status >= 400:
		return domain.ErrInvalidRequest
	default:
		return domain.ErrAPI
	}
}

func retryable(t domain.ErrorType) bool {
	switch t {
	case domain.ErrRateLimit, domain.ErrOverloaded, domain.ErrAPI, domain.ErrTimeout:
		return true
	default:
		return false
	}
}

// EncodeError renders a canonical error in the Anthropic envelope.
func EncodeError(ge domain.GatewayError) (body []byte, status int) {
	t := string(ge.Type)
	if t == "" {
		t = string(domain.ErrAPI)
	}
	body, _ = json.Marshal(errorEnvelope{
		Type:  "error",
		Error: errorPayload{Type: t, Message: ge.Message},
	})
	return body, ge.Status
}
