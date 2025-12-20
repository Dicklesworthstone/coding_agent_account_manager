package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

// setupCooldownTestEnv sets up a test environment for cooldown tests.
func setupCooldownTestEnv(t *testing.T) (tmpDir string, cleanup func()) {
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

func TestCooldownSet_SetsInDatabase(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	cmd := &cobra.Command{}
	cmd.Flags().Int("minutes", 30, "")
	cmd.Flags().String("notes", "", "")
	_ = cmd.Flags().Set("minutes", "30")
	_ = cmd.Flags().Set("notes", "rate limit hit")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runCooldownSet(cmd, []string{"codex/testprofile"}); err != nil {
		t.Fatalf("runCooldownSet() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Recorded cooldown for codex/testprofile") {
		t.Errorf("output should contain success message, got: %s", output)
	}

	// Verify cooldown was set in DB
	now := time.Now().UTC()
	ev, err := db.ActiveCooldown("codex", "testprofile", now)
	if err != nil {
		t.Fatalf("ActiveCooldown() error = %v", err)
	}
	if ev == nil {
		t.Fatal("Expected cooldown to be set, got nil")
	}
	if ev.Provider != "codex" || ev.ProfileName != "testprofile" {
		t.Errorf("Cooldown profile mismatch: got %s/%s", ev.Provider, ev.ProfileName)
	}
	if ev.Notes != "rate limit hit" {
		t.Errorf("Notes mismatch: got %q, want %q", ev.Notes, "rate limit hit")
	}
}

func TestCooldownSet_DefaultMinutes(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	cmd := &cobra.Command{}
	cmd.Flags().Int("minutes", 0, "")
	cmd.Flags().String("notes", "", "")
	// Don't set minutes, use default

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runCooldownSet(cmd, []string{"codex/default_test"}); err != nil {
		t.Fatalf("runCooldownSet() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Recorded cooldown") {
		t.Errorf("output should contain success message, got: %s", output)
	}
}

func TestCooldownClear_ClearsSpecificProfile(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Set a cooldown first
	now := time.Now().UTC()
	_, err = db.SetCooldown("codex", "toClear", now, 60*time.Minute, "test")
	if err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().Bool("all", false, "")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runCooldownClear(cmd, []string{"codex/toClear"}); err != nil {
		t.Fatalf("runCooldownClear() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Cleared") && !strings.Contains(output, "cooldown") {
		t.Errorf("output should mention cleared cooldown, got: %s", output)
	}

	// Verify cooldown was cleared
	ev, err := db.ActiveCooldown("codex", "toClear", time.Now().UTC())
	if err != nil {
		t.Fatalf("ActiveCooldown() error = %v", err)
	}
	if ev != nil {
		t.Error("Expected cooldown to be cleared, but it still exists")
	}
}

func TestCooldownClear_ClearsAll(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Set multiple cooldowns
	now := time.Now().UTC()
	_, _ = db.SetCooldown("codex", "profile1", now, 60*time.Minute, "test1")
	_, _ = db.SetCooldown("codex", "profile2", now, 60*time.Minute, "test2")
	_, _ = db.SetCooldown("claude", "work", now, 60*time.Minute, "test3")

	cmd := &cobra.Command{}
	cmd.Flags().Bool("all", true, "")
	_ = cmd.Flags().Set("all", "true")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runCooldownClear(cmd, []string{}); err != nil {
		t.Fatalf("runCooldownClear(--all) error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Cleared") {
		t.Errorf("output should mention cleared cooldowns, got: %s", output)
	}

	// Verify all cooldowns were cleared
	events, err := db.ListActiveCooldowns(time.Now().UTC())
	if err != nil {
		t.Fatalf("ListActiveCooldowns() error = %v", err)
	}
	if len(events) != 0 {
		t.Errorf("Expected 0 active cooldowns after --all, got %d", len(events))
	}
}

func TestCooldownClear_AllWithArgs_ReturnsError(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	cmd := &cobra.Command{}
	cmd.Flags().Bool("all", true, "")
	_ = cmd.Flags().Set("all", "true")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runCooldownClear(cmd, []string{"codex/profile"})
	if err == nil {
		t.Fatal("runCooldownClear(--all with args) should return error")
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error should mention --all: %v", err)
	}
}

func TestCooldownClear_NoArgs_ReturnsError(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	cmd := &cobra.Command{}
	cmd.Flags().Bool("all", false, "")

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runCooldownClear(cmd, []string{})
	if err == nil {
		t.Fatal("runCooldownClear() with no args should return error")
	}
	if !strings.Contains(err.Error(), "provide") {
		t.Errorf("error should ask for profile: %v", err)
	}
}

func TestCooldownList_ShowsActiveCooldowns(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	// Set a cooldown
	now := time.Now().UTC()
	_, _ = db.SetCooldown("claude", "work", now, 60*time.Minute, "testing list")

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runCooldownList(cmd, []string{}); err != nil {
		t.Fatalf("runCooldownList() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "claude") || !strings.Contains(output, "work") {
		t.Errorf("output should list claude/work cooldown, got: %s", output)
	}
	if !strings.Contains(output, "testing list") {
		t.Errorf("output should contain notes, got: %s", output)
	}
}

func TestCooldownList_Empty(t *testing.T) {
	_, cleanup := setupCooldownTestEnv(t)
	defer cleanup()

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runCooldownList(cmd, []string{}); err != nil {
		t.Fatalf("runCooldownList() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No active cooldowns") {
		t.Errorf("output should indicate no cooldowns, got: %s", output)
	}
}

func TestResolveProviderProfile_ExplicitFormat(t *testing.T) {
	tests := []struct {
		input    string
		provider string
		profile  string
		wantErr  bool
	}{
		{"codex/work", "codex", "work", false},
		{"claude/personal", "claude", "personal", false},
		{"gemini/test", "gemini", "test", false},
		{"", "", "", true},
		{"/", "", "", true},
		{"codex/", "", "", true},
		{"/profile", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			provider, profile, err := resolveProviderProfile(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("resolveProviderProfile(%q) expected error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProviderProfile(%q) error = %v", tc.input, err)
			}
			if provider != tc.provider || profile != tc.profile {
				t.Errorf("resolveProviderProfile(%q) = (%q, %q), want (%q, %q)",
					tc.input, provider, profile, tc.provider, tc.profile)
			}
		})
	}
}

func TestFormatDurationShort(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{0, "0m"},
		{-1 * time.Minute, "0m"},
		{30 * time.Second, "1m"}, // rounds up to 1 minute
		{1 * time.Minute, "1m"},
		{30 * time.Minute, "30m"},
		{59 * time.Minute, "59m"},
		{60 * time.Minute, "1h"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
		{24 * time.Hour, "24h"},
		{25*time.Hour + 30*time.Minute, "25h30m"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatDurationShort(tc.duration)
			if got != tc.want {
				t.Errorf("formatDurationShort(%v) = %q, want %q", tc.duration, got, tc.want)
			}
		})
	}
}

func TestRenderCooldownList(t *testing.T) {
	now := time.Now().UTC()
	events := []caamdb.CooldownEvent{
		{
			Provider:      "codex",
			ProfileName:   "work",
			HitAt:         now.Add(-30 * time.Minute),
			CooldownUntil: now.Add(30 * time.Minute),
			Notes:         "rate limit",
		},
		{
			Provider:      "claude",
			ProfileName:   "personal",
			HitAt:         now.Add(-10 * time.Minute),
			CooldownUntil: now.Add(50 * time.Minute),
			Notes:         "testing",
		},
	}

	var buf bytes.Buffer
	if err := renderCooldownList(&buf, now, events); err != nil {
		t.Fatalf("renderCooldownList() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Active Cooldowns") {
		t.Error("output should contain header")
	}
	if !strings.Contains(output, "codex") || !strings.Contains(output, "work") {
		t.Error("output should contain codex/work")
	}
	if !strings.Contains(output, "claude") || !strings.Contains(output, "personal") {
		t.Error("output should contain claude/personal")
	}
	if !strings.Contains(output, "rate limit") {
		t.Error("output should contain notes")
	}
}

func TestConfirmProceed(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"N\n", false},
		{"no\n", false},
		{"\n", false},
		{"anything\n", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			r := strings.NewReader(tc.input)
			var buf bytes.Buffer
			result, err := confirmProceed(r, &buf)
			if err != nil {
				t.Fatalf("confirmProceed() error = %v", err)
			}
			if result != tc.expected {
				t.Errorf("confirmProceed(%q) = %v, want %v", tc.input, result, tc.expected)
			}
		})
	}
}
