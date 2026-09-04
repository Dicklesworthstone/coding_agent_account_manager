package health

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// Styles for health status display
var (
	healthyStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#50fa7b")) // Green
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f1fa8c")) // Yellow
	criticalStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5555")) // Red
	unknownStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#6272a4")) // Gray
)

// FormatOptions controls how health status is formatted.
type FormatOptions struct {
	NoColor    bool // Disable color output
	ShowReason bool // Include reason in output
	Compact    bool // Use compact format
}

// FormatHealthStatus returns a formatted string for the health status.
// Example outputs: "🟢 59m left", "🟡 12m left", "🔴 Expired"
func FormatHealthStatus(status HealthStatus, health *ProfileHealth, opts FormatOptions) string {
	icon := status.Icon()

	var text string
	if health == nil {
		text = "Unknown"
	} else if !health.TokenExpiresAt.IsZero() {
		ttl := time.Until(health.TokenExpiresAt)
		switch {
		case ttl > 0:
			text = FormatTimeRemaining(health.TokenExpiresAt)
		case health.CredentialRenewable():
			// The credential renews without a human — the provider's CLI does
			// it on next use, or caam refreshes it from the stored refresh
			// token. "Expired" would read as a dead account (PR #84, #102).
			text = "Auto-refresh"
		default:
			text = "Expired"
		}
	} else {
		// No expiry info
		switch status {
		case StatusHealthy:
			text = "Valid"
		case StatusWarning:
			if health.ErrorCount1h > 0 {
				text = fmt.Sprintf("%d errors", health.ErrorCount1h)
			} else {
				text = "Warning"
			}
		case StatusCritical:
			if health.ErrorCount1h >= 3 {
				text = fmt.Sprintf("%d errors", health.ErrorCount1h)
			} else {
				text = "Critical"
			}
		default:
			text = "Unknown"
		}
	}

	result := fmt.Sprintf("%s %s", icon, text)

	if !opts.NoColor {
		result = colorizeStatus(status, result)
	}

	return result
}

// FormatTimeRemaining returns a human-readable time remaining string.
// Examples: "59m left", "23h left", "3d left", "< 1m left"
func FormatTimeRemaining(expiry time.Time) string {
	ttl := time.Until(expiry)
	if ttl <= 0 {
		return "Expired"
	}

	// Round to avoid showing seconds
	ttl = ttl.Round(time.Minute)

	switch {
	case ttl < time.Minute:
		return "< 1m left"
	case ttl < time.Hour:
		return fmt.Sprintf("%dm left", int(ttl.Minutes()))
	case ttl < 24*time.Hour:
		hours := int(ttl.Hours())
		mins := int(ttl.Minutes()) % 60
		if mins > 0 && hours < 12 {
			return fmt.Sprintf("%dh%dm left", hours, mins)
		}
		return fmt.Sprintf("%dh left", hours)
	default:
		days := int(ttl.Hours() / 24)
		return fmt.Sprintf("%dd left", days)
	}
}

// StatusReasons lists the human-readable causes behind a profile's health
// verdict, most important first.
//
// An active rate-limit cooldown is reported first and suppresses the
// "Token expired" reason: a capped account recovers on the reset timer, and
// its recorded expiry may come from a stale vault snapshot while the live
// token is still valid, so blaming the token misdirects the operator toward
// a re-login that fixes nothing (PR #82).
func StatusReasons(h *ProfileHealth) []string {
	if h == nil {
		return nil
	}

	var reasons []string
	now := time.Now()
	rateLimited := h.RateLimited(now)

	if rateLimited {
		reasons = append(reasons, fmt.Sprintf("Rate limited (resets in %s)", formatDurationNatural(h.RateLimitedUntil.Sub(now))))
	}

	// Check token expiry. A renewable credential is skipped: it is renewed in
	// place by the provider's CLI or by caam's refresher, so its TTL is not a
	// reason for the account's verdict (PR #84, issue #102). Refresh
	// scheduling reads Signals.RefreshDue instead, which stays true for a
	// renewable-but-lapsed Codex or Grok credential.
	if !h.TokenExpiresAt.IsZero() && !h.CredentialRenewable() {
		ttl := h.TokenExpiresAt.Sub(now)
		if ttl <= 0 {
			if !rateLimited {
				reasons = append(reasons, "Token expired")
			}
		} else if ttl < time.Hour {
			reasons = append(reasons, fmt.Sprintf("Token expires in %s", formatDurationNatural(ttl)))
		}
	}

	// Check errors
	if h.ErrorCount1h > 0 {
		if h.ErrorCount1h == 1 {
			reasons = append(reasons, "1 recent error")
		} else {
			reasons = append(reasons, fmt.Sprintf("%d recent errors", h.ErrorCount1h))
		}
	}

	// Check penalty
	if h.Penalty >= 1.0 {
		reasons = append(reasons, "High penalty from errors")
	}

	return reasons
}

