package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactionMasksSecretAttributes(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)

	l.Info("provider saved",
		"api_key", "sk-live-SECRETVALUE",
		"Authorization", "Bearer SECRET",
		"provider.token", "tok-SECRET",
		"nested", slog.GroupValue(slog.String("session_id", "SECRET")),
		"name", "openai",
		"retries", 3,
	)

	out := buf.String()
	if strings.Contains(out, "SECRETVALUE") ||
		strings.Contains(out, "SECRET") {
		t.Fatalf("secret leaked into logs:\n%s", out)
	}
	if !strings.Contains(out, "***REDACTED***") {
		t.Fatalf("redaction marker missing:\n%s", out)
	}
	if !strings.Contains(out, "openai") || !strings.Contains(out, `"retries":3`) {
		t.Fatalf("non-secret values must survive:\n%s", out)
	}
}

func TestRedactionCoversGroupMembers(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)
	l.Info("req", "headers", slog.GroupValue(
		slog.String("x-api-key", "SECRET"),
		slog.String("content-type", "application/json"),
	))
	out := buf.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("group member leaked: %s", out)
	}
	if !strings.Contains(out, "application/json") {
		t.Fatalf("non-secret group member lost: %s", out)
	}
}

func TestTraceIDContextRoundTrip(t *testing.T) {
	ctx := WithTraceID(context.Background(), "tr-123")
	if got := TraceID(ctx); got != "tr-123" {
		t.Fatalf("TraceID: want tr-123, got %q", got)
	}
	if got := TraceID(context.Background()); got != "" {
		t.Fatalf("empty ctx should yield \"\", got %q", got)
	}
}

func TestWithTraceAttachesTraceID(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("debug", &buf)
	ctx := WithTraceID(context.Background(), "tr-abc")
	WithTrace(ctx, l).Info("hello", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if rec["trace_id"] != "tr-abc" {
		t.Fatalf("trace_id not attached: %v", rec)
	}
}

func TestLevels(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("error", &buf)
	l.Info("should not appear")
	if buf.Len() != 0 {
		t.Fatalf("info leaked at error level: %s", buf.String())
	}
	l.Error("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Fatal("error log missing")
	}
}
