package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeQuotaRejectsMalformedNumbers(t *testing.T) {
	for _, raw := range []string{`null`, `"null"`, `"NaN"`, `"Inf"`, `"-Inf"`, `"12 trailing"`, `"12junk"`, `"0x1p2"`, `"1_0"`, `1e999`, `{}`, `true`} {
		t.Run("percent/"+raw, func(t *testing.T) {
			if n, ok := jsonFloat(json.RawMessage(raw)); ok {
				t.Fatalf("accepted malformed percentage %s as %v", raw, n)
			}
			info := parseGrokBilling([]byte(`{"config":{"creditUsagePercent":`+raw+`}}`), time.Now())
			if info.NumericQuotaKnown() {
				t.Fatalf("malformed percentage became quota: %+v", info)
			}
		})
	}
	for _, raw := range []string{`-0.1`, `0.9`, `"1.5"`, `"12 trailing"`, `"12junk"`, `9223372036854775808`, `-9223372036854775809`, `1e100`, `null`, `true`} {
		t.Run("cents/"+raw, func(t *testing.T) {
			if n, ok := jsonInt(json.RawMessage(raw)); ok {
				t.Fatalf("accepted malformed integer %s as %d", raw, n)
			}
			if n, ok := parseCent(json.RawMessage(raw)); ok {
				t.Fatalf("accepted malformed cents %s as %d", raw, n)
			}
			if n, ok := parseCent(json.RawMessage(`{"val":` + raw + `}`)); ok {
				t.Fatalf("accepted malformed Cent.val %s as %d", raw, n)
			}
		})
	}
	for _, raw := range []string{`0`, `"0"`, `100`, `"100"`, `9223372036854775807`, `"9223372036854775807"`} {
		if _, ok := jsonInt(json.RawMessage(raw)); !ok {
			t.Errorf("rejected valid integer %s", raw)
		}
		if _, ok := parseCent(json.RawMessage(raw)); !ok {
			t.Errorf("rejected valid cents %s", raw)
		}
	}
	if n, ok := parseCent(json.RawMessage(`{}`)); !ok || n != 0 {
		t.Fatalf("proto zero Cent = %d, %v", n, ok)
	}
	for _, raw := range []string{`0`, `"0"`, `42.5`, `"42.5"`, `"1e2"`} {
		if _, ok := jsonFloat(json.RawMessage(raw)); !ok {
			t.Errorf("rejected valid percentage %s", raw)
		}
	}
}

