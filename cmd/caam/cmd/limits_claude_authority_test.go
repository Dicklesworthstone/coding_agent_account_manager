package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestLimitsClaudeVaultCredentialAuthority(t *testing.T) {
	const modern = `{"claudeAiOauth":{"accessToken":"selected-A","expiresAt":4102444800000}}`
	const legacy = `{"oauthToken":"legacy-B","expires_at":"2099-01-01T00:00:00Z","refresh_token":"ignored-renewal"}`
	for _, tc := range []struct {
		name, path, data string
		usable, modern   bool
	}{
		{name: "flat auth precedes legacy", path: "auth.json", usable: true,
			data: `{"access_token":"selected-A","expires_at":"2099-01-01T00:00:00Z"}`},
		{name: "flat auth ignores embedded grant", path: "auth.json", usable: true,
			data: `{"access_token":"selected-A","expires_at":"2099-01-01T00:00:00Z","claudeAiOauth":{"accessToken":"legacy-B","expiresAt":1}}`},
		{name: "malformed modern", path: ".credentials.json", data: "{"},
		{name: "modern without access token", path: ".credentials.json", data: `{"claudeAiOauth":{"refreshToken":"selected-renewal"}}`},
		{name: "malformed auth", path: "auth.json", data: "{"},
		{name: "API key auth", path: "auth.json", data: `{"apiKey":"selected-api-key"}`},
		{name: "malformed settings", path: "settings.json", data: "{", modern: true},
		{name: "helper settings", path: "settings.json", data: `{"apiKeyHelper":"synthetic-helper"}`, modern: true},
		{name: "gateway settings", path: "settings.json", data: `{"env":{"ANTHROPIC_BASE_URL":"https://gateway.example.invalid"}}`, modern: true},
		{name: "cloud settings", path: "settings.json", data: `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`, modern: true},
	} {
		for _, source := range []string{"", credNamespaceVault} {
			t.Run(fmt.Sprintf("%s/source=%q", tc.name, source), func(t *testing.T) {
				// Borrowing the ignored login's 0% quota would change the winner.
				setupSubscriptionQuotaProfiles(t, "claude", map[string]string{
					"selected-A": subscriptionQuotaResponse("claude", 100),
					"legacy-B":   subscriptionQuotaResponse("claude", 0),
					"available":  subscriptionQuotaResponse("claude", 40),
				})
				f := newNamespaceFixture(t)
				f.lookup.VaultDir = getVaultDir()
				f.isolatedClaude(t, "seat", time.Now().Add(time.Hour), true)
				oldProfiles := profileStore
				profileStore = f.store
				t.Cleanup(func() { profileStore = oldProfiles })
				dir := filepath.Join(getVaultDir(), "claude", "seat")
				writeQuotaSelectionFile(t, filepath.Join(dir, ".claude.json"), legacy)
				if tc.modern {
					writeQuotaSelectionFile(t, filepath.Join(dir, ".credentials.json"), modern)
				}
				writeQuotaSelectionFile(t, filepath.Join(dir, tc.path), tc.data)
				writeQuotaSelectionFile(t, filepath.Join(getVaultDir(), "claude", "available", ".credentials.json"),
					`{"claudeAiOauth":{"accessToken":"available","expiresAt":4102444800000}}`)
				before := captureQuotaSelectionTree(t, getVaultDir())
				isolatedBefore := captureQuotaSelectionTree(t, f.root)
				transport := http.DefaultTransport
				var requestMu sync.Mutex
				requests := make(map[string]int)
				http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
					token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
					requestMu.Lock()
					requests[token]++
					requestMu.Unlock()
					return transport.RoundTrip(req)
				})
				cmd := quotaAdviceCommand("best", "", false)
				if err := cmd.Flags().Set("source", source); err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				if err := runLimits(cmd, []string{"claude"}); err != nil {
					t.Fatal(err)
				}
				var best *usage.ProfileUsage
				if err := json.Unmarshal(output.Bytes(), &best); err != nil {
					t.Fatal(err)
				}
				if best == nil || best.ProfileName != "available" {
					t.Fatalf("borrowed another login's capacity: %s", output.String())
				}
				// A healthier isolated alternative must not hide invalid vault auth.
				output.Reset()
				cmd = quotaAdviceCommand("", "", false)
				for name, value := range map[string]string{"source": source, "profile": "seat"} {
					if err := cmd.Flags().Set(name, value); err != nil {
						t.Fatal(err)
					}
				}
				var selectedError string
				if !tc.usable {
					candidate := f.lookup.inspect(credNamespaceVault, "claude", "seat")
					if candidate.Err == nil || candidate.State != credStateInvalid || candidate.Token != "" {
						t.Fatalf("invalid selected source was discarded: %+v", candidate)
					}
					selectedError = candidate.Err.Error()
					if err := cmd.Flags().Set("format", "table"); err != nil {
						t.Fatal(err)
					}
				}
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				if err := runLimits(cmd, []string{"claude"}); err != nil {
					t.Fatal(err)
				}
				if tc.usable {
					var rows []usage.ProfileUsage
					if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
						t.Fatal(err)
					}
					if len(rows) != 1 || rows[0].ProfileName != "seat" || rows[0].Usage == nil ||
						rows[0].Usage.Error != "" || rows[0].Usage.SecondaryWindow == nil ||
						rows[0].Usage.SecondaryWindow.UsedPercent != 100 {
						t.Fatalf("named read used another quota account: %s", output.String())
					}
					got := rows[0].CredentialSource
					if got == nil || got.Namespace != credNamespaceVault || got.Path != filepath.Join(dir, "auth.json") ||
						got.State != credStateHealthy || got.Explicit != (source != "") {
						t.Fatalf("named read reported another credential source: %+v", got)
					}
				} else if !strings.Contains(output.String(), selectedError) {
					t.Fatalf("named read lost the selected-source error: %s", output.String())
				}
				wantSelected, wantAccounts := 0, 1
				if tc.usable {
					wantSelected, wantAccounts = 2, 2
				}
				requestMu.Lock()
				selectedCalls, ignoredCalls, availableCalls, accounts :=
					requests["selected-A"], requests["legacy-B"], requests["available"], len(requests)
				requestMu.Unlock()
				if selectedCalls != wantSelected || ignoredCalls != 0 || availableCalls != 1 || accounts != wantAccounts {
					t.Fatalf("wrong credential requests: selected=%d ignored=%d available=%d accounts=%d", selectedCalls, ignoredCalls, availableCalls, accounts)
				}
				assertQuotaSelectionTree(t, getVaultDir(), before)
				assertQuotaSelectionTree(t, f.root, isolatedBefore)
			})
		}
	}
}

