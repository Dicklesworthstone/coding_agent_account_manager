package health

import (
	"fmt"
	"strings"
	"time"

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

// FormatStatusWithReason returns a detailed status string with explanation.
// Example: "🟡 Warning - Token expires in 12 minutes"
func FormatStatusWithReason(status HealthStatus, health *ProfileHealth, opts FormatOptions) string {
	icon := status.Icon()
	statusStr := status.String()

	var reasons []string

	if health != nil {
		// Check token expiry
		if !health.TokenExpiresAt.IsZero() {
			ttl := time.Until(health.TokenExpiresAt)
			if ttl <= 0 {
				reasons = append(reasons, "Token expired")
			} else if ttl < 15*time.Minute {
				reasons = append(reasons, fmt.Sprintf("Token expires in %s", formatDurationNatural(ttl)))
			} else if ttl < time.Hour {
				reasons = append(reasons, fmt.Sprintf("Token expires in %s", formatDurationNatural(ttl)))
			}
		}

		// Check errors
		if health.ErrorCount1h > 0 {
			if health.ErrorCount1h == 1 {
				reasons = append(reasons, "1 recent error")
			} else {
				reasons = append(reasons, fmt.Sprintf("%d recent errors", health.ErrorCount1h))
			}
		}

		// Check penalty
		if health.Penalty >= 1.0 {
			reasons = append(reasons, "High penalty from errors")
		}
	}

	var result string
	if len(reasons) > 0 {
		result = fmt.Sprintf("%s %s - %s", icon, strings.Title(statusStr), strings.Join(reasons, ", "))
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

	// Check token expiry
	if !health.TokenExpiresAt.IsZero() {
		ttl := time.Until(health.TokenExpiresAt)
		if ttl <= 0 {
			recs = append(recs, fmt.Sprintf("Run \"caam login %s %s\" to re-authenticate", provider, profile))
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
