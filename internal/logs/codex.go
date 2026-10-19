package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// CodexScanner reads token usage from Codex CLI session rollouts.
//
// Codex appends one JSONL rollout per session under
// $CODEX_HOME/sessions/YYYY/MM/DD/ (default CODEX_HOME: ~/.codex), and moves
// archived ones to $CODEX_HOME/archived_sessions/. Each model response is
// followed by an event_msg line of payload.type "token_count" whose
// info.last_token_usage is that response's usage and info.total_token_usage
// the session's running total. The model comes from the latest turn_context
// line before it.
type CodexScanner struct {
	logDirs []string
}

// NewCodexScanner creates a scanner over Codex's rollout stores.
func NewCodexScanner() *CodexScanner {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		homeDir, _ := os.UserHomeDir()
		codexHome = filepath.Join(homeDir, ".codex")
	}
	return &CodexScanner{logDirs: []string{
		filepath.Join(codexHome, "sessions"),
		filepath.Join(codexHome, "archived_sessions"),
	}}
}

// NewCodexScannerWithDir creates a scanner over one custom rollout root.
// Useful for testing.
func NewCodexScannerWithDir(logDir string) *CodexScanner {
	return &CodexScanner{logDirs: []string{logDir}}
}

// LogDir returns the primary rollout root.
func (s *CodexScanner) LogDir() string {
	if len(s.logDirs) == 0 {
		return ""
	}
	return s.logDirs[0]
}

// Scan returns one entry per Codex model response at or after since. If logDir
// is empty, every default rollout root is read.
func (s *CodexScanner) Scan(ctx context.Context, logDir string, since time.Time) (*ScanResult, error) {
	roots := s.logDirs
	if logDir != "" {
		roots = []string{logDir}
	}

	result := &ScanResult{
		Provider: "codex",
		Since:    since,
		Until:    time.Now(),
		Entries:  make([]*LogEntry, 0),
	}
	for _, root := range roots {
		skipped, err := walkJSONLFiles(ctx, root, since, func(path string) error {
			return s.scanFile(ctx, path, since, result)
		})
		result.ParseErrors += skipped
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

type codexRolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexRolloutPayload struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Model string `json:"model"`
	Info  *struct {
		TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
		LastTokenUsage  *codexTokenUsage `json:"last_token_usage"`
	} `json:"info"`
}

// codexTokenUsage follows OpenAI's accounting: input_tokens includes the
// cached and cache-write input, output_tokens includes reasoning, and
// total_tokens is input plus output.
type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func (u codexTokenUsage) sub(prev codexTokenUsage) (codexTokenUsage, bool) {
	d := codexTokenUsage{
		InputTokens:           u.InputTokens - prev.InputTokens,
		CachedInputTokens:     u.CachedInputTokens - prev.CachedInputTokens,
		CacheWriteInputTokens: u.CacheWriteInputTokens - prev.CacheWriteInputTokens,
		OutputTokens:          u.OutputTokens - prev.OutputTokens,
		TotalTokens:           u.TotalTokens - prev.TotalTokens,
	}
	ok := d.InputTokens >= 0 && d.CachedInputTokens >= 0 && d.CacheWriteInputTokens >= 0 && d.OutputTokens >= 0 && d.TotalTokens >= 0
	return d, ok
}

// scanFile replays one rollout in order: the model is carried from the latest
// turn_context, and a token_count that repeats the previous running total
// (Codex re-emits it, for example with a rate-limit update) is not counted
// twice.
func (s *CodexScanner) scanFile(ctx context.Context, path string, since time.Time, result *ScanResult) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var model, sessionID string
	var prevTotal *codexTokenUsage
	skipped, err := forEachJSONLLine(ctx, f, func(line []byte) {
		result.TotalEntries++
		if !bytes.Contains(line, []byte(`"token_count"`)) && !bytes.Contains(line, []byte(`"turn_context"`)) &&
			!bytes.Contains(line, []byte(`"session_meta"`)) {
			return
		}
		var raw codexRolloutLine
		if err := json.Unmarshal(line, &raw); err != nil {
			result.ParseErrors++
			return
		}
		var payload codexRolloutPayload
		if len(raw.Payload) > 0 {
			if err := json.Unmarshal(raw.Payload, &payload); err != nil {
				result.ParseErrors++
				return
			}
		}
		switch {
		case raw.Type == "session_meta":
			sessionID = payload.ID
			if payload.Model != "" {
				model = payload.Model
			}
			return
		case raw.Type == "turn_context":
			if payload.Model != "" {
				model = payload.Model
			}
			return
		case raw.Type != "event_msg" || payload.Type != "token_count" || payload.Info == nil:
			return // Rate-limit-only token_count events carry no info.
		}

		var usage codexTokenUsage
		total := payload.Info.TotalTokenUsage
		switch {
		case total != nil && prevTotal != nil && *total == *prevTotal:
			return // The same running total again: nothing new was used.
		case payload.Info.LastTokenUsage != nil:
			usage = *payload.Info.LastTokenUsage
		case total != nil && prevTotal != nil:
			delta, ok := total.sub(*prevTotal)
			if !ok {
				delta = *total // The running total restarted.
			}
			usage = delta
		case total != nil:
			usage = *total
		default:
			return
		}
		if total != nil {
			t := *total
			prevTotal = &t
		}

		entry := codexEntry(usage)
		if entry.TotalTokens == 0 {
			return
		}
		if t, err := time.Parse(time.RFC3339Nano, raw.Timestamp); err == nil {
			entry.Timestamp = t
		}
		if !since.IsZero() && entry.Timestamp.Before(since) {
			return
		}
		entry.Model = model
		entry.ConversationID = sessionID
		result.Entries = append(result.Entries, entry)
		result.ParsedEntries++
	})
	result.ParseErrors += skipped
	return err
}

// codexEntry maps OpenAI accounting onto LogEntry's disjoint buckets, so that
// CalculateTotalTokens equals Codex's own total (input + output).
func codexEntry(u codexTokenUsage) *LogEntry {
	input := max(u.InputTokens, 0)
	cached := min(max(u.CachedInputTokens, 0), input)
	written := min(max(u.CacheWriteInputTokens, 0), input-cached)
	entry := &LogEntry{
		Type:              "token_count",
		InputTokens:       input - cached - written,
		CacheReadTokens:   cached,
		CacheCreateTokens: written,
		OutputTokens:      max(u.OutputTokens, 0),
	}
	entry.TotalTokens = entry.CalculateTotalTokens()
	return entry
}

// Ensure CodexScanner implements Scanner interface.
var _ Scanner = (*CodexScanner)(nil)
