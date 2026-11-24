package usage

import (
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
)

// TokenEntry represents a single token usage record.
type TokenEntry struct {
	// Timestamp when this usage occurred.
	Timestamp time.Time `json:"timestamp"`

	// Model used (e.g., "claude-3-opus", "gpt-4").
	Model string `json:"model,omitempty"`

	// ConversationID groups related messages.
	ConversationID string `json:"conversation_id,omitempty"`

	// MessageID uniquely identifies this message.
	MessageID string `json:"message_id,omitempty"`

	// InputTokens consumed for the request.
	InputTokens int64 `json:"input_tokens"`

	// OutputTokens generated in the response.
	OutputTokens int64 `json:"output_tokens"`

	// CacheTokens includes cache read/write tokens.
	CacheTokens int64 `json:"cache_tokens,omitempty"`

	// Source indicates where this data came from.
	// Values: "api_response", "log_parse", "stream_estimate"
	Source string `json:"source"`
}

// TotalTokens returns the sum of all token types.
func (e *TokenEntry) TotalTokens() int64 {
	return e.InputTokens + e.OutputTokens + e.CacheTokens
}

// SessionTracker tracks token usage during an active session in real-time.
// It maintains a rolling window of token entries and can calculate burn rates.
type SessionTracker struct {
	mu         sync.RWMutex
	entries    []TokenEntry
	windowSize time.Duration
	maxEntries int

	// Callbacks
	onUsage func(entry TokenEntry)

	// Stats
	totalInput  int64
	totalOutput int64
	totalCache  int64
}

// SessionTrackerOption configures a SessionTracker.
type SessionTrackerOption func(*SessionTracker)

// WithWindowSize sets the time window for burn rate calculation.
func WithWindowSize(d time.Duration) SessionTrackerOption {
	return func(t *SessionTracker) {
		t.windowSize = d
	}
}

// WithMaxEntries sets the maximum number of entries to retain.
func WithMaxEntries(n int) SessionTrackerOption {
	return func(t *SessionTracker) {
		t.maxEntries = n
	}
}

// WithOnUsage sets a callback invoked when new usage is recorded.
func WithOnUsage(fn func(TokenEntry)) SessionTrackerOption {
	return func(t *SessionTracker) {
		t.onUsage = fn
	}
}

// NewSessionTracker creates a new session tracker with optional configuration.
func NewSessionTracker(opts ...SessionTrackerOption) *SessionTracker {
	t := &SessionTracker{
		windowSize: 2 * time.Hour, // Default 2-hour window
		maxEntries: 10000,         // Reasonable limit
	}

	for _, opt := range opts {
		opt(t)
	}

	return t
}

// Record adds a new token usage entry.
// Thread-safe for concurrent access.
func (t *SessionTracker) Record(entry TokenEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	t.mu.Lock()

	t.entries = append(t.entries, entry)

	// Update running totals
	t.totalInput += entry.InputTokens
	t.totalOutput += entry.OutputTokens
	t.totalCache += entry.CacheTokens

	// Enforce max entries limit
	if len(t.entries) > t.maxEntries {
		// Remove oldest entries, keeping the most recent
		excess := len(t.entries) - t.maxEntries
		for i := 0; i < excess; i++ {
			// Subtract from totals
			t.totalInput -= t.entries[i].InputTokens
			t.totalOutput -= t.entries[i].OutputTokens
			t.totalCache -= t.entries[i].CacheTokens
		}
		t.entries = t.entries[excess:]
	}

	// Capture callback before releasing lock to avoid race
	callback := t.onUsage
	t.mu.Unlock()

	// Fire callback outside lock to prevent deadlock if callback
	// calls SessionTracker methods that acquire the mutex.
	if callback != nil {
		callback(entry)
	}
}

// RecordFromResponse records token usage extracted from an API response.
func (t *SessionTracker) RecordFromResponse(model string, inputTokens, outputTokens int64, convID, msgID string) {
	t.Record(TokenEntry{
		Timestamp:      time.Now(),
		Model:          model,
		ConversationID: convID,
		MessageID:      msgID,
		InputTokens:    inputTokens,
		OutputTokens:   outputTokens,
		Source:         "api_response",
	})
}

