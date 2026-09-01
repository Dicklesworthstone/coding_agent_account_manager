package health

import (
	"strings"
	"time"
)

// HealthStatus represents the overall health state of a profile.
type HealthStatus int

const (
	// StatusUnknown indicates health cannot be determined.
	StatusUnknown HealthStatus = iota
	// StatusHealthy indicates the profile is in good standing (token valid >1hr, no recent errors).
	StatusHealthy
	// StatusWarning indicates potential issues (token expiring <1hr or recent errors).
	StatusWarning
	// StatusCritical indicates the profile needs attention (token expired or many errors).
	StatusCritical
)

// String returns the string representation of a HealthStatus.
func (s HealthStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusWarning:
		return "warning"
	case StatusCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Icon returns the emoji icon for a HealthStatus.
func (s HealthStatus) Icon() string {
	switch s {
	case StatusHealthy:
		return "🟢"
	case StatusWarning:
		return "🟡"
	case StatusCritical:
		return "🔴"
	default:
		return "⚪"
	}
}

// PlanTier ranks a subscription plan by the usage headroom it buys. Health
// scoring and rotation scoring both rank plans, so the ordering lives here once
// instead of in a switch per package: adding a plan means editing PlanTierOf
// and nothing else.
type PlanTier int

const (
	// PlanTierUnrated covers free plans, blank values, and provider-specific
	// strings we do not recognize (for example "claude_pro_2025"). These earn
	// no scoring bonus.
	PlanTierUnrated PlanTier = iota
	// PlanTierStandard covers entry paid seats: pro, plus, team.
	PlanTierStandard
	// PlanTierHighVolume covers premium individual seats with substantially
	// larger quotas: Claude Max, Gemini Ultra, and similar.
	PlanTierHighVolume
	// PlanTierEnterprise covers negotiated enterprise contracts, which have
	// the most headroom and are the safest to route work to.
	PlanTierEnterprise
)

// PlanTierOf maps a plan string (as stored by normalizePlanType: lowercase and
// trimmed) to its tier. Unknown values are deliberately unrated rather than
// guessed at, so a new provider spelling degrades to "no bonus" instead of a
// wrong one.
func PlanTierOf(planType string) PlanTier {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "enterprise":
		return PlanTierEnterprise
	case "max", "ultra", "premium":
		return PlanTierHighVolume
	case "pro", "plus", "team":
		return PlanTierStandard
	default:
		return PlanTierUnrated
	}
}

// HealthConfig defines thresholds for health calculation.
type HealthConfig struct {
	TokenExpiryWarningMinutes  int
	TokenExpiryCriticalMinutes int
	ErrorCountWarning          int
	ErrorCountCritical         int
}

// DefaultHealthConfig returns standard thresholds.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		TokenExpiryWarningMinutes:  60,
		TokenExpiryCriticalMinutes: 15,
		ErrorCountWarning:          1, // Any error is a warning
		ErrorCountCritical:         3, // 3+ errors is critical
	}
}

// CalculateStatus determines the health status from ProfileHealth data using default config.
// This is a wrapper around CalculateHealth for backward compatibility/simplicity.
func CalculateStatus(health *ProfileHealth) HealthStatus {
	status, _ := CalculateHealth(health, DefaultHealthConfig())
	return status
}

// CalculateHealth performs detailed health scoring based on multiple factors.
// Returns the status and the raw numerical score.
func CalculateHealth(h *ProfileHealth, config HealthConfig) (HealthStatus, float64) {
	if h == nil {
		return StatusUnknown, 0
	}

	score := 0.0
	now := time.Now()

	// Factor 1: Token expiry (primary)
	if h.TokenExpiresAt.IsZero() {
		// Unknown expiry - neutral
	} else if h.TokenExpiresAt.Before(now) {
		score -= 1.0 // Expired
	} else {
		ttl := h.TokenExpiresAt.Sub(now)
		switch {
		case ttl > time.Duration(config.TokenExpiryWarningMinutes)*time.Minute:
			score += 1.0 // Healthy
		case ttl > time.Duration(config.TokenExpiryCriticalMinutes)*time.Minute:
			score += 0.5 // Warning zone
		default:
			// < Critical threshold: no bonus (effectively warning/critical)
		}
	}

	// Factor 2: Recent errors
	switch {
	case h.ErrorCount1h == 0:
		score += 0.3
	case h.ErrorCount1h <= config.ErrorCountWarning:
		// Neutral
	default:
		score -= 0.5
	}

	// Factor 3: Plan type bonus, ranked by PlanTierOf so this agrees with the
	// rotation scorer.
	switch PlanTierOf(h.PlanType) {
	case PlanTierEnterprise:
		score += 0.3
	case PlanTierHighVolume:
		score += 0.25
	case PlanTierStandard:
		score += 0.2
	case PlanTierUnrated:
		// Free/unknown plans get no bonus.
	}

	// Factor 4: Penalty (from errors, with decay)
	score -= h.Penalty

	// Convert to status
	status := StatusHealthy
	if score < 0 {
		status = StatusCritical
	} else if score <= 0.5 {
		status = StatusWarning
	}

	// Override if token is strictly expired or critical errors met
	if !h.TokenExpiresAt.IsZero() {
		if h.TokenExpiresAt.Before(now) {
			if h.RateLimited(now) {
				// An active rate-limit cooldown outranks the recorded expiry:
				// the account recovers on the reset timer, not via re-login,
				// and the expiry timestamp may come from a stale vault
				// snapshot while the live token is still valid. Reporting a
				// cap as token-expired misdirects the operator (PR #82).
				if status == StatusCritical && h.ErrorCount1h < config.ErrorCountCritical {
					status = StatusWarning
				}
			} else {
				status = StatusCritical
			}
		} else {
			ttl := h.TokenExpiresAt.Sub(now)
			criticalTTL := time.Duration(config.TokenExpiryCriticalMinutes) * time.Minute
			warningTTL := time.Duration(config.TokenExpiryWarningMinutes) * time.Minute
			if criticalTTL > 0 && ttl <= criticalTTL {
				status = StatusCritical
			} else if warningTTL > 0 && ttl <= warningTTL {
				// If within warning window (e.g. < 1h), ensure at least Warning
				if status == StatusHealthy {
					status = StatusWarning
				}
			}
		}
	}

	// An active cap is a real (if temporary) constraint: never report a
	// rate-limited profile as fully healthy.
	if h.RateLimited(now) && status == StatusHealthy {
		status = StatusWarning
	}

	if h.ErrorCount1h >= config.ErrorCountCritical {
		status = StatusCritical
	}

	return status, score
}