func TestGrokBillingOmittedZeroRequiresActivePeriod(t *testing.T) {
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	const active = `"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-05T21:44:41.777818+00:00","end":"2026-10-12T21:44:41.777818+00:00"}`
	cases := []struct {
		name, config string
		known        bool
		percent      int
	}{
		{"reset zero", active + `,"onDemandCap":{"val":0},"onDemandUsed":{"val":0},"prepaidBalance":{"val":0},"isUnifiedBillingUser":true`, true, 0},
		{"explicit percent", active + `,"creditUsagePercent":25`, true, 25},
		{"explicit snake percent", active + `,"credit_usage_percent":25`, true, 25},
		{"used ratio", active + `,"used":{"val":25},"monthlyLimit":{"val":100}`, true, 25},
		{"explicit zero", active + `,"creditUsagePercent":0`, true, 0},
		{"start inclusive", `"currentPeriod":{"start":"2026-10-06T08:00:00Z","end":"2026-10-13T08:00:00Z"}`, true, 0},
		{"top level bounds", `"billingPeriodStart":"2026-10-05T21:44:41.777818Z","billingPeriodEnd":"2026-10-12T21:44:41.777818Z"`, true, 0},
		{"snake bounds", `"current_period":{"start":"2026-10-05T21:44:41.777818Z","end":"2026-10-12T21:44:41.777818Z"}`, true, 0},
		{"null period with fallback", `"currentPeriod":null,"billingPeriodStart":"2026-10-05T21:44:41Z","billingPeriodEnd":"2026-10-12T21:44:41Z"`, false, 0},
		{"incomplete period with fallback", `"currentPeriod":{"end":"2026-10-12T21:44:41Z"},"billingPeriodStart":"2026-10-05T21:44:41Z"`, false, 0},
		{"contradictory bounds", active + `,"billingPeriodStart":"2026-10-04T21:44:41Z"`, false, 0},
		{"null alternate bound", active + `,"billing_period_end":null`, false, 0},
		{"missing period", ``, false, 0},
		{"null period", `"currentPeriod":null`, false, 0},
		{"malformed period", `"currentPeriod":[]`, false, 0},
		{"missing start", `"currentPeriod":{"end":"2026-10-12T21:44:41Z"}`, false, 0},
		{"missing end", `"currentPeriod":{"start":"2026-10-05T21:44:41Z"}`, false, 0},
		{"null start", `"currentPeriod":{"start":null,"end":"2026-10-12T21:44:41Z"}`, false, 0},
		{"bad start", `"currentPeriod":{"start":"bad","end":"2026-10-12T21:44:41Z"}`, false, 0},
		{"bad end", `"currentPeriod":{"start":"2026-10-05T21:44:41Z","end":"bad"}`, false, 0},
		{"inverted", `"currentPeriod":{"start":"2026-10-12T21:44:41Z","end":"2026-10-05T21:44:41Z"}`, false, 0},
		{"empty interval", `"currentPeriod":{"start":"2026-10-06T08:00:00Z","end":"2026-10-06T08:00:00Z"}`, false, 0},
		{"future", `"currentPeriod":{"start":"2026-10-12T21:44:41Z","end":"2026-10-19T21:44:41Z"}`, false, 0},
		{"expired", `"currentPeriod":{"start":"2026-09-28T21:44:41Z","end":"2026-10-05T21:44:41Z"}`, false, 0},
		{"end exclusive", `"currentPeriod":{"start":"2026-09-29T08:00:00Z","end":"2026-10-06T08:00:00Z"}`, false, 0},
		{"used without limit", active + `,"used":0`, false, 0},
		{"empty Cent used without limit", active + `,"used":{}`, false, 0},
		{"limit without used", active + `,"monthlyLimit":100`, false, 0},
		{"snake limit without used", active + `,"monthly_limit":100`, false, 0},
		{"zero limit without used", active + `,"monthlyLimit":0`, false, 0},
		{"empty Cent limit without used", active + `,"monthlyLimit":{}`, false, 0},
		{"zero snake limit without used", active + `,"monthly_limit":0`, false, 0},
		{"empty Cent snake limit without used", active + `,"monthly_limit":{}`, false, 0},
	}
	for _, key := range []string{"creditUsagePercent", "credit_usage_percent", "used", "monthlyLimit", "monthly_limit"} {
		for _, raw := range []string{`null`, `"12junk"`, `true`, `[]`, `-1`, `101`, `1e999`} {
			cases = append(cases, struct {
				name, config string
				known        bool
				percent      int
			}{key + "/" + raw, active + fmt.Sprintf(",%q:%s", key, raw), false, 0})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := parseGrokBilling([]byte(`{"config":{`+tc.config+`}}`), now)
			if info.NumericQuotaKnown() != tc.known {
				t.Fatalf("known=%v, want %v: %+v", info.NumericQuotaKnown(), tc.known, info)
			}
			if tc.known {
				w := info.PrimaryWindow
				if info.QuotaStatus != QuotaOK || info.Error != "" || info.QuotaNote != "" || w == nil || w.Unmeasured ||
					w.Kind != "billing" || w.Label != "included" ||
					w.UsedPercent != tc.percent || w.Utilization != float64(tc.percent)/100 || w.WindowDuration != 7*24*time.Hour || !w.ResetsAt.After(now) {
					t.Fatalf("unexpected measured window: %+v / %+v", info, w)
				}
			} else if info.QuotaStatus != QuotaDegraded || info.AvailabilityScore() != 0 {
				t.Fatalf("unknown quota became available: %+v", info)
			}
		})
	}
	for _, raw := range []string{``, `{`, `null`, `[]`} {
		info := parseGrokBilling([]byte(raw), now)
		if info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() {
			t.Errorf("invalid JSON/object became quota: %q: %+v", raw, info)
		}
	}
	for _, raw := range []string{`{}`, `{"config":null}`} {
		info := parseGrokBilling([]byte(raw), now)
		if info.QuotaStatus != QuotaDegraded || info.NumericQuotaKnown() {
			t.Errorf("missing config became quota: %s: %+v", raw, info)
		}
	}
	t.Run("precise bounds preserve metadata", func(t *testing.T) {
		start := now.Add(100 * time.Nanosecond)
		end := start.Add(7*24*time.Hour + 800*time.Nanosecond)
		zone := time.FixedZone("provider", -4*60*60)
		startText, endText := start.In(zone).Format(time.RFC3339Nano), end.In(zone).Format(time.RFC3339Nano)
		raw := []byte(fmt.Sprintf(`{"subscriptionTier":"pro","onDemandEnabled":true,"config":{`+
			`"currentPeriod":{"type":"WEEKLY","start":%q,"end":%q},"billing_period_start":%q,"billingPeriodEnd":%q,`+
			`"onDemandCap":{"val":"2500"},"onDemandUsed":{},"prepaidBalance":{"val":"800"},"isUnifiedBillingUser":false}}`,
			startText, endText, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)))
		for _, tc := range []struct {
			name      string
			fetchedAt time.Time
			known     bool
		}{
			{"before start", start.Add(-time.Nanosecond), false},
			{"at start", start, true},
			{"before end", end.Add(-time.Nanosecond), true},
			{"at end", end, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				info := parseGrokBilling(raw, tc.fetchedAt)
				if info.NumericQuotaKnown() != tc.known || info.Error != "" ||
					(tc.known && (info.QuotaStatus != QuotaOK || info.QuotaNote != "" || info.AvailabilityScore() != 100)) ||
					(!tc.known && (info.QuotaStatus != QuotaDegraded || info.QuotaNote == "" || info.AvailabilityScore() != 0)) {
					t.Fatalf("incorrect quota at nanosecond boundary: %+v", info)
				}
				if w := info.PrimaryWindow; w == nil || w.Unmeasured == tc.known || w.UsedPercent != 0 || w.Utilization != 0 ||
					w.Kind != "billing" || w.Label != "included" || !w.ResetsAt.Equal(end) || w.WindowDuration != end.Sub(start) {
					t.Fatalf("lost exact billing window: %+v", w)
				}
				bill := info.Billing
				if info.Provider != "grok" || info.Source != SourceAPI || info.PlanType != "pro" || !info.FetchedAt.Equal(tc.fetchedAt) ||
					bill == nil || bill.PeriodType != "WEEKLY" || bill.PeriodStart != startText || bill.PeriodEnd != endText ||
					bill.OnDemandCapCents == nil || *bill.OnDemandCapCents != 2500 || bill.OnDemandUsedCents == nil || *bill.OnDemandUsedCents != 0 ||
					bill.PrepaidBalanceCents == nil || *bill.PrepaidBalanceCents != 800 || bill.OnDemandEnabled == nil || !*bill.OnDemandEnabled ||
					bill.Unified == nil || *bill.Unified || info.Credits == nil || !info.Credits.HasCredits {
					t.Fatalf("lost source or billing metadata: info=%+v billing=%+v", info, bill)
				}
			})
		}
	})
	t.Run("zero clock uses actual fetch time", func(t *testing.T) {
		before := time.Now()
		start, end := before.Add(-time.Hour), before.Add(time.Hour)
		raw := []byte(fmt.Sprintf(`{"config":{"currentPeriod":{"start":%q,"end":%q}}}`, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)))
		info := parseGrokBilling(raw, time.Time{})
		if !info.NumericQuotaKnown() || info.QuotaStatus != QuotaOK || info.FetchedAt.Before(before) || info.FetchedAt.After(time.Now()) {
			t.Fatalf("zero clock did not validate against actual fetch time: %+v", info)
		}
	})
}

