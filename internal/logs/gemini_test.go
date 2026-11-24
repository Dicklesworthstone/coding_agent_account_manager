package logs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewGeminiScanner(t *testing.T) {
	oldEnv := os.Getenv("GEMINI_HOME")
	defer os.Setenv("GEMINI_HOME", oldEnv)

	if err := os.Unsetenv("GEMINI_HOME"); err != nil {
		t.Fatalf("Unsetenv(GEMINI_HOME) error: %v", err)
	}

	scanner := NewGeminiScanner()
	if scanner == nil {
		t.Fatal("NewGeminiScanner() returned nil")
	}

	homeDir, _ := os.UserHomeDir()
	expected := filepath.Join(homeDir, ".gemini", "logs")
	if scanner.LogDir() != expected {
		t.Errorf("LogDir() = %q, want %q", scanner.LogDir(), expected)
	}
}

func TestNewGeminiScannerWithDir(t *testing.T) {
	customDir := "/custom/gemini/logs"
	scanner := NewGeminiScannerWithDir(customDir)
	if scanner.LogDir() != customDir {
		t.Errorf("LogDir() = %q, want %q", scanner.LogDir(), customDir)
	}
}

func TestGeminiScanner_ScanMissingDirectory(t *testing.T) {
	scanner := NewGeminiScannerWithDir("/nonexistent/gemini/logs")

	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil for missing directory", err)
	}

	if result.Provider != "gemini" {
		t.Errorf("Provider = %q, want gemini", result.Provider)
	}
	if len(result.Entries) != 0 {
		t.Errorf("Entries = %d, want 0 for missing directory", len(result.Entries))
	}
}

func TestGeminiScanner_ScanEmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	scanner := NewGeminiScannerWithDir(tmpDir)

	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 0 {
		t.Errorf("Entries = %d, want 0 for empty directory", len(result.Entries))
	}
}

