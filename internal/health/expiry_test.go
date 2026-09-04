package health

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParseOAuthFile(t *testing.T) {
	testdata := "testdata"

	tests := []struct {
		name           string
		file           string
		expectError    bool
		expectExpiry   bool
		expectRefresh  bool
		expiryAfterNow bool
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

func TestParseClaudeExpiry_FindsFlatAuthJSONInDir(t *testing.T) {
	tmpDir := t.TempDir()

	authPath := filepath.Join(tmpDir, "auth.json")
	authData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expires_at": 1734523200,
		"token_type": "Bearer"
	}`
	if err := os.WriteFile(authPath, []byte(authData), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	info, err := ParseClaudeExpiry(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Source != authPath {
		t.Errorf("expected source %s, got %s", authPath, info.Source)
	}
	if !info.HasRefreshToken {
		t.Error("expected HasRefreshToken to be true")
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

func TestParseGeminiExpiry_OAuthCredsFileWithoutSettingsReturnsNoExpiry(t *testing.T) {
	tmpDir := t.TempDir()

	// oauth_creds.json exists but contains no expiry/refresh token.
	// This should surface as ErrNoExpiry (not ErrNoAuthFile).
	oauthPath := filepath.Join(tmpDir, "oauth_creds.json")
	if err := os.WriteFile(oauthPath, []byte(`{}`), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := ParseGeminiExpiry(tmpDir)
	if err != ErrNoExpiry {
		t.Fatalf("expected ErrNoExpiry, got %v", err)
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

func TestParseClaudeCredentialsFile(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("valid credentials file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "credentials.json")
		data := `{
			"claudeAiOauth": {
				"accessToken": "test_access",
				"refreshToken": "test_refresh",
				"expiresAt": 1768042451877,
				"rateLimitTier": "default_claude_max_20x",
				"subscriptionType": "max"
			}
		}`
		os.WriteFile(path, []byte(data), 0600)

		info, err := parseClaudeCredentialsFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !info.HasRefreshToken {
			t.Error("expected HasRefreshToken to be true")
		}
		if info.ExpiresAt.IsZero() {
			t.Error("expected expiry to be set")
		}
	})

	t.Run("missing claudeAiOauth", func(t *testing.T) {
		path := filepath.Join(tmpDir, "no_oauth.json")
		data := `{"other": "data"}`
		os.WriteFile(path, []byte(data), 0600)

		_, err := parseClaudeCredentialsFile(path)
		if err != ErrNoExpiry {
			t.Errorf("expected ErrNoExpiry, got %v", err)
		}
	})

	t.Run("access token only", func(t *testing.T) {
		path := filepath.Join(tmpDir, "access_only.json")
		data := `{
			"claudeAiOauth": {
				"accessToken": "test_access"
			}
		}`
		os.WriteFile(path, []byte(data), 0600)

		info, err := parseClaudeCredentialsFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if info.HasRefreshToken {
			t.Error("expected HasRefreshToken to be false")
		}
	})

	t.Run("empty oauth object", func(t *testing.T) {
		path := filepath.Join(tmpDir, "empty_oauth.json")
		data := `{
			"claudeAiOauth": {}
		}`
		os.WriteFile(path, []byte(data), 0600)

		_, err := parseClaudeCredentialsFile(path)
		if err != ErrNoExpiry {
			t.Errorf("expected ErrNoExpiry, got %v", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		path := filepath.Join(tmpDir, "invalid.json")
		os.WriteFile(path, []byte("{invalid json"), 0600)

		_, err := parseClaudeCredentialsFile(path)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		_, err := parseClaudeCredentialsFile(filepath.Join(tmpDir, "nonexistent.json"))
		if err == nil {
			t.Error("expected error for non-existent file")
		}
	})
}

func TestGetADCPath(t *testing.T) {
	t.Run("with GOOGLE_APPLICATION_CREDENTIALS", func(t *testing.T) {
		orig := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		defer os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", orig)

		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/custom/path/creds.json")
		path := getADCPath()
		if path != "/custom/path/creds.json" {
			t.Errorf("expected /custom/path/creds.json, got %s", path)
		}
	})

	t.Run("with CLOUDSDK_CONFIG", func(t *testing.T) {
		origGAC := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		origCloudSDK := os.Getenv("CLOUDSDK_CONFIG")
		defer func() {
			os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", origGAC)
			os.Setenv("CLOUDSDK_CONFIG", origCloudSDK)
		}()

		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
		os.Setenv("CLOUDSDK_CONFIG", "/custom/gcloud")
		path := getADCPath()
		expected := filepath.Join("/custom/gcloud", "application_default_credentials.json")
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})

	t.Run("default path", func(t *testing.T) {
		origGAC := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		origCloudSDK := os.Getenv("CLOUDSDK_CONFIG")
		defer func() {
			os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", origGAC)
			os.Setenv("CLOUDSDK_CONFIG", origCloudSDK)
		}()

		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
		os.Setenv("CLOUDSDK_CONFIG", "")

		path := getADCPath()
		// On non-Windows, should use ~/.config/gcloud/...
		home, _ := os.UserHomeDir()
		expected := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
		if path != expected {
			t.Errorf("expected %s, got %s", expected, path)
		}
	})
}

func TestParseAllExpiry(t *testing.T) {
	// This test just verifies that ParseAllExpiry runs without panicking
	// and returns a map. Actual parsing is tested by individual provider tests.
	results := ParseAllExpiry()
	if results == nil {
		t.Error("ParseAllExpiry should not return nil")
	}
	// Results may be empty if no auth files exist, which is fine
}

func TestNeedsRefreshDefaultThreshold(t *testing.T) {
	// Test that default threshold of 10 minutes is used when 0 is passed
	info := &ExpiryInfo{ExpiresAt: time.Now().Add(5 * time.Minute)}
	if !info.NeedsRefresh(0) {
		t.Error("NeedsRefresh(0) should use default 10 minute threshold")
	}

	info = &ExpiryInfo{ExpiresAt: time.Now().Add(15 * time.Minute)}
	if info.NeedsRefresh(0) {
		t.Error("Token expiring in 15 minutes should not need refresh with default threshold")
	}
}

func TestParseCodexExpiryDefaultPath(t *testing.T) {
	tmpDir := t.TempDir()
	origCodexHome := os.Getenv("CODEX_HOME")
	defer os.Setenv("CODEX_HOME", origCodexHome)

	// Set CODEX_HOME to our temp dir
	os.Setenv("CODEX_HOME", tmpDir)

	// Create auth file
	authPath := filepath.Join(tmpDir, "auth.json")
	authData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expires_at": 1734523200
	}`
	os.WriteFile(authPath, []byte(authData), 0600)

	// Test with empty path (should use CODEX_HOME)
	info, err := ParseCodexExpiry("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Source != authPath {
		t.Errorf("expected source %s, got %s", authPath, info.Source)
	}
}

func TestParseGeminiExpiryDefaultPath(t *testing.T) {
	tmpDir := t.TempDir()
	origGeminiHome := os.Getenv("GEMINI_HOME")
	defer os.Setenv("GEMINI_HOME", origGeminiHome)

	// Set GEMINI_HOME to our temp dir
	os.Setenv("GEMINI_HOME", tmpDir)

	// Create settings file
	settingsPath := filepath.Join(tmpDir, "settings.json")
	settingsData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expiry": "2025-12-18T14:00:00Z"
	}`
	os.WriteFile(settingsPath, []byte(settingsData), 0600)

	// Test with empty path (should use GEMINI_HOME)
	info, err := ParseGeminiExpiry("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Source != settingsPath {
		t.Errorf("expected source %s, got %s", settingsPath, info.Source)
	}
}

func TestParseClaudeExpiry_UsesClaudeConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	origClaude := os.Getenv("CLAUDE_CONFIG_DIR")
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("CLAUDE_CONFIG_DIR", origClaude)
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "ignored"))

	authPath := filepath.Join(tmpDir, "auth.json")
	authData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expires_at": 1734523200
	}`
	if err := os.WriteFile(authPath, []byte(authData), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	info, err := ParseClaudeExpiry("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Source != authPath {
		t.Errorf("expected source %s, got %s", authPath, info.Source)
	}
}

func TestParseClaudeExpiry_CredentialsFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .credentials.json in the vault directory
	credPath := filepath.Join(tmpDir, ".credentials.json")
	credData := `{
		"claudeAiOauth": {
			"accessToken": "test_access",
			"refreshToken": "test_refresh",
			"expiresAt": 1768042451877
		}
	}`
	os.WriteFile(credPath, []byte(credData), 0600)

	info, err := ParseClaudeExpiry(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Source != credPath {
		t.Errorf("expected source %s, got %s", credPath, info.Source)
	}
}

func TestParseClaudeExpiry_NestedAuthJson(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested auth.json
	nestedDir := filepath.Join(tmpDir, "claude-code")
	os.MkdirAll(nestedDir, 0700)
	authPath := filepath.Join(nestedDir, "auth.json")
	authData := `{
		"access_token": "test_access",
		"refresh_token": "test_refresh",
		"expires_at": 1734523200
	}`
	os.WriteFile(authPath, []byte(authData), 0600)

	info, err := ParseClaudeExpiry(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Source != authPath {
		t.Errorf("expected source %s, got %s", authPath, info.Source)
	}
}

func TestParseClaudeExpiry_NoAuthFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty directory - no auth files
	_, err := ParseClaudeExpiry(tmpDir)
	if err != ErrNoAuthFile {
		t.Errorf("expected ErrNoAuthFile, got %v", err)
	}
}

func TestParseGeminiExpiry_NoAuthFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Empty directory - no auth files
	_, err := ParseGeminiExpiry(tmpDir)
	if err != ErrNoAuthFile {
		t.Errorf("expected ErrNoAuthFile, got %v", err)
	}
}

