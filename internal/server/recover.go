package server

import (
	"net/http"
	"runtime/debug"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// panicLogMessage is what clients see when a handler panics. The panic
// value and stack stay in the server log — leaking them into the
// response would disclose internals (paths, provider details, stack
// frames) to callers.
const panicLogMessage = "internal server error"

// recoverPanic middleware: convert handler panics into 500 responses.
//
// Two cases, by design:
//
//   - Panic before any byte is written: render the neutral error
//     envelope. The connection stays alive and usable — the acceptance
//     criterion for p3.http-server. retryable=false: a panic is a
//     OneGate bug, likely deterministic; retrying invites a storm of
//     identical failures.
//
//   - Panic after the response started (mid-stream): a 500 can no
//     longer be delivered — the client would parse it as body content.
//     Log and re-panic: net/http terminates the connection, which is
//     the only honest signal left. The streaming path (p3.stream)
//     owns graceful mid-stream error events; this is the backstop.
func (r *Router) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				r.metrics.ObservePanic()
				traceID := traceIDOf(req)

				sw, ok := w.(*statusWriter)
				if ok && sw.wroteHeader {
					// Response already committed: no clean 500 possible.
					r.opts.Logger.Error("panic after response started",
						"panic", rec,
						"stack", string(debug.Stack()),
						"trace_id", traceID,
					)
					// Re-panic so net/http closes the connection.
					panic(rec)
				}

				r.opts.Logger.Error("panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
					"trace_id", traceID,
				)
				// writeError flows through the outer statusWriter, so
				// the access log records the 500 the client receives.
				writeError(w, domain.GatewayError{
					Status:    http.StatusInternalServerError,
					Type:      domain.ErrInternal,
					Message:   panicLogMessage,
					Retryable: false,
				})
			}
		}()
		next.ServeHTTP(w, req)
	})
}
