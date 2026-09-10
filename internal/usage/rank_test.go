package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rankNow is a fixed clock so every reset horizon in these tables is exact.
var rankNow = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// weekly builds a seven-day window used to `pct` percent, resetting at
// rankNow+in.
func weekly(pct int, in time.Duration) *UsageWindow {
	return &UsageWindow{
		Utilization:    float64(pct) / 100,
		UsedPercent:    pct,
		ResetsAt:       rankNow.Add(in),
		WindowDuration: 7 * 24 * time.Hour,
	}
}

// session builds a five-hour window, the one that rolls over on its own and
// must NOT be what the rank mode sorts on.
func session(pct int, in time.Duration) *UsageWindow {
	return &UsageWindow{
		Utilization:    float64(pct) / 100,
		UsedPercent:    pct,
		ResetsAt:       rankNow.Add(in),
		WindowDuration: 5 * time.Hour,
	}
}

// seat builds one Codex-shaped usage row.
func seat(name string, primary, secondary *UsageWindow, credits *CreditInfo) ProfileUsage {
	return ProfileUsage{
		Provider:    "codex",
		ProfileName: name,
		Usage: &UsageInfo{
			Provider:        "codex",
			ProfileName:     name,
			PlanType:        "pro",
			PrimaryWindow:   primary,
			SecondaryWindow: secondary,
			Credits:         credits,
		},
	}
}

func rank(t *testing.T, rows []ProfileUsage, opts RankOptions) *RankResult {
	t.Helper()
	opts.Now = rankNow
	if opts.Mode == "" {
		opts.Mode = RankEarliestResetHeadroom
	}
	res := RankProfiles(rows, opts)
	if res == nil {
		t.Fatal("RankProfiles returned nil")
	}
	return res
}

// order flattens a result into "name:tier" strings for compact assertions.
func order(res *RankResult) []string {
	out := make([]string, 0, len(res.Profiles))
	for _, p := range res.Profiles {
		out = append(out, p.Profile+":"+p.Tier)
	}
	return out
}

func selected(res *RankResult) string {
	if res.Selected == nil {
		return ""
	}
	return res.Selected.Profile
}

// TestRank_EarliestResetWithHeadroomOrdersTheReportedPool reproduces the
// three-seat Pro pool from issue #105: --best picked the idle seat whose
// window resets LATER, which is the reserve the quota law says to preserve.
func TestRank_EarliestResetWithHeadroomOrdersTheReportedPool(t *testing.T) {
	rows := []ProfileUsage{
		// The 0% seat that resets last: the one --best chose.
		seat("reserve", session(0, time.Hour), weekly(0, 120*time.Hour), nil),
		// The 34% seat that resets soonest: the one to spend.
		seat("spend-me", session(10, 2*time.Hour), weekly(34, 20*time.Hour), nil),
		seat("middle", session(5, 3*time.Hour), weekly(12, 60*time.Hour), nil),
	}

	res := rank(t, rows, RankOptions{})

	if got := selected(res); got != "spend-me" {
		t.Fatalf("selected = %q, want %q (earliest governing reset with headroom)", got, "spend-me")
	}
	want := []string{"spend-me:included_headroom", "middle:included_headroom", "reserve:included_headroom"}
	got := order(res)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
	for i, p := range res.Profiles {
		if p.Rank != i+1 {
			t.Errorf("profile %s rank = %d, want %d", p.Profile, p.Rank, i+1)
		}
	}
	if res.Error != "" {
		t.Errorf("unexpected error on a rankable pool: %q", res.Error)
	}

	// The historical ordering must still be reachable, and must still disagree.
	avail := rank(t, rows, RankOptions{Mode: RankAvailability})
	if got := selected(avail); got != "reserve" {
		t.Errorf("availability mode selected %q, want %q (unchanged lowest-utilization behavior)", got, "reserve")
	}
}

