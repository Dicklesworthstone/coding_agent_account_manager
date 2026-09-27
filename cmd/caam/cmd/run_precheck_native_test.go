package cmd

import (
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Issue #79: `caam run grok|cursor --precheck` uses the native quota readers.
// They are fail-closed, so the precheck must tell "near the limit", "fine"
// and "could not measure" apart instead of reading an unmeasured row as 0%.
func TestPrecheckNearLimitNative(t *testing.T) {
	window := func(pct int) *usage.UsageWindow {
		return &usage.UsageWindow{UsedPercent: pct, Utilization: float64(pct) / 100}
	}
	for _, provider := range []string{"grok", "cursor"} {
		cases := []struct {
			name              string
			u                 *usage.UsageInfo
			wantNear, wantMsr bool
		}{
			{"measured low", &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaOK, PrimaryWindow: window(20)}, false, true},
			{"measured zero", &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaOK, PrimaryWindow: window(0)}, false, true},
			{"measured high", &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaOK, PrimaryWindow: window(95)}, true, true},
			{"limit stage without percentage", &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaDegraded, LimitStage: "hard_limit"}, true, true},
			{"degraded", &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaDegraded, PrimaryWindow: &usage.UsageWindow{Unmeasured: true}}, false, false},
			{"unavailable", &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaUnavailable, Error: "grok is not installed"}, false, false},
		}
		for _, tc := range cases {
			near, measured := precheckNearLimit(provider, tc.u, 0.8, "")
			if near != tc.wantNear || measured != tc.wantMsr {
				t.Errorf("%s/%s: got near=%v measured=%v, want near=%v measured=%v",
					provider, tc.name, near, measured, tc.wantNear, tc.wantMsr)
			}
		}
	}
}

// Claude and Codex keep the historical rule: only a measured window at the
// threshold triggers a switch, and a row is never reported as unmeasured.
func TestPrecheckNearLimitLegacyUnchanged(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		low := &usage.UsageInfo{Provider: provider, PrimaryWindow: &usage.UsageWindow{UsedPercent: 10, Utilization: 0.1}}
		if near, measured := precheckNearLimit(provider, low, 0.8, ""); near || !measured {
			t.Errorf("%s low: near=%v measured=%v", provider, near, measured)
		}
		high := &usage.UsageInfo{Provider: provider, PrimaryWindow: &usage.UsageWindow{UsedPercent: 90, Utilization: 0.9}}
		if near, measured := precheckNearLimit(provider, high, 0.8, ""); !near || !measured {
			t.Errorf("%s high: near=%v measured=%v", provider, near, measured)
		}
		failed := &usage.UsageInfo{Provider: provider, Error: "unauthorized: token expired or invalid"}
		if near, measured := precheckNearLimit(provider, failed, 0.8, ""); near || !measured {
			t.Errorf("%s failed read: near=%v measured=%v (legacy precheck never reported unmeasured)", provider, near, measured)
		}
	}
}
