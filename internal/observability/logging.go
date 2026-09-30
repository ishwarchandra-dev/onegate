// Package observability owns logging, trace propagation, and (in Phase 5)
// metrics and the live-log feed.
//
// Logging rules enforced here:
//   - JSON structured logs to the given writer (stdout in production).
//   - Redaction happens at the handler layer: any attribute whose key
//     looks like a credential is masked before serialization. Call sites
//     cannot bypass this by "forgetting" to redact.
//   - Trace IDs ride in the context and are attached to log lines via
//     TraceAttr / the logger helper in this package.
package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// secretKeyFragments are lower-case substrings that mark an attribute key
// as sensitive. Matched against the key's last path segment ("req.api_key"
// -> "api_key" is checked).
var secretKeyFragments = []string{
	"api_key", "apikey", "authorization", "auth", "token", "secret",
	"password", "passwd", "credential", "key_hash", "cookie", "session_id",
}

// redactedValue replaces secret attribute values.
const redactedValue = "***REDACTED***"

// isSecretKey reports whether an attribute key names sensitive material.
func isSecretKey(key string) bool {
	// normalize: take the last dotted segment, lowercase, hyphens to
	// underscores ("x-api-key" ~ "x_api_key" ~ "apiKey" families all match)
	if i := strings.LastIndex(key, "."); i >= 0 {
		key = key[i+1:]
	}
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, frag := range secretKeyFragments {
		if strings.Contains(key, frag) {
			return true
		}
	}
	// exact "key" is sensitive too (e.g. provider api key attrs)
	return key == "key"
}

// redactAttrs returns a ReplaceAttr function masking secret values.
// It recurses into groups. Non-string secret values (ints, bools) are
// masked as well — a secret that happens to be numeric is still a secret.
func redactAttrs(groups []string, a slog.Attr) slog.Attr {
	if isSecretKey(a.Key) {
		return slog.Any(a.Key, redactedValue)
	}
	if a.Value.Kind() == slog.KindGroup {
		grp := a.Value.Group()
		masked := make([]slog.Attr, len(grp))
		for i, ga := range grp {
			masked[i] = redactAttrs(append(groups, a.Key), ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(masked...)}
	}
	return a
}

// NewLogger builds the process logger. level: debug|info|warn|error.
func NewLogger(level string, w io.Writer) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       lv,
		ReplaceAttr: redactAttrs,
	}))
}

// ---------------------------------------------------------------------------
// Trace IDs
// ---------------------------------------------------------------------------

type traceIDKey struct{}

// WithTraceID returns a context carrying the trace ID.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceID extracts the trace ID from the context ("" when absent).
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey{}).(string); ok {
		return v
	}
	return ""
}

// TraceAttr returns the standard log attribute for the trace ID.
func TraceAttr(ctx context.Context) slog.Attr {
	return slog.String("trace_id", TraceID(ctx))
}

// WithTrace returns a logger that always emits the context's trace ID.
// Usage: observability.WithTrace(ctx, logger).Info("routing decided", ...)
func WithTrace(ctx context.Context, l *slog.Logger) *slog.Logger {
	return l.With(TraceAttr(ctx))
}
