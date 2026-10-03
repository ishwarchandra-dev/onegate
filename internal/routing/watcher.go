// Package routing registry watcher and hot reload pipeline.
package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// ChangeEvent is published whenever a storage change triggers a registry swap.
type ChangeEvent struct {
	OldSnapshot *Snapshot
	NewSnapshot *Snapshot
	TimestampMS int64
}

// WatcherConfig configures the storage change watcher.
type WatcherConfig struct {
	Registry     *Registry
	Source       StorageSource
	PollInterval time.Duration
	Logger       *slog.Logger
	OnChange     func(ChangeEvent)
}

// Watcher monitors storage for changes to models, providers, and routing rules.
// When changes are detected, it atomically swaps the registry snapshot pointer.
//
// In-flight requests holding a previously acquired Snapshot continue to completion
// on that snapshot; new requests immediately observe the new Snapshot.
type Watcher struct {
	reg      *Registry
	src      StorageSource
	pollInt  time.Duration
	logger   *slog.Logger
	onChange func(ChangeEvent)

	mu       sync.Mutex
	lastHash string
	stop     chan struct{}
	stopped  sync.Once
}

// NewWatcher creates a Watcher instance.
func NewWatcher(cfg WatcherConfig) *Watcher {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 1 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Watcher{
		reg:      cfg.Registry,
		src:      cfg.Source,
		pollInt:  cfg.PollInterval,
		logger:   cfg.Logger,
		onChange: cfg.OnChange,
		stop:     make(chan struct{}),
	}
}

// computeFingerprint generates a stable SHA256 checksum across all routing entities.
func computeFingerprint(models []domain.Model, providers []domain.Provider, rules []domain.RoutingRule) string {
	h := sha256.New()
	bModels, _ := json.Marshal(models)
	bProvs, _ := json.Marshal(providers)
	bRules, _ := json.Marshal(rules)

	h.Write(bModels)
	h.Write(bProvs)
	h.Write(bRules)
	return hex.EncodeToString(h.Sum(nil))
}

// ReloadNow forces an immediate check against storage and swaps the registry snapshot
// if entities have changed.
func (w *Watcher) ReloadNow(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	models, err := w.src.ListModels()
	if err != nil {
		return fmt.Errorf("watcher: list models: %w", err)
	}

	providers, err := w.src.ListProviders()
	if err != nil {
		return fmt.Errorf("watcher: list providers: %w", err)
	}

	rules, err := w.src.ListRules()
	if err != nil {
		return fmt.Errorf("watcher: list rules: %w", err)
	}

	fingerprint := computeFingerprint(models, providers, rules)
	if fingerprint == w.lastHash && w.lastHash != "" {
		// No changes detected
		return nil
	}

	oldSnap := w.reg.Snapshot()
	w.reg.Load(models, providers, rules)
	newSnap := w.reg.Snapshot()
	w.lastHash = fingerprint

	w.logger.InfoContext(ctx, "routing registry hot-reloaded from storage",
		slog.Int("models", len(models)),
		slog.Int("providers", len(providers)),
		slog.Int("rules", len(rules)),
	)

	if w.onChange != nil {
		w.onChange(ChangeEvent{
			OldSnapshot: oldSnap,
			NewSnapshot: newSnap,
			TimestampMS: time.Now().UnixMilli(),
		})
	}

	return nil
}

// Start launches the background polling loop. It runs until ctx is cancelled or Stop is called.
func (w *Watcher) Start(ctx context.Context) {
	// Perform initial load
	if err := w.ReloadNow(ctx); err != nil {
		w.logger.ErrorContext(ctx, "initial routing reload failed", slog.String("error", err.Error()))
	}

	ticker := time.NewTicker(w.pollInt)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-ticker.C:
				if err := w.ReloadNow(ctx); err != nil {
					w.logger.WarnContext(ctx, "background routing reload error", slog.String("error", err.Error()))
				}
			}
		}
	}()
}

// Stop shuts down background polling.
func (w *Watcher) Stop() {
	w.stopped.Do(func() {
		close(w.stop)
	})
}