// TestRank_SortsOnTheLongestWindowNotTheSoonest pins the difference from the
// rotation drain policy: a five-hour window that resets in minutes must not
// outrank a weekly allowance that expires tomorrow.
func TestRank_SortsOnTheLongestWindowNotTheSoonest(t *testing.T) {
	rows := []ProfileUsage{
		// Weekly resets far out, but its session window resets in 5 minutes.
		seat("fresh-weekly", session(80, 5*time.Minute), weekly(10, 150*time.Hour), nil),
		// Weekly expires tomorrow: this is the quota actually at risk.
		seat("expiring-weekly", session(20, 4*time.Hour), weekly(40, 24*time.Hour), nil),
	}

	res := rank(t, rows, RankOptions{})

	if got := selected(res); got != "expiring-weekly" {
		t.Fatalf("selected = %q, want %q", got, "expiring-weekly")
	}
	if got := res.Profiles[0].GoverningWindow; got != "secondary" {
		t.Errorf("governing window = %q, want %q (the weekly cap)", got, "secondary")
	}
	// The binding window is still the worst-used one, independently.
	if got := res.Profiles[0].BindingWindow; got != "secondary" {
		t.Errorf("binding window = %q, want %q", got, "secondary")
	}
}

// TestRank_Tiers walks every tier boundary in one table.
func TestRank_Tiers(t *testing.T) {
	credits := &CreditInfo{HasCredits: true}
	noCredits := &CreditInfo{HasCredits: false}

	tests := []struct {
		name         string
		row          ProfileUsage
		ceiling      int
		wantTier     string
		wantEligible bool
		reasonHas    string
	}{
		{
			name:         "headroom under the ceiling is eligible",
			row:          seat("a", session(0, time.Hour), weekly(50, 30*time.Hour), nil),
			wantTier:     TierIncludedHeadroom,
			wantEligible: true,
			reasonHas:    "50% used",
		},
		{
			name:         "at the ceiling counts as spent",
			row:          seat("a", session(0, time.Hour), weekly(95, 30*time.Hour), noCredits),
			wantTier:     TierExhausted,
			wantEligible: false,
			reasonHas:    "no paid credits",
		},
		{
			name:         "spent included quota with paid credits is usable but last",
			row:          seat("a", session(0, time.Hour), weekly(99, 30*time.Hour), credits),
			wantTier:     TierPaidCredits,
			wantEligible: true,
			reasonHas:    "ranks last",
		},
		{
			name:         "unlimited credits count as paid credits",
			row:          seat("a", session(0, time.Hour), weekly(100, 30*time.Hour), &CreditInfo{Unlimited: true}),
			wantTier:     TierPaidCredits,
			wantEligible: true,
			reasonHas:    "paid credits remain",
		},
		{
			name:         "a lower ceiling holds more back in reserve",
			row:          seat("a", session(0, time.Hour), weekly(60, 30*time.Hour), noCredits),
			ceiling:      50,
			wantTier:     TierExhausted,
			wantEligible: false,
			reasonHas:    "ceiling 50%",
		},
		{
			name:         "headroom with no reset time cannot be ordered",
			row:          seat("a", nil, &UsageWindow{UsedPercent: 10, WindowDuration: 7 * 24 * time.Hour}, nil),
			wantTier:     TierUnknown,
			wantEligible: false,
			reasonHas:    "no future reset time",
		},
		{
			name:         "a reset time already in the past cannot be ordered",
			row:          seat("a", nil, weekly(10, -2*time.Hour), nil),
			wantTier:     TierUnknown,
			wantEligible: false,
			reasonHas:    "no future reset time",
		},
		{
			name:         "no windows at all is unknown, not idle",
			row:          seat("a", nil, nil, nil),
			wantTier:     TierUnknown,
			wantEligible: false,
			reasonHas:    "no rate limit window",
		},
		{
			name: "a failed fetch is unknown, not idle",
			row: ProfileUsage{Provider: "codex", ProfileName: "a", Usage: &UsageInfo{
				Provider: "codex", Error: "unauthorized: token expired or invalid",
			}},
			wantTier:     TierUnknown,
			wantEligible: false,
			reasonHas:    "limits could not be read",
		},
		{
			name:         "a row with no usage at all is unknown",
			row:          ProfileUsage{Provider: "codex", ProfileName: "a"},
			wantTier:     TierUnknown,
			wantEligible: false,
			reasonHas:    "no usage data",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := rank(t, []ProfileUsage{tc.row}, RankOptions{HeadroomCeiling: tc.ceiling})
			got := res.Profiles[0]
			if got.Tier != tc.wantTier {
				t.Errorf("tier = %q, want %q (reason: %s)", got.Tier, tc.wantTier, got.Reason)
			}
			if got.Eligible != tc.wantEligible {
				t.Errorf("eligible = %v, want %v (reason: %s)", got.Eligible, tc.wantEligible, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.reasonHas) {
				t.Errorf("reason = %q, want it to mention %q", got.Reason, tc.reasonHas)
			}
			if tc.wantEligible && got.Rank != 1 {
				t.Errorf("rank = %d, want 1", got.Rank)
			}
			if !tc.wantEligible && got.Rank != 0 {
				t.Errorf("rank = %d, want 0 for an ineligible profile", got.Rank)
			}
		})
	}
}