func TestGrokResetZeroIsEligibleForUsageAwareRouting(t *testing.T) {
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	info := parseGrokBilling([]byte(`{"config":{"currentPeriod":{"start":"2026-10-06T08:00:00Z","end":"2026-10-13T08:00:00Z"}}}`), now)
	unknown := parseGrokBilling([]byte(`{"config":{"creditUsagePercent":null}}`), now)
	if !info.NumericQuotaKnown() || info.AvailabilityScore() != 100 {
		t.Fatalf("fresh account did not retain full headroom: %+v", info)
	}
	for _, mode := range RankModes {
		result := RankProfiles([]ProfileUsage{
			{Provider: "grok", ProfileName: "unknown", Usage: unknown},
			{Provider: "grok", ProfileName: "reset", Usage: info},
		}, RankOptions{Mode: mode, Now: now})
		if result.Selected == nil || result.Selected.Profile != "reset" {
			t.Errorf("%s did not select freshly reset account: %+v", mode, result)
		}
	}
}

func TestCursorInvalidSpendDoesNotFallBackToRemaining(t *testing.T) {
	for _, fields := range []string{
		`"includedSpend":-0.1,"remaining":100`,
		`"includedSpend":-1,"remaining":100`,
		`"includedSpend":101,"remaining":100`,
		`"includedSpend":"broken","remaining":100`,
		`"includedSpend":null,"remaining":100`,
		`"includedSpend":25,"remaining":100`,
		`"includedSpend":25,"apiPercentUsed":"NaN"`,
		`"includedSpend":25,"apiPercentUsed":101`,
	} {
		info := parseCursorPeriod([]byte(`{"planUsage":{"limit":100,`+fields+`}}`), time.Now())
		if info.NumericQuotaKnown() {
			t.Errorf("invalid or contradictory figures became capacity: %s (%+v)", fields, info)
		}
	}
	for _, fields := range []string{`"includedSpend":0`, `"remaining":75`, `"includedSpend":25,"remaining":75,"apiPercentUsed":60`} {
		info := parseCursorPeriod([]byte(`{"planUsage":{"limit":100,`+fields+`}}`), time.Now())
		if !info.NumericQuotaKnown() {
			t.Errorf("valid measured quota rejected: %s (%+v)", fields, info)
		}
	}
}

