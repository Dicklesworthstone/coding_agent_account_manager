package usage

import (
	"testing"
	"time"
)

func TestNativeRankingRejectsDegradedMeasuredWindows(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for _, provider := range []string{"grok", "cursor"} {
		for _, status := range []string{"", QuotaDegraded, QuotaUnavailable, "future-status"} {
			for _, mode := range RankModes {
				u := &UsageInfo{
					Provider: provider, QuotaStatus: status, QuotaNote: "incomplete measurement",
					PrimaryWindow: &UsageWindow{UsedPercent: 0, ResetsAt: now.Add(time.Hour)},
					Credits:       &CreditInfo{HasCredits: true},
				}
				result := RankProfiles([]ProfileUsage{{Provider: provider, ProfileName: "unknown", Usage: u}}, RankOptions{Mode: mode, Now: now})
				if result.Selected != nil || result.Error == "" || result.Profiles[0].Eligible {
					t.Errorf("%s/%s/%s selected incomplete row: %+v", provider, status, mode, result)
				}
			}
		}
	}
}

func TestNativeRankingAcceptsMeasuredZeroAndRejectsLimitStage(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for _, provider := range []string{"grok", "cursor"} {
		for _, mode := range RankModes {
			known := &UsageInfo{Provider: provider, QuotaStatus: QuotaOK,
				PrimaryWindow: &UsageWindow{ResetsAt: now.Add(time.Hour)}}
			blocked := *known
			blocked.LimitStage = "HARD_BLOCK"
			result := RankProfiles([]ProfileUsage{
				{Provider: provider, ProfileName: "blocked", Usage: &blocked},
				{Provider: provider, ProfileName: "known", Usage: known},
			}, RankOptions{Mode: mode, Now: now})
			if result.Selected == nil || result.Selected.Profile != "known" || result.Profiles[1].Eligible {
				t.Errorf("%s/%s ranking = %+v", provider, mode, result)
			}
		}
	}
}
