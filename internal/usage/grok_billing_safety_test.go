package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const grokBillingFreshFixture = `{"key":"SYNTHETIC-ACCESS","refresh_token":"SYNTHETIC-REFRESH","expires_at":"2099-01-01T00:00:00Z"}`

// The fake CLI records every launch and RPC. No installed Grok binary, real
// credential, provider endpoint or browser is needed to exercise the process.
func grokBillingProcessFixture(t *testing.T, initialize string) (*GrokFetcher, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake CLI; credential/handshake validators are portable")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("CAAM_TEST_BILLING_LOG", log)
	t.Setenv("CAAM_TEST_BILLING_INITIALIZE", initialize)
	t.Setenv("CAAM_TEST_BILLING_HANG", "")
	t.Setenv("CAAM_TEST_BILLING_BROWSER", "")
	t.Setenv("CAAM_TEST_BILLING_CAPTURE", filepath.Join(dir, "capture"))
	bin := filepath.Join(dir, "grok")
	script := `#!/bin/sh
printf 'start\n' >> "$CAAM_TEST_BILLING_LOG"
cat "$GROK_HOME/auth.json" > "$CAAM_TEST_BILLING_CAPTURE"
if [ -n "$CAAM_TEST_BILLING_BROWSER" ]; then
  "$BROWSER" 'https://example.invalid/consent' && exit 10
  open 'https://example.invalid/consent' && exit 11
  xdg-open 'https://example.invalid/consent' && exit 12
fi
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  printf '%s\n' "$method" >> "$CAAM_TEST_BILLING_LOG"
  case "$method" in
    initialize) result="$CAAM_TEST_BILLING_INITIALIZE" ;;
    authenticate)
      case "$line" in *'"methodId":"cached_token"'*) ;; *) exit 13 ;; esac
      if [ -n "$CAAM_TEST_BILLING_HANG" ]; then sleep "$CAAM_TEST_BILLING_HANG"; fi
      result='{}' ;;
    session/new) result='{"sessionId":"synthetic"}' ;;
    _x.ai/billing) result='{"config":{"creditUsagePercent":25}}' ;;
    *) exit 14 ;;
  esac
  printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &GrokFetcher{Bin: bin}, log
}

func TestGrokBillingPreflightNeverStartsProcess(t *testing.T) {
	for name, auth := range map[string]string{
		"expired":                            `{"key":"SYNTHETIC-ACCESS","expires_at":"2020-01-01T00:00:00Z"}`,
		"expiring":                           fmt.Sprintf(`{"key":"SYNTHETIC-ACCESS","expires_at":%q}`, time.Now().Add(time.Minute).Format(time.RFC3339Nano)),
		"unknown deadline":                   `{"key":"SYNTHETIC-ACCESS"}`,
		"relative lifetime without issuance": `{"key":"SYNTHETIC-ACCESS","expires_in":3600}`,
		"missing access":                     `{"refresh_token":"SYNTHETIC-REFRESH","expires_at":"2099-01-01T00:00:00Z"}`,
		"empty access":                       `{"key":" ","expires_at":"2099-01-01T00:00:00Z"}`,
		"padded access":                      `{"key":" SYNTHETIC-ACCESS ","expires_at":"2099-01-01T00:00:00Z"}`,
		"null access":                        `{"key":null,"expires_at":"2099-01-01T00:00:00Z"}`,
		"invalid access":                     `{"key":42,"expires_at":"2099-01-01T00:00:00Z"}`,
		"invalid deadline":                   `{"key":"SYNTHETIC-ACCESS","expires_at":"SYNTHETIC-SECRET"}`,
		"null deadline":                      `{"key":"SYNTHETIC-ACCESS","expires_at":null}`,
		"missing entry":                      `{}`,
		"malformed entry":                    `{"https://auth.x.ai::client":null}`,
		"stale sibling":                      `{"one":` + grokBillingFreshFixture + `,"two":{"key":"SYNTHETIC-OLD","expires_at":"2020-01-01T00:00:00Z"}}`,
		"incomplete sibling":                 `{"one":` + grokBillingFreshFixture + `,"two":{"refresh_token":"SYNTHETIC-REFRESH"}}`,
		"contradictory aliases":              `{"key":"SYNTHETIC-ACCESS","access_token":"SYNTHETIC-OTHER","expires_at":"2099-01-01T00:00:00Z"}`,
		"invalid alternate deadline":         `{"key":"SYNTHETIC-ACCESS","expires_at":"2099-01-01T00:00:00Z","expiresAt":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, log := grokBillingProcessFixture(t, `{"protocolVersion":1,"authMethods":[{"id":"cached_token"}]}`)
			original := []byte(auth)
			result, err := f.queryBilling(context.Background(), original)
			if err == nil || len(result) != 0 {
				t.Errorf("unusable credential reached billing: error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "SYNTHETIC") {
				t.Error("preflight error exposed credential contents")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Error("unusable credential launched Grok")
			}
			if string(original) != auth {
				t.Error("preflight changed caller's credential")
			}
		})
	}
}

