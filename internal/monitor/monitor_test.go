package monitor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestMonitorCursorHealthTracksCredentialReplacement(t *testing.T) {
	root := t.TempDir()
	vault := authfile.NewVault(filepath.Join(root, "custom-vault"))
	store := health.NewStorage(filepath.Join(root, "other-metadata", "health.json"))
	mon := NewMonitor(WithVault(vault), WithHealthStore(store), WithProviders([]string{"cursor"}), WithFetcher(&fakeFetcher{}))
	for _, tc := range []struct {
		name   string
		ttl    time.Duration
		apiKey string
		want   health.HealthStatus
	}{
		{name: "session warning", ttl: 5 * 24 * time.Hour, want: health.StatusWarning},
		{name: "session expired", ttl: -time.Hour, want: health.StatusCritical},
		{name: "replaced with API key", ttl: -time.Hour, apiKey: "synthetic-key", want: health.StatusHealthy},
		{name: "new session", ttl: 30 * 24 * time.Hour, want: health.StatusHealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(tc.ttl).Unix())))
			body := fmt.Sprintf(`{"accessToken":%q,"apiKey":%q}`, "e30."+payload+".synthetic", tc.apiKey)
			writeProfileFile(t, vault, "cursor", "work", "auth.json", body)
			if err := mon.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			state := mon.GetProfile("cursor", "work")
			if state == nil || state.Health != tc.want {
				t.Fatalf("monitor health=%+v, want %s", state, tc.want)
			}
		})
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Errorf("monitor health read created metadata: %v", err)
	}
}

type fakeFetcher struct {
	usages map[string]*usage.UsageInfo
}

func (f *fakeFetcher) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []usage.ProfileUsage {
	results := make([]usage.ProfileUsage, 0, len(profiles))
	for name := range profiles {
		key := provider + "/" + name
		info := f.usages[key]
		if info == nil {
			info = &usage.UsageInfo{
				Provider:    provider,
				ProfileName: name,
				Error:       "missing usage",
				FetchedAt:   time.Now(),
			}
		}
		results = append(results, usage.ProfileUsage{
			Provider:    provider,
			ProfileName: name,
			Usage:       info,
		})
	}
	return results
}

func TestMonitorRefreshBuildsState(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	writeProfileFile(t, vault, "claude", "alice", ".credentials.json", `{"claudeAiOauth":{"accessToken":"tok-claude"}}`)
	writeProfileFile(t, vault, "codex", "bob", "auth.json", `{"tokens":{"access_token":"tok-codex"}}`)

	fetcher := &fakeFetcher{
		usages: map[string]*usage.UsageInfo{
			"claude/alice": {
				Provider:    "claude",
				ProfileName: "alice",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: 80,
				},
			},
			"codex/bob": {
				Provider:    "codex",
				ProfileName: "bob",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: 20,
				},
			},
		},
	}

	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(fetcher),
		WithProviders([]string{"claude", "codex"}),
		WithHealthStore(nil),
	)

	if err := mon.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	state := mon.GetState()
	if state == nil {
		t.Fatal("GetState() returned nil")
	}
	if len(state.Profiles) != 2 {
		t.Fatalf("Profiles count = %d, want 2", len(state.Profiles))
	}

	claude := state.Profiles["claude/alice"]
	if claude == nil {
		t.Fatal("missing claude/alice profile")
	}
	if claude.Alert == nil || claude.Alert.Type != AlertWarning {
		t.Fatalf("claude alert = %v, want warning", claude.Alert)
	}

	codex := state.Profiles["codex/bob"]
	if codex == nil {
		t.Fatal("missing codex/bob profile")
	}
	if codex.Alert != nil {
		t.Fatalf("codex alert = %v, want nil", codex.Alert)
	}
}

type monitorQuotaTransport func(*http.Request) (*http.Response, error)

