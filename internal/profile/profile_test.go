// Package profile contains tests for profile management.
package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// =============================================================================
// Profile Methods Tests
// =============================================================================

func TestProfilePaths(t *testing.T) {
	prof := &Profile{
		Name:     "test-profile",
		Provider: "claude",
		BasePath: "/tmp/test-profiles/claude/test-profile",
	}

	tests := []struct {
		name     string
		method   func() string
		expected string
	}{
		{"HomePath", prof.HomePath, "/tmp/test-profiles/claude/test-profile/home"},
		{"XDGConfigPath", prof.XDGConfigPath, "/tmp/test-profiles/claude/test-profile/xdg_config"},
		{"CodexHomePath", prof.CodexHomePath, "/tmp/test-profiles/claude/test-profile/codex_home"},
		{"LockPath", prof.LockPath, "/tmp/test-profiles/claude/test-profile/.lock"},
		{"MetaPath", prof.MetaPath, "/tmp/test-profiles/claude/test-profile/profile.json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.method()
			if got != tc.expected {
				t.Errorf("%s() = %q, want %q", tc.name, got, tc.expected)
			}
		})
	}
}

func TestHasBrowserConfig(t *testing.T) {
	tests := []struct {
		name           string
		browserCommand string
		browserProfile string
		expected       bool
	}{
		{"no config", "", "", false},
		{"command only", "chrome", "", true},
		{"profile only", "", "Profile 1", true},
		{"both set", "chrome", "Profile 1", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prof := &Profile{
				BrowserCommand:    tc.browserCommand,
				BrowserProfileDir: tc.browserProfile,
			}
			if got := prof.HasBrowserConfig(); got != tc.expected {
				t.Errorf("HasBrowserConfig() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestBrowserDisplayName(t *testing.T) {
	tests := []struct {
		name           string
		profileName    string
		browserCmd     string
		browserProfile string
		expected       string
	}{
		{"custom name set", "My Chrome", "chrome", "Profile 1", "My Chrome"},
		{"profile only", "", "chrome", "Profile 1", "chrome (Profile 1)"},
		{"command only", "", "chrome", "", "chrome"},
		{"profile dir only", "", "", "Profile 1", "Profile 1"},
		{"nothing set", "", "", "", "system default"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prof := &Profile{
				BrowserProfileName: tc.profileName,
				BrowserCommand:     tc.browserCmd,
				BrowserProfileDir:  tc.browserProfile,
			}
			if got := prof.BrowserDisplayName(); got != tc.expected {
				t.Errorf("BrowserDisplayName() = %q, want %q", got, tc.expected)
			}
		})
	}
}

// =============================================================================
// Lock Management Tests
// =============================================================================

func TestLockUnlock(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// Initially not locked
	if prof.IsLocked() {
		t.Error("expected profile to be unlocked initially")
	}

	// Lock
	if err := prof.Lock(); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	// Now locked
	if !prof.IsLocked() {
		t.Error("expected profile to be locked after Lock()")
	}

	// Lock again should fail
	if err := prof.Lock(); err == nil {
		t.Error("expected Lock() to fail when already locked")
	}

	// Unlock
	if err := prof.Unlock(); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}

	// Now unlocked
	if prof.IsLocked() {
		t.Error("expected profile to be unlocked after Unlock()")
	}

	// Unlock again should be safe (idempotent)
	if err := prof.Unlock(); err != nil {
		t.Errorf("Unlock() on unlocked profile error = %v", err)
	}
}

func TestLockAtomicity(t *testing.T) {
	// Test that Lock() uses atomic file creation (O_EXCL)
	tmpDir := t.TempDir()
	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// Create a pre-existing lock file manually
	lockPath := filepath.Join(tmpDir, ".lock")
	if err := os.WriteFile(lockPath, []byte("existing"), 0600); err != nil {
		t.Fatalf("failed to create pre-existing lock: %v", err)
	}

	// Lock should fail because file exists
	if err := prof.Lock(); err == nil {
		t.Error("expected Lock() to fail when lock file already exists")
	}
}

func TestGetLockInfo(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// No lock file - should return nil, nil
	info, err := prof.GetLockInfo()
	if err != nil {
		t.Fatalf("GetLockInfo() error = %v", err)
	}
	if info != nil {
		t.Error("expected nil lock info when no lock exists")
	}

	// Create lock
	if err := prof.Lock(); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	// Get lock info
	info, err = prof.GetLockInfo()
	if err != nil {
		t.Fatalf("GetLockInfo() error = %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil lock info")
	}

	if info.PID != os.Getpid() {
		t.Errorf("LockInfo.PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.LockedAt.IsZero() {
		t.Error("expected non-zero LockedAt")
	}

	// Clean up
	prof.Unlock()
}

func TestIsLockStale(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// No lock - not stale (no lock to be stale)
	stale, err := prof.IsLockStale()
	if err != nil {
		t.Fatalf("IsLockStale() error = %v", err)
	}
	if stale {
		t.Error("expected not stale when no lock exists")
	}

	// Create lock with current PID (not stale)
	if err := prof.Lock(); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}

	stale, err = prof.IsLockStale()
	if err != nil {
		t.Fatalf("IsLockStale() error = %v", err)
	}
	if stale {
		t.Error("expected not stale when lock is from current process")
	}

	prof.Unlock()

	// Create lock with fake dead PID
	lockPath := filepath.Join(tmpDir, ".lock")
	fakePID := 99999999 // Unlikely to be a real process
	content := `{"pid": 99999999, "locked_at": "2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(lockPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write fake lock: %v", err)
	}

	stale, err = prof.IsLockStale()
	if err != nil {
		t.Fatalf("IsLockStale() error = %v (pid=%d)", err, fakePID)
	}
	if !stale {
		t.Error("expected stale when lock is from dead process")
	}
}

func TestCleanStaleLock(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// No lock - should return false, nil
	cleaned, err := prof.CleanStaleLock()
	if err != nil {
		t.Fatalf("CleanStaleLock() error = %v", err)
	}
	if cleaned {
		t.Error("expected cleaned=false when no lock exists")
	}

	// Create stale lock
	lockPath := filepath.Join(tmpDir, ".lock")
	content := `{"pid": 99999999, "locked_at": "2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(lockPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write stale lock: %v", err)
	}

	cleaned, err = prof.CleanStaleLock()
	if err != nil {
		t.Fatalf("CleanStaleLock() error = %v", err)
	}
	if !cleaned {
		t.Error("expected cleaned=true for stale lock")
	}

	// Lock should be gone
	if prof.IsLocked() {
		t.Error("expected lock to be removed after CleanStaleLock")
	}
}

func TestLockWithCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// Create stale lock first
	lockPath := filepath.Join(tmpDir, ".lock")
	content := `{"pid": 99999999, "locked_at": "2025-01-01T00:00:00Z"}`
	if err := os.WriteFile(lockPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write stale lock: %v", err)
	}

	// LockWithCleanup should clean stale lock and acquire new lock
	if err := prof.LockWithCleanup(); err != nil {
		t.Fatalf("LockWithCleanup() error = %v", err)
	}

	// Should now be locked with current PID
	info, err := prof.GetLockInfo()
	if err != nil {
		t.Fatalf("GetLockInfo() error = %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("LockInfo.PID = %d, want %d", info.PID, os.Getpid())
	}

	prof.Unlock()
}

func TestIsProcessAlive(t *testing.T) {
	// Current process should be alive
	if !IsProcessAlive(os.Getpid()) {
		t.Error("expected current process to be alive")
	}

	// PID 0 is never a user process
	if IsProcessAlive(0) {
		t.Error("expected PID 0 to not be alive")
	}

	// Negative PID should not be alive
	if IsProcessAlive(-1) {
		t.Error("expected negative PID to not be alive")
	}

	// Very high PID unlikely to exist
	if IsProcessAlive(99999999) {
		t.Error("expected very high PID to not be alive")
	}
}

// =============================================================================
// Profile Save/Load Tests
// =============================================================================

func TestProfileSave(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "claude", "test-profile")

	prof := &Profile{
		Name:         "test-profile",
		Provider:     "claude",
		AuthMode:     "oauth",
		BasePath:     basePath,
		AccountLabel: "test@example.com",
		CreatedAt:    time.Now(),
		Metadata:     map[string]string{"key": "value"},
	}

	// Save
	if err := prof.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	metaPath := filepath.Join(basePath, "profile.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read saved profile: %v", err)
	}

	// Parse and verify
	var loaded Profile
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse saved profile: %v", err)
	}

	if loaded.Name != prof.Name {
		t.Errorf("loaded.Name = %q, want %q", loaded.Name, prof.Name)
	}
	if loaded.Provider != prof.Provider {
		t.Errorf("loaded.Provider = %q, want %q", loaded.Provider, prof.Provider)
	}
	if loaded.AccountLabel != prof.AccountLabel {
		t.Errorf("loaded.AccountLabel = %q, want %q", loaded.AccountLabel, prof.AccountLabel)
	}
}

func TestProfileDescriptionPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "claude", "with-description")

	prof := &Profile{
		Name:        "with-description",
		Provider:    "claude",
		AuthMode:    "oauth",
		BasePath:    basePath,
		Description: "Client X consulting project",
		CreatedAt:   time.Now(),
	}

	// Save
	if err := prof.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load and verify Description persisted
	metaPath := filepath.Join(basePath, "profile.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read saved profile: %v", err)
	}

	var loaded Profile
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse saved profile: %v", err)
	}

	if loaded.Description != "Client X consulting project" {
		t.Errorf("loaded.Description = %q, want %q", loaded.Description, "Client X consulting project")
	}
}

func TestProfileDescriptionOmitEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "codex", "no-description")

	prof := &Profile{
		Name:     "no-description",
		Provider: "codex",
		AuthMode: "api-key",
		BasePath: basePath,
		// Description intentionally left empty
		CreatedAt: time.Now(),
	}

	// Save
	if err := prof.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Read raw JSON and verify "description" key is not present
	metaPath := filepath.Join(basePath, "profile.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read saved profile: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to parse saved profile: %v", err)
	}

	if _, exists := raw["description"]; exists {
		t.Error("expected 'description' key to be omitted when empty")
	}
}

func TestUpdateLastUsed(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "codex", "test")

	prof := &Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: basePath,
	}

	// Initially zero
	if !prof.LastUsedAt.IsZero() {
		t.Error("expected LastUsedAt to be zero initially")
	}

	before := time.Now()
	if err := prof.UpdateLastUsed(); err != nil {
		t.Fatalf("UpdateLastUsed() error = %v", err)
	}
	after := time.Now()

	if prof.LastUsedAt.Before(before) || prof.LastUsedAt.After(after) {
		t.Errorf("LastUsedAt = %v, expected between %v and %v", prof.LastUsedAt, before, after)
	}
}

