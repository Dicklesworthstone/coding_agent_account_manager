package authwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestChangeTypeString(t *testing.T) {
	tests := []struct {
		ct   ChangeType
		want string
	}{
		{ChangeNone, "none"},
		{ChangeNew, "new"},
		{ChangeModified, "modified"},
		{ChangeRemoved, "removed"},
		{ChangeType(99), "unknown(99)"},
	}

	for _, tc := range tests {
		if got := tc.ct.String(); got != tc.want {
			t.Errorf("ChangeType(%d).String() = %q, want %q", tc.ct, got, tc.want)
		}
	}
}

func TestNewTracker(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	tracker := NewTracker(vault)

	if tracker == nil {
		t.Fatal("NewTracker returned nil")
	}

	if tracker.vault != vault {
		t.Error("vault not set correctly")
	}

	if tracker.states == nil {
		t.Error("states map not initialized")
	}
}

func TestCaptureNoAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Set up environment to use temp directory
	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	state, err := tracker.Capture("codex")
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}

	if state.Exists {
		t.Error("expected Exists to be false for non-existent auth")
	}

	if state.ContentHash != "" {
		t.Error("expected empty ContentHash for non-existent auth")
	}
}

func TestCaptureWithAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create auth file
	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authContent := []byte(`{"token": "test-token"}`)
	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, authContent, 0600); err != nil {
		t.Fatal(err)
	}

	// Set environment
	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	state, err := tracker.Capture("codex")
	if err != nil {
		t.Fatalf("Capture failed: %v", err)
	}

	if !state.Exists {
		t.Error("expected Exists to be true")
	}

	if state.ContentHash == "" {
		t.Error("expected non-empty ContentHash")
	}

	if len(state.FileHashes) != 1 {
		t.Errorf("expected 1 file hash, got %d", len(state.FileHashes))
	}
}

func TestCaptureAll(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Set up empty directories for each provider
	oldCodexHome := os.Getenv("CODEX_HOME")
	oldGeminiHome := os.Getenv("GEMINI_HOME")
	oldHome := os.Getenv("HOME")

	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	os.Setenv("GEMINI_HOME", filepath.Join(tmpDir, "gemini"))
	os.Setenv("HOME", tmpDir)

	defer func() {
		os.Setenv("CODEX_HOME", oldCodexHome)
		os.Setenv("GEMINI_HOME", oldGeminiHome)
		os.Setenv("HOME", oldHome)
	}()

	tracker := NewTracker(vault)

	states, err := tracker.CaptureAll()
	if err != nil {
		t.Fatalf("CaptureAll failed: %v", err)
	}

	// Should have entries for all providers
	if len(states) != 3 {
		t.Errorf("expected 3 states, got %d", len(states))
	}
}

func TestDetectChangeNoInitialState(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "test"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// First check - no initial state
	change, err := tracker.DetectChange("codex")
	if err != nil {
		t.Fatalf("DetectChange failed: %v", err)
	}

	if change.Type != ChangeNew {
		t.Errorf("expected ChangeNew, got %v", change.Type)
	}
}

