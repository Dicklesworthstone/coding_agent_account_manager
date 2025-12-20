package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

// setupHistoryTestEnv sets up a test environment for history tests.
func setupHistoryTestEnv(t *testing.T) (tmpDir string, cleanup func()) {
	t.Helper()
	tmpDir = t.TempDir()

	oldCodexHome := os.Getenv("CODEX_HOME")
	oldCaamHome := os.Getenv("CAAM_HOME")

	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}

	// Use a temp vault
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))

	cleanup = func() {
		_ = os.Setenv("CODEX_HOME", oldCodexHome)
		_ = os.Setenv("CAAM_HOME", oldCaamHome)
		vault = oldVault
	}

	return tmpDir, cleanup
}

func TestHistory_ListsEvents(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Log some events
	_ = db.LogEvent(caamdb.Event{
		Type:        caamdb.EventActivate,
		Provider:    "codex",
		ProfileName: "work",
	})
	_ = db.LogEvent(caamdb.Event{
		Type:        caamdb.EventRefresh,
		Provider:    "claude",
		ProfileName: "personal",
	})

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 20, "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", false, "")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "codex") || !strings.Contains(output, "work") {
		t.Errorf("output should contain codex/work event, got: %s", output)
	}
	if !strings.Contains(output, "claude") || !strings.Contains(output, "personal") {
		t.Errorf("output should contain claude/personal event, got: %s", output)
	}
}

func TestHistory_FilterByProvider(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Log events for different providers
	_ = db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"})
	_ = db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: "claude", ProfileName: "personal"})

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 20, "")
	cmd.Flags().String("provider", "codex", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("provider", "codex")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "codex") {
		t.Errorf("output should contain codex, got: %s", output)
	}
	if strings.Contains(output, "claude") {
		t.Errorf("output should NOT contain claude when filtering by codex, got: %s", output)
	}
}

func TestHistory_FilterByType(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Log events of different types
	_ = db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"})
	_ = db.LogEvent(caamdb.Event{Type: caamdb.EventError, Provider: "codex", ProfileName: "work"})

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 20, "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "activate", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("type", "activate")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "activate") {
		t.Errorf("output should contain activate, got: %s", output)
	}
	// Error events should be filtered out
}

func TestHistory_JSONOutput(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Log an event
	_ = db.LogEvent(caamdb.Event{
		Type:        caamdb.EventActivate,
		Provider:    "codex",
		ProfileName: "work",
	})

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 20, "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", true, "")
	_ = cmd.Flags().Set("json", "true")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	output := buf.String()

	// Should be valid JSON
	var result historyOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("output should be valid JSON, got error: %v\noutput: %s", err, output)
	}

	if result.Count != 1 {
		t.Errorf("expected count=1, got %d", result.Count)
	}
	if len(result.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(result.Events))
	}
	if result.Events[0].Provider != "codex" {
		t.Errorf("expected provider=codex, got %s", result.Events[0].Provider)
	}
}

func TestHistory_EmptyNoEvents(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 20, "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", false, "")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No events") {
		t.Errorf("output should indicate no events, got: %s", output)
	}
}

func TestHistory_EmptyJSON(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 20, "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", true, "")
	_ = cmd.Flags().Set("json", "true")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	output := buf.String()

	// Should be valid JSON with empty events
	var result historyOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("output should be valid JSON, got error: %v\noutput: %s", err, output)
	}

	if result.Count != 0 {
		t.Errorf("expected count=0, got %d", result.Count)
	}
}

