// Package usage provides real-time usage and rate limit information
// from provider APIs. This enables smart account rotation based on
// actual usage limits rather than guessing.
package usage

import (
	"context"
	"strings"
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

	// Label is the human-readable name of what this window limits, as the
	// provider reports it — a model's display name ("Opus", "Fable") for a
	// scoped window, empty for the general ones.
	Label string `json:"label,omitempty"`

	// Kind is the provider's name for the window: for Claude, one of
	// "session" (5-hour), "weekly_all", or "weekly_scoped".
	Kind string `json:"kind,omitempty"`

	// Severity is the provider's own assessment: "normal", "warning" or
	// "critical".
	Severity string `json:"severity,omitempty"`

	// IsActive reports whether the provider flagged this window as the one
	// currently binding the account.
	IsActive bool `json:"is_active,omitempty"`
}

// Claude reports each rate limit window under one of these kinds.
const (
	LimitKindSession      = "session"
	LimitKindWeeklyAll    = "weekly_all"
	LimitKindWeeklyScoped = "weekly_scoped"
)

// Severity values the provider attaches to a limit.
const (
	SeverityNormal   = "normal"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

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

// windowUtilization returns a window's utilization as a 0-1 fraction, falling
// back to UsedPercent for windows that only carry the integer percent.
func windowUtilization(w *UsageWindow) float64 {
	if w == nil {
		return 0
	}
	util := w.Utilization
	if util == 0 && w.UsedPercent > 0 {
		util = float64(w.UsedPercent) / 100.0
	}
	return util
}

// AvailabilityScore calculates a score for account rotation (0-100).
// Higher scores indicate more available capacity.
//
// Model-scoped limits count against the score at their worst, so an account
// whose per-model allowance is spent ranks below an equally-idle one whose is
// not. Use AvailabilityScoreForModel when the model is known.
func (u *UsageInfo) AvailabilityScore() int {
	return u.AvailabilityScoreForModel("")
}

// AvailabilityScoreForModel is AvailabilityScore narrowed to the work actually
// about to run: only the scoped window for model constrains the score, so an
// account out of Fable capacity is still ranked as available for Sonnet work.
//
// An empty model means "unknown", and every scoped window is then taken into
// account at its worst.
func (u *UsageInfo) AvailabilityScoreForModel(model string) int {
	if u == nil || u.Error != "" {
		return 0
	}

	// Base score starts at 100
	score := 100.0

	// Primary window is most important (weight: 50%)
	score -= windowUtilization(u.PrimaryWindow) * 50

	// Secondary window (weight: 25%)
	score -= windowUtilization(u.SecondaryWindow) * 25

	// Premium-model limits (weight: 15%). The legacy tertiary window and the
	// scoped windows describe the same kind of constraint, so they share one
	// weight and the worst of the ones that apply is what counts.
	score -= u.premiumUtilization(model) * 15

	// Credit availability (weight: 10%)
	if u.Credits != nil && !u.Credits.Unlimited && !u.Credits.HasCredits {
		score -= 10
	}

	if score < 0 {
		return 0
	}
	return int(score)
}

// premiumWindows returns the per-model windows that constrain work on model —
// the scoped windows plus the legacy tertiary one, which is itself an Opus
// allowance on the accounts that still report it that way.
//
// An empty model means "unknown", and then every one of them applies: an
// omitted scoped limit must never read as spare capacity (issue #97).
func (u *UsageInfo) premiumWindows(model string) []*UsageWindow {
	if u == nil {
		return nil
	}
	var out []*UsageWindow
	if u.TertiaryWindow != nil && windowAppliesToModel(u.TertiaryWindow, model) {
		out = append(out, u.TertiaryWindow)
	}
	if model != "" {
		if w := u.scopedWindowForModel(model); w != nil {
			out = append(out, w)
		}
		return out
	}
	for _, w := range u.ModelWindows {
		if w != nil {
			out = append(out, w)
		}
	}
	return out
}

// windowAppliesToModel reports whether a labelled window constrains model. An
// unlabelled window constrains everything, and so does any window when the
// model is unknown.
func windowAppliesToModel(w *UsageWindow, model string) bool {
	if w == nil {
		return false
	}
	if model == "" || w.Label == "" {
		return true
	}
	return modelKeysMatch(NormalizeModelKey(model), NormalizeModelKey(w.Label))
}

// premiumUtilization returns the worst utilization among the per-model windows
// that apply to model.
func (u *UsageInfo) premiumUtilization(model string) float64 {
	return windowUtilization(u.ScopedLimit(model))
}

// ScopedLimit returns the per-model window closest to its cap among those that
// constrain model — every one of them when model is empty — or nil when the
// account has none. It is what a caller shows when asking "what else could
// stop this account from running the work?".
func (u *UsageInfo) ScopedLimit(model string) *UsageWindow {
	var worst *UsageWindow
	for _, w := range u.premiumWindows(model) {
		if worst == nil || windowUtilization(w) > windowUtilization(worst) {
			worst = w
		}
	}
	return worst
}

// scopedWindowForModel returns the model-scoped window that applies to model,
// or nil. Unlike WindowForModel it never falls back to the tertiary window.
func (u *UsageInfo) scopedWindowForModel(model string) *UsageWindow {
	if u == nil || len(u.ModelWindows) == 0 || model == "" {
		return nil
	}
	if w, ok := u.ModelWindows[model]; ok {
		return w
	}
	want := NormalizeModelKey(model)
	for key, w := range u.ModelWindows {
		if modelKeysMatch(want, NormalizeModelKey(key)) {
			return w
		}
		// A scoped window also carries the provider's display name, which is
		// what the API keys the limit on ("Fable", "Opus").
		if w != nil && w.Label != "" && modelKeysMatch(want, NormalizeModelKey(w.Label)) {
			return w
		}
	}
	return nil
}

// NormalizeModelKey reduces a model name to a comparable token: lower case,
// letters and digits only, with a leading "claude" dropped. It lets the model
// identifier a caller passes ("claude-opus-4-1-20250805") line up with the
// display name the usage API scopes a limit to ("Opus").
func NormalizeModelKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return strings.TrimPrefix(b.String(), "claude")
}

// modelKeysMatch reports whether two normalized model keys name the same model
// family. Containment handles the common shape — a family name inside a full
// model identifier — and the length floor keeps short fragments from matching
// everything.
func modelKeysMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) >= 4 && strings.Contains(b, a) {
		return true
	}
	return len(b) >= 4 && strings.Contains(a, b)
}

// IsNearLimit returns true if usage is approaching the limit.
// threshold is the utilization fraction to consider "near" (e.g., 0.8 for 80%).
func (u *UsageInfo) IsNearLimit(threshold float64) bool {
	return u.IsNearLimitForModel(threshold, "")
}

// IsNearLimitForModel is IsNearLimit narrowed to the model about to be used:
// the general windows always count, but of the model-scoped windows only the
// one covering model does. An empty model means "unknown", and then every
// scoped window counts — an omitted scoped limit must never read as capacity
// (issue #97).
func (u *UsageInfo) IsNearLimitForModel(threshold float64, model string) bool {
	if u == nil {
		return false
	}

	atLimit := func(w *UsageWindow) bool {
		return w != nil && windowUtilization(w) >= threshold
	}
	if atLimit(u.PrimaryWindow) || atLimit(u.SecondaryWindow) {
		return true
	}

	for _, w := range u.premiumWindows(model) {
		if atLimit(w) {
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

	if w := u.scopedWindowForModel(model); w != nil {
		return w
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
