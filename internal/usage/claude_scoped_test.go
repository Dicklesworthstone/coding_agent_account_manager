package usage

// Tests for issue #97: Anthropic reports per-model allowances only in the
// usage response's limits[] array. Ignoring them let an account whose Fable
// quota was spent rank as fully available.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// scopedUsageBody is the shape the OAuth usage endpoint returns: general
// windows nearly idle, a weekly model-scoped allowance exhausted. is_active
// marks the limit currently binding the account, not the ones that apply.
const scopedUsageBody = `{
  "five_hour":  {"utilization": 0.0,  "resets_at": "2026-09-04T03:40:00Z"},
  "seven_day":  {"utilization": 56.0, "resets_at": "2026-09-05T00:00:00Z"},
  "seven_day_opus": null,
  "limits": [
    {"kind": "session",       "group": "session", "percent": 0,   "severity": "normal",   "resets_at": "2026-09-04T03:40:00Z", "scope": null, "is_active": false},
    {"kind": "weekly_all",    "group": "weekly",  "percent": 56,  "severity": "normal",   "resets_at": "2026-09-05T00:00:00Z", "scope": null, "is_active": false},
    {"kind": "weekly_scoped", "group": "weekly",  "percent": 100, "severity": "critical", "resets_at": "2026-09-05T00:00:00Z",
     "scope": {"model": {"id": null, "display_name": "Fable"}, "surface": null}, "is_active": true}
  ]
}`

func fetchFixture(t *testing.T, body string) *UsageInfo {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	fetcher := NewClaudeFetcher()
	fetcher.baseURL = server.URL

	info, err := fetcher.Fetch(context.Background(), "test-token")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return info
}

func TestClaudeFetchParsesScopedLimits(t *testing.T) {
	info := fetchFixture(t, scopedUsageBody)

	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 0 {
		t.Fatalf("primary window = %+v, want 0%%", info.PrimaryWindow)
	}
	if info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 56 {
		t.Fatalf("secondary window = %+v, want 56%%", info.SecondaryWindow)
	}

	fable, ok := info.ModelWindows["Fable"]
	if !ok {
		t.Fatalf("no Fable window; ModelWindows = %v", info.ModelWindows)
	}
	if fable.UsedPercent != 100 || fable.Utilization != 1.0 {
		t.Fatalf("Fable window = %+v, want 100%%", fable)
	}
	if fable.Kind != LimitKindWeeklyScoped || fable.Severity != SeverityCritical || !fable.IsActive {
		t.Fatalf("Fable window lost its descriptive fields: %+v", fable)
	}
	if fable.ResetsAt.IsZero() {
		t.Fatal("Fable window has no reset time")
	}

	// The general windows keep the float precision of the top-level fields but
	// pick up the limits[] annotations.
	if info.PrimaryWindow.Kind != LimitKindSession {
		t.Fatalf("primary window kind = %q, want %q", info.PrimaryWindow.Kind, LimitKindSession)
	}
	if info.SecondaryWindow.Kind != LimitKindWeeklyAll {
		t.Fatalf("secondary window kind = %q, want %q", info.SecondaryWindow.Kind, LimitKindWeeklyAll)
	}
}

// TestExhaustedScopedQuotaIsNotAvailable is the reported failure: five_hour at
// 0% made the account look freshly available while Fable stayed spent.
func TestExhaustedScopedQuotaIsNotAvailable(t *testing.T) {
	info := fetchFixture(t, scopedUsageBody)

	if !info.IsNearLimitForModel(0.8, "fable") {
		t.Error("a 100% Fable quota did not register as near limit for Fable work")
	}
	if !info.IsNearLimitForModel(0.8, "claude-fable-5-20260115") {
		t.Error("a full model identifier did not resolve to the Fable quota")
	}
	if !info.IsNearLimit(0.8) {
		t.Error("with the model unknown, an exhausted scoped quota must still count")
	}
	if info.IsNearLimitForModel(0.8, "claude-sonnet-4-5") {
		t.Error("the Fable quota blocked Sonnet work, which it does not constrain")
	}

	fableScore := info.AvailabilityScoreForModel("fable")
	sonnetScore := info.AvailabilityScoreForModel("claude-sonnet-4-5")
	if fableScore >= sonnetScore {
		t.Errorf("Fable score %d is not below the Sonnet score %d", fableScore, sonnetScore)
	}
	// 100 - 0*50 - 0.56*25 - 1.0*15 = 71
	if fableScore != 71 {
		t.Errorf("AvailabilityScoreForModel(fable) = %d, want 71", fableScore)
	}
	// 100 - 0*50 - 0.56*25 = 86
	if sonnetScore != 86 {
		t.Errorf("AvailabilityScoreForModel(sonnet) = %d, want 86", sonnetScore)
	}
	if score := info.AvailabilityScore(); score != fableScore {
		t.Errorf("AvailabilityScore() = %d; with the model unknown the worst quota must count (%d)", score, fableScore)
	}

	scoped := info.ScopedLimit("fable")
	if scoped == nil || scoped.Label != "Fable" || scoped.UsedPercent != 100 {
		t.Fatalf("ScopedLimit(fable) = %+v", scoped)
	}
	if info.ScopedLimit("claude-sonnet-4-5") != nil {
		t.Error("ScopedLimit reported a Fable quota as constraining Sonnet")
	}
}

