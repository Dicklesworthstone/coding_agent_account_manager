package cmd

// Tests for issue #97: the model-scoped quota has to survive the trip from the
// usage API into the rotation selector, and `caam run … -- --model X` has to
// tell caam which quota matters.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestModelFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, ""},
		{"no model flag", []string{"-p", "write tests"}, ""},
		{"separate value", []string{"--model", "opus", "-p", "hi"}, "opus"},
		{"equals form", []string{"--model=claude-fable-5"}, "claude-fable-5"},
		{"after a separator", []string{"--", "--model", "fable"}, "fable"},
		{"missing value", []string{"--model"}, ""},
		{"value is another flag", []string{"--model", "--verbose"}, ""},
		{"short form is not guessed", []string{"-m", "opus"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelFromArgs(tc.args); got != tc.want {
				t.Errorf("modelFromArgs(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestRotationAdapterEnforcesFractionOnlyAndWeeklyQuotas(t *testing.T) {
	for _, tc := range []struct {
		name    string
		info    *usage.UsageInfo
		allowed bool
	}{
		{"missing general quota", &usage.UsageInfo{Provider: "claude"}, false},
		{"weekly-only measured zero", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{}}, true},
		{"fraction-only exhausted weekly", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{Utilization: 1}}, false},
		{"fraction-only exhausted model", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{}, ModelWindows: map[string]*usage.UsageWindow{"fable": {Utilization: 1, Label: "Fable"}}}, false},
		{"unmeasured session", &usage.UsageInfo{Provider: "claude", PrimaryWindow: &usage.UsageWindow{Unmeasured: true}, SecondaryWindow: &usage.UsageWindow{}}, false},
		{"degraded partial response", &usage.UsageInfo{Provider: "claude", SecondaryWindow: &usage.UsageWindow{}, QuotaStatus: usage.QuotaDegraded}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]*rotation.UsageInfo{"seat": toRotationUsageInfo("seat", tc.info, "fable")}
			cfg := config.DefaultSPMConfig()
			cfg.Stealth.Rotation.Algorithm = "random"
			result, err := selectProfileWithRotationAndUsage("claude", []string{"seat"}, "", cfg, nil, data, true)
			if tc.allowed {
				if err != nil || result == nil || result.Selected != "seat" {
					t.Fatalf("measured weekly-only quota was rejected: %+v, %v", result, err)
				}
			} else if err == nil || result != nil {
				t.Fatalf("unusable quota became a selection: %+v, %v", result, err)
			}
		})
	}
}

func TestToRotationUsageInfoCarriesScopedQuota(t *testing.T) {
	info := &usage.UsageInfo{
		PrimaryWindow:   &usage.UsageWindow{UsedPercent: 0, Utilization: 0},
		SecondaryWindow: &usage.UsageWindow{UsedPercent: 56, Utilization: 0.56},
		ModelWindows: map[string]*usage.UsageWindow{
			"Fable": {UsedPercent: 100, Utilization: 1.0, Label: "Fable", Kind: usage.LimitKindWeeklyScoped},
		},
	}

	forFable := toRotationUsageInfo("acct", info, "claude-fable-5")
	if forFable.ScopedPercent != 100 || forFable.ScopedLabel != "Fable" {
		t.Fatalf("scoped quota lost in translation: %+v", forFable)
	}
	if forFable.PrimaryPercent != 0 || forFable.SecondaryPercent != 56 {
		t.Fatalf("general windows wrong: %+v", forFable)
	}

	forSonnet := toRotationUsageInfo("acct", info, "claude-sonnet-4-5")
	if forSonnet.ScopedPercent != 0 {
		t.Errorf("the Fable quota was applied to Sonnet work: %+v", forSonnet)
	}
	if forSonnet.AvailScore <= forFable.AvailScore {
		t.Errorf("Sonnet score %d is not above the Fable score %d", forSonnet.AvailScore, forFable.AvailScore)
	}

	// With the model unknown, every scoped quota counts.
	unknown := toRotationUsageInfo("acct", info, "")
	if unknown.ScopedPercent != 100 {
		t.Errorf("with no model given the worst scoped quota must count, got %d", unknown.ScopedPercent)
	}

	if toRotationUsageInfo("acct", nil, "") != nil {
		t.Error("nil usage should convert to nil")
	}
}

