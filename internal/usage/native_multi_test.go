package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeBatchRejectsIncompleteQuotaInEverySelectionHelper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == cursorPeriodUsagePath && r.Header.Get("Authorization") == "Bearer SYNTHETIC-KNOWN" {
			fmt.Fprint(w, `{"planUsage":{"limit":100,"includedSpend":25,"apiPercentUsed":40}}`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	credentials := make(map[string]string)
	for name, token := range map[string]string{"known": "SYNTHETIC-KNOWN", "incomplete": "SYNTHETIC-INCOMPLETE"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(path, []byte(`{"accessToken":"`+token+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		credentials[name] = "cursor-root:" + path
	}
	fetcher := &MultiProfileFetcher{cursorFetcher: &CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}}
	ctx := context.Background()
	rows := fetcher.FetchAllProfiles(ctx, "cursor", credentials)
	if len(rows) != 2 {
		t.Fatalf("batch dropped a profile: %+v", rows)
	}
	for _, row := range rows {
		if row.Provider != "cursor" || row.Usage.ProfileName != row.ProfileName {
			t.Errorf("profile identity changed: %+v", row)
		}
		if row.ProfileName == "incomplete" && (row.Usage.QuotaStatus != QuotaDegraded || row.Usage.Error == "" || row.Usage.NumericQuotaKnown()) {
			t.Errorf("incomplete row could appear healthy to a legacy consumer: %+v", row.Usage)
		}
	}
	encoded, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(encoded), "SYNTHETIC-") {
		t.Fatalf("quota JSON serialization failed or exposed credentials: %s, %v", encoded, err)
	}
	if best := fetcher.GetBestProfile(ctx, "cursor", credentials); best == nil || best.ProfileName != "known" {
		t.Fatalf("best profile = %+v", best)
	}
	if available := fetcher.GetProfilesAboveThreshold(ctx, "cursor", credentials, 0.8); len(available) != 1 || available[0].ProfileName != "known" {
		t.Fatalf("threshold selection included unknown capacity: %+v", available)
	}
	onlyUnknown := map[string]string{"incomplete": credentials["incomplete"]}
	if best := fetcher.GetBestProfile(ctx, "cursor", onlyUnknown); best != nil {
		t.Fatalf("only incomplete profile selected: %+v", best)
	}
}

func TestNativeBatchUnavailableFetchersStayUnavailable(t *testing.T) {
	fetcher := &MultiProfileFetcher{}
	for _, provider := range []string{"grok", "cursor"} {
		credentials := map[string]string{"one": "unused", "two": "unused"}
		rows := fetcher.FetchAllProfiles(context.Background(), provider, credentials)
		if len(rows) != 2 {
			t.Fatalf("%s lost error rows: %+v", provider, rows)
		}
		for _, row := range rows {
			if row.Usage == nil || row.Usage.Provider != provider || row.Usage.QuotaStatus != QuotaUnavailable || row.Usage.NumericQuotaKnown() {
				t.Errorf("%s unavailable row = %+v", provider, row)
			}
		}
		if best := fetcher.GetBestProfile(context.Background(), provider, credentials); best != nil {
			t.Errorf("%s selected a failed fetch: %+v", provider, best)
		}
	}
}

// Preserve all failures for diagnostics, but never offer one to a caller
// asking which account should receive new work, regardless of provider.
func TestBatchSelectionRejectsFailedFetchesForEveryProvider(t *testing.T) {
	fetcher := &MultiProfileFetcher{}
	for _, provider := range []string{"claude", "codex", "grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			credentials := map[string]string{"one": "unused", "two": "unused"}
			rows := fetcher.FetchAllProfiles(context.Background(), provider, credentials)
			if len(rows) != 2 {
				t.Fatal("diagnostic rows were dropped")
			}
			for _, row := range rows {
				if row.Usage == nil || row.Usage.Error == "" {
					t.Fatal("unavailable fetcher did not report a failure")
				}
			}
			if best := fetcher.GetBestProfile(context.Background(), provider, credentials); best != nil {
				t.Fatal("best-profile selection recommended a failed fetch")
			}
			if available := fetcher.GetProfilesAboveThreshold(context.Background(), provider, credentials, 0.8); len(available) != 0 {
				t.Fatal("failed fetch was interpreted as spare capacity")
			}
		})
	}
}

