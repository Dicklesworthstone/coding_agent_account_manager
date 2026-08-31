// Package usage provides real-time usage and rate limit information
// from provider APIs. This enables smart account rotation based on
// actual usage limits rather than guessing.
package usage

import (
	"context"
	"time"
)

// UsageWindow represents a rate limit window with utilization data.
type UsageWindow struct {
	// Utilization is the fraction used (0.0 to 1.0).
	Utilization float64 `json:"utilization"`

	// UsedPercent is the percentage used (0-100).
	UsedPercent int `json:"used_percent"`

	// ResetsAt is when this window resets.
	ResetsAt time.Time `json:"resets_at"`

	// WindowDuration is the window size (if known).
	WindowDuration time.Duration `json:"window_duration,omitempty"`
}

// UsageInfo contains rate limit and usage information for a provider account.
type UsageInfo struct {
	// Provider is "claude" or "codex".
	Provider string `json:"provider"`

	// ProfileName is the CAAM profile name (if known).
	ProfileName string `json:"profile_name,omitempty"`

	// PlanType describes the subscription tier (e.g., "max", "pro", "plus").
	PlanType string `json:"plan_type,omitempty"`

	// RateLimitTier is the rate limit tier from the provider.
	RateLimitTier string `json:"rate_limit_tier,omitempty"`

	// PrimaryWindow is the main rate limit window (usually shorter).
	// For Claude: 5-hour rolling window for all models.
	PrimaryWindow *UsageWindow `json:"primary_window,omitempty"`

	// SecondaryWindow is the secondary window (usually longer).
	// For Claude: 7-day weekly cap for all models.
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"`

	// TertiaryWindow is an additional window for premium model limits.
	// For Claude: Opus-specific daily/weekly limits.
	TertiaryWindow *UsageWindow `json:"tertiary_window,omitempty"`

	// ModelWindows contains per-model rate limit windows.
	// Key is model name (e.g., "claude-3-opus", "claude-3-sonnet").
	// Useful when different models have independent limits.
	ModelWindows map[string]*UsageWindow `json:"model_windows,omitempty"`

	// Credits contains credit balance info (Codex only).
	Credits *CreditInfo `json:"credits,omitempty"`

	// FetchedAt is when this usage info was fetched.
	FetchedAt time.Time `json:"fetched_at"`

	// Error contains any error message from fetching.
	Error string `json:"error,omitempty"`

	// BurnRate contains token consumption rate from log/session data.
	BurnRate *BurnRateInfo `json:"burn_rate,omitempty"`

	// EstimatedDepletion is when the rate limit is predicted to be hit
	// at the current burn rate. Zero time if cannot predict.
	EstimatedDepletion time.Time `json:"estimated_depletion,omitempty"`

	// DepletionConfidence is how confident the depletion prediction is (0-1).
	// Based on burn rate data quality and sample size.
	DepletionConfidence float64 `json:"depletion_confidence,omitempty"`
}

// CreditInfo contains credit/balance information (primarily for Codex).
type CreditInfo struct {
	HasCredits bool     `json:"has_credits"`
	Unlimited  bool     `json:"unlimited"`
	Balance    *float64 `json:"balance,omitempty"`
}

// Fetcher defines the interface for fetching usage data.
type Fetcher interface {
	Fetch(ctx context.Context, accessToken string) (*UsageInfo, error)
}

