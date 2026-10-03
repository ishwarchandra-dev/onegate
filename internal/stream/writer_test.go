package stream

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

type mockFlusher struct {
	bytes.Buffer
	flushed int
}

func (m *mockFlusher) Flush() {
	m.flushed++
}

func TestWriterHeadersAndFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewWriter(rec)

	if w.Written() {
		t.Fatal("expected Written() to be false before any write")
	}
	if w.HeadersSent() {
		t.Fatal("expected HeadersSent() to be false before write")
	}

	frame := sse.Frame{
		Event: "custom_event",
		Data:  "line 1\nline 2",
	}

	if err := w.WriteFrame(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	if !w.Written() {
		t.Fatal("expected Written() to be true after write")
	}
	if !w.HeadersSent() {
		t.Fatal("expected HeadersSent() to be true after write")
	}
	if w.BytesWritten() == 0 {
		t.Fatal("expected BytesWritten() > 0")
	}

	resp := rec.Result()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("content-type: got %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("cache-control: got %q, want no-cache", cc)
	}
	if xaccel := resp.Header.Get("X-Accel-Buffering"); xaccel != "no" {
		t.Errorf("x-accel-buffering: got %q, want no", xaccel)
	}

	body := rec.Body.String()
	want := "event: custom_event\ndata: line 1\ndata: line 2\n\n"
	if body != want {
		t.Fatalf("got body %q, want %q", body, want)
	}
}

func TestWriterCustomFlusher(t *testing.T) {
	mf := &mockFlusher{}
	w := NewCustomWriter(mf, mf)

	if err := w.WriteFrame(sse.Frame{Data: "chunk 1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.WriteFrame(sse.Frame{Data: "chunk 2"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if mf.flushed != 2 {
		t.Fatalf("expected 2 flushes, got %d", mf.flushed)
	}

	expected := "data: chunk 1\n\ndata: chunk 2\n\n"
	if mf.String() != expected {
		t.Fatalf("want %q, got %q", expected, mf.String())
	}
}

func TestWriterConcurrentSafe(t *testing.T) {
	var buf bytes.Buffer
	w := NewCustomWriter(&buf, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = w.WriteFrame(sse.Frame{Data: "concurrent message"})
		}(i)
	}
	wg.Wait()

	if !w.Written() {
		t.Fatal("expected written to be true")
	}
	if w.BytesWritten() == 0 {
		t.Fatal("expected bytes written > 0")
	}
}
