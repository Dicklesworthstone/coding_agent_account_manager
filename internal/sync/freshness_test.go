package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCompareFreshness tests the CompareFreshness function.
func TestCompareFreshness(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Hour)
	later := now.Add(time.Hour)

	tests := []struct {
		name string
		a    *TokenFreshness
		b    *TokenFreshness
		want bool
	}{
		{
			name: "nil a returns false",
			a:    nil,
			b:    &TokenFreshness{ExpiresAt: now},
			want: false,
		},
		{
			name: "nil b returns true",
			a:    &TokenFreshness{ExpiresAt: now},
			b:    nil,
			want: true,
		},
		{
			name: "both nil returns false",
			a:    nil,
			b:    nil,
			want: false,
		},
		{
			name: "a expires later wins",
			a:    &TokenFreshness{ExpiresAt: later, ModifiedAt: earlier},
			b:    &TokenFreshness{ExpiresAt: now, ModifiedAt: later},
			want: true,
		},
		{
			name: "b expires later loses",
			a:    &TokenFreshness{ExpiresAt: now, ModifiedAt: later},
			b:    &TokenFreshness{ExpiresAt: later, ModifiedAt: earlier},
			want: false,
		},
		{
			name: "equal expiry uses modification time - a newer",
			a:    &TokenFreshness{ExpiresAt: now, ModifiedAt: later},
			b:    &TokenFreshness{ExpiresAt: now, ModifiedAt: earlier},
			want: true,
		},
		{
			name: "equal expiry uses modification time - b newer",
			a:    &TokenFreshness{ExpiresAt: now, ModifiedAt: earlier},
			b:    &TokenFreshness{ExpiresAt: now, ModifiedAt: later},
			want: false,
		},
		{
			name: "completely equal returns false",
			a:    &TokenFreshness{ExpiresAt: now, ModifiedAt: now},
			b:    &TokenFreshness{ExpiresAt: now, ModifiedAt: now},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareFreshness(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("CompareFreshness() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGetExtractor tests the GetExtractor function.
func TestGetExtractor(t *testing.T) {
	tests := []struct {
		provider string
		wantNil  bool
	}{
		{"claude", false},
		{"codex", false},
		{"gemini", false},
		{"unknown", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := GetExtractor(tt.provider)
			if tt.wantNil {
				if got != nil {
					t.Errorf("GetExtractor(%q) = %T, want nil", tt.provider, got)
				}
			} else {
				if got == nil {
					t.Errorf("GetExtractor(%q) = nil, want non-nil", tt.provider)
				}
			}
		})
	}
}

// TestClaudeFreshnessExtractor tests Claude token parsing.
func TestClaudeFreshnessExtractor(t *testing.T) {
	expiry := time.Date(2025, 12, 20, 14, 29, 0, 0, time.UTC)

	validClaudeToken := `{
		"oauthToken": {
			"access_token": "test-access-token",
			"refresh_token": "test-refresh-token",
			"token_type": "Bearer",
			"expiry": "2025-12-20T14:29:00Z"
		}
	}`

	tests := []struct {
		name      string
		authFiles map[string][]byte
		wantErr   bool
		checkFn   func(*testing.T, *TokenFreshness)
	}{
		{
			name: "valid token",
			authFiles: map[string][]byte{
				".claude.json": []byte(validClaudeToken),
			},
			wantErr: false,
			checkFn: func(t *testing.T, f *TokenFreshness) {
				if f.Provider != "claude" {
					t.Errorf("Provider = %q, want %q", f.Provider, "claude")
				}
				if f.Profile != "test-profile" {
					t.Errorf("Profile = %q, want %q", f.Profile, "test-profile")
				}
				if !f.ExpiresAt.Equal(expiry) {
					t.Errorf("ExpiresAt = %v, want %v", f.ExpiresAt, expiry)
				}
				if f.Source != "local" {
					t.Errorf("Source = %q, want %q", f.Source, "local")
				}
			},
		},
		{
			name: "valid token with full path",
			authFiles: map[string][]byte{
				"/home/user/.claude.json": []byte(validClaudeToken),
			},
			wantErr: false,
			checkFn: func(t *testing.T, f *TokenFreshness) {
				if !f.ExpiresAt.Equal(expiry) {
					t.Errorf("ExpiresAt = %v, want %v", f.ExpiresAt, expiry)
				}
			},
		},
		{
			name:      "no auth files",
			authFiles: map[string][]byte{},
			wantErr:   true,
		},
		{
			name: "wrong file name",
			authFiles: map[string][]byte{
				"auth.json": []byte(validClaudeToken),
			},
			wantErr: true,
		},
		{
			name: "invalid JSON",
			authFiles: map[string][]byte{
				".claude.json": []byte("not json"),
			},
			wantErr: true,
		},
		{
			name: "missing expiry",
			authFiles: map[string][]byte{
				".claude.json": []byte(`{"oauthToken": {"access_token": "test"}}`),
			},
			wantErr: true,
		},
	}

	extractor := &ClaudeFreshnessExtractor{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract("claude", "test-profile", tt.authFiles)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.checkFn != nil {
				tt.checkFn(t, got)
			}
		})
	}
}

// TestCodexFreshnessExtractor tests Codex token parsing.
func TestCodexFreshnessExtractor(t *testing.T) {
	// Unix timestamp for 2025-12-20T14:29:00Z
	expiresAt := int64(1766245740)
	expiryTime := time.Unix(expiresAt, 0)

	validCodexToken := `{
		"access_token": "test-access-token",
		"refresh_token": "test-refresh-token",
		"expires_at": 1766245740
	}`

	tests := []struct {
		name      string
		authFiles map[string][]byte
		wantErr   bool
		checkFn   func(*testing.T, *TokenFreshness)
	}{
		{
			name: "valid token",
			authFiles: map[string][]byte{
				"auth.json": []byte(validCodexToken),
			},
			wantErr: false,
			checkFn: func(t *testing.T, f *TokenFreshness) {
				if f.Provider != "codex" {
					t.Errorf("Provider = %q, want %q", f.Provider, "codex")
				}
				if !f.ExpiresAt.Equal(expiryTime) {
					t.Errorf("ExpiresAt = %v, want %v", f.ExpiresAt, expiryTime)
				}
			},
		},
		{
			name: "valid token with path",
			authFiles: map[string][]byte{
				"/home/user/.codex/auth.json": []byte(validCodexToken),
			},
			wantErr: false,
			checkFn: func(t *testing.T, f *TokenFreshness) {
				if !f.ExpiresAt.Equal(expiryTime) {
					t.Errorf("ExpiresAt = %v, want %v", f.ExpiresAt, expiryTime)
				}
			},
		},
		{
			name:      "no auth files",
			authFiles: map[string][]byte{},
			wantErr:   true,
		},
		{
			name: "wrong file name",
			authFiles: map[string][]byte{
				".claude.json": []byte(validCodexToken),
			},
			wantErr: true,
		},
		{
			name: "invalid JSON",
			authFiles: map[string][]byte{
				"auth.json": []byte("not json"),
			},
			wantErr: true,
		},
		{
			name: "missing expires_at",
			authFiles: map[string][]byte{
				"auth.json": []byte(`{"access_token": "test"}`),
			},
			wantErr: true,
		},
		{
			name: "zero expires_at",
			authFiles: map[string][]byte{
				"auth.json": []byte(`{"access_token": "test", "expires_at": 0}`),
			},
			wantErr: true,
		},
	}

	extractor := &CodexFreshnessExtractor{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract("codex", "test-profile", tt.authFiles)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.checkFn != nil {
				tt.checkFn(t, got)
			}
		})
	}
}