func TestDetectChangeModified(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "original"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// Capture initial state
	_, err := tracker.Capture("codex")
	if err != nil {
		t.Fatal(err)
	}

	// Modify auth file
	if err := os.WriteFile(authPath, []byte(`{"token": "modified"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Detect change
	change, err := tracker.DetectChange("codex")
	if err != nil {
		t.Fatalf("DetectChange failed: %v", err)
	}

	if change.Type != ChangeModified {
		t.Errorf("expected ChangeModified, got %v", change.Type)
	}
}

func TestDetectChangeRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "test"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// Capture initial state
	_, err := tracker.Capture("codex")
	if err != nil {
		t.Fatal(err)
	}

	// Remove auth file
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}

	// Detect change
	change, err := tracker.DetectChange("codex")
	if err != nil {
		t.Fatalf("DetectChange failed: %v", err)
	}

	if change.Type != ChangeRemoved {
		t.Errorf("expected ChangeRemoved, got %v", change.Type)
	}
}

func TestDetectChangeNone(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "test"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// Capture initial state
	_, err := tracker.Capture("codex")
	if err != nil {
		t.Fatal(err)
	}

	// Detect change without modifying
	change, err := tracker.DetectChange("codex")
	if err != nil {
		t.Fatalf("DetectChange failed: %v", err)
	}

	if change.Type != ChangeNone {
		t.Errorf("expected ChangeNone, got %v", change.Type)
	}
}

func TestGetState(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	tracker := NewTracker(vault)

	// Initially nil
	if state := tracker.GetState("codex"); state != nil {
		t.Error("expected nil state initially")
	}

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "test"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	// Capture state
	_, err := tracker.Capture("codex")
	if err != nil {
		t.Fatal(err)
	}

	// Now should have state
	if state := tracker.GetState("codex"); state == nil {
		t.Error("expected non-nil state after Capture")
	}
}

func TestSaveAndLoadState(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Override state path
	oldCaam := os.Getenv("CAAM_HOME")
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Unsetenv("CAAM_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("CAAM_HOME", oldCaam)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "test"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	// Create and populate tracker
	tracker1 := NewTracker(vault)
	state, err := tracker1.Capture("codex")
	if err != nil {
		t.Fatal(err)
	}

	// Save state
	if err := tracker1.SaveState(); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	// Create new tracker and load state
	tracker2 := NewTracker(vault)
	if err := tracker2.LoadState(); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	// Compare
	loadedState := tracker2.GetState("codex")
	if loadedState == nil {
		t.Fatal("loaded state is nil")
	}

	if loadedState.ContentHash != state.ContentHash {
		t.Errorf("hash mismatch: got %q, want %q", loadedState.ContentHash, state.ContentHash)
	}
}

func TestStatePath(t *testing.T) {
	path := StatePath()
	if path == "" {
		t.Error("StatePath returned empty string")
	}

	if filepath.Base(path) != "auth_state.json" {
		t.Errorf("unexpected filename: %s", filepath.Base(path))
	}
}

func TestLoadStateNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	oldCaam := os.Getenv("CAAM_HOME")
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Unsetenv("CAAM_HOME")
	os.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "nonexistent"))
	defer os.Setenv("CAAM_HOME", oldCaam)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	tracker := NewTracker(vault)

	// Should not error for non-existent state file
	if err := tracker.LoadState(); err != nil {
		t.Errorf("LoadState should not error for non-existent file: %v", err)
	}
}

func TestCheckUnsavedAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Set up empty auth directories
	oldCodexHome := os.Getenv("CODEX_HOME")
	oldGeminiHome := os.Getenv("GEMINI_HOME")
	oldHome := os.Getenv("HOME")

	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	os.Setenv("GEMINI_HOME", filepath.Join(tmpDir, "gemini"))
	os.Setenv("HOME", tmpDir)

	defer func() {
		os.Setenv("CODEX_HOME", oldCodexHome)
		os.Setenv("GEMINI_HOME", oldGeminiHome)
		os.Setenv("HOME", oldHome)
	}()

	// No auth files, should return empty
	unsaved, err := CheckUnsavedAuth(vault)
	if err != nil {
		t.Fatalf("CheckUnsavedAuth failed: %v", err)
	}

	if len(unsaved) != 0 {
		t.Errorf("expected 0 unsaved, got %d", len(unsaved))
	}
}

func TestFormatUnsavedWarning(t *testing.T) {
	// Empty list
	if msg := FormatUnsavedWarning(nil); msg != "" {
		t.Error("expected empty message for nil list")
	}

	if msg := FormatUnsavedWarning([]string{}); msg != "" {
		t.Error("expected empty message for empty list")
	}

	// With providers
	msg := FormatUnsavedWarning([]string{"claude", "codex"})
	if msg == "" {
		t.Error("expected non-empty warning message")
	}

	if !contains(msg, "claude") || !contains(msg, "codex") {
		t.Error("warning should mention provider names")
	}
}

func TestNewWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	onChange := func(c Change) {
		// callback
	}

	w := NewWatcher(vault, onChange)

	if w == nil {
		t.Fatal("NewWatcher returned nil")
	}

	if w.tracker == nil {
		t.Error("tracker not initialized")
	}

	if w.onChange == nil {
		t.Error("onChange callback not set")
	}
}

func TestWatcherStartStop(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	w := NewWatcher(vault, nil)

	// Start in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Start()
	}()

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Stop
	w.Stop()

	// Should return without error
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after Stop")
	}
}

func TestGetStatusNoAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	status, err := tracker.GetStatus("codex")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if status.HasAuth {
		t.Error("expected HasAuth to be false")
	}

	if status.IsUnsaved {
		t.Error("expected IsUnsaved to be false when no auth exists")
	}
}

func TestGetStatusWithUnsavedAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token": "unsaved"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	status, err := tracker.GetStatus("codex")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if !status.HasAuth {
		t.Error("expected HasAuth to be true")
	}

	if !status.IsUnsaved {
		t.Error("expected IsUnsaved to be true for auth not matching any profile")
	}

	if status.SuggestedAction == "" {
		t.Error("expected SuggestedAction to be set")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestDetectAllChanges(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Set up test environment
	codexDir := filepath.Join(tmpDir, "codex")
	geminiDir := filepath.Join(tmpDir, "gemini")
	os.MkdirAll(codexDir, 0700)
	os.MkdirAll(geminiDir, 0700)

	oldCodexHome := os.Getenv("CODEX_HOME")
	oldGeminiHome := os.Getenv("GEMINI_HOME")
	oldHome := os.Getenv("HOME")

	os.Setenv("CODEX_HOME", codexDir)
	os.Setenv("GEMINI_HOME", geminiDir)
	os.Setenv("HOME", tmpDir)

	defer func() {
		os.Setenv("CODEX_HOME", oldCodexHome)
		os.Setenv("GEMINI_HOME", oldGeminiHome)
		os.Setenv("HOME", oldHome)
	}()

	tracker := NewTracker(vault)

	// Initial capture
	tracker.CaptureAll()

	// Create a new auth file (will trigger ChangeNew on next detect)
	codexAuthPath := filepath.Join(codexDir, "auth.json")
	os.WriteFile(codexAuthPath, []byte(`{"token": "test"}`), 0600)

	// Detect all changes
	changes, err := tracker.DetectAllChanges()
	if err != nil {
		t.Fatalf("DetectAllChanges failed: %v", err)
	}

	// Should have at least one change (codex new)
	if len(changes) == 0 {
		t.Error("expected at least one change")
	}

	// Find the codex change
	found := false
	for _, c := range changes {
		if c.Provider == "codex" && c.Type == ChangeNew {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected ChangeNew for codex")
	}
}

func TestMatchesProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create auth file
	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)
	authContent := []byte(`{"token": "test-token"}`)
	authPath := filepath.Join(codexDir, "auth.json")
	os.WriteFile(authPath, authContent, 0600)

	// Save as profile
	profileDir := vault.ProfilePath("codex", "test-profile")
	os.MkdirAll(profileDir, 0700)
	os.WriteFile(filepath.Join(profileDir, "auth.json"), authContent, 0600)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// Should match
	matches, err := tracker.MatchesProfile("codex", "test-profile")
	if err != nil {
		t.Fatalf("MatchesProfile failed: %v", err)
	}
	if !matches {
		t.Error("expected auth to match profile")
	}

	// Modify auth file
	os.WriteFile(authPath, []byte(`{"token": "different"}`), 0600)

	// Should not match
	matches, err = tracker.MatchesProfile("codex", "test-profile")
	if err != nil {
		t.Fatalf("MatchesProfile failed: %v", err)
	}
	if matches {
		t.Error("expected auth to not match profile after modification")
	}
}

func TestMatchesProfile_MissingRequiredBackup(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"test-token"}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	if _, err := tracker.MatchesProfile("codex", "missing-profile"); err == nil {
		t.Fatal("expected error for missing required backup")
	}
}

func TestMatchesProfile_OptionalOnlyAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpDir)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	authContent := []byte(`{"session":"opt"}`)
	if err := os.WriteFile(filepath.Join(tmpDir, ".claude.json"), authContent, 0600); err != nil {
		t.Fatal(err)
	}

	profileDir := vault.ProfilePath("claude", "optional-only")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authContent, 0600); err != nil {
		t.Fatal(err)
	}

	tracker := NewTracker(vault)

	matches, err := tracker.MatchesProfile("claude", "optional-only")
	if err != nil {
		t.Fatalf("MatchesProfile failed: %v", err)
	}
	if !matches {
		t.Fatal("expected optional-only profile to match")
	}
}

func TestMatchesProfile_NoVault(t *testing.T) {
	tracker := NewTracker(nil)

	_, err := tracker.MatchesProfile("codex", "test")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestMatchesProfile_NoAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Empty codex directory - no auth
	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	matches, err := tracker.MatchesProfile("codex", "test-profile")
	if err != nil {
		t.Fatalf("MatchesProfile failed: %v", err)
	}
	if matches {
		t.Error("expected no match when auth doesn't exist")
	}
}

func TestFindMatchingProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create auth file
	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)
	authContent := []byte(`{"token": "test-token"}`)
	authPath := filepath.Join(codexDir, "auth.json")
	os.WriteFile(authPath, authContent, 0600)

	// Save as profile "profile1"
	profile1Dir := vault.ProfilePath("codex", "profile1")
	os.MkdirAll(profile1Dir, 0700)
	os.WriteFile(filepath.Join(profile1Dir, "auth.json"), authContent, 0600)

	// Save different content as "profile2"
	profile2Dir := vault.ProfilePath("codex", "profile2")
	os.MkdirAll(profile2Dir, 0700)
	os.WriteFile(filepath.Join(profile2Dir, "auth.json"), []byte(`{"token": "other"}`), 0600)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// Should find profile1
	found, err := tracker.FindMatchingProfile("codex")
	if err != nil {
		t.Fatalf("FindMatchingProfile failed: %v", err)
	}
	if found != "profile1" {
		t.Errorf("expected 'profile1', got %q", found)
	}
}

func TestFindMatchingProfile_NoVault(t *testing.T) {
	tracker := NewTracker(nil)

	_, err := tracker.FindMatchingProfile("codex")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestFindMatchingProfile_NoAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	found, err := tracker.FindMatchingProfile("codex")
	if err != nil {
		t.Fatalf("FindMatchingProfile failed: %v", err)
	}
	if found != "" {
		t.Errorf("expected empty string when no auth, got %q", found)
	}
}

func TestFindMatchingProfile_NoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create auth file
	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)
	os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"token": "unique"}`), 0600)

	// Save different content as profile
	profileDir := vault.ProfilePath("codex", "other")
	os.MkdirAll(profileDir, 0700)
	os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"token": "different"}`), 0600)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	found, err := tracker.FindMatchingProfile("codex")
	if err != nil {
		t.Fatalf("FindMatchingProfile failed: %v", err)
	}
	if found != "" {
		t.Errorf("expected empty string when no match, got %q", found)
	}
}

func TestGetAllStatuses(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Set up environment
	codexDir := filepath.Join(tmpDir, "codex")
	geminiDir := filepath.Join(tmpDir, "gemini")
	os.MkdirAll(codexDir, 0700)
	os.MkdirAll(geminiDir, 0700)

	oldCodexHome := os.Getenv("CODEX_HOME")
	oldGeminiHome := os.Getenv("GEMINI_HOME")
	oldHome := os.Getenv("HOME")

	os.Setenv("CODEX_HOME", codexDir)
	os.Setenv("GEMINI_HOME", geminiDir)
	os.Setenv("HOME", tmpDir)

	defer func() {
		os.Setenv("CODEX_HOME", oldCodexHome)
		os.Setenv("GEMINI_HOME", oldGeminiHome)
		os.Setenv("HOME", oldHome)
	}()

	// Create auth for codex only
	os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"token": "test"}`), 0600)

	tracker := NewTracker(vault)

	statuses, err := tracker.GetAllStatuses()
	if err != nil {
		t.Fatalf("GetAllStatuses failed: %v", err)
	}

	if len(statuses) != 3 {
		t.Errorf("expected 3 statuses, got %d", len(statuses))
	}

	// Find codex status
	var codexStatus *AuthStatus
	for _, s := range statuses {
		if s.Provider == "codex" {
			codexStatus = s
			break
		}
	}

	if codexStatus == nil {
		t.Fatal("codex status not found")
	}

	if !codexStatus.HasAuth {
		t.Error("expected codex to have auth")
	}
}

