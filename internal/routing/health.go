// Package routing target health tracking and circuit breaking state machine.
package routing

import (
	"sync"
	"time"
)

// CircuitState represents the operational health status of an upstream target.
type CircuitState string

const (
	// StateClosed indicates normal healthy operation: all traffic is routed.
	StateClosed CircuitState = "closed"
	// StateOpen indicates circuit is tripped: target is in cooldown and traffic is blocked.
	StateOpen CircuitState = "open"
	// StateHalfOpen indicates cooldown has elapsed: probe traffic is admitted to test recovery.
	StateHalfOpen CircuitState = "half_open"
)

// HealthEvent records a transition in target circuit state.
type HealthEvent struct {
	TimestampMS         int64        `json:"timestamp_ms"`
	ProviderID          string       `json:"provider_id"`
	Model               string       `json:"model"`
	OldState            CircuitState `json:"old_state"`
	NewState            CircuitState `json:"new_state"`
	Reason              string       `json:"reason"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
}

// HealthConfig configures threshold and timing policies for target health.
type HealthConfig struct {
	// ConsecutiveFailures threshold to trip the circuit from closed to open (default 3).
	ConsecutiveFailures int
	// Cooldown is the duration the circuit remains open before entering half-open (default 10s).
	Cooldown time.Duration
	// MaxCooldown caps exponential backoff on repeated circuit trips (default 5m).
	MaxCooldown time.Duration
	// SuccessThreshold is consecutive successes in half-open required to recover to closed (default 2).
	SuccessThreshold int
	// MaxEventLog caps the in-memory health event ring buffer (default 1000).
	MaxEventLog int
}

// DefaultHealthConfig provides production defaults for LLM target health tracking.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		ConsecutiveFailures: 3,
		Cooldown:            10 * time.Second,
		MaxCooldown:         5 * time.Minute,
		SuccessThreshold:    2,
		MaxEventLog:         1000,
	}
}

type targetHealthState struct {
	state                CircuitState
	consecutiveFailures  int
	consecutiveSuccesses int
	lastStateChange      time.Time
	currentCooldown      time.Duration
	inFlightProbes       int
}

// HealthTracker manages per-target circuit breakers and an observable event log.
// It implements the HealthView interface and is thread-safe.
type HealthTracker struct {
	cfg       HealthConfig
	mu        sync.RWMutex
	targets   map[string]*targetHealthState // key: "providerID:model"
	events    []HealthEvent
	listeners []func(HealthEvent)
	now       func() time.Time
}

// NewHealthTracker creates a HealthTracker with the given configuration.
func NewHealthTracker(cfg HealthConfig) *HealthTracker {
	if cfg.ConsecutiveFailures <= 0 {
		cfg.ConsecutiveFailures = 3
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 10 * time.Second
	}
	if cfg.MaxCooldown <= 0 {
		cfg.MaxCooldown = 5 * time.Minute
	}
	if cfg.SuccessThreshold <= 0 {
		cfg.SuccessThreshold = 2
	}
	if cfg.MaxEventLog <= 0 {
		cfg.MaxEventLog = 1000
	}

	return &HealthTracker{
		cfg:     cfg,
		targets: make(map[string]*targetHealthState),
		events:  make([]HealthEvent, 0, 64),
		now:     time.Now,
	}
}

// SetClock allows tests to swap in a mock clock function.
func (h *HealthTracker) SetClock(now func() time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = now
}

func targetKey(providerID, model string) string {
	return providerID + ":" + model
}

func (h *HealthTracker) getOrCreate(key string) *targetHealthState {
	st, exists := h.targets[key]
	if !exists {
		st = &targetHealthState{
			state:           StateClosed,
			lastStateChange: h.now(),
			currentCooldown: h.cfg.Cooldown,
		}
		h.targets[key] = st
	}
	return st
}

// IsAvailable implements the HealthView interface. It determines whether
// the target is currently available for traffic or eligible for a recovery probe.
func (h *HealthTracker) IsAvailable(providerID, model string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := targetKey(providerID, model)
	st := h.getOrCreate(key)
	now := h.now()

	switch st.state {
	case StateClosed:
		return true

	case StateOpen:
		// Check if cooldown period has elapsed
		if now.Sub(st.lastStateChange) >= st.currentCooldown {
			// Transition to half-open to allow probe traffic
			h.transition(key, providerID, model, st, StateHalfOpen, "cooldown elapsed, testing recovery with probe")
			st.inFlightProbes++
			return true
		}
		return false

	case StateHalfOpen:
		// In half-open, admit a single concurrent probe request
		if st.inFlightProbes == 0 {
			st.inFlightProbes++
			return true
		}
		return false

	default:
		return true
	}
}

// RecordSuccess records a successful call to the target.
func (h *HealthTracker) RecordSuccess(providerID, model string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := targetKey(providerID, model)
	st := h.getOrCreate(key)

	if st.inFlightProbes > 0 {
		st.inFlightProbes--
	}

	switch st.state {
	case StateHalfOpen:
		st.consecutiveSuccesses++
		if st.consecutiveSuccesses >= h.cfg.SuccessThreshold {
			st.consecutiveFailures = 0
			st.currentCooldown = h.cfg.Cooldown
			h.transition(key, providerID, model, st, StateClosed, "target successfully recovered")
		}

	case StateClosed:
		st.consecutiveFailures = 0
		st.consecutiveSuccesses = 0

	case StateOpen:
		// If an out-of-band check succeeded, recover
		h.transition(key, providerID, model, st, StateClosed, "out-of-band success")
	}
}

// RecordFailure records a failed call to the target.
func (h *HealthTracker) RecordFailure(providerID, model string, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := targetKey(providerID, model)
	st := h.getOrCreate(key)

	if st.inFlightProbes > 0 {
		st.inFlightProbes--
	}
	st.consecutiveSuccesses = 0
	st.consecutiveFailures++

	switch st.state {
	case StateClosed:
		if st.consecutiveFailures >= h.cfg.ConsecutiveFailures {
			st.currentCooldown = h.cfg.Cooldown
			h.transition(key, providerID, model, st, StateOpen, reason)
		}

	case StateHalfOpen:
		// Probe failed: trip circuit back to open with exponential backoff
		st.currentCooldown = st.currentCooldown * 2
		if st.currentCooldown > h.cfg.MaxCooldown {
			st.currentCooldown = h.cfg.MaxCooldown
		}
		h.transition(key, providerID, model, st, StateOpen, "probe failed: "+reason)

	case StateOpen:
		// Already open, update last state change if needed
	}
}

// GetState returns the current circuit state for a target.
func (h *HealthTracker) GetState(providerID, model string) CircuitState {
	h.mu.RLock()
	defer h.mu.RUnlock()

	key := targetKey(providerID, model)
	st, exists := h.targets[key]
	if !exists {
		return StateClosed
	}
	return st.state
}

// transition updates state and dispatches HealthEvent. Caller must hold h.mu.
func (h *HealthTracker) transition(
	key string,
	providerID string,
	model string,
	st *targetHealthState,
	newState CircuitState,
	reason string,
) {
	oldState := st.state
	st.state = newState
	st.lastStateChange = h.now()
	if newState == StateHalfOpen {
		st.consecutiveSuccesses = 0
	}

	event := HealthEvent{
		TimestampMS:         h.now().UnixMilli(),
		ProviderID:          providerID,
		Model:               model,
		OldState:            oldState,
		NewState:            newState,
		Reason:              reason,
		ConsecutiveFailures: st.consecutiveFailures,
	}

	// Append to event log (bounded ring buffer)
	if len(h.events) >= h.cfg.MaxEventLog {
		// drop oldest quarter of events
		drop := h.cfg.MaxEventLog / 4
		if drop <= 0 {
			drop = 1
		}
		h.events = append(h.events[:0], h.events[drop:]...)
	}
	h.events = append(h.events, event)

	// Notify subscribers synchronously
	for _, l := range h.listeners {
		l(event)
	}
}

// Events returns a copy of the recent health transition events up to limit (newest first).
func (h *HealthTracker) Events(limit int) []HealthEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()

	n := len(h.events)
	if limit <= 0 || limit > n {
		limit = n
	}

	out := make([]HealthEvent, limit)
	for i := 0; i < limit; i++ {
		out[i] = h.events[n-1-i]
	}
	return out
}

// Subscribe registers a listener callback invoked whenever a health state transition occurs.
// It returns an unsubscribe function.
func (h *HealthTracker) Subscribe(listener func(HealthEvent)) func() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.listeners = append(h.listeners, listener)
	idx := len(h.listeners) - 1

	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if idx < len(h.listeners) {
			h.listeners = append(h.listeners[:idx], h.listeners[idx+1:]...)
		}
	}
}
