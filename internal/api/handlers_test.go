package api

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "<1m"},
		{1 * time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h 0m"},
		{1*time.Hour + 30*time.Minute, "1h 30m"},
		{2 * time.Hour, "2h 0m"},
		{2*time.Hour + 45*time.Minute, "2h 45m"},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestNewHandlers(t *testing.T) {
	// Test with nil dependencies
	h := NewHandlers(nil, nil, nil)
	if h == nil {
		t.Fatal("NewHandlers() returned nil")
	}
}

func TestGetStatusWithNilDeps(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should not panic with nil vault
	// Note: This will return empty tools since vault is nil
	status, err := h.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if status == nil {
		t.Fatal("GetStatus() returned nil")
	}
	if status.Version == "" {
		t.Error("GetStatus() version is empty")
	}
	if status.Timestamp == "" {
		t.Error("GetStatus() timestamp is empty")
	}
}

func TestGetProfilesWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// With nil vault, should return error when trying to list
	_, err := h.GetProfiles("")
	if err == nil {
		t.Error("GetProfiles() expected error with nil vault")
	}
}

func TestGetUsageWithNilDeps(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return empty usage without error
	usage, err := h.GetUsage("")
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage == nil {
		t.Fatal("GetUsage() returned nil")
	}
	if len(usage.Usage) != 0 {
		t.Errorf("GetUsage() with nil deps should return empty, got %d entries", len(usage.Usage))
	}
}

func TestGetCoordinators(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return empty coordinators list
	coords, err := h.GetCoordinators()
	if err != nil {
		t.Fatalf("GetCoordinators() error = %v", err)
	}
	if coords == nil {
		t.Fatal("GetCoordinators() returned nil")
	}
	if coords.Coordinators == nil {
		t.Error("GetCoordinators() coordinators list is nil")
	}
}

func TestActivateWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := ActivateRequest{
		Tool:    "unknown",
		Profile: "test",
	}

	_, err := h.Activate(req)
	if err == nil {
		t.Error("Activate() expected error for unknown tool")
	}
}

func TestBackupWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := BackupRequest{
		Tool:    "unknown",
		Profile: "test",
	}

	_, err := h.Backup(req)
	if err == nil {
		t.Error("Backup() expected error for unknown tool")
	}
}

func TestGetProfileHealthWithNilStore(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return nil without panic
	health := h.getProfileHealth("claude", "test")
	if health != nil {
		t.Errorf("getProfileHealth() with nil store should return nil, got %v", health)
	}
}

func TestGetProfileIdentityWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return nil without panic
	id := h.getProfileIdentity("claude", "test")
	if id != nil {
		t.Errorf("getProfileIdentity() with nil vault should return nil, got %v", id)
	}
}

func TestToolsMapContainsExpectedTools(t *testing.T) {
	expectedTools := []string{"codex", "claude", "gemini"}

	for _, tool := range expectedTools {
		if _, ok := tools[tool]; !ok {
			t.Errorf("tools map missing %q", tool)
		}
	}
}
