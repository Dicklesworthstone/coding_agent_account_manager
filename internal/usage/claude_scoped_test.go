package usage

// Tests for issue #97: Anthropic reports per-model allowances only in the
// usage response's limits[] array. Ignoring them let an account whose Fable
// quota was spent rank as fully available.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	info, err := fetchClaudeFixtureResult(t, body)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return info
}

func fetchClaudeFixtureResult(t *testing.T, body string) (*UsageInfo, error) {
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

	return fetcher.Fetch(context.Background(), "test-token")
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

// An exact model ID and its family label can both be present. They are
// simultaneous constraints, not alternative representations where the exact
// key is allowed to hide a spent family allowance (or vice versa).
func TestOverlappingScopedLimitsCannotHideExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"family exhausted", `{"five_hour":{"utilization":0},"seven_day":{"utilization":0},"limits":[
			{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Opus"}}},
			{"kind":"weekly_scoped","percent":10,"scope":{"model":{"id":"claude-opus-4-1"}}}]}`},
		{"specific model exhausted", `{"five_hour":{"utilization":0},"seven_day":{"utilization":0},"limits":[
			{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Opus"}}},
			{"kind":"weekly_scoped","percent":100,"scope":{"model":{"id":"claude-opus-4-1"}}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := fetchFixture(t, tc.body)
			before, err := json.Marshal(info)
			if err != nil {
				t.Fatal(err)
			}
			for _, model := range []string{"Opus", "claude-opus-4-1", "CLAUDE-OPUS-4-1-20250805"} {
				// Repeated reads must not depend on Go's map iteration order.
				for repeat := 0; repeat < 64; repeat++ {
					if !info.IsNearLimitForModel(1, model) {
						t.Fatalf("%s ignored an exhausted applicable allowance", model)
					}
					if w := info.WindowForModel(model); w == nil || w.UsedPercent != 100 {
						t.Fatalf("%s displayed the less constrained model window: %+v", model, w)
					}
					if w := info.ScopedLimit(model); w == nil || w.UsedPercent != 100 {
						t.Fatalf("%s selected the wrong scoped limit: %+v", model, w)
					}
					if score := info.AvailabilityScoreForModel(model); score != 85 {
						t.Fatalf("%s scored %d instead of retaining the full scoped penalty", model, score)
					}
				}
			}
			if info.IsNearLimitForModel(0.8, "sonnet") || info.AvailabilityScoreForModel("sonnet") != 100 {
				t.Fatal("an Opus-only limit constrained unrelated Sonnet work")
			}
			after, err := json.Marshal(info)
			if err != nil || string(before) != string(after) {
				t.Fatal("model lookup rewrote the provider's measurements")
			}
		})
	}
}

func TestScopedWindowSelectionUsesAllMatchingMeasurements(t *testing.T) {
	exact := &UsageWindow{Label: "Opus", Utilization: 0.1, UsedPercent: 10}
	family := &UsageWindow{Label: "Opus", UsedPercent: 95}
	unknown := &UsageWindow{Label: "Opus", Unmeasured: true, Utilization: 1}
	for _, tc := range []struct {
		name    string
		windows map[string]*UsageWindow
		want    *UsageWindow
	}{
		{"exact plus family", map[string]*UsageWindow{"opus": exact, "claude-opus-4-1": family}, family},
		{"label-only match", map[string]*UsageWindow{"opus": exact, "opaque-window-id": family}, family},
		{"nil exact key", map[string]*UsageWindow{"opus": nil, "family": family}, family},
		{"unmeasured exact key", map[string]*UsageWindow{"opus": unknown, "family": family}, family},
		{"unmeasured family", map[string]*UsageWindow{"opus": family, "family": unknown}, family},
		{"unknown only remains unknown", map[string]*UsageWindow{"family": unknown}, unknown},
		{"all nil", map[string]*UsageWindow{"opus": nil, "family": nil}, nil},
		{"no applicable scope", map[string]*UsageWindow{"sonnet": {Label: "Sonnet", UsedPercent: 100}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &UsageInfo{ModelWindows: tc.windows}
			for repeat := 0; repeat < 64; repeat++ {
				if got := info.scopedWindowForModel("opus"); got != tc.want {
					t.Fatalf("wrong governing scope: got=%+v want=%+v", got, tc.want)
				}
			}
			if tc.want == family && !info.IsNearLimitForModel(0.9, "opus") {
				t.Fatal("a matched measured limit was hidden by unknown data")
			}
		})
	}
	// Equal percentages have a stable diagnostic winner, not a map-order tie.
	first := &UsageWindow{Label: "Opus", Utilization: 0.505, UsedPercent: 50}
	second := &UsageWindow{Label: "Opus", Utilization: 0.505, UsedPercent: 50}
	info := &UsageInfo{ModelWindows: map[string]*UsageWindow{"z": second, "a": first}}
	for repeat := 0; repeat < 64; repeat++ {
		if info.scopedWindowForModel("opus") != first {
			t.Fatal("equal scopes changed diagnostics with map order")
		}
	}
	second.Utilization = 0.509
	if info.scopedWindowForModel("opus") != second {
		t.Fatal("fractional utilization was rounded before selecting the tightest cap")
	}
	if (*UsageInfo)(nil).scopedWindowForModel("opus") != nil || info.scopedWindowForModel("") != nil {
		t.Fatal("empty model or nil usage invented a scoped window")
	}
}

// Issue #133 has no captured verified-Ultra response. These are synthetic
// compatibility documents, shared with the cache tests so both entry points
// preserve the same restrictions without guessing an account's plan.
const (
	claudeCompatibilityWeekly      = `"seven_day":{"utilization":25,"resets_at":"2099-01-05T00:00:00Z"}`
	claudeCompatibilityWeeklyLimit = `{"kind":"weekly_all","group":"weekly","percent":25,"resets_at":"2099-01-05T00:00:00Z"}`
	claudeCompatibilityFableLimit  = `{"kind":"weekly_scoped","group":"weekly","percent":100,"resets_at":"2099-01-05T00:00:00Z","scope":{"model":{"id":null,"display_name":"Fable"}},"is_active":false}`
)

func claudeCompatibilityBody(fields string, limits ...string) string {
	if fields != "" {
		fields += ","
	}
	return "{" + fields + `"limits":[` + strings.Join(limits, ",") + "]}"
}

type claudeSessionCompatibilityCase struct {
	name           string
	body           string
	wantPrimary    bool
	primaryPercent float64
}

func claudeSessionCompatibilityCases() []claudeSessionCompatibilityCase {
	return []claudeSessionCompatibilityCase{
		{"session absent", claudeCompatibilityBody(claudeCompatibilityWeekly, claudeCompatibilityFableLimit), false, 0},
		{"whole session null", claudeCompatibilityBody(`"five_hour":null,`+claudeCompatibilityWeekly, claudeCompatibilityFableLimit), false, 0},
		{"session utilization null", claudeCompatibilityBody(`"five_hour":{"utilization":null,"resets_at":null},`+claudeCompatibilityWeekly, claudeCompatibilityFableLimit), false, 0},
		{"limits session absent", claudeCompatibilityBody("", claudeCompatibilityWeeklyLimit, claudeCompatibilityFableLimit), false, 0},
		{"limits session percent null", claudeCompatibilityBody("", `{"kind":"session","group":"session","percent":null,"resets_at":null,"is_active":true}`, claudeCompatibilityWeeklyLimit, claudeCompatibilityFableLimit), false, 0},
		{"both session measurements null", claudeCompatibilityBody(`"five_hour":{"utilization":null,"resets_at":null}`, `{"kind":"session","percent":null}`, claudeCompatibilityWeeklyLimit, claudeCompatibilityFableLimit), false, 0},
		{"numeric zero without reset", claudeCompatibilityBody(`"five_hour":{"utilization":0,"resets_at":null},`+claudeCompatibilityWeekly, claudeCompatibilityFableLimit), true, 0},
		{"limits zero without reset", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"session","percent":0,"resets_at":null}`, claudeCompatibilityFableLimit), true, 0},
		{"conventional session window", claudeCompatibilityBody(`"five_hour":{"utilization":12.5,"resets_at":"2099-01-01T05:00:00Z"},`+claudeCompatibilityWeekly, claudeCompatibilityFableLimit), true, 12.5},
		{"conventional limits session", claudeCompatibilityBody("", `{"kind":"session","group":"session","percent":12.5,"resets_at":"2099-01-01T05:00:00Z"}`, claudeCompatibilityWeeklyLimit, claudeCompatibilityFableLimit), true, 12.5},
		{"null top session retains measured limit", claudeCompatibilityBody(`"five_hour":{"utilization":null},`+claudeCompatibilityWeekly, `{"kind":"session","percent":12.5}`, claudeCompatibilityFableLimit), true, 12.5},
		{"null limit retains measured top session", claudeCompatibilityBody(`"five_hour":{"utilization":12.5},`+claudeCompatibilityWeekly, `{"kind":"session","percent":null}`, claudeCompatibilityFableLimit), true, 12.5},
		{"null legacy weekly retains measured limit", claudeCompatibilityBody(`"five_hour":null,"seven_day":{"utilization":null}`, claudeCompatibilityWeeklyLimit, claudeCompatibilityFableLimit), false, 0},
		{"unknown null limit is inapplicable", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"future_all","group":"monthly","percent":null}`, claudeCompatibilityFableLimit), false, 0},
		{"null model limit does not erase measured one", claudeCompatibilityBody(claudeCompatibilityWeekly, claudeCompatibilityFableLimit, `{"kind":"weekly_scoped","percent":null,"scope":{"model":{"display_name":"Fable"}}}`), false, 0},
	}
}

func assertClaudeSessionCompatibility(t *testing.T, info *UsageInfo, tc claudeSessionCompatibilityCase) {
	t.Helper()
	if info == nil || info.Error != "" || !info.NumericQuotaKnown() {
		t.Fatalf("valid remaining quota became unavailable: %+v", info)
	}
	if (info.PrimaryWindow != nil) != tc.wantPrimary {
		t.Fatalf("primary window = %+v, want present=%v", info.PrimaryWindow, tc.wantPrimary)
	}
	if w := info.PrimaryWindow; w != nil {
		if w.Utilization != tc.primaryPercent/100 || w.UsedPercent != int(tc.primaryPercent) || w.WindowDuration != 5*time.Hour || w.Unmeasured {
			t.Fatalf("measured session changed: %+v", w)
		}
		if tc.primaryPercent == 0 && !w.ResetsAt.IsZero() {
			t.Fatalf("zero measurement acquired a fabricated reset: %+v", w)
		}
	}
	if w := info.SecondaryWindow; w == nil || w.Utilization != 0.25 || w.UsedPercent != 25 || w.WindowDuration != 7*24*time.Hour || w.Unmeasured {
		t.Fatalf("weekly measurement lost or changed: %+v", w)
	}
	if w := info.WindowForModel("fable"); w == nil || w.Utilization != 1 || w.UsedPercent != 100 || w.WindowDuration != 7*24*time.Hour {
		t.Fatalf("model restriction lost or changed: %+v", w)
	}
	if !info.IsNearLimitForModel(0.8, "fable") || !info.IsNearLimit(0.8) || info.IsNearLimitForModel(0.8, "sonnet") {
		t.Fatal("optional session changed which model the remaining quota constrains")
	}
	for _, mode := range []string{RankAvailability, RankEarliestResetHeadroom} {
		for _, model := range []string{"fable", "sonnet", ""} {
			result := RankProfiles([]ProfileUsage{{Provider: "claude", ProfileName: "optional-session", Usage: info}}, RankOptions{
				Mode: mode, Model: model, Now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
			})
			if got, want := result.Selected != nil, model == "sonnet"; got != want {
				t.Fatalf("%s rank for %q selected=%v, want %v: %+v", mode, model, got, want, result.Profiles)
			}
		}
	}
}

func TestClaudeUsageCompatibilityOptionalSession(t *testing.T) {
	for _, tc := range claudeSessionCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info := fetchFixture(t, tc.body)
			assertClaudeSessionCompatibility(t, info, tc)
		})
	}
}

type claudeWeeklyCompatibilityCase struct {
	name, body  string
	wantPercent int
}

func claudeWeeklyOnlyCompatibilityCases() []claudeWeeklyCompatibilityCase {
	return []claudeWeeklyCompatibilityCase{
		{"weekly top-level only", `{` + claudeCompatibilityWeekly + `}`, 25},
		{"weekly limit only", claudeCompatibilityBody("", claudeCompatibilityWeeklyLimit), 25},
		{"weekly group omitted", claudeCompatibilityBody("", `{"kind":"weekly_all","percent":25,"resets_at":"2099-01-05T00:00:00Z"}`), 25},
		{"empty general scope", claudeCompatibilityBody("", `{"kind":"weekly_all","percent":25,"resets_at":"2099-01-05T00:00:00Z","scope":{}}`), 25},
		{"null scope dimensions", claudeCompatibilityBody("", `{"kind":"weekly_all","percent":25,"resets_at":"2099-01-05T00:00:00Z","scope":{"model":null,"surface":null}}`), 25},
		{"weekly cap exhausted", `{"five_hour":{"utilization":null},"seven_day":{"utilization":100,"resets_at":"2099-01-05T00:00:00Z"}}`, 100},
	}
}

func assertClaudeWeeklyOnlyCompatibility(t *testing.T, info *UsageInfo, wantPercent int) {
	t.Helper()
	if info == nil || !info.NumericQuotaKnown() || info.PrimaryWindow != nil || info.SecondaryWindow == nil || info.TertiaryWindow != nil || len(info.ModelWindows) != 0 {
		t.Fatalf("weekly-only document invented or lost a window: %+v", info)
	}
	if w := info.SecondaryWindow; w.WindowDuration != 7*24*time.Hour || w.UsedPercent != wantPercent || w.Utilization != float64(wantPercent)/100 {
		t.Fatalf("weekly measurement = %+v, want %d%% over 7 days", w, wantPercent)
	}
	spent := wantPercent == 100
	if info.IsNearLimitForModel(0.8, "sonnet") != spent || info.IsNearLimitForModel(0.8, "fable") != spent {
		t.Fatal("weekly-only general cap did not constrain all models")
	}
	for _, mode := range []string{RankAvailability, RankEarliestResetHeadroom} {
		result := RankProfiles([]ProfileUsage{{Provider: "claude", ProfileName: "weekly-only", Usage: info}}, RankOptions{
			Mode: mode, Now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		})
		if (result.Selected != nil) == spent {
			t.Fatalf("%s weekly-only eligibility did not follow the weekly cap: %+v", mode, result)
		}
		// Requiring a model window must still reject an account that did not
		// report one, even though its general weekly capacity is measured.
		result = RankProfiles([]ProfileUsage{{Provider: "claude", ProfileName: "weekly-only", Usage: info}}, RankOptions{
			Mode: mode, Model: "fable", RequireModelWindow: true, Now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		})
		if result.Selected != nil {
			t.Fatal("weekly-only response fabricated a required model allowance")
		}
	}
}

func TestClaudeUsageCompatibilityWeeklyOnly(t *testing.T) {
	for _, tc := range claudeWeeklyOnlyCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertClaudeWeeklyOnlyCompatibility(t, fetchFixture(t, tc.body), tc.wantPercent)
		})
	}
}

type claudeDocumentCase struct{ name, body string }

func claudeMalformedCompatibilityCases() []claudeDocumentCase {
	var cases []claudeDocumentCase
	for _, measurement := range []struct{ name, value string }{
		{"missing", ""}, {"string", `"0"`}, {"boolean", "false"},
		{"object", "{}"}, {"array", "[]"}, {"negative", "-0.1"},
		{"above cap", "100.1"}, {"overflow", "1e309"},
	} {
		window := `{}`
		limit := `{"kind":"session"}`
		if measurement.value != "" {
			window = `{"utilization":` + measurement.value + `}`
			limit = `{"kind":"session","percent":` + measurement.value + `}`
		}
		cases = append(cases,
			claudeDocumentCase{"window " + measurement.name, claudeCompatibilityBody(`"five_hour":`+window+`,`+claudeCompatibilityWeekly, claudeCompatibilityFableLimit)},
			claudeDocumentCase{"limit " + measurement.name, claudeCompatibilityBody(claudeCompatibilityWeekly, limit, claudeCompatibilityFableLimit)},
		)
	}
	return append(cases,
		claudeDocumentCase{"session is an array", claudeCompatibilityBody(`"five_hour":[],`+claudeCompatibilityWeekly, claudeCompatibilityFableLimit)},
		claudeDocumentCase{"null array entry", claudeCompatibilityBody(claudeCompatibilityWeekly, `null`, claudeCompatibilityFableLimit)},
		claudeDocumentCase{"nonobject array entry", claudeCompatibilityBody(claudeCompatibilityWeekly, `[]`, claudeCompatibilityFableLimit)},
		claudeDocumentCase{"unknown kind missing measurement", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"future_all"}`, claudeCompatibilityFableLimit)},
		claudeDocumentCase{"unknown kind malformed measurement", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"future_all","percent":"unknown"}`, claudeCompatibilityFableLimit)},
		claudeDocumentCase{"model malformed measurement", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"weekly_scoped","percent":false,"scope":{"model":{"display_name":"Fable"}}}`)},
	)
}