func TestCursorGrantsAreNotGeneralCapacity(t *testing.T) {
	for _, grants := range []string{
		`[{"totalCents":100,"remainingCents":100,"allowedModelIds":["restricted-model"]}]`,
		`[{"totalCents":100,"remainingCents":100,"expiresAtMs":1}]`,
		`[{"totalCents":100,"remainingCents":100}]`,
		`[{"totalCents":"9223372036854775807","remainingCents":0},{"totalCents":2,"remainingCents":2}]`,
	} {
		info := parseCursorUsage([]byte(`{"activeGrants":`+grants+`}`), time.Now())
		if info.NumericQuotaKnown() || info.AvailabilityScore() != 0 {
			t.Errorf("grants became unscoped capacity: %+v", info)
		}
		if len(info.Grants) == 0 {
			t.Error("grant metadata was lost")
		}
	}
}

func TestNativeQuotaRequiresMeasuredWindow(t *testing.T) {
	for _, provider := range []string{"cursor", "grok"} {
		for _, row := range []*UsageInfo{
			{Provider: provider},
			{Provider: provider, QuotaStatus: QuotaOK},
			{Provider: provider, QuotaStatus: "future-status", PrimaryWindow: &UsageWindow{}},
			{Provider: provider, QuotaStatus: QuotaOK, PrimaryWindow: &UsageWindow{Unmeasured: true}},
			{Provider: provider, QuotaStatus: QuotaOK, PrimaryWindow: &UsageWindow{Utilization: math.NaN()}},
			{Provider: provider, QuotaStatus: QuotaOK, PrimaryWindow: &UsageWindow{Utilization: math.Inf(1)}},
			{Provider: provider, QuotaStatus: QuotaOK, PrimaryWindow: &UsageWindow{UsedPercent: -1}},
			{Provider: provider, QuotaStatus: QuotaOK, PrimaryWindow: &UsageWindow{}, SecondaryWindow: &UsageWindow{Unmeasured: true}},
		} {
			if row.NumericQuotaKnown() || row.AvailabilityScore() != 0 {
				t.Errorf("unmeasured row ranked as capacity: %+v", row)
			}
		}
		zero := &UsageInfo{Provider: provider, QuotaStatus: QuotaOK, PrimaryWindow: &UsageWindow{}}
		if !zero.NumericQuotaKnown() || zero.AvailabilityScore() != 100 {
			t.Errorf("explicit measured zero rejected: %+v", zero)
		}
	}
	for _, provider := range []string{"claude", "codex"} {
		row := &UsageInfo{Provider: provider, PrimaryWindow: &UsageWindow{UsedPercent: 25}}
		if !row.NumericQuotaKnown() {
			t.Errorf("legacy %s quota changed", provider)
		}
	}
}

