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
	"runtime"
	"strconv"
	"strings"
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
// Claude Code stores OAuth credentials in:
//   - ~/.claude/.credentials.json (primary - contains claudeAiOauth object)
//
// The credentials file structure:
//
//	{
//	  "claudeAiOauth": {
//	    "accessToken": "...",
//	    "refreshToken": "...",
//	    "expiresAt": 1768042451877,  // Unix milliseconds
//	    "rateLimitTier": "default_claude_max_20x",
//	    "subscriptionType": "max",
//	    "scopes": [...]
//	  }
//	}
func ParseClaudeExpiry(authDir string) (*ExpiryInfo, error) {
	homeDir, _ := os.UserHomeDir()

	if authDir == "" {
		var (
			info *ExpiryInfo
			err  error
		)

		var authCandidates []string
		if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
			authCandidates = append(authCandidates, filepath.Join(cfg, "auth.json"))
		}

		xdgConfig := os.Getenv("XDG_CONFIG_HOME")
		if xdgConfig == "" {
			xdgConfig = filepath.Join(homeDir, ".config")
		}
		authCandidates = append(authCandidates, filepath.Join(xdgConfig, "claude-code", "auth.json"))

		for _, candidate := range authCandidates {
			info, err = parseOAuthFile(candidate)
			if err == nil {
				info.Source = candidate
				return info, nil
			}
		}

		// System state probing - check the actual credentials file location
		credentialsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
		info, err = parseClaudeCredentialsFile(credentialsPath)
		if err == nil {
			info.Source = credentialsPath
			return info, nil
		}

		// Fallback to legacy locations for backwards compatibility
		claudeJsonPath := filepath.Join(homeDir, ".claude.json")
		info, err = parseOAuthFile(claudeJsonPath)
		if err == nil {
			info.Source = claudeJsonPath
			return info, nil
		}

		if _, statErr := os.Stat(credentialsPath); os.IsNotExist(statErr) {
			if _, statErr2 := os.Stat(claudeJsonPath); os.IsNotExist(statErr2) {
				missing := true
				for _, candidate := range authCandidates {
					if _, statErr3 := os.Stat(candidate); !os.IsNotExist(statErr3) {
						missing = false
						break
					}
				}
				if missing {
					return nil, ErrNoAuthFile
				}
			}
		}

		return nil, ErrNoExpiry
	}

	// Vault/profile probing - check for credentials file in vault
	credentialsPath := filepath.Join(authDir, ".credentials.json")
	info, err := parseClaudeCredentialsFile(credentialsPath)
	if err == nil {
		info.Source = credentialsPath
		return info, nil
	}

	// Fallback to legacy vault structure
	claudeJsonPath := filepath.Join(authDir, ".claude.json")
	info, err = parseOAuthFile(claudeJsonPath)
	if err == nil {
		info.Source = claudeJsonPath
		return info, nil
	}

	flatAuthPath := filepath.Join(authDir, "auth.json")
	info, err = parseOAuthFile(flatAuthPath)
	if err == nil {
		info.Source = flatAuthPath
		return info, nil
	}

	nestedAuthPath := filepath.Join(authDir, "claude-code", "auth.json")
	info, err = parseOAuthFile(nestedAuthPath)
	if err == nil {
		info.Source = nestedAuthPath
		return info, nil
	}

	if _, statErr := os.Stat(credentialsPath); os.IsNotExist(statErr) {
		if _, statErr2 := os.Stat(claudeJsonPath); os.IsNotExist(statErr2) {
			if _, statErr3 := os.Stat(flatAuthPath); os.IsNotExist(statErr3) {
				if _, statErr4 := os.Stat(nestedAuthPath); os.IsNotExist(statErr4) {
					return nil, ErrNoAuthFile
				}
			}
		}
	}

	return nil, ErrNoExpiry
}

// claudeCredentialsJSON represents the Claude Code credentials file structure.
type claudeCredentialsJSON struct {
	ClaudeAiOauth *claudeOAuthJSON `json:"claudeAiOauth"`
}

