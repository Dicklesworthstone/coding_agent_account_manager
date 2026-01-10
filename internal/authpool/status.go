// Package authpool provides authentication pool management for tracking
// profile token freshness and coordinating profile rotation.
package authpool

import (
	"time"
)

// PoolStatus represents the authentication state of a pooled profile.
type PoolStatus int

const (
	// PoolStatusUnknown indicates the profile state is not yet determined.
	PoolStatusUnknown PoolStatus = iota
	// PoolStatusReady indicates the token is valid and ready to use.
	PoolStatusReady
	// PoolStatusRefreshing indicates a token refresh is in progress.
	PoolStatusRefreshing
	// PoolStatusExpired indicates the token has expired and needs refresh.
	PoolStatusExpired
	// PoolStatusCooldown indicates the profile hit rate limits and is cooling down.
	PoolStatusCooldown
	// PoolStatusError indicates refresh or validation failed.
	PoolStatusError
)

// String returns a human-readable status name.
func (s PoolStatus) String() string {
	switch s {
	case PoolStatusReady:
		return "ready"
	case PoolStatusRefreshing:
		return "refreshing"
	case PoolStatusExpired:
		return "expired"
	case PoolStatusCooldown:
		return "cooldown"
	case PoolStatusError:
		return "error"
	default:
		return "unknown"
	}
}

// IsUsable returns true if the profile can be used for requests.
func (s PoolStatus) IsUsable() bool {
	return s == PoolStatusReady
}

// NeedsRefresh returns true if the profile needs a token refresh.
func (s PoolStatus) NeedsRefresh() bool {
	return s == PoolStatusExpired || s == PoolStatusError
}

// PooledProfile represents a profile tracked in the authentication pool.
type PooledProfile struct {
	// Provider is the provider ID (e.g., "claude", "codex", "gemini").
	Provider string `json:"provider"`

	// ProfileName is the CAAM profile name.
	ProfileName string `json:"profile_name"`

	// Status is the current authentication state.
	Status PoolStatus `json:"status"`

	// TokenExpiry is when the current token expires.
	// Zero time means unknown or no expiry.
	TokenExpiry time.Time `json:"token_expiry,omitempty"`

	// LastRefresh is when the token was last refreshed.
	LastRefresh time.Time `json:"last_refresh,omitempty"`

	// LastCheck is when the profile state was last validated.
	LastCheck time.Time `json:"last_check,omitempty"`

	// LastUsed is when the profile was last used for a request.
	LastUsed time.Time `json:"last_used,omitempty"`

	// CooldownUntil is when a rate-limited profile can be used again.
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`

	// ErrorCount is the number of consecutive errors.
	ErrorCount int `json:"error_count,omitempty"`

	// ErrorMessage contains the last error message.
	ErrorMessage string `json:"error_message,omitempty"`

	// Priority is a weight for selection (higher = preferred).
	Priority int `json:"priority,omitempty"`
}

// Key returns a unique identifier for this profile.
func (p *PooledProfile) Key() string {
	return p.Provider + ":" + p.ProfileName
}

// IsExpired returns true if the token has expired.
func (p *PooledProfile) IsExpired() bool {
	if p.TokenExpiry.IsZero() {
		return false // Unknown expiry, assume valid
	}
	return time.Now().After(p.TokenExpiry)
}

// IsExpiringSoon returns true if the token expires within the given threshold.
func (p *PooledProfile) IsExpiringSoon(threshold time.Duration) bool {
	if p.TokenExpiry.IsZero() {
		return false
	}
	return time.Until(p.TokenExpiry) < threshold
}

// IsInCooldown returns true if the profile is in rate limit cooldown.
func (p *PooledProfile) IsInCooldown() bool {
	if p.CooldownUntil.IsZero() {
		return false
	}
	return time.Now().Before(p.CooldownUntil)
}

// TimeUntilExpiry returns the duration until token expiry.
// Returns 0 if already expired or expiry is unknown.
func (p *PooledProfile) TimeUntilExpiry() time.Duration {
	if p.TokenExpiry.IsZero() {
		return 0
	}
	ttl := time.Until(p.TokenExpiry)
	if ttl < 0 {
		return 0
	}
	return ttl
}

// TimeInCooldown returns how long until cooldown ends.
// Returns 0 if not in cooldown.
func (p *PooledProfile) TimeInCooldown() time.Duration {
	if p.CooldownUntil.IsZero() {
		return 0
	}
	ttl := time.Until(p.CooldownUntil)
	if ttl < 0 {
		return 0
	}
	return ttl
}

// Clone returns a copy of the profile.
func (p *PooledProfile) Clone() *PooledProfile {
	if p == nil {
		return nil
	}
	clone := *p
	return &clone
}
