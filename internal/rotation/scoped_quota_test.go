package rotation

// Tests for issue #97: a model-scoped quota (Anthropic's weekly Fable or Opus
// allowance) constrains an account just as the general windows do, so it has
// to reach the selector rather than being invisible to it.

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestSmartSelectionAvoidsSpentScopedQuota(t *testing.T) {
	usageData := map[string]*UsageInfo{
		// Idle on paper, but its per-model allowance is gone.
		"spent": {
			ProfileName:    "spent",
			PrimaryPercent: 0,
			AvailScore:     71,
			ScopedPercent:  100,
			ScopedLabel:    "Fable",
		},
		"has-quota": {
			ProfileName:    "has-quota",
			PrimaryPercent: 40,
			AvailScore:     63,
			ScopedPercent:  12,
			ScopedLabel:    "Fable",
		},
	}

	s := NewSelector(AlgorithmSmart, nil, nil)
	s.SetRNG(rand.New(rand.NewSource(42)))
	s.SetUsageData(usageData)

	result, err := s.Select("claude", []string{"spent", "has-quota"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Selected != "has-quota" {
		t.Errorf("selected %q, want 'has-quota' — the spent Fable quota was ignored", result.Selected)
	}

	var spentReasons []string
	for _, score := range result.Alternatives {
		if score.Name == "spent" {
			for _, r := range score.Reasons {
				spentReasons = append(spentReasons, r.Text)
			}
		}
	}
	joined := strings.Join(spentReasons, " | ")
	if !strings.Contains(joined, "Fable limit 100% used") {
		t.Errorf("the spent profile's reasons do not name the quota: %s", joined)
	}
}

// TestDrainPolicyCountsScopedQuota: the drain policy ranks by headroom, and a
// spent per-model quota is a ceiling like any other.
func TestDrainPolicyCountsScopedQuota(t *testing.T) {
	now := time.Now()
	usageData := map[string]*UsageInfo{
		// Barely touched on its general windows, but its per-model allowance
		// is gone: it is above the drain ceiling, not a drain candidate.
		"scoped-spent": {
			ProfileName:    "scoped-spent",
			PrimaryPercent: 10,
			AvailScore:     70,
			ScopedPercent:  100,
			ScopedLabel:    "Fable",
			ResetsAt:       timePtr(now.Add(1 * time.Hour)),
		},
		"clean": {
			ProfileName:    "clean",
			PrimaryPercent: 10,
			AvailScore:     85,
			ResetsAt:       timePtr(now.Add(2 * time.Hour)),
		},
	}

	s := NewSelector(AlgorithmSmart, nil, nil)
	s.SetRNG(rand.New(rand.NewSource(1)))
	s.SetPolicy(PolicyDrain)
	s.SetUsageData(usageData)

	result, err := s.Select("claude", []string{"scoped-spent", "clean"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Without the scoped quota, "scoped-spent" would win: it resets soonest
	// and reads as 10% used.
	if result.Selected != "clean" {
		t.Errorf("drain selected %q, want 'clean' — the spent Fable quota was ignored", result.Selected)
	}
	for _, ps := range result.Alternatives {
		if ps.Name != "scoped-spent" {
			continue
		}
		var joined []string
		for _, r := range ps.Reasons {
			joined = append(joined, r.Text)
		}
		if !strings.Contains(strings.Join(joined, " | "), "100% used") {
			t.Errorf("the spent profile's headroom ignored the scoped quota: %v", joined)
		}
	}
}

func TestScopedQuotaLabelFallsBack(t *testing.T) {
	if got := scopedQuotaLabel(nil); got != "Model-scoped" {
		t.Errorf("scopedQuotaLabel(nil) = %q", got)
	}
	if got := scopedQuotaLabel(&UsageInfo{}); got != "Model-scoped" {
		t.Errorf("scopedQuotaLabel(unlabelled) = %q", got)
	}
	if got := scopedQuotaLabel(&UsageInfo{ScopedLabel: "Opus"}); got != "Opus" {
		t.Errorf("scopedQuotaLabel(Opus) = %q", got)
	}
}
