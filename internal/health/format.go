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
		if ttl <= 0 {
			text = "Expired"
		} else {
			text = FormatTimeRemaining(health.TokenExpiresAt)
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

// PrimaryReason is the single cause shown on `caam status`. A rate-limit
// cooldown outranks a stale snapshot expiry so a cap is never reported as
// "Token expired" (agent-factory-21fp).
func PrimaryReason(status HealthStatus, h *ProfileHealth) string {
	if h == nil {
		return ""
	}
	now := time.Now()
	if !h.CooldownUntil.IsZero() && h.CooldownUntil.After(now) {
		return fmt.Sprintf("Rate limited (resets in %s)", formatDurationNatural(h.CooldownUntil.Sub(now)))
	}
	if !h.TokenExpiresAt.IsZero() {
		ttl := time.Until(h.TokenExpiresAt)
		if ttl <= 0 {
			if !h.ExpiryLive {
				return "" // stale snapshot — unknown, never "Token expired"
			}
			if h.HasRefreshToken && h.ErrorCount1h == 0 {
				return "" // access token expired, refresh still valid — self-healing
			}
			return "Token expired"
		}
		if ttl < time.Hour {
			return fmt.Sprintf("Token expires in %s", formatDurationNatural(ttl))
		}
	}
	if h.ErrorCount1h > 0 {
		if h.ErrorCount1h == 1 {
			return "1 recent error"
		}
		return fmt.Sprintf("%d recent errors", h.ErrorCount1h)
	}
	if h.Penalty >= 1.0 {
		return "High penalty from errors"
	}
	_ = status
	return ""
}

// FormatStatusWithReason returns a detailed status string with explanation.
// Example: "🟡 Warning - Token expires in 12 minutes"
func FormatStatusWithReason(status HealthStatus, health *ProfileHealth, opts FormatOptions) string {
	icon := status.Icon()
	statusStr := status.String()

	var reasons []string
	if reason := PrimaryReason(status, health); reason != "" {
		reasons = append(reasons, reason)
	}

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
	if !health.CooldownUntil.IsZero() && health.CooldownUntil.After(now) {
		recs = append(recs, fmt.Sprintf(
			"Rate limited; wait for reset in %s. Do not re-login — a cap clears on its timer. Note: claude login is machine-wide and would disrupt every live claude pane.",
			formatDurationNatural(health.CooldownUntil.Sub(now)),
		))
		return strings.Join(recs, "\n")
	}

	// Never recommend a machine-wide re-login from a timestamp alone.
	// Require an observed auth failure (error_count > 0) and a LIVE expiry
	// that is actually past, with no refresh token (agent-factory-21fp).
	if !health.TokenExpiresAt.IsZero() {
		ttl := time.Until(health.TokenExpiresAt)
		if ttl <= 0 {
			if health.ExpiryLive && health.ErrorCount1h > 0 && !health.HasRefreshToken {
				recs = append(recs, fmt.Sprintf(
					"Run \"caam login %s %s\" to re-authenticate. Note: claude login is machine-wide and would disrupt every live claude pane on this box.",
					provider, profile,
				))
			}
		} else if ttl < time.Hour && health.ExpiryLive {
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