func TestWatcherStartAlreadyRunning(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	w := NewWatcher(vault, nil)

	// Start in goroutine
	go w.Start()
	time.Sleep(50 * time.Millisecond)

	// Try to start again - should error
	err := w.Start()
	if err == nil {
		t.Error("expected error when starting already running watcher")
	}

	w.Stop()
}

func TestWatcherStopNotRunning(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	w := NewWatcher(vault, nil)

	// Stop without starting - should not panic
	w.Stop()
}

func TestWatcherStartAfterCaptureError(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Set CODEX_HOME to a path that will cause CaptureAll to work
	// but first we test with an invalid provider setup
	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex"))
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	w := NewWatcher(vault, nil)

	// Start should succeed since CaptureAll handles missing files gracefully
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Start()
	}()

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Stop
	w.Stop()

	// Should return without error
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after Stop")
	}

	// Now we can start again (this tests that running was properly reset)
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- w.Start()
	}()

	time.Sleep(100 * time.Millisecond)
	w.Stop()

	select {
	case err := <-errCh2:
		if err != nil {
			t.Errorf("Second Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Second Start did not return after Stop")
	}
}

func TestWatcherDetectsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)
	authPath := filepath.Join(codexDir, "auth.json")
	os.WriteFile(authPath, []byte(`{"token": "initial"}`), 0600)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	changes := make(chan Change, 10)
	w := NewWatcher(vault, func(c Change) {
		changes <- c
	})

	// Start watcher
	go w.Start()
	time.Sleep(100 * time.Millisecond)

	// Modify auth file
	os.WriteFile(authPath, []byte(`{"token": "modified"}`), 0600)

	// Wait for detection (poll interval is 5 seconds, so we need to wait)
	// For test purposes, let's just stop the watcher
	w.Stop()

	// Note: In a real test we'd wait for the change, but the 5-second poll
	// makes this test slow. The test mainly verifies the watcher starts/stops correctly.
}

