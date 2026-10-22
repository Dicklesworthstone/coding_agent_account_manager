package cmd

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	cursorprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/cursor"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

func TestDoctorValidatesIsolatedCursorCredentials(t *testing.T) {
	setupCodexVerificationVault(t)
	profileStore = profile.NewStore(t.TempDir())
	oldRegistry := registry
	t.Cleanup(func() { registry = oldRegistry })
	registry = provider.NewRegistry()
	registry.Register(cursorprovider.New())
	for _, tc := range []struct {
		name, auth, want string
		loggedIn         bool
	}{
		{"expired", string(cursorHealthCredential(t, time.Now().Add(-time.Hour), false)), "fail", false},
		{"near_expiry", string(cursorHealthCredential(t, time.Now().Add(3*24*time.Hour), false)), "warn", true},
		{"fresh", string(cursorHealthCredential(t, time.Now().Add(30*24*time.Hour), false)), "pass", true},
		{"renewable", string(cursorHealthCredential(t, time.Now().Add(-time.Hour), true)), "pass", true},
		{"empty", `{}`, "fail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof, err := profileStore.Create("cursor", tc.name, "oauth")
			if err != nil {
				t.Fatal(err)
			}
			env, err := cursorprovider.New().Env(context.Background(), prof)
			if err != nil {
				t.Fatal(err)
			}
			paths := authfile.ResolveCursorPaths(prof.HomePath(), runtime.GOOS, func(key string) string { return env[key] })
			writeNativeTestCredential(t, paths.AuthFile, tc.auth)
			writeNativeTestCredential(t, filepath.Join(paths.ConfigDir, "cli-config.json"), `{"authInfo":{"email":"stale@example.invalid"}}`)
			found := false
			for _, result := range checkTokenValidation() {
				if result.Name != "cursor/"+tc.name {
					continue
				}
				found = true
				if result.Status != tc.want {
					t.Fatalf("doctor result = %+v, want %s", result, tc.want)
				}
				if tc.name == "near_expiry" && (!strings.Contains(result.Details, "caam login cursor") || strings.Contains(result.Details, "caam refresh")) {
					t.Fatalf("long-lead warning omitted relogin action: %+v", result)
				}
			}
			if !found {
				t.Fatal("doctor omitted the isolated Cursor profile")
			}
			output, err := captureStdout(t, func() error { return profileStatusCmd.RunE(profileStatusCmd, []string{"cursor", tc.name}) })
			if err != nil || !strings.Contains(output, fmt.Sprintf("Logged in: %v", tc.loggedIn)) {
				t.Fatalf("profile status = %q, %v", output, err)
			}
			switch tc.name {
			case "expired":
				if !strings.Contains(output, "Expires:") || !strings.Contains(output, "log in again") || !strings.Contains(output, "caam login cursor expired") || strings.Contains(output, "caam refresh") {
					t.Fatalf("expired status omitted expiry or relogin advice: %q", output)
				}
			case "near_expiry":
				if !strings.Contains(output, "Expires:") || !strings.Contains(output, "Action: Log in again") || !strings.Contains(output, "caam login cursor near_expiry") {
					t.Fatalf("near-expiry status omitted long-lead relogin action: %q", output)
				}
			case "renewable":
				if strings.Contains(output, "Expires:") || strings.Contains(output, "Action:") || strings.Contains(strings.ToLower(output), "log in again") {
					t.Fatalf("renewable status presented a cached token as a login deadline: %q", output)
				}
			}
		})
	}
}

