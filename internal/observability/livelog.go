package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LogEntry represents one structured log event in the live feed and ring buffer.
type LogEntry struct {
	Timestamp time.Time      `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	TraceID   string         `json:"trace_id,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	Attrs     map[string]any `json:"attrs,omitempty"`
}

// LogFilter defines filtering criteria for ring buffer queries and SSE feeds.
type LogFilter struct {
	MinLevel string `json:"min_level,omitempty"` // "debug" | "info" | "warn" | "error"
	TraceID  string `json:"trace_id,omitempty"`  // exact match
	Provider string `json:"provider,omitempty"`  // exact match
}

// Matches reports whether entry satisfies the filter.
func (f LogFilter) Matches(e LogEntry) bool {
	if f.MinLevel != "" {
		if parseLogLevel(e.Level) < parseLogLevel(f.MinLevel) {
			return false
		}
	}
	if f.TraceID != "" && e.TraceID != f.TraceID {
		return false
	}
	if f.Provider != "" && e.Provider != f.Provider {
		return false
	}
	return true
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// RingBuffer stores a fixed-capacity circular buffer of recent LogEntries in memory.
// Safe for concurrent use.
type RingBuffer struct {
	mu       sync.RWMutex
	capacity int
	entries  []LogEntry
	start    int
	count    int
}

// NewRingBuffer constructs a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		capacity: capacity,
		entries:  make([]LogEntry, capacity),
	}
}

// Push appends an entry to the circular buffer, overwriting the oldest when full.
func (r *RingBuffer) Push(entry LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := (r.start + r.count) % r.capacity
	if r.count == r.capacity {
		r.entries[idx] = entry
		r.start = (r.start + 1) % r.capacity
	} else {
		r.entries[idx] = entry
		r.count++
	}
}

// Recent returns up to limit recent entries matching filter (oldest first).
func (r *RingBuffer) Recent(limit int, filter LogFilter) []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > r.count {
		limit = r.count
	}

	var matched []LogEntry
	for i := 0; i < r.count; i++ {
		idx := (r.start + i) % r.capacity
		e := r.entries[idx]
		if filter.Matches(e) {
			matched = append(matched, e)
		}
	}

	if len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched
}

// Total returns the current number of entries in the ring.
func (r *RingBuffer) Total() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Subscription represents an active SSE client stream listener.
type Subscription struct {
	id      int64
	filter  LogFilter
	ch      chan LogEntry
	dropped atomic.Int64
}

// Channel returns the receive-only channel for log entries.
func (s *Subscription) Channel() <-chan LogEntry {
	return s.ch
}

// DroppedCount returns the number of dropped events.
func (s *Subscription) DroppedCount() int64 {
	return s.dropped.Load()
}

// LogHub coordinates the live log ring buffer and active SSE dashboard subscribers.
type LogHub struct {
	ring        *RingBuffer
	mu          sync.RWMutex
	subscribers map[int64]*Subscription
	nextID      int64
}

// NewLogHub creates a new LogHub with a ring buffer of capacity.
func NewLogHub(capacity int) *LogHub {
	return &LogHub{
		ring:        NewRingBuffer(capacity),
		subscribers: make(map[int64]*Subscription),
	}
}

// Ring returns the underlying ring buffer.
func (h *LogHub) Ring() *RingBuffer { return h.ring }

// Publish stores an entry in the ring buffer and dispatches to matching subscribers.
// Non-blocking: slow subscribers have events dropped and their drop count incremented.
func (h *LogHub) Publish(entry LogEntry) {
	h.ring.Push(entry)

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subscribers {
		if sub.filter.Matches(entry) {
			select {
			case sub.ch <- entry:
			default:
				// Slow dashboard subscriber never backpressures the proxy: drop and notify
				sub.dropped.Add(1)
			}
		}
	}
}

