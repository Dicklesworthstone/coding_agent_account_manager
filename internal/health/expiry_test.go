package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseOAuthFile(t *testing.T) {
	testdata := "testdata"

	tests := []struct {
		name            string
		file            string
		expectError     bool
		expectExpiry    bool
		expectRefresh   bool
		expiryAfterNow  bool
	}{
		{
			name:           "claude oauth with ISO8601 expiry",
			file:           "claude_oauth.json",
			expectError:    false,
			expectExpiry:   true,
			expectRefresh:  true,
			expiryAfterNow: true,
		},
		{
			name:           "codex auth with unix timestamp",
			file:           "codex_auth.json",
			expectError:    false,
			expectExpiry:   true,
			expectRefresh:  true,
			expiryAfterNow: true,
		},
		{
			name:           "gemini settings with expiry field",
			file:           "gemini_settings.json",
			expectError:    false,
			expectExpiry:   true,
			expectRefresh:  true,
			expiryAfterNow: true,
		},
		{
			name:          "refresh token only",
			file:          "refresh_only.json",
			expectError:   false,
			expectExpiry:  false,
			expectRefresh: true,
		},
		{
			name:          "no expiry info",
			file:          "no_expiry.json",
			expectError:   true,
			expectExpiry:  false,
			expectRefresh: false,
		},
		{
			name:        "non-existent file",
			file:        "does_not_exist.json",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(testdata, tc.file)
			info, err := parseOAuthFile(path)

			if tc.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.expectExpiry && info.ExpiresAt.IsZero() {
				t.Error("expected expiry to be set")
			}
			if !tc.expectExpiry && !info.ExpiresAt.IsZero() {
				t.Errorf("expected no expiry, got %v", info.ExpiresAt)
			}

			if tc.expectRefresh && !info.HasRefreshToken {
				t.Error("expected HasRefreshToken to be true")
			}
			if !tc.expectRefresh && info.HasRefreshToken {
				t.Error("expected HasRefreshToken to be false")
			}

			if tc.expiryAfterNow && !info.ExpiresAt.IsZero() {
				if info.ExpiresAt.Before(time.Now()) {
					t.Error("expected expiry to be in the future")
				}
			}
		})
	}
}

func TestParseADCFile(t *testing.T) {
	testdata := "testdata"

	t.Run("valid ADC file", func(t *testing.T) {
		path := filepath.Join(testdata, "gcloud_adc.json")
		info, err := parseADCFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !info.HasRefreshToken {
			t.Error("expected HasRefreshToken to be true")
		}
		if !info.ExpiresAt.IsZero() {
			t.Error("ADC should not have expiry")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := parseADCFile(filepath.Join(testdata, "nonexistent.json"))
		if err == nil {
			t.Error("expected error for non-existent file")
		}
	})
}

func TestParseExpiryField(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected time.Time
	}{
		{
			name:     "nil",
			input:    nil,
			expected: time.Time{},
		},
		{
			name:     "RFC3339 string",
			input:    "2025-12-18T12:00:00Z",
			expected: time.Date(2025, 12, 18, 12, 0, 0, 0, time.UTC),
		},
		{
			name:     "RFC3339 with timezone",
			input:    "2025-12-18T12:00:00+00:00",
			expected: time.Date(2025, 12, 18, 12, 0, 0, 0, time.UTC),
		},
		{
			name:     "unix timestamp float64",
			input:    float64(1734523200),
			expected: time.Unix(1734523200, 0),
		},
		{
			name:     "unix timestamp int64",
			input:    int64(1734523200),
			expected: time.Unix(1734523200, 0),
		},
		{
			name:     "unix timestamp int",
			input:    int(1734523200),
			expected: time.Unix(1734523200, 0),
		},
		{
			name:     "milliseconds timestamp",
			input:    float64(1734523200000),
			expected: time.UnixMilli(1734523200000),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseExpiryField(tc.input)
			if !result.Equal(tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

func TestParseExpiresIn(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected int64
	}{
		{"nil", nil, 0},
		{"float64", float64(3600), 3600},
		{"int64", int64(7200), 7200},
		{"int", int(1800), 1800},
		{"string", "3600", 3600},
		{"empty string", "", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseExpiresIn(tc.input)
			if result != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, result)
			}
		})
	}
}