func TestGeminiScanner_ScanValidJSONL(t *testing.T) {
	tmpDir := t.TempDir()

	logContent := `{"timestamp":"2025-01-10T12:00:00Z","event":"response","model":"gemini-pro","session_id":"sess-1","message_id":"msg-1","usage":{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300}}
{"time":1736510460,"event_type":"response","model_name":"gemini-ultra","conversation_id":"conv-2","request_id":"req-2","tokens":{"input_tokens":50,"output_tokens":75}}
`
	logFile := filepath.Join(tmpDir, "gemini.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewGeminiScannerWithDir(tmpDir)
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

	entry := result.Entries[0]
	if entry.Model != "gemini-pro" {
		t.Errorf("Entry[0].Model = %q, want gemini-pro", entry.Model)
	}
	if entry.InputTokens != 100 {
		t.Errorf("Entry[0].InputTokens = %d, want 100", entry.InputTokens)
	}
	if entry.OutputTokens != 200 {
		t.Errorf("Entry[0].OutputTokens = %d, want 200", entry.OutputTokens)
	}
	if entry.TotalTokens != 300 {
		t.Errorf("Entry[0].TotalTokens = %d, want 300", entry.TotalTokens)
	}
	if entry.ConversationID != "sess-1" {
		t.Errorf("Entry[0].ConversationID = %q, want sess-1", entry.ConversationID)
	}
	if entry.MessageID != "msg-1" {
		t.Errorf("Entry[0].MessageID = %q, want msg-1", entry.MessageID)
	}

	second := result.Entries[1]
	if second.Model != "gemini-ultra" {
		t.Errorf("Entry[1].Model = %q, want gemini-ultra", second.Model)
	}
	if second.InputTokens != 50 {
		t.Errorf("Entry[1].InputTokens = %d, want 50", second.InputTokens)
	}
	if second.OutputTokens != 75 {
		t.Errorf("Entry[1].OutputTokens = %d, want 75", second.OutputTokens)
	}
	if second.TotalTokens != 125 {
		t.Errorf("Entry[1].TotalTokens = %d, want 125", second.TotalTokens)
	}
	if second.ConversationID != "conv-2" {
		t.Errorf("Entry[1].ConversationID = %q, want conv-2", second.ConversationID)
	}
	if second.MessageID != "req-2" {
		t.Errorf("Entry[1].MessageID = %q, want req-2", second.MessageID)
	}
}

func TestGeminiScanner_ScanMalformedLines(t *testing.T) {
	tmpDir := t.TempDir()

	logContent := `{"timestamp":"2025-01-10T12:00:00Z","event":"response","model":"gemini-pro","usage":{"prompt_tokens":100,"completion_tokens":200}}
not valid json
{"timestamp":"2025-01-10T12:01:00Z","event":"response","model":"gemini-ultra","usage":{"prompt_tokens":50,"completion_tokens":100}}
also not valid {
`
	logFile := filepath.Join(tmpDir, "gemini.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewGeminiScannerWithDir(tmpDir)
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

func TestGeminiScanner_ScanWithTimestampFilter(t *testing.T) {
	tmpDir := t.TempDir()

	logContent := `{"timestamp":"2025-01-10T10:00:00Z","event":"response","model":"old","usage":{"prompt_tokens":100,"completion_tokens":200}}
{"timestamp":"2025-01-10T12:00:00Z","event":"response","model":"new","usage":{"prompt_tokens":50,"completion_tokens":100}}
`
	logFile := filepath.Join(tmpDir, "gemini.jsonl")
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	scanner := NewGeminiScannerWithDir(tmpDir)
	ctx := context.Background()

	since := time.Date(2025, 1, 10, 11, 0, 0, 0, time.UTC)
	result, err := scanner.Scan(ctx, "", since)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(result.Entries))
	}
	if result.Entries[0].Model != "new" {
		t.Errorf("Entry[0].Model = %q, want new", result.Entries[0].Model)
	}
}

func TestGeminiScanner_ScanMultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()

	log1 := `{"timestamp":"2025-01-10T12:00:00Z","event":"response","model":"file1","usage":{"prompt_tokens":100,"completion_tokens":200}}
`
	log2 := `{"timestamp":"2025-01-10T12:01:00Z","event":"response","model":"file2","usage":{"prompt_tokens":50,"completion_tokens":100}}
`
	if err := os.WriteFile(filepath.Join(tmpDir, "log1.jsonl"), []byte(log1), 0600); err != nil {
		t.Fatalf("Failed to write log1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "log2.jsonl"), []byte(log2), 0600); err != nil {
		t.Fatalf("Failed to write log2: %v", err)
	}

	scanner := NewGeminiScannerWithDir(tmpDir)
	ctx := context.Background()
	result, err := scanner.Scan(ctx, "", time.Time{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if len(result.Entries) != 2 {
		t.Errorf("Entries = %d, want 2", len(result.Entries))
	}
}

func TestGeminiScanner_ParseMissingFields(t *testing.T) {
	scanner := NewGeminiScannerWithDir("/ignored")
	entry, err := scanner.parseLine([]byte(`{"event":"response"}`))
	if err != nil {
		t.Fatalf("parseLine() error = %v", err)
	}
	if entry.Model != "" {
		t.Errorf("Model = %q, want empty", entry.Model)
	}
	if !entry.Timestamp.IsZero() {
		t.Errorf("Timestamp = %v, want zero", entry.Timestamp)
	}
}

func TestGeminiScanner_TimestampParsing(t *testing.T) {
	scanner := NewGeminiScannerWithDir("/ignored")
	entry, err := scanner.parseLine([]byte(`{"time":1736510460}`))
	if err != nil {
		t.Fatalf("parseLine() error = %v", err)
	}
	if entry.Timestamp.IsZero() {
		t.Fatal("Timestamp should be parsed")
	}
}

func TestGeminiScannerImplementsScanner(t *testing.T) {
	var _ Scanner = (*GeminiScanner)(nil)
}

// ============== asInt64 Helper Tests ==============

func TestAsInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantVal int64
		wantOK  bool
	}{
		{
			name:    "float64 positive",
			input:   float64(123.0),
			wantVal: 123,
			wantOK:  true,
		},
		{
			name:    "float64 with decimals",
			input:   float64(456.789),
			wantVal: 456, // truncated
			wantOK:  true,
		},
		{
			name:    "int64 positive",
			input:   int64(999),
			wantVal: 999,
			wantOK:  true,
		},
		{
			name:    "int positive",
			input:   int(12345),
			wantVal: 12345,
			wantOK:  true,
		},
		{
			name:    "json.Number integer",
			input:   json.Number("67890"),
			wantVal: 67890,
			wantOK:  true,
		},
		{
			name:    "json.Number float",
			input:   json.Number("123.456"),
			wantVal: 123,
			wantOK:  true,
		},
		{
			name:    "string numeric",
			input:   "54321",
			wantVal: 54321,
			wantOK:  true,
		},
		{
			name:    "string non-numeric",
			input:   "hello",
			wantVal: 0,
			wantOK:  false,
		},
		{
			name:    "nil",
			input:   nil,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name:    "bool",
			input:   true,
			wantVal: 0,
			wantOK:  false,
		},
		{
			name:    "slice",
			input:   []int{1, 2, 3},
			wantVal: 0,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("[TEST] Input: %v (%T)", tt.input, tt.input)
			val, ok := asInt64(tt.input)
			t.Logf("[TEST] Result: val=%d, ok=%v", val, ok)

			if val != tt.wantVal {
				t.Errorf("asInt64() value = %d, want %d", val, tt.wantVal)
			}
			if ok != tt.wantOK {
				t.Errorf("asInt64() ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

func TestAsMap(t *testing.T) {
	t.Run("valid map", func(t *testing.T) {
		input := map[string]any{"key": "value"}
		m, ok := asMap(input)
		if !ok {
			t.Error("asMap() returned false for valid map")
		}
		if m["key"] != "value" {
			t.Errorf("asMap() map[key] = %v, want value", m["key"])
		}
	})

	t.Run("invalid type", func(t *testing.T) {
		input := "not a map"
		_, ok := asMap(input)
		if ok {
			t.Error("asMap() returned true for non-map")
		}
	})
}

func TestExtractInt64(t *testing.T) {
	data := map[string]any{
		"present": int64(100),
		"float":   float64(200.5),
		"string":  "300",
		"non_int": "not a number",
	}

	tests := []struct {
		key  string
		want int64
	}{
		{"present", 100},
		{"float", 200},
		{"string", 300},
		{"non_int", 0},
		{"missing", 0},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Logf("[TEST] Extracting key: %s", tt.key)
			got := extractInt64(data, tt.key)
			t.Logf("[TEST] Result: %d", got)
			if got != tt.want {
				t.Errorf("extractInt64(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}

func TestGeminiScanner_ParseTokenFields(t *testing.T) {
	scanner := NewGeminiScannerWithDir("/ignored")

	tests := []struct {
		name       string
		input      string
		wantInput  int64
		wantOutput int64
		wantTotal  int64
	}{
		{
			name:       "prompt_tokens and completion_tokens",
			input:      `{"usage":{"prompt_tokens":100,"completion_tokens":200,"total_tokens":300}}`,
			wantInput:  100,
			wantOutput: 200,
			wantTotal:  300,
		},
		{
			name:       "input_tokens and output_tokens",
			input:      `{"tokens":{"input_tokens":50,"output_tokens":75}}`,
			wantInput:  50,
			wantOutput: 75,
			wantTotal:  125,
		},
		{
			name:       "cached_content_token_count",
			input:      `{"usage":{"prompt_tokens":100,"cached_content_token_count":30,"completion_tokens":50}}`,
			wantInput:  100,
			wantOutput: 50,
			wantTotal:  150, // input+output (cache tracked separately)
		},
		{
			name:       "empty usage",
			input:      `{}`,
			wantInput:  0,
			wantOutput: 0,
			wantTotal:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("[TEST] Input: %s", tt.input)
			entry, err := scanner.parseLine([]byte(tt.input))
			if err != nil {
				t.Fatalf("parseLine() error = %v", err)
			}
			t.Logf("[TEST] Result: input=%d, output=%d, total=%d",
				entry.InputTokens, entry.OutputTokens, entry.TotalTokens)

			if entry.InputTokens != tt.wantInput {
				t.Errorf("InputTokens = %d, want %d", entry.InputTokens, tt.wantInput)
			}
			if entry.OutputTokens != tt.wantOutput {
				t.Errorf("OutputTokens = %d, want %d", entry.OutputTokens, tt.wantOutput)
			}
			if entry.TotalTokens != tt.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", entry.TotalTokens, tt.wantTotal)
			}
		})
	}
}

func TestGeminiScanner_ParseEntryType(t *testing.T) {
	scanner := NewGeminiScannerWithDir("/ignored")

	tests := []struct {
		name     string
		input    string
		wantType string
	}{
		{
			name:     "event field",
			input:    `{"event":"response"}`,
			wantType: "response",
		},
		{
			name:     "event_type field",
			input:    `{"event_type":"request"}`,
			wantType: "request",
		},
		{
			name:     "type field",
			input:    `{"type":"completion"}`,
			wantType: "completion",
		},
		{
			name:     "no event field",
			input:    `{"model":"gemini"}`,
			wantType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("[TEST] Input: %s", tt.input)
			entry, err := scanner.parseLine([]byte(tt.input))
			if err != nil {
				t.Fatalf("parseLine() error = %v", err)
			}
			t.Logf("[TEST] Result: Type=%q", entry.Type)

			if entry.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", entry.Type, tt.wantType)
			}
		})
	}
}