// Subscribe registers a new subscriber with filter and buffer size.
// Returns the subscription and an unsubscribe cancellation function.
func (h *LogHub) Subscribe(filter LogFilter, bufferSize int) (*Subscription, func()) {
	if bufferSize <= 0 {
		bufferSize = 100
	}

	h.mu.Lock()
	h.nextID++
	id := h.nextID
	sub := &Subscription{
		id:     id,
		filter: filter,
		ch:     make(chan LogEntry, bufferSize),
	}
	h.subscribers[id] = sub
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		delete(h.subscribers, id)
		h.mu.Unlock()
	}
	return sub, unsubscribe
}

// ServeHTTP serves the Server-Sent Events (SSE) live log feed.
// Query parameters:
//   - min_level: minimum log level ("debug", "info", "warn", "error");
//     "level" is accepted as a legacy alias (pre-p6 clients)
//   - trace_id: filter by specific trace ID
//   - provider: filter by provider ID
//   - history: number of historical entries to pre-populate from ring (default: 50, max: 500)
func (h *LogHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported by client connection", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	minLevel := query.Get("min_level")
	if minLevel == "" {
		minLevel = query.Get("level")
	}
	filter := LogFilter{
		MinLevel: minLevel,
		TraceID:  query.Get("trace_id"),
		Provider: query.Get("provider"),
	}

	historyLimit := 50
	if hStr := query.Get("history"); hStr != "" {
		if n, err := strconv.Atoi(hStr); err == nil && n >= 0 {
			if n > 500 {
				n = 500
			}
			historyLimit = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 1. Emit recent history from ring buffer
	if historyLimit > 0 {
		recent := h.ring.Recent(historyLimit, filter)
		for _, entry := range recent {
			data, err := json.Marshal(entry)
			if err == nil {
				fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			}
		}
		flusher.Flush()
	}

	// 2. Subscribe for live events
	sub, unsubscribe := h.Subscribe(filter, 100)
	defer unsubscribe()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-ticker.C:
			// Periodic ping to keep SSE connection alive through proxies
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()

		case entry, ok := <-sub.ch:
			if !ok {
				return
			}
			// Acceptance: Slow dashboard subscriber never backpressures the proxy (drops, notifies)
			if dropped := sub.dropped.Swap(0); dropped > 0 {
				fmt.Fprintf(w, "event: dropped\ndata: {\"dropped\":%d}\n\n", dropped)
			}
			data, err := json.Marshal(entry)
			if err == nil {
				fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			}
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// Logging Layer Redaction Integration
// ---------------------------------------------------------------------------

// HubHandler connects slog to a LogHub while preserving Handler-layer redaction.
type HubHandler struct {
	inner slog.Handler
	hub   *LogHub
}

func (h *HubHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *HubHandler) Handle(ctx context.Context, r slog.Record) error {
	// First let inner handler process (e.g. stdout formatting with redaction)
	err := h.inner.Handle(ctx, r)

	if h.hub == nil {
		return err
	}

	// Build LogEntry with guaranteed redaction
	entry := LogEntry{
		Timestamp: r.Time,
		Level:     strings.ToLower(r.Level.String()),
		Message:   r.Message,
		TraceID:   TraceID(ctx),
		Attrs:     make(map[string]any),
	}

	r.Attrs(func(a slog.Attr) bool {
		// Acceptance: Redaction applies at the logging layer, inherited by the feed
		ra := redactAttrs(nil, a)
		if ra.Key == "trace_id" && entry.TraceID == "" {
			entry.TraceID = ra.Value.String()
		} else if ra.Key == "provider" || ra.Key == "provider_id" {
			entry.Provider = ra.Value.String()
		}
		entry.Attrs[ra.Key] = ra.Value.Any()
		return true
	})

	h.hub.Publish(entry)
	return err
}

func (h *HubHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &HubHandler{
		inner: h.inner.WithAttrs(attrs),
		hub:   h.hub,
	}
}

func (h *HubHandler) WithGroup(name string) slog.Handler {
	return &HubHandler{
		inner: h.inner.WithGroup(name),
		hub:   h.hub,
	}
}
