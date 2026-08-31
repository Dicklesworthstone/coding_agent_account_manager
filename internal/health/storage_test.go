package health

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStorage(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	store := NewStorage(path)

	if store.Path() != path {
		t.Errorf("expected path %s, got %s", path, store.Path())
	}
}

func TestStorage_Load_Save(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Test Load on non-existent file
	store, err := storage.Load()
	if err != nil {
		t.Fatalf("Load on non-existent file failed: %v", err)
	}
	if len(store.Profiles) != 0 {
		t.Errorf("expected empty profiles, got %d", len(store.Profiles))
	}

	// Test Save
	now := time.Now().Truncate(time.Second) // Truncate for JSON comparison
	store.Profiles["test/profile"] = &ProfileHealth{
		TokenExpiresAt: now.Add(time.Hour),
		ErrorCount1h:   5,
		PlanType:       "pro",
	}

	if err := storage.Save(store); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("file was not created")
	}

	// Test Load again
	loadedStore, err := storage.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	profile := loadedStore.Profiles["test/profile"]
	if profile == nil {
		t.Fatal("profile not found after load")
	}

	if !profile.TokenExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("expected expiry %v, got %v", now.Add(time.Hour), profile.TokenExpiresAt)
	}
	if profile.ErrorCount1h != 5 {
		t.Errorf("expected error count 5, got %d", profile.ErrorCount1h)
	}
	if profile.PlanType != "pro" {
		t.Errorf("expected plan pro, got %s", profile.PlanType)
	}
}

func TestStorage_UpdateProfile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	health := &ProfileHealth{
		ErrorCount1h: 1,
	}

	if err := storage.UpdateProfile("claude", "user@example.com", health); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	retrieved, err := storage.GetProfile("claude", "user@example.com")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("profile not found")
	}
	if retrieved.ErrorCount1h != 1 {
		t.Errorf("expected error count 1, got %d", retrieved.ErrorCount1h)
	}
}

func TestStorage_RecordError(t *testing.T) {
	tmpDir := t.TempDir()
	healthPath := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(healthPath)

	// Record an error
	errCause := errors.New("401 Unauthorized")
	if err := storage.RecordError("codex", "work", errCause); err != nil {
		t.Fatalf("RecordError failed: %v", err)
	}

	// Verify
	health, err := storage.GetProfile("codex", "work")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if health.ErrorCount1h != 1 {
		t.Errorf("expected 1 error, got %d", health.ErrorCount1h)
	}
	if health.Penalty != 1.0 {
		t.Errorf("expected penalty 1.0, got %f", health.Penalty)
	}

	// Record another error
	if err := storage.RecordError("codex", "work", errors.New("429 Too Many Requests")); err != nil {
		t.Fatalf("RecordError failed: %v", err)
	}

	health, _ = storage.GetProfile("codex", "work")
	if health.ErrorCount1h != 2 {
		t.Errorf("expected 2 errors, got %d", health.ErrorCount1h)
	}
	if health.Penalty != 1.5 { // 1.0 + 0.5
		t.Errorf("expected penalty 1.5, got %f", health.Penalty)
	}

	// Clear errors
	if err := storage.ClearErrors("codex", "work"); err != nil {
		t.Fatalf("ClearErrors failed: %v", err)
	}

	health, _ = storage.GetProfile("codex", "work")
	if health.ErrorCount1h != 0 {
		t.Errorf("expected 0 errors, got %d", health.ErrorCount1h)
	}
	// Penalty should persist!
	if health.Penalty != 1.5 {
		t.Errorf("expected penalty 1.5 to persist, got %f", health.Penalty)
	}
}

func TestStorage_CorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Write corrupted JSON
	if err := os.WriteFile(path, []byte("{ invalid json"), 0600); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}

	// Load should recover and return empty store
	store, err := storage.Load()
	if err != nil {
		t.Fatalf("Load failed on corrupted file: %v", err)
	}
	if len(store.Profiles) != 0 {
		t.Error("expected empty profiles on corrupted file")
	}

	// Should be able to save new data
	if err := storage.UpdateProfile("test", "test", &ProfileHealth{}); err != nil {
		t.Fatalf("failed to save after corruption: %v", err)
	}
}

func TestStorage_SetTokenExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	expiry := time.Now().Add(24 * time.Hour)
	if err := storage.SetTokenExpiry("gemini", "personal", expiry); err != nil {
		t.Fatalf("SetTokenExpiry failed: %v", err)
	}

	profile, err := storage.GetProfile("gemini", "personal")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if !profile.TokenExpiresAt.Equal(expiry) {
		t.Errorf("expiry time mismatch")
	}
	if profile.LastChecked.IsZero() {
		t.Error("LastChecked not set")
	}
}

