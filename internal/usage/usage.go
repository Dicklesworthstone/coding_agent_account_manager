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
	PrimaryWindow *UsageWindow `json:"primary_window,omitempty"`

	// SecondaryWindow is the secondary window (usually longer).
	SecondaryWindow *UsageWindow `json:"secondary_window,omitempty"`

	// Credits contains credit balance info (Codex only).
	Credits *CreditInfo `json:"credits,omitempty"`

	// FetchedAt is when this usage info was fetched.
	FetchedAt time.Time `json:"fetched_at"`

	// Error contains any error message from fetching.
	Error string `json:"error,omitempty"`
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

	// Primary window is most important (weight: 60%)
	if u.PrimaryWindow != nil {
		primaryUtil := u.PrimaryWindow.Utilization
		if primaryUtil == 0 && u.PrimaryWindow.UsedPercent > 0 {
			primaryUtil = float64(u.PrimaryWindow.UsedPercent) / 100.0
		}
		score -= primaryUtil * 60
	}

	// Secondary window (weight: 30%)
	if u.SecondaryWindow != nil {
		secondaryUtil := u.SecondaryWindow.Utilization
		if secondaryUtil == 0 && u.SecondaryWindow.UsedPercent > 0 {
			secondaryUtil = float64(u.SecondaryWindow.UsedPercent) / 100.0
		}
		score -= secondaryUtil * 30
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

	return false
}

// TimeUntilReset returns the shortest time until any window resets.
func (u *UsageInfo) TimeUntilReset() time.Duration {
	if u == nil {
		return 0
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

	if earliest.IsZero() {
		return 0
	}

	ttl := time.Until(earliest)
	if ttl < 0 {
		return 0
	}
	return ttl
}