func assertClaudeQuotaNotRoutable(t *testing.T, info *UsageInfo) {
	t.Helper()
	if info == nil || info.NumericQuotaKnown() || info.AvailabilityScore() != 0 {
		t.Fatalf("incomplete quota became capacity: %+v", info)
	}
	for _, mode := range []string{RankAvailability, RankEarliestResetHeadroom} {
		result := RankProfiles([]ProfileUsage{{Provider: "claude", ProfileName: "incomplete", Usage: info}}, RankOptions{
			Mode: mode, Model: "sonnet", Now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		})
		if result.Selected != nil || len(result.Profiles) != 1 || result.Profiles[0].Eligible {
			t.Fatalf("%s rank selected incomplete quota: %+v", mode, result)
		}
	}
	if len(profilesBelowThreshold([]ProfileUsage{{Provider: "claude", ProfileName: "incomplete", Usage: info}}, 1)) != 0 {
		t.Fatal("threshold routing selected incomplete quota")
	}
}

func TestClaudeUsageCompatibilityRejectsMalformedMeasurements(t *testing.T) {
	for _, tc := range claudeMalformedCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := fetchClaudeFixtureResult(t, tc.body)
			if err == nil || info == nil || info.QuotaStatus != QuotaUnavailable || info.Error == "" {
				t.Fatalf("malformed document accepted: info=%+v err=%v", info, err)
			}
			assertClaudeQuotaNotRoutable(t, info)
		})
	}
}

