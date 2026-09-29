package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Field names and types follow aiserver.v1.GetCurrentPeriodUsageResponse as
// shipped in cursor-agent 2026.09.28 (proto JSON: int64 as strings, zero
// values omitted).
const cursorTeamPeriodBody = `{
	"billingCycleStart":"1788659081000",
	"billingCycleEnd":"1791251081000",
	"planUsage":{"totalSpend":2000,"includedSpend":2000,"limit":2000,"apiPercentUsed":42.35},
	"spendLimitUsage":{"totalSpend":91272,"pooledLimit":"100000","pooledUsed":91272,"pooledRemaining":"8728","individualUsed":150,"limitType":"team"}
}`

func TestCursorFetchReportsTeamOnDemandPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == cursorPeriodUsagePath {
			fmt.Fprint(w, cursorTeamPeriodBody)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"accessToken":"SYNTHETIC"}`), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := (&CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}).Fetch(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	// The included-usage measurement is unchanged by the billing metadata.
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 100 || info.QuotaStatus != QuotaOK {
		t.Fatalf("included usage changed: %+v", info.PrimaryWindow)
	}
	b := info.Billing
	if b == nil {
		t.Fatal("billing metadata missing")
	}
	check := func(name string, got *int64, want int64) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %d", name, got, want)
		}
	}
	check("team pool cap", b.TeamPoolCapCents, 100000)
	check("team pool used", b.TeamPoolUsedCents, 91272)
	check("team pool remaining", b.TeamPoolRemainingCents, 8728)
	check("individual on-demand used", b.OnDemandUsedCents, 150)
	if b.OnDemandCapCents != nil {
		t.Errorf("absent individual limit reported as %d", *b.OnDemandCapCents)
	}
	if b.LimitType != "team" {
		t.Errorf("limit type = %q", b.LimitType)
	}
	if b.PeriodStart != "2026-09-06T01:44:41Z" || b.PeriodEnd != "2026-10-06T01:44:41Z" {
		t.Errorf("period = %q .. %q", b.PeriodStart, b.PeriodEnd)
	}
}

func TestParseCursorSpendLimitAbsentOrMalformed(t *testing.T) {
	for _, body := range []string{
		`not-json`,
		`{}`,
		`{"planUsage":{"limit":100,"includedSpend":5}}`,
		`{"spendLimitUsage":"nope"}`,
		`{"spendLimitUsage":{"pooledLimit":"-5","individualUsed":"x"}}`,
	} {
		if b := parseCursorSpendLimit([]byte(body)); b != nil {
			t.Errorf("%s: want nil billing, got %+v", body, b)
		}
	}
}
