package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
)

func TestCostCommand(t *testing.T) {
	if costCmd.Use != "cost" {
		t.Errorf("Expected Use 'cost', got %q", costCmd.Use)
	}

	if costCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

func TestCostSubcommands(t *testing.T) {
	// Check sessions subcommand
	if costSessionsCmd.Use != "sessions" {
		t.Errorf("Expected Use 'sessions', got %q", costSessionsCmd.Use)
	}

	// Check rates subcommand
	if costRatesCmd.Use != "rates" {
		t.Errorf("Expected Use 'rates', got %q", costRatesCmd.Use)
	}
}

func TestCostFlags(t *testing.T) {
	// Check main command flags
	flags := []string{"provider", "since", "json"}
	for _, name := range flags {
		flag := costCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Flag %q not found on cost command", name)
		}
	}

	// Check sessions flags
	sessionsFlags := []string{"limit", "provider", "since", "json"}
	for _, name := range sessionsFlags {
		flag := costSessionsCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Flag %q not found on cost sessions command", name)
		}
	}

	// Check rates flags
	ratesFlags := []string{"json", "set", "per-minute", "per-session"}
	for _, name := range ratesFlags {
		flag := costRatesCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Flag %q not found on cost rates command", name)
		}
	}
}

func TestFormatDollars(t *testing.T) {
	tests := []struct {
		cents    int
		expected string
	}{
		{0, "$0.00"},
		{1, "$0.01"},
		{50, "$0.50"},
		{100, "$1.00"},
		{150, "$1.50"},
		{1234, "$12.34"},
		{10000, "$100.00"},
	}

	for _, tc := range tests {
		got := formatDollars(tc.cents)
		if got != tc.expected {
			t.Errorf("formatDollars(%d) = %q, want %q", tc.cents, got, tc.expected)
		}
	}
}

func TestRenderCostSummary_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := renderCostSummary(&buf, nil, time.Time{})
	if err != nil {
		t.Fatalf("renderCostSummary() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty output for empty summaries")
	}
}

func TestRenderCostSummaryJSON_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := renderCostSummaryJSON(&buf, nil, time.Time{})
	if err != nil {
		t.Fatalf("renderCostSummaryJSON() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty JSON output")
	}
}

func TestRenderSessions_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := renderSessions(&buf, nil)
	if err != nil {
		t.Fatalf("renderSessions() error = %v", err)
	}

	output := buf.String()
	// Should still have header
	if output == "" {
		t.Error("Expected header row even with empty sessions")
	}
}

func TestRenderSessionsJSON_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := renderSessionsJSON(&buf, nil)
	if err != nil {
		t.Fatalf("renderSessionsJSON() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty JSON output")
	}
}

func TestRenderRates(t *testing.T) {
	rates := []caamdb.CostRate{
		{Provider: "claude", CentsPerMinute: 5, CentsPerSession: 0, UpdatedAt: time.Now()},
		{Provider: "codex", CentsPerMinute: 3, CentsPerSession: 10, UpdatedAt: time.Now()},
	}

	var buf bytes.Buffer
	err := renderRates(&buf, rates)
	if err != nil {
		t.Fatalf("renderRates() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty output")
	}
}

func TestRenderRatesJSON(t *testing.T) {
	rates := []caamdb.CostRate{
		{Provider: "claude", CentsPerMinute: 5, CentsPerSession: 0, UpdatedAt: time.Now()},
	}

	var buf bytes.Buffer
	err := renderRatesJSON(&buf, rates)
	if err != nil {
		t.Fatalf("renderRatesJSON() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty JSON output")
	}
}