func TestCursorVaultUsesCanonicalCredentialOnly(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(canonical, []byte(`{"accessToken":"SYNTHETIC-CURRENT"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xdg-auth.json"), []byte(`{"accessToken":"SYNTHETIC-STALE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	locator, err := cursorVaultLocator(dir)
	if err != nil || locator != "cursor-root:"+canonical {
		t.Fatalf("locator=%q err=%v, want canonical main vault path", locator, err)
	}
	if err := os.WriteFile(canonical, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if locator, err := cursorVaultLocator(dir); err == nil {
		t.Errorf("invalid canonical credential fell back to another account: %q", locator)
	}
	if path, err := findCursorAuth(dir); err == nil {
		t.Errorf("invalid canonical root fell back to another account: %q", path)
	}
}

func TestCursorReadRejectsRedirectsAndOversizedBodies(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		fmt.Fprint(w, `{}`)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	fetcher := &CursorFetcher{BaseURL: origin.URL, HTTP: client, ClientVersion: "synthetic"}
	_, status, err := fetcher.post(context.Background(), "SYNTHETIC", cursorUsagePath)
	if redirected.Load() != 0 || (err == nil && status >= 200 && status < 300) {
		t.Fatalf("followed a credential-bearing redirect: hits=%d status=%d err=%v", redirected.Load(), status, err)
	}
	if client.CheckRedirect(nil, nil) != nil {
		t.Fatal("fetcher mutated the supplied HTTP client")
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`+strings.Repeat(" ", 1<<20))
	}))
	defer large.Close()
	fetcher.BaseURL = large.URL
	if _, _, err := fetcher.post(context.Background(), "SYNTHETIC", cursorUsagePath); err == nil {
		t.Fatal("oversized valid JSON prefix was silently accepted")
	}
}

type nativeQuotaTransport func(*http.Request) (*http.Response, error)

func (f nativeQuotaTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestCursorIgnoresAmbientEndpoint(t *testing.T) {
	t.Setenv("CURSOR_API_ENDPOINT", "https://wrong-account.example")
	fetcher := &CursorFetcher{ClientVersion: "synthetic", HTTP: &http.Client{
		Transport: nativeQuotaTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "api2.cursor.sh" || req.URL.Scheme != "https" {
				t.Errorf("ambient endpoint received profile credential: %s", req.URL)
			}
			if req.Header.Get("Authorization") != "Bearer SYNTHETIC" {
				t.Error("missing authorization header")
			}
			if _, ok := req.Context().Deadline(); !ok {
				t.Error("request has no deadline with a supplied HTTP client")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		}),
	}}
	if _, _, err := fetcher.post(context.Background(), "SYNTHETIC", cursorUsagePath); err != nil {
		t.Fatal(err)
	}
}

func TestCursorLimitStageVetoesSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case cursorPeriodUsagePath:
			fmt.Fprint(w, `{"planUsage":{"limit":100,"includedSpend":0,"apiPercentUsed":0}}`)
		case cursorUsagePath:
			fmt.Fprint(w, `{"usageLimitPolicyStatus":{"stage":"LIMIT_HIT_STAGE_HARD_BLOCK"}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"accessToken":"SYNTHETIC"}`), 0600); err != nil {
		t.Fatal(err)
	}
	fetcher := &CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}
	info, err := fetcher.Fetch(context.Background(), root)
	if err != nil || info.NumericQuotaKnown() || info.AvailabilityScore() != 0 {
		t.Fatalf("blocked account became selectable: %+v err=%v", info, err)
	}
	if info.LimitStage != "HARD_BLOCK" || info.PrimaryWindow == nil {
		t.Fatalf("lost measured quota or restriction metadata: %+v", info)
	}
}

func TestGrokProviderErrorsNeverEchoOpaqueCredentials(t *testing.T) {
	for _, text := range []string{"invalid token SYNTHETIC-OPAQUE-SECRET", "SYNTHETIC-API-KEY", "request failed\nSYNTHETIC", "Bearer SYNTHETIC"} {
		if got := sanitizeProviderText(text); strings.Contains(got, "SYNTHETIC") {
			t.Errorf("provider text leaked a credential: %q", got)
		}
	}
}

func TestGrokStagesOnlyCredentialsAndIsolatesConfig(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("# Do not copy executable hooks or MCP configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	stage, err := stageGrokHome(auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stage) })
	if _, err := os.Stat(filepath.Join(stage, "config.toml")); !os.IsNotExist(err) {
		t.Error("billing stage inherited arbitrary client configuration")
	}
	st, err := os.Stat(filepath.Join(stage, "auth.json"))
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0600) {
		t.Fatalf("staged credential permissions: %v, %v", st, err)
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "GROK_AUTH", "GROK_AUTH_PATH", "GROK_API_KEY", "GROK_DEPLOYMENT_KEY", "XAI_API_KEY", "XAI_API_TOKEN"} {
		t.Setenv(key, "SYNTHETIC-AMBIENT")
	}
	guardDir, err := grokBrowserGuardDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range grokChildEnv(stage, guardDir) {
		if strings.Contains(entry, "SYNTHETIC-AMBIENT") {
			t.Errorf("ambient configuration/credential inherited: %s", entry)
		}
	}
}

func TestGrokDeadlineClosesInheritedProtocolPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "grok")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	info, err := (&GrokFetcher{Bin: bin}).Fetch(ctx, home)
	if err != nil || info.QuotaStatus != QuotaUnavailable {
		t.Fatalf("timeout should produce an unavailable row: %+v %v", info, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("deadline did not release inherited stdout: %s", elapsed)
	}
}

func TestCursorPrimaryFailureCannotBeHiddenByMetadata(t *testing.T) {
	for _, body := range []string{"not-json", `{"planUsage":{}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case cursorPeriodUsagePath:
					fmt.Fprint(w, body)
				case cursorUsagePath:
					fmt.Fprint(w, `{"activeGrants":[{"totalCents":100,"remainingCents":100}]}`)
				default:
					fmt.Fprint(w, `{}`)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"accessToken":"SYNTHETIC"}`), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := (&CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}).Fetch(context.Background(), dir)
			if err != nil || info.NumericQuotaKnown() || len(info.Grants) != 1 {
				t.Fatalf("failed period or grants not retained: %+v %v", info, err)
			}
			if body == "not-json" && info.QuotaStatus != QuotaUnavailable {
				t.Fatalf("malformed period became %s", info.QuotaStatus)
			}
		})
	}
}

func TestGrokBillingOnlyUsesReadProtocolAndLeavesCredentialUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	home := t.TempDir()
	auth := `{"key":"SYNTHETIC","email":"test@example.com","expires_at":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "methods")
	t.Setenv("CAAM_GROK_METHODS", log)
	t.Setenv("CAAM_GROK_BILLING_ERROR", "0")
	bin := filepath.Join(t.TempDir(), "grok")
	script := `#!/bin/sh
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  printf '%s\n' "$method" >> "$CAAM_GROK_METHODS"
  case "$method" in
    _x.ai/billing)
      if [ "$CAAM_GROK_BILLING_ERROR" = "1" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"SYNTHETIC-PROVIDER-SECRET"}}\n' "$id"
      else
        printf '{"jsonrpc":"2.0","id":%s,"result":{"config":{"creditUsagePercent":42.5}}}\n' "$id"
      fi
      exit 0
      ;;
    initialize|authenticate|session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"synthetic"}}\n' "$id"
      ;;
    *) exit 1 ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
	if err != nil || !info.NumericQuotaKnown() || info.PrimaryWindow.Utilization != 0.425 {
		t.Fatalf("billing = %+v, err=%v", info, err)
	}
	methods, err := os.ReadFile(log)
	if err != nil || string(methods) != "initialize\nauthenticate\nsession/new\n_x.ai/billing\n" {
		t.Fatalf("unexpected ACP methods: %q, %v", methods, err)
	}
	got, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(got) != auth {
		t.Fatal("source credential was modified")
	}
	t.Run("billing protocol error remains unavailable", func(t *testing.T) {
		t.Setenv("CAAM_GROK_BILLING_ERROR", "1")
		info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
		if err != nil || info.QuotaStatus != QuotaUnavailable || info.Error == "" || info.QuotaNote == "" ||
			info.NumericQuotaKnown() || info.AvailabilityScore() != 0 || info.PrimaryWindow != nil {
			t.Fatalf("billing protocol failure became quota: %+v, err=%v", info, err)
		}
		if strings.Contains(info.Error, "SYNTHETIC") || strings.Contains(info.QuotaNote, "SYNTHETIC") {
			t.Fatal("billing protocol failure echoed provider secrets")
		}
		got, err := os.ReadFile(filepath.Join(home, "auth.json"))
		if err != nil || string(got) != auth {
			t.Fatal("billing protocol failure modified the source credential")
		}
	})
}

func TestCursorAuthRejectionCannotBeHiddenByAnotherUsageResponse(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, rejectedPath := range []string{cursorPeriodUsagePath, cursorUsagePath} {
			t.Run(fmt.Sprintf("%d/%s", code, rejectedPath), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == rejectedPath {
						w.WriteHeader(code)
						fmt.Fprint(w, "SYNTHETIC-PROVIDER-SECRET")
					} else if r.URL.Path == cursorPeriodUsagePath {
						fmt.Fprint(w, `{"planUsage":{"includedSpend":10,"limit":100}}`)
					} else {
						fmt.Fprint(w, `{}`)
					}
				}))
				defer server.Close()
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"accessToken":"SYNTHETIC"}`), 0600); err != nil {
					t.Fatal(err)
				}
				info, err := (&CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}).Fetch(context.Background(), dir)
				if err != nil || info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() || info.Error != "unauthorized: token expired or invalid" {
					t.Fatalf("rejected token reported quota: %+v %v", info, err)
				}
			})
		}
	}
}

