package rotation

import "fmt"

// QuotaCandidates limits an explicitly usage-aware selection to measured,
// unspent accounts, independently of provider or ranking algorithm. A nil
// usage map means no quota read was requested; an empty non-nil map means
// none of the requested accounts could be measured.
func QuotaCandidates(provider string, profiles []string, data map[string]*UsageInfo) ([]string, error) {
	if data == nil {
		return profiles, nil
	}
	available := make([]string, 0, len(profiles))
	for _, name := range profiles {
		if quotaIneligibilityReason(data[name]) == "" {
			available = append(available, name)
		}
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("no %s profiles with measured available quota; refusing usage-aware activation", provider)
	}
	return available, nil
}

// quotaIneligibilityReason is shared by callers that need a candidate list
// and Select itself. Quota eligibility precedes scoring: a failed read or
// exhausted allowance cannot become capacity through a health/plan bonus,
// random choice, or a reserve fallback. Missing optional windows are zero
// in this normalized representation; the adapter must validate that at least
// one general quota was actually measured before constructing an error-free
// row.
func quotaIneligibilityReason(u *UsageInfo) string {
	if u == nil {
		return "Usage data unavailable: no measured quota"
	}
	if u.Error != "" {
		return "Usage data unavailable: quota read failed"
	}
	windows := []struct {
		label   string
		percent int
	}{
		{"Primary", u.PrimaryPercent},
		{"Secondary", u.SecondaryPercent},
		{scopedQuotaLabel(u), u.ScopedPercent},
	}
	for _, w := range windows {
		if w.percent < 0 || w.percent > 100 {
			return fmt.Sprintf("%s quota has an invalid utilization", w.label)
		}
	}
	if u.AvailScore < 0 || u.AvailScore > 100 {
		return "Usage data unavailable: invalid availability score"
	}
	for _, w := range windows {
		if w.percent == 100 {
			return fmt.Sprintf("%s limit %d%% used (exhausted)", w.label, w.percent)
		}
	}
	return ""
}
