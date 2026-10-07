package ratelimit

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewDetector(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		patterns []string
		wantErr  bool
	}{
		{
			name:     "claude with defaults",
			provider: ProviderClaude,
			patterns: nil,
			wantErr:  false,
		},
		{
			name:     "codex with defaults",
			provider: ProviderCodex,
			patterns: nil,
			wantErr:  false,
		},
		{
			name:     "gemini with defaults",
			provider: ProviderGemini,
			patterns: nil,
			wantErr:  false,
		},
		{
			name:     "custom patterns",
			provider: ProviderClaude,
			patterns: []string{`test pattern`, `\d+`},
			wantErr:  false,
		},
		{
			name:     "invalid regex",
			provider: ProviderClaude,
			patterns: []string{`[invalid`},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewDetector(tt.provider, tt.patterns)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewDetector() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && d == nil {
				t.Error("NewDetector() returned nil detector without error")
			}
		})
	}
}

func TestDetector_Check(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		texts    []string
		want     bool
	}{
		{
			name:     "claude rate limit",
			provider: ProviderClaude,
			texts:    []string{"Error: rate limit exceeded"},
			want:     true,
		},
		{
			name:     "claude usage limit",
			provider: ProviderClaude,
			texts:    []string{"usage limit reached"},
			want:     true,
		},
		{
			name:     "claude 429",
			provider: ProviderClaude,
			texts:    []string{"HTTP 429 Too Many Requests"},
			want:     true,
		},
		{
			name:     "claude capacity",
			provider: ProviderClaude,
			texts:    []string{"Over capacity, please try again"},
			want:     true,
		},
		{
			name:     "codex rate limit",
			provider: ProviderCodex,
			texts:    []string{"rate-limit hit"},
			want:     true,
		},
		{
			name:     "codex quota exceeded",
			provider: ProviderCodex,
			texts:    []string{"quota exceeded for this model"},
			want:     true,
		},
		{
			name:     "gemini resource exhausted",
			provider: ProviderGemini,
			texts:    []string{"RESOURCE_EXHAUSTED: Too many requests"},
			want:     true,
		},
		{
			name:     "gemini quota",
			provider: ProviderGemini,
			texts:    []string{"Quota exceeded for project"},
			want:     true,
		},
		{
			name:     "normal output",
			provider: ProviderClaude,
			texts:    []string{"Here is the code you requested", "function foo() { return 42; }"},
			want:     false,
		},
		{
			name:     "multiple lines with rate limit at end",
			provider: ProviderClaude,
			texts:    []string{"Starting...", "Processing...", "Error: rate limit"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewDetector(tt.provider, nil)
			if err != nil {
				t.Fatalf("NewDetector() error = %v", err)
			}

			var got bool
			for _, text := range tt.texts {
				got = d.Check(text)
			}

			if got != tt.want {
				t.Errorf("Check() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetector_StickyDetection(t *testing.T) {
	d, err := NewDetector(ProviderClaude, nil)
	if err != nil {
		t.Fatalf("NewDetector() error = %v", err)
	}

	// Initially not detected
	if d.Detected() {
		t.Error("Detected() = true before any check")
	}

	// Normal output
	d.Check("Hello, world!")
	if d.Detected() {
		t.Error("Detected() = true after normal output")
	}

	// Rate limit detected
	d.Check("rate limit exceeded")
	if !d.Detected() {
		t.Error("Detected() = false after rate limit")
	}

	// Should remain true after more checks
	d.Check("normal text again")
	if !d.Detected() {
		t.Error("Detected() = false, should be sticky")
	}

	// Reset should clear
	d.Reset()
	if d.Detected() {
		t.Error("Detected() = true after Reset()")
	}
}

func TestDetector_Reason(t *testing.T) {
	d, err := NewDetector(ProviderClaude, nil)
	if err != nil {
		t.Fatalf("NewDetector() error = %v", err)
	}

	// No reason before detection
	if d.Reason() != "" {
		t.Errorf("Reason() = %q before detection, want empty", d.Reason())
	}

	// Detect rate limit
	d.Check("Error: rate limit exceeded")

	reason := d.Reason()
	if reason == "" {
		t.Error("Reason() is empty after detection")
	}
	if !strings.Contains(strings.ToLower(reason), "rate") {
		t.Errorf("Reason() = %q, expected to contain 'rate'", reason)
	}
}

func TestDetector_CommonProviders(t *testing.T) {
	for _, provider := range []Provider{ProviderGrok, ProviderCursor, ProviderOpenCode, ProviderAGY} {
		t.Run(string(provider), func(t *testing.T) {
			for _, tc := range []struct {
				text string
				want bool
			}{
				{"HTTP 429 Too Many Requests", true},
				{"Error: rate limit exceeded", true},
				{"RATE_LIMIT_EXCEEDED", true},
				{"USAGE_LIMIT_EXCEEDED", true},
				{"Usage-limit reached", true},
				{"QUOTA_EXCEEDED", true},
				{"Exceeded the project quota", true},
				{"RESOURCE_EXHAUSTED", true},
				{"Too many requests", true},
				{"Capacity remaining: 90%", false},
				{"Quota: 10000 tokens remaining", false},
				{"The quota configuration was saved", false},
				{"Processed 1429 tokens successfully", false},
				{"The request completed successfully", false},
			} {
				t.Run(tc.text, func(t *testing.T) {
					d, err := NewDetector(provider, nil)
					if err != nil {
						t.Fatal(err)
					}
					if got := d.Check(tc.text); got != tc.want {
						t.Fatalf("Check(%q) = %v, want %v", tc.text, got, tc.want)
					}
				})
			}
		})
	}
}

func TestParseRetryAfterHeaders(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		text string
		want time.Time
	}{
		{"seconds", "Retry-After: 120", now.Add(2 * time.Minute)},
		{"zero seconds", "Retry-After: 0", now},
		{"case and whitespace", "\tretry-after:\t15 \r", now.Add(15 * time.Second)},
		{"HTTP date", "Retry-After: " + now.Add(time.Hour).Format(http.TimeFormat), now.Add(time.Hour)},
		{"expired HTTP date", "Retry-After: " + now.Add(-time.Hour).Format(http.TimeFormat), now.Add(-time.Hour)},
		{"latest complete header", "HTTP 429\r\nRetry-After: 90\r\nRetry-After: 30\r\n", now.Add(90 * time.Second)},
		{"empty", "Retry-After:", time.Time{}},
		{"negative", "Retry-After: -1", time.Time{}},
		{"signed", "Retry-After: +1", time.Time{}},
		{"fraction", "Retry-After: 1.5", time.Time{}},
		{"exponent", "Retry-After: 1e3", time.Time{}},
		{"duration overflow", "Retry-After: 9223372037", time.Time{}},
		{"integer overflow", "Retry-After: 18446744073709551616", time.Time{}},
		{"invalid date", "Retry-After: Wednesday next week", time.Time{}},
		{"unit suffix", "Retry-After: 60 seconds", time.Time{}},
		{"other header", "X-RateLimit-Reset: 1791374400", time.Time{}},
		{"arbitrary prose", "Error429: please retry after 60 seconds", time.Time{}},
		{"embedded prose", "The Retry-After: 60 field was missing", time.Time{}},
		{"quoted JSON field", `{"Retry-After":60}`, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := parseRetryAfterHeaders(tc.text, now)
			if !got.Equal(tc.want) {
				t.Fatalf("deadline = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDetector_RetryAfter(t *testing.T) {
	d, err := NewDetector(ProviderCodex, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A header is not a rate-limit error, even when its value matches a status
	// code. Headers after a detected error must still update the deadline.
	if d.Check("Retry-After: 429") {
		t.Fatal("Retry-After header triggered rate-limit detection")
	}
	d.Reset()
	w := NewObservingWriter(d, nil)
	w.Write([]byte("HTTP 429 Too Many Requests\nRetry-Af"))
	if !d.Detected() || !d.RetryAfter().IsZero() {
		t.Fatal("partial header was interpreted before its value arrived")
	}
	before := time.Now()
	w.Write([]byte("ter: 120\r\n"))
	deadline := d.RetryAfter()
	if deadline.Before(before.Add(120*time.Second)) || deadline.After(time.Now().Add(120*time.Second)) {
		t.Fatalf("deadline = %s, want 120 seconds after the completed header", deadline)
	}
	d.Check("Retry-After: malformed")
	d.Check("Retry-After: 1")
	if !d.RetryAfter().Equal(deadline) {
		t.Fatal("invalid or earlier header shortened the server deadline")
	}
	d.Reset()
	if d.Detected() || !d.RetryAfter().IsZero() {
		t.Fatal("Reset retained detection or server deadline")
	}
	w.Write([]byte("Retry-After: 60"))
	if !d.RetryAfter().IsZero() {
		t.Fatal("unterminated header interpreted before flush")
	}
	w.Flush()
	if d.RetryAfter().IsZero() || d.Detected() {
		t.Fatal("flush did not record the standalone header correctly")
	}
}

func TestRetryDelay(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		backoff  time.Duration
		deadline time.Time
		want     time.Duration
	}{
		{"backoff only", time.Second, time.Time{}, time.Second},
		{"no delay", 0, time.Time{}, 0},
		{"expired deadline", time.Second, now.Add(-time.Minute), time.Second},
		{"backoff is longer", time.Minute, now.Add(time.Second), time.Minute},
		{"server delay is longer", time.Second, now.Add(time.Hour), time.Hour},
		{"server delay with zero backoff", 0, now.Add(time.Minute), time.Minute},
		{"negative backoff", -time.Second, time.Time{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetryDelay(tc.backoff, tc.deadline, now); got != tc.want {
				t.Fatalf("delay = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestObservingWriter(t *testing.T) {
	d, err := NewDetector(ProviderClaude, nil)
	if err != nil {
		t.Fatalf("NewDetector() error = %v", err)
	}

	var lines []string
	w := NewObservingWriter(d, func(line string) {
		lines = append(lines, line)
	})

	// Write some data with newlines
	input := "Line 1\nLine 2\nrate limit error\nLine 4\n"
	n, err := w.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != len(input) {
		t.Errorf("Write() = %d, want %d", n, len(input))
	}

	// Should have detected rate limit
	if !d.Detected() {
		t.Error("Detected() = false after rate limit in stream")
	}

	// Should have captured all lines
	if len(lines) != 4 {
		t.Errorf("callback called %d times, want 4", len(lines))
	}
}

func TestObservingWriter_PartialLines(t *testing.T) {
	d, err := NewDetector(ProviderClaude, nil)
	if err != nil {
		t.Fatalf("NewDetector() error = %v", err)
	}

	var lines []string
	w := NewObservingWriter(d, func(line string) {
		lines = append(lines, line)
	})

	// Write partial data
	w.Write([]byte("Hello "))
	w.Write([]byte("rate "))
	w.Write([]byte("limit\n"))
	w.Write([]byte("Done"))
	w.Flush()

	// Should have detected rate limit
	if !d.Detected() {
		t.Error("Detected() = false after rate limit across writes")
	}

	// Should have two lines
	if len(lines) != 2 {
		t.Errorf("callback called %d times, want 2", len(lines))
	}
}

func TestProviderFromString(t *testing.T) {
	tests := []struct {
		input string
		want  Provider
	}{
		{"claude", ProviderClaude},
		{"Claude", ProviderClaude},
		{"CLAUDE", ProviderClaude},
		{"codex", ProviderCodex},
		{"Codex", ProviderCodex},
		{"gemini", ProviderGemini},
		{"Gemini", ProviderGemini},
		{"grok", ProviderGrok},
		{"Grok", ProviderGrok},
		{"cursor", ProviderCursor},
		{"Cursor", ProviderCursor},
		{"opencode", ProviderOpenCode},
		{"OpenCode", ProviderOpenCode},
		{"agy", ProviderAGY},
		{"AGY", ProviderAGY},
		{"unknown", ProviderClaude}, // default
		{"", ProviderClaude},        // default
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ProviderFromString(tt.input)
			if got != tt.want {
				t.Errorf("ProviderFromString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultPatterns(t *testing.T) {
	patterns := DefaultPatterns()

	// Should have patterns for all providers
	if len(patterns[ProviderClaude]) == 0 {
		t.Error("No default patterns for Claude")
	}
	if len(patterns[ProviderCodex]) == 0 {
		t.Error("No default patterns for Codex")
	}
	if len(patterns[ProviderGemini]) == 0 {
		t.Error("No default patterns for Gemini")
	}
}