type claudeDegradedCompatibilityCase struct {
	name, body             string
	wantWeekly, wantScoped bool
}

func claudeDegradedCompatibilityCases() []claudeDegradedCompatibilityCase {
	cases := []claudeDegradedCompatibilityCase{
		{"null window alone", `{"five_hour":{"utilization":null,"resets_at":null}}`, false, false},
		{"null limit alone", `{"limits":[{"kind":"session","percent":null}]}`, false, false},
		{"only model quota", claudeCompatibilityBody("", claudeCompatibilityFableLimit), false, true},
		{"null general and measured model", claudeCompatibilityBody(`"five_hour":{"utilization":null},"seven_day":{"utilization":null}`, claudeCompatibilityFableLimit), false, true},
	}
	for _, limit := range []struct{ name, body string }{
		{"unknown general kind", `{"kind":"future_all","group":"monthly","percent":0,"is_active":false}`},
		{"unknown kind with familiar group", `{"kind":"future_all","group":"weekly","percent":100}`},
		{"general session future group", `{"kind":"session","group":"future","percent":100}`},
		{"general session contradictory group", `{"kind":"session","group":"weekly","percent":100}`},
		{"general weekly contradictory group", `{"kind":"weekly_all","group":"session","percent":100}`},
		{"unnamed scoped limit", `{"kind":"weekly_scoped","group":"weekly","percent":100,"scope":null}`},
		{"empty model identity", `{"kind":"weekly_scoped","percent":100,"scope":{"model":{"id":null,"display_name":" "}}}`},
		{"unnamed model on general kind", `{"kind":"weekly_all","percent":100,"scope":{"model":{}}}`},
		{"surface-only limit", `{"kind":"session","percent":100,"scope":{"surface":{"display_name":"Claude Code"}}}`},
		{"model and surface limit", `{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"},"surface":{"display_name":"Claude Code"}}}`},
		{"future scope dimension", `{"kind":"weekly_all","percent":100,"scope":{"feature":{"display_name":"Extended thinking"}}}`},
		{"model and future scope", `{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"},"feature":{"display_name":"Extended thinking"}}}`},
	} {
		cases = append(cases, claudeDegradedCompatibilityCase{
			limit.name, claudeCompatibilityBody(claudeCompatibilityWeekly, claudeCompatibilityFableLimit, limit.body), true, true,
		})
	}
	return cases
}