func TestGrokBillingHandshakeRequiresCachedToken(t *testing.T) {
	for _, result := range []string{
		`null`, `{}`, `[]`, `{"protocolVersion":1,"authMethods":null}`,
		`{"protocolVersion":1,"authMethods":[]}`,
		`{"protocolVersion":1,"authMethods":[{"id":"browser"}]}`,
		`{"protocolVersion":1,"authMethods":[{"id":"cached_token","type":"terminal"}]}`,
		`{"protocolVersion":2,"authMethods":[{"id":"cached_token"}]}`,
		`{"protocolVersion":1,"authMethods":[{"id":"cached_token"},{"id":"cached_token"}]}`,
		`{"protocolVersion":1,"authMethods":[{"id":42}]}`,
	} {
		t.Run(result, func(t *testing.T) {
			f, log := grokBillingProcessFixture(t, result)
			if _, err := f.queryBilling(context.Background(), []byte(grokBillingFreshFixture)); err == nil {
				t.Error("unadvertised or incompatible auth method was used")
			}
			calls, err := os.ReadFile(log)
			if err != nil || string(calls) != "start\ninitialize\n" {
				t.Fatalf("unsafe handshake advanced: %q: %v", calls, err)
			}
		})
	}
}

func TestGrokBillingFreshCachedTokenUsesReadOnlyProtocol(t *testing.T) {
	f, log := grokBillingProcessFixture(t, `{"protocolVersion":1,"authMethods":[{"id":"browser"},{"id":"cached_token"}]}`)
	input := []byte(grokBillingFreshFixture)
	result, err := f.queryBilling(context.Background(), input)
	if err != nil || string(result) != `{"config":{"creditUsagePercent":25}}` {
		t.Fatalf("fresh credential failed billing: %s: %v", result, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "start\ninitialize\nauthenticate\nsession/new\n_x.ai/billing\n" {
		t.Fatalf("unexpected RPC flow: %q: %v", calls, err)
	}
	staged, err := os.ReadFile(os.Getenv("CAAM_TEST_BILLING_CAPTURE"))
	if err != nil || !strings.Contains(string(staged), "SYNTHETIC-ACCESS") || strings.Contains(string(staged), "REFRESH") {
		t.Fatalf("wrong staged credential: %v", err)
	}
	if string(input) != grokBillingFreshFixture {
		t.Fatal("probe changed source credential")
	}
}

func TestGrokBillingAuthenticationCancellationClosesPipes(t *testing.T) {
	f, log := grokBillingProcessFixture(t, `{"protocolVersion":1,"authMethods":[{"id":"cached_token"}]}`)
	t.Setenv("CAAM_TEST_BILLING_HANG", "2")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := f.queryBilling(ctx, []byte(grokBillingFreshFixture)); err == nil {
		t.Fatal("hung authentication succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("authentication held inherited pipes after cancellation")
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "start\ninitialize\nauthenticate\n" {
		t.Fatalf("unexpected hung-auth protocol: %q: %v", calls, err)
	}
}

func TestGrokBillingAuthenticationHasIndependentDeadline(t *testing.T) {
	f, log := grokBillingProcessFixture(t, `{"protocolVersion":1,"authMethods":[{"id":"cached_token"}]}`)
	t.Setenv("CAAM_TEST_BILLING_HANG", "8")
	stages := t.TempDir()
	t.Setenv("TMPDIR", stages)
	start := time.Now()
	if _, err := f.queryBilling(context.Background(), []byte(grokBillingFreshFixture)); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unbounded or successful hung authentication: %v", err)
	}
	if elapsed := time.Since(start); elapsed < grokBillingAuthTimeout || elapsed > grokBillingAuthTimeout+2*time.Second {
		t.Fatalf("authentication deadline not enforced: %s", elapsed)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "start\ninitialize\nauthenticate\n" {
		t.Fatalf("timeout advanced protocol: %q: %v", calls, err)
	}
	entries, err := os.ReadDir(stages)
	if err != nil || len(entries) != 0 {
		t.Fatalf("timed-out probe left staged credentials: %v", err)
	}
}

func TestGrokBillingBrowserGuardOverridesAmbientOpeners(t *testing.T) {
	f, _ := grokBillingProcessFixture(t, `{"protocolVersion":1,"authMethods":[{"id":"cached_token"}]}`)
	t.Setenv("CAAM_TEST_BILLING_BROWSER", "1")
	ambient := t.TempDir()
	marker := filepath.Join(ambient, "opened")
	t.Setenv("CAAM_TEST_BROWSER_MARKER", marker)
	for _, name := range []string{"browser", "open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(ambient, name), []byte("#!/bin/sh\nprintf 'opened' > \"$CAAM_TEST_BROWSER_MARKER\"\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BROWSER", filepath.Join(ambient, "browser"))
	t.Setenv("PATH", ambient+string(os.PathListSeparator)+os.Getenv("PATH"))
	parentBrowser, parentPath := os.Getenv("BROWSER"), os.Getenv("PATH")
	stages := t.TempDir()
	t.Setenv("TMPDIR", stages)
	if _, err := f.queryBilling(context.Background(), []byte(grokBillingFreshFixture)); err != nil {
		t.Fatalf("opener defense did not preserve cached billing: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("quota probe invoked an ambient browser or opener")
	}
	if os.Getenv("BROWSER") != parentBrowser || os.Getenv("PATH") != parentPath {
		t.Fatal("probe mutated the parent's browser environment")
	}
	entries, err := os.ReadDir(stages)
	if err != nil || len(entries) != 0 {
		t.Fatalf("completed probe left staged credentials/guards: %v", err)
	}
}

func TestGrokBillingAuthDeadlineAndLayouts(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Hour, 0, 15 * time.Minute, 15*time.Minute + time.Nanosecond, time.Hour} {
		deadline := now.Add(offset)
		for _, field := range []string{"key", "token", "access_token", "accessToken"} {
			entry := fmt.Sprintf(`{%q:"SYNTHETIC-ACCESS","expires_at":%q}`, field, deadline.Format(time.RFC3339Nano))
			for _, auth := range []string{entry, `{"https://auth.x.ai::client":` + entry + `}`, `{"one":` + entry + `,"two":` + entry + `}`} {
				if got := validateGrokBillingAuth([]byte(auth), now) == nil; got != (offset > 15*time.Minute) {
					t.Fatalf("wrong freshness/layout verdict for %s at %s: %v", field, offset, got)
				}
			}
		}
	}
	for _, raw := range []string{`null`, `{}`, `[]`, `true`, `-1`, `0`, `1.5`, `1e99`, `9223372036854775807`, `"invalid"`, `"9999999999999999999"`} {
		if !grokBillingDeadline(json.RawMessage(raw)).IsZero() {
			t.Errorf("unsafe deadline accepted: %s", raw)
		}
	}
	for _, raw := range []string{`"2026-10-20T13:00:00Z"`, fmt.Sprint(now.Add(time.Hour).Unix()), fmt.Sprint(now.Add(time.Hour).UnixMilli()), fmt.Sprintf("%q", fmt.Sprint(now.Add(time.Hour).Unix()))} {
		if got := grokBillingDeadline(json.RawMessage(raw)); !got.Equal(now.Add(time.Hour)) {
			t.Errorf("absolute deadline changed: %s -> %s", raw, got)
		}
	}
	for _, auth := range []string{`null`, `[]`, `{`, `{} {}`, `{"expires_at":"2099-01-01T00:00:00Z"}`, `{"one":` + grokBillingFreshFixture + `,"issuer::client":[]}`} {
		if validateGrokBillingAuth([]byte(auth), now) == nil {
			t.Errorf("malformed/partial credential accepted: %s", auth)
		}
	}
}

func TestGrokBillingHandshakeValidator(t *testing.T) {
	for _, advertised := range []string{
		`{"protocolVersion":1,"authMethods":[{"id":"cached_token"}]}`,
		`{"protocolVersion":1,"authMethods":[{"id":"cached_token","type":"agent"}]}`,
		`{"protocolVersion":1,"authMethods":[{"id":"browser"},{"id":"cached_token"}]}`,
	} {
		if !grokBillingCachedTokenAvailable(json.RawMessage(advertised)) {
			t.Errorf("supported cached-token flow refused: %s", advertised)
		}
	}
	for _, advertised := range []string{`null`, `{}`, `[]`, `{`, `{"protocolVersion":2}`, `{"protocolVersion":1,"authMethods":[{"id":"cached_token","type":"env_var"}]}`} {
		if grokBillingCachedTokenAvailable(json.RawMessage(advertised)) {
			t.Errorf("unsupported handshake accepted: %s", advertised)
		}
	}
}
