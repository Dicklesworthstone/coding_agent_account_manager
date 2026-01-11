package warnings

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelInfo, "info"},
		{LevelWarning, "warning"},
		{LevelCritical, "critical"},
		{Level(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.level.String(); got != tt.want {
				t.Errorf("Level.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "less than a minute"},
		{1 * time.Minute, "1 minute"},
		{45 * time.Minute, "45 minutes"},
		{1 * time.Hour, "1 hour"},
		{2 * time.Hour, "2 hours"},
		{23 * time.Hour, "23 hours"},
		{24 * time.Hour, "1 day"},
		{48 * time.Hour, "2 days"},
		{7 * 24 * time.Hour, "7 days"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatDuration(tt.d); got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestPrint(t *testing.T) {
	tests := []struct {
		name     string
		warnings []Warning
		noColor  bool
		wantPrinted bool
	}{
		{
			name:        "empty warnings",
			warnings:    []Warning{},
			noColor:     true,
			wantPrinted: false,
		},
		{
			name: "single warning",
			warnings: []Warning{
				{
					Level:   LevelWarning,
					Tool:    "claude",
					Profile: "work",
					Message: "Token expires in 2 hours",
					Action:  "caam refresh claude work",
				},
			},
			noColor:     true,
			wantPrinted: true,
		},
		{
			name: "critical warning",
			warnings: []Warning{
				{
					Level:   LevelCritical,
					Tool:    "codex",
					Profile: "personal",
					Message: "Token EXPIRED",
					Action:  "caam login codex personal",
				},
			},
			noColor:     true,
			wantPrinted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := Print(&buf, tt.warnings, tt.noColor)
			if got != tt.wantPrinted {
				t.Errorf("Print() = %v, want %v", got, tt.wantPrinted)
			}

			if tt.wantPrinted && buf.Len() == 0 {
				t.Error("Print() returned true but wrote nothing")
			}

			if !tt.wantPrinted && buf.Len() > 0 {
				t.Errorf("Print() returned false but wrote: %s", buf.String())
			}
		})
	}
}

func TestPrintOutput(t *testing.T) {
	warnings := []Warning{
		{
			Level:   LevelWarning,
			Tool:    "claude",
			Profile: "work",
			Message: "Token expires in 2 hours",
			Action:  "caam refresh claude work",
		},
	}

	var buf bytes.Buffer
	Print(&buf, warnings, true)

	output := buf.String()
	if output == "" {
		t.Fatal("Print() wrote nothing")
	}

	// Check key parts are present
	checks := []string{
		"claude/work",
		"Token expires in 2 hours",
		"caam refresh claude work",
	}

	for _, check := range checks {
		if !bytes.Contains(buf.Bytes(), []byte(check)) {
			t.Errorf("Print() output missing %q\nGot: %s", check, output)
		}
	}
}

func TestPrintWithColor(t *testing.T) {
	warnings := []Warning{
		{
			Level:   LevelCritical,
			Tool:    "codex",
			Profile: "test",
			Message: "Token EXPIRED",
		},
	}

	var buf bytes.Buffer
	Print(&buf, warnings, false) // With color

	// Should contain ANSI color codes
	if !bytes.Contains(buf.Bytes(), []byte("\033[31m")) {
		t.Error("Print() with color should contain red ANSI code for critical")
	}
	if !bytes.Contains(buf.Bytes(), []byte("\033[0m")) {
		t.Error("Print() with color should contain reset ANSI code")
	}

	// But without color should not
	buf.Reset()
	Print(&buf, warnings, true) // No color

	if bytes.Contains(buf.Bytes(), []byte("\033[")) {
		t.Error("Print() without color should not contain ANSI codes")
	}
}

func TestFilter(t *testing.T) {
	warnings := []Warning{
		{Level: LevelInfo, Message: "info1"},
		{Level: LevelWarning, Message: "warn1"},
		{Level: LevelCritical, Message: "crit1"},
		{Level: LevelInfo, Message: "info2"},
		{Level: LevelWarning, Message: "warn2"},
	}

	tests := []struct {
		minLevel Level
		wantLen  int
	}{
		{LevelInfo, 5},
		{LevelWarning, 3},
		{LevelCritical, 1},
	}

	for _, tt := range tests {
		t.Run(tt.minLevel.String(), func(t *testing.T) {
			got := Filter(warnings, tt.minLevel)
			if len(got) != tt.wantLen {
				t.Errorf("Filter() returned %d warnings, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestWarningStruct(t *testing.T) {
	w := Warning{
		Level:   LevelWarning,
		Tool:    "claude",
		Profile: "work",
		Message: "Token expires soon",
		Action:  "caam refresh claude work",
	}

	if w.Level != LevelWarning {
		t.Errorf("Level = %v, want %v", w.Level, LevelWarning)
	}
	if w.Tool != "claude" {
		t.Errorf("Tool = %q, want %q", w.Tool, "claude")
	}
	if w.Profile != "work" {
		t.Errorf("Profile = %q, want %q", w.Profile, "work")
	}
	if w.Message != "Token expires soon" {
		t.Errorf("Message = %q, want %q", w.Message, "Token expires soon")
	}
	if w.Action != "caam refresh claude work" {
		t.Errorf("Action = %q, want %q", w.Action, "caam refresh claude work")
	}
}

func TestNewChecker(t *testing.T) {
	checker := NewChecker(nil, nil, nil)
	if checker == nil {
		t.Fatal("NewChecker() returned nil")
	}

	if checker.CriticalThreshold != 1*time.Hour {
		t.Errorf("CriticalThreshold = %v, want %v", checker.CriticalThreshold, 1*time.Hour)
	}

	if checker.WarningThreshold != 24*time.Hour {
		t.Errorf("WarningThreshold = %v, want %v", checker.WarningThreshold, 24*time.Hour)
	}
}

func TestPrintInfoLevel(t *testing.T) {
	warnings := []Warning{
		{
			Level:   LevelInfo,
			Tool:    "gemini",
			Profile: "test",
			Message: "Info message",
		},
	}

	var buf bytes.Buffer
	Print(&buf, warnings, false) // With color

	// Should contain cyan ANSI code for info
	if !bytes.Contains(buf.Bytes(), []byte("\033[36m")) {
		t.Error("Print() with color should contain cyan ANSI code for info level")
	}
}

func TestPrintWarningLevel(t *testing.T) {
	warnings := []Warning{
		{
			Level:   LevelWarning,
			Tool:    "claude",
			Profile: "test",
			Message: "Warning message",
		},
	}

	var buf bytes.Buffer
	Print(&buf, warnings, false) // With color

	// Should contain yellow ANSI code for warning
	if !bytes.Contains(buf.Bytes(), []byte("\033[33m")) {
		t.Error("Print() with color should contain yellow ANSI code for warning level")
	}
}

func TestPrintNoAction(t *testing.T) {
	warnings := []Warning{
		{
			Level:   LevelWarning,
			Tool:    "claude",
			Profile: "test",
			Message: "Warning without action",
			Action:  "", // No action
		},
	}

	var buf bytes.Buffer
	Print(&buf, warnings, true)

	// Should not contain "Run:" line since no action
	if bytes.Contains(buf.Bytes(), []byte("Run:")) {
		t.Error("Print() should not show Run: line when Action is empty")
	}
}

func TestFilterEmptyWarnings(t *testing.T) {
	result := Filter(nil, LevelWarning)
	if len(result) != 0 {
		t.Errorf("Filter(nil) should return empty slice, got %d", len(result))
	}

	result = Filter([]Warning{}, LevelWarning)
	if len(result) != 0 {
		t.Errorf("Filter([]) should return empty slice, got %d", len(result))
	}
}

func TestCheckerCheckAllEmptyVault(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckAll(context.Background())

	if len(warnings) != 0 {
		t.Errorf("CheckAll() on empty vault should return no warnings, got %d", len(warnings))
	}
}

func TestCheckerCheckAllWithProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a claude profile directory with an expiring token
	profilePath := vault.ProfilePath("claude", "test")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Create a credentials file with an expiring token (expiresAt is Unix milliseconds)
	expiresAt := time.Now().Add(30 * time.Minute).UnixMilli() // Expires in 30 minutes (within critical threshold)
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken": "fake-token",
			"expiresAt":   expiresAt,
		},
	}
	credsData, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(profilePath, ".credentials.json"), credsData, 0600); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckAll(context.Background())

	// Should find the expiring token
	if len(warnings) == 0 {
		t.Error("CheckAll() should find warnings for expiring token")
	}

	// Should be critical since 30min < 1 hour threshold
	foundCritical := false
	for _, w := range warnings {
		if w.Level == LevelCritical && w.Tool == "claude" && w.Profile == "test" {
			foundCritical = true
			break
		}
	}
	if !foundCritical {
		t.Error("CheckAll() should return critical warning for token expiring within 1 hour")
	}
}

func TestCheckerCheckAllExpiredToken(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a claude profile with an already expired token
	profilePath := vault.ProfilePath("claude", "expired")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Create a credentials file with an expired token (Unix milliseconds)
	expiresAt := time.Now().Add(-1 * time.Hour).UnixMilli() // Expired 1 hour ago
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken": "fake-token",
			"expiresAt":   expiresAt,
		},
	}
	credsData, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(profilePath, ".credentials.json"), credsData, 0600); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckAll(context.Background())

	// Should find the expired token
	foundExpired := false
	for _, w := range warnings {
		if w.Level == LevelCritical && w.Message == "Token EXPIRED" {
			foundExpired = true
			break
		}
	}
	if !foundExpired {
		t.Error("CheckAll() should return 'Token EXPIRED' for expired token")
	}
}

func TestCheckerCheckAllWarningThreshold(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a claude profile with a token expiring within warning threshold (24h)
	profilePath := vault.ProfilePath("claude", "warning")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Token expires in 12 hours (within 24h warning threshold, but not critical)
	expiresAt := time.Now().Add(12 * time.Hour).UnixMilli()
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken": "fake-token",
			"expiresAt":   expiresAt,
		},
	}
	credsData, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(profilePath, ".credentials.json"), credsData, 0600); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckAll(context.Background())

	// Should find the warning-level alert
	foundWarning := false
	for _, w := range warnings {
		if w.Level == LevelWarning && w.Tool == "claude" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Error("CheckAll() should return warning for token expiring within 24 hours")
	}
}