func TestGrokAuthRejectionClassification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "grok")
	script := `#!/bin/sh
IFS= read -r line
id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
printf '{"jsonrpc":"2.0","id":%s,"error":%s}\n' "$id" "$CAAM_TEST_GROK_AUTH_ERROR"
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body   string
		unauthorized bool
	}{
		{"401", `{"code":401,"message":"SYNTHETIC"}`, true},
		{"403", `{"code":403,"message":"SYNTHETIC"}`, true},
		{"expired token", `{"code":-32603,"message":"token expired: SYNTHETIC"}`, true},
		{"unauthorized", `{"code":-32603,"message":"Unauthorized: SYNTHETIC"}`, true},
		{"server error", `{"code":-32000,"message":"SYNTHETIC"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAAM_TEST_GROK_AUTH_ERROR", tc.body)
			info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
			if err != nil || info.NumericQuotaKnown() || info.QuotaStatus != QuotaUnavailable || info.Error == "" {
				t.Fatalf("failure reported quota: %+v %v", info, err)
			}
			if got := info.Error == "unauthorized: token expired or invalid"; got != tc.unauthorized {
				t.Fatalf("authentication classification=%t want %t: %s", got, tc.unauthorized, info.Error)
			}
			if strings.Contains(info.Error, "SYNTHETIC") {
				t.Fatal("error echoed provider credential text")
			}
		})
	}
}

