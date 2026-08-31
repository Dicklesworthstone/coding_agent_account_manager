package health

import "time"

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

	// An active rate-limit cooldown is the real constraint. It must score as
	// a warning (the cap is real) and must not be outranked by a stale
	// snapshot expiry that would otherwise go critical (agent-factory-21fp).
	rateLimited := !h.CooldownUntil.IsZero() && h.CooldownUntil.After(now)
	staleSnapshotExpiry := !h.TokenExpiresAt.IsZero() && h.TokenExpiresAt.Before(now) && !h.ExpiryLive
	refreshableAccessExpiry := !h.TokenExpiresAt.IsZero() && h.TokenExpiresAt.Before(now) && h.ExpiryLive && h.HasRefreshToken && h.ErrorCount1h == 0
	confirmedExpiry := !h.TokenExpiresAt.IsZero() && h.TokenExpiresAt.Before(now) && h.ExpiryLive && !refreshableAccessExpiry

	// Factor 1: Token expiry (primary) — only the LIVE credential counts.
	if h.TokenExpiresAt.IsZero() || staleSnapshotExpiry {
		// Unknown / stale snapshot: neutral.
	} else if refreshableAccessExpiry {
		score += 1.0 // access token expired, refresh still valid — self-healing, silent
	} else if confirmedExpiry {
		score -= 1.0 // Expired live access token without a usable refresh
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

	// Factor 3: Plan type bonus
	switch h.PlanType {
	case "enterprise":
		score += 0.3
	case "pro", "team":
		score += 0.2
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

	// Override if the LIVE token is strictly expired, or critical errors met.
	// A stale snapshot expiry with error_count 0 reports unknown, never
	// critical — that was the 2026-08-30 misroute (agent-factory-21fp).
	if staleSnapshotExpiry && h.ErrorCount1h == 0 && !rateLimited {
		status = StatusUnknown
	} else if confirmedExpiry {
		status = StatusCritical
	} else if !h.TokenExpiresAt.IsZero() && h.TokenExpiresAt.After(now) {
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

	if rateLimited && status == StatusHealthy {
		status = StatusWarning
	}

	if h.ErrorCount1h >= config.ErrorCountCritical {
		status = StatusCritical
	}

	return status, score
}