// =============================================================================
// Store Tests
// =============================================================================

func TestNewStore(t *testing.T) {
	store := NewStore("/tmp/test-profiles")
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
	if store.basePath != "/tmp/test-profiles" {
		t.Errorf("basePath = %q, want %q", store.basePath, "/tmp/test-profiles")
	}
}

func TestStoreProfilePath(t *testing.T) {
	store := NewStore("/tmp/profiles")
	path := store.ProfilePath("claude", "work")
	expected := "/tmp/profiles/claude/work"
	if path != expected {
		t.Errorf("ProfilePath() = %q, want %q", path, expected)
	}
}

func TestStoreCreate(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create profile
	prof, err := store.Create("codex", "test-profile", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if prof.Name != "test-profile" {
		t.Errorf("Name = %q, want %q", prof.Name, "test-profile")
	}
	if prof.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", prof.Provider, "codex")
	}
	if prof.AuthMode != "oauth" {
		t.Errorf("AuthMode = %q, want %q", prof.AuthMode, "oauth")
	}
	if prof.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}

	// Verify file exists
	metaPath := filepath.Join(tmpDir, "codex", "test-profile", "profile.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Error("expected profile.json to exist")
	}

	// Create duplicate should fail
	_, err = store.Create("codex", "test-profile", "oauth")
	if err == nil {
		t.Error("expected Create() to fail for duplicate profile")
	}
}

