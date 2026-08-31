package rotation

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestNewSelector(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)
	if s == nil {
		t.Fatal("expected non-nil selector")
	}
	if s.algorithm != AlgorithmSmart {
		t.Errorf("expected algorithm %q, got %q", AlgorithmSmart, s.algorithm)
	}
}

func TestSelectRandom(t *testing.T) {
	s := NewSelector(AlgorithmRandom, nil, nil)
	s.SetRNG(rand.New(rand.NewSource(42))) // Fixed seed for determinism

	profiles := []string{"alpha", "beta", "gamma"}
	result, err := s.Select("claude", profiles, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Algorithm != AlgorithmRandom {
		t.Errorf("expected algorithm %q, got %q", AlgorithmRandom, result.Algorithm)
	}

	// Selected should be one of the profiles
	found := false
	for _, p := range profiles {
		if result.Selected == p {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("selected profile %q not in input list", result.Selected)
	}

	// All profiles should appear in alternatives
	if len(result.Alternatives) != len(profiles) {
		t.Errorf("expected %d alternatives, got %d", len(profiles), len(result.Alternatives))
	}
}

func TestSelectRoundRobin(t *testing.T) {
	s := NewSelector(AlgorithmRoundRobin, nil, nil)

	t.Run("selects next profile in sequence", func(t *testing.T) {
		profiles := []string{"alpha", "beta", "gamma"}
		result, err := s.Select("claude", profiles, "alpha")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Selected != "beta" {
			t.Errorf("expected 'beta' after 'alpha', got %q", result.Selected)
		}
	})

	t.Run("wraps around to first profile", func(t *testing.T) {
		profiles := []string{"alpha", "beta", "gamma"}
		result, err := s.Select("claude", profiles, "gamma")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Selected != "alpha" {
			t.Errorf("expected 'alpha' after 'gamma', got %q", result.Selected)
		}
	})

	t.Run("handles no current profile", func(t *testing.T) {
		profiles := []string{"alpha", "beta", "gamma"}
		result, err := s.Select("claude", profiles, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should select first in sorted order
		if result.Selected != "alpha" {
			t.Errorf("expected 'alpha' with no current, got %q", result.Selected)
		}
	})
}

func TestSelectSmart(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)
	s.SetRNG(rand.New(rand.NewSource(42))) // Fixed seed for determinism

	t.Run("selects from available profiles", func(t *testing.T) {
		profiles := []string{"alpha", "beta", "gamma"}
		result, err := s.Select("claude", profiles, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Algorithm != AlgorithmSmart {
			t.Errorf("expected algorithm %q, got %q", AlgorithmSmart, result.Algorithm)
		}

		// Selected should be one of the profiles
		found := false
		for _, p := range profiles {
			if result.Selected == p {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("selected profile %q not in input list", result.Selected)
		}
	})

	t.Run("provides reasons for selection", func(t *testing.T) {
		profiles := []string{"work", "personal"}
		result, err := s.Select("claude", profiles, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should have at least one alternative with reasons
		if len(result.Alternatives) == 0 {
			t.Fatal("expected alternatives")
		}

		hasReasons := false
		for _, alt := range result.Alternatives {
			if len(alt.Reasons) > 0 {
				hasReasons = true
				break
			}
		}
		if !hasReasons {
			t.Error("expected at least one alternative with reasons")
		}
	})
}

func TestSelectFiltersSystemProfiles(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)

	t.Run("excludes system profiles", func(t *testing.T) {
		profiles := []string{"_original", "_backup_20241217", "work", "personal"}
		result, err := s.Select("claude", profiles, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should not select a system profile
		if strings.HasPrefix(result.Selected, "_") {
			t.Errorf("selected system profile %q", result.Selected)
		}

		// Alternatives should only have user profiles
		for _, alt := range result.Alternatives {
			if strings.HasPrefix(alt.Name, "_") {
				t.Errorf("alternative contains system profile %q", alt.Name)
			}
		}
	})

	t.Run("errors when only system profiles exist", func(t *testing.T) {
		profiles := []string{"_original", "_backup_20241217"}
		_, err := s.Select("claude", profiles, "")
		if err == nil {
			t.Fatal("expected error when only system profiles exist")
		}
		if !strings.Contains(err.Error(), "no user profiles") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestSelectSingleProfile(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)

	profiles := []string{"only-one"}
	result, err := s.Select("claude", profiles, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Selected != "only-one" {
		t.Errorf("expected 'only-one', got %q", result.Selected)
	}

	// Should have a reason indicating it's the only profile
	found := false
	for _, alt := range result.Alternatives {
		if alt.Name == "only-one" {
			for _, r := range alt.Reasons {
				if strings.Contains(r.Text, "Only available") {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Error("expected 'only available profile' reason")
	}
}

func TestSelectNoProfiles(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)

	_, err := s.Select("claude", nil, "")
	if err == nil {
		t.Fatal("expected error with no profiles")
	}
	if !strings.Contains(err.Error(), "no profiles") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h 30m"},
		{2 * time.Hour, "2h"},
		{25 * time.Hour, "1d 1h"},
		{48 * time.Hour, "2d"},
		{50 * time.Hour, "2d 2h"},
	}

	for _, tc := range tests {
		result := formatDuration(tc.duration)
		if result != tc.expected {
			t.Errorf("formatDuration(%v) = %q, expected %q", tc.duration, result, tc.expected)
		}
	}
}

func TestFormatResult(t *testing.T) {
	t.Run("nil result", func(t *testing.T) {
		output := FormatResult(nil)
		if !strings.Contains(output, "No selection") {
			t.Errorf("unexpected output for nil: %q", output)
		}
	})

	t.Run("basic result", func(t *testing.T) {
		result := &Result{
			Selected:  "work",
			Algorithm: AlgorithmSmart,
			Alternatives: []ProfileScore{
				{
					Name:  "work",
					Score: 150,
					Reasons: []Reason{
						{Text: "Healthy token", Positive: true},
						{Text: "Not used recently", Positive: true},
					},
				},
				{
					Name:  "personal",
					Score: 100,
					Reasons: []Reason{
						{Text: "Used recently", Positive: false},
					},
				},
			},
		}

		output := FormatResult(result)
		if !strings.Contains(output, "Recommended: work") {
			t.Errorf("output missing recommended profile: %q", output)
		}
		if !strings.Contains(output, "Healthy token") {
			t.Errorf("output missing reason: %q", output)
		}
		if !strings.Contains(output, "Alternatives:") {
			t.Errorf("output missing alternatives section: %q", output)
		}
		if !strings.Contains(output, "personal") {
			t.Errorf("output missing alternative profile: %q", output)
		}
	})

	t.Run("with cooldown profiles", func(t *testing.T) {
		result := &Result{
			Selected:  "work",
			Algorithm: AlgorithmSmart,
			Alternatives: []ProfileScore{
				{
					Name:  "work",
					Score: 150,
					Reasons: []Reason{
						{Text: "Healthy", Positive: true},
					},
				},
				{
					Name:  "blocked",
					Score: -10000,
					Reasons: []Reason{
						{Text: "In cooldown (2h remaining)", Positive: false},
					},
				},
			},
		}

		output := FormatResult(result)
		if !strings.Contains(output, "In cooldown:") {
			t.Errorf("output missing cooldown section: %q", output)
		}
		if !strings.Contains(output, "blocked") {
			t.Errorf("output missing cooldown profile: %q", output)
		}
	})
}

func TestAlgorithmConstants(t *testing.T) {
	// Ensure algorithm constants have expected values
	if AlgorithmSmart != "smart" {
		t.Errorf("AlgorithmSmart = %q, expected 'smart'", AlgorithmSmart)
	}
	if AlgorithmRoundRobin != "round_robin" {
		t.Errorf("AlgorithmRoundRobin = %q, expected 'round_robin'", AlgorithmRoundRobin)
	}
	if AlgorithmRandom != "random" {
		t.Errorf("AlgorithmRandom = %q, expected 'random'", AlgorithmRandom)
	}
}

// timePtr returns a pointer to t.
func timePtr(t time.Time) *time.Time {
	return &t
}

// TestDrainPolicyABCase is the A/B test case from issue #81:
//   - Profile A: 90% used, resets in one hour.
//   - Profile B: 5% used, resets in six days.
//
// In drain-before-reset mode, A should win (its quota expires soonest and it
// is still under the safety ceiling). Under the default availability policy,
// B should win — proving drain is strictly opt-in.
func TestDrainPolicyABCase(t *testing.T) {
	now := time.Now()
	usageData := map[string]*UsageInfo{
		"profile-a": {
			ProfileName:    "profile-a",
			PrimaryPercent: 90,
			AvailScore:     10,
			// One hour out (plus a few seconds so the selector's own clock,
			// read slightly later, still formats the horizon as "1h").
			ResetsAt: timePtr(now.Add(1*time.Hour + 30*time.Second)),
		},
		"profile-b": {
			ProfileName:    "profile-b",
			PrimaryPercent: 5,
			AvailScore:     95,
			ResetsAt:       timePtr(now.Add(6 * 24 * time.Hour)),
		},
	}

	t.Run("drain prefers soonest reset", func(t *testing.T) {
		s := NewSelector(AlgorithmSmart, nil, nil)
		s.SetRNG(rand.New(rand.NewSource(42)))
		s.SetPolicy(PolicyDrain)
		s.SetUsageData(usageData)

		result, err := s.Select("codex", []string{"profile-a", "profile-b"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Selected != "profile-a" {
			t.Errorf("drain policy selected %q, expected 'profile-a' (soonest reset)", result.Selected)
		}

		// Explanation shape from issue #81:
		// "chose X: resets in 42m, 91% used; fallback Y held in reserve"
		for _, want := range []string{"chose profile-a", "resets in 1h", "90% used", "fallback profile-b held in reserve"} {
			if !strings.Contains(result.Explanation, want) {
				t.Errorf("explanation %q missing %q", result.Explanation, want)
			}
		}
	})

	t.Run("default availability policy is unchanged", func(t *testing.T) {
		s := NewSelector(AlgorithmSmart, nil, nil)
		s.SetRNG(rand.New(rand.NewSource(42)))
		// No SetPolicy call: default must remain availability-first.
		s.SetUsageData(usageData)

		result, err := s.Select("codex", []string{"profile-a", "profile-b"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Selected != "profile-b" {
			t.Errorf("default policy selected %q, expected 'profile-b' (most headroom)", result.Selected)
		}
		if result.Explanation != "" {
			t.Errorf("default policy should not emit a drain explanation, got %q", result.Explanation)
		}
	})
}

// TestDrainPolicyCeiling verifies the headroom ceiling: a profile at or above
// the ceiling is held in reserve even if its quota resets soonest.
func TestDrainPolicyCeiling(t *testing.T) {
	now := time.Now()
	usageData := map[string]*UsageInfo{
		"nearly-empty": {
			ProfileName:    "nearly-empty",
			PrimaryPercent: 96, // above the default 95% ceiling
			AvailScore:     4,
			ResetsAt:       timePtr(now.Add(1 * time.Hour)),
		},
		"fresh": {
			ProfileName:    "fresh",
			PrimaryPercent: 5,
			AvailScore:     95,
			ResetsAt:       timePtr(now.Add(6 * 24 * time.Hour)),
		},
	}

	t.Run("default ceiling holds 96% profile in reserve", func(t *testing.T) {
		s := NewSelector(AlgorithmSmart, nil, nil)
		s.SetPolicy(PolicyDrain)
		s.SetUsageData(usageData)

		result, err := s.Select("codex", []string{"nearly-empty", "fresh"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Selected != "fresh" {
			t.Errorf("selected %q, expected 'fresh' (nearly-empty is above the 95%% ceiling)", result.Selected)
		}
	})

	t.Run("raised ceiling re-enables drain candidate", func(t *testing.T) {
		s := NewSelector(AlgorithmSmart, nil, nil)
		s.SetPolicy(PolicyDrain)
		s.SetDrainCeiling(98)
		s.SetUsageData(usageData)

		result, err := s.Select("codex", []string{"nearly-empty", "fresh"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Selected != "nearly-empty" {
			t.Errorf("selected %q, expected 'nearly-empty' under a 98%% ceiling", result.Selected)
		}
	})

	t.Run("secondary window counts toward the ceiling", func(t *testing.T) {
		s := NewSelector(AlgorithmSmart, nil, nil)
		s.SetPolicy(PolicyDrain)
		data := map[string]*UsageInfo{
			"weekly-capped": {
				ProfileName:      "weekly-capped",
				PrimaryPercent:   10,
				SecondaryPercent: 97, // weekly cap nearly exhausted
				AvailScore:       10,
				ResetsAt:         timePtr(now.Add(30 * time.Minute)),
			},
			"fresh": {
				ProfileName:    "fresh",
				PrimaryPercent: 5,
				AvailScore:     95,
				ResetsAt:       timePtr(now.Add(6 * 24 * time.Hour)),
			},
		}
		s.SetUsageData(data)

		result, err := s.Select("codex", []string{"weekly-capped", "fresh"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Selected != "fresh" {
			t.Errorf("selected %q, expected 'fresh' (weekly-capped secondary window is above the ceiling)", result.Selected)
		}
	})
}

// TestDrainPolicyNoResetData verifies that with no drain-eligible candidates
// (no reset timestamps), drain falls back to availability-ranked reserves.
func TestDrainPolicyNoResetData(t *testing.T) {
	usageData := map[string]*UsageInfo{
		"alpha": {ProfileName: "alpha", PrimaryPercent: 60, AvailScore: 40},
		"beta":  {ProfileName: "beta", PrimaryPercent: 20, AvailScore: 80},
	}

	s := NewSelector(AlgorithmSmart, nil, nil)
	s.SetPolicy(PolicyDrain)
	s.SetUsageData(usageData)

	result, err := s.Select("codex", []string{"alpha", "beta"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Selected != "beta" {
		t.Errorf("selected %q, expected 'beta' (highest availability among reserves)", result.Selected)
	}
	if !strings.Contains(result.Explanation, "no drain-eligible profile") {
		t.Errorf("explanation %q should note there was no drain-eligible profile", result.Explanation)
	}
}

func TestSetPolicyIgnoresUnknown(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)
	if s.policy != PolicyAvailability {
		t.Fatalf("default policy = %q, expected availability", s.policy)
	}
	s.SetPolicy(Policy("bogus"))
	if s.policy != PolicyAvailability {
		t.Errorf("unknown policy changed selector policy to %q", s.policy)
	}
	s.SetPolicy(PolicyDrain)
	if s.policy != PolicyDrain {
		t.Errorf("policy = %q, expected drain", s.policy)
	}

	// Ceiling bounds
	if s.drainCeiling != DefaultDrainCeiling {
		t.Fatalf("default ceiling = %d, expected %d", s.drainCeiling, DefaultDrainCeiling)
	}
	s.SetDrainCeiling(0)
	if s.drainCeiling != DefaultDrainCeiling {
		t.Errorf("out-of-range ceiling was applied: %d", s.drainCeiling)
	}
	s.SetDrainCeiling(80)
	if s.drainCeiling != 80 {
		t.Errorf("ceiling = %d, expected 80", s.drainCeiling)
	}
}

func TestSetAvoidRecent(t *testing.T) {
	s := NewSelector(AlgorithmSmart, nil, nil)

	// Default
	if s.avoidRecent != 30*time.Minute {
		t.Errorf("default avoidRecent = %v, expected 30m", s.avoidRecent)
	}

	// After setting
	s.SetAvoidRecent(2 * time.Hour)
	if s.avoidRecent != 2*time.Hour {
		t.Errorf("avoidRecent = %v, expected 2h", s.avoidRecent)
	}
}
