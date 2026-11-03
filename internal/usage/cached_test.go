package usage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #88 / PR #88: `caam limits --cached` reads the usage snapshot Claude
// Code leaves in each account's own .claude.json, so the question "which
// account still has headroom" can be answered offline. The whole value of the
// feature depends on it being honest about staleness, so these tests pin the
// two ways it could lie: inventing a 0% for an account with no snapshot, and
// carrying a stale percentage past the window it describes.

const cachedFixture = `{
  "userID": "anon",
  "cachedUsageUtilization": {
    "fetchedAtMs": 1756900000000,
    "accountUuid": "11111111-2222-3333-4444-555555555555",
    "utilization": {
      "limits": [
        {"kind": "session", "group": "session", "percent": 53, "severity": "normal",
         "resets_at": "2099-01-01T08:00:00Z", "is_active": true},
        {"kind": "weekly_all", "group": "weekly", "percent": 11, "severity": "normal",
         "resets_at": "2099-01-05T00:00:00Z"},
        {"kind": "weekly_scoped", "group": "weekly", "percent": 19, "severity": "normal",
         "resets_at": "2099-01-05T00:00:00Z",
         "scope": {"model": {"id": "claude-fable-5-1", "display_name": "Fable"}}}
      ]
    }
  }
}`

func TestParseCachedClaudeUsage(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	info, err := ParseCachedClaudeUsage([]byte(cachedFixture), now)
	if err != nil {
		t.Fatalf("ParseCachedClaudeUsage: %v", err)
	}

	if info.Source != SourceCache {
		t.Errorf("Source = %q, want %q", info.Source, SourceCache)
	}
	if info.AccountID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("AccountID = %q", info.AccountID)
	}
	if want := time.UnixMilli(1756900000000); !info.FetchedAt.Equal(want) {
		t.Errorf("FetchedAt = %v, want the snapshot's own timestamp %v", info.FetchedAt, want)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 53 {
		t.Errorf("PrimaryWindow = %+v, want 53%%", info.PrimaryWindow)
	}
	if info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 11 {
		t.Errorf("SecondaryWindow = %+v, want 11%%", info.SecondaryWindow)
	}
	// The per-model allowance must land where the live fetcher puts it, so
	// --model and the SCOPED column work identically offline.
	scoped := info.ScopedLimit("fable")
	if scoped == nil || scoped.UsedPercent != 19 || scoped.Label != "Fable" {
		t.Errorf("ScopedLimit(fable) = %+v, want Fable at 19%%", scoped)
	}

	// The snapshot's age is the caveat, so it must be reported, not implied.
	age, ok := info.CacheAge(now)
	if !ok {
		t.Fatal("CacheAge reported unknown for a snapshot that carries a timestamp")
	}
	if want := now.Sub(time.UnixMilli(1756900000000)); age != want {
		t.Errorf("CacheAge = %v, want %v", age, want)
	}
}

// TestParseCachedClaudeUsageMarksRolledWindows: a window whose reset time has
// passed emptied at that moment, so the recorded percentage is no longer true.
// It reads 0 — but flagged, so a caller can say so rather than presenting it
// as a measurement.
func TestParseCachedClaudeUsageMarksRolledWindows(t *testing.T) {
	body := `{"cachedUsageUtilization":{"fetchedAtMs":1756900000000,"accountUuid":"u",
	  "utilization":{"limits":[
	    {"kind":"session","group":"session","percent":88,"resets_at":"2020-01-01T00:00:00Z"},
	    {"kind":"weekly_all","group":"weekly","percent":40,"resets_at":"2099-01-01T00:00:00Z"}]}}}`
	info, err := ParseCachedClaudeUsage([]byte(body), time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, errCachedQuotaRolled) || info == nil {
		t.Fatalf("rolled cache should retain diagnostics and require refresh: info=%+v err=%v", info, err)
	}
	if info.PrimaryWindow == nil || !info.PrimaryWindow.Rolled {
		t.Fatalf("PrimaryWindow = %+v, want Rolled", info.PrimaryWindow)
	}
	if info.PrimaryWindow.UsedPercent != 0 || info.PrimaryWindow.Utilization != 0 {
		t.Errorf("rolled window still reports %d%%, want 0", info.PrimaryWindow.UsedPercent)
	}
	if info.SecondaryWindow == nil || info.SecondaryWindow.Rolled {
		t.Errorf("SecondaryWindow = %+v, want not rolled", info.SecondaryWindow)
	}
	if info.SecondaryWindow.UsedPercent != 40 {
		t.Errorf("live window = %d%%, want 40", info.SecondaryWindow.UsedPercent)
	}
	assertClaudeQuotaNotRoutable(t, info)
}