func TestStoreAllowsPlusInName(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	prof, err := store.Create("codex", "work+tag", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if prof.Name != "work+tag" {
		t.Errorf("Name = %q, want %q", prof.Name, "work+tag")
	}

	metaPath := filepath.Join(tmpDir, "codex", "work+tag", "profile.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Error("expected profile.json to exist for work+tag")
	}
}

func TestStoreRejectsUnsafeSegments(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	cases := []struct {
		provider string
		name     string
	}{
		{provider: "/", name: "work"},
		{provider: "codex", name: "/"},
		{provider: "codex", name: ".."},
		{provider: "codex", name: "a/b"},
		{provider: "codex", name: "a\\b"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.provider+"/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := store.Create(tc.provider, tc.name, "oauth"); err == nil {
				t.Fatal("expected Create() to fail for unsafe provider/name")
			}
			if err := store.Delete(tc.provider, tc.name); err == nil {
				t.Fatal("expected Delete() to fail for unsafe provider/name")
			}
			if store.Exists(tc.provider, tc.name) {
				t.Fatal("expected Exists() to be false for unsafe provider/name")
			}
		})
	}
}

func TestStoreLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create first
	created, err := store.Create("claude", "work", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	created.AccountLabel = "work@company.com"
	created.Save()

	// Load
	loaded, err := store.Load("claude", "work")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Name != "work" {
		t.Errorf("Name = %q, want %q", loaded.Name, "work")
	}
	if loaded.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", loaded.Provider, "claude")
	}
	if loaded.AccountLabel != "work@company.com" {
		t.Errorf("AccountLabel = %q, want %q", loaded.AccountLabel, "work@company.com")
	}

	// Load non-existent should fail
	_, err = store.Load("claude", "nonexistent")
	if err == nil {
		t.Error("expected Load() to fail for non-existent profile")
	}
}

func TestStoreDelete(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create
	if _, err := store.Create("gemini", "personal", "oauth"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Delete
	if err := store.Delete("gemini", "personal"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Should no longer exist
	if store.Exists("gemini", "personal") {
		t.Error("expected profile to not exist after Delete")
	}

	// Delete non-existent should fail
	if err := store.Delete("gemini", "personal"); err == nil {
		t.Error("expected Delete() to fail for non-existent profile")
	}
}

func TestStoreDeleteLockedFails(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create and lock
	prof, err := store.Create("codex", "locked-profile", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := prof.Lock(); err != nil {
		t.Fatalf("Lock() error = %v", err)
	}
	defer prof.Unlock()

	// Delete should fail
	if err := store.Delete("codex", "locked-profile"); err == nil {
		t.Error("expected Delete() to fail for locked profile")
	}
}

func TestStoreList(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create some profiles
	store.Create("claude", "work", "oauth")
	store.Create("claude", "personal", "oauth")
	store.Create("codex", "main", "api-key")

	// List claude profiles
	profiles, err := store.List("claude")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(profiles) != 2 {
		t.Errorf("len(profiles) = %d, want 2", len(profiles))
	}

	// List codex profiles
	profiles, err = store.List("codex")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("len(profiles) = %d, want 1", len(profiles))
	}

	// List non-existent provider
	profiles, err = store.List("gemini")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("len(profiles) = %d, want 0", len(profiles))
	}
}

func TestStoreListAll(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create profiles for multiple providers
	store.Create("claude", "a", "oauth")
	store.Create("claude", "b", "oauth")
	store.Create("codex", "c", "api-key")

	allProfiles, err := store.ListAll()
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}

	if len(allProfiles) != 2 {
		t.Errorf("len(allProfiles) = %d, want 2", len(allProfiles))
	}
	if len(allProfiles["claude"]) != 2 {
		t.Errorf("len(allProfiles[claude]) = %d, want 2", len(allProfiles["claude"]))
	}
	if len(allProfiles["codex"]) != 1 {
		t.Errorf("len(allProfiles[codex]) = %d, want 1", len(allProfiles["codex"]))
	}
}

func TestStoreExists(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Doesn't exist yet
	if store.Exists("claude", "test") {
		t.Error("expected Exists() = false before creation")
	}

	// Create
	store.Create("claude", "test", "oauth")

	// Now exists
	if !store.Exists("claude", "test") {
		t.Error("expected Exists() = true after creation")
	}
}

