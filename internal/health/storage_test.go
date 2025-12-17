package health

import (
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
	path := filepath.Join(tmpDir, "health.json")
	storage := NewStorage(path)

	// Record first error
	if err := storage.RecordError("codex", "work"); err != nil {
		t.Fatalf("RecordError failed: %v", err)
	}

	profile, err := storage.GetProfile("codex", "work")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if profile.ErrorCount1h != 1 {
		t.Errorf("expected error count 1, got %d", profile.ErrorCount1h)
	}
	if profile.LastError.IsZero() {
		t.Error("LastError not set")
	}

	// Record second error
	if err := storage.RecordError("codex", "work"); err != nil {
		t.Fatalf("RecordError failed: %v", err)
	}

	profile, err = storage.GetProfile("codex", "work")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}
	if profile.ErrorCount1h != 2 {
		t.Errorf("expected error count 2, got %d", profile.ErrorCount1h)
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
			name: "expired token",
			health: &ProfileHealth{
				TokenExpiresAt: time.Now().Add(-1 * time.Hour),
			},
			expected: StatusCritical,
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