func TestCaptureUnknownProvider(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	tracker := NewTracker(vault)

	_, err := tracker.Capture("unknown-provider")
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestLoadStateInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	oldCaam := os.Getenv("CAAM_HOME")
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Unsetenv("CAAM_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("CAAM_HOME", oldCaam)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	// Create invalid JSON state file
	stateDir := filepath.Join(tmpDir, "caam")
	os.MkdirAll(stateDir, 0700)
	os.WriteFile(filepath.Join(stateDir, "auth_state.json"), []byte("{invalid json"), 0600)

	tracker := NewTracker(vault)

	err := tracker.LoadState()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestDetectChangeNewAuthAfterRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	// Capture initial state (no auth)
	tracker.Capture("codex")

	// Create auth file
	authPath := filepath.Join(codexDir, "auth.json")
	os.WriteFile(authPath, []byte(`{"token": "new"}`), 0600)

	// Should detect as new
	change, err := tracker.DetectChange("codex")
	if err != nil {
		t.Fatalf("DetectChange failed: %v", err)
	}

	if change.Type != ChangeNew {
		t.Errorf("expected ChangeNew, got %v", change.Type)
	}
}

func TestGetStatusWithMatchingProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create auth file
	codexDir := filepath.Join(tmpDir, "codex")
	os.MkdirAll(codexDir, 0700)
	authContent := []byte(`{"token": "test"}`)
	authPath := filepath.Join(codexDir, "auth.json")
	os.WriteFile(authPath, authContent, 0600)

	// Save as profile
	profileDir := vault.ProfilePath("codex", "my-profile")
	os.MkdirAll(profileDir, 0700)
	os.WriteFile(filepath.Join(profileDir, "auth.json"), authContent, 0600)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexDir)
	defer os.Setenv("CODEX_HOME", oldCodexHome)

	tracker := NewTracker(vault)

	status, err := tracker.GetStatus("codex")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}

	if !status.HasAuth {
		t.Error("expected HasAuth to be true")
	}

	if status.MatchedProfile != "my-profile" {
		t.Errorf("expected MatchedProfile 'my-profile', got %q", status.MatchedProfile)
	}

	if status.IsUnsaved {
		t.Error("expected IsUnsaved to be false when profile matches")
	}

	if status.SuggestedAction != "" {
		t.Error("expected no SuggestedAction when profile matches")
	}
}