// TestGeminiFreshnessExtractor tests Gemini token parsing.
func TestGeminiFreshnessExtractor(t *testing.T) {
	expiry := time.Date(2025, 12, 20, 14, 29, 0, 0, time.UTC)

	validGeminiToken := `{
		"oauth_credentials": {
			"access_token": "test-access-token",
			"refresh_token": "test-refresh-token",
			"expiry": "2025-12-20T14:29:00Z"
		}
	}`

	tests := []struct {
		name      string
		authFiles map[string][]byte
		wantErr   bool
		checkFn   func(*testing.T, *TokenFreshness)
	}{
		{
			name: "valid token",
			authFiles: map[string][]byte{
				"settings.json": []byte(validGeminiToken),
			},
			wantErr: false,
			checkFn: func(t *testing.T, f *TokenFreshness) {
				if f.Provider != "gemini" {
					t.Errorf("Provider = %q, want %q", f.Provider, "gemini")
				}
				if !f.ExpiresAt.Equal(expiry) {
					t.Errorf("ExpiresAt = %v, want %v", f.ExpiresAt, expiry)
				}
			},
		},
		{
			name: "valid token with path",
			authFiles: map[string][]byte{
				"/home/user/.gemini/settings.json": []byte(validGeminiToken),
			},
			wantErr: false,
			checkFn: func(t *testing.T, f *TokenFreshness) {
				if !f.ExpiresAt.Equal(expiry) {
					t.Errorf("ExpiresAt = %v, want %v", f.ExpiresAt, expiry)
				}
			},
		},
		{
			name:      "no auth files",
			authFiles: map[string][]byte{},
			wantErr:   true,
		},
		{
			name: "wrong file name",
			authFiles: map[string][]byte{
				"auth.json": []byte(validGeminiToken),
			},
			wantErr: true,
		},
		{
			name: "invalid JSON",
			authFiles: map[string][]byte{
				"settings.json": []byte("not json"),
			},
			wantErr: true,
		},
		{
			name: "missing oauth_credentials",
			authFiles: map[string][]byte{
				"settings.json": []byte(`{"other_field": true}`),
			},
			wantErr: true,
		},
		{
			name: "missing expiry in oauth_credentials",
			authFiles: map[string][]byte{
				"settings.json": []byte(`{"oauth_credentials": {"access_token": "test"}}`),
			},
			wantErr: true,
		},
	}

	extractor := &GeminiFreshnessExtractor{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract("gemini", "test-profile", tt.authFiles)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if tt.checkFn != nil {
				tt.checkFn(t, got)
			}
		})
	}
}

