package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// ChangeEvent is published to subscribers after a successful reload.
// ChangedFields lists top-level field names that differ (host, port,
// data_dir, log_level, reload).
type ChangeEvent struct {
	Old           Config
	New           Config
	ChangedFields []string
}

// Watcher hot-reloads configuration: on SIGHUP and on file modification
// (mtime polling). It swaps the active config atomically; a failed reload
// keeps the last-good config and logs the error. In-flight readers that
// captured the old value finish with it (snapshot semantics).
type Watcher struct {
	path    string
	pollMS  int
	current *atomicConfig
	logger  *slog.Logger

	mu          sync.Mutex
	subscribers []chan ChangeEvent
	lastSum     string
	stop        chan struct{}
	stopped     sync.Once
}

// atomicConfig holds the current config behind a mutex-light snapshot.
type atomicConfig struct {
	mu sync.RWMutex
	c  Config
}

func (a *atomicConfig) Load() Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.c
}

func (a *atomicConfig) Store(c Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.c = c
}

// NewWatcher creates a watcher for the given config path. If path is empty
// (no file in use), only SIGHUP-triggered env reload is possible and file
// polling is disabled.
func NewWatcher(path string, initial Config, pollMS int, logger *slog.Logger) *Watcher {
	if logger == nil {
		logger = slog.Default()
	}
	if pollMS < 100 {
		pollMS = 100
	}
	sum := fileSum(path)
	return &Watcher{
		path:    path,
		pollMS:  pollMS,
		current: &atomicConfig{c: initial},
		logger:  logger,
		lastSum: sum,
		stop:    make(chan struct{}),
	}
}

// Current returns the active config snapshot.
func (w *Watcher) Current() Config {
	return w.current.Load()
}

// Subscribe registers a channel receiving change events. The channel is
// dropped (never blocks the watcher) if the subscriber is slow; use a
// buffered channel. Returns a cancel function.
func (w *Watcher) Subscribe(ch chan ChangeEvent) (cancel func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.subscribers = append(w.subscribers, ch)
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		for i, s := range w.subscribers {
			if s == ch {
				w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
				return
			}
		}
	}
}

// Start launches the SIGHUP listener and mtime poller until ctx is done.
// It returns immediately; failures are logged, never fatal — a broken
// watcher must not take down the gateway.
func (w *Watcher) Start(ctx context.Context) {
	go w.watchSignals(ctx)
	if w.path != "" {
		go w.watchFile(ctx)
	}
}

func (w *Watcher) watchSignals(ctx context.Context) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-sigCh:
			w.logger.Info("config reload: SIGHUP received")
			w.ReloadNow()
		}
	}
}

func (w *Watcher) watchFile(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(w.pollMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
			sum := fileSum(w.path)
			w.mu.Lock()
			changed := sum != w.lastSum && sum != ""
			if changed {
				w.lastSum = sum
			}
			w.mu.Unlock()
			if changed {
				w.logger.Info("config reload: file changed", "path", w.path)
				w.ReloadNow()
			}
		}
	}
}

// ReloadNow re-reads the config file and env. On success the active config
// is swapped and subscribers are notified; on failure the last-good config
// stays active.
func (w *Watcher) ReloadNow() (Config, error) {
	newCfg, err := Load(LoadOptions{Path: w.path})
	if err != nil {
		w.logger.Error("config reload failed; keeping last-good config", "error", err)
		return w.Current(), err
	}
	old := w.Current()
	if old == newCfg {
		return newCfg, nil
	}
	w.current.Store(newCfg)
	evt := ChangeEvent{Old: old, New: newCfg, ChangedFields: diffFields(old, newCfg)}
	w.logger.Info("config reloaded",
		"path", w.path, "changed", evt.ChangedFields)

	w.mu.Lock()
	subs := make([]chan ChangeEvent, len(w.subscribers))
	copy(subs, w.subscribers)
	w.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default: // slow subscriber: drop rather than block the hot path
		}
	}
	return newCfg, nil
}

// Stop halts all watcher goroutines. Safe to call multiple times.
func (w *Watcher) Stop() {
	w.stopped.Do(func() { close(w.stop) })
}

// diffFields lists top-level fields that differ between two configs.
func diffFields(a, b Config) []string {
	var out []string
	if a.Host != b.Host {
		out = append(out, "host")
	}
	if a.Port != b.Port {
		out = append(out, "port")
	}
	if a.DataDir != b.DataDir {
		out = append(out, "data_dir")
	}
	if a.LogLevel != b.LogLevel {
		out = append(out, "log_level")
	}
	if a.Reload != b.Reload {
		out = append(out, "reload")
	}
	return out
}

// fileSum returns a short content hash of the file at path ("" if absent).
func fileSum(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// Ensure DataDir exists and is a directory (idempotent).
func EnsureDataDir(cfg Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}
	return nil
}