// =============================================================================
// DefaultStorePath Tests
// =============================================================================

func TestDefaultStorePath(t *testing.T) {
	originalCaamHome := os.Getenv("CAAM_HOME")
	// Test with XDG_DATA_HOME set
	originalXDG := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("CAAM_HOME", originalCaamHome)
	defer os.Setenv("XDG_DATA_HOME", originalXDG)

	os.Setenv("CAAM_HOME", "/custom/caam")
	os.Setenv("XDG_DATA_HOME", "/custom/data")
	path := DefaultStorePath()
	expected := "/custom/caam/data/profiles"
	if path != expected {
		t.Errorf("DefaultStorePath() with CAAM_HOME = %q, want %q", path, expected)
	}

	os.Unsetenv("CAAM_HOME")
	os.Setenv("XDG_DATA_HOME", "/custom/data")
	path = DefaultStorePath()
	expected = "/custom/data/caam/profiles"
	if path != expected {
		t.Errorf("DefaultStorePath() with XDG = %q, want %q", path, expected)
	}

	// Test without XDG_DATA_HOME (uses home dir)
	os.Unsetenv("XDG_DATA_HOME")
	path = DefaultStorePath()
	// Should contain ".local/share/caam/profiles" relative to home
	if !filepath.IsAbs(path) {
		// If path is relative, it means UserHomeDir failed - that's the fallback
		expected := ".local/share/caam/profiles"
		if path != expected {
			t.Errorf("DefaultStorePath() fallback = %q, want %q", path, expected)
		}
	}
}

// =============================================================================
// Clone Tests
// =============================================================================

func TestStoreClone(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create source profile
	source, err := store.Create("claude", "source", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	source.Description = "Original profile"
	source.BrowserCommand = "chrome"
	source.BrowserProfileDir = "Profile 1"
	source.Metadata = map[string]string{"key": "value"}
	if err := source.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Clone without auth
	cloned, err := store.Clone("claude", "source", "target", CloneOptions{})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	if cloned.Name != "target" {
		t.Errorf("Name = %q, want %q", cloned.Name, "target")
	}
	if cloned.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", cloned.Provider, "claude")
	}
	if cloned.AuthMode != "oauth" {
		t.Errorf("AuthMode = %q, want %q", cloned.AuthMode, "oauth")
	}
	if cloned.Description != "Cloned from source" {
		t.Errorf("Description = %q, want %q", cloned.Description, "Cloned from source")
	}
	if cloned.BrowserCommand != "chrome" {
		t.Errorf("BrowserCommand = %q, want %q", cloned.BrowserCommand, "chrome")
	}
	if cloned.BrowserProfileDir != "Profile 1" {
		t.Errorf("BrowserProfileDir = %q, want %q", cloned.BrowserProfileDir, "Profile 1")
	}
	if cloned.Metadata["key"] != "value" {
		t.Errorf("Metadata[key] = %q, want %q", cloned.Metadata["key"], "value")
	}

	// Verify cloned profile exists on disk
	if !store.Exists("claude", "target") {
		t.Error("expected cloned profile to exist")
	}
}

