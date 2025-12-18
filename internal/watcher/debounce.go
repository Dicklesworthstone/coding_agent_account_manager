package watcher

import (
	"sync"
	"time"
)

type debouncer struct {
	delay time.Duration

	mu   sync.Mutex
	last map[string]time.Time
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

	if last, ok := d.last[key]; ok {
		if now.Sub(last) < d.delay {
			return false
		}
	}
	d.last[key] = now
	return true
}
