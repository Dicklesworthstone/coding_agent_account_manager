// Package prediction provides depletion prediction for rate limit management.
// It combines multiple data sources (real-time session, historical logs, API)
// to forecast when rate limits will be hit.
package prediction

import (
	"context"
	"math"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// WarningLevel indicates the urgency of predicted depletion.
type WarningLevel int

const (
	// WarningNone means no imminent depletion.
	WarningNone WarningLevel = iota
	// WarningApproaching means depletion expected within 30 minutes.
	WarningApproaching
	// WarningImminent means depletion expected within 10 minutes.
	WarningImminent
)

// String returns a human-readable warning level.
func (w WarningLevel) String() string {
	switch w {
	case WarningApproaching:
		return "approaching"
	case WarningImminent:
		return "imminent"
	default:
		return "none"
	}
}

// Prediction contains the result of a depletion forecast.
type Prediction struct {
	// Profile is the profile name being predicted.
	Profile string `json:"profile"`

	// Provider is the provider (claude, codex, gemini).
	Provider string `json:"provider"`

	// CurrentPercent is the current utilization percentage (0-100).
	CurrentPercent float64 `json:"current_percent"`

	// CurrentPercentKnown distinguishes measured zero usage from missing quota.
	// A measurement can be known even when no depletion forecast is possible.
	CurrentPercentKnown bool `json:"current_percent_known"`

	// BurnRate contains token consumption rate data.
	BurnRate *usage.BurnRateInfo `json:"burn_rate,omitempty"`

	// PredictedTime is when depletion is expected.
	PredictedTime time.Time `json:"predicted_time,omitempty"`

	// TimeToDepletion is the duration until depletion.
	TimeToDepletion time.Duration `json:"time_to_depletion"`

	// Confidence is how reliable the prediction is (0-1).
	Confidence float64 `json:"confidence"`

	// Warning indicates the urgency level.
	Warning WarningLevel `json:"warning"`

	// DataSources lists where burn rate data came from.
	// Values: "session", "logs", "api", "current_usage" (already exhausted).
	DataSources []string `json:"data_sources"`

	// Error contains any error message from prediction.
	Error string `json:"error,omitempty"`
}

// hasCurrentPercent reports whether the current measurement is usable,
// independently of whether the burn rate supports a forecast.
func (p *Prediction) hasCurrentPercent() bool {
	return p != nil && p.CurrentPercentKnown && !math.IsNaN(p.CurrentPercent) &&
		!math.IsInf(p.CurrentPercent, 0) && p.CurrentPercent >= 0 && p.CurrentPercent <= 100
}

// IsValid reports whether a forecast or observed exhaustion has usable evidence.
// CurrentPercentKnown remains available when only the forecast is unavailable.
func (p *Prediction) IsValid() bool {
	return p.hasCurrentPercent() && p.Error == "" && len(p.DataSources) > 0 &&
		!math.IsNaN(p.Confidence) && !math.IsInf(p.Confidence, 0) && p.Confidence >= 0 && p.Confidence <= 1 &&
		p.TimeToDepletion >= 0
}

// ShouldRotate returns true if rotation is recommended based on prediction.
func (p *Prediction) ShouldRotate(threshold time.Duration) bool {
	if !p.IsValid() || p.TimeToDepletion <= 0 {
		return false
	}
	return p.TimeToDepletion < threshold && p.Confidence >= 0.3
}

// PredictionEngine combines multiple data sources for depletion prediction.
type PredictionEngine struct {
	logScanner     logs.Scanner
	sessionTracker *usage.SessionTracker

	// Configuration
	logWindow     time.Duration // How far back to look in logs
	sessionWindow time.Duration // Window for session burn rate
	model         string        // Requested model; empty includes all scopes
}

// EngineOption configures a PredictionEngine.
type EngineOption func(*PredictionEngine)

// WithModel limits predictions to general quotas and restrictions applicable
// to the requested model. An empty model conservatively includes every scope.
func WithModel(model string) EngineOption {
	return func(e *PredictionEngine) {
		e.model = model
	}
}

// WithLogScanner sets the log scanner for historical data.
func WithLogScanner(s logs.Scanner) EngineOption {
	return func(e *PredictionEngine) {
		e.logScanner = s
	}
}

// WithSessionTracker sets the session tracker for real-time data.
func WithSessionTracker(t *usage.SessionTracker) EngineOption {
	return func(e *PredictionEngine) {
		e.sessionTracker = t
	}
}

// WithLogWindow sets how far back to look in log data.
func WithLogWindow(d time.Duration) EngineOption {
	return func(e *PredictionEngine) {
		e.logWindow = d
	}
}

// WithSessionWindow sets the window for session burn rate calculation.
func WithSessionWindow(d time.Duration) EngineOption {
	return func(e *PredictionEngine) {
		e.sessionWindow = d
	}
}

// NewPredictionEngine creates a new prediction engine.
func NewPredictionEngine(opts ...EngineOption) *PredictionEngine {
	e := &PredictionEngine{
		logWindow:     2 * time.Hour,    // Default: look back 2 hours
		sessionWindow: 30 * time.Minute, // Default: 30 min window for session
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

// Predict forecasts when the rate limit will be hit for a profile.
// It tries data sources in order of preference:
// 1. Real-time session data (most accurate)
// 2. Historical log data (good accuracy)
// 3. API-reported burn rate (if available in usageInfo)
func (e *PredictionEngine) Predict(ctx context.Context, usageInfo *usage.UsageInfo) *Prediction {
	pred, window := e.currentUsagePrediction(usageInfo)
	if window == nil {
		return pred
	}

	// Already at limit?
	if pred.CurrentPercent >= 100 {
		pred.Warning = WarningImminent
		pred.PredictedTime = time.Now()
		pred.TimeToDepletion = 0
		pred.DataSources = append(pred.DataSources, "current_usage")
		pred.Confidence = 1.0 // Certain - already depleted
		return pred
	}

	// Try to get burn rate from multiple sources
	// Only use a source if it has valid PercentPerHour (requires TokenLimit)
	var burnRate *usage.BurnRateInfo

	// Priority 1: Real-time session data (most accurate, most recent)
	if e.sessionTracker != nil {
		if sessionRate := e.sessionTracker.BurnRate(e.sessionWindow); usablePercentRate(sessionRate) {
			burnRate = sessionRate
			pred.DataSources = append(pred.DataSources, "session")
		}
	}

	// Priority 2: Historical log data
	if burnRate == nil && e.logScanner != nil {
		if result, err := e.logScanner.Scan(ctx, "", time.Now().Add(-e.logWindow)); err == nil && len(result.Entries) > 0 {
			logRate := usage.CalculateBurnRate(result.Entries, e.logWindow, nil)
			if usablePercentRate(logRate) {
				burnRate = logRate
				pred.DataSources = append(pred.DataSources, "logs")
			}
		}
	}

	// Priority 3: API-reported burn rate (always has PercentPerHour if set)
	if burnRate == nil && usablePercentRate(usageInfo.BurnRate) {
		burnRate = usageInfo.BurnRate
		pred.DataSources = append(pred.DataSources, "api")
	}

	// If we have no burn rate data, we can't predict
	if burnRate == nil {
		pred.Error = "insufficient data for prediction"
		return pred
	}

	pred.BurnRate = burnRate

	e.forecastAtRate(pred, window, burnRate)
	return pred
}

// PredictWithBurnRate creates a prediction using an externally-provided burn rate.
// Useful when burn rate is already calculated elsewhere.
func (e *PredictionEngine) PredictWithBurnRate(usageInfo *usage.UsageInfo, burnRate *usage.BurnRateInfo, source string) *Prediction {
	pred, window := e.currentUsagePrediction(usageInfo)
	if window == nil {
		return pred
	}

	if pred.CurrentPercent >= 100 {
		pred.Warning = WarningImminent
		pred.PredictedTime = time.Now()
		pred.TimeToDepletion = 0
		pred.DataSources = append(pred.DataSources, "current_usage")
		pred.Confidence = 1.0 // Certain - already depleted
		return pred
	}

	if !usablePercentRate(burnRate) {
		pred.Error = "no burn rate data"
		return pred
	}
	pred.DataSources = append(pred.DataSources, source)
	pred.BurnRate = burnRate

	e.forecastAtRate(pred, window, burnRate)
	return pred
}

// currentUsagePrediction preserves a trustworthy measurement even when the
// caller cannot obtain a burn rate. Degraded or rolled quota cannot establish
// healthy usage, including when a diagnostic window still has a percentage.
func (e *PredictionEngine) currentUsagePrediction(info *usage.UsageInfo) (*Prediction, *usage.UsageWindow) {
	pred := &Prediction{DataSources: make([]string, 0)}
	if info == nil {
		pred.Error = "no usage info provided"
		return pred, nil
	}
	pred.Profile = info.ProfileName
	pred.Provider = info.Provider
	if !info.NumericQuotaKnown() {
		pred.Error = "no measured quota available"
		return pred, nil
	}
	window := info.MostConstrainedWindowForModel(e.model)
	if window == nil {
		pred.Error = "no usage window available"
		return pred, nil
	}
	pred.CurrentPercent = math.Max(window.Utilization*100, float64(window.UsedPercent))
	pred.CurrentPercentKnown = true
	return pred, window
}

func usablePercentRate(rate *usage.BurnRateInfo) bool {
	return rate != nil && rate.PercentPerHour > 0 && !math.IsNaN(rate.PercentPerHour) && !math.IsInf(rate.PercentPerHour, 0)
}

// forecastAtRate only publishes a time within the measured window. A reset
// ends that measurement's horizon; it is neither depletion nor evidence of
// how much quota will be available afterward.
func (e *PredictionEngine) forecastAtRate(pred *Prediction, window *usage.UsageWindow, rate *usage.BurnRateInfo) {
	nanoseconds := (100 - pred.CurrentPercent) / rate.PercentPerHour * float64(time.Hour)
	if math.IsNaN(nanoseconds) || math.IsInf(nanoseconds, 0) || nanoseconds < 1 || nanoseconds >= float64(math.MaxInt64) {
		pred.Error = "depletion time is outside the supported range"
		return
	}
	duration := time.Duration(nanoseconds)
	predicted := time.Now().Add(duration)
	if !window.ResetsAt.IsZero() && !predicted.Before(window.ResetsAt) {
		pred.Error = "quota resets before predicted depletion"
		return
	}
	pred.TimeToDepletion = duration
	pred.PredictedTime = predicted
	if duration < 10*time.Minute {
		pred.Warning = WarningImminent
	} else if duration < 30*time.Minute {
		pred.Warning = WarningApproaching
	}
	pred.Confidence = e.calculateConfidence(rate, len(pred.DataSources))
}

// calculateConfidence determines prediction reliability based on data quality.
func (e *PredictionEngine) calculateConfidence(burnRate *usage.BurnRateInfo, sourceCount int) float64 {
	if burnRate == nil || math.IsNaN(burnRate.Confidence) || math.IsInf(burnRate.Confidence, 0) || burnRate.Confidence < 0 {
		return 0
	}

	// Base confidence from burn rate calculation
	confidence := burnRate.Confidence

	// Boost for multiple data sources (up to 20%)
	if sourceCount > 1 {
		boost := float64(sourceCount-1) * 0.1
		confidence = math.Min(1.0, confidence*(1+boost))
	}

	// Slight penalty if sample size is very small
	if burnRate.SampleSize < 5 {
		confidence *= 0.9
	}

	return math.Min(confidence, 1.0)
}

// PredictAll creates predictions for multiple profiles.
func (e *PredictionEngine) PredictAll(ctx context.Context, profiles []*usage.UsageInfo) []*Prediction {
	predictions := make([]*Prediction, len(profiles))
	for i, info := range profiles {
		predictions[i] = e.Predict(ctx, info)
	}
	return predictions
}

// MostUrgent returns the prediction with the shortest time to depletion.
// Only considers predictions with confidence >= minConfidence.
func MostUrgent(predictions []*Prediction, minConfidence float64) *Prediction {
	var urgent *Prediction

	for _, p := range predictions {
		if !p.IsValid() || p.Confidence < minConfidence {
			continue
		}
		if p.TimeToDepletion <= 0 {
			continue
		}
		if urgent == nil || p.TimeToDepletion < urgent.TimeToDepletion {
			urgent = p
		}
	}

	return urgent
}

// FilterByWarning returns predictions at or above the specified warning level.
func FilterByWarning(predictions []*Prediction, minWarning WarningLevel) []*Prediction {
	var result []*Prediction
	for _, p := range predictions {
		if p.IsValid() && p.Warning >= minWarning {
			result = append(result, p)
		}
	}
	return result
}
