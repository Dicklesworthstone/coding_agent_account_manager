package authpool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoad(t *testing.T) {
	// Create temp directory for state file
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.json")
	opts := PersistOptions{StatePath: statePath}

	// Create pool with some profiles
	pool := NewAuthPool()
	pool.AddProfile("claude", "alice")
	pool.SetStatus("claude", "alice", PoolStatusReady)
	pool.UpdateTokenExpiry("claude", "alice", time.Now().Add(time.Hour))
	pool.MarkUsed("claude", "alice")

	pool.AddProfile("codex", "bob")
	pool.SetCooldown("codex", "bob", 30*time.Minute)

	pool.AddProfile("gemini", "charlie")
	pool.SetError("gemini", "charlie", errTest("test error"))

	// Save
	if err := pool.Save(opts); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if !StateExists(opts) {
		t.Fatal("state file should exist after Save()")
	}

	// Load into new pool
	pool2 := NewAuthPool()
	if err := pool2.Load(opts); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify profiles loaded
	if pool2.Count() != 3 {
		t.Errorf("Count() = %d, want 3", pool2.Count())
	}

	// Verify alice profile
	alice := pool2.GetProfile("claude", "alice")
	if alice == nil {
		t.Fatal("alice profile not loaded")
	}
	if alice.Status != PoolStatusReady {
		t.Errorf("alice.Status = %v, want Ready", alice.Status)
	}
	if alice.TokenExpiry.IsZero() {
		t.Error("alice.TokenExpiry should be set")
	}
	if alice.LastUsed.IsZero() {
		t.Error("alice.LastUsed should be set")
	}

	// Verify bob profile
	bob := pool2.GetProfile("codex", "bob")
	if bob == nil {
		t.Fatal("bob profile not loaded")
	}
	if bob.Status != PoolStatusCooldown {
		t.Errorf("bob.Status = %v, want Cooldown", bob.Status)
	}
	if bob.CooldownUntil.IsZero() {
		t.Error("bob.CooldownUntil should be set")
	}

	// Verify charlie profile
	charlie := pool2.GetProfile("gemini", "charlie")
	if charlie == nil {
		t.Fatal("charlie profile not loaded")
	}
	if charlie.ErrorCount != 1 {
		t.Errorf("charlie.ErrorCount = %d, want 1", charlie.ErrorCount)
	}
	if charlie.ErrorMessage != "test error" {
		t.Errorf("charlie.ErrorMessage = %q, want 'test error'", charlie.ErrorMessage)
	}
}

func TestLoad_NonExistent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "nonexistent.json")
	opts := PersistOptions{StatePath: statePath}

	pool := NewAuthPool()
	if err := pool.Load(opts); err != nil {
		t.Errorf("Load() on non-existent should not error, got: %v", err)
	}

	// Pool should be empty
	if pool.Count() != 0 {
		t.Errorf("Count() = %d, want 0", pool.Count())
	}
}

func TestLoad_CorruptedJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "corrupted.json")
	opts := PersistOptions{StatePath: statePath}

	// Write corrupted JSON
	if err := os.WriteFile(statePath, []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("writing corrupted file: %v", err)
	}

	pool := NewAuthPool()
	err = pool.Load(opts)
	if err == nil {
		t.Error("Load() on corrupted JSON should return error")
	}
	if !strings.Contains(err.Error(), "parsing state file") {
		t.Errorf("error should mention parsing, got: %v", err)
	}
}

func TestLoad_FutureVersion(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "future.json")
	opts := PersistOptions{StatePath: statePath}

	// Write state with future version
	futureState := `{"version": 999, "updated_at": "2025-01-01T00:00:00Z", "profiles": {}}`
	if err := os.WriteFile(statePath, []byte(futureState), 0600); err != nil {
		t.Fatalf("writing future version file: %v", err)
	}

	pool := NewAuthPool()
	err = pool.Load(opts)
	if err == nil {
		t.Error("Load() on future version should return error")
	}
	if !strings.Contains(err.Error(), "newer than supported") {
		t.Errorf("error should mention version incompatibility, got: %v", err)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.json")
	opts := PersistOptions{StatePath: statePath}

	pool := NewAuthPool()
	pool.AddProfile("claude", "test")

	// Save
	if err := pool.Save(opts); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify no temp files left
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("reading temp dir: %v", err)
	}

	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file %s should not remain after Save()", e.Name())
		}
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Path with non-existent subdirectory
	statePath := filepath.Join(tmpDir, "subdir", "deep", "state.json")
	opts := PersistOptions{StatePath: statePath}

	pool := NewAuthPool()
	pool.AddProfile("claude", "test")

	if err := pool.Save(opts); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file was created
	if !StateExists(opts) {
		t.Error("state file should exist after Save()")
	}
}