// TestExtractFreshnessFromBytes tests the ExtractFreshnessFromBytes function.
func TestExtractFreshnessFromBytes(t *testing.T) {
	validClaudeToken := `{
		"oauthToken": {
			"access_token": "test",
			"expiry": "2025-12-20T14:29:00Z"
		}
	}`

	tests := []struct {
		name      string
		provider  string
		profile   string
		authFiles map[string][]byte
		wantErr   bool
	}{
		{
			name:     "valid claude token",
			provider: "claude",
			profile:  "test",
			authFiles: map[string][]byte{
				".claude.json": []byte(validClaudeToken),
			},
			wantErr: false,
		},
		{
			name:      "unknown provider",
			provider:  "unknown",
			profile:   "test",
			authFiles: map[string][]byte{},
			wantErr:   true,
		},
		{
			name:      "empty provider",
			provider:  "",
			profile:   "test",
			authFiles: map[string][]byte{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFreshnessFromBytes(tt.provider, tt.profile, tt.authFiles)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if got == nil {
				t.Error("Expected non-nil result")
			}
		})
	}
}

// TestExtractFreshnessFromFiles tests reading from disk.
func TestExtractFreshnessFromFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files
	claudeJSON := filepath.Join(tmpDir, ".claude.json")
	validToken := `{
		"oauthToken": {
			"access_token": "test",
			"expiry": "2025-12-20T14:29:00Z"
		}
	}`
	if err := os.WriteFile(claudeJSON, []byte(validToken), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	tests := []struct {
		name      string
		provider  string
		profile   string
		filePaths []string
		wantErr   bool
	}{
		{
			name:      "valid file",
			provider:  "claude",
			profile:   "test",
			filePaths: []string{claudeJSON},
			wantErr:   false,
		},
		{
			name:      "file not found - skipped",
			provider:  "claude",
			profile:   "test",
			filePaths: []string{filepath.Join(tmpDir, "nonexistent.json")},
			wantErr:   true, // No files found after skipping
		},
		{
			name:      "unknown provider",
			provider:  "unknown",
			profile:   "test",
			filePaths: []string{claudeJSON},
			wantErr:   true,
		},
		{
			name:      "empty file paths",
			provider:  "claude",
			profile:   "test",
			filePaths: []string{},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFreshnessFromFiles(tt.provider, tt.profile, tt.filePaths)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}
			if got == nil {
				t.Error("Expected non-nil result")
			}
		})
	}
}