func TestCostRatesCommand_SetRate(t *testing.T) {
	// Set up temp environment
	tmpDir := t.TempDir()
	oldCaamHome := os.Getenv("CAAM_HOME")
	os.Setenv("CAAM_HOME", tmpDir)
	defer os.Setenv("CAAM_HOME", oldCaamHome)

	// Create DB directory
	dbDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		t.Fatalf("mkdir error: %v", err)
	}

	// Open DB to run migrations and test SetCostRate directly
	db, err := caamdb.OpenAt(filepath.Join(dbDir, "caam.db"))
	if err != nil {
		t.Fatalf("OpenAt error: %v", err)
	}
	defer db.Close()

	// Test SetCostRate function directly
	if err := db.SetCostRate("claude", 10, 5); err != nil {
		t.Fatalf("SetCostRate error: %v", err)
	}

	// Verify rate was set
	rate, err := db.GetCostRate("claude")
	if err != nil {
		t.Fatalf("GetCostRate error: %v", err)
	}

	if rate.CentsPerMinute != 10 {
		t.Errorf("CentsPerMinute = %d, want 10", rate.CentsPerMinute)
	}
	if rate.CentsPerSession != 5 {
		t.Errorf("CentsPerSession = %d, want 5", rate.CentsPerSession)
	}
}

// =============================================================================
// Token Cost Analysis Tests
// =============================================================================

func TestCostTokensSubcommand(t *testing.T) {
	if costTokensCmd.Use != "tokens [provider]" {
		t.Errorf("Expected Use 'tokens [provider]', got %q", costTokensCmd.Use)
	}

	if costTokensCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

func TestCostTokensFlags(t *testing.T) {
	flags := []string{"last", "format"}
	for _, name := range flags {
		flag := costTokensCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Flag %q not found on cost tokens command", name)
		}
	}
}

// `caam cost tokens` returned [] on a host with fresh Claude Code transcripts
// and Codex rollouts: neither scanner read where those CLIs write (issue #126).
func TestCostTokensReadsClaudeAndCodexSessionLogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("GEMINI_HOME", filepath.Join(home, "no-gemini"))
	ts := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude", "projects", "-work-proj", "s.jsonl"),
		`{"type":"user","message":{"role":"user","content":"hi"},"timestamp":"`+ts+`"}`+"\n"+
			`{"type":"assistant","sessionId":"s","requestId":"req_1","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":40,"output_tokens_details":{},"server_tool_use":{}}},"timestamp":"`+ts+`"}`+"\n")
	write(filepath.Join(home, ".codex", "sessions", "2026", "10", "15", "rollout-x.jsonl"),
		`{"timestamp":"`+ts+`","type":"turn_context","payload":{"model":"gpt-5-codex"}}`+"\n"+
			`{"timestamp":"`+ts+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":7,"total_tokens":107},"last_token_usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":7,"total_tokens":107}}}}`+"\n")

	c := &cobra.Command{RunE: runCostTokens}
	c.Flags().String("last", "48h", "")
	c.Flags().String("format", "json", "")
	var out bytes.Buffer
	c.SetOut(&out)
	if err := runCostTokens(c, nil); err != nil {
		t.Fatalf("cost tokens: %v", err)
	}
	var analyses []TokenCostAnalysis
	if err := json.Unmarshal(out.Bytes(), &analyses); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	got := map[string]TokenCostAnalysis{}
	for _, a := range analyses {
		got[a.Provider] = a
	}
	claude, codex := got["claude"], got["codex"]
	if len(analyses) != 2 || claude.TotalTokens != 370 || codex.TotalTokens != 107 {
		t.Fatalf("analyses = %s, want claude 370 and codex 107 tokens", out.String())
	}
	if len(claude.ByModel) != 1 || claude.ByModel[0].Model != "claude-opus-5-5" || claude.CacheReadTokens != 300 {
		t.Fatalf("claude per-model = %+v", claude)
	}
	if len(codex.ByModel) != 1 || codex.ByModel[0].Model != "gpt-5-codex" || codex.CacheReadTokens != 60 || codex.InputTokens != 40 {
		t.Fatalf("codex per-model = %+v", codex)
	}
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		tokens   int64
		expected string
	}{
		{0, "0"},
		{500, "500"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{10000, "10.0K"},
		{100000, "100.0K"},
		{1000000, "1.0M"},
		{5500000, "5.5M"},
	}

	for _, tc := range tests {
		got := formatTokenCount(tc.tokens)
		if got != tc.expected {
			t.Errorf("formatTokenCount(%d) = %q, want %q", tc.tokens, got, tc.expected)
		}
	}
}

