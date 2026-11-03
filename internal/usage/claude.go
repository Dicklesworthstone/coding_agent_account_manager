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
	Utilization *float64 `json:"utilization"` // percent on a 0-100 scale; explicit null is inapplicable
	ResetsAt    string   `json:"resets_at"`   // ISO8601 timestamp
}

func (w *claudeWindow) UnmarshalJSON(data []byte) error {
	percent, err := nullableUsagePercent(data, "utilization")
	if err != nil {
		return err
	}
	type plain claudeWindow
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	decoded.Utilization = percent
	*w = claudeWindow(decoded)
	return nil
}

// claudeLimit is one entry of the usage response's limits[] array.
type claudeLimit struct {
	Kind     string       `json:"kind"`     // "session" | "weekly_all" | "weekly_scoped"
	Group    string       `json:"group"`    // "session" | "weekly"
	Percent  *float64     `json:"percent"`  // 0-100; explicit null is inapplicable
	Severity string       `json:"severity"` // "normal" | "warning" | "critical"
	ResetsAt string       `json:"resets_at"`
	Scope    *claudeScope `json:"scope"`
	IsActive bool         `json:"is_active"`
}

func (l *claudeLimit) UnmarshalJSON(data []byte) error {
	percent, err := nullableUsagePercent(data, "percent")
	if err != nil {
		return err
	}
	type plain claudeLimit
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	decoded.Percent = percent
	*l = claudeLimit(decoded)
	return nil
}

type claudeScope struct {
	Model *struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Surface *struct {
		DisplayName string `json:"display_name"`
	} `json:"surface"`
	unsupported bool
}

func (s *claudeScope) UnmarshalJSON(data []byte) error {
	type plain claudeScope
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for field := range fields {
		if field != "model" && field != "surface" {
			// A new scope dimension must not silently become a general
			// allowance or be represented as a model-only restriction.
			decoded.unsupported = true
		}
	}
	*s = claudeScope(decoded)
	return nil
}

// modelName returns the model a limit identifies, or "" when it has no
// usable model name. An empty name alone does not prove a general allowance.
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
	w := &UsageWindow{
		Utilization:    *l.Percent / 100.0,
		UsedPercent:    int(*l.Percent),
		ResetsAt:       parseISO8601(l.ResetsAt),
		WindowDuration: duration,
		Label:          l.modelName(),
		Kind:           l.Kind,
		Severity:       l.Severity,
		IsActive:       l.IsActive,
	}
	if l.Scope != nil && l.Scope.Model != nil {
		w.ModelID = strings.TrimSpace(l.Scope.Model.ID)
	}
	return w
}

// applyLimits folds the limits[] array into info.
//
// Every measured entry counts, not only the ones flagged is_active: the API
// sets that flag on the window currently binding the account. Explicit null
// measurements are inapplicable. Unknown measured constraints make the row
// unsafe for selection, while recognized windows remain available for display.
func applyLimits(info *UsageInfo, limits []claudeLimit, rollAt time.Time) error {
	unsupported := false
	conflicting := false
	rolled := false
	seenSession, seenWeekly := false, false
	for _, l := range limits {
		if l.Percent == nil {
			continue
		}
		switch {
		case l.Scope != nil && (l.Scope.unsupported || l.Scope.Surface != nil || (l.Scope.Model != nil && l.modelName() == "")):
			// An unnamed model or a product-surface restriction cannot be
			// represented as either a general or a named model allowance.
			unsupported = true
		case l.modelName() != "":
			// A model-scoped allowance. Keyed by the display name the API
			// scopes it to; UsageInfo.WindowForModel normalizes lookups, so a
			// caller may ask with a full model identifier.
			if info.ModelWindows == nil {
				info.ModelWindows = make(map[string]*UsageWindow)
			}
			name := l.modelName()
			w := l.window(claudeLimitDuration(l))
			if !rollAt.IsZero() {
				// Project cached candidates before choosing the binding one:
				// a spent but expired session must not hide a live weekly cap.
				if markRolled(w, rollAt) {
					rolled = true
				}
			}
			// Keep every simultaneous constraint. A lower-utilization window
			// can still supply the longest allowance or the earliest reset.
			info.ScopedWindows = append(info.ScopedWindows, w)
			// Preserve the existing strongest-window map for compact display.
			if preferScopedWindow(w, info.ModelWindows[name]) {
				info.ModelWindows[name] = w
			}
		case l.Kind == LimitKindSession && (l.Group == "" || l.Group == "session"):
			if seenSession {
				// Multiple measured general constraints cannot share a slot:
				// retaining only one can hide another cap or reset horizon.
				unsupported = true
				continue
			}
			seenSession = true
			if info.PrimaryWindow == nil {
				info.PrimaryWindow = l.window(claudeLimitDuration(l))
			} else if !annotateWindow(info.PrimaryWindow, l) {
				conflicting = true
			}
		case l.Kind == LimitKindWeeklyAll && (l.Group == "" || l.Group == "weekly"):
			if seenWeekly {
				unsupported = true
				continue
			}
			seenWeekly = true
			if info.SecondaryWindow == nil {
				info.SecondaryWindow = l.window(claudeLimitDuration(l))
			} else if !annotateWindow(info.SecondaryWindow, l) {
				conflicting = true
			}
		default:
			unsupported = true
		}
	}
	if unsupported {
		return fmt.Errorf("Claude response included an unsupported measured quota limit")
	}
	if conflicting {
		return fmt.Errorf("Claude response included conflicting measurements for a general quota")
	}
	if rolled {
		// A live same-model window can win the display slot without proving
		// the capacity of a different window whose old measurement expired.
		return errCachedQuotaRolled
	}
	return nil
}

