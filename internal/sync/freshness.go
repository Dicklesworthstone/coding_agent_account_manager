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

	// If either expiry is unknown, fall back to modification time.
	if a.ExpiresAt.IsZero() || b.ExpiresAt.IsZero() {
		return a.ModifiedAt.After(b.ModifiedAt)
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
	case "opencode", "cursor":
		return &GenericFreshnessExtractor{}
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

// claudeCredentials represents the structure of .credentials.json
type claudeCredentials struct {
	ClaudeAiOauth *claudeCredentialsOAuth `json:"claudeAiOauth"`
}

// claudeCredentialsOAuth represents OAuth data inside .credentials.json
type claudeCredentialsOAuth struct {
	ExpiresAt float64 `json:"expiresAt"` // Unix milliseconds (or seconds)
}

// Extract implements FreshnessExtractor for Claude.
func (e *ClaudeFreshnessExtractor) Extract(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error) {
	// Claude tokens are in .credentials.json (preferred) or legacy .claude.json
	var credentialsData []byte
	var credentialsModTime time.Time
	authPresent := false
	var claudeData []byte
	var modTime time.Time

	for path, data := range authFiles {
		if containsPath(path, "auth.json") {
			authPresent = true
		}
		// Look for .credentials.json first (preferred)
		if credentialsData == nil && containsPath(path, ".credentials.json") {
			credentialsData = data
			if info, err := os.Stat(path); err == nil {
				credentialsModTime = info.ModTime()
			}
			continue
		}
		// Look for legacy .claude.json file
		if claudeData == nil && containsPath(path, ".claude.json") {
			claudeData = data
			// Try to get mod time from file if it exists
			if info, err := os.Stat(path); err == nil {
				modTime = info.ModTime()
			}
		}
	}

	if credentialsData != nil {
		expiry, err := parseClaudeCredentialsExpiry(credentialsData)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		if !expiry.IsZero() {
			return &TokenFreshness{
				Provider:   provider,
				Profile:    profile,
				ExpiresAt:  expiry,
				ModifiedAt: credentialsModTime,
				IsExpired:  now.After(expiry),
				Source:     "local",
			}, nil
		}
		return &TokenFreshness{Provider: provider, Profile: profile, ModifiedAt: credentialsModTime, Source: "local"}, nil
	}
	if authPresent {
		return nil, fmt.Errorf("selected Claude auth.json has no supported expiry contract")
	}

	if claudeData == nil {
		return nil, fmt.Errorf("no .credentials.json or .claude.json found in auth files")
	}

	var token claudeToken
	if err := json.Unmarshal(claudeData, &token); err != nil {
		return nil, fmt.Errorf("parse .claude.json: %w", err)
	}

	now := time.Now()
	if token.OAuthToken.Expiry.IsZero() {
		return &TokenFreshness{
			Provider:   provider,
			Profile:    profile,
			ExpiresAt:  time.Time{},
			ModifiedAt: modTime,
			IsExpired:  false,
			Source:     "local",
		}, nil
	}
	return &TokenFreshness{
		Provider:   provider,
		Profile:    profile,
		ExpiresAt:  token.OAuthToken.Expiry,
		ModifiedAt: modTime,
		IsExpired:  now.After(token.OAuthToken.Expiry),
		Source:     "local",
	}, nil
}

func parseClaudeCredentialsExpiry(data []byte) (time.Time, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return time.Time{}, fmt.Errorf("parse .credentials.json: %w", err)
	}
	if creds.ClaudeAiOauth == nil {
		return time.Time{}, nil
	}
	if creds.ClaudeAiOauth.ExpiresAt <= 0 {
		return time.Time{}, nil
	}
	return unixTimeFromMaybeMillis(creds.ClaudeAiOauth.ExpiresAt), nil
}

func unixTimeFromMaybeMillis(value float64) time.Time {
	secs := int64(value)
	if secs <= 0 {
		return time.Time{}
	}
	if secs > 1_000_000_000_000 {
		return time.UnixMilli(secs)
	}
	return time.Unix(secs, 0)
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
		return &TokenFreshness{
			Provider:   provider,
			Profile:    profile,
			ExpiresAt:  time.Time{},
			ModifiedAt: modTime,
			IsExpired:  false,
			Source:     "local",
		}, nil
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

// geminiToken represents the structure of settings.json for Gemini.
// Some versions store tokens at the top level, others under oauth_credentials.
type geminiToken struct {
	OAuthCredentials *struct {
		AccessToken  string    `json:"access_token"`
		RefreshToken string    `json:"refresh_token"`
		Expiry       time.Time `json:"expiry"`
	} `json:"oauth_credentials"`

	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Expiry       time.Time `json:"expiry"`
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

	expiry := time.Time{}
	if token.OAuthCredentials != nil {
		expiry = token.OAuthCredentials.Expiry
	}
	if expiry.IsZero() {
		expiry = token.Expiry
	}
	if expiry.IsZero() {
		return &TokenFreshness{
			Provider:   provider,
			Profile:    profile,
			ExpiresAt:  time.Time{},
			ModifiedAt: modTime,
			IsExpired:  false,
			Source:     "local",
		}, nil
	}

	now := time.Now()
	return &TokenFreshness{
		Provider:   provider,
		Profile:    profile,
		ExpiresAt:  expiry,
		ModifiedAt: modTime,
		IsExpired:  now.After(expiry),
		Source:     "local",
	}, nil
}

// containsPath checks if the path ends with the given filename.
// It properly handles path separators to avoid false positives like
// matching "auth.json.backup" when looking for "auth.json".
// GenericFreshnessExtractor extracts freshness from generic auth files (OpenCode, Cursor).
// Since we don't know the token format, we use file modification time as the freshness signal.
type GenericFreshnessExtractor struct{}

// Extract implements FreshnessExtractor for generic providers.
func (e *GenericFreshnessExtractor) Extract(provider, profile string, authFiles map[string][]byte) (*TokenFreshness, error) {
	freshness := &TokenFreshness{
		Provider: provider,
		Profile:  profile,
	}

	// Use the latest modification time across all auth files
	for path := range authFiles {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(freshness.ModifiedAt) {
			freshness.ModifiedAt = info.ModTime()
		}
	}

	if freshness.ModifiedAt.IsZero() {
		return nil, fmt.Errorf("no valid auth files found")
	}

	return freshness, nil
}

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