// AvailabilityScore calculates a score for account rotation (0-100).
// Higher scores indicate more available capacity.
func (u *UsageInfo) AvailabilityScore() int {
	if u == nil || u.Error != "" {
		return 0
	}

	// Base score starts at 100
	score := 100.0

	// Primary window is most important (weight: 50%)
	if u.PrimaryWindow != nil {
		primaryUtil := u.PrimaryWindow.Utilization
		if primaryUtil == 0 && u.PrimaryWindow.UsedPercent > 0 {
			primaryUtil = float64(u.PrimaryWindow.UsedPercent) / 100.0
		}
		score -= primaryUtil * 50
	}

	// Secondary window (weight: 25%)
	if u.SecondaryWindow != nil {
		secondaryUtil := u.SecondaryWindow.Utilization
		if secondaryUtil == 0 && u.SecondaryWindow.UsedPercent > 0 {
			secondaryUtil = float64(u.SecondaryWindow.UsedPercent) / 100.0
		}
		score -= secondaryUtil * 25
	}

	// Tertiary window for premium model limits (weight: 15%)
	if u.TertiaryWindow != nil {
		tertiaryUtil := u.TertiaryWindow.Utilization
		if tertiaryUtil == 0 && u.TertiaryWindow.UsedPercent > 0 {
			tertiaryUtil = float64(u.TertiaryWindow.UsedPercent) / 100.0
		}
		score -= tertiaryUtil * 15
	}

	// Credit availability (weight: 10%)
	if u.Credits != nil && !u.Credits.Unlimited && !u.Credits.HasCredits {
		score -= 10
	}

	if score < 0 {
		return 0
	}
	return int(score)
}

// IsNearLimit returns true if usage is approaching the limit.
// threshold is the utilization fraction to consider "near" (e.g., 0.8 for 80%).
func (u *UsageInfo) IsNearLimit(threshold float64) bool {
	if u == nil {
		return false
	}

	if u.PrimaryWindow != nil {
		util := u.PrimaryWindow.Utilization
		if util == 0 && u.PrimaryWindow.UsedPercent > 0 {
			util = float64(u.PrimaryWindow.UsedPercent) / 100.0
		}
		if util >= threshold {
			return true
		}
	}

	if u.SecondaryWindow != nil {
		util := u.SecondaryWindow.Utilization
		if util == 0 && u.SecondaryWindow.UsedPercent > 0 {
			util = float64(u.SecondaryWindow.UsedPercent) / 100.0
		}
		if util >= threshold {
			return true
		}
	}

	if u.TertiaryWindow != nil {
		util := u.TertiaryWindow.Utilization
		if util == 0 && u.TertiaryWindow.UsedPercent > 0 {
			util = float64(u.TertiaryWindow.UsedPercent) / 100.0
		}
		if util >= threshold {
			return true
		}
	}

	// Check model-specific windows
	for _, window := range u.ModelWindows {
		if window == nil {
			continue
		}
		util := window.Utilization
		if util == 0 && window.UsedPercent > 0 {
			util = float64(window.UsedPercent) / 100.0
		}
		if util >= threshold {
			return true
		}
	}

	return false
}

// EarliestReset returns the earliest known reset time across all windows.
// Returns the zero time if no window has a reset timestamp.
func (u *UsageInfo) EarliestReset() time.Time {
	if u == nil {
		return time.Time{}
	}

	var earliest time.Time

	if u.PrimaryWindow != nil && !u.PrimaryWindow.ResetsAt.IsZero() {
		earliest = u.PrimaryWindow.ResetsAt
	}

	if u.SecondaryWindow != nil && !u.SecondaryWindow.ResetsAt.IsZero() {
		if earliest.IsZero() || u.SecondaryWindow.ResetsAt.Before(earliest) {
			earliest = u.SecondaryWindow.ResetsAt
		}
	}

	if u.TertiaryWindow != nil && !u.TertiaryWindow.ResetsAt.IsZero() {
		if earliest.IsZero() || u.TertiaryWindow.ResetsAt.Before(earliest) {
			earliest = u.TertiaryWindow.ResetsAt
		}
	}

	// Check model-specific windows
	for _, window := range u.ModelWindows {
		if window != nil && !window.ResetsAt.IsZero() {
			if earliest.IsZero() || window.ResetsAt.Before(earliest) {
				earliest = window.ResetsAt
			}
		}
	}

	return earliest
}

// TimeUntilReset returns the shortest time until any window resets.
func (u *UsageInfo) TimeUntilReset() time.Duration {
	earliest := u.EarliestReset()
	if earliest.IsZero() {
		return 0
	}

	ttl := time.Until(earliest)
	if ttl < 0 {
		return 0
	}
	return ttl
}

