package api

import (
	"sync"
)

// Rate-limit classes and their per-minute budgets, mirroring
// x-rate-limit-classes in docs/api/openapi.yaml. The stream class is a
// concurrency gauge, not a rate.
const (
	classAuth   = "auth"
	classRead   = "read"
	classWrite  = "write"
	classStream = "stream"
	classProbe  = "probe"
)

var classBudgets = map[string]int64{
	classAuth:  10,
	classRead:  120,
	classWrite: 30,
	classProbe: 6,
}

// limiter enforces per-(class, principal) fixed-window budgets. A fixed
// window is deliberately simple: management traffic is low and bursty
// dashboard polling fits comfortably inside one-minute buckets.
type limiter struct {
	mu     sync.Mutex
	nowMS  func() int64
	window map[string]*fixedWindow // key: class + "|" + principal
}

// fixedWindow counts requests inside the current one-minute window.
type fixedWindow struct {
	startMS int64
	count   int64
}

func newLimiter(nowMS func() int64) *limiter {
	return &limiter{nowMS: nowMS, window: map[string]*fixedWindow{}}
}

// allow consumes one unit from the class budget for the principal.
func (l *limiter) allow(class, principal string) bool {
	if class == classStream {
		return true // concurrency handled by the SSE handler itself
	}
	budget, ok := classBudgets[class]
	if !ok {
		return true // unknown class: fail open (spec table drives classes)
	}
	now := l.nowMS()
	win := now / 60000 // one-minute buckets, UTC

	l.mu.Lock()
	defer l.mu.Unlock()
	key := class + "|" + principal
	fw, ok := l.window[key]
	if !ok || fw.startMS != win {
		// New window (or first use). Prune stale entries opportunistically
		// so the map stays bounded by active principals.
		if len(l.window) > 1024 {
			for k, v := range l.window {
				if v.startMS != win {
					delete(l.window, k)
				}
			}
		}
		fw = &fixedWindow{startMS: win}
		l.window[key] = fw
	}
	if fw.count >= budget {
		return false
	}
	fw.count++
	return true
}