// TestRank_PaidRanksBehindEveryIncludedSeat checks the tier ordering itself,
// including that a paid seat resetting in an hour still loses to an included
// seat resetting in a week.
func TestRank_PaidRanksBehindEveryIncludedSeat(t *testing.T) {
	rows := []ProfileUsage{
		seat("paid-soon", session(99, time.Hour), weekly(99, 2*time.Hour), &CreditInfo{HasCredits: true}),
		seat("dead", session(99, time.Hour), weekly(99, 3*time.Hour), &CreditInfo{HasCredits: false}),
		seat("included-late", session(0, time.Hour), weekly(1, 160*time.Hour), nil),
	}

	res := rank(t, rows, RankOptions{})

	want := []string{"included-late:included_headroom", "paid-soon:paid_credits", "dead:exhausted"}
	if got := order(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
	if got := selected(res); got != "included-late" {
		t.Errorf("selected = %q, want %q", got, "included-late")
	}
	// Ranks number the eligible seats only, in order.
	if res.Profiles[0].Rank != 1 || res.Profiles[1].Rank != 2 || res.Profiles[2].Rank != 0 {
		t.Errorf("ranks = %d,%d,%d; want 1,2,0",
			res.Profiles[0].Rank, res.Profiles[1].Rank, res.Profiles[2].Rank)
	}
}

// TestRank_TiesBreakByName keeps the ordering deterministic for a caller that
// spawns repeatedly.
func TestRank_TiesBreakByName(t *testing.T) {
	rows := []ProfileUsage{
		seat("charlie", session(0, time.Hour), weekly(10, 30*time.Hour), nil),
		seat("alpha", session(0, time.Hour), weekly(10, 30*time.Hour), nil),
		seat("bravo", session(0, time.Hour), weekly(10, 30*time.Hour), nil),
	}

	for i := 0; i < 5; i++ {
		res := rank(t, rows, RankOptions{})
		want := []string{"alpha:included_headroom", "bravo:included_headroom", "charlie:included_headroom"}
		if got := order(res); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("run %d order = %v, want %v", i, got, want)
		}
	}
}

// TestRank_NoSelectableProfileFailsVisibly is the behavior the spawn caller
// depends on: an unreadable pool must produce an explicit failure, never a
// confident answer that a caller would satisfy with a static pin.
func TestRank_NoSelectableProfileFailsVisibly(t *testing.T) {
	tests := []struct {
		name      string
		rows      []ProfileUsage
		errorHas  string
		wantCount int
	}{
		{
			name:      "no rows at all",
			rows:      nil,
			errorHas:  "no profiles to rank",
			wantCount: 0,
		},
		{
			name: "every fetch failed",
			rows: []ProfileUsage{
				{Provider: "codex", ProfileName: "a", Usage: &UsageInfo{Error: "request failed"}},
				{Provider: "codex", ProfileName: "b", Usage: &UsageInfo{Error: "request failed"}},
			},
			errorHas:  "2 unknown",
			wantCount: 2,
		},
		{
			name: "every seat is spent with no credits",
			rows: []ProfileUsage{
				seat("a", session(99, time.Hour), weekly(99, 30*time.Hour), &CreditInfo{}),
				seat("b", session(97, time.Hour), weekly(97, 40*time.Hour), &CreditInfo{}),
			},
			errorHas:  "2 exhausted",
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := rank(t, tc.rows, RankOptions{})
			if res.Selected != nil {
				t.Fatalf("selected = %+v, want none", res.Selected)
			}
			if res.Error == "" {
				t.Fatal("want a non-empty error when nothing is selectable")
			}
			if !strings.Contains(res.Error, tc.errorHas) {
				t.Errorf("error = %q, want it to mention %q", res.Error, tc.errorHas)
			}
			if !strings.Contains(res.Error, "static pin") {
				t.Errorf("error = %q, want it to warn against falling back to a static pin", res.Error)
			}
			if len(res.Profiles) != tc.wantCount {
				t.Errorf("profiles = %d, want %d (ineligible rows stay visible with reasons)",
					len(res.Profiles), tc.wantCount)
			}
		})
	}
}