func TestProfilesBelowThresholdRetainsOnlyUsableCapacity(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			row := func(name string, used float64) ProfileUsage {
				return ProfileUsage{Provider: provider, ProfileName: name, Usage: &UsageInfo{
					Provider: provider, QuotaStatus: QuotaOK,
					PrimaryWindow: &UsageWindow{Utilization: used, UsedPercent: int(used * 100)},
				}}
			}
			failed := row("failed", 0)
			failed.Usage.Error = "request failed"
			degraded := row("degraded", 0)
			degraded.Usage.QuotaStatus = QuotaDegraded
			unknown := row("unknown", 0)
			unknown.Usage.PrimaryWindow.Unmeasured = true
			secondary := row("secondary-exhausted", 0)
			secondary.Usage.SecondaryWindow = &UsageWindow{UsedPercent: 100}
			tertiary := row("tertiary-exhausted", 0)
			tertiary.Usage.TertiaryWindow = &UsageWindow{UsedPercent: 100}
			scoped := row("scoped-exhausted", 0)
			scoped.Usage.ModelWindows = map[string]*UsageWindow{"opus": {UsedPercent: 100}}
			blocked := row("blocked", 0)
			blocked.Usage.LimitStage = "HARD_BLOCK"
			rows := []ProfileUsage{
				failed, {Provider: provider, ProfileName: "nil"}, degraded, unknown, blocked,
				row("zero", 0), row("some", 0.25), row("boundary", 0.8), row("primary-exhausted", 1),
				secondary, tertiary, scoped,
			}
			before, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				threshold float64
				want      []string
			}{
				{0.8, []string{"zero", "some"}},
				{1, []string{"zero", "some", "boundary"}},
				{0.25, []string{"zero"}},
			} {
				var names []string
				for _, result := range profilesBelowThreshold(rows, tc.threshold) {
					names = append(names, result.ProfileName)
				}
				if !reflect.DeepEqual(names, tc.want) {
					t.Fatalf("threshold=%v got=%v want=%v", tc.threshold, names, tc.want)
				}
			}
			after, err := json.Marshal(rows)
			if err != nil || string(before) != string(after) {
				t.Fatal("selection changed diagnostic rows or their ordering")
			}
			if got := profilesBelowThreshold(rows[:5], 1); len(got) != 0 {
				t.Fatal("all-failed pool supplied a candidate")
			}
			if got := profilesBelowThreshold(nil, 1); len(got) != 0 {
				t.Fatal("empty pool supplied a candidate")
			}
		})
	}
}

// Claude and Codex historically omit QuotaStatus. Do not require a new
// wire format to fix error handling; keep their measured windows usable.
func TestProfilesBelowThresholdPreservesLegacyQuotaStatus(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		usable := ProfileUsage{Provider: provider, ProfileName: "usable", Usage: &UsageInfo{
			Provider: provider, PrimaryWindow: &UsageWindow{UsedPercent: 25},
		}}
		failed := ProfileUsage{Provider: provider, ProfileName: "failed", Usage: &UsageInfo{Provider: provider, Error: "request failed"}}
		spent := ProfileUsage{Provider: provider, ProfileName: "spent", Usage: &UsageInfo{
			Provider: provider, PrimaryWindow: &UsageWindow{UsedPercent: 100},
		}}
		got := profilesBelowThreshold([]ProfileUsage{failed, spent, usable}, 1)
		if len(got) != 1 || got[0].ProfileName != "usable" {
			t.Fatalf("%s selected failed/exhausted legacy quota or rejected valid quota", provider)
		}
	}
}
