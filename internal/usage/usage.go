// Package usage provides real-time usage and rate limit information
// from provider APIs. This enables smart account rotation based on
// actual usage limits rather than guessing.
package usage

import (
	"context"
	"math"
	"sort"
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

	// ModelID retains the provider's model identifier alongside its display
	// label. Either can identify an applicable model restriction.
	ModelID string `json:"model_id,omitempty"`

	// Kind is the provider's name for the window: for Claude, one of
	// "session" (5-hour), "weekly_all", or "weekly_scoped".
	Kind string `json:"kind,omitempty"`

	// Severity is the provider's own assessment: "normal", "warning" or
	// "critical".
	Severity string `json:"severity,omitempty"`

	// IsActive reports whether the provider flagged this window as the one
	// currently binding the account.
	IsActive bool `json:"is_active,omitempty"`

	// Rolled reports that ResetsAt had already passed when these figures were
	// read, so the window has emptied since they were recorded. Only a cached
	// (on-disk) snapshot can be stale enough for this; the percentages are
	// zeroed when it is set, and the flag keeps that zero distinguishable from
	// a window that is genuinely untouched.
	Rolled bool `json:"rolled,omitempty"`

	// Unmeasured means this window carries a reset time or a label but the
	// provider did not supply a numeric utilization. A zero UsedPercent is
	// then not a measurement and must not be read as "unused".
	Unmeasured bool `json:"unmeasured,omitempty"`
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

	// HTTPStatus is the provider's HTTP status for a failed usage request,
	// when there was one. Not serialized; it lets callers tell a credential
	// rejection (401) from a 403 an edge proxy may send.
	HTTPStatus int `json:"-"`

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

	// ModelWindows contains the strongest reported window for each model.
	// Key is model name (e.g., "claude-3-opus", "claude-3-sonnet").
	// It remains a compact display projection; quota calculations also use
	// ScopedWindows so overlapping session and weekly limits are not lost.
	ModelWindows map[string]*UsageWindow `json:"model_windows,omitempty"`

	// ScopedWindows retains every model-scoped constraint, including several
	// windows for the same model. The highest usage and the governing reset
	// need not belong to the same window.
	ScopedWindows []*UsageWindow `json:"scoped_windows,omitempty"`

	// Credits contains credit balance info (Codex only).
	Credits *CreditInfo `json:"credits,omitempty"`

	// AccountID is the provider's own identifier for the account these
	// figures belong to, when the source reports one.
	AccountID string `json:"account_id,omitempty"`

	// Source says where the figures came from: SourceAPI (a live request) or
	// SourceCache (a snapshot read off local disk). Empty means the live API,
	// for compatibility with rows produced before offline reads existed.
	Source string `json:"source,omitempty"`

	// FetchedAt is when this usage info was fetched. For SourceCache it is the
	// snapshot's OWN timestamp — when the provider last refreshed it — not
	// when caam read the file, so now-FetchedAt is the real staleness.
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

	// QuotaStatus is "ok" when a numeric utilization was derived from the
	// provider's figures, "degraded" when the provider responded but did not
	// include enough to compute one, and "unavailable" when the fetch failed.
	// Empty is the legacy Claude/Codex status; the windows must still contain
	// usable measurements before the row can be used for selection.
	QuotaStatus string `json:"quota_status,omitempty"`

	// QuotaNote explains a degraded or unavailable read. It is not a percentage.
	QuotaNote string `json:"quota_note,omitempty"`

	// LimitStage is a provider limit state that is not a percentage (Cursor's
	// limit-hit stage, for example). Empty when the provider did not report one.
	LimitStage string `json:"limit_stage,omitempty"`

	// Billing carries provider billing fields that do not fit a rate-limit
	// window: period bounds, on-demand cents, prepaid balance. Amounts are
	// omitted when the provider did not send them.
	Billing *BillingSnapshot `json:"billing,omitempty"`

	// Grants are credit grants the provider returned. A grant is not turned
	// into a utilization percentage unless both a total and a remaining
	// amount were present.
	Grants []GrantSnapshot `json:"grants,omitempty"`
}

// Quota status values.
const (
	QuotaOK          = "ok"
	QuotaDegraded    = "degraded"
	QuotaUnavailable = "unavailable"
)

// BillingSnapshot is the subset of a billing response caam will repeat.
// Pointer fields are omitted when the provider did not send them, so a zero
// that was actually reported stays distinguishable from an absent field.
type BillingSnapshot struct {
	PeriodType          string `json:"period_type,omitempty"`
	PeriodStart         string `json:"period_start,omitempty"`
	PeriodEnd           string `json:"period_end,omitempty"`
	OnDemandCapCents    *int64 `json:"on_demand_cap_cents,omitempty"`
	OnDemandUsedCents   *int64 `json:"on_demand_used_cents,omitempty"`
	PrepaidBalanceCents *int64 `json:"prepaid_balance_cents,omitempty"`
	Unified             *bool  `json:"unified,omitempty"`
	OnDemandEnabled     *bool  `json:"on_demand_enabled,omitempty"`
	// LimitType is who owns the on-demand limit, as the provider names it
	// (Cursor sends "team" when a team admin sets it).
	LimitType string `json:"limit_type,omitempty"`
	// TeamPool* is a team's shared on-demand pool (Cursor Team plans). It can
	// run low while the member's own figures above still look fine.
	TeamPoolCapCents       *int64 `json:"team_pool_cap_cents,omitempty"`
	TeamPoolUsedCents      *int64 `json:"team_pool_used_cents,omitempty"`
	TeamPoolRemainingCents *int64 `json:"team_pool_remaining_cents,omitempty"`
}

