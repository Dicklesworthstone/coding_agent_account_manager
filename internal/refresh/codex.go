package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Codex Constants
var (
	CodexTokenURL = "https://auth.openai.com/oauth/token"
)

const (
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexScopes   = "openid profile email"
)

// RefreshCodexToken refreshes the OAuth token for OpenAI Codex.
func RefreshCodexToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	body := map[string]string{
		"client_id":     CodexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         CodexScopes,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", CodexTokenURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex refresh failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("codex refresh error %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &tokenResp, nil
}

// UpdateCodexAuth updates the auth file with the new token.
func UpdateCodexAuth(path string, resp *TokenResponse) error {
	// Read existing file
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}

	// Update fields
	auth["access_token"] = resp.AccessToken
	if resp.RefreshToken != "" {
		auth["refresh_token"] = resp.RefreshToken
	}

	// Calculate expires_at from expires_in
	if resp.ExpiresIn > 0 {
		auth["expires_at"] = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).Unix()
	}

	// Atomic write
	updatedData, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal updated auth: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, updatedData, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename file: %w", err)
	}

	return nil
}

// VerifyCodexToken verifies if a token works.
func VerifyCodexToken(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token verification failed with status %d", resp.StatusCode)
	}

	return nil
}