func TestGrokProbeReadyRejectsUnusableCredential(t *testing.T) {
	now := time.Now()
	valid := func(expires string) string {
		return fmt.Sprintf(`{"https://auth.x.ai::client":{"key":"SYNTHETIC-ACCESS","expires_at":%q}}`, expires)
	}
	cases := []struct {
		name string
		auth string
		want error // nil means usable
	}{
		{"valid dynamic-key", valid("2099-01-01T00:00:00Z"), nil},
		{"valid flat", `{"access_token":"SYNTHETIC-ACCESS","expires_at":"2099-01-01T00:00:00Z"}`, nil},
		{"expired", valid(now.Add(-time.Hour).Format(time.RFC3339)), errGrokNeedsLogin},
		{"near expiry", valid(now.Add(10 * time.Minute).Format(time.RFC3339)), errGrokNeedsLogin},
		{"missing access token", `{"https://auth.x.ai::client":{"expires_at":"2099-01-01T00:00:00Z"}}`, errGrokNeedsLogin},
		{"no expiry", `{"https://auth.x.ai::client":{"key":"SYNTHETIC-ACCESS"}}`, errGrokNeedsLogin},
		{"not an object", `[]`, errGrokNeedsLogin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(path, []byte(tc.auth), 0600); err != nil {
				t.Fatal(err)
			}
			err := grokProbeReady(path, now)
			if (err == nil) != (tc.want == nil) {
				t.Fatalf("grokProbeReady = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestGrokFetcherSkipsCLIForUnusableCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	for _, tc := range []struct {
		name    string
		expires string
	}{
		{"expired", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		{"near expiry", time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			auth := fmt.Sprintf(`{"key":"SYNTHETIC-ACCESS","expires_at":%q}`, tc.expires)
			if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); err != nil {
				t.Fatal(err)
			}
			started := filepath.Join(t.TempDir(), "started")
			t.Setenv("CAAM_TEST_GROK_STARTED", started)
			bin := filepath.Join(t.TempDir(), "grok")
			script := `#!/bin/sh
touch "$CAAM_TEST_GROK_STARTED"
`
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
			if err != nil {
				t.Fatal(err)
			}
			if info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() {
				t.Fatalf("unusable credential reported quota: %+v", info)
			}
			if !strings.Contains(info.QuotaNote, "run grok") {
				t.Fatalf("unusable credential note = %q, want a hint to run grok", info.QuotaNote)
			}
			if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
				t.Fatal("grok binary was started for an unusable credential")
			}
		})
	}
}

func TestGrokFetcherRejectsMissingCachedTokenMethod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC-ACCESS","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	methods := filepath.Join(t.TempDir(), "methods")
	t.Setenv("CAAM_TEST_GROK_METHODS", methods)
	bin := filepath.Join(t.TempDir(), "grok")
	script := `#!/bin/sh
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  printf '%s\n' "$method" >> "$CAAM_TEST_GROK_METHODS"
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"authMethods":[{"id":"xai.api_key"},{"id":"grok.com"}]}}\n' "$id"
      ;;
    *) exit 1 ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
	if err != nil || info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() || !strings.Contains(info.QuotaNote, "run grok") {
		t.Fatalf("missing cached_token should be unavailable: %+v err=%v", info, err)
	}
	got, _ := os.ReadFile(methods)
	if strings.TrimSpace(string(got)) != "initialize" {
		t.Fatalf("authenticate was attempted after cached_token was absent: %q", got)
	}
}

func TestGrokFetcherBoundsAuthenticateWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC-ACCESS","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "grok")
	script := `#!/bin/sh
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"authMethods":[{"id":"cached_token"}]}}\n' "$id"
      ;;
    authenticate)
      sleep 30
      ;;
    *) exit 1 ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
	if err != nil || info.QuotaStatus != QuotaUnavailable {
		t.Fatalf("hung authenticate should be unavailable: %+v err=%v", info, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("authenticate wait was not bounded: %s", elapsed)
	}
}