// GrantSnapshot is one credit grant. Cents are the provider's minor units.
type GrantSnapshot struct {
	Type           string     `json:"type,omitempty"`
	TotalCents     *int64     `json:"total_cents,omitempty"`
	RemainingCents *int64     `json:"remaining_cents,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Models         []string   `json:"models,omitempty"`
}

// NumericQuotaKnown reports whether the row is safe to use for routing.
// Claude/Codex may omit the status or an inapplicable general window. They
// still need at least one measured general quota, and every present window
// must be usable. Native sources retain their explicit status/primary-window
// requirement. A projected cache rollover is not a fresh measurement.
func (u *UsageInfo) NumericQuotaKnown() bool {
	if u == nil || u.Error != "" || u.LimitStage != "" {
		return false
	}
	legacy := u.Provider == "" || u.Provider == "claude" || u.Provider == "codex"
	switch u.QuotaStatus {
	case "":
		if !legacy {
			return false
		}
	case QuotaOK:
	default:
		return false
	}
	if u.PrimaryWindow == nil && (!legacy || u.SecondaryWindow == nil) {
		return false
	}
	valid := func(w *UsageWindow) bool {
		return w == nil || (!w.Unmeasured && !w.Rolled && !math.IsNaN(w.Utilization) && !math.IsInf(w.Utilization, 0) &&
			w.Utilization >= 0 && w.Utilization <= 1 && w.UsedPercent >= 0 && w.UsedPercent <= 100)
	}
	if !valid(u.PrimaryWindow) || !valid(u.SecondaryWindow) || !valid(u.TertiaryWindow) {
		return false
	}
	for _, w := range u.ModelWindows {
		if !valid(w) {
			return false
		}
	}
	for _, w := range u.ScopedWindows {
		if !valid(w) {
			return false
		}
	}
	return true
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

// windowUtilization returns the stricter numeric representation as a 0-1
// fraction. Legacy rows can carry either field; a nonzero fraction must not
// hide an exhausted integer percentage, and fractional precision is retained.
func windowUtilization(w *UsageWindow) float64 {
	if w == nil {
		return 0
	}
	return math.Max(w.Utilization, float64(w.UsedPercent)/100)
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
// about to run: only the scoped windows for model constrain the score, so an
// account out of Fable capacity is still ranked as available for Sonnet work.
//
// An empty model means "unknown", and every scoped window is then taken into
// account at its worst.
func (u *UsageInfo) AvailabilityScoreForModel(model string) int {
	if u == nil || u.Error != "" || !u.NumericQuotaKnown() {
		return 0
	}

	// Base score starts at 100
	score := 100.0

	// Primary window is most important (weight: 50%)
	if u.PrimaryWindow != nil && !u.PrimaryWindow.Unmeasured {
		score -= windowUtilization(u.PrimaryWindow) * 50
	}

	// Secondary window (weight: 25%)
	if u.SecondaryWindow != nil && !u.SecondaryWindow.Unmeasured {
		score -= windowUtilization(u.SecondaryWindow) * 25
	}

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
	return append(out, u.modelWindowsForModel(model)...)
}

// modelWindowsForModel keeps every matching scoped constraint, rather than
// reducing simultaneous session, weekly, family and model-ID limits to one
// display winner. Older normalized rows may only contain ModelWindows, so
// both representations count. Sorting makes equal-window diagnostics stable
// without changing the source collection or the provider's measurements.
func (u *UsageInfo) modelWindowsForModel(model string) []*UsageWindow {
	if u == nil {
		return nil
	}
	out := make([]*UsageWindow, 0, len(u.ModelWindows)+len(u.ScopedWindows))
	seen := make(map[*UsageWindow]bool)
	appendWindow := func(key string, w *UsageWindow) {
		if !scopedWindowAppliesToModel(key, w, model) || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	keys := make([]string, 0, len(u.ModelWindows))
	for key := range u.ModelWindows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		appendWindow(key, u.ModelWindows[key])
	}
	for _, w := range u.ScopedWindows {
		appendWindow("", w)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return preferScopedWindow(out[i], out[j])
	})
	return out
}

// applicableWindows is the complete set used for quota and reset decisions.
// General windows always count; model restrictions are filtered together.
func (u *UsageInfo) applicableWindows(model string) []*UsageWindow {
	if u == nil {
		return nil
	}
	return append([]*UsageWindow{u.PrimaryWindow, u.SecondaryWindow}, u.premiumWindows(model)...)
}

// windowAppliesToModel reports whether a window's label or provider ID
// constrains model. A window without either identity constrains everything,
// and so does any window when the requested model is unknown.
func windowAppliesToModel(w *UsageWindow, model string) bool {
	return scopedWindowAppliesToModel("", w, model)
}

func scopedWindowAppliesToModel(key string, w *UsageWindow, model string) bool {
	if w == nil {
		return false
	}
	if model == "" || (key == "" && w.Label == "" && w.ModelID == "") {
		return true
	}
	want := NormalizeModelKey(model)
	return modelKeysMatch(want, NormalizeModelKey(key)) ||
		modelKeysMatch(want, NormalizeModelKey(w.Label)) ||
		modelKeysMatch(want, NormalizeModelKey(w.ModelID))
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
		if preferScopedWindow(w, worst) {
			worst = w
		}
	}
	return worst
}

// scopedWindowForModel returns the tightest matching model allowance, or nil.
// An exact identifier and a family/display-name match are simultaneous limits:
// neither may shadow the other just because its map key is more specific.
// Unlike WindowForModel this only reads the explicit scoped collections.
func (u *UsageInfo) scopedWindowForModel(model string) *UsageWindow {
	if model == "" {
		return nil
	}
	var worst *UsageWindow
	for _, w := range u.modelWindowsForModel(model) {
		if preferScopedWindow(w, worst) {
			worst = w
		}
	}
	return worst
}

// preferScopedWindow chooses the compact diagnostic projection without
// discarding the other constraints. Measurements precede display-only or
// rolled values; ties have a stable order independent of limits[] ordering.
// The same preference is used while parsing and when selecting the binding
// scope from the complete normalized row.
func preferScopedWindow(candidate, current *UsageWindow) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	candidateMeasured := !candidate.Unmeasured && !candidate.Rolled
	currentMeasured := !current.Unmeasured && !current.Rolled
	switch {
	case candidateMeasured != currentMeasured:
		return candidateMeasured
	case windowUtilization(candidate) != windowUtilization(current):
		return windowUtilization(candidate) > windowUtilization(current)
	case candidate.WindowDuration != current.WindowDuration:
		return candidate.WindowDuration > current.WindowDuration
	case !candidate.ResetsAt.Equal(current.ResetsAt):
		return candidate.ResetsAt.After(current.ResetsAt)
	case candidate.Kind != current.Kind:
		return candidate.Kind < current.Kind
	case candidate.ModelID != current.ModelID:
		return candidate.ModelID < current.ModelID
	case candidate.Label != current.Label:
		return candidate.Label < current.Label
	case candidate.Severity != current.Severity:
		return candidate.Severity < current.Severity
	case candidate.IsActive != current.IsActive:
		return candidate.IsActive
	case candidate.Unmeasured != current.Unmeasured:
		return !candidate.Unmeasured
	case candidate.Rolled != current.Rolled:
		return !candidate.Rolled
	default:
		return false
	}
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
// the general windows always count, but only model-scoped windows covering
// model do. An empty model means "unknown", and then every
// scoped window counts — an omitted scoped limit must never read as capacity
// (issue #97).
func (u *UsageInfo) IsNearLimitForModel(threshold float64, model string) bool {
	if u == nil {
		return false
	}

	atLimit := func(w *UsageWindow) bool {
		return w != nil && !w.Unmeasured && windowUtilization(w) >= threshold
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
	return u.EarliestResetForModel("")
}

// EarliestResetForModel returns the earliest known reset among the general
// quotas and all scopes that constrain model. An unrelated model's reset
// cannot make the requested work a drain candidate. Empty model includes all
// scopes; zero time means no applicable reset was reported.
func (u *UsageInfo) EarliestResetForModel(model string) time.Time {
	var earliest time.Time
	for _, w := range u.applicableWindows(model) {
		if w != nil && !w.ResetsAt.IsZero() && (earliest.IsZero() || w.ResetsAt.Before(earliest)) {
			earliest = w.ResetsAt
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
	return u.MostConstrainedWindowForModel("")
}

// MostConstrainedWindowForModel returns the most used general or applicable
// model allowance. An empty model conservatively includes every scoped limit.
// Callers still need NumericQuotaKnown before treating the result as measured
// available capacity.
func (u *UsageInfo) MostConstrainedWindowForModel(model string) *UsageWindow {
	if u == nil {
		return nil
	}

	var mostConstrained *UsageWindow
	var highestUtil float64

	checkWindow := func(w *UsageWindow) {
		if w == nil || w.Unmeasured {
			return
		}
		util := windowUtilization(w)
		if mostConstrained == nil || util > highestUtil {
			mostConstrained = w
			highestUtil = util
		}
	}

	for _, w := range u.applicableWindows(model) {
		checkWindow(w)
	}

	return mostConstrained
}

// WindowForModel returns the most constrained allowance for a specific model,
// including the legacy tertiary window only when it applies to that model.
// An empty model considers every scoped allowance.
func (u *UsageInfo) WindowForModel(model string) *UsageWindow {
	return u.ScopedLimit(model)
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
