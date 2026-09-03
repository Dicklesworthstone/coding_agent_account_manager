package cmd

// Tests for issue #97: the model-scoped quota has to survive the trip from the
// usage API into the rotation selector, and `caam run … -- --model X` has to
// tell caam which quota matters.

import (
	"testing"

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