func TestCalculateStatus(t *testing.T) {
	tests := []struct {
		name     string
		health   *ProfileHealth
		expected HealthStatus
	}{
		{
			name:     "nil health",
			health:   nil,
			expected: StatusUnknown,
		},
		{
			name: "expired live token",
			health: &ProfileHealth{
				TokenExpiresAt: time.Now().Add(-1 * time.Hour),
				ExpiryLive:     true,
			},
			expected: StatusCritical,
		},
		{
			name: "expired snapshot without live confirmation",
			health: &ProfileHealth{
				TokenExpiresAt: time.Now().Add(-1 * time.Hour),
			},
			expected: StatusUnknown,
		},
		{
			name: "expiring soon token",
			health: &ProfileHealth{
				TokenExpiresAt: time.Now().Add(30 * time.Minute),
			},
			expected: StatusWarning,
		},
		{
			name: "valid token",
			health: &ProfileHealth{
				TokenExpiresAt: time.Now().Add(2 * time.Hour),
			},
			expected: StatusHealthy,
		},
		{
			name: "many errors",
			health: &ProfileHealth{
				ErrorCount1h: 5,
			},
			expected: StatusCritical,
		},
		{
			name: "some errors",
			health: &ProfileHealth{
				ErrorCount1h: 1,
			},
			expected: StatusWarning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateStatus(tt.health)
			if got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestDefaultHealthPath(t *testing.T) {
	t.Run("with CAAM_HOME", func(t *testing.T) {
		origCaam := os.Getenv("CAAM_HOME")
		origXDG := os.Getenv("XDG_DATA_HOME")
		defer os.Setenv("CAAM_HOME", origCaam)
		defer os.Setenv("XDG_DATA_HOME", origXDG)

		os.Setenv("CAAM_HOME", "/custom/caam")
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		path := DefaultHealthPath()
		expected := "/custom/caam/data/health.json"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("with XDG_DATA_HOME", func(t *testing.T) {
		origCaam := os.Getenv("CAAM_HOME")
		orig := os.Getenv("XDG_DATA_HOME")
		defer os.Setenv("CAAM_HOME", origCaam)
		defer os.Setenv("XDG_DATA_HOME", orig)

		os.Unsetenv("CAAM_HOME")
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		path := DefaultHealthPath()
		expected := "/custom/data/caam/health.json"
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("without XDG_DATA_HOME", func(t *testing.T) {
		origCaam := os.Getenv("CAAM_HOME")
		orig := os.Getenv("XDG_DATA_HOME")
		defer os.Setenv("CAAM_HOME", origCaam)
		defer os.Setenv("XDG_DATA_HOME", orig)

		os.Unsetenv("CAAM_HOME")
		os.Setenv("XDG_DATA_HOME", "")
		path := DefaultHealthPath()
		// Should use home dir
		home, _ := os.UserHomeDir()
		if home != "" {
			expected := filepath.Join(home, ".local", "share", "caam", "health.json")
			if path != expected {
				t.Errorf("expected %s, got %s", expected, path)
			}
		}
	})
}

func TestNewStorage_EmptyPath(t *testing.T) {
	// NewStorage with empty path should use DefaultHealthPath
	storage := NewStorage("")
	expected := DefaultHealthPath()
	if storage.Path() != expected {
		t.Errorf("expected path %s, got %s", expected, storage.Path())
	}
}

func TestStorage_DeleteProfile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Create a profile first
	health := &ProfileHealth{
		ErrorCount1h: 3,
		PlanType:     "pro",
	}
	if err := storage.UpdateProfile("claude", "test@example.com", health); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	// Verify it exists
	retrieved, err := storage.GetProfile("claude", "test@example.com")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("profile should exist before deletion")
	}

	// Delete the profile
	if err := storage.DeleteProfile("claude", "test@example.com"); err != nil {
		t.Fatalf("DeleteProfile failed: %v", err)
	}

	// Verify it's gone
	retrieved, err = storage.GetProfile("claude", "test@example.com")
	if err != nil {
		t.Fatalf("GetProfile failed after delete: %v", err)
	}
	if retrieved != nil {
		t.Error("profile should be nil after deletion")
	}
}

func TestStorage_SetPlanType(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Set plan type on non-existent profile
	if err := storage.SetPlanType("gemini", "user", "enterprise"); err != nil {
		t.Fatalf("SetPlanType failed: %v", err)
	}

	// Verify
	profile, err := storage.GetProfile("gemini", "user")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if profile == nil {
		t.Fatal("profile should exist after SetPlanType")
	}
	if profile.PlanType != "enterprise" {
		t.Errorf("expected plan type 'enterprise', got '%s'", profile.PlanType)
	}

	// Update plan type
	if err := storage.SetPlanType("gemini", "user", "pro"); err != nil {
		t.Fatalf("SetPlanType update failed: %v", err)
	}

	profile, err = storage.GetProfile("gemini", "user")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if profile.PlanType != "pro" {
		t.Errorf("expected plan type 'pro', got '%s'", profile.PlanType)
	}
}

func TestStorage_DecayPenalties(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Create a profile with a penalty
	health := &ProfileHealth{
		Penalty:          1.0,
		PenaltyUpdatedAt: time.Now().Add(-2 * time.Hour), // 2 hours ago
	}
	if err := storage.UpdateProfile("claude", "test", health); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	// Decay penalties
	if err := storage.DecayPenalties(); err != nil {
		t.Fatalf("DecayPenalties failed: %v", err)
	}

	// Verify penalty was decayed
	profile, err := storage.GetProfile("claude", "test")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if profile.Penalty >= 1.0 {
		t.Errorf("penalty should have decayed from 1.0, got %f", profile.Penalty)
	}
}

func TestStorage_DecayPenalties_NoChange(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Create a profile with zero penalty
	health := &ProfileHealth{
		Penalty:          0,
		PenaltyUpdatedAt: time.Now(),
	}
	if err := storage.UpdateProfile("claude", "test", health); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	// Decay penalties (should not save since no change)
	if err := storage.DecayPenalties(); err != nil {
		t.Fatalf("DecayPenalties failed: %v", err)
	}

	// Verify penalty is still 0
	profile, err := storage.GetProfile("claude", "test")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if profile.Penalty != 0 {
		t.Errorf("penalty should still be 0, got %f", profile.Penalty)
	}
}

func TestStorage_GetStatus(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Test unknown status for non-existent profile
	status, err := storage.GetStatus("unknown", "profile")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("expected StatusUnknown for non-existent profile, got %v", status)
	}

	// Create a healthy profile
	health := &ProfileHealth{
		TokenExpiresAt: time.Now().Add(2 * time.Hour),
		ErrorCount1h:   0,
	}
	if err := storage.UpdateProfile("claude", "healthy", health); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	status, err = storage.GetStatus("claude", "healthy")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status != StatusHealthy {
		t.Errorf("expected StatusHealthy, got %v", status)
	}

	// Create an expired profile
	health = &ProfileHealth{
		TokenExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	if err := storage.UpdateProfile("claude", "expired", health); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	status, err = storage.GetStatus("claude", "expired")
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status != StatusUnknown {
		t.Errorf("expected StatusUnknown for stored snapshot expiry (not live-confirmed), got %v", status)
	}
}

func TestStorage_ListProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// List empty profiles
	profiles, err := storage.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles failed: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("expected 0 profiles, got %d", len(profiles))
	}

	// Add some profiles
	if err := storage.UpdateProfile("claude", "user1", &ProfileHealth{PlanType: "pro"}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}
	if err := storage.UpdateProfile("codex", "user2", &ProfileHealth{PlanType: "free"}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}
	if err := storage.UpdateProfile("gemini", "user3", &ProfileHealth{PlanType: "enterprise"}); err != nil {
		t.Fatalf("UpdateProfile failed: %v", err)
	}

	// List again
	profiles, err = storage.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles failed: %v", err)
	}
	if len(profiles) != 3 {
		t.Errorf("expected 3 profiles, got %d", len(profiles))
	}

	// Verify profiles are present with correct data
	if p, ok := profiles["claude/user1"]; !ok || p.PlanType != "pro" {
		t.Error("claude/user1 profile not found or has wrong PlanType")
	}
	if p, ok := profiles["codex/user2"]; !ok || p.PlanType != "free" {
		t.Error("codex/user2 profile not found or has wrong PlanType")
	}
	if p, ok := profiles["gemini/user3"]; !ok || p.PlanType != "enterprise" {
		t.Error("gemini/user3 profile not found or has wrong PlanType")
	}

	// Verify it's a copy (modifying should not affect original)
	profiles["claude/user1"].PlanType = "modified"
	original, _ := storage.GetProfile("claude", "user1")
	if original.PlanType != "pro" {
		t.Error("ListProfiles should return a copy, not the original")
	}
}
