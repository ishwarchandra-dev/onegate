package stream

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/ishwarchandra-dev/onegate/internal/protocol/sse"
)

// Writer formats and flushes SSE frames to a client HTTP connection or
// generic io.Writer. It guarantees immediate per-frame flushes and records
// whether any bytes have been transmitted (critical for retry/fallback
// safety: retries are allowed only before first byte reaches client).
type Writer struct {
	mu           sync.Mutex
	rw           http.ResponseWriter
	out          io.Writer
	flusher      http.Flusher
	headersSent  bool
	written      bool
	bytesWritten int64
}

// NewWriter wraps an http.ResponseWriter. If the response writer supports
// http.Flusher, frames are flushed immediately after each write.
func NewWriter(w http.ResponseWriter) *Writer {
	flusher, _ := w.(http.Flusher)
	return &Writer{
		rw:      w,
		out:     w,
		flusher: flusher,
	}
}

// NewCustomWriter wraps a general io.Writer with an optional Flusher
// (used in tests or custom pipeline consumers).
func NewCustomWriter(out io.Writer, flusher http.Flusher) *Writer {
	return &Writer{
		out:     out,
		flusher: flusher,
	}
}

// WriteFrame formats and writes one SSE frame to the client, flushing
// immediately.
func (w *Writer) WriteFrame(f sse.Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ensureHeadersLocked()

	// Format frame wire bytes.
	var b strings.Builder
	if f.Event != "" {
		b.WriteString("event: ")
		b.WriteString(f.Event)
		b.WriteString("\n")
	}

	lines := strings.Split(f.Data, "\n")
	for _, l := range lines {
		b.WriteString("data: ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	n, err := io.WriteString(w.out, b.String())
	if n > 0 {
		w.written = true
		w.bytesWritten += int64(n)
	}
	if err != nil {
		return fmt.Errorf("stream: write frame: %w", err)
	}

	if w.flusher != nil {
		w.flusher.Flush()
	}
	return nil
}

// WriteRaw writes raw bytes to the underlying connection and flushes.
func (w *Writer) WriteRaw(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ensureHeadersLocked()

	n, err := w.out.Write(data)
	if n > 0 {
		w.written = true
		w.bytesWritten += int64(n)
	}
	if err != nil {
		return n, err
	}

	if w.flusher != nil {
		w.flusher.Flush()
	}
	return n, nil
}

// Flush explicitly flushes the writer if a flusher is available.
func (w *Writer) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

// Written reports whether any bytes have been transmitted to the client.
func (w *Writer) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written
}

// BytesWritten reports the total bytes written to the client.
func (w *Writer) BytesWritten() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytesWritten
}

// HeadersSent reports whether SSE headers have been committed.
func (w *Writer) HeadersSent() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.headersSent
}

// ensureHeadersLocked writes the required SSE response headers.
func (w *Writer) ensureHeadersLocked() {
	if w.headersSent {
		return
	}
	w.headersSent = true

	if w.rw == nil {
		return
	}

	h := w.rw.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
	}
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	if h.Get("Connection") == "" {
		h.Set("Connection", "keep-alive")
	}
	h.Set("X-Accel-Buffering", "no")

	w.rw.WriteHeader(http.StatusOK)
}