// claudeOAuthJSON represents the OAuth data within the credentials file.
type claudeOAuthJSON struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        float64  `json:"expiresAt"` // Unix milliseconds
	RateLimitTier    string   `json:"rateLimitTier"`
	SubscriptionType string   `json:"subscriptionType"`
	Scopes           []string `json:"scopes"`
}

// parseClaudeCredentialsFile parses the Claude Code credentials file format.
func parseClaudeCredentialsFile(path string) (*ExpiryInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var creds claudeCredentialsJSON
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	if creds.ClaudeAiOauth == nil {
		return nil, ErrNoExpiry
	}

	oauth := creds.ClaudeAiOauth
	info := &ExpiryInfo{
		HasRefreshToken: oauth.RefreshToken != "",
	}

	// Parse expiresAt (Unix milliseconds)
	if oauth.ExpiresAt > 0 {
		info.ExpiresAt = time.UnixMilli(int64(oauth.ExpiresAt))
	}

	// If we have expiry info, return successfully
	if !info.ExpiresAt.IsZero() || info.HasRefreshToken {
		return info, nil
	}

	// If we have an access token but no expiry, still return valid
	if oauth.AccessToken != "" {
		return info, nil
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
//   - ~/.gemini/oauth_creds.json (optional)
//
// Note: Google OAuth tokens via ADC may not include expiry in the file itself.
// The expiry is typically short-lived and requires refresh.
func ParseGeminiExpiry(authDir string) (*ExpiryInfo, error) {
	checkSystem := false
	if authDir == "" {
		checkSystem = true
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

	// Try oauth_creds.json
	oauthPath := filepath.Join(authDir, "oauth_creds.json")
	info, err = parseOAuthFile(oauthPath)
	if err == nil {
		info.Source = oauthPath
		return info, nil
	}

	// Try gcloud ADC format (only if checking system state)
	adcPath := ""
	if checkSystem {
		adcPath = getADCPath()
		info, err = parseADCFile(adcPath)
		if err == nil {
			info.Source = adcPath
			return info, nil
		}
	}

	// Report ErrNoAuthFile only when *none* of the supported auth files exist.
	_, settingsErr := os.Stat(settingsPath)
	_, oauthErr := os.Stat(oauthPath)
	adcExists := false
	if checkSystem && adcPath != "" {
		if _, err := os.Stat(adcPath); err == nil {
			adcExists = true
		}
	}
	if os.IsNotExist(settingsErr) && os.IsNotExist(oauthErr) && !adcExists {
		return nil, ErrNoAuthFile
	}

	return nil, ErrNoExpiry
}

func getADCPath() string {
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		return path
	}

	if configDir := os.Getenv("CLOUDSDK_CONFIG"); configDir != "" {
		return filepath.Join(configDir, "application_default_credentials.json")
	}

	// Windows support
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "gcloud", "application_default_credentials.json")
		}
	}

	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
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
		if issuedAt.IsZero() {
			// If issued_at is missing, assume now (common for OAuth tokens).
			issuedAt = time.Now()
		}
		info.ExpiresAt = issuedAt.Add(time.Duration(expiresIn) * time.Second)
		return info, nil
	}
	if expiresIn := parseExpiresIn(oauth.ExpiresInCamel); expiresIn > 0 {
		issuedAt := parseExpiryField(oauth.IssuedAt)
		if issuedAt.IsZero() {
			issuedAt = parseExpiryField(oauth.IssuedTime)
		}
		if issuedAt.IsZero() {
			// Without issued_at, assume now (less accurate but better than nothing)
			issuedAt = time.Now()
		}
		info.ExpiresAt = issuedAt.Add(time.Duration(expiresIn) * time.Second)
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
		val = strings.TrimSpace(val)
		if val == "" {
			return time.Time{}
		}
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

		// Try numeric strings (unix seconds or milliseconds).
		if isNumericString(val) {
			if num, err := strconv.ParseFloat(val, 64); err == nil {
				if num > 1e12 {
					return time.UnixMilli(int64(num))
				}
				return time.Unix(int64(num), 0)
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

func isNumericString(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for i, r := range s {
		if r >= '0' && r <= '9' {
			hasDigit = true
			continue
		}
		if r == '.' {
			continue
		}
		if (r == '-' || r == '+') && i == 0 {
			continue
		}
		return false
	}
	return hasDigit
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