func assertClaudeDegradedCompatibility(t *testing.T, info *UsageInfo, tc claudeDegradedCompatibilityCase) {
	t.Helper()
	if info == nil || info.QuotaStatus != QuotaDegraded || info.Error == "" || info.QuotaNote == "" {
		t.Fatalf("missing general quota or unknown restriction was not degraded: %+v", info)
	}
	if tc.wantWeekly && (info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 25) {
		t.Fatalf("known weekly quota lost from degraded document: %+v", info.SecondaryWindow)
	}
	if info.PrimaryWindow != nil || (!tc.wantWeekly && info.SecondaryWindow != nil) {
		t.Fatalf("inapplicable or unknown restriction became a fabricated general window: %+v", info)
	}
	if tc.wantScoped {
		if w := info.WindowForModel("fable"); w == nil || w.UsedPercent != 100 {
			t.Fatalf("known model quota lost from degraded document: %+v", w)
		}
		if !info.IsNearLimitForModel(0.8, "fable") {
			t.Fatal("known exhausted model constraint vanished with general quota")
		}
	}
	assertClaudeQuotaNotRoutable(t, info)
}

func TestClaudeUsageCompatibilityKeepsUnknownRestrictionsConservative(t *testing.T) {
	for _, tc := range claudeDegradedCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := fetchClaudeFixtureResult(t, tc.body)
			if err == nil {
				t.Fatal("incomplete quota was reported as a complete response")
			}
			assertClaudeDegradedCompatibility(t, info, tc)
		})
	}
}

