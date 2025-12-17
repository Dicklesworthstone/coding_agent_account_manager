package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
func RefreshGeminiToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*GoogleTokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
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
		body, _ := io.ReadAll(resp.Body)
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
		return nil, fmt.Errorf("ADC file missing required fields (client_id, client_secret, refresh_token)")
	}

	return &adc, nil
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
