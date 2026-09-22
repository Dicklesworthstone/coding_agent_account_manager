package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every fixture in this file is synthetic. No token, auth id, or real account.

func TestParseGrokBilling_FullPercent(t *testing.T) {
	raw := []byte(`{
		"config": {
			"creditUsagePercent": 42.5,
			"currentPeriod": {
				"type": "USAGE_PERIOD_TYPE_WEEKLY",
				"start": "2026-06-01T00:00:00Z",
				"end": "2026-06-08T00:00:00Z"
			},
			"onDemandCap": {"val": 5000},
			"onDemandUsed": {"val": 300},
			"prepaidBalance": {"val": 1250},
			"isUnifiedBillingUser": true
		},
		"subscription_tier": "SuperGrok",
		"on_demand_enabled": true
	}`)
	info := parseGrokBilling(raw, time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC))
	if info.QuotaStatus != QuotaOK {
		t.Fatalf("status %q note %q", info.QuotaStatus, info.QuotaNote)
	}
	if info.PlanType != "SuperGrok" {
		t.Fatalf("plan %q", info.PlanType)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.Unmeasured || info.PrimaryWindow.UsedPercent != 42 {
		t.Fatalf("window %+v", info.PrimaryWindow)
	}
	if got := info.PrimaryWindow.ResetsAt.UTC().Format(time.RFC3339); got != "2026-06-08T00:00:00Z" {
		t.Fatalf("reset %s", got)
	}
	if info.Billing == nil || info.Billing.OnDemandCapCents == nil || *info.Billing.OnDemandCapCents != 5000 {
		t.Fatalf("billing %+v", info.Billing)
	}
	if info.Billing.PrepaidBalanceCents == nil || *info.Billing.PrepaidBalanceCents != 1250 {
		t.Fatal("prepaid missing")
	}
	if info.Credits == nil || !info.Credits.HasCredits {
		t.Fatal("expected prepaid credits")
	}
}

func TestParseGrokBilling_PartialObservedShape(t *testing.T) {
	// The shape observed from `_x.ai/billing` when the included percentage
	// is absent: period, tier, on-demand and prepaid only.
	raw := []byte(`{
		"config": {
			"currentPeriod": {
				"type": "USAGE_PERIOD_TYPE_WEEKLY",
				"start": "2026-09-21T21:44:41Z",
				"end": "2026-09-28T21:44:41Z"
			},
			"onDemandCap": {"val": 0},
			"onDemandUsed": {"val": 0},
			"prepaidBalance": {"val": 0},
			"isUnifiedBillingUser": true,
			"billingPeriodStart": "2026-09-21T21:44:41Z",
			"billingPeriodEnd": "2026-09-28T21:44:41Z"
		},
		"subscription_tier": "SuperGrok Heavy"
	}`)
	info := parseGrokBilling(raw, time.Now())
	if info.QuotaStatus != QuotaDegraded {
		t.Fatalf("status %q, want degraded", info.QuotaStatus)
	}
	if info.Error != "" {
		t.Fatalf("error %q, want empty so the reset is still reported", info.Error)
	}
	if info.PrimaryWindow == nil || !info.PrimaryWindow.Unmeasured || info.PrimaryWindow.UsedPercent != 0 {
		t.Fatalf("window %+v", info.PrimaryWindow)
	}
	if info.NumericQuotaKnown() {
		t.Fatal("partial response was treated as a measured quota")
	}
	if info.AvailabilityScore() != 0 {
		t.Fatalf("score %d, want 0 rather than a perfect score", info.AvailabilityScore())
	}
	if info.PlanType != "SuperGrok Heavy" {
		t.Fatalf("plan %q", info.PlanType)
	}
	if info.Billing == nil || info.Billing.OnDemandCapCents == nil || *info.Billing.OnDemandCapCents != 0 {
		t.Fatal("a reported zero on-demand cap must be preserved")
	}
}

func TestParseGrokBilling_LegacyUsedOverLimit(t *testing.T) {
	raw := []byte(`{
		"config": {
			"monthlyLimit": {"val": 2000},
			"used": {"val": 500},
			"billingPeriodEnd": "2026-05-01T00:00:00Z"
		}
	}`)
	info := parseGrokBilling(raw, time.Now())
	if info.QuotaStatus != QuotaOK || info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 25 {
		t.Fatalf("status %q window %+v", info.QuotaStatus, info.PrimaryWindow)
	}
}