func TestRemoveState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.json")
	opts := PersistOptions{StatePath: statePath}

	// Create state file
	pool := NewAuthPool()
	pool.AddProfile("claude", "test")
	if err := pool.Save(opts); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify exists
	if !StateExists(opts) {
		t.Fatal("state file should exist")
	}

	// Remove
	if err := RemoveState(opts); err != nil {
		t.Fatalf("RemoveState() error = %v", err)
	}

	// Verify removed
	if StateExists(opts) {
		t.Error("state file should not exist after RemoveState()")
	}

	// Remove again should not error
	if err := RemoveState(opts); err != nil {
		t.Errorf("RemoveState() on non-existent should not error, got: %v", err)
	}
}

func TestLoadFromReader(t *testing.T) {
	stateJSON := `{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"profiles": {
			"claude:test": {
				"provider": "claude",
				"profile_name": "test",
				"status": "ready",
				"priority": 5
			}
		}
	}`

	pool := NewAuthPool()
	if err := pool.LoadFromReader(strings.NewReader(stateJSON)); err != nil {
		t.Fatalf("LoadFromReader() error = %v", err)
	}

	profile := pool.GetProfile("claude", "test")
	if profile == nil {
		t.Fatal("profile should be loaded")
	}
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v, want Ready", profile.Status)
	}
	if profile.Priority != 5 {
		t.Errorf("Priority = %d, want 5", profile.Priority)
	}
}

func TestPoolStatus_JSON(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   string
	}{
		{PoolStatusUnknown, `"unknown"`},
		{PoolStatusReady, `"ready"`},
		{PoolStatusRefreshing, `"refreshing"`},
		{PoolStatusExpired, `"expired"`},
		{PoolStatusCooldown, `"cooldown"`},
		{PoolStatusError, `"error"`},
	}

	for _, tc := range tests {
		t.Run(tc.status.String(), func(t *testing.T) {
			// Test marshal
			data, err := tc.status.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON() error = %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("MarshalJSON() = %s, want %s", data, tc.want)
			}

			// Test unmarshal
			var status PoolStatus
			if err := status.UnmarshalJSON(data); err != nil {
				t.Fatalf("UnmarshalJSON() error = %v", err)
			}
			if status != tc.status {
				t.Errorf("UnmarshalJSON() = %v, want %v", status, tc.status)
			}
		})
	}
}

func TestPoolStatus_UnmarshalJSON_Unknown(t *testing.T) {
	var status PoolStatus
	if err := status.UnmarshalJSON([]byte(`"something_else"`)); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	if status != PoolStatusUnknown {
		t.Errorf("unknown string should unmarshal to Unknown, got %v", status)
	}
}

func TestDefaultStatePath(t *testing.T) {
	// Clear XDG_DATA_HOME to test fallback
	orig := os.Getenv("XDG_DATA_HOME")
	os.Unsetenv("XDG_DATA_HOME")
	defer func() {
		if orig != "" {
			os.Setenv("XDG_DATA_HOME", orig)
		}
	}()

	path := DefaultStatePath()
	if !strings.Contains(path, ".local/share/caam/auth_pool_state.json") {
		t.Errorf("DefaultStatePath() = %s, expected to contain .local/share/caam/auth_pool_state.json", path)
	}
}

func TestDefaultStatePath_WithXDG(t *testing.T) {
	orig := os.Getenv("XDG_DATA_HOME")
	defer func() {
		if orig != "" {
			os.Setenv("XDG_DATA_HOME", orig)
		} else {
			os.Unsetenv("XDG_DATA_HOME")
		}
	}()

	os.Setenv("XDG_DATA_HOME", "/custom/data")
	path := DefaultStatePath()
	expected := "/custom/data/caam/auth_pool_state.json"
	if path != expected {
		t.Errorf("DefaultStatePath() = %s, want %s", path, expected)
	}
}

