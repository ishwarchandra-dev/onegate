package gemini

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// DecodeError converts a Gemini error body (plus HTTP status) into a
// canonical gateway error. Classification rides on the gRPC status string
// (mapping doc §3.4).
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
		Code:    eb.Error.Status,
	}
	// The body status string wins — except that HTTP 429 always
	// classifies as a rate limit (checklist C-17: status beats a generic
	// body classification).
	if t, ok := typeFromString(eb.Error.Status); ok && status != http.StatusTooManyRequests {
		ge.Type = t
	} else {
		ge.Type = statusType(status)
	}
	ge.Retryable = retryable(ge.Type)
	return ge
}

func typeFromString(s string) (domain.ErrorType, bool) {
	switch s {
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION":
		return domain.ErrInvalidRequest, true
	case "UNAUTHENTICATED":
		return domain.ErrAuthentication, true
	case "PERMISSION_DENIED":
		return domain.ErrPermission, true
	case "NOT_FOUND":
		return domain.ErrNotFound, true
	case "RESOURCE_EXHAUSTED":
		return domain.ErrRateLimit, true
	case "UNAVAILABLE":
		// "The model is overloaded" arrives as UNAVAILABLE (503)
		// [quirk:gemini-overloaded-unavailable].
		return domain.ErrOverloaded, true
	case "INTERNAL":
		return domain.ErrAPI, true
	}
	return "", false
}

func statusType(status int) domain.ErrorType {
	switch {
	case status == 401:
		return domain.ErrAuthentication
	case status == 403:
		return domain.ErrPermission
	case status == 404:
		return domain.ErrNotFound
	case status == 413:
		return domain.ErrTooLarge
	case status == 429:
		return domain.ErrRateLimit
	case status == 503:
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
	case domain.ErrAuthentication:
		// The PROVIDER rejected the gateway's credential: hopeless for this
		// target, but the next target (a different provider, different
		// credential) may serve — retry across targets (checklist C-16).
		return true
	default:
		return false
	}
}

// EncodeError renders a canonical error in the Gemini envelope.
func EncodeError(ge domain.GatewayError) (body []byte, status int) {
	status = ge.Status
	if status == 0 {
		status = 500
	}
	we := wireErrorObj{Code: status, Message: ge.Message}
	switch ge.Type {
	case domain.ErrInvalidRequest:
		we.Status = "INVALID_ARGUMENT"
	case domain.ErrAuthentication:
		we.Status = "UNAUTHENTICATED"
	case domain.ErrPermission:
		we.Status = "PERMISSION_DENIED"
	case domain.ErrNotFound:
		we.Status = "NOT_FOUND"
	case domain.ErrTooLarge:
		we.Status = "INVALID_ARGUMENT"
	case domain.ErrRateLimit:
		we.Status = "RESOURCE_EXHAUSTED"
	case domain.ErrOverloaded:
		we.Status = "UNAVAILABLE"
	case domain.ErrTimeout, domain.ErrCancelled:
		we.Status = "DEADLINE_EXCEEDED"
	default:
		we.Status = "INTERNAL"
	}
	body, _ = json.Marshal(errorEnvelope{Error: we})
	return body, status
}
