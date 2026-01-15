package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// Gemini Constants
var (
	GeminiTokenURL = "https://oauth2.googleapis.com/token"
)

var ErrADCIncomplete = errors.New("ADC file missing required fields")

// ADC represents Google Application Default Credentials.
type ADC struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	Type         string `json:"type"`
}

// GoogleTokenResponse represents the Google OAuth token response.
type GoogleTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"` // Seconds
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`
}

// RefreshGeminiToken refreshes the OAuth token for Google Gemini.
var RefreshGeminiToken = func(ctx context.Context, clientID, clientSecret, refreshToken string) (*GoogleTokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	if err := validateTokenEndpoint(GeminiTokenURL, []string{"oauth2.googleapis.com"}); err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	req, err := http.NewRequestWithContext(ctx, "POST", GeminiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini refresh failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Use bounded read to prevent memory exhaustion from large error responses
		body, err := readLimitedBody(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gemini refresh error %d (failed to read body: %v)", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("gemini refresh error %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp GoogleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &tokenResp, nil
}

// ReadADC reads the ADC file to get credentials.
func ReadADC(path string) (*ADC, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ADC file: %w", err)
	}

	var adc ADC
	if err := json.Unmarshal(data, &adc); err != nil {
		return nil, fmt.Errorf("parse ADC file: %w", err)
	}

	if adc.ClientID == "" || adc.ClientSecret == "" || adc.RefreshToken == "" {
		return nil, fmt.Errorf("%w (need client_id, client_secret, refresh_token)", ErrADCIncomplete)
	}

	return &adc, nil
}

// UpdateGeminiAuth updates Gemini auth settings with a refreshed access token and expiry.
func UpdateGeminiAuth(path string, resp *GoogleTokenResponse) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}

	// Access token (support both snake_case and camelCase).
	if _, ok := auth["access_token"]; ok {
		auth["access_token"] = resp.AccessToken
	} else if _, ok := auth["accessToken"]; ok {
		auth["accessToken"] = resp.AccessToken
	} else {
		auth["access_token"] = resp.AccessToken
	}

	// Expiry: prefer existing field name when possible.
	if resp.ExpiresIn > 0 {
		expiresAt := time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)

		if _, ok := auth["expiry"]; ok {
			auth["expiry"] = expiresAt
		} else if _, ok := auth["expires_at"]; ok {
			auth["expires_at"] = expiresAt
		} else if _, ok := auth["expiresAt"]; ok {
			auth["expiresAt"] = expiresAt
		} else {
			auth["expiry"] = expiresAt
		}
	}

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
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename file: %w", err)
	}

	return nil
}

// UpdateGeminiHealth updates the health metadata with the new expiry.
// We do NOT update the ADC file itself as it is managed by gcloud.
func UpdateGeminiHealth(store *health.Storage, provider, profile string, resp *GoogleTokenResponse) error {
	healthData, err := store.GetProfile(provider, profile)
	if err != nil {
		return err
	}
	if healthData == nil {
		healthData = &health.ProfileHealth{}
	}

	if resp.ExpiresIn > 0 {
		healthData.TokenExpiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
		healthData.LastChecked = time.Now()
	}

	return store.UpdateProfile(provider, profile, healthData)
}
