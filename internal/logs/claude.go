package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ClaudeScanner parses Claude Code JSONL logs.
// Claude Code stores logs at ~/.local/share/claude/logs/ in JSONL format.
type ClaudeScanner struct {
	logDir string
}

// NewClaudeScanner creates a scanner with default log directory.
func NewClaudeScanner() *ClaudeScanner {
	homeDir, _ := os.UserHomeDir()
	return &ClaudeScanner{
		logDir: filepath.Join(homeDir, ".local", "share", "claude", "logs"),
	}
}

// NewClaudeScannerWithDir creates a scanner with a custom log directory.
// Useful for testing.
func NewClaudeScannerWithDir(logDir string) *ClaudeScanner {
	return &ClaudeScanner{logDir: logDir}
}

// LogDir returns the default log directory.
func (s *ClaudeScanner) LogDir() string {
	return s.logDir
}

// Scan parses logs since the given time.
// If logDir is empty, uses the default log directory.
func (s *ClaudeScanner) Scan(ctx context.Context, logDir string, since time.Time) (*ScanResult, error) {
	if logDir == "" {
		logDir = s.logDir
	}

	result := &ScanResult{
		Provider: "claude",
		Since:    since,
		Until:    time.Now(),
		Entries:  make([]*LogEntry, 0),
	}

	// Check if directory exists
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		// No logs directory = no logs, not an error
		return result, nil
	}

	// Find all .jsonl files
	files, err := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	if err != nil {
		return result, err
	}

	for _, file := range files {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		// Check file modification time for quick filtering
		info, err := os.Stat(file)
		if err != nil {
			result.ParseErrors++
			continue
		}

		// Skip files that haven't been modified since our cutoff
		// (they can't contain new entries)
		if !since.IsZero() && info.ModTime().Before(since) {
			continue
		}

		if err := s.scanFile(ctx, file, since, result); err != nil {
			// Log error but continue with other files
			result.ParseErrors++
		}
	}

	return result, nil
}

// scanFile parses a single JSONL file.
func (s *ClaudeScanner) scanFile(ctx context.Context, filePath string, since time.Time, result *ScanResult) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Increase buffer size for potentially large log lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max line size

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		result.TotalEntries++
		line := scanner.Bytes()

		entry, err := s.parseLine(line)
		if err != nil {
			result.ParseErrors++
			continue
		}

		// Filter by timestamp
		if !since.IsZero() && entry.Timestamp.Before(since) {
			continue
		}

		result.Entries = append(result.Entries, entry)
		result.ParsedEntries++
	}

	return scanner.Err()
}

// claudeLogEntry represents the raw JSON structure from Claude logs.
type claudeLogEntry struct {
	Timestamp        string       `json:"timestamp"`
	Type             string       `json:"type"`
	Model            string       `json:"model"`
	ConversationUUID string       `json:"conversation_uuid"`
	MessageUUID      string       `json:"message_uuid"`
	Usage            *claudeUsage `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// parseLine parses a single JSONL line into a LogEntry.
func (s *ClaudeScanner) parseLine(line []byte) (*LogEntry, error) {
	var raw claudeLogEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}

	entry := &LogEntry{
		Type:           raw.Type,
		Model:          raw.Model,
		ConversationID: raw.ConversationUUID,
		MessageID:      raw.MessageUUID,
		Raw:            make(map[string]any),
	}

	// Parse timestamp
	if raw.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw.Timestamp); err == nil {
			entry.Timestamp = t
		} else if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
			entry.Timestamp = t
		}
		// If parsing fails, leave as zero time
	}

	// Extract token counts
	if raw.Usage != nil {
		entry.InputTokens = raw.Usage.InputTokens
		entry.OutputTokens = raw.Usage.OutputTokens
		entry.CacheReadTokens = raw.Usage.CacheReadInputTokens
		entry.CacheCreateTokens = raw.Usage.CacheCreationInputTokens
		entry.TotalTokens = entry.CalculateTotalTokens()
	}

	return entry, nil
}

// Ensure ClaudeScanner implements Scanner interface.
var _ Scanner = (*ClaudeScanner)(nil)