// TestParseCachedClaudeUsageNoSnapshot: absent, empty and legacy files are all
// "no cached data", never 0% used. This is the whole difference between a
// useful offline view and one that recommends switching to an account it knows
// nothing about.
func TestParseCachedClaudeUsageNoSnapshot(t *testing.T) {
	now := time.Now()
	for name, body := range map[string]string{
		"no key":         `{"userID":"anon","theme":"dark"}`,
		"null key":       `{"cachedUsageUtilization":null}`,
		"empty limits":   `{"cachedUsageUtilization":{"fetchedAtMs":1,"utilization":{"limits":[]}}}`,
		"no utilization": `{"cachedUsageUtilization":{"fetchedAtMs":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage([]byte(body), now)
			if err == nil {
				t.Fatalf("want ErrNoCachedUsage, got %+v", info)
			}
			if err.Error() != ErrNoCachedUsage.Error() {
				t.Fatalf("err = %v, want %v", err, ErrNoCachedUsage)
			}
		})
	}

	if _, err := ParseCachedClaudeUsage([]byte(`not json`), now); err == nil {
		t.Error("malformed JSON should be an error, not silently empty")
	}
}

func TestReadCachedClaudeUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(cachedFixture), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadCachedClaudeUsage(path, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ReadCachedClaudeUsage: %v", err)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 53 {
		t.Errorf("PrimaryWindow = %+v", info.PrimaryWindow)
	}

	// A profile that has never been used has no file at all; that is the
	// common case, and it must not read as an error the caller shows as a
	// failure or as an idle account.
	if _, err := ReadCachedClaudeUsage(filepath.Join(dir, "absent.json"), time.Now()); err == nil {
		t.Error("missing file should report ErrNoCachedUsage")
	} else if err.Error() == "" {
		t.Error("empty error text")
	}
}

// TestCacheAgeUnknownWithoutTimestamp: a snapshot with no fetchedAtMs has an
// unknown age; reporting "0s ago" would be a fabrication.
func TestCacheAgeUnknownWithoutTimestamp(t *testing.T) {
	body := `{"cachedUsageUtilization":{"accountUuid":"u","utilization":{"limits":[
	  {"kind":"session","group":"session","percent":5,"resets_at":"2099-01-01T00:00:00Z"}]}}}`
	info, err := ParseCachedClaudeUsage([]byte(body), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !info.FetchedAt.IsZero() {
		t.Errorf("FetchedAt = %v, want zero", info.FetchedAt)
	}
	if _, ok := info.CacheAge(time.Now()); ok {
		t.Error("CacheAge reported a known age for a snapshot with no timestamp")
	}

	// A live API row is never "cached", however old it is.
	live := &UsageInfo{Source: SourceAPI, FetchedAt: time.Now().Add(-time.Hour)}
	if _, ok := live.CacheAge(time.Now()); ok {
		t.Error("CacheAge reported an age for a live row")
	}
}

func cachedClaudeCompatibilityDocument(body string) []byte {
	return []byte(`{"cachedUsageUtilization":{"fetchedAtMs":1792580400000,"accountUuid":"synthetic-account","utilization":` + body + `}}`)
}

func assertCachedClaudeCompatibilityMetadata(t *testing.T, info *UsageInfo) {
	t.Helper()
	if info == nil || info.Source != SourceCache || info.AccountID != "synthetic-account" || !info.FetchedAt.Equal(time.UnixMilli(1792580400000)) {
		t.Fatalf("cache identity, source, or original timestamp lost: %+v", info)
	}
}

func TestCachedClaudeUsageCompatibilityOptionalSession(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range claudeSessionCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if err != nil {
				t.Fatalf("optional cached session discarded usable quota: %v", err)
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeSessionCompatibility(t, info, tc)
		})
	}
}

func TestCachedClaudeUsageCompatibilityWeeklyOnly(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range claudeWeeklyOnlyCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if err != nil {
				t.Fatalf("weekly-only cached quota failed: %v", err)
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeWeeklyOnlyCompatibility(t, info, tc.wantPercent)
		})
	}
}

func TestCachedClaudeUsageCompatibilityRejectsMalformedMeasurements(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range claudeMalformedCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if err == nil || info != nil {
				t.Fatalf("malformed cached measurement became capacity: info=%+v err=%v", info, err)
			}
		})
	}
}

func TestCachedClaudeUsageCompatibilityKeepsUnknownRestrictionsConservative(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range claudeDegradedCompatibilityCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if err == nil {
				t.Fatal("incomplete cached quota was reported as a complete response")
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeDegradedCompatibility(t, info, tc)
		})
	}
}

func TestCachedClaudeUsageCompatibilityWeeklyOnlyRollover(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []claudeDocumentCase{
		{"legacy weekly window", `{"five_hour":{"utilization":null},"seven_day":{"utilization":100,"resets_at":"2026-11-03T11:00:00Z"}}`},
		{"limits weekly window", `{"limits":[{"kind":"session","percent":null},{"kind":"weekly_all","percent":100,"resets_at":"2026-11-03T11:00:00Z"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if !errors.Is(err, errCachedQuotaRolled) || info == nil {
				t.Fatalf("rolled cache should retain diagnostics and require refresh: info=%+v err=%v", info, err)
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeQuotaNotRoutable(t, info)
			if info.PrimaryWindow != nil || info.SecondaryWindow == nil || !info.SecondaryWindow.Rolled || info.SecondaryWindow.UsedPercent != 0 || info.SecondaryWindow.Utilization != 0 {
				t.Fatalf("optional session changed rollover provenance: %+v", info)
			}
			for _, mode := range []string{RankAvailability, RankEarliestResetHeadroom} {
				result := RankProfiles([]ProfileUsage{{Provider: "claude", ProfileName: "rolled-weekly", Usage: info}}, RankOptions{Mode: mode, Now: now})
				if result.Selected != nil || result.Profiles[0].Eligible {
					t.Fatalf("%s selected stale weekly quota as an idle account: %+v", mode, result)
				}
			}
		})
	}
}

func TestCachedClaudeUsageCompatibilityRolledModelDoesNotHideLiveCap(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	expired := `{"kind":"session","group":"session","percent":100,"resets_at":"2026-11-03T11:00:00Z","scope":{"model":{"display_name":"Fable"}}}`
	weekly := `{"kind":"weekly_scoped","group":"weekly","percent":80,"resets_at":"2099-01-05T00:00:00Z","scope":{"model":{"display_name":"Fable"}}}`
	for _, tc := range []struct {
		name   string
		limits []string
	}{
		{"expired first", []string{expired, weekly}},
		{"expired last", []string{weekly, expired}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := claudeCompatibilityBody(claudeCompatibilityWeekly, tc.limits...)
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(body), now)
			if !errors.Is(err, errCachedQuotaRolled) || info == nil {
				t.Fatalf("discarded rolled model constraint must require refresh: info=%+v err=%v", info, err)
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeQuotaNotRoutable(t, info)
			w := info.WindowForModel("fable")
			if w == nil || w.UsedPercent != 80 || w.Utilization != 0.8 || w.Rolled || w.WindowDuration != 7*24*time.Hour || !w.ResetsAt.After(now) {
				t.Fatalf("expired model session hid the surviving weekly cap: %+v", w)
			}
			if !info.IsNearLimitForModel(0.8, "fable") || info.IsNearLimitForModel(0.81, "fable") || info.IsNearLimitForModel(0.8, "sonnet") {
				t.Fatal("cached weekly model cap changed threshold eligibility")
			}
			for _, ceiling := range []int{80, 95, 100} {
				for _, mode := range []string{RankAvailability, RankEarliestResetHeadroom} {
					result := RankProfiles([]ProfileUsage{{Provider: "claude", ProfileName: "cached-model", Usage: info}}, RankOptions{
						Mode: mode, Model: "fable", HeadroomCeiling: ceiling, Now: now,
					})
					if result.Selected != nil {
						t.Fatalf("%s selected unmeasured model capacity after session rollover at ceiling %d: %+v", mode, ceiling, result)
					}
				}
			}

			// Only disk snapshots are rolled forward. A fresh HTTP response's
			// measured restriction must remain authoritative even if its reset
			// timestamp is already past the consumer's clock.
			live := fetchFixture(t, body)
			w = live.WindowForModel("fable")
			if w == nil || w.UsedPercent != 100 || w.Utilization != 1 || w.Rolled || !live.IsNearLimitForModel(1, "fable") {
				t.Fatalf("live response was prematurely rolled forward: %+v", w)
			}
		})
	}
}

func TestCachedClaudeUsageRolledConstraintsRequireRefresh(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []claudeDocumentCase{
		{"session with live weekly", claudeCompatibilityBody(`"five_hour":{"utilization":100,"resets_at":"2026-11-03T11:00:00Z"},` + claudeCompatibilityWeekly)},
		{"zero session at reset boundary", claudeCompatibilityBody(`"five_hour":{"utilization":0,"resets_at":"2026-11-03T12:00:00Z"},` + claudeCompatibilityWeekly)},
		{"session limit with live weekly", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"session","percent":100,"resets_at":"2026-11-03T11:00:00Z"}`)},
		{"weekly with live session", `{"five_hour":{"utilization":0,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":100,"resets_at":"2026-11-03T11:00:00Z"}}`},
		{"legacy premium with live weekly", claudeCompatibilityBody(`"opus":{"utilization":100,"resets_at":"2026-11-03T11:00:00Z"},` + claudeCompatibilityWeekly)},
		{"premium alias with live weekly", claudeCompatibilityBody(`"seven_day_opus":{"utilization":100,"resets_at":"2026-11-03T11:00:00Z"},` + claudeCompatibilityWeekly)},
		{"model with live weekly", claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"weekly_scoped","percent":100,"resets_at":"2026-11-03T11:00:00Z","scope":{"model":{"display_name":"Fable"}}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if !errors.Is(err, errCachedQuotaRolled) || info == nil || info.QuotaStatus != QuotaDegraded || info.Error == "" || info.QuotaNote == "" {
				t.Fatalf("expired quota was treated as a fresh measurement: info=%+v err=%v", info, err)
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeQuotaNotRoutable(t, info)
			var rolled bool
			windows := []*UsageWindow{info.PrimaryWindow, info.SecondaryWindow, info.TertiaryWindow}
			for _, w := range info.ModelWindows {
				windows = append(windows, w)
			}
			for _, w := range windows {
				if w != nil && w.Rolled {
					rolled = true
					if w.UsedPercent != 0 || w.Utilization != 0 || w.ResetsAt.IsZero() {
						t.Fatalf("diagnostic rolled window changed its display representation: %+v", w)
					}
				}
			}
			if !rolled {
				t.Fatal("expired diagnostic window disappeared")
			}
		})
	}

	// A new provider read can legitimately remove the session window entirely.
	// Cache degradation must not be sticky or turn its absence into a required
	// restriction after the provider supplies fresh weekly-only quota.
	freshBody := `{"five_hour":null,` + claudeCompatibilityWeekly + `}`
	live := fetchFixture(t, freshBody)
	if live.Source != SourceAPI || live.Error != "" || live.QuotaNote != "" {
		t.Fatalf("new provider read retained stale cache diagnostics: %+v", live)
	}
	assertClaudeWeeklyOnlyCompatibility(t, live, 25)

	// A null measurement is explicitly inapplicable, even if the provider
	// retains an old reset timestamp alongside it. It must not trigger this
	// stale-measurement guard.
	for _, body := range []string{
		claudeCompatibilityBody(`"five_hour":{"utilization":null,"resets_at":"2026-11-03T11:00:00Z"},` + claudeCompatibilityWeekly),
		claudeCompatibilityBody(claudeCompatibilityWeekly, `{"kind":"session","percent":null,"resets_at":"2026-11-03T11:00:00Z"}`),
	} {
		info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(body), now)
		if err != nil {
			t.Fatalf("inapplicable session was mistaken for a stale measurement: %v", err)
		}
		assertClaudeWeeklyOnlyCompatibility(t, info, 25)
	}
}

func TestCachedClaudeUsageGeneralDuplicateReconciliation(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range claudeGeneralReconciliationCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			assertClaudeGeneralReconciliation(t, info, err, tc)
			assertCachedClaudeCompatibilityMetadata(t, info)
		})
	}
}

