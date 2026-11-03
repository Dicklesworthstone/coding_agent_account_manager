package usage

import (
	"math"
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

func TestSubscriptionRankingRequiresUsableGeneralQuota(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, provider := range []string{"claude", "codex"} {
		for _, status := range []string{"", QuotaOK} {
			for _, mode := range RankModes {
				t.Run(provider+"/"+status+"/"+string(mode), func(t *testing.T) {
					future := now.Add(24 * time.Hour)
					unknown := []*UsageInfo{
						{},
						{PrimaryWindow: &UsageWindow{Unmeasured: true, ResetsAt: future}},
						{PrimaryWindow: &UsageWindow{Utilization: math.NaN(), ResetsAt: future}},
						{PrimaryWindow: &UsageWindow{UsedPercent: -1, ResetsAt: future}},
						{PrimaryWindow: &UsageWindow{Rolled: true}, SecondaryWindow: &UsageWindow{UsedPercent: 10, ResetsAt: future}},
						{ModelWindows: map[string]*UsageWindow{"Fable": {UsedPercent: 10, ResetsAt: future}}},
						{LimitStage: "HARD_LIMIT", PrimaryWindow: &UsageWindow{ResetsAt: future}},
					}
					rows := make([]ProfileUsage, 0, len(unknown)+1)
					for i, info := range unknown {
						info.Provider, info.QuotaStatus = provider, status
						info.Credits = &CreditInfo{HasCredits: true, Unlimited: true}
						rows = append(rows, ProfileUsage{Provider: provider, ProfileName: string(rune('a' + i)), Usage: info})
					}
					assertUnknown := func(result *RankResult) {
						t.Helper()
						for _, row := range result.Profiles {
							if row.Profile != "weekly" && (row.Eligible || row.AvailabilityScore != 0) {
								t.Fatalf("unknown quota entered ranking: %+v", row)
							}
						}
					}
					result := RankProfiles(rows, RankOptions{Mode: mode, Now: now})
					if result.Selected != nil || result.Error == "" {
						t.Fatalf("ranking selected an account without usable general quota: %+v", result)
					}
					assertUnknown(result)
					rows = append(rows, ProfileUsage{Provider: provider, ProfileName: "weekly", Usage: &UsageInfo{
						Provider: provider, QuotaStatus: status,
						SecondaryWindow: &UsageWindow{UsedPercent: 10, ResetsAt: future},
					}})
					result = RankProfiles(rows, RankOptions{Mode: mode, Now: now})
					if result.Selected == nil || result.Selected.Profile != "weekly" {
						t.Fatalf("ranking rejected valid weekly-only quota: %+v", result)
					}
					assertUnknown(result)
				})
			}
		}
	}
}
