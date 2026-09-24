package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
