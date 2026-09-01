package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Claude Constants
//
// IMPORTANT: These are SPECULATIVE values. The OAuth refresh endpoint is
// undocumented and may not exist or work as expected. Claude Code handles
// token refresh internally; attempting external refresh may cause auth
// corruption or undefined behavior.
//
// Claude token refresh is DISABLED in RefreshProfile(). Users should
// re-authenticate via the /login command when tokens expire.
//
// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-006)
var (
	ClaudeTokenURL = "https://api.anthropic.com/oauth/token" // SPECULATIVE - do not use
	ClaudeClientID = "claude-code-cli"                       // SPECULATIVE - do not use

	// ClaudeRefreshDisabled indicates that Claude token refresh is disabled.
	// Callers can check this before attempting refresh operations.
	ClaudeRefreshDisabled = true
)

// SelfRefreshing reports whether the provider's own CLI renews its OAuth
// access token in place, from the refresh token stored beside it.
//
// Claude Code does: it refreshes on next use, and caam's own Claude refresh is
// disabled (see ClaudeRefreshDisabled and refreshClaude), so "caam refresh
// claude <profile>" always fails. For such a provider a short access-token TTL
// is routine lifecycle rather than something the operator can act on, and
// health and warnings must not report it as a fault (issue #22).
func SelfRefreshing(provider string) bool {
	return provider == "claude" && ClaudeRefreshDisabled
}

// TokenResponse represents the OAuth token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`           // Seconds
	ExpiresAt    string `json:"expires_at,omitempty"` // ISO8601
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope,omitempty"`
}

// RefreshClaudeToken refreshes the OAuth token for Claude Code.
var RefreshClaudeToken = func(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	if err := validateTokenEndpoint(ClaudeTokenURL, []string{"anthropic.com"}); err != nil {
		return nil, err
	}

	data := url.Values{}
	data.Set("client_id", ClaudeClientID)
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, "POST", ClaudeTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &tokenResp, nil
}

// UpdateClaudeAuth updates the auth files with the new token.
func UpdateClaudeAuth(path string, resp *TokenResponse) error {
	// Read existing file to preserve other fields
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}

	// If this is the modern Claude credentials format, update nested fields.
	if rawOauth, ok := auth["claudeAiOauth"]; ok {
		oauth, ok := rawOauth.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid claudeAiOauth format")
		}

		oauth["accessToken"] = resp.AccessToken
		if resp.RefreshToken != "" {
			oauth["refreshToken"] = resp.RefreshToken
		}

		newExpiry := claudeExpiryFromResponse(resp)
		if !newExpiry.IsZero() {
			oauth["expiresAt"] = newExpiry.UnixMilli()
		}

		auth["claudeAiOauth"] = oauth
		return writeAuthFile(path, auth)
	}

	// Update fields (support both camelCase and snake_case as seen in wild)
	if _, ok := auth["access_token"]; ok {
		auth["access_token"] = resp.AccessToken
	} else if _, ok := auth["accessToken"]; ok {
		auth["accessToken"] = resp.AccessToken
	} else {
		// Default to snake_case if neither exists or ambiguous
		auth["access_token"] = resp.AccessToken
	}

	if resp.RefreshToken != "" {
		if _, ok := auth["refresh_token"]; ok {
			auth["refresh_token"] = resp.RefreshToken
		} else if _, ok := auth["refreshToken"]; ok {
			auth["refreshToken"] = resp.RefreshToken
		} else {
			auth["refresh_token"] = resp.RefreshToken
		}
	}

	// Handle expiry
	// If response has expires_at (ISO8601), use it.
	// If response has expires_in (seconds), calculate expires_at.
	newExpiry := claudeExpiryFromResponse(resp)

	if !newExpiry.IsZero() {
		expiryStr := newExpiry.Format(time.RFC3339)
		if _, ok := auth["expires_at"]; ok {
			auth["expires_at"] = expiryStr
		} else if _, ok := auth["expiresAt"]; ok {
			auth["expiresAt"] = expiryStr
		} else {
			auth["expires_at"] = expiryStr
		}
	}

	return writeAuthFile(path, auth)
}

func claudeExpiryFromResponse(resp *TokenResponse) time.Time {
	if resp == nil {
		return time.Time{}
	}
	if resp.ExpiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339, resp.ExpiresAt); err == nil {
			return parsed
		}
	}
	if resp.ExpiresIn > 0 {
		return time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	return time.Time{}
}

func writeAuthFile(path string, auth map[string]interface{}) error {
	updatedData, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal updated auth: %w", err)
	}

	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := f.Write(updatedData); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename file: %w", err)
	}

	return nil
}
