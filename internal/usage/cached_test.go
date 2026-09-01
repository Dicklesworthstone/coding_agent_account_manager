package usage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedNow is the reference "current time" for cached-usage tests. Windows that
// reset before it have rolled; windows that reset after it are still counting.
var fixedNow = time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)

const fullCacheJSON = `{
  "userID": "irrelevant",
  "cachedUsageUtilization": {
    "fetchedAtMs": 1788292800000,
    "accountUuid": "0552fa96-40f9-4b38-a33a-0d5ac585167d",
    "utilization": {
      "limits": [
        {"kind": "session",       "percent": 53, "resets_at": "2026-09-01T22:50:00.110099+00:00", "scope": null},
        {"kind": "weekly_all",    "percent": 11, "resets_at": "2026-09-04T06:00:00.110125+00:00", "scope": null},
        {"kind": "weekly_scoped", "percent": 19, "resets_at": "2026-09-04T06:00:00.110393+00:00",
         "scope": {"model": {"id": null, "display_name": "Fable"}, "surface": null}}
      ]
    }
  }
}`

func TestParseCachedUsageFullSnapshot(t *testing.T) {
	got, err := ParseCachedUsage([]byte(fullCacheJSON), fixedNow)
	if err != nil {
		t.Fatalf("ParseCachedUsage: %v", err)
	}
	if got.AccountUUID != "0552fa96-40f9-4b38-a33a-0d5ac585167d" {
		t.Errorf("AccountUUID = %q", got.AccountUUID)
	}
	wantFetched := time.UnixMilli(1788292800000)
	if !got.FetchedAt.Equal(wantFetched) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, wantFetched)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("len(Windows) = %d, want 3", len(got.Windows))
	}

	session := got.Window(CachedKindSession)
	if session == nil {
		t.Fatal("no session window")
	}
	if session.Percent != 53 || session.Rolled {
		t.Errorf("session = %+v, want 53%% and not rolled", session)
	}
	if session.Label != "5h" {
		t.Errorf("session label = %q, want 5h", session.Label)
	}
	if session.ResetsAt == nil || !session.ResetsAt.Equal(time.Date(2026, 9, 1, 22, 50, 0, 110099000, time.UTC)) {
		t.Errorf("session ResetsAt = %v", session.ResetsAt)
	}

	weekly := got.Window(CachedKindWeeklyAll)
	if weekly == nil || weekly.Percent != 11 || weekly.Label != "weekly" {
		t.Errorf("weekly = %+v, want 11%% labelled weekly", weekly)
	}
}

func TestParseCachedUsageWeeklyScopedLabelIsModelDisplayName(t *testing.T) {
	got, err := ParseCachedUsage([]byte(fullCacheJSON), fixedNow)
	if err != nil {
		t.Fatalf("ParseCachedUsage: %v", err)
	}
	scoped := got.Window(CachedKindWeeklyScoped)
	if scoped == nil {
		t.Fatal("no weekly_scoped window")
	}
	if scoped.Label != "Fable" {
		t.Errorf("scoped label = %q, want the model display name Fable", scoped.Label)
	}
	if scoped.Percent != 19 {
		t.Errorf("scoped percent = %d, want 19", scoped.Percent)
	}
	if got.ScopedLabel() != "Fable" {
		t.Errorf("ScopedLabel() = %q, want Fable", got.ScopedLabel())
	}
}

func TestParseCachedUsageRolledWindowReadsZero(t *testing.T) {
	// resets_at is before now: the window rolled, so nothing has been used since.
	const rolled = `{"cachedUsageUtilization": {"fetchedAtMs": 1788200000000, "accountUuid": "u",
	  "utilization": {"limits": [
	    {"kind": "session", "percent": 92, "resets_at": "2026-09-01T10:00:00+00:00", "scope": null}
	  ]}}}`

	got, err := ParseCachedUsage([]byte(rolled), fixedNow)
	if err != nil {
		t.Fatalf("ParseCachedUsage: %v", err)
	}
	session := got.Window(CachedKindSession)
	if session == nil {
		t.Fatal("no session window")
	}
	if session.Percent != 0 {
		t.Errorf("percent = %d, want 0 for a window whose reset has passed", session.Percent)
	}
	if !session.Rolled {
		t.Error("Rolled = false, want true for a window whose reset has passed")
	}
}