func TestResolveVaultClaudeStateOnlyLegacyIsMissing(t *testing.T) {
	f := newNamespaceFixture(t)
	writeQuotaSelectionFile(t, filepath.Join(f.vaultDir, "claude", "work", ".claude.json"),
		`{"oauthAccount":{"accountUuid":"metadata-only"},"projects":{},"cachedUsageUtilization":{"utilization":{"seven_day":{"utilization":0}}}}`)
	f.isolatedClaude(t, "work", f.lookup.Now.Add(time.Hour), true)
	before := captureQuotaSelectionTree(t, f.root)
	candidate := f.lookup.inspect(credNamespaceVault, "claude", "work")
	if candidate.State != credStateMissing || candidate.Err != nil || candidate.Token != "" {
		t.Fatalf("state-only legacy document became an invalid login: %+v", candidate)
	}
	resolved, err := resolveProfileCredential(f.lookup, "claude", "work", "")
	if err != nil || resolved == nil || resolved.Selected.Namespace != credNamespaceIsolated {
		t.Fatalf("state-only vault document blocked the isolated login: %+v, %v", resolved, err)
	}
	if resolved, err := resolveProfileCredential(f.lookup, "claude", "work", credNamespaceVault); err == nil || resolved != nil {
		t.Fatalf("explicit vault request borrowed the isolated login: %+v, %v", resolved, err)
	}
	assertQuotaSelectionTree(t, f.root, before)
}

func TestResolveVaultClaudeExpiryBelongsToSelectedCredential(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, filename := range []string{".credentials.json", "auth.json"} {
		for _, tc := range []struct {
			name, state     string
			expires         time.Time
			renewable       bool
			relativeSeconds int
		}{
			{name: "missing expiry", state: credStateUnknown},
			{name: "relative expiry without issuance", state: credStateUnknown, relativeSeconds: 3600},
			{name: "expired nonrenewable", state: credStateExpired, expires: now.Add(-time.Hour)},
			{name: "expired renewable", state: credStateHealthy, expires: now.Add(-time.Hour), renewable: true},
		} {
			t.Run(filename+"/"+tc.name, func(t *testing.T) {
				f := newNamespaceFixture(t)
				f.lookup.Now = now
				dir := filepath.Join(f.vaultDir, "claude", "work")
				grant := map[string]any{"accessToken": "selected-A"}
				if !tc.expires.IsZero() {
					grant["expiresAt"] = tc.expires.UnixMilli()
				}
				if tc.renewable {
					grant["refreshToken"] = "selected-renewal"
				}
				if tc.relativeSeconds != 0 {
					grant["expires_in"] = tc.relativeSeconds
				}
				var document any = grant
				if filename == ".credentials.json" {
					document = map[string]any{"claudeAiOauth": grant}
				}
				data, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				selectedPath := filepath.Join(dir, filename)
				writeQuotaSelectionFile(t, selectedPath, string(data))
				writeQuotaSelectionFile(t, filepath.Join(dir, ".claude.json"),
					`{"oauthToken":"legacy-B","expires_at":"2099-01-01T00:00:00Z","refresh_token":"ignored-renewal"}`)
				f.isolatedClaude(t, "work", now.Add(time.Hour), true)
				before := captureQuotaSelectionTree(t, f.root)
				resolved, err := resolveProfileCredential(f.lookup, "claude", "work", "")
				if err != nil || resolved == nil {
					t.Fatalf("resolve: %+v, %v", resolved, err)
				}
				got := resolved.Selected
				if got.Namespace != credNamespaceVault || got.Path != selectedPath || got.Token != "selected-A" ||
					got.State != tc.state || !got.ExpiresAt.Equal(tc.expires) {
					t.Fatalf("token and expiry came from different sources: %+v", got)
				}
				wantHealthier := 1
				if tc.state == credStateHealthy {
					wantHealthier = 0
				}
				if len(resolved.Healthier) != wantHealthier || wantHealthier == 1 && resolved.Healthier[0].Namespace != credNamespaceIsolated {
					t.Fatalf("borrowed expiry changed namespace ambiguity: %+v", resolved.Healthier)
				}
				assertQuotaSelectionTree(t, f.root, before)
			})
		}
	}
}
