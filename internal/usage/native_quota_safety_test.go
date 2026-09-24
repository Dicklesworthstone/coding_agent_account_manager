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
	stage, err := stageGrokHome(home)
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
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "GROK_DEPLOYMENT_KEY", "XAI_API_KEY"} {
		t.Setenv(key, "SYNTHETIC-AMBIENT")
	}
	for _, entry := range grokChildEnv(stage) {
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
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"key":"SYNTHETIC"}`), 0600); err != nil {
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
	auth := `{"key":"SYNTHETIC","email":"test@example.com"}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "methods")
	t.Setenv("CAAM_GROK_METHODS", log)
	bin := filepath.Join(t.TempDir(), "grok")
	script := `#!/bin/sh
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  printf '%s\n' "$method" >> "$CAAM_GROK_METHODS"
  case "$method" in
    _x.ai/billing)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"config":{"creditUsagePercent":42.5}}}\n' "$id"
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
}
