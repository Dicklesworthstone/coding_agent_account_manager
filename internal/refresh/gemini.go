package refresh

import (
	"bytes"
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
		if code, rejected := classifyRefreshRejection(resp.StatusCode, body); rejected {
			return nil, &RefreshRejectedError{Provider: "gemini", StatusCode: resp.StatusCode, Code: code}
		}
		return nil, fmt.Errorf("gemini refresh error %d", resp.StatusCode)
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

	return parseADC(data)
}

func parseADC(data []byte) (*ADC, error) {
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
	source, err := readCredentialSnapshot(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}
	data, err := updatedGeminiAuth(source.data, resp, false)
	if err != nil {
		return err
	}
	return source.publish(data)
}

func updatedGeminiAuth(data []byte, resp *GoogleTokenResponse, flatADC bool) ([]byte, error) {
	if resp == nil || strings.TrimSpace(resp.AccessToken) == "" || resp.ExpiresIn < 0 {
		return nil, fmt.Errorf("refresh returned an invalid access credential")
	}
	var auth map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&auth); err != nil || auth == nil {
		return nil, fmt.Errorf("auth file must contain a JSON object")
	}

	// Check for nested structures common in settings.json
	updated := false
	if oauth, ok := auth["oauth"].(map[string]interface{}); ok && !flatADC {
		updateGeminiTokenMap(oauth, resp)
		updated = true
	} else if creds, ok := auth["credentials"].(map[string]interface{}); ok && !flatADC {
		updateGeminiTokenMap(creds, resp)
		updated = true
	}

	// If no nested structure found, or if we want to support flat files too (like oauth_creds.json),
	// we check if we should update the top level.
	// If we updated a nested object, we shouldn't update top level unless it ALSO has token fields.
	// But usually it's one or the other.
	// If we didn't find a nested object, assume flat.
	if !updated {
		updateGeminiTokenMap(auth, resp)
	}

	return json.MarshalIndent(auth, "", "  ")
}

func updateGeminiTokenMap(m map[string]interface{}, resp *GoogleTokenResponse) {
	// Access token (support both snake_case and camelCase).
	if _, ok := m["access_token"]; ok {
		m["access_token"] = resp.AccessToken
	} else if _, ok := m["accessToken"]; ok {
		m["accessToken"] = resp.AccessToken
	} else {
		m["access_token"] = resp.AccessToken
	}

	// Prefer the existing spelling, but never attach an older generation's
	// deadline to a new opaque token when its response carries no lifetime.
	expiryKey := "expiry"
	if _, ok := m["expiry"]; !ok {
		if _, ok := m["expires_at"]; ok {
			expiryKey = "expires_at"
		} else if _, ok := m["expiresAt"]; ok {
			expiryKey = "expiresAt"
		}
	}
	clearCredentialExpiry(m)
	if resp.ExpiresIn > 0 {
		expiresAt := time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
		m[expiryKey] = expiresAt
	}
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
