package cmd

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestGrokPrecheckSwitchesToResetZero(t *testing.T) {
	setupGrokResetProfiles(t)
	files := authfile.GrokAuthFiles()
	if err := vault.Restore(files, "spent"); err != nil {
		t.Fatal(err)
	}
	if active, err := vault.ActiveProfile(files); err != nil || active != "spent" {
		t.Fatalf("could not establish spent profile as active: %q, %v", active, err)
	}
	if switched := runPrecheck(context.Background(), "grok", 0.8, true, nil, rotation.AlgorithmSmart, config.DefaultSPMConfig(), ""); !switched {
		t.Fatal("precheck did not switch from spent quota to the reset-zero account")
	}
	if active, err := vault.ActiveProfile(files); err != nil || active != "fresh" {
		t.Fatalf("precheck did not activate the measured reset account: %q, %v", active, err)
	}
}

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

// Claude and Codex distinguish an unreadable quota from a measured low quota,
// while a valid weekly-only account remains usable without a session window.
func TestPrecheckNearLimitSubscriptionQuotas(t *testing.T) {
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
		if near, measured := precheckNearLimit(provider, failed, 0.8, ""); near || measured {
			t.Errorf("%s failed read: near=%v measured=%v", provider, near, measured)
		}
		for _, missing := range []*usage.UsageInfo{nil, {Provider: provider}} {
			if near, measured := precheckNearLimit(provider, missing, 0.8, ""); near || measured {
				t.Errorf("%s missing quota: near=%v measured=%v", provider, near, measured)
			}
		}
		for _, percent := range []int{0, 79, 80, 100} {
			weeklyOnly := &usage.UsageInfo{Provider: provider, SecondaryWindow: &usage.UsageWindow{UsedPercent: percent, Utilization: float64(percent) / 100}}
			if near, measured := precheckNearLimit(provider, weeklyOnly, 0.8, ""); !measured || near != (percent >= 80) {
				t.Errorf("%s weekly %d%%: near=%v measured=%v", provider, percent, near, measured)
			}
		}
	}
}

func TestSubscriptionPrecheckSwitchesOnlyToMeasuredBelowThreshold(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, hasReady := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ready=%t", provider, hasReady), func(t *testing.T) {
				responses := map[string]string{
					"current":  subscriptionQuotaResponse(provider, 90),
					"a-near":   subscriptionQuotaResponse(provider, 85),
					"b-spent":  subscriptionQuotaResponse(provider, 100),
					"c-failed": "",
				}
				if hasReady {
					responses["z-ready"] = subscriptionQuotaResponse(provider, 20)
					if provider == "claude" {
						// Synthetic compatibility case; no verified Ultra payload
						// establishes how an inapplicable session is represented.
						responses["z-ready"] = `{"five_hour":{"utilization":null},"seven_day":{"utilization":20}}`
					}
				}
				files, live := setupSubscriptionQuotaProfiles(t, provider, responses)
				if err := vault.Restore(files, "current"); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(live)
				if err != nil {
					t.Fatal(err)
				}
				switched := runPrecheck(context.Background(), provider, 0.8, true, nil, rotation.AlgorithmRoundRobin, config.DefaultSPMConfig(), "")
				if switched != hasReady {
					t.Fatalf("precheck switched=%v, want %v", switched, hasReady)
				}
				want := "current"
				if hasReady {
					want = "z-ready"
				} else {
					requireSwitchCredential(t, live, string(before))
				}
				if active, err := vault.ActiveProfile(files); err != nil || active != want {
					t.Fatalf("precheck activated %q, want %q: %v", active, want, err)
				}
			})
		}
	}
}

func TestClaudePrecheckRespectsModelQuotaDuringActivation(t *testing.T) {
	for _, model := range []string{"fable", "sonnet"} {
		t.Run(model, func(t *testing.T) {
			response := func(weekly, fable int) string {
				return fmt.Sprintf(`{"seven_day":{"utilization":%d},"limits":[{"kind":"weekly_scoped","percent":%d,"scope":{"model":{"display_name":"Fable"}}}]}`, weekly, fable)
			}
			files, _ := setupSubscriptionQuotaProfiles(t, "claude", map[string]string{
				"current": response(10, 100), "a-idle": response(0, 100), "z-ready": response(30, 20),
			})
			if err := vault.Restore(files, "current"); err != nil {
				t.Fatal(err)
			}
			switched := runPrecheck(context.Background(), "claude", 0.8, true, nil, rotation.AlgorithmRoundRobin, config.DefaultSPMConfig(), model)
			want := "current"
			if model == "fable" {
				want = "z-ready"
			}
			if switched != (model == "fable") {
				t.Fatalf("model %s switched=%v", model, switched)
			}
			if active, err := vault.ActiveProfile(files); err != nil || active != want {
				t.Fatalf("model %s activated %q, want %q: %v", model, active, want, err)
			}
		})
	}
}