// claudeLimitDuration keeps the documented durations for recognized windows.
// A new kind or contradictory group does not establish a duration, even if
// its model scope and numeric measurement are usable. A reset timestamp is
// not a window length; do not infer a duration from the time remaining.
func claudeLimitDuration(l claudeLimit) time.Duration {
	switch l.Kind {
	case LimitKindSession:
		if l.Group == "" || l.Group == "session" {
			return 5 * time.Hour
		}
	case LimitKindWeeklyAll, LimitKindWeeklyScoped:
		if l.Group == "" || l.Group == "weekly" {
			return 7 * 24 * time.Hour
		}
	}
	return 0
}

// annotateWindow merges a measured general limit only when both representations
// agree. There is no verified rounding contract for differing percentages or
// reset instants: retain the top-level measurement for diagnostics and refuse
// selection rather than silently discarding a possibly binding restriction.
// Comparing the percentages after the same conversion preserves fractional
// accuracy without multiplying an already-normalized float back to percent.
func annotateWindow(w *UsageWindow, l claudeLimit) bool {
	if w.Utilization != *l.Percent/100.0 {
		return false
	}
	reset := parseISO8601(l.ResetsAt)
	if !w.ResetsAt.IsZero() && !reset.IsZero() && !w.ResetsAt.Equal(reset) {
		return false
	}
	w.Kind = l.Kind
	w.Severity = l.Severity
	w.IsActive = l.IsActive
	if w.ResetsAt.IsZero() {
		w.ResetsAt = reset
	}
	return true
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
		Source:    SourceAPI,
		FetchedAt: time.Now(),
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// Success - parse response
	case http.StatusUnauthorized, http.StatusForbidden:
		info.Error = ErrorUnauthorized
		return info, fmt.Errorf("unauthorized: status %d", resp.StatusCode)
	default:
		info.Error = fmt.Sprintf("API error: status %d", resp.StatusCode)
		return info, fmt.Errorf("API error: status %d", resp.StatusCode)
	}

	var usage claudeUsageResponse
	if err := decodeUsageResponse(resp.Body, &usage); err != nil {
		info.QuotaStatus = QuotaUnavailable
		info.Error, info.QuotaNote = err.Error(), err.Error()
		return info, fmt.Errorf("decode response: %w", err)
	}

	if err := usage.apply(info, time.Time{}); err != nil {
		return info, err
	}
	return info, nil
}

// apply normalizes live responses and cached quota documents identically.
// Inapplicable windows are absent from UsageInfo; measured zero stays zero,
// including when no reset is known. Plan names never determine applicability.
// rollAt is only set for cached reads, to resolve overlapping model windows
// after projecting their recorded resets. Live measurements never roll here.
func (usage claudeUsageResponse) apply(info *UsageInfo, rollAt time.Time) error {
	// Convert to UsageInfo.
	//
	// The Claude OAuth usage API reports `utilization` as a percent on a 0-100
	// scale: 1.0 means 1%, 4.0 means 4%, 100.0 means 100%. We must NOT treat
	// small values (<= 1.0) as a 0-1 fraction — doing so mis-scaled a fresh
	// subscription sitting at 1.0% into "100%" (issue #52). UsedPercent stores
	// the percent directly (0-100) and Utilization stores the 0-1 fraction.
	if usage.FiveHour != nil && usage.FiveHour.Utilization != nil {
		pct := *usage.FiveHour.Utilization
		info.PrimaryWindow = &UsageWindow{
			Utilization:    pct / 100.0,
			UsedPercent:    int(pct),
			ResetsAt:       parseISO8601(usage.FiveHour.ResetsAt),
			WindowDuration: 5 * time.Hour,
		}
	}

	if usage.SevenDay != nil && usage.SevenDay.Utilization != nil {
		pct := *usage.SevenDay.Utilization
		info.SecondaryWindow = &UsageWindow{
			Utilization:    pct / 100.0,
			UsedPercent:    int(pct),
			ResetsAt:       parseISO8601(usage.SevenDay.ResetsAt),
			WindowDuration: 7 * 24 * time.Hour,
		}
	}

	// Preserve both accepted legacy fields if they are measured together.
	// They may report different restrictions; choosing one must not hide an
	// exhausted or expired allowance. Keep the first measured field in the
	// legacy display slot and retain every constraint for quota decisions.
	for _, opus := range []*claudeWindow{usage.Opus, usage.SevenDayOpus} {
		if opus == nil || opus.Utilization == nil {
			continue
		}
		w := legacyOpusWindow(opus)
		info.ScopedWindows = append(info.ScopedWindows, w)
		if info.TertiaryWindow == nil {
			info.TertiaryWindow = w
		}
	}

	// Per-model allowances, and the general windows for accounts that report
	// them only here (issue #97).
	err := applyLimits(info, usage.Limits, rollAt)
	if err == nil && info.PrimaryWindow == nil && info.SecondaryWindow == nil {
		err = fmt.Errorf("Claude response did not include a measured general quota")
	}
	if err != nil {
		info.QuotaStatus = QuotaDegraded
		info.Error = err.Error()
		info.QuotaNote = info.Error
		return err
	}

	return nil
}

func legacyOpusWindow(w *claudeWindow) *UsageWindow {
	pct := *w.Utilization
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
