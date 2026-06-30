package monitor

import (
	"fmt"
	"strings"
	"time"
)

// Render implements the Renderer interface for TableRenderer.
func (r *TableRenderer) Render(state *MonitorState) string {
	if state == nil {
		return "No data available"
	}

	width := r.Width
	if width <= 0 {
		width = 75
	}
	if width < 40 {
		width = 40
	}

	innerWidth := width - 2
	if innerWidth < 1 {
		innerWidth = 1
	}

	var b strings.Builder

	writeBorder(&b, innerWidth)
	writeCentered(&b, innerWidth, "LIVE USAGE MONITOR")
	updated := fmt.Sprintf("Last updated: %s", formatUpdatedAt(state.UpdatedAt))
	writeCentered(&b, innerWidth, updated)
	writeBorder(&b, innerWidth)

	if len(state.Profiles) == 0 {
		writeCentered(&b, innerWidth, "No profiles configured")
	} else {
		keys := sortProfileKeys(state)
		currentProvider := ""
		now := time.Now()

		for _, key := range keys {
			p := state.Profiles[key]
			if p == nil {
				continue
			}

			if p.Provider != currentProvider {
				currentProvider = p.Provider
				provHeader := fmt.Sprintf("  %s", strings.ToUpper(p.Provider))
				writeLine(&b, innerWidth, provHeader)
			}

			indicator := ""
			if r.ShowEmoji {
				indicator = fmt.Sprintf("[%s] ", healthEmoji(p.Health))
			}

			var line string
			if reason := usageUnavailable(p.Usage); reason != "" {
				// The fetch failed or returned no data (e.g. an expired token) —
				// show why instead of a misleading 0% bar, so logged-in accounts
				// don't look idle (issue #37).
				line = fmt.Sprintf("  %s%-20s %s", indicator, truncate(p.ProfileName, 20), reason)
			} else {
				percent := usagePercent(p.Usage)
				bar := progressBar(percent, 20)
				percentStr := fmt.Sprintf("%3.0f%%", percent)

				status := p.PoolStatus.String()
				if p.InCooldown && p.CooldownUntil != nil {
					status = fmt.Sprintf("cooldown %s", formatCooldown(p.CooldownUntil, now))
				}

				line = fmt.Sprintf("  %s%-20s %s %s | %s", indicator, truncate(p.ProfileName, 20), bar, percentStr, status)
			}
			writeLine(&b, innerWidth, line)
		}
	}

	writeBorder(&b, innerWidth)
	if len(state.Errors) > 0 {
		b.WriteString(renderErrors(state))
	}

	return b.String()
}

func writeBorder(b *strings.Builder, innerWidth int) {
	if innerWidth < 1 {
		innerWidth = 1
	}
	b.WriteString("+")
	b.WriteString(strings.Repeat("-", innerWidth))
	b.WriteString("+\n")
}

func writeCentered(b *strings.Builder, innerWidth int, text string) {
	writeLine(b, innerWidth, centerText(text, innerWidth))
}

func writeLine(b *strings.Builder, innerWidth int, content string) {
	if innerWidth < 1 {
		innerWidth = 1
	}
	if len(content) > innerWidth {
		content = content[:innerWidth]
	}
	b.WriteString("|")
	b.WriteString(content)
	if pad := innerWidth - len(content); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString("|\n")
}

func centerText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(text) >= width {
		return text[:width]
	}
	left := (width - len(text)) / 2
	right := width - len(text) - left
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
}
