package usage

import (
	"os"
	"path/filepath"
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
	if err != nil {
		t.Fatalf("ParseCachedClaudeUsage: %v", err)
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
