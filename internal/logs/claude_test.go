package logs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewClaudeScanner(t *testing.T) {
	scanner := NewClaudeScanner()
	if scanner == nil {
		t.Fatal("NewClaudeScanner() returned nil")
	}

	homeDir, _ := os.UserHomeDir()
	expectedDir := filepath.Join(homeDir, ".local", "share", "claude", "logs")
	if scanner.LogDir() != expectedDir {
		t.Errorf("LogDir() = %q, want %q", scanner.LogDir(), expectedDir)
	}
}

func TestNewClaudeScannerWithDir(t *testing.T) {
	customDir := "/custom/logs"
	scanner := NewClaudeScannerWithDir(customDir)
	if scanner.LogDir() != customDir {
		t.Errorf("LogDir() = %q, want %q", scanner.LogDir(), customDir)
	}
}

func TestClaudeScanner_ScanMissingDirectory(t *testing.T) {
	scanner := NewClaudeScannerWithDir("/nonexistent/directory/that/does/not/exist")

	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil for missing directory", err)
	}

	if result.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", result.Provider)
	}
	if len(result.Entries) != 0 {
		t.Errorf("Entries = %d, want 0 for missing directory", len(result.Entries))
	}
}

func TestClaudeScanner_ScanEmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewClaudeScannerWithDir(tmpDir)

	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 0 {
		t.Errorf("Entries = %d, want 0 for empty directory", len(result.Entries))
	}
}

func TestClaudeScanner_ScanValidJSONL(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test JSONL file
	logContent := `{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"claude-3-opus","conversation_uuid":"conv-123","message_uuid":"msg-456","usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":50,"cache_creation_input_tokens":25}}
{"timestamp":"2025-01-10T12:01:00Z","type":"response","model":"claude-3-sonnet","conversation_uuid":"conv-123","message_uuid":"msg-789","usage":{"input_tokens":50,"output_tokens":100}}
`
	logFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if result.TotalEntries != 2 {
		t.Errorf("TotalEntries = %d, want 2", result.TotalEntries)
	}
	if result.ParsedEntries != 2 {
		t.Errorf("ParsedEntries = %d, want 2", result.ParsedEntries)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2", len(result.Entries))
	}

	// Check first entry
	entry := result.Entries[0]
	if entry.Model != "claude-3-opus" {
		t.Errorf("Entry[0].Model = %q, want claude-3-opus", entry.Model)
	}
	if entry.InputTokens != 100 {
		t.Errorf("Entry[0].InputTokens = %d, want 100", entry.InputTokens)
	}
	if entry.OutputTokens != 200 {
		t.Errorf("Entry[0].OutputTokens = %d, want 200", entry.OutputTokens)
	}
	if entry.CacheReadTokens != 50 {
		t.Errorf("Entry[0].CacheReadTokens = %d, want 50", entry.CacheReadTokens)
	}
	if entry.CacheCreateTokens != 25 {
		t.Errorf("Entry[0].CacheCreateTokens = %d, want 25", entry.CacheCreateTokens)
	}
	if entry.TotalTokens != 375 {
		t.Errorf("Entry[0].TotalTokens = %d, want 375", entry.TotalTokens)
	}
	if entry.ConversationID != "conv-123" {
		t.Errorf("Entry[0].ConversationID = %q, want conv-123", entry.ConversationID)
	}
}

func TestClaudeScanner_ScanMalformedLines(t *testing.T) {
	tmpDir := t.TempDir()

	// Mix of valid and invalid lines
	logContent := `{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"claude-3-opus","usage":{"input_tokens":100,"output_tokens":200}}
not valid json
{"timestamp":"2025-01-10T12:01:00Z","type":"response","model":"claude-3-sonnet","usage":{"input_tokens":50,"output_tokens":100}}
also not valid {
`
	logFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if result.TotalEntries != 4 {
		t.Errorf("TotalEntries = %d, want 4", result.TotalEntries)
	}
	if result.ParsedEntries != 2 {
		t.Errorf("ParsedEntries = %d, want 2", result.ParsedEntries)
	}
	if result.ParseErrors != 2 {
		t.Errorf("ParseErrors = %d, want 2", result.ParseErrors)
	}
}

func TestClaudeScanner_ScanWithTimestampFilter(t *testing.T) {
	tmpDir := t.TempDir()

	logContent := `{"timestamp":"2025-01-10T10:00:00Z","type":"response","model":"old","usage":{"input_tokens":100,"output_tokens":200}}
{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"new","usage":{"input_tokens":50,"output_tokens":100}}
`
	logFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()

	// Filter to only get entries after 11:00
	since := time.Date(2025, 1, 10, 11, 0, 0, 0, time.UTC)
	result, err := scanner.Scan(ctx, "", since)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1 (only new entry)", len(result.Entries))
	}
	if result.Entries[0].Model != "new" {
		t.Errorf("Entry[0].Model = %q, want new", result.Entries[0].Model)
	}
}