// Issue #78: doctor used to report every non-200 probe answer as "access token
// rejected by API" with refresh-token-reuse guidance, sending users into an
// unnecessary re-login on a 429 or 5xx. Issue #127 extends this to 403:
// permission or edge-proxy refusals are not proof of a rejected credential.
func TestClassifyCodexProbeError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  string
		wantMessage string
		wantDetail  string
	}{
		{
			name:        "401 fails",
			err:         &refresh.TokenVerifyError{StatusCode: 401, Attempts: 1},
			wantStatus:  "fail",
			wantMessage: "access token rejected by API (HTTP 401)",
			wantDetail:  "caam login codex work",
		},
		{
			name:        "403 warns without diagnosing the credential",
			err:         &refresh.TokenVerifyError{StatusCode: 403, Attempts: 1},
			wantStatus:  "warn",
			wantMessage: "could not verify token (unexpected HTTP 403)",
			wantDetail:  "neither an acceptance",
		},
		{
			name:        "wrapped 403 remains inconclusive",
			err:         fmt.Errorf("probe: %w", &refresh.TokenVerifyError{StatusCode: 403, Attempts: 2}),
			wantStatus:  "warn",
			wantMessage: "could not verify token (unexpected HTTP 403)",
			wantDetail:  "nor a credential rejection",
		},
		{
			name:        "429 warns and keeps the status",
			err:         &refresh.TokenVerifyError{StatusCode: 429, Attempts: 2},
			wantStatus:  "warn",
			wantMessage: "could not verify token (HTTP 429, transient)",
			wantDetail:  "after 2 attempt(s)",
		},
		{
			name:        "503 warns",
			err:         &refresh.TokenVerifyError{StatusCode: 503, Attempts: 2},
			wantStatus:  "warn",
			wantMessage: "could not verify token (HTTP 503, transient)",
			wantDetail:  "not a credential rejection",
		},
		{
			name:        "404 warns as unexpected",
			err:         &refresh.TokenVerifyError{StatusCode: 404, Attempts: 1},
			wantStatus:  "warn",
			wantMessage: "could not verify token (unexpected HTTP 404)",
			wantDetail:  "HTTP 404",
		},
		{
			name:        "wrapped verify error is still classified",
			err:         fmt.Errorf("probe: %w", &refresh.TokenVerifyError{StatusCode: 401, Attempts: 1}),
			wantStatus:  "fail",
			wantMessage: "access token rejected by API (HTTP 401)",
		},
		{
			name:        "transport failure warns",
			err:         fmt.Errorf("request failed: %w", errors.New("dial tcp: connection refused")),
			wantStatus:  "warn",
			wantMessage: "could not verify token (network error)",
			wantDetail:  "connection refused",
		},
		{
			name:        "timeout warns",
			err:         fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			wantStatus:  "warn",
			wantMessage: "could not verify token (network error)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCodexProbeError("codex", "work", tt.err)
			if got == nil {
				t.Fatal("expected a check result")
			}
			if got.Name != "codex/work token" {
				t.Errorf("Name = %q", got.Name)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if tt.wantDetail != "" && !strings.Contains(got.Details, tt.wantDetail) {
				t.Errorf("Details should contain %q, got %q", tt.wantDetail, got.Details)
			}
			// Refresh-token reuse can only be established by the token endpoint,
			// never by this probe, so it must not be asserted for non-rejections.
			if got.Status != "fail" && strings.Contains(got.Details, "refresh_token_reused") {
				t.Errorf("non-rejection must not diagnose refresh_token_reused: %q", got.Details)
			}
			if got.Status != "fail" && (strings.Contains(got.Details, "caam login") || strings.Contains(got.Details, "caam refresh")) {
				t.Errorf("inconclusive probe recommended changing credentials: %q", got.Details)
			}
		})
	}
}

