package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/observability"
)

// statusWriter records what the inner handlers actually wrote: the
// status code (1xx informational writes collapse to the final code) and
// the byte count. Recover also uses wroteHeader to decide whether a 500
// is still renderable.
type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func newStatusWriter(w http.ResponseWriter) *statusWriter {
	// Go writes an implicit 200 when handlers write without calling
	// WriteHeader.
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush lets streaming handlers flush through the wrapper (p3.stream).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// accessLog middleware: one structured line per request. 5xx responses
// log at error level so failures are greppable without lowering the
// signal threshold for the normal traffic stream.
func (r *Router) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		sw := newStatusWriter(w)

		defer func() {
			r.metrics.ObserveRequest(req.Method, sw.status)
			level := slog.LevelInfo
			if sw.status >= 500 {
				level = slog.LevelError
			}
			r.opts.Logger.Log(req.Context(), level, "http_request",
				"method", req.Method,
				"path", req.URL.Path,
				"status", sw.status,
				"bytes", sw.bytes,
				"duration_ms", float64(time.Since(start).Microseconds())/1000.0,
				"remote_addr", req.RemoteAddr,
				"trace_id", traceIDOf(req),
			)
		}()

		next.ServeHTTP(sw, req)
	})
}

// traceIDOf is a tiny seam for tests: it reads the effective trace ID
// that the outer RequestID middleware put into the context.
func traceIDOf(req *http.Request) string { return observability.TraceID(req.Context()) }
