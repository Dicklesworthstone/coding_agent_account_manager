// Package health provides token expiry parsing for all supported providers.
//
// Each provider stores OAuth tokens differently. This file contains parsers
// that extract token expiration times from auth files.
package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrNoExpiry indicates that expiry information could not be determined.
var ErrNoExpiry = errors.New("expiry not found in auth file")

// ErrNoAuthFile indicates that the auth file does not exist.
var ErrNoAuthFile = errors.New("auth file not found")

// ExpiryInfo contains parsed token expiry information.
type ExpiryInfo struct {
	// ExpiresAt is when the token expires.
	ExpiresAt time.Time

	// HasRefreshToken indicates if a refresh token is available.
	HasRefreshToken bool

	// Source describes where the expiry was parsed from.
	Source string
}

// ParseClaudeExpiry extracts token expiry from Claude Code auth files.
//
// Claude Code stores auth in two locations:
//   - ~/.claude.json (main OAuth session state)
//   - ~/.config/claude-code/auth.json (auth credentials)
//
// Expected JSON structures (based on common OAuth patterns):
//
//	{
//	  "accessToken": "...",
//	  "refreshToken": "...",
//	  "expiresAt": "2025-12-17T18:00:00Z"  // ISO8601
//	}
//
// Or:
//
//	{
//	  "access_token": "...",
//	  "refresh_token": "...",
//	  "expires_at": 1734451200  // Unix timestamp
//	}
func ParseClaudeExpiry(authDir string) (*ExpiryInfo, error) {
	// Try ~/.claude.json first
	homeDir, _ := os.UserHomeDir()
	claudeJsonPath := filepath.Join(homeDir, ".claude.json")
	if authDir != "" {
		claudeJsonPath = filepath.Join(authDir, ".claude.json")
	}

	info, err := parseOAuthFile(claudeJsonPath)
	if err == nil {
		info.Source = claudeJsonPath
		return info, nil
	}

	// Try ~/.config/claude-code/auth.json
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(homeDir, ".config")
	}
	if authDir != "" {
		xdgConfig = authDir
	}
	authJsonPath := filepath.Join(xdgConfig, "claude-code", "auth.json")

	info, err = parseOAuthFile(authJsonPath)
	if err == nil {
		info.Source = authJsonPath
		return info, nil
	}

	// Check if any auth file exists at all
	if _, statErr := os.Stat(claudeJsonPath); os.IsNotExist(statErr) {
		if _, statErr2 := os.Stat(authJsonPath); os.IsNotExist(statErr2) {
			return nil, ErrNoAuthFile
		}
	}

	return nil, ErrNoExpiry
}

// ParseCodexExpiry extracts token expiry from Codex CLI auth file.
//
// Codex stores auth in $CODEX_HOME/auth.json (default ~/.codex/auth.json).
//
// Expected JSON structure:
//
//	{
//	  "access_token": "...",
//	  "refresh_token": "...",
//	  "expires_at": 1734451200,  // Unix timestamp (seconds)
//	  "token_type": "Bearer"
//	}
func ParseCodexExpiry(authPath string) (*ExpiryInfo, error) {
	if authPath == "" {
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			homeDir, _ := os.UserHomeDir()
			codexHome = filepath.Join(homeDir, ".codex")
		}
		authPath = filepath.Join(codexHome, "auth.json")
	}

	info, err := parseOAuthFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoAuthFile
		}
		return nil, err
	}

	info.Source = authPath
	return info, nil
}

// ParseGeminiExpiry extracts token expiry from Gemini CLI auth files.
//
// Gemini CLI stores auth in:
//   - ~/.gemini/settings.json
//   - ~/.gemini/oauth_credentials.json (optional)
//
// Note: Google OAuth tokens via ADC may not include expiry in the file itself.
// The expiry is typically short-lived and requires refresh.
func ParseGeminiExpiry(authDir string) (*ExpiryInfo, error) {
	if authDir == "" {
		geminiHome := os.Getenv("GEMINI_HOME")
		if geminiHome == "" {
			homeDir, _ := os.UserHomeDir()
			geminiHome = filepath.Join(homeDir, ".gemini")
		}
		authDir = geminiHome
	}

	// Try settings.json
	settingsPath := filepath.Join(authDir, "settings.json")
	info, err := parseOAuthFile(settingsPath)
	if err == nil {
		info.Source = settingsPath
		return info, nil
	}

	// Try oauth_credentials.json
	oauthPath := filepath.Join(authDir, "oauth_credentials.json")
	info, err = parseOAuthFile(oauthPath)
	if err == nil {
		info.Source = oauthPath
		return info, nil
	}

	// Try gcloud ADC format
	adcPath := filepath.Join(os.Getenv("HOME"), ".config", "gcloud", "application_default_credentials.json")
	info, err = parseADCFile(adcPath)
	if err == nil {
		info.Source = adcPath
		return info, nil
	}

	// Check if settings.json exists
	if _, statErr := os.Stat(settingsPath); os.IsNotExist(statErr) {
		return nil, ErrNoAuthFile
	}

	return nil, ErrNoExpiry
}

// oauthJSON represents common OAuth token response structures.
// Different providers use different field naming conventions.
type oauthJSON struct {
	// Snake case (common in OAuth specs)
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    any    `json:"expires_at"`
	ExpiresIn    any    `json:"expires_in"`

	// Camel case (some providers)
	AccessTokenCamel  string `json:"accessToken"`
	RefreshTokenCamel string `json:"refreshToken"`
	ExpiresAtCamel    any    `json:"expiresAt"`
	ExpiresInCamel    any    `json:"expiresIn"`

	// Other common fields
	Expiry     string `json:"expiry"`
	TokenType  string `json:"token_type"`
	IssuedAt   any    `json:"issued_at"`
	IssuedTime any    `json:"issuedTime"`
}

