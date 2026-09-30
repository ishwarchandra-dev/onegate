package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

// HeaderRequestID is the request/response header carrying the trace ID.
// OmniRoute v3.8.52 used x-request-id (lowercase on the wire); Go
// canonicalizes to X-Request-Id, which is byte-identical over HTTP/1.1.
const HeaderRequestID = "X-Request-Id"

// requestIDPrefix namespaces gateway-generated IDs so they are visibly
// distinct from client-supplied ones in logs.
const requestIDPrefix = "req-"

// requestIDLen is the random entropy of generated IDs in bytes
// (16 bytes -> 32 hex chars).
const requestIDLen = 16

// requestID validates or generates the trace ID for a request.
//
// A client-supplied X-Request-Id is honored when it is safe: 1-64 chars
// from [A-Za-z0-9._:-]. Anything else (control characters, whitespace,
// over-long values, exotic Unicode) is replaced with a generated ID.
// Honoring well-formed client IDs keeps distributed tracing usable;
// rejecting the rest prevents header/response-splitting and log injection.
func requestID(incoming string) string {
	if validRequestID(incoming) {
		return incoming
	}
	return newRequestID()
}

// validRequestID reports whether s is a safe client-supplied request ID.
func validRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

// newRequestID generates "req-" + 32 hex chars of crypto randomness.
// Cryptographic (not pseudo) randomness: request IDs appear in logs and
// responses, and predictable IDs make cross-tenant log forgery possible.
func newRequestID() string {
	b := make([]byte, requestIDLen)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on Linux after init; if it somehow
		// does, fail closed with a constant — still unique enough to
		// grep, never misleadingly empty.
		return requestIDPrefix + "unavailable0000000000000000000000"
	}
	return requestIDPrefix + hex.EncodeToString(b)
}

// requestID middleware: assign the trace ID, expose it to handlers via
// the request context (observability.TraceID), and echo it on the
// response so clients can correlate support tickets with logs.
func (r *Router) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id := requestID(req.Header.Get(HeaderRequestID))
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, req.WithContext(observability.WithTraceID(req.Context(), id)))
	})
}