func TestSaveAndLoad_AllStatuses(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.json")
	opts := PersistOptions{StatePath: statePath}

	// Create pool with all status types
	pool := NewAuthPool()

	statuses := []struct {
		name   string
		status PoolStatus
	}{
		{"ready", PoolStatusReady},
		{"refreshing", PoolStatusRefreshing},
		{"expired", PoolStatusExpired},
		{"cooldown", PoolStatusCooldown},
		{"error", PoolStatusError},
		{"unknown", PoolStatusUnknown},
	}

	for _, s := range statuses {
		pool.AddProfile("claude", s.name)
		pool.SetStatus("claude", s.name, s.status)
	}

	// Save
	if err := pool.Save(opts); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load into new pool
	pool2 := NewAuthPool()
	if err := pool2.Load(opts); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify all statuses preserved
	for _, s := range statuses {
		profile := pool2.GetProfile("claude", s.name)
		if profile == nil {
			t.Fatalf("profile %s not loaded", s.name)
		}
		if profile.Status != s.status {
			t.Errorf("profile %s status = %v, want %v", s.name, profile.Status, s.status)
		}
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "authpool_test")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.json")
	opts := PersistOptions{StatePath: statePath}

	// Create pool with detailed profile data
	pool := NewAuthPool()
	pool.AddProfile("claude", "detailed")

	// Set all fields
	now := time.Now()
	pool.mu.Lock()
	profile := pool.profiles["claude:detailed"]
	profile.Status = PoolStatusReady
	profile.TokenExpiry = now.Add(time.Hour)
	profile.LastRefresh = now.Add(-30 * time.Minute)
	profile.LastCheck = now.Add(-5 * time.Minute)
	profile.LastUsed = now.Add(-10 * time.Minute)
	profile.CooldownUntil = time.Time{} // Not in cooldown
	profile.ErrorCount = 2
	profile.ErrorMessage = "previous error"
	profile.Priority = 10
	pool.mu.Unlock()

	// Save
	if err := pool.Save(opts); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	pool2 := NewAuthPool()
	if err := pool2.Load(opts); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify all fields
	loaded := pool2.GetProfile("claude", "detailed")
	if loaded == nil {
		t.Fatal("profile not loaded")
	}

	if loaded.Provider != "claude" {
		t.Errorf("Provider = %s, want claude", loaded.Provider)
	}
	if loaded.ProfileName != "detailed" {
		t.Errorf("ProfileName = %s, want detailed", loaded.ProfileName)
	}
	if loaded.Status != PoolStatusReady {
		t.Errorf("Status = %v, want Ready", loaded.Status)
	}
	if loaded.ErrorCount != 2 {
		t.Errorf("ErrorCount = %d, want 2", loaded.ErrorCount)
	}
	if loaded.ErrorMessage != "previous error" {
		t.Errorf("ErrorMessage = %s, want 'previous error'", loaded.ErrorMessage)
	}
	if loaded.Priority != 10 {
		t.Errorf("Priority = %d, want 10", loaded.Priority)
	}

	// Times should be close (within a second due to JSON round-trip)
	if loaded.TokenExpiry.Sub(profile.TokenExpiry).Abs() > time.Second {
		t.Errorf("TokenExpiry mismatch: got %v, want %v", loaded.TokenExpiry, profile.TokenExpiry)
	}
	if loaded.LastRefresh.Sub(profile.LastRefresh).Abs() > time.Second {
		t.Errorf("LastRefresh mismatch: got %v, want %v", loaded.LastRefresh, profile.LastRefresh)
	}
	if loaded.LastCheck.Sub(profile.LastCheck).Abs() > time.Second {
		t.Errorf("LastCheck mismatch: got %v, want %v", loaded.LastCheck, profile.LastCheck)
	}
	if loaded.LastUsed.Sub(profile.LastUsed).Abs() > time.Second {
		t.Errorf("LastUsed mismatch: got %v, want %v", loaded.LastUsed, profile.LastUsed)
	}
}
