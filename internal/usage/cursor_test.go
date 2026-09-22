package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures are synthetic. No real Cursor token, auth id, or account.

func TestParseCursorUsage_GrantsAndReset(t *testing.T) {
	raw := []byte(`{
		"usageLimitPolicyStatus": {
			"stage": "LIMIT_HIT_STAGE_SLOW_POOL",
			"resetAtMs": "1780000000000",
			"isInSlowPool": true,
			"allowedModelIds": ["composer-2"]
		},
		"activeGrants": [{
			"grantType": "included",
			"totalCents": "10000",
			"remainingCents": 2500,
			"expiresAtMs": 1780000000000,
			"allowedModelIds": ["composer-2"]
		}]
	}`)
	info := parseCursorUsage(raw, time.Unix(1_700_000_000, 0).UTC())
	if info.QuotaStatus != QuotaOK {
		t.Fatalf("status %q note %q", info.QuotaStatus, info.QuotaNote)
	}
	if info.LimitStage != "SLOW_POOL" {
		t.Fatalf("stage %q", info.LimitStage)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.Unmeasured || info.PrimaryWindow.UsedPercent != 75 {
		t.Fatalf("window %+v", info.PrimaryWindow)
	}
	if info.PrimaryWindow.ResetsAt.UnixMilli() != 1780000000000 {
		t.Fatalf("reset %s", info.PrimaryWindow.ResetsAt)
	}
	if len(info.Grants) < 2 || info.Grants[1].RemainingCents == nil || *info.Grants[1].RemainingCents != 2500 {
		t.Fatalf("grants %+v", info.Grants)
	}
}

func TestParseCursorUsage_StageOnly(t *testing.T) {
	raw := []byte(`{"usageLimitPolicyStatus":{"stage":2,"resetAtMs":1780000000000}}`)
	info := parseCursorUsage(raw, time.Now())
	if info.QuotaStatus != QuotaDegraded || info.NumericQuotaKnown() {
		t.Fatalf("status %q known %v", info.QuotaStatus, info.NumericQuotaKnown())
	}
	if info.LimitStage != "SLOW_POOL" {
		t.Fatalf("stage %q", info.LimitStage)
	}
	if info.PrimaryWindow == nil || !info.PrimaryWindow.Unmeasured {
		t.Fatalf("window %+v", info.PrimaryWindow)
	}
	if info.AvailabilityScore() != 0 {
		t.Fatal("stage-only row scored as spare capacity")
	}
}

func TestParseCursorUsage_SparseObservedShape(t *testing.T) {
	// Fields the live client returned for an account that is not in a limit
	// stage: spend-limit flags only, int64 as a string, no reset and no grants.
	raw := []byte(`{
		"usageLimitPolicyStatus": {
			"canConfigureSpendLimit": true,
			"recommendedOnDemandLimitCents": "10000"
		}
	}`)
	info := parseCursorUsage(raw, time.Now())
	if info.QuotaStatus != QuotaDegraded || info.NumericQuotaKnown() || info.LimitStage != "" {
		t.Fatalf("%+v", info)
	}
	if info.PrimaryWindow != nil {
		t.Fatal("sparse response invented a window")
	}
}

func TestParseCursorUsage_Malformed(t *testing.T) {
	for _, raw := range []string{"", "not-json", "[]", `{"usageLimitPolicyStatus":"nope"}`} {
		info := parseCursorUsage([]byte(raw), time.Now())
		if raw == `{"usageLimitPolicyStatus":"nope"}` {
			// An object that simply lacks the fields we understand is degraded,
			// not a transport failure.
			if info.QuotaStatus != QuotaDegraded || info.NumericQuotaKnown() {
				t.Fatalf("raw %q status %q", raw, info.QuotaStatus)
			}
			continue
		}
		if info.QuotaStatus != QuotaUnavailable || info.Error == "" || info.PrimaryWindow != nil {
			t.Fatalf("raw %q -> %+v", raw, info)
		}
	}
}

func TestCursorFetch_TwoProfilesAndNoGlobalLeak(t *testing.T) {
	global := t.TempDir()
	if err := os.MkdirAll(filepath.Join(global, "cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	globalTok := "synthetic-global-token"
	if err := os.WriteFile(filepath.Join(global, "cursor", "auth.json"), []byte(`{"accessToken":"`+globalTok+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", global)
	t.Setenv("HOME", t.TempDir())

	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cursorUsagePath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			http.Error(w, "protocol", http.StatusBadRequest)
			return
		}
		authz := r.Header.Get("Authorization")
		seen[authz]++
		var reset, stage string
		switch {
		case strings.HasSuffix(authz, "synthetic-token-a"):
			reset = "1780000000000"
			stage = "LIMIT_HIT_STAGE_UNSPECIFIED"
		case strings.HasSuffix(authz, "synthetic-token-b"):
			reset = "1790000000000"
			stage = "LIMIT_HIT_STAGE_HARD_BLOCK"
		default:
			http.Error(w, "unexpected credential", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usageLimitPolicyStatus":{"stage":"` + stage + `","resetAtMs":"` + reset + `"}}`))
	}))
	defer srv.Close()

	f := &CursorFetcher{BaseURL: srv.URL, ClientVersion: "cli-test", HTTP: srv.Client()}
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		token string
		stage string
		reset int64
	}{
		{"a", "synthetic-token-a", "", 1780000000000},
		{"b", "synthetic-token-b", "HARD_BLOCK", 1790000000000},
	} {
		root := t.TempDir()
		dir := filepath.Join(root, "xdg_config", "cursor")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		body := `{"accessToken":"` + tc.token + `","refreshToken":"synthetic-refresh"}`
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		info := `{"authInfo":{"email":"` + tc.name + `@example.com"}}`
		if err := os.WriteFile(filepath.Join(dir, "cli-config.json"), []byte(info), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := f.Fetch(ctx, "cursor-root:"+root)
		if err != nil {
			t.Fatal(err)
		}
		if got.QuotaStatus != QuotaDegraded || got.NumericQuotaKnown() {
			t.Fatalf("%s status %+v", tc.name, got)
		}
		if got.LimitStage != tc.stage {
			t.Fatalf("%s stage %q", tc.name, got.LimitStage)
		}
		if got.PrimaryWindow == nil || got.PrimaryWindow.ResetsAt.UnixMilli() != tc.reset || !got.PrimaryWindow.Unmeasured {
			t.Fatalf("%s window %+v", tc.name, got.PrimaryWindow)
		}
		if got.AccountID != tc.name+"@example.com" {
			t.Fatalf("%s account %q", tc.name, got.AccountID)
		}
	}
	if seen["Bearer "+globalTok] != 0 {
		t.Fatal("fetch presented the machine-global Cursor token")
	}
	if len(seen) != 2 {
		t.Fatalf("saw %d distinct credentials, want 2", len(seen))
	}
}

func TestCursorFetch_HTTPFailureIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xdg-auth.json"), []byte(`{"accessToken":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	f := &CursorFetcher{BaseURL: srv.URL, ClientVersion: "cli-test", HTTP: srv.Client()}
	info, err := f.Fetch(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() || !strings.Contains(info.Error, "502") {
		t.Fatalf("%+v", info)
	}
}

func TestCursorFetch_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"accessToken":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	f := &CursorFetcher{BaseURL: srv.URL, ClientVersion: "cli-test", HTTP: srv.Client()}
	info, err := f.Fetch(context.Background(), "cursor-root:"+filepath.Join(dir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.QuotaStatus != QuotaUnavailable || info.PrimaryWindow != nil {
		t.Fatalf("%+v", info)
	}
}