// Exercise the real doctor probe, not only its error formatter. Unknown,
// accepted, and rejected saved health must ALL survive an inconclusive 403.
// The live and saved auth files may be in use by independent native processes.
func TestProbeVaultTokenForbiddenPreservesCredentialsAndHealth(t *testing.T) {
	for _, prior := range []string{"unknown", "accepted", "rejected"} {
		t.Run(prior, func(t *testing.T) {
			vaultDir, store := setupCodexVerificationVault(t)
			data := writeCodexVaultAuth(t, vaultDir, "work", "synthetic-refresh-token", time.Now().Add(24*time.Hour))
			liveRoot := t.TempDir()
			t.Setenv("CODEX_HOME", liveRoot)
			writeNativeTestCredential(t, filepath.Join(liveRoot, "auth.json"), string(data))
			if prior != "unknown" {
				verdict := health.ProviderVerification{
					Accepted: prior == "accepted", At: time.Now().Add(-time.Hour),
					Fingerprint: health.CodexCredentialFingerprint(data),
				}
				if prior == "rejected" {
					verdict.Reason = "refresh_token_invalidated"
				}
				if err := store.RecordProviderVerification("codex", "work", verdict); err != nil {
					t.Fatal(err)
				}
			}
			beforeHealth, err := store.GetProfile("codex", "work")
			if err != nil {
				t.Fatal(err)
			}
			beforeJSON, err := json.Marshal(beforeHealth)
			if err != nil {
				t.Fatal(err)
			}
			liveBefore, err := captureBackupTree(liveRoot)
			if err != nil {
				t.Fatal(err)
			}
			vaultBefore, err := captureBackupTree(vaultDir)
			if err != nil {
				t.Fatal(err)
			}
			var requests, refreshes int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.ContentLength > 0 {
					t.Error("diagnostic attempted a write or token exchange")
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("<html>edge-proxy-response-must-not-be-logged</html>"))
			}))
			defer server.Close()
			oldURL, oldRefresh := refresh.CodexVerifyURL, refresh.RefreshCodexToken
			refresh.CodexVerifyURL = server.URL
			refresh.RefreshCodexToken = func(context.Context, string) (*refresh.TokenResponse, error) {
				refreshes++
				return nil, errors.New("unexpected refresh")
			}
			defer func() { refresh.CodexVerifyURL, refresh.RefreshCodexToken = oldURL, oldRefresh }()

			result := probeVaultToken("codex", "work")
			if result == nil || result.Status != "warn" || !strings.Contains(result.Message, "403") || requests != 1 || refreshes != 0 {
				t.Fatalf("403 was not a read-only warning: result=%+v requests=%d refreshes=%d", result, requests, refreshes)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"caam login", "caam refresh", "refresh_token_reused", "edge-proxy-response-must-not-be-logged", "synthetic-refresh-token"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("unsafe doctor diagnostic contains %q", forbidden)
				}
			}
			afterHealth, err := store.GetProfile("codex", "work")
			if err != nil {
				t.Fatal(err)
			}
			afterJSON, err := json.Marshal(afterHealth)
			if err != nil || string(beforeJSON) != string(afterJSON) {
				t.Fatalf("inconclusive 403 changed provider verification: %v", err)
			}
			assertBackupTreeUnchanged(t, liveRoot, liveBefore)
			assertBackupTreeUnchanged(t, vaultDir, vaultBefore)
		})
	}
}