func TestGrokFetcherSuccessPathAdvertisesCachedToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC-ACCESS","expires_at":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	envLog := filepath.Join(t.TempDir(), "env")
	t.Setenv("CAAM_TEST_GROK_ENV", envLog)
	bin := filepath.Join(t.TempDir(), "grok")
	script := `#!/bin/sh
env | sort > "$CAAM_TEST_GROK_ENV"
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"authMethods":[{"id":"cached_token"},{"id":"grok.com"}]}}\n' "$id"
      ;;
    authenticate|session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"synthetic"}}\n' "$id"
      ;;
    _x.ai/billing)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"config":{"creditUsagePercent":42.5}}}\n' "$id"
      exit 0
      ;;
    *) exit 1 ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := (&GrokFetcher{Bin: bin}).Fetch(context.Background(), home)
	if err != nil || !info.NumericQuotaKnown() || info.PrimaryWindow.Utilization != 0.425 {
		t.Fatalf("billing = %+v, err=%v", info, err)
	}
	raw, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	envStr := string(raw)
	if !strings.Contains(envStr, "BROWSER=/usr/bin/false") {
		t.Fatalf("child did not receive the BROWSER guard: %q", envStr)
	}
	if !strings.Contains(envStr, "browser-guard") {
		t.Fatalf("child PATH does not contain the browser guard dir: %q", envStr)
	}
}

func TestGrokChildEnvCarriesBrowserGuards(t *testing.T) {
	stage := t.TempDir()
	guardDir, err := grokBrowserGuardDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", "SYNTHETIC-AMBIENT-BROWSER")
	env := grokChildEnv(stage, guardDir)

	var (
		sawBrowser bool
		pathEntry  string
		hasPath    bool
	)
	browserCount := 0
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "BROWSER":
			sawBrowser = true
			browserCount++
			if val != "/usr/bin/false" {
				t.Fatalf("BROWSER = %q, want /usr/bin/false", val)
			}
		case "PATH":
			pathEntry = val
			hasPath = true
		}
	}
	if !sawBrowser {
		t.Fatal("BROWSER not set in child environment")
	}
	if browserCount != 1 {
		t.Fatalf("BROWSER appears %d times, want 1 (inherited value must be dropped)", browserCount)
	}
	if !hasPath {
		t.Fatal("PATH not present in child environment")
	}
	if !strings.HasPrefix(pathEntry, guardDir+string(os.PathListSeparator)) {
		t.Fatalf("PATH does not lead with browser guard dir: %q", pathEntry)
	}
	for _, name := range []string{"open", "xdg-open"} {
		st, err := os.Stat(filepath.Join(guardDir, name))
		if err != nil || st.Mode()&0111 == 0 {
			t.Fatalf("guard script %s missing or not executable: %v", name, err)
		}
	}
}