func TestStoreCloneWithCustomDescription(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create source
	if _, err := store.Create("codex", "src", "api-key"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Clone with custom description
	cloned, err := store.Clone("codex", "src", "dst", CloneOptions{
		Description: "Custom description",
	})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	if cloned.Description != "Custom description" {
		t.Errorf("Description = %q, want %q", cloned.Description, "Custom description")
	}
}

func TestStoreCloneWithAuth(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create source and add auth file
	source, err := store.Create("claude", "with-auth", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Create home directory (Store.Create doesn't create subdirs)
	if err := os.MkdirAll(source.HomePath(), 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create mock auth file in home directory
	authPath := filepath.Join(source.HomePath(), "auth.json")
	authContent := []byte(`{"token": "secret123"}`)
	if err := os.WriteFile(authPath, authContent, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Clone with auth
	cloned, err := store.Clone("claude", "with-auth", "cloned-auth", CloneOptions{
		WithAuth: true,
	})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	// Verify auth file was copied
	clonedAuthPath := filepath.Join(cloned.HomePath(), "auth.json")
	data, err := os.ReadFile(clonedAuthPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != string(authContent) {
		t.Errorf("auth file content = %q, want %q", string(data), string(authContent))
	}
}

func TestStoreCloneWithoutAuth(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create source and add auth file
	source, err := store.Create("claude", "has-auth", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Create home directory (Store.Create doesn't create subdirs)
	if err := os.MkdirAll(source.HomePath(), 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	authPath := filepath.Join(source.HomePath(), "auth.json")
	if err := os.WriteFile(authPath, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Clone without auth (default)
	cloned, err := store.Clone("claude", "has-auth", "no-auth", CloneOptions{})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	// Verify auth file was NOT copied
	clonedAuthPath := filepath.Join(cloned.HomePath(), "auth.json")
	if _, err := os.Stat(clonedAuthPath); !os.IsNotExist(err) {
		t.Error("expected auth file to NOT be copied when WithAuth=false")
	}
}

func TestStoreCloneErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create source
	if _, err := store.Create("claude", "existing", "oauth"); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Clone non-existent source
	_, err := store.Clone("claude", "nonexistent", "target", CloneOptions{})
	if err == nil {
		t.Error("expected Clone() to fail for non-existent source")
	}

	// Clone to same name
	_, err = store.Clone("claude", "existing", "existing", CloneOptions{})
	if err == nil {
		t.Error("expected Clone() to fail when source == target")
	}

	// Clone to existing target without --force
	store.Create("claude", "target", "oauth")
	_, err = store.Clone("claude", "existing", "target", CloneOptions{})
	if err == nil {
		t.Error("expected Clone() to fail when target exists without Force")
	}

	// Clone to existing target WITH --force
	_, err = store.Clone("claude", "existing", "target", CloneOptions{Force: true})
	if err != nil {
		t.Errorf("Clone() with Force=true error = %v", err)
	}
}

func TestStoreCloneIndependence(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create source
	source, err := store.Create("codex", "source", "oauth")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	source.Description = "Original"
	source.Save()

	// Clone
	_, err = store.Clone("codex", "source", "clone", CloneOptions{})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	// Modify source
	source.Description = "Modified"
	source.Save()

	// Load clone and verify it's unchanged
	clone, err := store.Load("codex", "clone")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if clone.Description != "Cloned from source" {
		t.Errorf("clone Description = %q, expected it to be independent of source modification", clone.Description)
	}
}

// =============================================================================
// Tag Tests
// =============================================================================

func TestValidateTag(t *testing.T) {
	tests := []struct {
		tag     string
		wantErr bool
	}{
		{"work", false},
		{"project-x", false},
		{"testing123", false},
		{"my-long-tag-name", false},
		{"", true},          // Empty
		{"   ", true},       // Whitespace only
		{"Work", true},      // Uppercase
		{"PROJECT", true},   // All uppercase
		{"project_x", true}, // Underscore not allowed
		{"project.x", true}, // Period not allowed
		{"project x", true}, // Space not allowed
		{"abcdefghijklmnopqrstuvwxyz0123456789", true}, // Too long (36 chars)
	}

	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			err := ValidateTag(tc.tag)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateTag(%q) error = %v, wantErr %v", tc.tag, err, tc.wantErr)
			}
		})
	}
}

func TestNormalizeTag(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"work", "work"},
		{"Work", "work"},
		{"WORK", "work"},
		{"  work  ", "work"},
		{"Project-X", "project-x"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := NormalizeTag(tc.input)
			if got != tc.expected {
				t.Errorf("NormalizeTag(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestProfileHasTag(t *testing.T) {
	prof := &Profile{
		Name:     "test",
		Provider: "claude",
		Tags:     []string{"work", "project-x"},
	}

	tests := []struct {
		tag      string
		expected bool
	}{
		{"work", true},
		{"project-x", true},
		{"personal", false},
		{"Work", true},      // Should match case-insensitively
		{"PROJECT-X", true}, // Should match case-insensitively
	}

	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			if got := prof.HasTag(tc.tag); got != tc.expected {
				t.Errorf("HasTag(%q) = %v, want %v", tc.tag, got, tc.expected)
			}
		})
	}
}

func TestProfileAddTag(t *testing.T) {
	t.Run("add valid tag", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude"}
		if err := prof.AddTag("work"); err != nil {
			t.Fatalf("AddTag() error = %v", err)
		}
		if !prof.HasTag("work") {
			t.Error("expected profile to have tag 'work'")
		}
	})

	t.Run("add duplicate tag is no-op", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude", Tags: []string{"work"}}
		if err := prof.AddTag("work"); err != nil {
			t.Fatalf("AddTag() error = %v", err)
		}
		if len(prof.Tags) != 1 {
			t.Errorf("len(Tags) = %d, want 1 (no duplicate)", len(prof.Tags))
		}
	})

	t.Run("uppercase tag gets normalized", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude"}
		// AddTag normalizes to lowercase before adding, so this should succeed
		if err := prof.AddTag("WORK"); err != nil {
			t.Fatalf("AddTag() error = %v", err)
		}
		// Should be stored as lowercase
		if !prof.HasTag("work") {
			t.Error("expected profile to have tag 'work' (normalized from 'WORK')")
		}
		if prof.Tags[0] != "work" {
			t.Errorf("tag stored as %q, want 'work'", prof.Tags[0])
		}
	})

	t.Run("invalid characters fail", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude"}
		// Underscore is not allowed even after normalization
		if err := prof.AddTag("invalid_tag"); err == nil {
			t.Error("expected AddTag() to fail for tag with underscore")
		}
	})

	t.Run("max tags limit", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude"}
		// Add max tags
		for i := 0; i < MaxTagCount; i++ {
			if err := prof.AddTag("tag" + string(rune('a'+i))); err != nil {
				t.Fatalf("AddTag() error = %v at tag %d", err, i)
			}
		}
		// Adding one more should fail
		if err := prof.AddTag("overflow"); err == nil {
			t.Error("expected AddTag() to fail when max tags exceeded")
		}
	})
}