// MostConstrainedWindow returns the window closest to its limit.
// Returns nil if no windows are available.
func (u *UsageInfo) MostConstrainedWindow() *UsageWindow {
	if u == nil {
		return nil
	}

	var mostConstrained *UsageWindow
	var highestUtil float64

	checkWindow := func(w *UsageWindow) {
		if w == nil {
			return
		}
		util := w.Utilization
		if util == 0 && w.UsedPercent > 0 {
			util = float64(w.UsedPercent) / 100.0
		}
		if mostConstrained == nil || util > highestUtil {
			mostConstrained = w
			highestUtil = util
		}
	}

	checkWindow(u.PrimaryWindow)
	checkWindow(u.SecondaryWindow)
	checkWindow(u.TertiaryWindow)

	for _, w := range u.ModelWindows {
		checkWindow(w)
	}

	return mostConstrained
}

// WindowForModel returns the rate limit window for a specific model.
// Falls back to TertiaryWindow if no model-specific window exists.
func (u *UsageInfo) WindowForModel(model string) *UsageWindow {
	if u == nil {
		return nil
	}

	if u.ModelWindows != nil {
		if w, ok := u.ModelWindows[model]; ok {
			return w
		}
	}

	// Fall back to tertiary (premium model) window
	return u.TertiaryWindow
}

// PredictDepletion calculates when the rate limit will be hit based on burn rate.
// Returns zero time if prediction is not possible (no data or no burn rate).
//
// The prediction considers:
// - Current utilization percentage
// - Burn rate (percent consumed per hour)
// - Window reset time (caps prediction at reset)
func PredictDepletion(currentPercent float64, burnRate *BurnRateInfo, window *UsageWindow) time.Time {
	if burnRate == nil || burnRate.PercentPerHour <= 0 {
		return time.Time{} // Cannot predict without burn rate
	}

	if currentPercent >= 100 {
		return time.Now() // Already depleted
	}

	remainingPercent := 100.0 - currentPercent
	hoursUntilDepletion := remainingPercent / burnRate.PercentPerHour

	predicted := time.Now().Add(time.Duration(hoursUntilDepletion * float64(time.Hour)))

	// Cap at window reset time (usage resets before depletion)
	if window != nil && !window.ResetsAt.IsZero() && predicted.After(window.ResetsAt) {
		return window.ResetsAt
	}

	return predicted
}

// UpdateDepletion calculates and sets the EstimatedDepletion and DepletionConfidence.
// Uses the most constrained window for prediction.
func (u *UsageInfo) UpdateDepletion() {
	if u == nil || u.BurnRate == nil {
		return
	}

	// Find the most constrained window
	window := u.MostConstrainedWindow()
	if window == nil {
		return
	}

	// Get current utilization
	util := window.Utilization
	if util == 0 && window.UsedPercent > 0 {
		util = float64(window.UsedPercent) / 100.0
	}
	currentPercent := util * 100

	// Predict depletion
	u.EstimatedDepletion = PredictDepletion(currentPercent, u.BurnRate, window)

	// Set confidence based on burn rate confidence
	u.DepletionConfidence = u.BurnRate.Confidence
}

// TimeToDepletion returns the duration until estimated depletion.
// Returns 0 if no depletion is predicted or already depleted.
func (u *UsageInfo) TimeToDepletion() time.Duration {
	if u == nil || u.EstimatedDepletion.IsZero() {
		return 0
	}

	ttd := time.Until(u.EstimatedDepletion)
	if ttd < 0 {
		return 0
	}
	return ttd
}

// IsDepletionImminent returns true if depletion is expected within the threshold.
// Common thresholds: 10 minutes (imminent), 30 minutes (approaching).
func (u *UsageInfo) IsDepletionImminent(threshold time.Duration) bool {
	if u == nil || u.EstimatedDepletion.IsZero() {
		return false
	}

	ttd := u.TimeToDepletion()
	return ttd > 0 && ttd <= threshold
}

// DepletionWarningLevel returns a warning level based on time to depletion.
// Returns 0 (none), 1 (approaching - <30min), or 2 (imminent - <10min).
func (u *UsageInfo) DepletionWarningLevel() int {
	if u == nil {
		return 0
	}

	ttd := u.TimeToDepletion()
	if ttd == 0 {
		return 0
	}

	if ttd < 10*time.Minute {
		return 2 // Imminent
	}
	if ttd < 30*time.Minute {
		return 1 // Approaching
	}
	return 0 // None
}