func (f monitorQuotaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Exercise real fetcher errors and decoding through Refresh and every renderer.
// HTTP failures can return an Error without assigning a QuotaStatus; they must
// remain unknown just like malformed or incomplete successful responses.
func TestMonitorRefreshQuotaMeasurements(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		body       string
		status     int
		networkErr bool
		known      bool
		percent    float64
		model      bool
		credit     bool
	}{
		{name: "claude unauthorized", provider: "claude", status: http.StatusUnauthorized},
		{name: "claude unavailable", provider: "claude", status: http.StatusServiceUnavailable},
		{name: "claude network failure", provider: "claude", networkErr: true},
		{name: "codex unauthorized", provider: "codex", status: http.StatusUnauthorized},
		{name: "codex unavailable", provider: "codex", status: http.StatusServiceUnavailable},
		{name: "codex network failure", provider: "codex", networkErr: true},
		{name: "claude null document", provider: "claude", body: `null`},
		{name: "claude missing measurements", provider: "claude", body: `{}`},
		{name: "claude malformed measurement", provider: "claude", body: `{"seven_day":{"utilization":true}}`},
		{name: "claude model only", provider: "claude", body: `{"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`, model: true},
		{name: "claude weekly zero without session", provider: "claude", body: `{"seven_day":{"utilization":0}}`, known: true},
		{name: "claude null session", provider: "claude", body: `{"five_hour":null,"seven_day":{"utilization":52.5}}`, known: true, percent: 52.5},
		{name: "claude null session measurement", provider: "claude", body: `{"five_hour":{"utilization":null},"seven_day":{"utilization":100}}`, known: true, percent: 100},
		{name: "claude null session entry", provider: "claude", body: `{"limits":[{"kind":"session","percent":null},{"kind":"weekly_all","percent":0}]}`, known: true},
		{name: "claude session limited", provider: "claude", body: `{"five_hour":{"utilization":42},"seven_day":{"utilization":10}}`, known: true, percent: 42},
		{name: "claude unknown general limit", provider: "claude", body: `{"seven_day":{"utilization":0},"limits":[{"kind":"future_general","percent":50}]}`},
		{name: "claude measured model zero", provider: "claude", body: `{"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Fable"}}}]}`, known: true, model: true},
		{name: "claude exhausted model", provider: "claude", body: `{"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`, known: true, percent: 100, model: true},
		{name: "codex null measurement", provider: "codex", body: `{"rate_limit":{"primary_window":{"used_percent":null}}}`},
		{name: "codex NaN credits", provider: "codex", body: `{"credits":{"has_credits":true,"balance":"NaN"}}`},
		{name: "codex infinite credits", provider: "codex", body: `{"credits":{"has_credits":true,"balance":"+Inf"}}`},
		{name: "codex negative infinite credits with quota", provider: "codex", body: `{"rate_limit":{"secondary_window":{"used_percent":0}},"credits":{"has_credits":true,"balance":"-Infinity"}}`},
		{name: "codex credits only", provider: "codex", body: `{"credits":{"has_credits":true,"balance":"12.50"}}`, credit: true},
		{name: "codex weekly zero with credits", provider: "codex", body: `{"rate_limit":{"secondary_window":{"used_percent":0,"limit_window_seconds":604800}},"credits":{"has_credits":true,"balance":"12.50"}}`, known: true, credit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vault := authfile.NewVault(t.TempDir())
			if tc.provider == "claude" {
				writeProfileFile(t, vault, tc.provider, "work", ".credentials.json", `{"claudeAiOauth":{"accessToken":"tok-test"}}`)
			} else {
				writeProfileFile(t, vault, tc.provider, "work", "auth.json", `{"tokens":{"access_token":"tok-test"}}`)
			}

			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			requests := 0
			http.DefaultTransport = monitorQuotaTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.Method != http.MethodGet || req.Header.Get("Authorization") != "Bearer tok-test" {
					t.Errorf("unexpected usage request: method=%s authorization=%q", req.Method, req.Header.Get("Authorization"))
				}
				if tc.networkErr {
					return nil, fmt.Errorf("synthetic network failure")
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})

			mon := NewMonitor(WithVault(vault), WithProviders([]string{tc.provider}), WithHealthStore(nil))
			if err := mon.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			if requests != 1 {
				t.Fatalf("usage requests=%d, want 1", requests)
			}
			state := mon.GetState()
			profile := state.Profiles[tc.provider+"/work"]
			if profile == nil || profile.Usage == nil {
				t.Fatalf("missing fetched usage: %+v", profile)
			}
			if (profile.Usage.Error == "") != tc.known {
				t.Fatalf("usage error=%q, known=%v", profile.Usage.Error, tc.known)
			}
			if (tc.status != 0 || tc.networkErr) && profile.Usage.QuotaStatus != "" {
				t.Fatalf("expected the early-failure shape without quota status: %+v", profile.Usage)
			}

			brief := NewBriefRenderer().Render(state)
			table := NewTableRenderer().Render(state)
			if tc.known {
				if !strings.HasPrefix(brief, fmt.Sprintf("%s:%.0f%%", tc.provider, tc.percent)) || !strings.Contains(table, fmt.Sprintf("%3.0f%%", tc.percent)) {
					t.Errorf("measured percent=%v missing from brief=%q or table:\n%s", tc.percent, brief, table)
				}
			} else if brief != tc.provider+":?" || !strings.Contains(table, "no usage:") || strings.Contains(table, "  0%") {
				t.Errorf("unknown quota misrepresented: brief=%q table:\n%s", brief, table)
			}

			var payload struct {
				Profiles []map[string]json.RawMessage `json:"profiles"`
			}
			if err := json.Unmarshal([]byte(NewJSONRenderer(false).Render(state)), &payload); err != nil {
				t.Fatalf("decode rendered JSON: %v", err)
			}
			if len(payload.Profiles) != 1 {
				t.Fatalf("JSON profiles=%d, want 1", len(payload.Profiles))
			}
			rawPercent, present := payload.Profiles[0]["usage_percent"]
			if present != tc.known {
				t.Fatalf("usage_percent present=%v (%s), known=%v", present, rawPercent, tc.known)
			}
			if tc.known {
				var percent float64
				if string(rawPercent) == "null" {
					t.Fatal("measured zero must be serialized as a number, not null")
				}
				if err := json.Unmarshal(rawPercent, &percent); err != nil || percent != tc.percent {
					t.Errorf("JSON percent=%v, error=%v; want %v", percent, err, tc.percent)
				}
			}
			var nested usage.UsageInfo
			if err := json.Unmarshal(payload.Profiles[0]["usage"], &nested); err != nil {
				t.Fatalf("decode nested usage: %v", err)
			}
			if nested.Error != profile.Usage.Error || (nested.SecondaryWindow == nil) != (profile.Usage.SecondaryWindow == nil) {
				t.Errorf("nested usage lost provider details: %+v", nested)
			}
			if tc.model && (nested.ModelWindows["Fable"] == nil || len(nested.ScopedWindows) == 0) {
				t.Errorf("nested usage lost model restrictions: %+v", nested)
			}
			if tc.credit {
				if nested.Credits == nil || nested.Credits.Balance == nil || *nested.Credits.Balance != 12.5 {
					t.Errorf("nested usage lost credit balance: %+v", nested.Credits)
				}
				if !tc.known && !strings.Contains(table, "Credit balance: 12.5") {
					t.Errorf("table lost credit balance:\n%s", table)
				}
			}
			alerts := NewAlertRenderer(80).Render(state)
			if tc.known && tc.percent == 100 {
				if profile.Alert == nil || profile.Alert.Type != AlertExhausted || !strings.Contains(alerts, "SWITCH RECOMMENDED") {
					t.Errorf("exhausted quota missing alert: %+v, %q", profile.Alert, alerts)
				}
			} else if profile.Alert != nil || alerts != "" {
				t.Errorf("unexpected quota alert: %+v, %q", profile.Alert, alerts)
			}
		})
	}
}

