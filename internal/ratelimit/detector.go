// Package ratelimit provides rate limit detection for AI CLI tools.
//
// It monitors stdout/stderr output for provider-specific rate limit patterns
// and signals when a rate limit is detected, enabling automatic profile switching.
package ratelimit

import (
	"bytes"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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

	ProviderGrok     Provider = "grok"
	ProviderCursor   Provider = "cursor"
	ProviderOpenCode Provider = "opencode"
	ProviderAGY      Provider = "agy"
)

// DefaultPatterns returns the default rate limit patterns for each provider.
func DefaultPatterns() map[Provider][]string {
	patterns := map[Provider][]string{
		ProviderClaude: {
			`(?i)rate.?limit`,
			`(?i)usage.?limit`,
			// "You've hit your session limit · resets 3pm" (weekly, Opus, …)
			`(?i)you['’]?ve hit your (?:[\w'’-]+ ){0,4}?limit`,
			`(?i)you['’]?re out of usage credits`,
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
	for _, provider := range []Provider{ProviderGrok, ProviderCursor, ProviderOpenCode, ProviderAGY} {
		patterns[provider] = []string{
			`(?i)\brate[ _-]?limit(?:\b|_)`,
			`(?i)\busage[ _-]?limit(?:\b|_)`,
			`(?i)\bquota[ _-]exceeded\b`,
			`(?i)\bexceeded[^\r\n]*\bquota\b`,
			`\b429\b`,
			`(?i)\btoo[ _-]many[ _-]requests\b`,
			`(?i)\bRESOURCE_EXHAUSTED\b`,
		}
	}
	return patterns
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
	mu         sync.RWMutex
	provider   Provider
	patterns   []*regexp.Regexp
	detected   bool
	reason     string
	retryAfter time.Time
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
	// Headers may follow the line that first reported the limit. Continue
	// collecting them even after detection becomes sticky.
	deadline, text := parseRetryAfterHeaders(text, time.Now())
	if !deadline.IsZero() {
		d.mu.Lock()
		if deadline.After(d.retryAfter) {
			d.retryAfter = deadline
		}
		d.mu.Unlock()
	}

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

// RetryAfter returns the latest explicit Retry-After deadline observed in the
// output. A header alone does not imply that the command was rate limited.
func (d *Detector) RetryAfter() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.retryAfter
}

// RetryDelay respects both the configured backoff and the server's deadline.
// An expired or absent deadline adds no delay; a server deadline is not capped
// by the local backoff maximum.
func RetryDelay(backoff time.Duration, retryAfter, now time.Time) time.Duration {
	if backoff < 0 {
		backoff = 0
	}
	if remaining := retryAfter.Sub(now); remaining > backoff {
		return remaining
	}
	return backoff
}

// parseRetryAfterHeaders accepts only complete HTTP-style Retry-After headers.
// Arbitrary numbers in error prose, reset timestamps, and other headers must
// never become retry delays. Remove the headers from pattern matching so a
// delay of 429 seconds does not itself imply HTTP status 429.
func parseRetryAfterHeaders(text string, now time.Time) (time.Time, string) {
	var latest time.Time
	lines := strings.Split(text, "\n")
	remaining := lines[:0]
	hasHeader := false
	for _, line := range lines {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.EqualFold(name, "Retry-After") {
			remaining = append(remaining, line)
			continue
		}
		hasHeader = true
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		digits := true
		for i := range len(value) {
			if value[i] < '0' || value[i] > '9' {
				digits = false
				break
			}
		}
		var deadline time.Time
		if digits {
			seconds, err := strconv.ParseUint(value, 10, 63)
			if err != nil || seconds > uint64((1<<63-1)/time.Second) {
				continue
			}
			deadline = now.Add(time.Duration(seconds) * time.Second)
		} else {
			var err error
			deadline, err = http.ParseTime(value)
			if err != nil {
				continue
			}
		}
		if deadline.After(latest) {
			latest = deadline
		}
	}
	if !hasHeader {
		return latest, text
	}
	return latest, strings.Join(remaining, "\n")
}

// Reset clears the detection state.
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.detected = false
	d.reason = ""
	d.retryAfter = time.Time{}
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
	case "grok":
		return ProviderGrok
	case "cursor":
		return ProviderCursor
	case "opencode":
		return ProviderOpenCode
	case "agy":
		return ProviderAGY
	default:
		return ProviderClaude
	}
}