// TestRank_ModelScopedAllowance covers the Fable case: a spent per-model
// allowance constrains a seat whose general windows read idle, and an OMITTED
// per-model row is not capacity.
func TestRank_ModelScopedAllowance(t *testing.T) {
	withScoped := func(name string, fablePct int, in time.Duration) ProfileUsage {
		row := seat(name, session(0, time.Hour), weekly(5, in), nil)
		row.Provider = "claude"
		row.Usage.Provider = "claude"
		row.Usage.ModelWindows = map[string]*UsageWindow{
			"Fable": {
				Utilization:    float64(fablePct) / 100,
				UsedPercent:    fablePct,
				ResetsAt:       rankNow.Add(in),
				WindowDuration: 7 * 24 * time.Hour,
				Label:          "Fable",
				Kind:           LimitKindWeeklyScoped,
			},
		}
		return row
	}

	t.Run("a spent Fable allowance disqualifies an otherwise idle seat", func(t *testing.T) {
		rows := []ProfileUsage{
			withScoped("fable-spent", 100, 30*time.Hour),
			withScoped("fable-free", 20, 90*time.Hour),
		}
		res := rank(t, rows, RankOptions{Model: "fable"})

		if got := selected(res); got != "fable-free" {
			t.Fatalf("selected = %q, want %q", got, "fable-free")
		}
		spent := res.Profiles[1]
		if spent.Tier != TierExhausted {
			t.Errorf("tier = %q, want %q (reason: %s)", spent.Tier, TierExhausted, spent.Reason)
		}
		if spent.BindingWindow != "scoped" {
			t.Errorf("binding window = %q, want %q", spent.BindingWindow, "scoped")
		}
		if spent.ScopedLimit == nil || spent.ScopedLimit.Label != "Fable" {
			t.Errorf("scoped limit = %+v, want the Fable allowance", spent.ScopedLimit)
		}
	})

	t.Run("an omitted Fable row is unknown, not capacity", func(t *testing.T) {
		missing := seat("no-fable-row", session(0, time.Hour), weekly(5, 30*time.Hour), nil)
		missing.Provider = "claude"
		missing.Usage.Provider = "claude"

		rows := []ProfileUsage{missing, withScoped("fable-free", 20, 90*time.Hour)}
		res := rank(t, rows, RankOptions{Model: "fable", RequireModelWindow: true})

		if got := selected(res); got != "fable-free" {
			t.Fatalf("selected = %q, want %q", got, "fable-free")
		}
		var got RankedProfile
		for _, p := range res.Profiles {
			if p.Profile == "no-fable-row" {
				got = p
			}
		}
		if got.Tier != TierUnknown || got.Eligible {
			t.Errorf("tier=%q eligible=%v, want unknown/ineligible (reason: %s)", got.Tier, got.Eligible, got.Reason)
		}
		if !strings.Contains(got.Reason, "omitted row") {
			t.Errorf("reason = %q, want it to say an omitted row is not capacity", got.Reason)
		}
	})

	t.Run("without the requirement an omitted row is only ranked, not refused", func(t *testing.T) {
		missing := seat("no-fable-row", session(0, time.Hour), weekly(5, 30*time.Hour), nil)
		missing.Provider = "claude"
		missing.Usage.Provider = "claude"

		res := rank(t, []ProfileUsage{missing}, RankOptions{Model: "fable", RequireModelWindow: false})
		if got := selected(res); got != "no-fable-row" {
			t.Errorf("selected = %q, want the seat to be rankable when the requirement is off", got)
		}
	})
}