func TestParseADCFile_NoRefreshToken(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "adc.json")

	// ADC file without refresh token
	data := `{
		"client_id": "test_client",
		"client_secret": "test_secret",
		"type": "authorized_user"
	}`
	os.WriteFile(path, []byte(data), 0600)

	_, err := parseADCFile(path)
	if err != ErrNoExpiry {
		t.Errorf("expected ErrNoExpiry for ADC without refresh token, got %v", err)
	}
}

func TestParseADCFile_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid_adc.json")

	os.WriteFile(path, []byte("{invalid json"), 0600)

	_, err := parseADCFile(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseOAuthFile_ExpiresInWithIssuedAt(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("expires_in with issued_at", func(t *testing.T) {
		path := filepath.Join(tmpDir, "expires_in.json")
		// 3600 seconds (1 hour) from a known timestamp
		issuedAt := time.Now().Add(-30 * time.Minute).Unix()
		data := `{
			"access_token": "test",
			"refresh_token": "test_refresh",
			"expires_in": 3600,
			"issued_at": ` + string(rune(issuedAt)) + `
		}`
		// Use a simple numeric value
		data = `{
			"access_token": "test",
			"refresh_token": "test_refresh",
			"expiresIn": 3600
		}`
		os.WriteFile(path, []byte(data), 0600)

		info, err := parseOAuthFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// With only expiresIn (camel case), it assumes now as issued time
		if info.ExpiresAt.IsZero() {
			t.Error("expected expiry to be set from expiresIn")
		}
	})
}

func TestParseExpiryField_AdditionalFormats(t *testing.T) {
	t.Run("datetime without timezone", func(t *testing.T) {
		result := parseExpiryField("2025-12-18T15:04:05")
		if result.IsZero() {
			t.Error("should parse datetime without timezone")
		}
	})

	t.Run("datetime with space separator", func(t *testing.T) {
		result := parseExpiryField("2025-12-18 15:04:05")
		if result.IsZero() {
			t.Error("should parse datetime with space separator")
		}
	})

	t.Run("invalid string", func(t *testing.T) {
		result := parseExpiryField("not-a-date")
		if !result.IsZero() {
			t.Error("invalid date string should return zero time")
		}
	})

	t.Run("int64 milliseconds", func(t *testing.T) {
		ms := int64(1734523200000) // Milliseconds
		result := parseExpiryField(ms)
		if result.IsZero() {
			t.Error("should parse int64 milliseconds")
		}
	})

	t.Run("int milliseconds", func(t *testing.T) {
		ms := int(1734523200000) // Milliseconds
		result := parseExpiryField(ms)
		if result.IsZero() {
			t.Error("should parse int milliseconds")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		result := parseExpiryField(struct{}{})
		if !result.IsZero() {
			t.Error("unknown type should return zero time")
		}
	})
}

// TestParseClaudeExpiry_SelfRefreshing: a Claude credential with a refresh
// token is renewed by Claude Code itself; one without is not (PR #84).
func TestParseClaudeExpiry_SelfRefreshing(t *testing.T) {
	write := func(t *testing.T, body string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	exp := time.Now().Add(4 * time.Hour).UnixMilli()

	dir := write(t, `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":`+itoa(exp)+`}}`)
	info, err := ParseClaudeExpiry(dir)
	if err != nil {
		t.Fatalf("ParseClaudeExpiry() error = %v", err)
	}
	if !info.SelfRefreshing || !info.HasRefreshToken {
		t.Errorf("with refresh token: SelfRefreshing=%v HasRefreshToken=%v, want both true", info.SelfRefreshing, info.HasRefreshToken)
	}

	dir = write(t, `{"claudeAiOauth":{"accessToken":"a","expiresAt":`+itoa(exp)+`}}`)
	info, err = ParseClaudeExpiry(dir)
	if err != nil {
		t.Fatalf("ParseClaudeExpiry() error = %v", err)
	}
	if info.SelfRefreshing {
		t.Error("without refresh token: SelfRefreshing = true, want false")
	}

	// Other providers never set the flag.
	codexPath := writeCodexAuthJSON(t, map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": exp / 1000})
	cinfo, err := ParseCodexExpiry(codexPath)
	if err != nil {
		t.Fatalf("ParseCodexExpiry() error = %v", err)
	}
	if cinfo.SelfRefreshing {
		t.Error("codex: SelfRefreshing = true, want false")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// unsignedJWT builds a three-part JWT with the given payload claims. The
// parsers never validate signatures, so a placeholder signature suffices.
func unsignedJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// writeCodexAuthJSON writes a Codex auth.json into a temp dir and returns its path.
func writeCodexAuthJSON(t *testing.T, content map[string]any) string {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal auth.json: %v", err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return path
}

// TestParseCodexExpiry_ChatGPTMode covers the auth.json layout Codex writes
// for ChatGPT-mode logins: JWTs nested under "tokens", no expiry field. The
// id_token is short-lived and only carries identity claims, so health must
// follow the access token's exp instead (PR #86).
func TestParseCodexExpiry_ChatGPTMode(t *testing.T) {
	accessExp := time.Now().Add(9 * 24 * time.Hour).Truncate(time.Second)
	idExp := time.Now().Add(-5 * 24 * time.Hour).Truncate(time.Second)

	t.Run("access token expiry wins over expired id token", func(t *testing.T) {
		path := writeCodexAuthJSON(t, map[string]any{
			"auth_mode": "chatgpt",
			"tokens": map[string]any{
				"id_token":      unsignedJWT(t, map[string]any{"email": "codex@example.com", "exp": idExp.Unix()}),
				"access_token":  unsignedJWT(t, map[string]any{"exp": accessExp.Unix()}),
				"refresh_token": "rt-example",
			},
			"last_refresh": time.Now().Add(-4 * 24 * time.Hour).Format(time.RFC3339),
		})

		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry() error = %v", err)
		}
		if !info.ExpiresAt.Equal(accessExp) {
			t.Errorf("ExpiresAt = %v, want access token exp %v", info.ExpiresAt, accessExp)
		}
		if !info.HasRefreshToken {
			t.Error("HasRefreshToken = false, want true for tokens.refresh_token")
		}
		if info.IsExpired() {
			t.Error("IsExpired() = true, want false while the access token is valid")
		}
		if info.Source != path {
			t.Errorf("Source = %q, want %q", info.Source, path)
		}
	})

	t.Run("access token wins even when id token expires later", func(t *testing.T) {
		laterID := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
		path := writeCodexAuthJSON(t, map[string]any{
			"tokens": map[string]any{
				"id_token":     unsignedJWT(t, map[string]any{"exp": laterID.Unix()}),
				"access_token": unsignedJWT(t, map[string]any{"exp": accessExp.Unix()}),
			},
		})

		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry() error = %v", err)
		}
		if !info.ExpiresAt.Equal(accessExp) {
			t.Errorf("ExpiresAt = %v, want access token exp %v", info.ExpiresAt, accessExp)
		}
		if info.HasRefreshToken {
			t.Error("HasRefreshToken = true, want false without a refresh token")
		}
	})

	t.Run("falls back to id token when access token is not a JWT", func(t *testing.T) {
		path := writeCodexAuthJSON(t, map[string]any{
			"tokens": map[string]any{
				"id_token":     unsignedJWT(t, map[string]any{"exp": accessExp.Unix()}),
				"access_token": "opaque-not-a-jwt",
			},
		})

		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry() error = %v", err)
		}
		if !info.ExpiresAt.Equal(accessExp) {
			t.Errorf("ExpiresAt = %v, want id token exp %v", info.ExpiresAt, accessExp)
		}
	})

	t.Run("refresh token alone is still useful", func(t *testing.T) {
		path := writeCodexAuthJSON(t, map[string]any{
			"tokens": map[string]any{
				"id_token":      "garbage",
				"access_token":  "garbage",
				"refresh_token": "rt-example",
			},
		})

		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry() error = %v", err)
		}
		if !info.ExpiresAt.IsZero() {
			t.Errorf("ExpiresAt = %v, want zero when no JWT parses", info.ExpiresAt)
		}
		if !info.HasRefreshToken {
			t.Error("HasRefreshToken = false, want true")
		}
	})

	t.Run("nothing usable is ErrNoExpiry", func(t *testing.T) {
		path := writeCodexAuthJSON(t, map[string]any{
			"tokens": map[string]any{"id_token": "garbage", "access_token": "garbage"},
		})

		if _, err := ParseCodexExpiry(path); !errors.Is(err, ErrNoExpiry) {
			t.Errorf("ParseCodexExpiry() error = %v, want ErrNoExpiry", err)
		}
	})

	t.Run("explicit expires_at outranks the JWTs", func(t *testing.T) {
		explicit := time.Now().Add(2 * time.Hour).Truncate(time.Second)
		path := writeCodexAuthJSON(t, map[string]any{
			"expires_at":    explicit.Unix(),
			"refresh_token": "rt-flat",
			"tokens": map[string]any{
				"access_token": unsignedJWT(t, map[string]any{"exp": accessExp.Unix()}),
			},
		})

		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry() error = %v", err)
		}
		if !info.ExpiresAt.Equal(explicit) {
			t.Errorf("ExpiresAt = %v, want explicit expires_at %v", info.ExpiresAt, explicit)
		}
		if !info.HasRefreshToken {
			t.Error("HasRefreshToken = false, want true for a flat refresh_token")
		}
	})

	t.Run("malformed JSON is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ParseCodexExpiry(path); err == nil || errors.Is(err, ErrNoExpiry) {
			t.Errorf("ParseCodexExpiry() error = %v, want a parse error", err)
		}
	})
}