func TestCheckerCheckAllNoWarningForHealthyToken(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a claude profile with a token that expires far in the future
	profilePath := vault.ProfilePath("claude", "healthy")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Token expires in 7 days (well beyond 24h warning threshold)
	expiresAt := time.Now().Add(7 * 24 * time.Hour).UnixMilli()
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken": "fake-token",
			"expiresAt":   expiresAt,
		},
	}
	credsData, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(profilePath, ".credentials.json"), credsData, 0600); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckAll(context.Background())

	// Should not find any warnings for healthy token
	for _, w := range warnings {
		if w.Tool == "claude" && w.Profile == "healthy" {
			t.Error("CheckAll() should not return warnings for healthy tokens")
		}
	}
}

func TestCheckerCheckAllCodexProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a codex profile with expiring token
	profilePath := vault.ProfilePath("codex", "test")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	// Token expires in 30 minutes (expires_at is Unix seconds for codex)
	expiresAt := time.Now().Add(30 * time.Minute).Unix()
	auth := map[string]interface{}{
		"access_token": "fake-token",
		"expires_at":   expiresAt,
	}
	authData, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(profilePath, "auth.json"), authData, 0600); err != nil {
		t.Fatalf("failed to write auth: %v", err)
	}

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckAll(context.Background())

	// Should find the expiring codex token
	foundCodex := false
	for _, w := range warnings {
		if w.Tool == "codex" && w.Profile == "test" {
			foundCodex = true
			break
		}
	}
	if !foundCodex {
		t.Error("CheckAll() should find warnings for expiring codex token")
	}
}

