package monitor

import "fmt"

// Render implements the Renderer interface for BriefRenderer.
func (r *BriefRenderer) Render(state *MonitorState) string {
	if state == nil || len(state.Profiles) == 0 {
		return ""
	}

	sep := r.Separator
	if sep == "" {
		sep = " "
	}

	byProvider := make(map[string]*ProfileState)
	for _, p := range state.Profiles {
		if p == nil {
			continue
		}
		existing := byProvider[p.Provider]
		if existing == nil || usagePercent(p.Usage) > usagePercent(existing.Usage) {
			byProvider[p.Provider] = p
		}
	}

	providers := []string{"claude", "codex", "gemini", "grok", "opencode", "cursor"}
	parts := make([]string, 0, len(providers))
	for _, prov := range providers {
		p := byProvider[prov]
		if p == nil {
			continue
		}
		percent := usagePercent(p.Usage)
		if percent < 0 {
			parts = append(parts, prov+":?")
			continue
		}
		trend := "-"
		switch {
		case percent >= 80:
			trend = "^"
		case percent < 50:
			trend = "v"
		}
		parts = append(parts, fmt.Sprintf("%s:%.0f%%%s", prov, percent, trend))
	}

	out := joinWithSeparator(parts, sep)
	if len(out) <= 80 {
		return out
	}

	out = joinWithSeparator(parts, "")
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func joinWithSeparator(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, part := range parts[1:] {
		out += sep + part
	}
	return out
}
