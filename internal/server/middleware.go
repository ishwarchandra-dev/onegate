package server

import "net/http"

// Middleware assembly. The chain order is a documented, tested contract
// (see package comment and docs/adr/005-http-server.md).

// chain wraps next in the full middleware stack.
//
// RequestID -> AccessLog -> Recover -> next
func (r *Router) chain(next http.Handler) http.Handler {
	return r.requestID(
		r.accessLog(
			r.recoverPanic(next),
		),
	)
}
