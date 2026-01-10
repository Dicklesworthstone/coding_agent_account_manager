package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Codex API constants.
const (
	CodexDefaultBaseURL = "https://chatgpt.com/backend-api"
	CodexUsagePath      = "/wham/usage"
	CodexUserAgent      = "caam/1.0"
	codexTimeout        = 30 * time.Second
)

// CodexFetcher fetches usage data from Codex/ChatGPT API.
type CodexFetcher struct {
	client  *http.Client
	baseURL string // Overridable for testing or custom endpoints
}

// NewCodexFetcher creates a new Codex usage fetcher.
func NewCodexFetcher() *CodexFetcher {
	return &CodexFetcher{
		client: &http.Client{Timeout: codexTimeout},
	}
}

// codexUsageResponse represents the Codex usage API response.
type codexUsageResponse struct {
	PlanType  string           `json:"plan_type"`
	RateLimit *codexRateLimit  `json:"rate_limit"`
	Credits   *codexCreditInfo `json:"credits"`
}

type codexRateLimit struct {
	PrimaryWindow   *codexWindow `json:"primary_window"`
	SecondaryWindow *codexWindow `json:"secondary_window"`
}

type codexWindow struct {
	UsedPercent        int `json:"used_percent"`
	ResetAt            int `json:"reset_at"`             // Unix timestamp (seconds)
	LimitWindowSeconds int `json:"limit_window_seconds"` // Window size in seconds
}

type codexCreditInfo struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance,omitempty"` // Sometimes string, sometimes number
}

// CodexFetchOptions provides optional parameters for fetching.
type CodexFetchOptions struct {
	AccountID string // ChatGPT-Account-Id header
}

// Fetch retrieves usage data from Codex/ChatGPT API.
func (f *CodexFetcher) Fetch(ctx context.Context, accessToken string) (*UsageInfo, error) {
	return f.FetchWithOptions(ctx, accessToken, nil)
}

// FetchWithOptions retrieves usage data with optional parameters.
func (f *CodexFetcher) FetchWithOptions(ctx context.Context, accessToken string, opts *CodexFetchOptions) (*UsageInfo, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}

	url := f.resolveUsageURL()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", CodexUserAgent)
	req.Header.Set("Accept", "application/json")

	if opts != nil && opts.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", opts.AccountID)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return &UsageInfo{
			Provider:  "codex",
			FetchedAt: time.Now(),
			Error:     fmt.Sprintf("request failed: %v", err),
		}, err
	}
	defer resp.Body.Close()

	info := &UsageInfo{
		Provider:  "codex",
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

	var usage codexUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		info.Error = fmt.Sprintf("decode error: %v", err)
		return info, fmt.Errorf("decode response: %w", err)
	}

	// Convert to UsageInfo
	info.PlanType = usage.PlanType

	if usage.RateLimit != nil {
		if usage.RateLimit.PrimaryWindow != nil {
			w := usage.RateLimit.PrimaryWindow
			info.PrimaryWindow = &UsageWindow{
				Utilization:    float64(w.UsedPercent) / 100.0,
				UsedPercent:    w.UsedPercent,
				ResetsAt:       time.Unix(int64(w.ResetAt), 0),
				WindowDuration: time.Duration(w.LimitWindowSeconds) * time.Second,
			}
		}

		if usage.RateLimit.SecondaryWindow != nil {
			w := usage.RateLimit.SecondaryWindow
			info.SecondaryWindow = &UsageWindow{
				Utilization:    float64(w.UsedPercent) / 100.0,
				UsedPercent:    w.UsedPercent,
				ResetsAt:       time.Unix(int64(w.ResetAt), 0),
				WindowDuration: time.Duration(w.LimitWindowSeconds) * time.Second,
			}
		}
	}

	if usage.Credits != nil {
		info.Credits = &CreditInfo{
			HasCredits: usage.Credits.HasCredits,
			Unlimited:  usage.Credits.Unlimited,
		}
		if usage.Credits.Balance != nil {
			var balance float64
			if _, err := fmt.Sscanf(*usage.Credits.Balance, "%f", &balance); err == nil {
				info.Credits.Balance = &balance
			}
		}
	}

	return info, nil
}

// resolveUsageURL determines the correct usage API URL.
// Checks for custom config in ~/.codex/config.toml.
func (f *CodexFetcher) resolveUsageURL() string {
	if f.baseURL != "" {
		return f.baseURL + CodexUsagePath
	}

	// Try to read custom base URL from config
	baseURL := f.resolveChatGPTBaseURL()
	return baseURL + CodexUsagePath
}

// resolveChatGPTBaseURL reads the base URL from config or returns default.
func (f *CodexFetcher) resolveChatGPTBaseURL() string {
	configPath := f.getConfigPath()
	if configPath == "" {
		return CodexDefaultBaseURL
	}

	contents, err := os.ReadFile(configPath)
	if err != nil {
		return CodexDefaultBaseURL
	}

	baseURL := parseChatGPTBaseURL(string(contents))
	if baseURL == "" {
		return CodexDefaultBaseURL
	}

	return normalizeChatGPTBaseURL(baseURL)
}

// getConfigPath returns the path to the Codex config file.
func (f *CodexFetcher) getConfigPath() string {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		codexHome = filepath.Join(homeDir, ".codex")
	}
	return filepath.Join(codexHome, "config.toml")
}

// parseChatGPTBaseURL extracts chatgpt_base_url from TOML config.
func parseChatGPTBaseURL(contents string) string {
	for _, line := range strings.Split(contents, "\n") {
		// Remove comments
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		if key != "chatgpt_base_url" {
			continue
		}

		value := strings.TrimSpace(parts[1])
		// Remove quotes
		value = strings.Trim(value, "\"'")
		return value
	}
	return ""
}

// normalizeChatGPTBaseURL normalizes the base URL.
func normalizeChatGPTBaseURL(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return CodexDefaultBaseURL
	}

	// Remove trailing slashes
	for strings.HasSuffix(url, "/") {
		url = url[:len(url)-1]
	}

	// Add /backend-api if needed for standard ChatGPT URLs
	if (strings.HasPrefix(url, "https://chatgpt.com") || strings.HasPrefix(url, "https://chat.openai.com")) &&
		!strings.Contains(url, "/backend-api") {
		url += "/backend-api"
	}

	return url
}