// TestRank_JSONShapeIsStable pins the contract a spawn-time caller reads.
func TestRank_JSONShapeIsStable(t *testing.T) {
	rows := []ProfileUsage{
		seat("spend-me", session(10, 2*time.Hour), weekly(34, 20*time.Hour), &CreditInfo{HasCredits: true}),
		seat("reserve", session(0, time.Hour), weekly(0, 120*time.Hour), nil),
	}
	res := rank(t, rows, RankOptions{Provider: "codex"})

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var payload struct {
		Rank            string `json:"rank"`
		Provider        string `json:"provider"`
		HeadroomCeiling int    `json:"headroom_ceiling_percent"`
		Selected        *struct {
			Provider        string `json:"provider"`
			Profile         string `json:"profile"`
			Rank            int    `json:"rank"`
			Eligible        bool   `json:"eligible"`
			Tier            string `json:"tier"`
			UsedPercent     int    `json:"used_percent"`
			HeadroomPercent int    `json:"headroom_percent"`
			ResetsAt        string `json:"resets_at"`
			ResetsInSeconds int64  `json:"resets_in_seconds"`
			HasCredits      bool   `json:"has_credits"`
			Reason          string `json:"reason"`
		} `json:"selected"`
		Profiles []map[string]any `json:"profiles"`
		Error    string           `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if payload.Rank != RankEarliestResetHeadroom {
		t.Errorf("rank = %q, want %q", payload.Rank, RankEarliestResetHeadroom)
	}
	if payload.Provider != "codex" {
		t.Errorf("provider = %q, want codex", payload.Provider)
	}
	if payload.HeadroomCeiling != DefaultHeadroomCeiling {
		t.Errorf("headroom_ceiling_percent = %d, want %d", payload.HeadroomCeiling, DefaultHeadroomCeiling)
	}
	if payload.Selected == nil {
		t.Fatal("selected is null on a rankable pool")
	}
	if payload.Selected.Profile != "spend-me" || payload.Selected.Rank != 1 || !payload.Selected.Eligible {
		t.Errorf("selected = %+v, want spend-me at rank 1", payload.Selected)
	}
	if payload.Selected.HeadroomPercent != 66 {
		t.Errorf("headroom_percent = %d, want 66", payload.Selected.HeadroomPercent)
	}
	if payload.Selected.ResetsInSeconds != int64((20 * time.Hour).Seconds()) {
		t.Errorf("resets_in_seconds = %d, want %d", payload.Selected.ResetsInSeconds, int64((20 * time.Hour).Seconds()))
	}
	if !payload.Selected.HasCredits {
		t.Error("has_credits = false, want true")
	}
	if len(payload.Profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(payload.Profiles))
	}
	if payload.Error != "" {
		t.Errorf("error = %q, want empty", payload.Error)
	}

	// A null `selected` plus a non-empty `error` is the documented failure
	// shape; a caller keys on exactly these two fields.
	fail := rank(t, nil, RankOptions{Provider: "codex"})
	failData, err := json.Marshal(fail)
	if err != nil {
		t.Fatalf("marshal failure: %v", err)
	}
	if !strings.Contains(string(failData), `"selected":null`) {
		t.Errorf("failure payload must carry selected:null, got %s", failData)
	}
	var failPayload map[string]any
	if err := json.Unmarshal(failData, &failPayload); err != nil {
		t.Fatalf("unmarshal failure: %v", err)
	}
	if msg, _ := failPayload["error"].(string); msg == "" {
		t.Error("failure payload must carry a non-empty error")
	}
}

// TestNormalizeRankMode covers the spellings a caller may type.
func TestNormalizeRankMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"earliest-reset-headroom", RankEarliestResetHeadroom, true},
		{"earliest-reset-with-headroom", RankEarliestResetHeadroom, true},
		{"earliest_reset_headroom", RankEarliestResetHeadroom, true},
		{"  EARLIEST-RESET-HEADROOM  ", RankEarliestResetHeadroom, true},
		{"availability", RankAvailability, true},
		{"best", "", false},
		{"", "", false},
		{"drain", "", false},
	}
	for _, tc := range tests {
		got, ok := NormalizeRankMode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeRankMode(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestGoverningWindow_PicksTheLongestAllowance covers the fallback path where
// no window reports a duration.
func TestGoverningWindow_PicksTheLongestAllowance(t *testing.T) {
	t.Run("longest duration wins", func(t *testing.T) {
		u := &UsageInfo{PrimaryWindow: session(0, time.Hour), SecondaryWindow: weekly(0, 100*time.Hour)}
		got := GoverningWindow(u, "")
		if got != u.SecondaryWindow {
			t.Errorf("got %+v, want the weekly window", got)
		}
	})

	t.Run("with no durations the furthest reset stands in", func(t *testing.T) {
		u := &UsageInfo{
			PrimaryWindow:   &UsageWindow{UsedPercent: 1, ResetsAt: rankNow.Add(time.Hour)},
			SecondaryWindow: &UsageWindow{UsedPercent: 1, ResetsAt: rankNow.Add(100 * time.Hour)},
		}
		if got := GoverningWindow(u, ""); got != u.SecondaryWindow {
			t.Errorf("got %+v, want the furthest-out window", got)
		}
	})

	t.Run("a window with no reset time is never governing", func(t *testing.T) {
		u := &UsageInfo{
			PrimaryWindow:   weekly(0, 10*time.Hour),
			SecondaryWindow: &UsageWindow{UsedPercent: 1, WindowDuration: 30 * 24 * time.Hour},
		}
		if got := GoverningWindow(u, ""); got != u.PrimaryWindow {
			t.Errorf("got %+v, want the only window with a reset time", got)
		}
	})

	t.Run("nil usage has no governing window", func(t *testing.T) {
		if got := GoverningWindow(nil, ""); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
}

// TestRank_DoesNotMutateInput keeps this a read: a caller may rank the same
// rows twice, or rank and then render them.
func TestRank_DoesNotMutateInput(t *testing.T) {
	rows := []ProfileUsage{
		seat("b", session(0, time.Hour), weekly(10, 30*time.Hour), nil),
		seat("a", session(0, time.Hour), weekly(20, 10*time.Hour), nil),
	}
	before := []string{rows[0].ProfileName, rows[1].ProfileName}

	res := rank(t, rows, RankOptions{})
	if selected(res) != "a" {
		t.Fatalf("selected = %q, want a", selected(res))
	}
	if rows[0].ProfileName != before[0] || rows[1].ProfileName != before[1] {
		t.Errorf("input rows were reordered: %v -> %v",
			before, []string{rows[0].ProfileName, rows[1].ProfileName})
	}
}

// TestRank_OverTheCodexWireFormat runs the reported three-seat Pro pool
// through the real Codex usage decoder and then the ranker, so the ordering
// is proven against the provider's own JSON rather than hand-built windows.
func TestRank_OverTheCodexWireFormat(t *testing.T) {
	// resets_at is a Unix seconds field on the wire; limit_window_seconds is
	// what tells the ranker which allowance is the weekly one.
	body := func(weeklyPct int, weeklyResetIn time.Duration, hasCredits bool) string {
		return fmt.Sprintf(`{
		  "plan_type": "pro",
		  "rate_limit": {
		    "primary_window":   {"used_percent": 4,  "reset_at": %d, "limit_window_seconds": 18000},
		    "secondary_window": {"used_percent": %d, "reset_at": %d, "limit_window_seconds": 604800}
		  },
		  "credits": {"has_credits": %t, "unlimited": false}
		}`,
			rankNow.Add(2*time.Hour).Unix(),
			weeklyPct,
			rankNow.Add(weeklyResetIn).Unix(),
			hasCredits)
	}

	seats := []struct {
		name          string
		weeklyPct     int
		weeklyResetIn time.Duration
		hasCredits    bool
	}{
		// The idle seat whose weekly window resets LAST: what --best picks.
		{"reserve", 0, 120 * time.Hour, false},
		// The 34% seat whose weekly window resets SOONEST: what to spend.
		{"spend-me", 34, 20 * time.Hour, false},
		// Included quota spent, paid credits left: usable, but last.
		{"on-credits", 99, 40 * time.Hour, true},
	}

	rows := make([]ProfileUsage, 0, len(seats))
	for _, s := range seats {
		payload := body(s.weeklyPct, s.weeklyResetIn, s.hasCredits)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, payload)
		}))
		defer server.Close()

		fetcher := NewCodexFetcher()
		fetcher.baseURL = server.URL
		info, err := fetcher.Fetch(context.Background(), "token")
		if err != nil {
			t.Fatalf("fetch %s: %v", s.name, err)
		}
		info.ProfileName = s.name
		rows = append(rows, ProfileUsage{Provider: "codex", ProfileName: s.name, Usage: info})
	}

	res := rank(t, rows, RankOptions{Provider: "codex"})

	want := []string{"spend-me:included_headroom", "reserve:included_headroom", "on-credits:paid_credits"}
	if got := order(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got := selected(res); got != "spend-me" {
		t.Errorf("selected = %q, want spend-me", got)
	}
	// The weekly window must be what the ordering keys on, not the shared
	// five-hour window every seat reports identically.
	if got := res.Profiles[0].GoverningWindow; got != "secondary" {
		t.Errorf("governing window = %q, want secondary (the weekly cap)", got)
	}
	if got := res.Profiles[0].UsedPercent; got != 34 {
		t.Errorf("used_percent = %d, want 34 from the weekly window", got)
	}
}

// TestRank_RolledCachedWindowIsNotIdle covers the offline (--cached) shape: a
// snapshot whose window had already reset reports 0% with a reset time in the
// past. That zero is not a measurement, and must never present the seat as the
// idlest one available.
func TestRank_RolledCachedWindowIsNotIdle(t *testing.T) {
	rolled := ProfileUsage{Provider: "claude", ProfileName: "stale", Usage: &UsageInfo{
		Provider: "claude", Source: SourceCache,
		PrimaryWindow: &UsageWindow{
			UsedPercent: 0, ResetsAt: rankNow.Add(-3 * time.Hour),
			WindowDuration: 5 * time.Hour, Rolled: true,
		},
		SecondaryWindow: &UsageWindow{
			UsedPercent: 0, ResetsAt: rankNow.Add(-2 * time.Hour),
			WindowDuration: 7 * 24 * time.Hour, Rolled: true,
		},
	}}
	live := seat("live", session(0, time.Hour), weekly(40, 30*time.Hour), nil)

	res := rank(t, []ProfileUsage{rolled, live}, RankOptions{})

	if got := selected(res); got != "live" {
		t.Fatalf("selected = %q, want %q: a rolled snapshot's 0%% is not headroom", got, "live")
	}
	var stale RankedProfile
	for _, p := range res.Profiles {
		if p.Profile == "stale" {
			stale = p
		}
	}
	if stale.Tier != TierUnknown || stale.Eligible {
		t.Errorf("tier=%q eligible=%v, want unknown/ineligible (reason: %s)", stale.Tier, stale.Eligible, stale.Reason)
	}
	if !strings.Contains(stale.Reason, "no future reset time") {
		t.Errorf("reason = %q, want it to name the stale reset time", stale.Reason)
	}
}

// TestRankProfiles_NormalizesTheModeItReports keeps the emitted `rank` field
// from ever naming an ordering that was not applied.
func TestRankProfiles_NormalizesTheModeItReports(t *testing.T) {
	rows := []ProfileUsage{seat("a", session(0, time.Hour), weekly(10, 30*time.Hour), nil)}

	for _, in := range []string{"", "earliest-reset-with-headroom", "nonsense"} {
		res := RankProfiles(rows, RankOptions{Mode: in, Now: rankNow})
		if res.Rank != RankEarliestResetHeadroom {
			t.Errorf("Mode %q reported rank %q, want %q", in, res.Rank, RankEarliestResetHeadroom)
		}
	}
	res := RankProfiles(rows, RankOptions{Mode: "availability", Now: rankNow})
	if res.Rank != RankAvailability {
		t.Errorf("rank = %q, want %q", res.Rank, RankAvailability)
	}
}