func TestParseCachedUsageNullResetsAtAndAbsentPercent(t *testing.T) {
	const partial = `{"cachedUsageUtilization": {"fetchedAtMs": 1788292800000, "accountUuid": "u",
	  "utilization": {"limits": [
	    {"kind": "session", "percent": 0, "resets_at": null, "scope": null},
	    {"kind": "weekly_all", "resets_at": null, "scope": null}
	  ]}}}`

	got, err := ParseCachedUsage([]byte(partial), fixedNow)
	if err != nil {
		t.Fatalf("ParseCachedUsage: %v", err)
	}
	session := got.Window(CachedKindSession)
	if session == nil || session.ResetsAt != nil {
		t.Errorf("session = %+v, want a nil ResetsAt", session)
	}
	if session.Rolled {
		t.Error("a window with no reset time must not be reported as rolled")
	}
	weekly := got.Window(CachedKindWeeklyAll)
	if weekly == nil || weekly.Percent != 0 {
		t.Errorf("weekly = %+v, want percent 0 when the field is absent", weekly)
	}
	if got.ScopedLabel() != "" {
		t.Errorf("ScopedLabel() = %q, want empty with no weekly_scoped window", got.ScopedLabel())
	}
}

func TestParseCachedUsageMissingKey(t *testing.T) {
	_, err := ParseCachedUsage([]byte(`{"userID": "x", "projects": {}}`), fixedNow)
	if !errors.Is(err, ErrNoCachedUsage) {
		t.Fatalf("err = %v, want ErrNoCachedUsage", err)
	}
}

func TestParseCachedUsageEmptyLimitsIsNoCache(t *testing.T) {
	const empty = `{"cachedUsageUtilization": {"fetchedAtMs": 1788292800000, "utilization": {"limits": []}}}`
	if _, err := ParseCachedUsage([]byte(empty), fixedNow); !errors.Is(err, ErrNoCachedUsage) {
		t.Fatalf("err = %v, want ErrNoCachedUsage for an empty limits array", err)
	}
}

func TestParseCachedUsageUnknownKindKeepsKindAsLabel(t *testing.T) {
	const odd = `{"cachedUsageUtilization": {"fetchedAtMs": 1, "utilization": {"limits": [
	  {"kind": "monthly_experiment", "percent": 7, "resets_at": null}]}}}`
	got, err := ParseCachedUsage([]byte(odd), fixedNow)
	if err != nil {
		t.Fatalf("ParseCachedUsage: %v", err)
	}
	w := got.Window("monthly_experiment")
	if w == nil || w.Label != "monthly_experiment" {
		t.Errorf("window = %+v, want the raw kind as its label", w)
	}
}

func TestParseCachedUsageInvalidJSON(t *testing.T) {
	if _, err := ParseCachedUsage([]byte(`{not json`), fixedNow); err == nil {
		t.Fatal("want an error for malformed JSON")
	} else if errors.Is(err, ErrNoCachedUsage) {
		t.Fatal("malformed JSON must not be reported as a missing cache")
	}
}

func TestReadCachedUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(fullCacheJSON), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ReadCachedUsage(path, fixedNow)
	if err != nil {
		t.Fatalf("ReadCachedUsage: %v", err)
	}
	if got.AccountUUID == "" || len(got.Windows) != 3 {
		t.Errorf("got = %+v, want the parsed snapshot", got)
	}
}

func TestReadCachedUsageMissingFile(t *testing.T) {
	_, err := ReadCachedUsage(filepath.Join(t.TempDir(), "absent.json"), fixedNow)
	if !errors.Is(err, ErrNoCachedUsage) {
		t.Fatalf("err = %v, want ErrNoCachedUsage for a missing file", err)
	}
}

func TestCachedUsageAge(t *testing.T) {
	c := &CachedUsage{FetchedAt: fixedNow.Add(-90 * time.Minute)}
	if got := c.Age(fixedNow); got != 90*time.Minute {
		t.Errorf("Age = %v, want 90m", got)
	}
	var missing CachedUsage
	if got := missing.Age(fixedNow); got != 0 {
		t.Errorf("Age with no fetch time = %v, want 0", got)
	}
}
