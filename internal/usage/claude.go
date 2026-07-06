package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Claude API constants.
const (
	ClaudeUsageURL  = "https://api.anthropic.com/api/oauth/usage"
	ClaudeAPIBeta   = "oauth-2025-04-20"
	ClaudeUserAgent = "caam/1.0"
	claudeTimeout   = 30 * time.Second
)

// ClaudeFetcher fetches usage data from Claude's OAuth API.
type ClaudeFetcher struct {
	client  *http.Client
	baseURL string // For testing
}

// NewClaudeFetcher creates a new Claude usage fetcher.
func NewClaudeFetcher() *ClaudeFetcher {
	return &ClaudeFetcher{
		client: &http.Client{Timeout: claudeTimeout},
	}
}

// claudeUsageResponse represents the Claude usage API response.
type claudeUsageResponse struct {
	FiveHour *claudeWindow `json:"five_hour"`
	SevenDay *claudeWindow `json:"seven_day"`
	Opus     *claudeWindow `json:"opus"`
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"` // percent on a 0-100 scale (1.0 == 1%, 100.0 == 100%)
	ResetsAt    string  `json:"resets_at"`   // ISO8601 timestamp
}

// Fetch retrieves usage data from Claude's API.
func (f *ClaudeFetcher) Fetch(ctx context.Context, accessToken string) (*UsageInfo, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}

	url := ClaudeUsageURL
	if f.baseURL != "" {
		url = f.baseURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", ClaudeAPIBeta)
	req.Header.Set("User-Agent", ClaudeUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return &UsageInfo{
			Provider:  "claude",
			FetchedAt: time.Now(),
			Error:     fmt.Sprintf("request failed: %v", err),
		}, err
	}
	defer resp.Body.Close()

	info := &UsageInfo{
		Provider:  "claude",
		FetchedAt: time.Now(),
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// Success - parse response
	case http.StatusUnauthorized, http.StatusForbidden:
		info.Error = "unauthorized: token expired or invalid"
		return info, fmt.Errorf("unauthorized: status %d", resp.StatusCode)
	default:
		info.Error = fmt.Sprintf("API error: status %d", resp.StatusCode)
		return info, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var usage claudeUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		info.Error = fmt.Sprintf("decode error: %v", err)
		return info, fmt.Errorf("decode response: %w", err)
	}

	// Convert to UsageInfo.
	//
	// The Claude OAuth usage API reports `utilization` as a percent on a 0-100
	// scale: 1.0 means 1%, 4.0 means 4%, 100.0 means 100%. We must NOT treat
	// small values (<= 1.0) as a 0-1 fraction — doing so mis-scaled a fresh
	// subscription sitting at 1.0% into "100%" (issue #52). UsedPercent stores
	// the percent directly (0-100) and Utilization stores the 0-1 fraction.
	if usage.FiveHour != nil {
		pct := usage.FiveHour.Utilization
		info.PrimaryWindow = &UsageWindow{
			Utilization:    pct / 100.0,
			UsedPercent:    int(pct),
			ResetsAt:       parseISO8601(usage.FiveHour.ResetsAt),
			WindowDuration: 5 * time.Hour,
		}
	}

	if usage.SevenDay != nil {
		pct := usage.SevenDay.Utilization
		info.SecondaryWindow = &UsageWindow{
			Utilization:    pct / 100.0,
			UsedPercent:    int(pct),
			ResetsAt:       parseISO8601(usage.SevenDay.ResetsAt),
			WindowDuration: 7 * 24 * time.Hour,
		}
	}

	if usage.Opus != nil {
		pct := usage.Opus.Utilization
		info.TertiaryWindow = &UsageWindow{
			Utilization: pct / 100.0,
			UsedPercent: int(pct),
			ResetsAt:    parseISO8601(usage.Opus.ResetsAt),
			// Opus limits are typically daily/weekly but window duration is variable
		}
	}

	return info, nil
}

// parseISO8601 parses an ISO8601 timestamp string.
func parseISO8601(s string) time.Time {
	if s == "" {
		return time.Time{}
	}

	// Try various ISO8601 formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t
		}
	}

	return time.Time{}
}
