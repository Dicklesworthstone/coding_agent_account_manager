package rotation

import "fmt"

// NativeQuotaCandidates limits an explicitly usage-aware native selection to
// measured, unspent accounts before any ranking algorithm runs. A nil usage
// map means no quota read was requested; an empty non-nil map means it failed.
// Legacy providers retain their existing fallback behavior.
func NativeQuotaCandidates(provider string, profiles []string, data map[string]*UsageInfo) ([]string, error) {
	if data == nil || (provider != "grok" && provider != "cursor") {
		return profiles, nil
	}
	available := make([]string, 0, len(profiles))
	for _, name := range profiles {
		u := data[name]
		if u == nil || u.Error != "" {
			continue
		}
		valid := true
		for _, percent := range []int{u.PrimaryPercent, u.SecondaryPercent, u.ScopedPercent} {
			if percent < 0 || percent >= 100 {
				valid = false
				break
			}
		}
		if valid {
			available = append(available, name)
		}
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("no %s profiles with measured available quota; refusing usage-aware activation", provider)
	}
	return available, nil
}