func TestHistory_LimitFlag(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Log 10 events
	for i := 0; i < 10; i++ {
		_ = db.LogEvent(caamdb.Event{
			Type:        caamdb.EventActivate,
			Provider:    "codex",
			ProfileName: "work",
		})
	}

	cmd := &cobra.Command{}
	cmd.Flags().IntP("limit", "n", 3, "")
	cmd.Flags().String("provider", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("type", "", "")
	cmd.Flags().String("since", "", "")
	cmd.Flags().Bool("json", true, "")
	_ = cmd.Flags().Set("limit", "3")
	_ = cmd.Flags().Set("json", "true")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runHistory(cmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}

	var result historyOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output should be valid JSON: %v", err)
	}

	if result.Count != 3 {
		t.Errorf("expected count=3, got %d", result.Count)
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"1h", time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"", 0, true},
		{"invalid", 0, true},
		{"xd", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result, err := parseDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("parseDuration(%q) expected error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDuration(%q) error = %v", tc.input, err)
			}
			if result != tc.expected {
				t.Errorf("parseDuration(%q) = %v, want %v", tc.input, result, tc.expected)
			}
		})
	}
}

func TestFilterEvents(t *testing.T) {
	now := time.Now()
	events := []caamdb.Event{
		{Type: "activate", Provider: "codex", ProfileName: "work", Timestamp: now},
		{Type: "error", Provider: "codex", ProfileName: "work", Timestamp: now.Add(-2 * time.Hour)},
		{Type: "activate", Provider: "claude", ProfileName: "personal", Timestamp: now.Add(-1 * time.Hour)},
		{Type: "refresh", Provider: "gemini", ProfileName: "test", Timestamp: now.Add(-3 * time.Hour)},
	}

	tests := []struct {
		name      string
		provider  string
		profile   string
		eventType string
		since     time.Time
		expected  int
	}{
		{"no filter", "", "", "", time.Time{}, 4},
		{"by provider", "codex", "", "", time.Time{}, 2},
		{"by profile", "", "work", "", time.Time{}, 2},
		{"by type", "", "", "activate", time.Time{}, 2},
		{"by since", "", "", "", now.Add(-90 * time.Minute), 2},
		{"combined", "codex", "", "activate", time.Time{}, 1},
		{"no match", "unknown", "", "", time.Time{}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := filterEvents(events, tc.provider, tc.profile, tc.eventType, tc.since)
			if len(result) != tc.expected {
				t.Errorf("filterEvents() returned %d events, want %d", len(result), tc.expected)
			}
		})
	}
}

func TestRenderEventsJSON(t *testing.T) {
	events := []caamdb.Event{
		{
			Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			Type:        "activate",
			Provider:    "codex",
			ProfileName: "work",
			Duration:    5 * time.Minute,
			Details:     map[string]any{"source": "test"},
		},
	}

	var buf bytes.Buffer
	if err := renderEventsJSON(&buf, events); err != nil {
		t.Fatalf("renderEventsJSON() error = %v", err)
	}

	var result historyOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output should be valid JSON: %v", err)
	}

	if result.Count != 1 {
		t.Errorf("expected count=1, got %d", result.Count)
	}
	if result.Events[0].Provider != "codex" {
		t.Errorf("expected provider=codex, got %s", result.Events[0].Provider)
	}
	if result.Events[0].DurationSeconds != 300 {
		t.Errorf("expected duration=300, got %d", result.Events[0].DurationSeconds)
	}
}

func TestRenderEventList(t *testing.T) {
	events := []caamdb.Event{
		{
			Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			Type:        "activate",
			Provider:    "codex",
			ProfileName: "work",
		},
		{
			Timestamp:   time.Date(2025, 1, 15, 9, 0, 0, 0, time.UTC),
			Type:        "refresh",
			Provider:    "claude",
			ProfileName: "personal",
		},
	}

	var buf bytes.Buffer
	if err := renderEventList(&buf, events); err != nil {
		t.Fatalf("renderEventList() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "TIMESTAMP") {
		t.Error("output should contain header")
	}
	if !strings.Contains(output, "codex") || !strings.Contains(output, "work") {
		t.Error("output should contain codex/work")
	}
	if !strings.Contains(output, "claude") || !strings.Contains(output, "personal") {
		t.Error("output should contain claude/personal")
	}
	if !strings.Contains(output, "activate") || !strings.Contains(output, "refresh") {
		t.Error("output should contain event types")
	}
}