// FormatStatusWithReason returns a detailed status string with explanation.
// Example: "🟡 Warning - Token expires in 12 minutes"
func FormatStatusWithReason(status HealthStatus, health *ProfileHealth, opts FormatOptions) string {
	icon := status.Icon()
	statusStr := status.String()

	reasons := StatusReasons(health)

	var result string
	if len(reasons) > 0 {
		result = fmt.Sprintf("%s %s - %s", icon, capitalizeFirst(statusStr), strings.Join(reasons, ", "))
	} else {
		switch status {
		case StatusHealthy:
			result = fmt.Sprintf("%s Healthy", icon)
		case StatusWarning:
			result = fmt.Sprintf("%s Warning", icon)
		case StatusCritical:
			result = fmt.Sprintf("%s Critical", icon)
		default:
			result = fmt.Sprintf("%s Unknown", icon)
		}
	}

	if !opts.NoColor {
		result = colorizeStatus(status, result)
	}

	return result
}

// FormatRecommendation returns a recommendation for fixing issues.
func FormatRecommendation(provider, profile string, health *ProfileHealth) string {
	if health == nil {
		return ""
	}

	var recs []string
	now := time.Now()

	if health.RateLimited(now) {
		// A usage cap clears on its own timer. Re-authenticating does not
		// lift it, and a login is disruptive (claude login is machine-wide),
		// so never steer a rate-limited profile toward "caam login" (PR #82).
		recs = append(recs, fmt.Sprintf("%s/%s is rate limited - wait %s for the cap to reset (re-login will not clear it)",
			provider, profile, formatDurationNatural(health.RateLimitedUntil.Sub(now))))
	} else if !health.TokenExpiresAt.IsZero() && !health.SelfRefreshing {
		// Check token expiry. Nothing to recommend for a self-refreshing
		// credential: the provider's CLI renews it on next use, "caam
		// refresh" is unsupported for it, and a re-login is disruptive.
		ttl := health.TokenExpiresAt.Sub(now)
		if ttl <= 0 {
			// A lapsed access token that still has something to renew itself
			// with does not need a login; sending the operator through one
			// would be disruptive and would fix nothing (issue #102).
			if health.TokenRenewable {
				recs = append(recs, fmt.Sprintf("Run \"caam refresh %s %s\" to renew the lapsed access token (no re-login needed)", provider, profile))
			} else {
				recs = append(recs, fmt.Sprintf("Run \"caam login %s %s\" to re-authenticate", provider, profile))
			}
		} else if ttl < time.Hour {
			recs = append(recs, fmt.Sprintf("Run \"caam refresh %s %s\" to refresh expiring token", provider, profile))
		}
	}

	// High error count
	if health.ErrorCount1h >= 3 {
		recs = append(recs, fmt.Sprintf("Profile %s/%s has frequent errors - consider switching to another profile", provider, profile))
	}

	return strings.Join(recs, "\n")
}

// FormatPlanType returns a formatted plan type string.
func FormatPlanType(planType string) string {
	switch strings.ToLower(planType) {
	case "enterprise":
		return "Enterprise"
	case "pro":
		return "Pro"
	case "team":
		return "Team"
	case "free":
		return "Free"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	case "plus":
		return "Plus"
	case "premium":
		return "Premium"
	default:
		if planType == "" {
			return ""
		}
		return planType
	}
}

// colorizeStatus applies the appropriate color to the status text.
func colorizeStatus(status HealthStatus, text string) string {
	switch status {
	case StatusHealthy:
		return healthyStyle.Render(text)
	case StatusWarning:
		return warningStyle.Render(text)
	case StatusCritical:
		return criticalStyle.Render(text)
	default:
		return unknownStyle.Render(text)
	}
}

// formatDurationNatural formats a duration in a natural way.
func formatDurationNatural(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

// capitalizeFirst returns the string with its first letter capitalized.
// This is a replacement for the deprecated strings.Title function.
// Uses Unicode-aware rune handling for proper UTF-8 support.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
