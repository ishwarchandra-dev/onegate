package routing

// Client-facing classification of routing sentinels (p7.parity-fixes,
// checklist C-10..C-12, C-23). Lives here — not in the proxy — because
// the proxy layer may not import routing (layering contract); the
// composition root and test resolvers call this once at the seam and
// wrap the result in their resolver-error type.
import (
	"errors"
	"net/http"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// ClientError maps a DecideTargets failure onto the client error
// contract. Unknown failures degrade to 503 overloaded_error, matching
// OmniRoute's posture for routing outages.
func ClientError(model string, err error) domain.GatewayError {
	switch {
	case errors.Is(err, ErrModelNotFound):
		return domain.GatewayError{
			Status:  http.StatusNotFound,
			Type:    domain.ErrNotFound,
			Message: "model not found: " + model,
		}
	case errors.Is(err, ErrScopeModelDenied):
		return domain.GatewayError{
			Status:  http.StatusForbidden,
			Type:    domain.ErrPermission,
			Message: "model not allowed by virtual key scope",
		}
	case errors.Is(err, ErrScopeProviderDenied):
		return domain.GatewayError{
			Status:  http.StatusForbidden,
			Type:    domain.ErrPermission,
			Message: "all provider targets denied by virtual key scope",
		}
	case errors.Is(err, ErrCapabilityMismatch):
		return domain.GatewayError{
			Status:  http.StatusBadRequest,
			Type:    domain.ErrInvalidRequest,
			Message: "no targets satisfy required capabilities",
		}
	case errors.Is(err, ErrNoHealthyTargets):
		return domain.GatewayError{
			Status:  http.StatusServiceUnavailable,
			Type:    domain.ErrOverloaded,
			Message: "no healthy targets available",
		}
	case errors.Is(err, ErrNoTargets), errors.Is(err, ErrNilRegistry):
		return domain.GatewayError{
			Status:  http.StatusServiceUnavailable,
			Type:    domain.ErrOverloaded,
			Message: "no targets configured for model",
		}
	default:
		return domain.GatewayError{
			Status:  http.StatusServiceUnavailable,
			Type:    domain.ErrOverloaded,
			Message: "routing error: " + err.Error(),
		}
	}
}