func TestFormatScopedLimit(t *testing.T) {
	if got := formatScopedLimit(nil); got != "-" {
		t.Errorf("formatScopedLimit(nil) = %q", got)
	}
	got := formatScopedLimit(&usage.UsageWindow{Label: "Fable", UsedPercent: 100})
	if got != "Fable 100%" {
		t.Errorf("formatScopedLimit = %q, want %q", got, "Fable 100%")
	}
	if got := formatScopedLimit(&usage.UsageWindow{UsedPercent: 42}); got != "model quota 42%" {
		t.Errorf("formatScopedLimit(unlabelled) = %q", got)
	}
}

func TestDrainSelectionUsesOnlyApplicableModelResets(t *testing.T) {
	for _, model := range []string{"sonnet", ""} {
		t.Run("model="+model, func(t *testing.T) {
			now := time.Now().UTC()
			stamp := func(after time.Duration) string { return now.Add(after).Format(time.RFC3339Nano) }
			responses := map[string]string{
				"a-unrelated-reset": fmt.Sprintf(`{"seven_day":{"utilization":20,"resets_at":%q},"seven_day_opus":{"utilization":10,"resets_at":%q},"limits":[{"kind":"weekly_scoped","percent":20,"resets_at":%q,"scope":{"model":{"display_name":"Sonnet"}}}]}`, stamp(24*time.Hour), stamp(5*time.Minute), stamp(24*time.Hour)),
				"b-sonnet-reset":    fmt.Sprintf(`{"seven_day":{"utilization":20,"resets_at":%q},"limits":[{"kind":"session","percent":20,"resets_at":%q,"scope":{"model":{"display_name":"Sonnet"}}},{"kind":"weekly_scoped","percent":60,"resets_at":%q,"scope":{"model":{"display_name":"Sonnet"}}}]}`, stamp(24*time.Hour), stamp(time.Hour), stamp(48*time.Hour)),
			}
			setupSubscriptionQuotaProfiles(t, "claude", responses)
			fetcher := usage.NewMultiProfileFetcher()
			rows := fetcher.FetchAllProfiles(context.Background(), "claude", map[string]string{
				"a-unrelated-reset": "a-unrelated-reset", "b-sonnet-reset": "b-sonnet-reset",
			})
			data := make(map[string]*rotation.UsageInfo, len(rows))
			for _, row := range rows {
				if row.Usage == nil || !row.Usage.NumericQuotaKnown() {
					t.Fatalf("synthetic measured quota was rejected: %+v", row.Usage)
				}
				data[row.ProfileName] = toRotationUsageInfo(row.ProfileName, row.Usage, model)
			}
			cfg := config.DefaultSPMConfig()
			cfg.Stealth.Rotation.Policy = "drain"
			result, err := selectProfileWithRotationAndUsage("claude", []string{"a-unrelated-reset", "b-sonnet-reset"}, "", cfg, nil, data, false)
			want := "b-sonnet-reset"
			if model == "" {
				want = "a-unrelated-reset"
			}
			if err != nil || result == nil || result.Selected != want {
				t.Fatalf("model %q drained an unrelated or discarded window: %+v, %v; want %s", model, result, err, want)
			}
			if model == "sonnet" {
				if got := data["b-sonnet-reset"].ResetsAt; got == nil || !got.Equal(now.Add(time.Hour)) {
					t.Fatalf("stronger weekly quota hid the earlier session reset: %v", got)
				}
				if got := data["a-unrelated-reset"].ResetsAt; got == nil || !got.Equal(now.Add(24*time.Hour)) {
					t.Fatalf("Sonnet borrowed the Opus reset: %v", got)
				}
			}
		})
	}
}