func TestClaudeUsageCompatibilityModelDurations(t *testing.T) {
	for _, tc := range []struct {
		name, kind, group string
		wantDuration      time.Duration
	}{
		{"known session", "session", "session", 5 * time.Hour},
		{"session group omitted", "session", "", 5 * time.Hour},
		{"known weekly", "weekly_scoped", "weekly", 7 * 24 * time.Hour},
		{"weekly group omitted", "weekly_scoped", "", 7 * 24 * time.Hour},
		{"future scoped kind", "future_scoped", "future", 0},
		{"future kind session group", "future_scoped", "session", 0},
		{"future kind weekly group", "future_scoped", "weekly", 0},
		{"session contradictory group", "session", "weekly", 0},
		{"weekly contradictory group", "weekly_scoped", "session", 0},
		{"known kind future group", "weekly_scoped", "future", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := ""
			if tc.group != "" {
				group = `,"group":"` + tc.group + `"`
			}
			body := claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"`+tc.kind+`"`+group+`,"percent":100,"resets_at":"2099-01-05T00:00:00Z","scope":{"model":{"display_name":"Fable"}}}`)
			cached, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(body), time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
			if err != nil {
				t.Fatalf("cached model quota failed: %v", err)
			}
			for _, result := range []struct {
				name string
				info *UsageInfo
			}{{"live", fetchFixture(t, body)}, {"cached", cached}} {
				t.Run(result.name, func(t *testing.T) {
					w := result.info.WindowForModel("fable")
					if w == nil || w.WindowDuration != tc.wantDuration || w.UsedPercent != 100 || w.Kind != tc.kind {
						t.Fatalf("model quota or its duration changed: %+v, duration want %v", w, tc.wantDuration)
					}
					if !result.info.NumericQuotaKnown() || !result.info.IsNearLimitForModel(0.8, "fable") || result.info.IsNearLimitForModel(0.8, "sonnet") {
						t.Fatal("model limit duration uncertainty changed quota eligibility")
					}
				})
			}
		})
	}
}

func TestClaudeUsageCompatibilityNullLegacyPremiumAlias(t *testing.T) {
	info := fetchFixture(t, claudeCompatibilityBody(`"opus":{"utilization":null},"seven_day_opus":{"utilization":100},`+claudeCompatibilityWeekly))
	if info.TertiaryWindow == nil || info.TertiaryWindow.UsedPercent != 100 || !info.IsNearLimitForModel(0.8, "opus") {
		t.Fatalf("null legacy alias hid the measured premium limit: %+v", info)
	}
}