func TestProfileRemoveTag(t *testing.T) {
	t.Run("remove existing tag", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude", Tags: []string{"work", "personal"}}
		if !prof.RemoveTag("work") {
			t.Error("expected RemoveTag() to return true for existing tag")
		}
		if prof.HasTag("work") {
			t.Error("expected tag 'work' to be removed")
		}
		if len(prof.Tags) != 1 {
			t.Errorf("len(Tags) = %d, want 1", len(prof.Tags))
		}
	})

	t.Run("remove non-existing tag", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude", Tags: []string{"work"}}
		if prof.RemoveTag("personal") {
			t.Error("expected RemoveTag() to return false for non-existing tag")
		}
		if len(prof.Tags) != 1 {
			t.Errorf("len(Tags) = %d, want 1", len(prof.Tags))
		}
	})

	t.Run("case insensitive removal", func(t *testing.T) {
		prof := &Profile{Name: "test", Provider: "claude", Tags: []string{"work"}}
		if !prof.RemoveTag("Work") {
			t.Error("expected RemoveTag() to work case-insensitively")
		}
	})
}

func TestProfileClearTags(t *testing.T) {
	prof := &Profile{Name: "test", Provider: "claude", Tags: []string{"work", "personal", "project-x"}}
	prof.ClearTags()
	if len(prof.Tags) != 0 {
		t.Errorf("len(Tags) = %d after ClearTags(), want 0", len(prof.Tags))
	}
}

func TestTagsPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "claude", "with-tags")

	prof := &Profile{
		Name:      "with-tags",
		Provider:  "claude",
		AuthMode:  "oauth",
		BasePath:  basePath,
		Tags:      []string{"work", "project-x", "testing"},
		CreatedAt: time.Now(),
	}

	// Save
	if err := prof.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load and verify Tags persisted
	metaPath := filepath.Join(basePath, "profile.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read saved profile: %v", err)
	}

	var loaded Profile
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse saved profile: %v", err)
	}

	if len(loaded.Tags) != 3 {
		t.Errorf("len(loaded.Tags) = %d, want 3", len(loaded.Tags))
	}
	if !loaded.HasTag("work") {
		t.Error("expected loaded profile to have tag 'work'")
	}
	if !loaded.HasTag("project-x") {
		t.Error("expected loaded profile to have tag 'project-x'")
	}
}

func TestTagsOmitEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	basePath := filepath.Join(tmpDir, "codex", "no-tags")

	prof := &Profile{
		Name:      "no-tags",
		Provider:  "codex",
		AuthMode:  "api-key",
		BasePath:  basePath,
		CreatedAt: time.Now(),
		// Tags intentionally left empty
	}

	// Save
	if err := prof.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Read raw JSON and verify "tags" key is not present
	metaPath := filepath.Join(basePath, "profile.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read saved profile: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to parse saved profile: %v", err)
	}

	if _, exists := raw["tags"]; exists {
		t.Error("expected 'tags' key to be omitted when empty")
	}
}

func TestStoreListByTag(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create profiles with tags
	p1, _ := store.Create("claude", "work1", "oauth")
	p1.Tags = []string{"work", "client-a"}
	p1.Save()

	p2, _ := store.Create("claude", "work2", "oauth")
	p2.Tags = []string{"work", "client-b"}
	p2.Save()

	p3, _ := store.Create("claude", "personal", "oauth")
	p3.Tags = []string{"personal"}
	p3.Save()

	// List by "work" tag
	workProfiles, err := store.ListByTag("claude", "work")
	if err != nil {
		t.Fatalf("ListByTag() error = %v", err)
	}
	if len(workProfiles) != 2 {
		t.Errorf("len(workProfiles) = %d, want 2", len(workProfiles))
	}

	// List by "personal" tag
	personalProfiles, err := store.ListByTag("claude", "personal")
	if err != nil {
		t.Fatalf("ListByTag() error = %v", err)
	}
	if len(personalProfiles) != 1 {
		t.Errorf("len(personalProfiles) = %d, want 1", len(personalProfiles))
	}

	// List by non-existent tag
	noProfiles, err := store.ListByTag("claude", "nonexistent")
	if err != nil {
		t.Fatalf("ListByTag() error = %v", err)
	}
	if len(noProfiles) != 0 {
		t.Errorf("len(noProfiles) = %d, want 0", len(noProfiles))
	}
}

func TestStoreListAllByTag(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create profiles across providers
	p1, _ := store.Create("claude", "work", "oauth")
	p1.Tags = []string{"work"}
	p1.Save()

	p2, _ := store.Create("codex", "work", "api-key")
	p2.Tags = []string{"work"}
	p2.Save()

	p3, _ := store.Create("claude", "personal", "oauth")
	p3.Tags = []string{"personal"}
	p3.Save()

	// List all by "work" tag
	workProfiles, err := store.ListAllByTag("work")
	if err != nil {
		t.Fatalf("ListAllByTag() error = %v", err)
	}
	if len(workProfiles) != 2 {
		t.Errorf("len(workProfiles) = %d, want 2 providers", len(workProfiles))
	}
	if len(workProfiles["claude"]) != 1 {
		t.Errorf("len(workProfiles[claude]) = %d, want 1", len(workProfiles["claude"]))
	}
	if len(workProfiles["codex"]) != 1 {
		t.Errorf("len(workProfiles[codex]) = %d, want 1", len(workProfiles["codex"]))
	}
}

func TestStoreAllTags(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	// Create profiles with various tags
	p1, _ := store.Create("claude", "p1", "oauth")
	p1.Tags = []string{"work", "client-a"}
	p1.Save()

	p2, _ := store.Create("claude", "p2", "oauth")
	p2.Tags = []string{"work", "client-b"}
	p2.Save()

	p3, _ := store.Create("claude", "p3", "oauth")
	p3.Tags = []string{"personal"}
	p3.Save()

	tags, err := store.AllTags("claude")
	if err != nil {
		t.Fatalf("AllTags() error = %v", err)
	}

	// Should have 4 unique tags: work, client-a, client-b, personal
	if len(tags) != 4 {
		t.Errorf("len(tags) = %d, want 4", len(tags))
	}

	// Verify expected tags are present
	tagSet := make(map[string]bool)
	for _, tag := range tags {
		tagSet[tag] = true
	}
	for _, expected := range []string{"work", "client-a", "client-b", "personal"} {
		if !tagSet[expected] {
			t.Errorf("expected tag %q to be present", expected)
		}
	}
}
