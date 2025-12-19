package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenFreshness represents the freshness of authentication tokens for a profile.
type TokenFreshness struct {
	// Provider is the auth provider (claude, codex, gemini).
	Provider string `json:"provider"`

	// Profile is the profile name.
	Profile string `json:"profile"`

	// ExpiresAt is when the token expires.
	ExpiresAt time.Time `json:"expires_at"`

	// ModifiedAt is when the auth file was last modified.
	ModifiedAt time.Time `json:"modified_at"`

	// IsExpired indicates if the token has already expired.
	IsExpired bool `json:"is_expired"`

	// Source is where this freshness came from ("local" or machine name).
	Source string `json:"source"`
}

// ProfileRef identifies a profile by provider and name.
type ProfileRef struct {
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
}

// FreshnessExtractor extracts token freshness from auth files.
type FreshnessExtractor interface {
	// Extract parses auth files and returns freshness information.
	// authFiles is a map of file paths to their contents.
	Extract(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error)
}

// CompareFreshness returns true if a is fresher than b.
// Primary criterion: later expiry time wins.
// Tiebreaker: later modification time wins.
func CompareFreshness(a, b *TokenFreshness) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}

	// Primary: later expiry wins
	if !a.ExpiresAt.Equal(b.ExpiresAt) {
		return a.ExpiresAt.After(b.ExpiresAt)
	}

	// Tiebreaker: later modification wins
	return a.ModifiedAt.After(b.ModifiedAt)
}

// GetExtractor returns the appropriate extractor for a provider.
func GetExtractor(provider string) FreshnessExtractor {
	switch provider {
	case "claude":
		return &ClaudeFreshnessExtractor{}
	case "codex":
		return &CodexFreshnessExtractor{}
	case "gemini":
		return &GeminiFreshnessExtractor{}
	default:
		return nil
	}
}

// ClaudeFreshnessExtractor extracts freshness from Claude auth files.
type ClaudeFreshnessExtractor struct{}

// claudeToken represents the structure of .claude.json
type claudeToken struct {
	OAuthToken struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		TokenType    string    `json:"token_type"`
		Expiry       time.Time `json:"expiry"`
	} `json:"oauthToken"`
}

// Extract implements FreshnessExtractor for Claude.
func (e *ClaudeFreshnessExtractor) Extract(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error) {
	// Claude tokens are in .claude.json
	var claudeData []byte
	var modTime time.Time

	for path, data := range authFiles {
		// Look for .claude.json file
		if containsPath(path, ".claude.json") {
			claudeData = data
			// Try to get mod time from file if it exists
			if info, err := os.Stat(path); err == nil {
				modTime = info.ModTime()
			}
			break
		}
	}

	if claudeData == nil {
		return nil, fmt.Errorf("no .claude.json found in auth files")
	}

	var token claudeToken
	if err := json.Unmarshal(claudeData, &token); err != nil {
		return nil, fmt.Errorf("parse .claude.json: %w", err)
	}

	if token.OAuthToken.Expiry.IsZero() {
		return nil, fmt.Errorf("no expiry in .claude.json")
	}

	now := time.Now()
	return &TokenFreshness{
		Provider:   provider,
		Profile:    profile,
		ExpiresAt:  token.OAuthToken.Expiry,
		ModifiedAt: modTime,
		IsExpired:  now.After(token.OAuthToken.Expiry),
		Source:     "local",
	}, nil
}

// CodexFreshnessExtractor extracts freshness from Codex auth files.
type CodexFreshnessExtractor struct{}

// codexToken represents the structure of auth.json for Codex
type codexToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp
}

// Extract implements FreshnessExtractor for Codex.
func (e *CodexFreshnessExtractor) Extract(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error) {
	// Codex tokens are in auth.json
	var authData []byte
	var modTime time.Time

	for path, data := range authFiles {
		// Look for auth.json file
		if containsPath(path, "auth.json") {
			authData = data
			// Try to get mod time from file if it exists
			if info, err := os.Stat(path); err == nil {
				modTime = info.ModTime()
			}
			break
		}
	}

	if authData == nil {
		return nil, fmt.Errorf("no auth.json found in auth files")
	}

	var token codexToken
	if err := json.Unmarshal(authData, &token); err != nil {
		return nil, fmt.Errorf("parse auth.json: %w", err)
	}

	if token.ExpiresAt == 0 {
		return nil, fmt.Errorf("no expires_at in auth.json")
	}

	expiresAt := time.Unix(token.ExpiresAt, 0)
	now := time.Now()

	return &TokenFreshness{
		Provider:   provider,
		Profile:    profile,
		ExpiresAt:  expiresAt,
		ModifiedAt: modTime,
		IsExpired:  now.After(expiresAt),
		Source:     "local",
	}, nil
}

// GeminiFreshnessExtractor extracts freshness from Gemini auth files.
type GeminiFreshnessExtractor struct{}

// geminiToken represents the structure of settings.json for Gemini
type geminiToken struct {
	OAuthCredentials struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		Expiry       time.Time `json:"expiry"`
	} `json:"oauth_credentials"`
}

// Extract implements FreshnessExtractor for Gemini.
func (e *GeminiFreshnessExtractor) Extract(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error) {
	// Gemini tokens are in settings.json
	var settingsData []byte
	var modTime time.Time

	for path, data := range authFiles {
		// Look for settings.json file
		if containsPath(path, "settings.json") {
			settingsData = data
			// Try to get mod time from file if it exists
			if info, err := os.Stat(path); err == nil {
				modTime = info.ModTime()
			}
			break
		}
	}

	if settingsData == nil {
		return nil, fmt.Errorf("no settings.json found in auth files")
	}

	var token geminiToken
	if err := json.Unmarshal(settingsData, &token); err != nil {
		return nil, fmt.Errorf("parse settings.json: %w", err)
	}

	if token.OAuthCredentials.Expiry.IsZero() {
		return nil, fmt.Errorf("no expiry in settings.json oauth_credentials")
	}

	now := time.Now()
	return &TokenFreshness{
		Provider:   provider,
		Profile:    profile,
		ExpiresAt:  token.OAuthCredentials.Expiry,
		ModifiedAt: modTime,
		IsExpired:  now.After(token.OAuthCredentials.Expiry),
		Source:     "local",
	}, nil
}

// containsPath checks if the path ends with the given filename.
// It properly handles path separators to avoid false positives like
// matching "auth.json.backup" when looking for "auth.json".
func containsPath(path, filename string) bool {
	if len(path) < len(filename) {
		return false
	}
	if filename == "" {
		return false
	}

	// Use filepath.Base to get the actual filename from the path
	base := filepath.Base(path)
	return base == filename
}

// ExtractFreshnessFromFiles reads auth files from disk and extracts freshness.
func ExtractFreshnessFromFiles(provider, profile string, filePaths []string) (*TokenFreshness, error) {
	extractor := GetExtractor(provider)
	if extractor == nil {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	authFiles := make(map[string][]byte)
	for _, path := range filePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Skip missing files
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		authFiles[path] = data
	}

	if len(authFiles) == 0 {
		return nil, fmt.Errorf("no auth files found for %s/%s", provider, profile)
	}

	return extractor.Extract(provider, profile, authFiles)
}

// ExtractFreshnessFromBytes extracts freshness from in-memory auth file data.
func ExtractFreshnessFromBytes(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error) {
	extractor := GetExtractor(provider)
	if extractor == nil {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	return extractor.Extract(provider, profile, authFiles)
}