func TestParseGrokBilling_OutOfRangePercentIsNotClamped(t *testing.T) {
	raw := []byte(`{"config":{"creditUsagePercent":150,"billingPeriodEnd":"2026-05-01T00:00:00Z"}}`)
	info := parseGrokBilling(raw, time.Now())
	if info.QuotaStatus != QuotaDegraded || info.NumericQuotaKnown() {
		t.Fatalf("status %q known %v", info.QuotaStatus, info.NumericQuotaKnown())
	}
	if info.PrimaryWindow != nil && !info.PrimaryWindow.Unmeasured {
		t.Fatal("out-of-range percent became a measurement")
	}
}

func TestParseGrokBilling_MalformedAndEmpty(t *testing.T) {
	for _, raw := range []string{"", "not-json", "[]", `{"config":"nope"}`} {
		info := parseGrokBilling([]byte(raw), time.Now())
		if info.QuotaStatus != QuotaUnavailable || info.Error == "" {
			t.Fatalf("raw %q status %q err %q", raw, info.QuotaStatus, info.Error)
		}
		if info.PrimaryWindow != nil {
			t.Fatalf("malformed response invented a window: %+v", info.PrimaryWindow)
		}
	}
}

func TestParseGrokBilling_MissingConfigIsDegraded(t *testing.T) {
	info := parseGrokBilling([]byte(`{"subscription_tier":"SuperGrok"}`), time.Now())
	if info.QuotaStatus != QuotaDegraded || info.NumericQuotaKnown() {
		t.Fatalf("status %q", info.QuotaStatus)
	}
	if info.PlanType != "SuperGrok" {
		t.Fatalf("plan %q", info.PlanType)
	}
}

func TestGrokFetch_UnavailableWithoutAuth(t *testing.T) {
	info, err := NewGrokFetcher().Fetch(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.QuotaStatus != QuotaUnavailable || info.Error == "" {
		t.Fatalf("%+v", info)
	}
}

func TestGrokFetch_FakeACP(t *testing.T) {
	home := t.TempDir()
	auth := `{"https://auth.example/synthetic":{"key":"SYNTHETIC-NOT-A-TOKEN","email":"grok-tester@example.com"}}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	script := `#!/bin/sh
# Speaks just enough ACP to return one billing fixture. Records methods so
# the test can see that no model prompt was sent.
log="$CAAM_GROK_METHODS"
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  printf '%s\n' "$method" >> "$log"
  case "$method" in
    session/prompt)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32600,"message":"model call"}}\n' "$id"
      exit 1
      ;;
    _x.ai/billing)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"config":{"creditUsagePercent":10,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-06-01T00:00:00Z","end":"2026-06-08T00:00:00Z"},"prepaidBalance":{"val":0}},"subscription_tier":"Synthetic"}}\n' "$id"
      exit 0
      ;;
    *)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"synthetic-session"}}\n' "$id"
      ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(bin, "grok"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "methods")
	t.Setenv("CAAM_GROK_METHODS", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	info, err := NewGrokFetcher().Fetch(context.Background(), "grok-home:"+home)
	if err != nil {
		t.Fatal(err)
	}
	if info.QuotaStatus != QuotaOK || info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 10 {
		t.Fatalf("%+v window %+v", info, info.PrimaryWindow)
	}
	if info.AccountID != "grok-tester@example.com" {
		t.Fatalf("account %q", info.AccountID)
	}
	if info.PlanType != "Synthetic" {
		t.Fatalf("plan %q", info.PlanType)
	}
	methods, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(methods)
	for _, want := range []string{"initialize", "authenticate", "session/new", "_x.ai/billing"} {
		if !strings.Contains(text, want) {
			t.Fatalf("methods %q missing %s", text, want)
		}
	}
	if strings.Contains(text, "session/prompt") {
		t.Fatal("billing fetch sent a model prompt")
	}
	// The staged copy must not leave the source auth rewritten.
	got, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != auth {
		t.Fatal("source auth.json was modified")
	}
}

func TestGrokFetch_CLIFailureIsUnavailable(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"email":"a@example.com"}`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "grok"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	info, err := NewGrokFetcher().Fetch(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() {
		t.Fatalf("%+v", info)
	}
}
