package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Alert represents a monitor alert for a profile.
type Alert struct {
	Type    AlertType
	Message string
	Since   time.Time
}

// AlertType indicates the severity of a monitor alert.
type AlertType int

const (
	AlertNone      AlertType = iota
	AlertWarning             // 70-85%
	AlertCritical            // 85-95%
	AlertExhausted           // 95-100%
)

const (
	warningThreshold   = 70.0
	criticalThreshold  = 85.0
	exhaustedThreshold = 95.0
)

func evaluateAlert(info *usage.UsageInfo, now time.Time) *Alert {
	if info == nil {
		return nil
	}

	percent := usagePercent(info)
	if percent <= 0 {
		return nil
	}

	switch {
	case percent >= exhaustedThreshold:
		return &Alert{
			Type:    AlertExhausted,
			Message: fmt.Sprintf("Usage at %.0f%% (limit nearly exhausted)", percent),
			Since:   now,
		}
	case percent >= criticalThreshold:
		return &Alert{
			Type:    AlertCritical,
			Message: fmt.Sprintf("Usage at %.0f%%", percent),
			Since:   now,
		}
	case percent >= warningThreshold:
		return &Alert{
			Type:    AlertWarning,
			Message: fmt.Sprintf("Usage at %.0f%%", percent),
			Since:   now,
		}
	default:
		return nil
	}
}

// usageUnavailable returns a short, human-readable reason when usage could not
// be fetched/computed for a profile, or "" when real usage data is present. It
// lets the table explain why a logged-in account shows no usage (e.g. an expired
// access token that needs re-login) instead of rendering a misleading "0%" bar
// (issue #37).
func usageUnavailable(info *usage.UsageInfo) string {
	if info == nil {
		return "no usage data"
	}
	// Real data present (a usage window, or a credit balance) -> show the number.
	if info.MostConstrainedWindow() != nil || info.Credits != nil {
		return ""
	}
	if info.Error != "" {
		// ASCII only: writeLine pads the table by byte length, so a multi-byte
		// rune here would shift the right border and break box alignment.
		return "no usage: " + shortUsageError(info.Error)
	}
	return ""
}

// shortUsageError condenses common usage-fetch errors into a brief hint that
// fits in the table without wrapping.
func shortUsageError(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "unauthorized"), strings.Contains(e, "token expired"),
		strings.Contains(e, "expired or invalid"), strings.Contains(e, "401"):
		return "auth expired (re-login)"
	case strings.Contains(e, "missing access token"), strings.Contains(e, "not logged in"),
		strings.Contains(e, "no access token"):
		return "not logged in"
	case strings.Contains(e, "not yet supported"), strings.Contains(e, "unsupported"),
		strings.Contains(e, "not supported"):
		return "usage not supported"
	}
	if len(err) > 40 {
		return err[:37] + "..."
	}
	return err
}

func usagePercent(info *usage.UsageInfo) float64 {
	if info == nil {
		return 0
	}
	window := info.MostConstrainedWindow()
	if window == nil {
		return 0
	}
	util := window.Utilization
	if util == 0 && window.UsedPercent > 0 {
		util = float64(window.UsedPercent) / 100.0
	}
	return util * 100
}
