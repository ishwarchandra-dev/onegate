package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTraceHandler_AutoInjectsTraceID(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)

	ctx := WithTraceID(context.Background(), "trace-xyz-789")
	l.InfoContext(ctx, "inbound request received", "route", "/v1/chat/completions")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if rec["trace_id"] != "trace-xyz-789" {
		t.Errorf("expected trace_id trace-xyz-789, got %v", rec["trace_id"])
	}
	if rec["msg"] != "inbound request received" {
		t.Errorf("expected msg 'inbound request received', got %v", rec["msg"])
	}
}

func TestTraceHandler_NoDuplicateWhenTraceAttrPresent(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)

	ctx := WithTraceID(context.Background(), "trace-dedup-123")
	// Call site explicitly passes TraceAttr(ctx) as well
	l.InfoContext(ctx, "explicit trace attr", TraceAttr(ctx), "provider", "openai")

	out := buf.String()
	// Count occurrences of "trace_id"
	count := strings.Count(out, `"trace_id"`)
	if count != 1 {
		t.Errorf("expected exactly 1 trace_id attribute in JSON, got %d: %s", count, out)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec["trace_id"] != "trace-dedup-123" {
		t.Errorf("expected trace_id trace-dedup-123, got %v", rec["trace_id"])
	}
}

func TestTraceHandler_NoTraceIDWhenEmptyContext(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)

	l.InfoContext(context.Background(), "background job without trace")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := rec["trace_id"]; ok {
		t.Errorf("expected no trace_id for background context, got %v", rec["trace_id"])
	}
}

func TestTraceHandler_WithGroupAndWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("debug", &buf)

	ctx := WithTraceID(context.Background(), "trace-nested-456")

	// 1. With attributes: trace_id remains at root alongside attrs
	buf.Reset()
	withAttr := l.With("app", "onegate")
	withAttr.InfoContext(ctx, "service log", "status", 200)

	var rec1 map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec1["trace_id"] != "trace-nested-456" {
		t.Errorf("expected root trace_id trace-nested-456, got %v", rec1["trace_id"])
	}
	if rec1["app"] != "onegate" {
		t.Errorf("expected app onegate, got %v", rec1["app"])
	}

	// 2. WithGroup: trace_id scoped within group
	buf.Reset()
	withGrp := l.WithGroup("upstream")
	withGrp.InfoContext(ctx, "calling provider", "status", 500)

	var rec2 map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	grp, ok := rec2["upstream"].(map[string]any)
	if !ok {
		t.Fatalf("expected upstream group, got %T", rec2["upstream"])
	}
	if grp["trace_id"] != "trace-nested-456" {
		t.Errorf("expected upstream.trace_id trace-nested-456, got %v", grp["trace_id"])
	}
	if grp["status"] != float64(500) {
		t.Errorf("expected upstream.status 500, got %v", grp["status"])
	}
}

func TestTraceHandler_PreservesRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger("info", &buf)

	ctx := WithTraceID(context.Background(), "trace-redact-999")
	l.InfoContext(ctx, "attempt failed",
		"provider_api_key", "sk-secret-live-token",
		"trace_id", "trace-redact-999",
	)

	out := buf.String()
	if strings.Contains(out, "sk-secret-live-token") {
		t.Fatalf("secret leaked in log output:\n%s", out)
	}
	if !strings.Contains(out, `"provider_api_key":"***REDACTED***"`) {
		t.Fatalf("redaction marker missing:\n%s", out)
	}
}