func TestCachedClaudeUsageRetainsAllLegacyPremiumConstraints(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range claudeLegacyPremiumCases() {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(tc.body), now)
			if err != nil {
				t.Fatalf("valid legacy constraints rejected: %v", err)
			}
			assertClaudeLegacyPremiumConstraints(t, info, tc)
			assertCachedClaudeCompatibilityMetadata(t, info)
		})
	}
}

func TestCachedClaudeUsageCannotHideRolledLegacyPremiumConstraint(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, expiredField := range []string{"opus", "seven_day_opus"} {
		t.Run(expiredField, func(t *testing.T) {
			currentField := "seven_day_opus"
			if expiredField == currentField {
				currentField = "opus"
			}
			body := claudeCompatibilityBody(claudeCompatibilityWeekly + `,"` + currentField + `":{"utilization":0,"resets_at":"2099-01-05T00:00:00Z"},"` + expiredField + `":{"utilization":100,"resets_at":"2026-11-03T11:00:00Z"}`)
			// A current provider response remains measured even when its reset
			// is in the past; only stale cached observations are projected.
			live := fetchFixture(t, body)
			if len(live.ScopedWindows) != 2 || !live.IsNearLimitForModel(1, "opus") || live.IsNearLimitForModel(1, "sonnet") {
				t.Fatalf("live legacy constraint disappeared: %+v", live)
			}
			info, err := ParseCachedClaudeUsage(cachedClaudeCompatibilityDocument(body), now)
			if err == nil || info == nil || info.QuotaStatus != QuotaDegraded || !strings.Contains(info.QuotaNote, "reset") || len(info.ScopedWindows) != 2 {
				t.Fatalf("expired legacy constraint became cached capacity: info=%+v err=%v", info, err)
			}
			assertCachedClaudeCompatibilityMetadata(t, info)
			assertClaudeQuotaNotRoutable(t, info)
			rolled := 0
			for _, w := range info.ScopedWindows {
				if w.Rolled {
					rolled++
					if w.Utilization != 0 || w.UsedPercent != 0 || !w.ResetsAt.Before(now) {
						t.Fatalf("legacy rollover diagnostics changed: %+v", w)
					}
				}
			}
			if rolled != 1 {
				t.Fatalf("retained %d rolled legacy constraints, want 1", rolled)
			}
		})
	}
}