func TestClaudeScanner_ScanMultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two log files
	log1 := `{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"file1","usage":{"input_tokens":100,"output_tokens":200}}
`
	log2 := `{"timestamp":"2025-01-10T12:01:00Z","type":"response","model":"file2","usage":{"input_tokens":50,"output_tokens":100}}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "log1.jsonl"), []byte(log1), 0600); err != nil {
		t.Fatalf("Failed to write log1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "log2.jsonl"), []byte(log2), 0600); err != nil {
		t.Fatalf("Failed to write log2: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 2 {
		t.Errorf("Entries = %d, want 2", len(result.Entries))
	}
}

func TestClaudeScanner_ScanWithContext(t *testing.T) {
	tmpDir := t.TempDir()

	logContent := `{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"test","usage":{"input_tokens":100,"output_tokens":200}}
`
	logFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)

	// Use canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != context.Canceled {
		t.Errorf("Scan() with canceled context error = %v, want context.Canceled", err)
	}
	// Should still return partial result
	if result == nil {
		t.Error("Scan() with canceled context should return partial result, got nil")
	}
}

func TestClaudeScanner_ParseMissingFields(t *testing.T) {
	tmpDir := t.TempDir()

	// Entry with minimal fields
	logContent := `{"type":"response"}
{"timestamp":"2025-01-10T12:00:00Z"}
{"usage":{"input_tokens":100}}
`
	logFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// All should parse successfully with zero values for missing fields
	if result.ParseErrors != 0 {
		t.Errorf("ParseErrors = %d, want 0 (missing fields should use zero values)", result.ParseErrors)
	}
	if result.ParsedEntries != 3 {
		t.Errorf("ParsedEntries = %d, want 3", result.ParsedEntries)
	}
}

func TestClaudeScanner_TimestampParsing(t *testing.T) {
	tmpDir := t.TempDir()

	// Various timestamp formats
	logContent := `{"timestamp":"2025-01-10T12:00:00Z","type":"response"}
{"timestamp":"2025-01-10T12:00:00.123Z","type":"response"}
{"timestamp":"2025-01-10T12:00:00.123456789Z","type":"response"}
`
	logFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	for i, entry := range result.Entries {
		if entry.Timestamp.IsZero() {
			t.Errorf("Entry[%d].Timestamp is zero, expected parsed time", i)
		}
	}
}

func TestClaudeScanner_CustomLogDir(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "custom")
	if err := os.MkdirAll(customDir, 0700); err != nil {
		t.Fatalf("Failed to create custom dir: %v", err)
	}

	logContent := `{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"custom","usage":{"input_tokens":100,"output_tokens":200}}
`
	if err := os.WriteFile(filepath.Join(customDir, "test.jsonl"), []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Scanner with different default, but pass custom dir to Scan
	scanner := NewClaudeScannerWithDir("/wrong/default")
	ctx := context.Background()
	result, err := scanner.Scan(ctx, customDir, time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 1 {
		t.Errorf("Entries = %d, want 1", len(result.Entries))
	}
}

func TestClaudeScanner_IgnoresNonJSONL(t *testing.T) {
	tmpDir := t.TempDir()

	// Create JSONL file and non-JSONL file
	jsonl := `{"timestamp":"2025-01-10T12:00:00Z","type":"response","model":"test","usage":{"input_tokens":100,"output_tokens":200}}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "test.jsonl"), []byte(jsonl), 0600); err != nil {
		t.Fatalf("Failed to write jsonl: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("not a log file"), 0600); err != nil {
		t.Fatalf("Failed to write txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "test.json"), []byte(`{"foo":"bar"}`), 0600); err != nil {
		t.Fatalf("Failed to write json: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	// Should only process the .jsonl file
	if result.TotalEntries != 1 {
		t.Errorf("TotalEntries = %d, want 1 (only .jsonl)", result.TotalEntries)
	}
}

func TestClaudeScanner_TokenUsageAggregation(t *testing.T) {
	tmpDir := t.TempDir()

	logContent := `{"timestamp":"2025-01-10T12:00:00Z","model":"claude-3-opus","usage":{"input_tokens":100,"output_tokens":200}}
{"timestamp":"2025-01-10T12:01:00Z","model":"claude-3-opus","usage":{"input_tokens":50,"output_tokens":100}}
{"timestamp":"2025-01-10T12:02:00Z","model":"claude-3-sonnet","usage":{"input_tokens":25,"output_tokens":50}}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "test.jsonl"), []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewClaudeScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	usage := result.TokenUsage()
	if usage.TotalTokens != 525 {
		t.Errorf("TokenUsage().TotalTokens = %d, want 525", usage.TotalTokens)
	}
	if usage.InputTokens != 175 {
		t.Errorf("TokenUsage().InputTokens = %d, want 175", usage.InputTokens)
	}

	opus := usage.ByModel["claude-3-opus"]
	if opus == nil {
		t.Fatal("ByModel[claude-3-opus] is nil")
	}
	if opus.TotalTokens != 450 {
		t.Errorf("opus.TotalTokens = %d, want 450", opus.TotalTokens)
	}
}

func TestClaudeScannerImplementsScanner(t *testing.T) {
	var _ Scanner = (*ClaudeScanner)(nil)
}