func TestMonitorRefreshUnsupportedProvider(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)
	writeProfileDir(t, vault, "gemini", "carol")

	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(&fakeFetcher{}),
		WithProviders([]string{"gemini"}),
		WithHealthStore(nil),
	)

	if err := mon.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	state := mon.GetState()
	profile := state.Profiles["gemini/carol"]
	if profile == nil || profile.Usage == nil || profile.Usage.Error == "" {
		t.Fatalf("expected usage error for gemini profile, got %+v", profile)
	}
}

func TestMonitorRefreshClaudeFallbackAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	writeProfileFile(t, vault, "claude", "legacy", ".claude.json", `{"oauthToken":"tok-legacy"}`)

	fetcher := &fakeFetcher{
		usages: map[string]*usage.UsageInfo{
			"claude/legacy": {
				Provider:    "claude",
				ProfileName: "legacy",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: 10,
				},
			},
		},
	}

	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(fetcher),
		WithProviders([]string{"claude"}),
		WithHealthStore(nil),
	)

	if err := mon.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	state := mon.GetState()
	profile := state.Profiles["claude/legacy"]
	if profile == nil || profile.Usage == nil || profile.Usage.Error != "" {
		t.Fatalf("expected legacy claude profile usage, got %+v", profile)
	}
}

func writeProfileFile(t *testing.T, vault *authfile.Vault, provider, profile, name, contents string) {
	t.Helper()
	dir := vault.ProfilePath(provider, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func writeProfileDir(t *testing.T, vault *authfile.Vault, provider, profile string) {
	t.Helper()
	dir := vault.ProfilePath(provider, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
}