func TestCachedClaudeUsageRejectsContradictedAccount(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, cached, account, wantID string
		mismatch                      bool
	}{
		{"same account", `"accountUuid":"seat-a",`, `"oauthAccount":{"accountUuid":"seat-a"},`, "seat-a", false},
		{"different account", `"accountUuid":"seat-a",`, `"oauthAccount":{"accountUuid":"seat-b"},`, "seat-a", true},
		{"trimmed identities", `"accountUuid":" seat-a ",`, `"oauthAccount":{"accountUuid":"seat-a"},`, " seat-a ", false},
		{"no account object", `"accountUuid":"seat-a",`, "", "seat-a", false},
		{"null account object", `"accountUuid":"seat-a",`, `"oauthAccount":null,`, "seat-a", false},
		{"legacy account label", `"accountUuid":"seat-a",`, `"oauthAccount":"old@example.test",`, "seat-a", false},
		{"missing current ID", `"accountUuid":"seat-a",`, `"oauthAccount":{},`, "seat-a", false},
		{"null current ID", `"accountUuid":"seat-a",`, `"oauthAccount":{"accountUuid":null},`, "seat-a", false},
		{"missing cached ID", "", `"oauthAccount":{"accountUuid":"seat-b"},`, "", false},
		{"null cached ID", `"accountUuid":null,`, `"oauthAccount":{"accountUuid":"seat-b"},`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{` + tc.account + `"cachedUsageUtilization":{"fetchedAtMs":1792580400000,` + tc.cached + `"utilization":{"five_hour":{"utilization":null},"seven_day":{"utilization":0,"resets_at":"2099-01-05T00:00:00Z"},"limits":[{"kind":"weekly_scoped","percent":80,"scope":{"model":{"display_name":"Fable"}}}]}}}`
			info, err := ParseCachedClaudeUsage([]byte(body), now)
			if info == nil || info.AccountID != tc.wantID || info.Source != SourceCache || !info.FetchedAt.Equal(time.UnixMilli(1792580400000)) || info.PrimaryWindow != nil || info.SecondaryWindow == nil || info.SecondaryWindow.UsedPercent != 0 {
				t.Fatalf("cache identity, age, or weekly-only evidence changed: %+v %v", info, err)
			}
			if scoped := info.ScopedLimit("fable"); scoped == nil || scoped.UsedPercent != 80 {
				t.Fatalf("account disagreement erased the model measurement: %+v", scoped)
			}
			if tc.mismatch {
				if !errors.Is(err, errCachedAccountMismatch) || info.QuotaStatus != QuotaDegraded || info.Error == "" || info.QuotaNote == "" {
					t.Fatalf("contradicted cached account remained eligible: %+v %v", info, err)
				}
				assertClaudeQuotaNotRoutable(t, info)
			} else if err != nil || !info.NumericQuotaKnown() {
				t.Fatalf("compatible legacy account metadata rejected: %+v %v", info, err)
			}
		})
	}
}
