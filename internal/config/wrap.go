// Package config wrap configuration for rate limit retry behavior.
package config

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

// WrapConfig holds configuration for the wrap command's retry and backoff behavior.
type WrapConfig struct {
	// MaxRetries is the maximum number of retry attempts on rate limit.
	// Set to 0 for no retries. Default: 3
	MaxRetries int `json:"max_retries"`

	// InitialDelay is the delay before the first retry.
	// Default: 30s
	InitialDelay Duration `json:"initial_delay"`

	// MaxDelay is the maximum delay between retries.
	// Default: 5m
	MaxDelay Duration `json:"max_delay"`

	// BackoffMultiplier is the factor by which delay increases after each retry.
	// Default: 2.0
	BackoffMultiplier float64 `json:"backoff_multiplier"`

	// Jitter adds randomization to delays to prevent thundering herd.
	// When true, delays vary by ±20%. Default: true
	Jitter bool `json:"jitter"`

	// CooldownDuration is how long a profile stays in cooldown after hitting a rate limit.
	// Default: 60m
	CooldownDuration Duration `json:"cooldown_duration"`

	// Providers contains per-provider overrides.
	// Example: {"claude": {"max_retries": 5}}
	Providers map[string]*WrapOverride `json:"providers,omitempty"`
}

// WrapOverride holds only the settings explicitly supplied for a provider.
// Pointers preserve the distinction between an omitted setting and zero/false.
type WrapOverride struct {
	MaxRetries        *int      `json:"max_retries,omitempty"`
	InitialDelay      *Duration `json:"initial_delay,omitempty"`
	MaxDelay          *Duration `json:"max_delay,omitempty"`
	BackoffMultiplier *float64  `json:"backoff_multiplier,omitempty"`
	Jitter            *bool     `json:"jitter,omitempty"`
	CooldownDuration  *Duration `json:"cooldown_duration,omitempty"`
}

// DefaultWrapConfig returns a WrapConfig with sensible defaults.
func DefaultWrapConfig() WrapConfig {
	return WrapConfig{
		MaxRetries:        3,
		InitialDelay:      Duration(30 * time.Second),
		MaxDelay:          Duration(5 * time.Minute),
		BackoffMultiplier: 2.0,
		Jitter:            true,
		CooldownDuration:  Duration(60 * time.Minute),
	}
}

// ForProvider returns the effective config for a specific provider.
// It merges per-provider overrides with the base config.
func (c *WrapConfig) ForProvider(provider string) WrapConfig {
	// Start with a copy of the base config
	result := *c
	result.Providers = nil // Don't copy nested providers

	// Apply per-provider overrides if they exist
	if c.Providers == nil {
		return result
	}

	override, exists := c.Providers[provider]
	if !exists || override == nil {
		return result
	}

	if override.MaxRetries != nil {
		result.MaxRetries = *override.MaxRetries
	}
	if override.InitialDelay != nil {
		result.InitialDelay = *override.InitialDelay
	}
	if override.MaxDelay != nil {
		result.MaxDelay = *override.MaxDelay
	}
	if override.BackoffMultiplier != nil {
		result.BackoffMultiplier = *override.BackoffMultiplier
	}
	if override.Jitter != nil {
		result.Jitter = *override.Jitter
	}
	if override.CooldownDuration != nil {
		result.CooldownDuration = *override.CooldownDuration
	}

	return result
}

// Validate checks effective settings after provider and CLI overrides are applied.
// Zero delays allow immediate retries; max_delay must still cover initial_delay.
func (c WrapConfig) Validate() error {
	if c.MaxRetries < 0 {
		return fmt.Errorf("max_retries must be nonnegative")
	}
	if c.InitialDelay < 0 {
		return fmt.Errorf("initial_delay must be nonnegative")
	}
	if c.MaxDelay < 0 {
		return fmt.Errorf("max_delay must be nonnegative")
	}
	if c.CooldownDuration < 0 {
		return fmt.Errorf("cooldown_duration must be nonnegative")
	}
	if c.BackoffMultiplier <= 0 || math.IsNaN(c.BackoffMultiplier) || math.IsInf(c.BackoffMultiplier, 0) {
		return fmt.Errorf("backoff_multiplier must be finite and positive")
	}
	if c.InitialDelay > c.MaxDelay {
		return fmt.Errorf("initial_delay must not exceed max_delay")
	}
	return nil
}

// NextDelay calculates the delay before the next retry attempt.
// The delay uses exponential backoff with optional jitter.
//
// Formula: min(min(initial * multiplier^attempt, max) * jitter, max).
// Jitter varies between 0.8 and 1.2; the final delay never exceeds max_delay.
func (c *WrapConfig) NextDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	initial := c.InitialDelay.Duration()
	maxDelay := c.MaxDelay.Duration()
	if initial <= 0 || maxDelay <= 0 {
		return 0
	}

	multiplier := c.BackoffMultiplier
	if multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		multiplier = 2.0
	}

	delay := float64(initial) * math.Pow(multiplier, float64(attempt))

	// Clamp overflow from exponential growth before applying jitter.
	limit := float64(maxDelay)
	if delay > limit {
		delay = limit
	}

	if c.Jitter {
		jitterFactor := 0.8 + rand.Float64()*0.4
		delay *= jitterFactor
	}

	// Compare before converting: float64(MaxInt64) rounds up past Duration's
	// range, so converting a capped float directly can produce a negative wait.
	if delay >= limit {
		return maxDelay
	}
	return time.Duration(delay)
}

// ShouldRetry returns true if another retry should be attempted.
func (c *WrapConfig) ShouldRetry(attempt int) bool {
	return attempt < c.MaxRetries
}