// EstimateFromStream estimates and records tokens from streaming output.
// Uses rough estimation: ~4 characters per token for English.
func (t *SessionTracker) EstimateFromStream(model, chunk string) {
	if len(chunk) == 0 {
		return
	}

	// Rough estimation: ~4 chars per token for English
	// This is intentionally conservative (underestimates)
	estimatedTokens := int64(len(chunk) / 4)
	if estimatedTokens < 1 && len(chunk) > 0 {
		estimatedTokens = 1
	}

	t.Record(TokenEntry{
		Timestamp:    time.Now(),
		Model:        model,
		OutputTokens: estimatedTokens,
		Source:       "stream_estimate",
	})
}

// BurnRate calculates the burn rate from entries within the specified window.
// Returns nil if insufficient data for calculation.
func (t *SessionTracker) BurnRate(window time.Duration) *BurnRateInfo {
	t.mu.RLock()
	entries := t.entriesInWindow(window)
	t.mu.RUnlock()

	if len(entries) == 0 {
		return nil
	}

	// Convert TokenEntry to logs.LogEntry for CalculateBurnRate
	logEntries := make([]*logs.LogEntry, len(entries))
	for i, e := range entries {
		logEntries[i] = &logs.LogEntry{
			Timestamp:    e.Timestamp,
			Model:        e.Model,
			InputTokens:  e.InputTokens,
			OutputTokens: e.OutputTokens,
			TotalTokens:  e.TotalTokens(),
		}
	}

	// Use more lenient options for session data (smaller samples acceptable)
	opts := &BurnRateOptions{
		MinSampleSize:      2,
		MinTimespanMinutes: 1,
	}

	return CalculateBurnRate(logEntries, window, opts)
}

// entriesInWindow returns entries within the time window.
// Must be called with at least read lock held.
func (t *SessionTracker) entriesInWindow(window time.Duration) []TokenEntry {
	cutoff := time.Now().Add(-window)
	var result []TokenEntry

	for _, e := range t.entries {
		if !e.Timestamp.Before(cutoff) {
			result = append(result, e)
		}
	}

	return result
}

// TotalTokens returns the total tokens recorded in this session.
func (t *SessionTracker) TotalTokens() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalInput + t.totalOutput + t.totalCache
}

// TokensByType returns tokens broken down by type.
func (t *SessionTracker) TokensByType() (input, output, cache int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalInput, t.totalOutput, t.totalCache
}

// EntryCount returns the number of entries currently tracked.
func (t *SessionTracker) EntryCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}

// EntriesInWindow returns the count of entries within the time window.
func (t *SessionTracker) EntriesInWindow(window time.Duration) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entriesInWindow(window))
}

// TokensInWindow returns total tokens consumed within the time window.
func (t *SessionTracker) TokensInWindow(window time.Duration) int64 {
	t.mu.RLock()
	entries := t.entriesInWindow(window)
	t.mu.RUnlock()

	var total int64
	for _, e := range entries {
		total += e.TotalTokens()
	}
	return total
}

// ByModel returns token totals grouped by model.
func (t *SessionTracker) ByModel() map[string]int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]int64)
	for _, e := range t.entries {
		if e.Model != "" {
			result[e.Model] += e.TotalTokens()
		}
	}
	return result
}

// BySource returns token totals grouped by source.
func (t *SessionTracker) BySource() map[string]int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]int64)
	for _, e := range t.entries {
		if e.Source != "" {
			result[e.Source] += e.TotalTokens()
		}
	}
	return result
}

// Clear removes all entries and resets totals.
func (t *SessionTracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.entries = nil
	t.totalInput = 0
	t.totalOutput = 0
	t.totalCache = 0
}

// Prune removes entries older than the window size.
func (t *SessionTracker) Prune() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-t.windowSize)
	var kept []TokenEntry
	var pruned int

	for _, e := range t.entries {
		if e.Timestamp.Before(cutoff) {
			// Remove from totals
			t.totalInput -= e.InputTokens
			t.totalOutput -= e.OutputTokens
			t.totalCache -= e.CacheTokens
			pruned++
		} else {
			kept = append(kept, e)
		}
	}

	t.entries = kept
	return pruned
}