// parseOAuthFile reads an OAuth token file and extracts expiry info.
func parseOAuthFile(path string) (*ExpiryInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var oauth oauthJSON
	if err := json.Unmarshal(data, &oauth); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	info := &ExpiryInfo{
		HasRefreshToken: oauth.RefreshToken != "" || oauth.RefreshTokenCamel != "",
	}

	// Try to extract expiry from various fields
	if expiry := parseExpiryField(oauth.ExpiresAt); !expiry.IsZero() {
		info.ExpiresAt = expiry
		return info, nil
	}
	if expiry := parseExpiryField(oauth.ExpiresAtCamel); !expiry.IsZero() {
		info.ExpiresAt = expiry
		return info, nil
	}
	if expiry := parseExpiryField(oauth.Expiry); !expiry.IsZero() {
		info.ExpiresAt = expiry
		return info, nil
	}

	// Try expires_in with issued_at
	if expiresIn := parseExpiresIn(oauth.ExpiresIn); expiresIn > 0 {
		issuedAt := parseExpiryField(oauth.IssuedAt)
		if issuedAt.IsZero() {
			issuedAt = parseExpiryField(oauth.IssuedTime)
		}
		if !issuedAt.IsZero() {
			info.ExpiresAt = issuedAt.Add(time.Duration(expiresIn) * time.Second)
			return info, nil
		}
	}
	if expiresIn := parseExpiresIn(oauth.ExpiresInCamel); expiresIn > 0 {
		// Without issued_at, assume now (less accurate but better than nothing)
		// This is a fallback for tokens that only have expires_in
		info.ExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
		return info, nil
	}

	// If we have a refresh token but no expiry, that's still useful info
	if info.HasRefreshToken {
		return info, nil
	}

	return nil, ErrNoExpiry
}

// adcJSON represents Google Application Default Credentials format.
type adcJSON struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	Type         string `json:"type"`
}

// parseADCFile reads Google ADC credentials.
// ADC files don't contain expiry - they contain refresh tokens.
func parseADCFile(path string) (*ExpiryInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var adc adcJSON
	if err := json.Unmarshal(data, &adc); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	if adc.RefreshToken == "" {
		return nil, ErrNoExpiry
	}

	// ADC with refresh token = valid but unknown expiry
	return &ExpiryInfo{
		HasRefreshToken: true,
	}, nil
}

// parseExpiryField attempts to parse an expiry value from various formats.
func parseExpiryField(v any) time.Time {
	if v == nil {
		return time.Time{}
	}

	switch val := v.(type) {
	case string:
		// Try ISO8601 / RFC3339
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t
		}
		// Try RFC3339Nano
		if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
			return t
		}
		// Try common date formats
		formats := []string{
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
		}
		for _, format := range formats {
			if t, err := time.Parse(format, val); err == nil {
				return t
			}
		}

	case float64:
		// Unix timestamp (seconds or milliseconds)
		if val > 1e12 {
			// Likely milliseconds
			return time.UnixMilli(int64(val))
		}
		return time.Unix(int64(val), 0)

	case int64:
		if val > 1e12 {
			return time.UnixMilli(val)
		}
		return time.Unix(val, 0)

	case int:
		if val > 1e12 {
			return time.UnixMilli(int64(val))
		}
		return time.Unix(int64(val), 0)
	}

	return time.Time{}
}

// parseExpiresIn extracts seconds from an expires_in field.
func parseExpiresIn(v any) int64 {
	if v == nil {
		return 0
	}

	switch val := v.(type) {
	case float64:
		return int64(val)
	case int64:
		return val
	case int:
		return int64(val)
	case string:
		// Sometimes expires_in is a string number
		var n int64
		fmt.Sscanf(val, "%d", &n)
		return n
	}

	return 0
}

// ParseAllExpiry attempts to parse expiry for all providers and returns combined results.
func ParseAllExpiry() map[string]*ExpiryInfo {
	results := make(map[string]*ExpiryInfo)

	if info, err := ParseClaudeExpiry(""); err == nil {
		results["claude"] = info
	}
	if info, err := ParseCodexExpiry(""); err == nil {
		results["codex"] = info
	}
	if info, err := ParseGeminiExpiry(""); err == nil {
		results["gemini"] = info
	}

	return results
}

// TTL returns the time until expiry, or 0 if expired or unknown.
func (e *ExpiryInfo) TTL() time.Duration {
	if e == nil || e.ExpiresAt.IsZero() {
		return 0
	}
	ttl := time.Until(e.ExpiresAt)
	if ttl < 0 {
		return 0
	}
	return ttl
}

// IsExpired returns true if the token is expired.
func (e *ExpiryInfo) IsExpired() bool {
	if e == nil || e.ExpiresAt.IsZero() {
		return false // Unknown expiry is not treated as expired
	}
	return time.Now().After(e.ExpiresAt)
}

// NeedsRefresh returns true if the token should be refreshed.
// Default threshold is 10 minutes before expiry.
func (e *ExpiryInfo) NeedsRefresh(threshold time.Duration) bool {
	if threshold == 0 {
		threshold = 10 * time.Minute
	}
	if e == nil || e.ExpiresAt.IsZero() {
		return false // Unknown expiry - can't determine if refresh needed
	}
	return time.Until(e.ExpiresAt) < threshold
}