// TestContainsPath tests the containsPath helper function.
func TestContainsPath(t *testing.T) {
	tests := []struct {
		path     string
		filename string
		want     bool
	}{
		// Positive cases - exact filename match
		{".claude.json", ".claude.json", true},
		{"/home/user/.claude.json", ".claude.json", true},
		{"auth.json", "auth.json", true},
		{"/path/to/auth.json", "auth.json", true},
		{"settings.json", "settings.json", true},
		{"/home/user/.gemini/settings.json", "settings.json", true},

		// Negative cases - should not match
		{".claude.json", "auth.json", false},
		{"auth.json", ".claude.json", false},
		{"", ".claude.json", false},
		{".claude.json", "", false},       // Empty filename should not match
		{"short", "longfilename", false},

		// False positive prevention - these should NOT match
		{"/path/to/auth.json.backup", "auth.json", false},  // Backup file
		{"/path/to/auth.json.tmp", "auth.json", false},     // Temp file
		{"/path/to/not.claude.json", ".claude.json", false}, // Different prefix
		{"/path/to/.claude.json.bak", ".claude.json", false}, // Backup suffix
	}

	for _, tt := range tests {
		t.Run(tt.path+"_"+tt.filename, func(t *testing.T) {
			got := containsPath(tt.path, tt.filename)
			if got != tt.want {
				t.Errorf("containsPath(%q, %q) = %v, want %v", tt.path, tt.filename, got, tt.want)
			}
		})
	}
}

// TestTokenFreshnessIsExpired tests the IsExpired field.
func TestTokenFreshnessIsExpired(t *testing.T) {
	extractor := &ClaudeFreshnessExtractor{}

	// Token that expires in the future
	futureToken := `{
		"oauthToken": {
			"access_token": "test",
			"expiry": "2099-12-20T14:29:00Z"
		}
	}`

	f, err := extractor.Extract("claude", "test", map[string][]byte{
		".claude.json": []byte(futureToken),
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if f.IsExpired {
		t.Error("Future token should not be expired")
	}

	// Token that expired in the past
	pastToken := `{
		"oauthToken": {
			"access_token": "test",
			"expiry": "2020-01-01T00:00:00Z"
		}
	}`

	f, err = extractor.Extract("claude", "test", map[string][]byte{
		".claude.json": []byte(pastToken),
	})
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if !f.IsExpired {
		t.Error("Past token should be expired")
	}
}

// TestProfileRef tests ProfileRef type.
func TestProfileRef(t *testing.T) {
	ref := ProfileRef{
		Provider: "claude",
		Profile:  "test@example.com",
	}

	if ref.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", ref.Provider, "claude")
	}
	if ref.Profile != "test@example.com" {
		t.Errorf("Profile = %q, want %q", ref.Profile, "test@example.com")
	}
}