func TestCheckerCheckActiveEmptyVault(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	checker := NewChecker(vault, nil, nil)
	warnings := checker.CheckActive(context.Background())

	// Should return no warnings when no profiles are active
	if len(warnings) != 0 {
		t.Errorf("CheckActive() on empty vault should return no warnings, got %d", len(warnings))
	}
}

func TestCheckerCustomThresholds(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a profile with token expiring in 2 hours
	profilePath := vault.ProfilePath("claude", "custom")
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("failed to create profile dir: %v", err)
	}

	expiresAt := time.Now().Add(2 * time.Hour).UnixMilli()
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken": "fake-token",
			"expiresAt":   expiresAt,
		},
	}
	credsData, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(profilePath, ".credentials.json"), credsData, 0600); err != nil {
		t.Fatalf("failed to write credentials: %v", err)
	}

	// Set custom thresholds
	checker := NewChecker(vault, nil, nil)
	checker.CriticalThreshold = 3 * time.Hour // 2 hours < 3 hours, so should be critical

	warnings := checker.CheckAll(context.Background())

	// Should be critical with custom threshold
	foundCritical := false
	for _, w := range warnings {
		if w.Level == LevelCritical && w.Tool == "claude" {
			foundCritical = true
			break
		}
	}
	if !foundCritical {
		t.Error("CheckAll() with custom threshold should return critical warning")
	}
}