func TestExpiryInfoMethods(t *testing.T) {
	t.Run("TTL positive", func(t *testing.T) {
		info := &ExpiryInfo{ExpiresAt: time.Now().Add(time.Hour)}
		ttl := info.TTL()
		if ttl < 59*time.Minute || ttl > 61*time.Minute {
			t.Errorf("expected TTL around 1 hour, got %v", ttl)
		}
	})

	t.Run("TTL expired", func(t *testing.T) {
		info := &ExpiryInfo{ExpiresAt: time.Now().Add(-time.Hour)}
		if info.TTL() != 0 {
			t.Error("expected TTL 0 for expired token")
		}
	})

	t.Run("TTL unknown", func(t *testing.T) {
		info := &ExpiryInfo{}
		if info.TTL() != 0 {
			t.Error("expected TTL 0 for unknown expiry")
		}
	})

	t.Run("TTL nil", func(t *testing.T) {
		var info *ExpiryInfo
		if info.TTL() != 0 {
			t.Error("expected TTL 0 for nil")
		}
	})

	t.Run("IsExpired true", func(t *testing.T) {
		info := &ExpiryInfo{ExpiresAt: time.Now().Add(-time.Hour)}
		if !info.IsExpired() {
			t.Error("expected IsExpired to be true")
		}
	})

	t.Run("IsExpired false", func(t *testing.T) {
		info := &ExpiryInfo{ExpiresAt: time.Now().Add(time.Hour)}
		if info.IsExpired() {
			t.Error("expected IsExpired to be false")
		}
	})

	t.Run("IsExpired unknown", func(t *testing.T) {
		info := &ExpiryInfo{}
		if info.IsExpired() {
			t.Error("unknown expiry should not be treated as expired")
		}
	})

	t.Run("NeedsRefresh true", func(t *testing.T) {
		info := &ExpiryInfo{ExpiresAt: time.Now().Add(5 * time.Minute)}
		if !info.NeedsRefresh(10 * time.Minute) {
			t.Error("expected NeedsRefresh to be true")
		}
	})

	t.Run("NeedsRefresh false", func(t *testing.T) {
		info := &ExpiryInfo{ExpiresAt: time.Now().Add(time.Hour)}
		if info.NeedsRefresh(10 * time.Minute) {
			t.Error("expected NeedsRefresh to be false")
		}
	})

	t.Run("NeedsRefresh unknown", func(t *testing.T) {
		info := &ExpiryInfo{}
		if info.NeedsRefresh(10 * time.Minute) {
			t.Error("unknown expiry should not need refresh")
		}
	})
}

func TestParseCodexExpiry(t *testing.T) {
	// Create a temp directory to simulate codex home
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")

	// Write test auth file
	authData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expires_at": 1734523200,
		"token_type": "Bearer"
	}`
	if err := os.WriteFile(authPath, []byte(authData), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	info, err := ParseCodexExpiry(authPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Source != authPath {
		t.Errorf("expected source %s, got %s", authPath, info.Source)
	}
	if !info.HasRefreshToken {
		t.Error("expected HasRefreshToken to be true")
	}
	if info.ExpiresAt.IsZero() {
		t.Error("expected expiry to be set")
	}
}

func TestParseClaudeExpiry(t *testing.T) {
	// Create a temp directory structure
	tmpDir := t.TempDir()

	// Write .claude.json
	claudeJsonPath := filepath.Join(tmpDir, ".claude.json")
	claudeData := `{
		"accessToken": "test_access",
		"refreshToken": "test_refresh",
		"expiresAt": "2025-12-18T12:00:00Z"
	}`
	if err := os.WriteFile(claudeJsonPath, []byte(claudeData), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	info, err := ParseClaudeExpiry(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Source != claudeJsonPath {
		t.Errorf("expected source %s, got %s", claudeJsonPath, info.Source)
	}
	if !info.HasRefreshToken {
		t.Error("expected HasRefreshToken to be true")
	}
	if info.ExpiresAt.IsZero() {
		t.Error("expected expiry to be set")
	}
}

func TestParseGeminiExpiry(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()

	// Write settings.json
	settingsPath := filepath.Join(tmpDir, "settings.json")
	settingsData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expiry": "2025-12-18T14:00:00Z"
	}`
	if err := os.WriteFile(settingsPath, []byte(settingsData), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	info, err := ParseGeminiExpiry(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Source != settingsPath {
		t.Errorf("expected source %s, got %s", settingsPath, info.Source)
	}
	if !info.HasRefreshToken {
		t.Error("expected HasRefreshToken to be true")
	}
	if info.ExpiresAt.IsZero() {
		t.Error("expected expiry to be set")
	}
}

func TestErrNoAuthFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with non-existent directory
	_, err := ParseCodexExpiry(filepath.Join(tmpDir, "nonexistent", "auth.json"))
	if err != ErrNoAuthFile {
		t.Errorf("expected ErrNoAuthFile, got %v", err)
	}
}
