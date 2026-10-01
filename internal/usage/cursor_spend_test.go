package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// GH #112: once a seat has spent its included dollars, includedSpend sits at
// limit while Cursor keeps serving its own models from bonus credit. The
// "cursor models" window must be autoPercentUsed, the "Auto" figure
// cursor-agent's /usage prints, not includedSpend / limit.
func TestCursorPeriodUsesAutoPercentUsed(t *testing.T) {
	raw := []byte(`{"billingCycleStart":"1759000000000","billingCycleEnd":"1761600000000","planUsage":{
		"totalSpend":44997,"includedSpend":2000,"bonusSpend":42997,"limit":2000,"remainingBonus":false,
		"autoPercentUsed":34.787619047619046,"apiPercentUsed":42.35,"totalPercentUsed":35.9976}}`)
	info := parseCursorPeriod(raw, time.Now())
	if !info.NumericQuotaKnown() || info.PrimaryWindow == nil || info.SecondaryWindow == nil {
		t.Fatalf("expected two measured windows, got %+v", info)
	}
	if got := info.PrimaryWindow.UsedPercent; got != 35 {
		t.Errorf("cursor models UsedPercent = %d, want 35", got)
	}
	if got := info.PrimaryWindow.Utilization; got < 0.3478 || got > 0.3479 {
		t.Errorf("cursor models Utilization = %v, want ~0.3479", got)
	}
	if info.PrimaryWindow.Label != "cursor models" || info.PrimaryWindow.WindowDuration <= 0 {
		t.Errorf("primary window metadata = %+v", info.PrimaryWindow)
	}
	if got := info.SecondaryWindow.UsedPercent; got != 42 {
		t.Errorf("other models UsedPercent = %d, want 42", got)
	}
	if score := info.AvailabilityScore(); score <= 0 {
		t.Errorf("seat with Auto at 35%% scored %d; it must stay routable", score)
	}

	// snake_case spelling and a string-encoded number are the same figure.
	info = parseCursorPeriod([]byte(`{"plan_usage":{"included_spend":2000,"limit":2000,"auto_percent_used":"89.7"}}`), time.Now())
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 90 {
		t.Errorf("snake_case auto_percent_used: %+v", info.PrimaryWindow)
	}
}

func TestCursorPeriodFallsBackToSpendWithoutAutoPercent(t *testing.T) {
	info := parseCursorPeriod([]byte(`{"planUsage":{"includedSpend":500,"limit":2000}}`), time.Now())
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 25 || !info.NumericQuotaKnown() {
		t.Fatalf("spend/limit fallback: %+v", info.PrimaryWindow)
	}
}

func TestCursorPeriodInvalidAutoPercentIsUnmeasured(t *testing.T) {
	for _, v := range []string{`101`, `-1`, `"NaN"`, `"broken"`, `null`, `{}`} {
		raw := []byte(`{"billingCycleEnd":"1761600000000","planUsage":{"includedSpend":500,"limit":2000,"autoPercentUsed":` + v + `,"apiPercentUsed":10}}`)
		info := parseCursorPeriod(raw, time.Now())
		if info.NumericQuotaKnown() || info.AvailabilityScore() != 0 {
			t.Errorf("autoPercentUsed=%s became capacity: %+v", v, info)
		}
		if info.PrimaryWindow == nil || !info.PrimaryWindow.Unmeasured || info.PrimaryWindow.ResetsAt.IsZero() {
			t.Errorf("autoPercentUsed=%s: want an unmeasured window that keeps the reset, got %+v", v, info.PrimaryWindow)
		}
		if info.QuotaNote == "" {
			t.Errorf("autoPercentUsed=%s: missing quota note", v)
		}
	}
}