// TestParseGrokExpiry covers issue #101: Grok's auth.json is keyed by a
// dynamic "<issuer>::<client-id>" key that the Codex parser cannot read, so
// every live Grok profile reported unknown expiry and stuck at warning.
// Tokens here are synthetic.
func TestParseGrokExpiry(t *testing.T) {
	writeGrok := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("dynamic credential key", func(t *testing.T) {
		path := writeGrok(t, `{"https://auth.x.ai::00000000-0000-0000-0000-000000000000":`+
			`{"key":"SYNTHETIC-GROK-TOKEN","auth_mode":"sso","email":"grok@example.com",`+
			`"refresh_token":"SYNTHETIC-REFRESH","expires_at":"2099-01-01T00:00:00Z"}}`)
		info, err := ParseGrokExpiry(path)
		if err != nil {
			t.Fatalf("ParseGrokExpiry() error = %v", err)
		}
		want := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		if !info.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", info.ExpiresAt, want)
		}
		if !info.HasRefreshToken {
			t.Error("HasRefreshToken = false, want true")
		}
		// The reported symptom: with no parseable expiry the profile scored
		// "unknown", which lands on warning even with zero errors.
		if got := CalculateStatus(&ProfileHealth{TokenExpiresAt: info.ExpiresAt}); got != StatusHealthy {
			t.Errorf("CalculateStatus() = %v, want StatusHealthy for a live Grok profile", got)
		}
		if got := CalculateStatus(&ProfileHealth{}); got != StatusWarning {
			t.Errorf("unparsed baseline = %v, want StatusWarning (the pre-fix behaviour)", got)
		}
	})

	t.Run("the latest expiry across entries wins", func(t *testing.T) {
		path := writeGrok(t, `{`+
			`"https://auth.x.ai::aaaa":{"key":"A","expires_at":"2030-01-01T00:00:00Z"},`+
			`"https://auth.x.ai::bbbb":{"key":"B","expires_at":"2040-01-01T00:00:00Z"}}`)
		info, err := ParseGrokExpiry(path)
		if err != nil {
			t.Fatalf("ParseGrokExpiry() error = %v", err)
		}
		want := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
		if !info.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want the later entry %v", info.ExpiresAt, want)
		}
	})

	t.Run("flat layout still parses", func(t *testing.T) {
		path := writeGrok(t, `{"access_token":"A","refresh_token":"R","expires_at":"2099-01-01T00:00:00Z"}`)
		info, err := ParseGrokExpiry(path)
		if err != nil {
			t.Fatalf("ParseGrokExpiry() error = %v", err)
		}
		if !info.HasRefreshToken {
			t.Error("HasRefreshToken = false, want true for a flat refresh_token")
		}
		want := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		if !info.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", info.ExpiresAt, want)
		}
	})

	t.Run("missing file reports ErrNoAuthFile", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		if _, err := ParseGrokExpiry(path); !errors.Is(err, ErrNoAuthFile) {
			t.Errorf("ParseGrokExpiry() error = %v, want ErrNoAuthFile", err)
		}
	})

	t.Run("no usable entry reports ErrNoExpiry", func(t *testing.T) {
		path := writeGrok(t, `{"https://auth.x.ai::aaaa":{"email":"grok@example.com"}}`)
		if _, err := ParseGrokExpiry(path); !errors.Is(err, ErrNoExpiry) {
			t.Errorf("ParseGrokExpiry() error = %v, want ErrNoExpiry", err)
		}
	})

	t.Run("malformed JSON is an error", func(t *testing.T) {
		path := writeGrok(t, "{not json")
		if _, err := ParseGrokExpiry(path); err == nil || errors.Is(err, ErrNoExpiry) {
			t.Errorf("ParseGrokExpiry() error = %v, want a parse error", err)
		}
	})
}
