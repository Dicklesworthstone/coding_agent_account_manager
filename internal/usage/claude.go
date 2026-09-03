package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
	// Opus is the legacy premium-model window. Current responses carry it as
	// seven_day_opus, and newer accounts report it (and the other per-model
	// allowances) only through Limits.
	Opus         *claudeWindow `json:"opus"`
	SevenDayOpus *claudeWindow `json:"seven_day_opus"`

	// Limits is the current shape: one entry per rate limit window, including
	// the per-model ("weekly_scoped") allowances that have no top-level field
	// of their own — a Fable or Opus quota an account can exhaust while its
	// general windows still look idle (issue #97).
	Limits []claudeLimit `json:"limits"`
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"` // percent on a 0-100 scale (1.0 == 1%, 100.0 == 100%)
	ResetsAt    string  `json:"resets_at"`   // ISO8601 timestamp
}

// claudeLimit is one entry of the usage response's limits[] array.
type claudeLimit struct {
	Kind     string       `json:"kind"`     // "session" | "weekly_all" | "weekly_scoped"
	Group    string       `json:"group"`    // "session" | "weekly"
	Percent  float64      `json:"percent"`  // 0-100
	Severity string       `json:"severity"` // "normal" | "warning" | "critical"
	ResetsAt string       `json:"resets_at"`
	Scope    *claudeScope `json:"scope"`
	IsActive bool         `json:"is_active"`
}

type claudeScope struct {
	Model *struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Surface *struct {
		DisplayName string `json:"display_name"`
	} `json:"surface"`
}

// modelName returns the model a limit is scoped to, or "" when the limit
// applies to everything.
func (l claudeLimit) modelName() string {
	if l.Scope == nil || l.Scope.Model == nil {
		return ""
	}
	if name := strings.TrimSpace(l.Scope.Model.DisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(l.Scope.Model.ID)
}

// window renders a limit as a UsageWindow.
func (l claudeLimit) window(duration time.Duration) *UsageWindow {
	return &UsageWindow{
		Utilization:    l.Percent / 100.0,
		UsedPercent:    int(l.Percent),
		ResetsAt:       parseISO8601(l.ResetsAt),
		WindowDuration: duration,
		Label:          l.modelName(),
		Kind:           l.Kind,
		Severity:       l.Severity,
		IsActive:       l.IsActive,
	}
}

// applyLimits folds the limits[] array into info.
//
// Every entry is used, not only the ones flagged is_active: the API sets that
// flag on the window currently binding the account, so filtering on it would
// throw away the general windows exactly when a scoped one is exhausted.
func applyLimits(info *UsageInfo, limits []claudeLimit) {
	for _, l := range limits {
		switch {
		case l.modelName() != "":
			// A model-scoped allowance. Keyed by the display name the API
			// scopes it to; UsageInfo.WindowForModel normalizes lookups, so a
			// caller may ask with a full model identifier.
			if info.ModelWindows == nil {
				info.ModelWindows = make(map[string]*UsageWindow)
			}
			name := l.modelName()
			w := l.window(claudeLimitDuration(l))
			// Several scoped limits can name the same model (a weekly and a
			// session one); keep the one closest to its cap.
			if prev, ok := info.ModelWindows[name]; !ok || w.Utilization > prev.Utilization {
				info.ModelWindows[name] = w
			}
		case l.Kind == LimitKindSession:
			if info.PrimaryWindow == nil {
				info.PrimaryWindow = l.window(5 * time.Hour)
			} else {
				annotateWindow(info.PrimaryWindow, l)
			}
		case l.Kind == LimitKindWeeklyAll:
			if info.SecondaryWindow == nil {
				info.SecondaryWindow = l.window(7 * 24 * time.Hour)
			} else {
				annotateWindow(info.SecondaryWindow, l)
			}
		}
	}
}

// claudeLimitDuration maps a limit's group to a window length.
func claudeLimitDuration(l claudeLimit) time.Duration {
	switch l.Group {
	case "session":
		return 5 * time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	default:
		return 0
	}
}

// annotateWindow carries a limit's descriptive fields onto a window already
// built from the response's top-level field, whose numbers stay authoritative
// (they are floats, where limits[] rounds to whole percent).
func annotateWindow(w *UsageWindow, l claudeLimit) {
	w.Kind = l.Kind
	w.Severity = l.Severity
	w.IsActive = l.IsActive
	if w.ResetsAt.IsZero() {
		w.ResetsAt = parseISO8601(l.ResetsAt)
	}
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

	// The legacy premium-model window, under either of the names the API has
	// used for it. Newer accounts report it through limits[] instead.
	if opus := usage.Opus; opus != nil {
		info.TertiaryWindow = legacyOpusWindow(opus)
	} else if opus := usage.SevenDayOpus; opus != nil {
		info.TertiaryWindow = legacyOpusWindow(opus)
	}

	// Per-model allowances, and the general windows for accounts that report
	// them only here (issue #97).
	applyLimits(info, usage.Limits)

	return info, nil
}

func legacyOpusWindow(w *claudeWindow) *UsageWindow {
	pct := w.Utilization
	return &UsageWindow{
		Utilization: pct / 100.0,
		UsedPercent: int(pct),
		ResetsAt:    parseISO8601(w.ResetsAt),
		Label:       "Opus",
		// Opus limits are typically daily/weekly but window duration is variable
	}
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
