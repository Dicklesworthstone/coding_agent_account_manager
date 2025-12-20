package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultBackupConfig(t *testing.T) {
	cfg := DefaultBackupConfig()

	if cfg.Enabled {
		t.Error("expected Enabled to be false by default")
	}
	if cfg.GetInterval() != 7*24*time.Hour {
		t.Errorf("expected interval to be 7 days, got %v", cfg.GetInterval())
	}
	if cfg.GetKeepLast() != 5 {
		t.Errorf("expected keep_last to be 5, got %d", cfg.GetKeepLast())
	}
	if cfg.GetLocation() == "" {
		t.Error("expected location to be set")
	}
}

func TestBackupConfig_GetMethods(t *testing.T) {
	tests := []struct {
		name        string
		cfg         BackupConfig
		wantEnabled bool
		wantInt     time.Duration
		wantKeep    int
	}{
		{
			name:        "defaults",
			cfg:         BackupConfig{},
			wantEnabled: false,
			wantInt:     7 * 24 * time.Hour,
			wantKeep:    5,
		},
		{
			name: "custom values",
			cfg: BackupConfig{
				Enabled:  true,
				Interval: Duration(24 * time.Hour),
				KeepLast: 10,
			},
			wantEnabled: true,
			wantInt:     24 * time.Hour,
			wantKeep:    10,
		},
		{
			name: "zero interval uses default",
			cfg: BackupConfig{
				Interval: 0,
			},
			wantEnabled: false,
			wantInt:     7 * 24 * time.Hour,
			wantKeep:    5,
		},
		{
			name: "zero keep_last uses default",
			cfg: BackupConfig{
				KeepLast: 0,
			},
			wantEnabled: false,
			wantInt:     7 * 24 * time.Hour,
			wantKeep:    5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsEnabled(); got != tc.wantEnabled {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.wantEnabled)
			}
			if got := tc.cfg.GetInterval(); got != tc.wantInt {
				t.Errorf("GetInterval() = %v, want %v", got, tc.wantInt)
			}
			if got := tc.cfg.GetKeepLast(); got != tc.wantKeep {
				t.Errorf("GetKeepLast() = %v, want %v", got, tc.wantKeep)
			}
		})
	}
}

func TestBackupConfig_GetLocation(t *testing.T) {
	tests := []struct {
		name     string
		location string
		wantSet  bool
	}{
		{
			name:     "empty uses default",
			location: "",
			wantSet:  true, // should still return a path
		},
		{
			name:     "custom location",
			location: "/custom/path",
			wantSet:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BackupConfig{Location: tc.location}
			got := cfg.GetLocation()
			if tc.wantSet && got == "" {
				t.Error("expected GetLocation() to return a path")
			}
			if tc.location != "" && got != tc.location {
				t.Errorf("GetLocation() = %q, want %q", got, tc.location)
			}
		})
	}
}

func TestBackupConfig_JSON(t *testing.T) {
	cfg := BackupConfig{
		Enabled:  true,
		Interval: Duration(48 * time.Hour),
		KeepLast: 3,
		Location: "/backups",
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded BackupConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.Enabled != cfg.Enabled {
		t.Errorf("Enabled = %v, want %v", decoded.Enabled, cfg.Enabled)
	}
	if decoded.Interval != cfg.Interval {
		t.Errorf("Interval = %v, want %v", decoded.Interval, cfg.Interval)
	}
	if decoded.KeepLast != cfg.KeepLast {
		t.Errorf("KeepLast = %v, want %v", decoded.KeepLast, cfg.KeepLast)
	}
	if decoded.Location != cfg.Location {
		t.Errorf("Location = %v, want %v", decoded.Location, cfg.Location)
	}
}

func TestBackupConfig_InConfig(t *testing.T) {
	// Test that BackupConfig integrates properly with Config
	cfg := DefaultConfig()

	if cfg.Backup.Enabled {
		t.Error("expected backup to be disabled by default")
	}

	// Enable backup and serialize
	cfg.Backup.Enabled = true
	cfg.Backup.Interval = Duration(24 * time.Hour)
	cfg.Backup.KeepLast = 10

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// Deserialize and verify
	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if !decoded.Backup.Enabled {
		t.Error("expected backup to be enabled after roundtrip")
	}
	if decoded.Backup.GetInterval() != 24*time.Hour {
		t.Errorf("Interval = %v, want 24h", decoded.Backup.GetInterval())
	}
	if decoded.Backup.GetKeepLast() != 10 {
		t.Errorf("KeepLast = %v, want 10", decoded.Backup.GetKeepLast())
	}
}