func TestTokenPercentage(t *testing.T) {
	tests := []struct {
		part, total int64
		expected    int
	}{
		{0, 0, 0},
		{0, 100, 0},
		{50, 100, 50},
		{25, 100, 25},
		{100, 100, 100},
		{33, 100, 33},
	}

	for _, tc := range tests {
		got := tokenPercentage(tc.part, tc.total)
		if got != tc.expected {
			t.Errorf("tokenPercentage(%d, %d) = %d, want %d", tc.part, tc.total, got, tc.expected)
		}
	}
}

func TestTokenProgressBar(t *testing.T) {
	tests := []struct {
		percent float64
		width   int
		filled  int
	}{
		{0, 10, 0},
		{50, 10, 5},
		{100, 10, 10},
		{75, 20, 15},
	}

	for _, tc := range tests {
		got := tokenProgressBar(tc.percent, tc.width)
		if len(got) != tc.width {
			t.Errorf("tokenProgressBar(%.0f, %d) length = %d, want %d", tc.percent, tc.width, len(got), tc.width)
		}
	}
}

func TestFormatTokenPeriod(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{1 * time.Hour, "1h"},
		{12 * time.Hour, "12h"},
		{24 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
		{168 * time.Hour, "7d"},
		{720 * time.Hour, "30d"},
	}

	for _, tc := range tests {
		got := formatTokenPeriod(tc.d)
		if got != tc.expected {
			t.Errorf("formatTokenPeriod(%v) = %q, want %q", tc.d, got, tc.expected)
		}
	}
}

func TestTruncateTokenModel(t *testing.T) {
	tests := []struct {
		s, expected string
		maxLen      int
	}{
		{"short", "short", 10},
		{"exactly10!", "exactly10!", 10},
		{"verylongmodel", "verylong...", 11},
		{"claude-3-opus", "claude-3-opus", 20},
	}

	for _, tc := range tests {
		got := truncateTokenModel(tc.s, tc.maxLen)
		if got != tc.expected {
			t.Errorf("truncateTokenModel(%q, %d) = %q, want %q", tc.s, tc.maxLen, got, tc.expected)
		}
	}
}

func TestRenderTokenCostTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	err := renderTokenCostTable(&buf, nil)
	if err != nil {
		t.Fatalf("renderTokenCostTable() error = %v", err)
	}
	// Empty input should produce no output
	if buf.Len() != 0 {
		t.Error("Expected empty output for nil analyses")
	}
}

func TestRenderTokenCostTable_WithData(t *testing.T) {
	analyses := []TokenCostAnalysis{
		{
			Provider:         "claude",
			Period:           "7d",
			TotalTokens:      1000000,
			InputTokens:      400000,
			OutputTokens:     600000,
			TotalAPICost:     15.50,
			SubscriptionCost: 46.67,
			Savings:          -31.17,
			SavingsPercent:   -201.1,
			ByModel: []TokenModelCost{
				{Model: "claude-3-opus", TotalTokens: 1000000, Percent: 100, APICost: 15.50},
			},
		},
	}

	var buf bytes.Buffer
	err := renderTokenCostTable(&buf, analyses)
	if err != nil {
		t.Fatalf("renderTokenCostTable() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("TOKEN COST ANALYSIS")) {
		t.Error("Expected header in output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("CLAUDE")) {
		t.Error("Expected provider name in output")
	}
}

func TestRenderTokenCostCSV_WithData(t *testing.T) {
	analyses := []TokenCostAnalysis{
		{
			Provider:         "claude",
			Period:           "7d",
			TotalTokens:      1000,
			InputTokens:      400,
			OutputTokens:     600,
			TotalAPICost:     0.01,
			SubscriptionCost: 46.67,
			ByModel: []TokenModelCost{
				{Model: "claude-3-opus", InputTokens: 400, OutputTokens: 600, TotalTokens: 1000, Percent: 100, APICost: 0.01},
			},
		},
	}

	var buf bytes.Buffer
	err := renderTokenCostCSV(&buf, analyses)
	if err != nil {
		t.Fatalf("renderTokenCostCSV() error = %v", err)
	}

	output := buf.String()
	if output == "" {
		t.Error("Expected non-empty CSV output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("provider,period,model")) {
		t.Error("Expected CSV header row")
	}
}
