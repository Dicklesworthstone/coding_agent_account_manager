package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverChromiumProfilesByDir(t *testing.T) {
	// Create a temp directory structure simulating Chrome profiles
	tempDir := t.TempDir()

	// Create Default profile
	defaultDir := filepath.Join(tempDir, "Default")
	if err := os.MkdirAll(defaultDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create Profile 1 with Preferences file
	profile1Dir := filepath.Join(tempDir, "Profile 1")
	if err := os.MkdirAll(profile1Dir, 0755); err != nil {
		t.Fatal(err)
	}

	prefs := map[string]any{
		"profile": map[string]any{
			"name": "Work Profile",
		},
		"account_info": []map[string]any{
			{"email": "work@example.com"},
		},
	}
	prefsData, _ := json.Marshal(prefs)
	if err := os.WriteFile(filepath.Join(profile1Dir, "Preferences"), prefsData, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a non-profile directory that should be ignored
	if err := os.MkdirAll(filepath.Join(tempDir, "Cache"), 0755); err != nil {
		t.Fatal(err)
	}

	profiles := discoverChromiumProfilesByDir(tempDir)

	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	// Check Default profile
	var defaultProfile, workProfile *BrowserProfile
	for i := range profiles {
		if profiles[i].ID == "Default" {
			defaultProfile = &profiles[i]
		}
		if profiles[i].ID == "Profile 1" {
			workProfile = &profiles[i]
		}
	}

	if defaultProfile == nil {
		t.Error("Default profile not found")
	} else {
		if !defaultProfile.IsDefault {
			t.Error("Default profile should have IsDefault=true")
		}
	}

	if workProfile == nil {
		t.Error("Work profile not found")
	} else {
		if workProfile.Name != "Work Profile" {
			t.Errorf("expected name 'Work Profile', got %q", workProfile.Name)
		}
		if workProfile.Email != "work@example.com" {
			t.Errorf("expected email 'work@example.com', got %q", workProfile.Email)
		}
	}
}

func TestDiscoverChromiumProfiles(t *testing.T) {
	// Create a temp directory with Local State file
	tempDir := t.TempDir()

	// Create profiles directory
	if err := os.MkdirAll(filepath.Join(tempDir, "Default"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "Profile 1"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create Local State with profile info
	localState := map[string]any{
		"profile": map[string]any{
			"info_cache": map[string]any{
				"Default": map[string]any{
					"name":      "Person 1",
					"user_name": "user1@gmail.com",
				},
				"Profile 1": map[string]any{
					"name":      "Person 2",
					"user_name": "user2@gmail.com",
					"gaia_name": "John Doe",
				},
			},
		},
	}
	localStateData, _ := json.Marshal(localState)
	if err := os.WriteFile(filepath.Join(tempDir, "Local State"), localStateData, 0644); err != nil {
		t.Fatal(err)
	}

	profiles := discoverChromiumProfiles(tempDir)

	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	// Verify profiles were parsed from Local State
	foundDefault := false
	foundProfile1 := false
	for _, p := range profiles {
		if p.ID == "Default" {
			foundDefault = true
			if p.Email != "user1@gmail.com" {
				t.Errorf("expected Default email 'user1@gmail.com', got %q", p.Email)
			}
		}
		if p.ID == "Profile 1" {
			foundProfile1 = true
			if p.Email != "user2@gmail.com" {
				t.Errorf("expected Profile 1 email 'user2@gmail.com', got %q", p.Email)
			}
		}
	}

	if !foundDefault {
		t.Error("Default profile not found")
	}
	if !foundProfile1 {
		t.Error("Profile 1 not found")
	}
}

func TestDiscoverFirefoxProfiles(t *testing.T) {
	// Create a temp directory with profiles.ini
	tempDir := t.TempDir()

	profilesINI := `[General]
StartWithLastProfile=1

[Profile0]
Name=default-release
IsRelative=1
Path=abc123.default-release
Default=1

[Profile1]
Name=work
IsRelative=1
Path=xyz789.work

[Install12345ABC]
Default=abc123.default-release
Locked=1
`

	if err := os.WriteFile(filepath.Join(tempDir, "profiles.ini"), []byte(profilesINI), 0644); err != nil {
		t.Fatal(err)
	}

	profiles := discoverFirefoxProfiles(tempDir)

	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	// Check for default-release and work profiles
	var defaultProfile, workProfile *BrowserProfile
	for i := range profiles {
		if profiles[i].Name == "default-release" {
			defaultProfile = &profiles[i]
		}
		if profiles[i].Name == "work" {
			workProfile = &profiles[i]
		}
	}

	if defaultProfile == nil {
		t.Error("default-release profile not found")
	} else {
		if !defaultProfile.IsDefault {
			t.Error("default-release should have IsDefault=true")
		}
	}

	if workProfile == nil {
		t.Error("work profile not found")
	}
}

func TestFileExists(t *testing.T) {
	tempDir := t.TempDir()

	// Test with existing file
	existingFile := filepath.Join(tempDir, "exists.txt")
	if err := os.WriteFile(existingFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	if !fileExists(existingFile) {
		t.Error("fileExists should return true for existing file")
	}

	// Test with non-existing file
	if fileExists(filepath.Join(tempDir, "nonexistent.txt")) {
		t.Error("fileExists should return false for non-existing file")
	}
}

func TestBrowserProfileDefaults(t *testing.T) {
	profile := BrowserProfile{
		Name:      "Test",
		ID:        "Profile 1",
		IsDefault: false,
	}

	if profile.IsDefault {
		t.Error("IsDefault should be false by default")
	}

	if profile.Email != "" {
		t.Error("Email should be empty by default")
	}
}

func TestDetectedBrowserFields(t *testing.T) {
	browser := DetectedBrowser{
		Type:    BrowserChrome,
		Name:    "Google Chrome",
		Command: "/usr/bin/google-chrome",
		DataDir: "/home/user/.config/google-chrome",
	}

	if browser.Type != BrowserChrome {
		t.Errorf("expected type BrowserChrome, got %v", browser.Type)
	}

	if browser.Name != "Google Chrome" {
		t.Errorf("expected name 'Google Chrome', got %q", browser.Name)
	}
}

func TestBrowserTypeConstants(t *testing.T) {
	tests := []struct {
		browserType BrowserType
		expected    string
	}{
		{BrowserChrome, "chrome"},
		{BrowserFirefox, "firefox"},
		{BrowserEdge, "edge"},
		{BrowserBrave, "brave"},
		{BrowserChromium, "chromium"},
		{BrowserDefault, "default"},
	}

	for _, tc := range tests {
		if string(tc.browserType) != tc.expected {
			t.Errorf("expected %q, got %q", tc.expected, string(tc.browserType))
		}
	}
}

func TestDetectBrowsersEmpty(t *testing.T) {
	// This test just ensures DetectBrowsers doesn't panic
	// In a test environment, it may find no browsers or find the system's browsers
	browsers := DetectBrowsers()
	// We just check it returns without error
	_ = browsers
}

func TestDiscoverChromiumProfilesMalformed(t *testing.T) {
	tempDir := t.TempDir()

	// Write malformed Local State
	if err := os.WriteFile(filepath.Join(tempDir, "Local State"), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should fall back to directory scanning
	profiles := discoverChromiumProfiles(tempDir)

	// Even with malformed Local State, should return empty (no profile dirs)
	if profiles != nil && len(profiles) > 0 {
		t.Errorf("expected no profiles from malformed Local State, got %d", len(profiles))
	}
}

func TestDiscoverFirefoxProfilesNoFile(t *testing.T) {
	tempDir := t.TempDir()

	// No profiles.ini file
	profiles := discoverFirefoxProfiles(tempDir)

	if profiles != nil {
		t.Errorf("expected nil from missing profiles.ini, got %v", profiles)
	}
}
