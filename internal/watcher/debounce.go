package watcher

import (
	"sync"
	"time"
)

// cleanupThreshold is how old an entry must be before it's eligible for cleanup.
// Set to 1 minute to balance memory usage with cleanup overhead.
const cleanupThreshold = 1 * time.Minute

// cleanupInterval controls how often we attempt cleanup (in ShouldEmit calls).
const cleanupInterval = 100

type debouncer struct {
	delay time.Duration

	mu          sync.Mutex
	last        map[string]time.Time
	callCounter int // tracks calls to periodically trigger cleanup
}

func newDebouncer(delay time.Duration) *debouncer {
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	return &debouncer{
		delay: delay,
		last:  make(map[string]time.Time),
	}
}

func (d *debouncer) ShouldEmit(key string) bool {
	if d == nil {
		return true
	}
	if key == "" {
		return true
	}

	now := time.Now()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Periodically cleanup stale entries to prevent unbounded memory growth
	d.callCounter++
	if d.callCounter >= cleanupInterval {
		d.callCounter = 0
		d.cleanupLocked(now)
	}

	if last, ok := d.last[key]; ok {
		if now.Sub(last) < d.delay {
			return false
		}
	}
	d.last[key] = now
	return true
}

// cleanupLocked removes entries older than cleanupThreshold.
// Must be called with d.mu held.
func (d *debouncer) cleanupLocked(now time.Time) {
	for key, ts := range d.last {
		if now.Sub(ts) > cleanupThreshold {
			delete(d.last, key)
		}
	}
}
