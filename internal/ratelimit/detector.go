// Package ratelimit provides rate limit detection for AI CLI tools.
//
// It monitors stdout/stderr output for provider-specific rate limit patterns
// and signals when a rate limit is detected, enabling automatic profile switching.
package ratelimit

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
)

// Provider identifies an AI CLI provider.
type Provider string

const (
	// ProviderClaude is Anthropic's Claude CLI.
	ProviderClaude Provider = "claude"

	// ProviderCodex is OpenAI's Codex CLI.
	ProviderCodex Provider = "codex"

	// ProviderGemini is Google's Gemini CLI.
	ProviderGemini Provider = "gemini"
)

// DefaultPatterns returns the default rate limit patterns for each provider.
func DefaultPatterns() map[Provider][]string {
	return map[Provider][]string{
		ProviderClaude: {
			`(?i)rate.?limit`,
			`(?i)usage.?limit`,
			`(?i)capacity`,
			`\b429\b`,
			`(?i)too.?many.?requests`,
			`(?i)exceeded.*quota`,
			`(?i)quota.*exceeded`,
		},
		ProviderCodex: {
			`(?i)rate.?limit`,
			`(?i)quota.?exceeded`,
			`\b429\b`,
			`(?i)too.?many.?requests`,
			`(?i)exceeded.*rate`,
			`(?i)slow.?down`,
		},
		ProviderGemini: {
			`(?i)RESOURCE_EXHAUSTED`,
			`(?i)quota`,
			`(?i)rate.?limit`,
			`\b429\b`,
			`(?i)too.?many.?requests`,
		},
	}
}

var (
	defaultCompiledPatterns map[Provider][]*regexp.Regexp
	initDefaultsOnce        sync.Once
)

func initDefaults() {
	defaultCompiledPatterns = make(map[Provider][]*regexp.Regexp)
	defaults := DefaultPatterns()

	for provider, patterns := range defaults {
		var compiled []*regexp.Regexp
		for _, p := range patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				// Should not happen with static default patterns
				continue
			}
			compiled = append(compiled, re)
		}
		defaultCompiledPatterns[provider] = compiled
	}
}

// Detector monitors output for rate limit patterns.
type Detector struct {
	mu       sync.RWMutex
	provider Provider
	patterns []*regexp.Regexp
	detected bool
	reason   string
}

// NewDetector creates a new rate limit detector for the given provider.
// Uses default patterns if none are provided.
func NewDetector(provider Provider, customPatterns []string) (*Detector, error) {
	d := &Detector{
		provider: provider,
	}

	// Use custom patterns if provided, otherwise use defaults
	if len(customPatterns) == 0 {
		initDefaultsOnce.Do(initDefaults)
		// Use pre-compiled defaults
		if patterns, ok := defaultCompiledPatterns[provider]; ok {
			// Copy patterns to avoid sharing backing array, ensuring thread safety if Detector is modified
			d.patterns = make([]*regexp.Regexp, len(patterns))
			copy(d.patterns, patterns)
			return d, nil
		}
		// If provider not found in defaults (shouldn't happen for known ones), fallback to empty
		return d, nil
	}

	// Compile custom patterns
	for _, p := range customPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		d.patterns = append(d.patterns, re)
	}

	return d, nil
}

// Check examines text for rate limit patterns.
// Returns true if a rate limit pattern is detected.
// The detection is sticky - once detected, it remains true.
func (d *Detector) Check(text string) bool {
	// Fast path: check if already detected using read lock
	d.mu.RLock()
	if d.detected {
		d.mu.RUnlock()
		return true
	}
	// Capture patterns while holding read lock (patterns slice is immutable after creation)
	patterns := d.patterns
	d.mu.RUnlock()

	// Perform expensive regex matching outside the lock
	for _, re := range patterns {
		if re.MatchString(text) {
			// Only acquire write lock when updating state
			d.mu.Lock()
			// Double-check in case another goroutine set it while we were matching
			if !d.detected {
				d.detected = true
				// Extract the matching portion for the reason
				match := re.FindString(text)
				d.reason = strings.TrimSpace(match)
			}
			d.mu.Unlock()
			return true
		}
	}

	return false
}

// Detected returns whether a rate limit has been detected.
func (d *Detector) Detected() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.detected
}

// Reason returns the detected rate limit text, if any.
func (d *Detector) Reason() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.reason
}

// Reset clears the detection state.
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.detected = false
	d.reason = ""
}

// Provider returns the provider this detector is configured for.
func (d *Detector) Provider() Provider {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.provider
}

// maxBufferSize is the maximum buffer size before forcing a flush (64KB).
// This prevents unbounded memory growth when output contains no newlines.
const maxBufferSize = 64 * 1024

// ObservingWriter wraps a writer and checks each write for rate limit patterns.
type ObservingWriter struct {
	mu       sync.Mutex
	detector *Detector
	callback func(line string) // Optional callback for each line
	buffer   []byte
}

// NewObservingWriter creates a writer that observes output for rate limits.
func NewObservingWriter(detector *Detector, callback func(line string)) *ObservingWriter {
	return &ObservingWriter{
		detector: detector,
		callback: callback,
	}
}

// Write implements io.Writer, buffering and checking each line.
func (w *ObservingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n = len(p)

	// Append to buffer
	w.buffer = append(w.buffer, p...)

	// Process complete lines
	for {
		idx := bytes.IndexByte(w.buffer, '\n')
		if idx == -1 {
			break
		}

		line := string(w.buffer[:idx])
		w.buffer = w.buffer[idx+1:]

		// Check for rate limit
		w.detector.Check(line)

		// Call callback if provided
		if w.callback != nil {
			w.callback(line)
		}
	}

	// Enforce buffer limit to prevent OOM on long lines without newlines
	if len(w.buffer) > maxBufferSize {
		// Process oversized buffer as a partial line
		line := string(w.buffer)
		w.detector.Check(line)
		if w.callback != nil {
			w.callback(line)
		}
		w.buffer = nil
	}

	// Compact buffer if it has grown large but contains little data
	// (capacity > 4KB and usage < 25%)
	if cap(w.buffer) > 4096 && len(w.buffer) < cap(w.buffer)/4 {
		newBuf := make([]byte, len(w.buffer))
		copy(newBuf, w.buffer)
		w.buffer = newBuf
	}

	return n, nil
}

// Flush processes any remaining buffered data.
func (w *ObservingWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.buffer) > 0 {
		line := string(w.buffer)
		w.detector.Check(line)
		if w.callback != nil {
			w.callback(line)
		}
		w.buffer = nil
	}
}

// ProviderFromString converts a string to a Provider, returning ProviderClaude
// as default for unknown providers.
func ProviderFromString(s string) Provider {
	switch strings.ToLower(s) {
	case "claude":
		return ProviderClaude
	case "codex":
		return ProviderCodex
	case "gemini":
		return ProviderGemini
	default:
		return ProviderClaude
	}
}