// TestScopedRoutingPrefersTheAccountWithQuota is the assertion the issue asks
// for: Fable work must not be routed to the profile whose Fable quota is gone,
// even though its general windows are the emptier of the two.
func TestScopedRoutingPrefersTheAccountWithQuota(t *testing.T) {
	exhausted := fetchFixture(t, scopedUsageBody)
	busy := fetchFixture(t, `{
      "five_hour": {"utilization": 40.0},
      "seven_day": {"utilization": 60.0},
      "limits": [
        {"kind": "weekly_scoped", "group": "weekly", "percent": 12, "severity": "normal",
         "scope": {"model": {"display_name": "Fable"}}, "is_active": false}
      ]
    }`)

	// Eligibility, not ranking, is what keeps the work off the spent account:
	// its general windows are the emptier of the two, so on score alone it
	// would still win.
	if !exhausted.IsNearLimitForModel(0.8, "fable") {
		t.Error("the profile with no Fable quota left was still eligible for Fable work")
	}
	if busy.IsNearLimitForModel(0.8, "fable") {
		t.Error("the busier profile was excluded despite having Fable quota left")
	}
	// The ordering flips for work the Fable quota does not constrain.
	if exhausted.AvailabilityScoreForModel("sonnet") <= busy.AvailabilityScoreForModel("sonnet") {
		t.Errorf("for Sonnet work the idle profile scored %d, not above the busier one at %d",
			exhausted.AvailabilityScoreForModel("sonnet"), busy.AvailabilityScoreForModel("sonnet"))
	}
}

// TestLegacyOpusWindowStillParses covers the older response shape, where the
// premium-model allowance had a top-level field instead of a limits[] entry.
func TestLegacyOpusWindowStillParses(t *testing.T) {
	info := fetchFixture(t, `{
      "five_hour": {"utilization": 10.0},
      "seven_day": {"utilization": 20.0},
      "seven_day_opus": {"utilization": 95.0, "resets_at": "2026-09-05T00:00:00Z"}
    }`)

	if info.TertiaryWindow == nil || info.TertiaryWindow.UsedPercent != 95 {
		t.Fatalf("tertiary window = %+v, want 95%%", info.TertiaryWindow)
	}
	if !info.IsNearLimitForModel(0.8, "claude-opus-4-1") {
		t.Error("the legacy Opus window did not constrain Opus work")
	}
	if info.IsNearLimitForModel(0.8, "claude-sonnet-4-5") {
		t.Error("the legacy Opus window constrained Sonnet work")
	}
}

func TestNormalizeModelKey(t *testing.T) {
	cases := map[string]string{
		"Fable":                     "fable",
		"claude-fable-5-20260115":   "fable520260115",
		"Opus":                      "opus",
		"claude-opus-4-1-20250805":  "opus4120250805",
		"  claude-3-5-sonnet-2024 ": "35sonnet2024",
		"":                          "",
	}
	for in, want := range cases {
		if got := NormalizeModelKey(in); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", in, got, want)
		}
	}

	if !modelKeysMatch(NormalizeModelKey("claude-fable-5"), NormalizeModelKey("Fable")) {
		t.Error("a full Fable identifier did not match the Fable display name")
	}
	if modelKeysMatch(NormalizeModelKey("claude-sonnet-4-5"), NormalizeModelKey("Fable")) {
		t.Error("Sonnet matched the Fable display name")
	}
	if modelKeysMatch(NormalizeModelKey("claude-opus-4"), NormalizeModelKey("")) {
		t.Error("an empty key matched")
	}
}