// MergeLogData merges historical log entries into the session tracker.
// Deduplicates based on MessageID if available, otherwise by timestamp.
// Prefers log data over stream estimates.
func (t *SessionTracker) MergeLogData(logEntries []*logs.LogEntry) int {
	if len(logEntries) == 0 {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Build lookup for existing entries
	existingByMsgID := make(map[string]int)   // message_id -> index
	existingBySecond := make(map[int64][]int) // unix_second -> indices

	for i, e := range t.entries {
		if e.MessageID != "" {
			existingByMsgID[e.MessageID] = i
		}
		sec := e.Timestamp.Unix()
		existingBySecond[sec] = append(existingBySecond[sec], i)
	}

	added := 0
	for _, le := range logEntries {
		// Check for duplicate by message ID
		if le.MessageID != "" {
			if idx, exists := existingByMsgID[le.MessageID]; exists {
				// Update existing entry if this is more authoritative
				existing := &t.entries[idx]
				if existing.Source == "stream_estimate" {
					// Log data is more accurate, update
					t.totalInput -= existing.InputTokens
					t.totalOutput -= existing.OutputTokens
					t.totalCache -= existing.CacheTokens

					existing.InputTokens = le.InputTokens
					existing.OutputTokens = le.OutputTokens
					existing.CacheTokens = le.CacheReadTokens + le.CacheCreateTokens
					existing.Source = "log_parse"

					t.totalInput += existing.InputTokens
					t.totalOutput += existing.OutputTokens
					t.totalCache += existing.CacheTokens
				}
				continue
			}
		}

		// Check for duplicate by timestamp (within 1 second tolerance)
		found := false
		sec := le.Timestamp.Unix()
		for _, candidate := range []int64{sec - 1, sec, sec + 1} {
			if indices, ok := existingBySecond[candidate]; ok {
				for _, idx := range indices {
					if idx < 0 || idx >= len(t.entries) {
						continue
					}
					existing := t.entries[idx]
					delta := existing.Timestamp.Sub(le.Timestamp)
					if delta < 0 {
						delta = -delta
					}
					if delta <= time.Second {
						found = true
						break
					}
				}
			}
			if found {
				break
			}
		}
		if found {
			continue
		}

		// Add new entry
		entry := TokenEntry{
			Timestamp:      le.Timestamp,
			Model:          le.Model,
			ConversationID: le.ConversationID,
			MessageID:      le.MessageID,
			InputTokens:    le.InputTokens,
			OutputTokens:   le.OutputTokens,
			CacheTokens:    le.CacheReadTokens + le.CacheCreateTokens,
			Source:         "log_parse",
		}

		t.entries = append(t.entries, entry)
		t.totalInput += entry.InputTokens
		t.totalOutput += entry.OutputTokens
		t.totalCache += entry.CacheTokens
		added++

		// Update lookup
		if entry.MessageID != "" {
			existingByMsgID[entry.MessageID] = len(t.entries) - 1
		}
		sec = entry.Timestamp.Unix()
		existingBySecond[sec] = append(existingBySecond[sec], len(t.entries)-1)
	}

	return added
}

// Snapshot returns a copy of current entries.
// Safe for concurrent access.
func (t *SessionTracker) Snapshot() []TokenEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]TokenEntry, len(t.entries))
	copy(result, t.entries)
	return result
}

// Stats returns a summary of session tracking statistics.
type SessionStats struct {
	TotalEntries int              `json:"total_entries"`
	TotalTokens  int64            `json:"total_tokens"`
	InputTokens  int64            `json:"input_tokens"`
	OutputTokens int64            `json:"output_tokens"`
	CacheTokens  int64            `json:"cache_tokens"`
	WindowSize   time.Duration    `json:"window_size"`
	OldestEntry  time.Time        `json:"oldest_entry,omitempty"`
	NewestEntry  time.Time        `json:"newest_entry,omitempty"`
	ByModel      map[string]int64 `json:"by_model,omitempty"`
	BySource     map[string]int64 `json:"by_source,omitempty"`
}

// Stats returns current session statistics.
func (t *SessionTracker) Stats() *SessionStats {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stats := &SessionStats{
		TotalEntries: len(t.entries),
		TotalTokens:  t.totalInput + t.totalOutput + t.totalCache,
		InputTokens:  t.totalInput,
		OutputTokens: t.totalOutput,
		CacheTokens:  t.totalCache,
		WindowSize:   t.windowSize,
		ByModel:      make(map[string]int64),
		BySource:     make(map[string]int64),
	}

	for _, e := range t.entries {
		if stats.OldestEntry.IsZero() || e.Timestamp.Before(stats.OldestEntry) {
			stats.OldestEntry = e.Timestamp
		}
		if stats.NewestEntry.IsZero() || e.Timestamp.After(stats.NewestEntry) {
			stats.NewestEntry = e.Timestamp
		}
		if e.Model != "" {
			stats.ByModel[e.Model] += e.TotalTokens()
		}
		if e.Source != "" {
			stats.BySource[e.Source] += e.TotalTokens()
		}
	}

	return stats
}