// Issue #127, after the probe moved to an endpoint that answers 401 for an
// invalid token: Codex rotates the ACTIVE profile's login in place, so its
// vault snapshot goes stale (caam writes the newer login back only when it
// switches away, internal/authfile/switch.go) and the snapshot's access
// token lapses while the live login is fresh. doctor judges
// the active profile's expiry from the live file, then probes the snapshot's
// lapsed access token and gets the expected 401. That 401 says nothing about
// the login, so it must not fail the check or advise a re-login, while a 401
// for an access token that is still live by its own claim still fails.
func TestDoctorStaleActiveCodexSnapshotIsNotARejectedLogin(t *testing.T) {
	jwt := func(exp time.Time) string {
		claims, err := json.Marshal(map[string]any{"email": "work@example.com", "exp": exp.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".SYNTHETIC"
	}
	codexAuth := func(exp time.Time, refreshToken string) string {
		return fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q}}`,
			jwt(exp), jwt(exp), refreshToken)
	}
	for _, tc := range []struct {
		name       string
		snapshot   time.Duration
		wantStatus string
	}{
		{"lapsed snapshot", -2 * time.Hour, "warn"},
		{"snapshot within clock skew", 2 * time.Minute, "warn"},
		{"live snapshot", 9 * 24 * time.Hour, "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultDir, store := setupCodexVerificationVault(t)
			liveRoot := t.TempDir()
			t.Setenv("CODEX_HOME", liveRoot)
			tools["codex"] = authfile.CodexAuthFiles
			snapshotExpiry := time.Now().Add(tc.snapshot)
			writeNativeTestCredential(t, filepath.Join(vaultDir, "codex", "work", "auth.json"),
				codexAuth(snapshotExpiry, "SYNTHETIC-SNAPSHOT-REFRESH"))
			writeNativeTestCredential(t, filepath.Join(liveRoot, "auth.json"),
				codexAuth(time.Now().Add(9*24*time.Hour), "SYNTHETIC-LIVE-REFRESH"))

			var probed []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probed = append(probed, r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			oldURL := refresh.CodexVerifyURL
			refresh.CodexVerifyURL = server.URL
			defer func() { refresh.CodexVerifyURL = oldURL }()

			var check *CheckResult
			for _, result := range checkAuthFiles() {
				if result.Name == "codex" && !strings.Contains(result.Message, "profile: work") {
					t.Fatalf("live login was not matched to the work profile: %+v", result)
				}
				if result.Name == "codex/work token" {
					check = &result
				}
			}
			if len(probed) != 1 || probed[0] != "Bearer "+jwt(snapshotExpiry) {
				t.Fatalf("doctor did not probe the snapshot's access token once: %q", probed)
			}
			if check == nil || check.Status != tc.wantStatus {
				t.Fatalf("doctor token check = %+v, want status %q", check, tc.wantStatus)
			}
			if tc.wantStatus == "fail" {
				return
			}
			encoded, err := json.Marshal(check)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"caam login", "caam refresh", "refresh_token_reused", "rejected"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("expected 401 for a lapsed snapshot diagnosed as a rejection (%q): %s", forbidden, encoded)
				}
			}
			if h, _ := store.GetProfile("codex", "work"); h != nil && !h.ProviderRejectedAt.IsZero() {
				t.Fatalf("lapsed snapshot's 401 was recorded as a provider rejection: %+v", h)
			}
		})
	}
}

func TestCheckDistributedWithoutAgentConfigIsSilent(t *testing.T) {
	if got := checkDistributed(context.Background(), filepath.Join(t.TempDir(), "none.json")); got != nil {
		t.Fatalf("checks = %+v, want no section without an agent config", got)
	}
}

func TestCheckDistributedReportsSetup(t *testing.T) {
	// A live coordinator (real API, scripted panes) and a dead one.
	coord := coordinator.New(coordinator.Config{PaneClient: &stubPanes{}, Logger: discardSlog()})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := coordinator.NewAPIServer(coord, "127.0.0.1", 0, discardSlog())
	go api.Serve(listener)
	defer api.Shutdown(context.Background())

	// The agent profile is signed in to Google.
	profileDir := t.TempDir()
	cookies := filepath.Join(profileDir, "Default", "Network", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookies), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cookies)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT, name TEXT); INSERT INTO cookies VALUES ('.google.com', 'SID')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	err = agent.WriteFileConfig(cfgPath, agent.FileConfig{
		ChromeProfile: profileDir,
		Coordinators: []*agent.CoordinatorEndpoint{
			{Name: "live", URL: "http://" + listener.Addr().String()},
			{Name: "dead", URL: "http://127.0.0.1:1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]CheckResult{}
	for _, c := range checkDistributed(context.Background(), cfgPath) {
		checks[c.Name] = c
	}
	for name, want := range map[string]string{
		"Agent config":     "pass",
		"Agent sign-ins":   "pass",
		"Coordinator live": "pass",
		"Coordinator dead": "fail",
	} {
		if got := checks[name]; got.Status != want {
			t.Errorf("%s = %+v, want %s", name, got, want)
		}
	}
	if !strings.Contains(checks["Coordinator live"].Message, "reachable via direct; stub") {
		t.Errorf("live coordinator message = %q", checks["Coordinator live"].Message)
	}
	if _, ok := checks["Agent service"]; !ok {
		t.Error("no agent service check")
	}
}

func TestCoordinatorCheckFlagsVersionSkew(t *testing.T) {
	ep := &agent.CoordinatorEndpoint{Name: "c", URL: "http://127.0.0.1:7890", SSH: &agent.SSHTunnel{Host: "build1"}}
	got := coordinatorCheck(ep, agent.CoordinatorProbe{Healthy: true, Backend: "tmux", Version: "v1.0.0"}, "v1.2.0")
	if got.Status != "warn" || !strings.Contains(got.Message, "v1.0.0") || !strings.Contains(got.Details, "caam update --remotes") {
		t.Fatalf("skewed coordinator = %+v", got)
	}
	if got := coordinatorCheck(ep, agent.CoordinatorProbe{Healthy: true, Version: "v1.2.0"}, "v1.2.0"); got.Status != "pass" || !strings.Contains(got.Message, "ssh build1") {
		t.Fatalf("matching coordinator = %+v", got)
	}
}

// stubPanes is a one-pane multiplexer for coordinator API tests.
type stubPanes struct{}

func (stubPanes) ListPanes(ctx context.Context) ([]coordinator.Pane, error) {
	return []coordinator.Pane{{PaneID: 1}}, nil
}
func (stubPanes) GetText(ctx context.Context, paneID, startLine int) (string, error) { return "", nil }
func (stubPanes) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	return nil
}
func (stubPanes) IsAvailable(ctx context.Context) bool { return true }
func (stubPanes) Backend() string                      { return "stub" }

func discardSlog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestCheckDistributedFlagsMissingAccounts(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	if err := agent.WriteFileConfig(cfgPath, agent.FileConfig{
		Coordinators: []*agent.CoordinatorEndpoint{{Name: "dead", URL: "http://127.0.0.1:1"}},
	}); err != nil {
		t.Fatal(err)
	}
	var rotation CheckResult
	for _, c := range checkDistributed(context.Background(), cfgPath) {
		if c.Name == "Account rotation" {
			rotation = c
		}
	}
	if rotation.Status != "warn" || !strings.Contains(rotation.Details, "accounts") {
		t.Fatalf("rotation check = %+v, want a warning to list accounts", rotation)
	}

	if err := agent.WriteFileConfig(cfgPath, agent.FileConfig{
		Accounts:     []string{"a@example.com", "b@example.com"},
		Strategy:     "round_robin",
		Coordinators: []*agent.CoordinatorEndpoint{{Name: "dead", URL: "http://127.0.0.1:1"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range checkDistributed(context.Background(), cfgPath) {
		if c.Name == "Account rotation" && (c.Status != "pass" || !strings.Contains(c.Message, "2 account(s), strategy round_robin")) {
			t.Fatalf("rotation check = %+v", c)
		}
	}
}

func TestHeldAccountsCheck(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "account_usage.json")
	accounts := []string{"a@example.com", "b@example.com"}

	if _, ok := heldAccountsCheck(accounts, path, now); ok {
		t.Fatal("no usage file: nothing to report")
	}

	write := func(usages []*agent.AccountUsage) {
		t.Helper()
		data, err := json.Marshal(usages)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write([]*agent.AccountUsage{
		{Email: "A@example.com", LimitedUntil: now.Add(time.Hour)},
		{Email: "b@example.com", LimitedUntil: now.Add(-time.Minute)}, // lifted
	})
	r, ok := heldAccountsCheck(accounts, path, now)
	if !ok || r.Status != "pass" || !strings.Contains(r.Message, "1 of 2") || !strings.Contains(r.Message, "a@example.com") {
		t.Fatalf("one held: %+v", r)
	}

	write([]*agent.AccountUsage{
		{Email: "a@example.com", LimitedUntil: now.Add(time.Hour)},
		{Email: "b@example.com", LimitedUntil: now.Add(2 * time.Hour)},
	})
	r, ok = heldAccountsCheck(accounts, path, now)
	if !ok || r.Status != "warn" || !strings.Contains(r.Message, "all 2 accounts") {
		t.Fatalf("all held: %+v", r)
	}
}
