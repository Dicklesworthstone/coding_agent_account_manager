package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeScanner reads token usage from Claude Code session transcripts.
//
// Claude Code appends one JSONL transcript per session under
// <config>/projects/<munged-project-path>/, with subagent transcripts in
// nested directories. <config> is $CLAUDE_CONFIG_DIR when set; otherwise
// both ~/.config/claude and ~/.claude are read, since Claude Code has used
// each as its default. Every API response is an "assistant" line whose
// message carries id, model and usage; a response with several content
// blocks is written as several lines repeating the same message id and
// request id, so usage is counted once per (message id, request id).
type ClaudeScanner struct {
	logDirs []string
}

// NewClaudeScanner creates a scanner over Claude Code's transcript stores.
func NewClaudeScanner() *ClaudeScanner {
	return &ClaudeScanner{logDirs: defaultClaudeProjectDirs()}
}

// NewClaudeScannerWithDir creates a scanner over one custom transcript root.
// Useful for testing.
func NewClaudeScannerWithDir(logDir string) *ClaudeScanner {
	return &ClaudeScanner{logDirs: []string{logDir}}
}

func defaultClaudeProjectDirs() []string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return []string{filepath.Join(dir, "projects")}
	}
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".config", "claude", "projects"),
		filepath.Join(home, ".claude", "projects"),
	}
}

// LogDir returns the primary transcript root.
func (s *ClaudeScanner) LogDir() string {
	if len(s.logDirs) == 0 {
		return ""
	}
	return s.logDirs[0]
}

// Scan returns one entry per Claude API response at or after since. If logDir
// is empty, every default transcript root is read.
func (s *ClaudeScanner) Scan(ctx context.Context, logDir string, since time.Time) (*ScanResult, error) {
	roots := s.logDirs
	if logDir != "" {
		roots = []string{logDir}
	}

	result := &ScanResult{
		Provider: "claude",
		Since:    since,
		Until:    time.Now(),
		Entries:  make([]*LogEntry, 0),
	}
	responses := make(map[string]*LogEntry)
	seenRoots := make(map[string]bool)
	for _, root := range roots {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			if seenRoots[resolved] {
				continue // Both defaults can point at one store.
			}
			seenRoots[resolved] = true
		}
		skipped, err := walkJSONLFiles(ctx, root, since, func(path string) error {
			return s.scanFile(ctx, path, since, result, responses)
		})
		result.ParseErrors += skipped
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *ClaudeScanner) scanFile(ctx context.Context, path string, since time.Time, result *ScanResult, responses map[string]*LogEntry) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	skipped, err := forEachJSONLLine(ctx, f, func(line []byte) {
		result.TotalEntries++
		// Most transcript bytes are user turns and tool output. Only decode
		// lines that can be an assistant response with usage.
		if !bytes.Contains(line, []byte(`"usage"`)) || !bytes.Contains(line, []byte(`"assistant"`)) {
			return
		}
		entry, key, err := parseClaudeTranscriptLine(line)
		if err != nil {
			result.ParseErrors++
			return
		}
		if entry == nil || (!since.IsZero() && entry.Timestamp.Before(since)) {
			return
		}
		if key != "" {
			if prev := responses[key]; prev != nil {
				mergeClaudeResponse(prev, entry)
				return
			}
			responses[key] = entry
		}
		result.Entries = append(result.Entries, entry)
		result.ParsedEntries++
	})
	result.ParseErrors += skipped
	return err
}

type claudeTranscriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Message   *struct {
		ID    string       `json:"id"`
		Model string       `json:"model"`
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// parseClaudeTranscriptLine returns the usage entry of one assistant line and
// the key that identifies its API response. It returns a nil entry for lines
// that carry no billable usage: other line types, Claude Code's "<synthetic>"
// placeholder replies, and all-zero usage.
func parseClaudeTranscriptLine(line []byte) (*LogEntry, string, error) {
	var raw claudeTranscriptLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, "", err
	}
	if raw.Type != "assistant" || raw.Message == nil || raw.Message.Usage == nil || raw.Message.Model == "<synthetic>" {
		return nil, "", nil
	}
	u := raw.Message.Usage
	entry := &LogEntry{
		Type:              raw.Type,
		Model:             raw.Message.Model,
		ConversationID:    raw.SessionID,
		MessageID:         raw.Message.ID,
		InputTokens:       max(u.InputTokens, 0),
		OutputTokens:      max(u.OutputTokens, 0),
		CacheReadTokens:   max(u.CacheReadInputTokens, 0),
		CacheCreateTokens: max(u.CacheCreationInputTokens, 0),
	}
	entry.TotalTokens = entry.CalculateTotalTokens()
	if entry.TotalTokens == 0 {
		return nil, "", nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw.Timestamp); err == nil {
		entry.Timestamp = t
	}
	key := ""
	if raw.Message.ID != "" || raw.RequestID != "" {
		key = raw.Message.ID + "\x00" + raw.RequestID
	}
	return entry, key, nil
}

// mergeClaudeResponse folds a repeated line of one API response into the
// entry already counted. The repeats normally carry identical usage; when an
// earlier line was written mid-stream with a partial count, the larger value
// is the response's real total.
func mergeClaudeResponse(dst, src *LogEntry) {
	dst.InputTokens = max(dst.InputTokens, src.InputTokens)
	dst.OutputTokens = max(dst.OutputTokens, src.OutputTokens)
	dst.CacheReadTokens = max(dst.CacheReadTokens, src.CacheReadTokens)
	dst.CacheCreateTokens = max(dst.CacheCreateTokens, src.CacheCreateTokens)
	dst.TotalTokens = dst.CalculateTotalTokens()
}

// Ensure ClaudeScanner implements Scanner interface.
var _ Scanner = (*ClaudeScanner)(nil)
