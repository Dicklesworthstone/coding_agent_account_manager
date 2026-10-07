package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

type lsVerificationRow struct {
	Name   string `json:"name"`
	Health struct {
		Status             string `json:"status"`
		ExpiresAt          string `json:"expires_at"`
		LaunchUsable       *bool  `json:"launch_usable"`
		LoginRequired      *bool  `json:"login_required"`
		Verification       string `json:"verification"`
		LastVerifiedAt     string `json:"last_verified_at"`
		ProviderRejection  string `json:"provider_rejection"`
		ProviderRejectedAt string `json:"provider_rejected_at"`
	} `json:"health"`
}

func TestSavedSelectedAPIKeyHealthPropagation(t *testing.T) {
	for _, tool := range []string{"codex", "gemini"} {
		t.Run(tool, func(t *testing.T) {
			vaultDir, store := setupCodexVerificationVault(t)
			nativeDir := t.TempDir()
			t.Setenv("CODEX_HOME", nativeDir)
			t.Setenv("GEMINI_HOME", nativeDir)
			t.Setenv("CAAM_KEYCHAIN", "0")
			tools = map[string]func() authfile.AuthFileSet{
				"codex":  authfile.CodexAuthFiles,
				"gemini": authfile.GeminiAuthFiles,
			}
			store.SetVaultPath(vaultDir)
			expired := time.Now().Add(-time.Hour)
			oauth := fmt.Sprintf(`{"access_token":"synthetic-old-oauth","expires_at":%d}`, expired.Unix())
			files := map[string]map[string]string{}
			if tool == "codex" {
				files["oauth"] = map[string]string{"auth.json": fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":"unused-key","tokens":%s}`, oauth)}
				files["api"] = map[string]string{"auth.json": fmt.Sprintf(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-selected-key","tokens":%s}`, oauth)}
				files["malformed-unused"] = map[string]string{"auth.json": `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-selected-key","tokens":17}`}
			} else {
				files["oauth"] = map[string]string{"settings.json": `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "oauth_creds.json": oauth, ".env": "GEMINI_API_KEY=unused-key\n"}
				files["api"] = map[string]string{"settings.json": `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`, "oauth_credentials.json": oauth, ".env": "GEMINI_API_KEY=synthetic-selected-key\n"}
				files["malformed-unused"] = map[string]string{"settings.json": `{"selectedAuthType":"gemini-api-key"}`, "oauth_credentials.json": "{", ".env": "GEMINI_API_KEY=synthetic-selected-key\n"}
			}
			for name, contents := range files {
				for filename, data := range contents {
					writeNativeTestCredential(t, filepath.Join(vaultDir, tool, name, filename), data)
				}
				if err := store.SetTokenExpiry(tool, name, expired); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordProviderVerification(tool, name, health.ProviderVerification{Reason: "refresh_token_reused", Fingerprint: "obsolete-oauth-credential"}); err != nil {
					t.Fatal(err)
				}
			}
			assertKeyHealth := func(t *testing.T, ph *health.ProfileHealth) {
				t.Helper()
				if ph == nil || !ph.TokenExpiresAt.IsZero() || !ph.TokenRenewable || !ph.SelfRefreshing || ph.CredentialFingerprint == "" || ph.ProviderRejected() || health.CalculateStatus(ph) != health.StatusHealthy {
					t.Fatalf("selected API key inherited unused OAuth health: %+v", ph)
				}
				signals := health.CredentialSignals(ph, health.DefaultHealthConfig())
				if signals.LaunchUsable == nil || !*signals.LaunchUsable || signals.LoginRequired == nil || *signals.LoginRequired || signals.RefreshDue == nil || *signals.RefreshDue {
					t.Fatalf("selected API key has wrong launch/refresh signals: %+v", signals)
				}
			}
			for _, name := range []string{"api", "malformed-unused"} {
				t.Run(name, func(t *testing.T) {
					assertKeyHealth(t, buildProfileHealth(tool, name))
					ph, err := readVaultProfileHealth(tool, name)
					if err != nil {
						t.Fatalf("passive saved credential check failed: %v", err)
					}
					assertKeyHealth(t, ph)
					verified := verifyProfile(tool, name)
					if verified.Status != "healthy" || verified.TokenExpiry != nil || verified.LaunchUsable == nil || !*verified.LaunchUsable || verified.LoginRequired == nil || *verified.LoginRequired {
						t.Fatalf("verify inherited unused OAuth state: %+v", verified)
					}
					row := runLsJSONForTest(t, tool)[name]
					if row.Health.Status != "healthy" || row.Health.ExpiresAt != "" || row.Health.LaunchUsable == nil || !*row.Health.LaunchUsable || row.Health.LoginRequired == nil || *row.Health.LoginRequired || row.Health.ProviderRejection != "" {
						t.Fatalf("ls inherited unused OAuth state: %+v", row)
					}
					for _, compact := range []bool{false, true} {
						robot := buildProfileInfo(tool, name, "", nil, compact)
						if robot.Health.Status != "healthy" || !robot.Health.Renewable || robot.Health.LoginRequired == nil || *robot.Health.LoginRequired {
							t.Fatalf("robot compact=%t inherited unused OAuth state: %+v", compact, robot.Health)
						}
					}
					validation, _ := validateVaultProfile(tool, name)
					if !validation.Valid || validation.ExpiresAt != "" || validation.LoginRequired == nil || *validation.LoginRequired {
						t.Fatalf("validation inherited unused OAuth state: %+v", validation)
					}
				})
			}
			for _, algorithm := range []rotation.Algorithm{rotation.AlgorithmSmart, rotation.AlgorithmRoundRobin, rotation.AlgorithmRandom} {
				for _, policy := range []rotation.Policy{rotation.PolicyAvailability, rotation.PolicyDrain} {
					selector := rotation.NewSelector(algorithm, store, nil)
					selector.SetVaultPath(vaultDir)
					selector.SetPolicy(policy)
					selector.SetIgnoreCooldown(true)
					if result, err := selector.Select(tool, []string{"oauth"}, ""); err == nil {
						t.Fatalf("explicit OAuth borrowed an unused key: %+v", result)
					}
					if result, err := selector.Select(tool, []string{"oauth", "api"}, ""); err != nil || result.Selected != "api" {
						t.Fatalf("%s/%s blocked selected key: %+v, %v", algorithm, policy, result, err)
					}
				}
			}
			// Listing and identity reads must leave legacy caches untouched,
			// even when they are invalid and unused by the selected method.
			for name, contents := range files {
				for filename, want := range contents {
					got, err := os.ReadFile(filepath.Join(vaultDir, tool, name, filename))
					if err != nil || string(got) != want {
						t.Fatalf("passive reporting changed %s/%s: %v", name, filename, err)
					}
				}
			}
		})
	}
}

func TestLiveSelectedAPIKeyReplacesVaultDeadline(t *testing.T) {
	for _, tool := range []string{"codex", "gemini"} {
		for _, isolated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/isolated=%t", tool, isolated), func(t *testing.T) {
				vaultDir, _ := setupCodexVerificationVault(t)
				dir := t.TempDir()
				t.Setenv("CODEX_HOME", dir)
				t.Setenv("GEMINI_HOME", dir)
				oauth := fmt.Sprintf(`{"access_token":"synthetic-old-oauth","expires_at":%d}`, time.Now().Add(-time.Hour).Unix())
				filename := "auth.json"
				if tool == "gemini" {
					filename = "oauth_creds.json"
				}
				writeNativeTestCredential(t, filepath.Join(vaultDir, tool, "work", filename), oauth)
				if ph := buildProfileHealth(tool, "work"); ph.TokenExpiresAt.IsZero() {
					t.Fatal("fixture has no old vault deadline to replace")
				}
				if isolated {
					profileStore = profile.NewStore(t.TempDir())
					prof, err := profileStore.Create(tool, "work", "api-key")
					if err != nil {
						t.Fatal(err)
					}
					dir = prof.CodexHomePath()
					if tool == "gemini" {
						dir = filepath.Join(prof.HomePath(), ".gemini")
					}
				}
				selectedPath := filepath.Join(dir, "auth.json")
				if tool == "codex" {
					writeNativeTestCredential(t, selectedPath, `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-new-key"}`)
				} else {
					selectedPath = filepath.Join(dir, "settings.json")
					writeNativeTestCredential(t, selectedPath, `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
					writeNativeTestCredential(t, filepath.Join(dir, ".env"), "GEMINI_API_KEY=synthetic-new-key\n")
				}
				currentHealth := func() *health.ProfileHealth {
					ph := buildProfileHealth(tool, "work")
					if !isolated {
						applyLiveExpiry(tool, ph)
					}
					return ph
				}
				ph := currentHealth()
				if !ph.TokenExpiresAt.IsZero() || !ph.TokenRenewable || !ph.SelfRefreshing || health.CalculateStatus(ph) != health.StatusHealthy {
					t.Fatalf("current API key retained the vault deadline: %+v", ph)
				}
				writeNativeTestCredential(t, selectedPath, "{")
				if ph := currentHealth(); !ph.TokenExpiresAt.IsZero() || ph.TokenRenewable || ph.SelfRefreshing {
					t.Fatalf("invalid current credential borrowed vault expiry or renewal: %+v", ph)
				}
			})
		}
	}
}

func TestDoctorDoesNotProbeUnusedCodexOAuth(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	oldURL := refresh.CodexVerifyURL
	refresh.CodexVerifyURL = server.URL
	t.Cleanup(func() { refresh.CodexVerifyURL = oldURL })
	liveOAuth := fmt.Sprintf(`{"access_token":"synthetic-unused-oauth","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
	for _, mode := range []string{"apikey", "chatgpt"} {
		data := fmt.Sprintf(`{"auth_mode":%q,"OPENAI_API_KEY":"synthetic-key","tokens":%s}`, mode, liveOAuth)
		writeNativeTestCredential(t, filepath.Join(vaultDir, "codex", mode, "auth.json"), data)
	}
	if result := probeVaultToken("codex", "apikey"); result != nil || requests.Load() != 0 {
		t.Fatalf("doctor probed unused OAuth: result=%+v requests=%d", result, requests.Load())
	}
	ph, err := store.GetProfile("codex", "apikey")
	if err != nil || !ph.ProviderRejectedAt.IsZero() || !ph.LastVerifiedAt.IsZero() {
		t.Fatalf("unused OAuth probe altered API key verification: %+v, %v", ph, err)
	}
	if result := probeVaultToken("codex", "chatgpt"); result == nil || requests.Load() != 1 {
		t.Fatalf("explicit OAuth probe was disabled: result=%+v requests=%d", result, requests.Load())
	}
}

func TestCursorSessionRobotReloginRecommendations(t *testing.T) {
	for _, tc := range []struct {
		remaining time.Duration
		status    health.HealthStatus
	}{
		{12 * time.Hour, health.StatusWarning},
		{10 * time.Minute, health.StatusCritical},
		{-time.Minute, health.StatusCritical},
	} {
		ph := &health.ProfileHealth{TokenExpiresAt: time.Now().Add(tc.remaining), ReloginWarningLead: 24 * time.Hour}
		reason := getHealthReason(ph, tc.status)
		rec := generateRecommendation(RobotProfileInfo{Health: RobotHealthInfo{Status: tc.status.String(), Reason: reason}})
		if !strings.Contains(reason, "login required") || !strings.Contains(rec, "log in again") || strings.Contains(rec, "refresh") {
			t.Fatalf("remaining %v: reason=%q recommendation=%q", tc.remaining, reason, rec)
		}
	}
}

func TestCursorStatusKeepsActiveLoginAfterConfigChange(t *testing.T) {
	setupCodexVerificationVault(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CURSOR_CONFIG_DIR", "")
	tools["cursor"] = authfile.CursorAuthFiles
	set := authfile.CursorAuthFiles()
	exp := time.Now().Add(6 * 24 * time.Hour).Truncate(time.Second)
	payload, err := json.Marshal(map[string]any{"exp": exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	for _, spec := range set.Files {
		if err := os.MkdirAll(filepath.Dir(spec.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		data := []byte(`{"model":"before"}`)
		if filepath.Base(spec.Path) == "auth.json" {
			data, err = json.Marshal(map[string]string{"accessToken": token, "refreshToken": token})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(spec.Path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := vault.Backup(set, "work"); err != nil {
		t.Fatal(err)
	}
	for _, spec := range set.Files {
		if filepath.Base(spec.Path) != "auth.json" {
			if err := os.WriteFile(spec.Path, []byte(`{"model":"after"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("no-color", true, "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runStatus(cmd, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	var out statusOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("status output %q: %v", buf.String(), err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("status = %+v", out)
	}
	st := out.Tools[0]
	if st.ActiveProfile != "work" || st.Health == nil || st.Health.Status != "warning" || !strings.Contains(st.Health.Reason, "log in again") {
		t.Fatalf("status lost active login/expiry: %+v", st)
	}
	if st.Health.LoginRequired == nil || *st.Health.LoginRequired || st.Health.RefreshDue == nil || *st.Health.RefreshDue {
		t.Fatalf("valid session signals = %+v", st.Health.Signals)
	}
}

func TestCursorVaultExpiryPropagation(t *testing.T) {
	vaultDir, _ := setupCodexVerificationVault(t)
	tools["cursor"] = authfile.CursorAuthFiles
	expires := time.Now().Add(3 * 24 * time.Hour).Truncate(time.Second)
	payload, err := json.Marshal(map[string]any{"exp": expires.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	for _, tc := range []struct {
		name   string
		apiKey string
		want   health.HealthStatus
	}{
		{"session", "", health.StatusWarning},
		{"api", "synthetic-api-key", health.StatusHealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(vaultDir, "cursor", tc.name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(map[string]string{"accessToken": token, "refreshToken": token, "apiKey": tc.apiKey})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			ph := buildProfileHealth("cursor", tc.name)
			if !ph.TokenExpiresAt.Equal(expires) || ph.TokenRenewable != (tc.apiKey != "") || health.CalculateStatus(ph) != tc.want {
				t.Fatalf("Cursor health = %+v, want %s renewable=%t", ph, tc.want.String(), tc.apiKey != "")
			}
			verified := verifyProfile("cursor", tc.name)
			if should, _, err := shouldRefreshProfile("cursor", tc.name, 7*24*time.Hour, true); err != nil || should {
				t.Fatalf("Cursor must not use CAAM refresh: should=%t error=%v", should, err)
			}
			if verified.Status != tc.want.String() || verified.TokenExpiry == nil || !verified.TokenExpiry.Equal(expires) {
				t.Fatalf("Cursor verify = %+v", verified)
			}
			rows := runLsJSONForTest(t, "cursor")
			listedExpiry, parseErr := time.Parse(time.RFC3339, rows[tc.name].Health.ExpiresAt)
			if rows[tc.name].Health.Status != tc.want.String() || parseErr != nil || !listedExpiry.Equal(expires) {
				t.Fatalf("Cursor ls = %+v", rows[tc.name])
			}
			for _, compact := range []bool{false, true} {
				robot := buildProfileInfo("cursor", tc.name, "", nil, compact)
				if robot.Health.Status != tc.want.String() || robot.Health.Renewable != (tc.apiKey != "") {
					t.Fatalf("Cursor robot compact=%t = %+v", compact, robot)
				}
				if tc.apiKey == "" && !strings.Contains(generateRecommendation(robot), "log in again") {
					t.Fatalf("Cursor robot compact=%t lost session relogin recommendation: %+v", compact, robot)
				}
			}
			found := false
			for _, check := range checkAuthFiles() {
				if check.Name == "cursor/"+tc.name+" token" {
					found = true
					if tc.apiKey == "" && (check.Status != "warn" || !strings.Contains(check.Details, "caam login cursor session")) {
						t.Fatalf("Cursor session doctor = %+v", check)
					}
					if tc.apiKey != "" && (check.Status != "pass" || !strings.Contains(check.Message, "can renew")) {
						t.Fatalf("Cursor API doctor = %+v", check)
					}
				}
			}
			if !found {
				t.Fatal("doctor omitted Cursor token check")
			}
		})
	}
}

func runLsJSONForTest(t *testing.T, tool string) map[string]lsVerificationRow {
	t.Helper()
	cmd := &cobra.Command{RunE: runLs}
	cmd.Flags().Bool("no-color", false, "")
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().String("tag", "", "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runLs(cmd, []string{tool}); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	var out struct {
		Profiles []lsVerificationRow `json:"profiles"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	rows := map[string]lsVerificationRow{}
	for _, p := range out.Profiles {
		rows[p.Name] = p
	}
	return rows
}

func setupCodexVerificationVault(t *testing.T) (vaultDir string, store *health.Storage) {
	t.Helper()
	origVault, origTools, origStore, origHealth := vault, tools, profileStore, healthStore
	t.Cleanup(func() { vault, tools, profileStore, healthStore = origVault, origTools, origStore, origHealth })

	root := t.TempDir()
	vaultDir = filepath.Join(root, "vault")
	vault = authfile.NewVault(vaultDir)
	profileStore = nil
	store = health.NewStorage(filepath.Join(root, "health.json"))
	healthStore = store
	tools = map[string]func() authfile.AuthFileSet{
		"codex": func() authfile.AuthFileSet {
			return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
		},
	}
	return vaultDir, store
}

func writeCodexVaultAuth(t *testing.T, vaultDir, name, refreshToken string, expiresAt time.Time) []byte {
	t.Helper()
	dir := filepath.Join(vaultDir, "codex", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"access_token":  "at-" + name,
		"refresh_token": refreshToken,
		"expires_at":    expiresAt.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

// Issue #108, end to end: a freshly minted Codex credential whose refresh
// token the provider has revoked read 🟢 "9d left" and launch_usable. Once a
// provider rejection is on record for that credential, `ls --json` must say
// login_required and not launch_usable; after a new login (a different
// credential) the rejection no longer applies.
func TestLsJSONHonoursProviderRejection(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	nineDays := time.Now().Add(9 * 24 * time.Hour)
	revoked := writeCodexVaultAuth(t, vaultDir, "revoked", "rt-revoked", nineDays)
	writeCodexVaultAuth(t, vaultDir, "untouched", "rt-fine", nineDays)

	if err := store.RecordProviderVerification("codex", "revoked", health.ProviderVerification{
		Reason:      "refresh_token_invalidated",
		Fingerprint: health.CodexCredentialFingerprint(revoked),
	}); err != nil {
		t.Fatal(err)
	}

	rows := runLsJSONForTest(t, "codex")

	r := rows["revoked"]
	if r.Health.Status != "critical" {
		t.Errorf("revoked: status = %q, want critical", r.Health.Status)
	}
	if r.Health.LaunchUsable == nil || *r.Health.LaunchUsable {
		t.Errorf("revoked: launch_usable = %v, want false", r.Health.LaunchUsable)
	}
	if r.Health.LoginRequired == nil || !*r.Health.LoginRequired {
		t.Errorf("revoked: login_required = %v, want true", r.Health.LoginRequired)
	}
	if r.Health.Verification != "provider" || r.Health.ProviderRejection != "refresh_token_invalidated" || r.Health.ProviderRejectedAt == "" {
		t.Errorf("revoked: verification block = %+v", r.Health)
	}
	if r.Health.ExpiresAt == "" {
		t.Error("revoked: expires_at dropped; the expiry column stays as it was")
	}

	u := rows["untouched"]
	if u.Health.Status != "healthy" || u.Health.LaunchUsable == nil || !*u.Health.LaunchUsable {
		t.Errorf("untouched: %+v, want healthy and launch_usable", u.Health)
	}
	if u.Health.Verification != "passive" || u.Health.LastVerifiedAt != "" {
		t.Errorf("untouched: verification = %q last_verified_at = %q, want passive and none", u.Health.Verification, u.Health.LastVerifiedAt)
	}

	// The operator logs in again: a new refresh token, a new credential.
	writeCodexVaultAuth(t, vaultDir, "revoked", "rt-new-login", nineDays)
	rows = runLsJSONForTest(t, "codex")
	r = rows["revoked"]
	if r.Health.Status != "healthy" || r.Health.LaunchUsable == nil || !*r.Health.LaunchUsable {
		t.Errorf("after re-login: %+v, want healthy and launch_usable", r.Health)
	}
	if r.Health.ProviderRejection != "" {
		t.Errorf("after re-login: stale rejection %q still reported", r.Health.ProviderRejection)
	}
}

// A live `caam limits codex` read is a provider round trip. An answered read
// is an acceptance; a 401 for an unexpired access token is a rejection; a
// 401 for an expired access token (routine, the refresh token renews it), a
// 403 (an edge proxy can send one) and transport errors record nothing.
func TestRecordCodexUsageVerdicts(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	future := time.Now().Add(9 * 24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	writeCodexVaultAuth(t, vaultDir, "ok", "rt-ok", future)
	rejected := writeCodexVaultAuth(t, vaultDir, "rejected", "rt-rej", future)
	writeCodexVaultAuth(t, vaultDir, "lapsed", "rt-lapsed", past)
	writeCodexVaultAuth(t, vaultDir, "offline", "rt-off", future)
	writeCodexVaultAuth(t, vaultDir, "edge403", "rt-403", future)

	now := time.Now()
	recordCodexUsageVerdicts(vaultDir, "codex", []usage.ProfileUsage{
		{ProfileName: "ok", Usage: &usage.UsageInfo{FetchedAt: now}},
		{ProfileName: "rejected", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized, HTTPStatus: 401}},
		{ProfileName: "lapsed", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized, HTTPStatus: 401}},
		{ProfileName: "edge403", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized, HTTPStatus: 403}},
		{ProfileName: "offline", Usage: &usage.UsageInfo{FetchedAt: now, Error: "request failed: dial tcp"}},
	})
	// Other providers are left alone.
	recordCodexUsageVerdicts(vaultDir, "claude", []usage.ProfileUsage{
		{ProfileName: "ok", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized}},
	})

	get := func(name string) *health.ProfileHealth {
		h, err := store.GetProfile("codex", name)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if h := get("ok"); h == nil || h.LastVerifiedAt.IsZero() {
		t.Errorf("ok: acceptance not recorded: %+v", h)
	}
	if h := get("rejected"); h == nil || h.ProviderRejectedAt.IsZero() || h.RejectedFingerprint != health.CodexCredentialFingerprint(rejected) {
		t.Errorf("rejected: rejection not recorded against its credential: %+v", h)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if h, exists := persisted.Profiles["codex/lapsed"]; exists {
		t.Errorf("lapsed: a 401 for an expired access token was recorded: %+v", h)
	}
	if h, exists := persisted.Profiles["codex/edge403"]; exists {
		t.Errorf("edge403: a 403 was recorded as a rejection: %+v", h)
	}
	if h, exists := persisted.Profiles["codex/offline"]; exists {
		t.Errorf("offline: a transport error was recorded: %+v", h)
	}
	for _, name := range []string{"lapsed", "edge403", "offline"} {
		if h := get(name); h == nil || h.ProviderRejected() || !h.ProviderRejectedAt.IsZero() || !h.LastVerifiedAt.IsZero() {
			t.Errorf("%s: current credentials should remain unverified without a persisted verdict: %+v", name, h)
		}
	}
	if h, exists := persisted.Profiles["claude/ok"]; exists {
		t.Errorf("claude row recorded: %+v", h)
	}
}

// doctor's live /v1/me probe records its verdict: a 401 marks the credential
// rejected, a 200 later clears it, and a 5xx records nothing.
func TestProbeVaultTokenRecordsVerdict(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	writeCodexVaultAuth(t, vaultDir, "work", "rt-work", time.Now().Add(9*24*time.Hour))

	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()
	oldURL := refresh.CodexVerifyURL
	refresh.CodexVerifyURL = server.URL
	defer func() { refresh.CodexVerifyURL = oldURL }()

	if res := probeVaultToken("codex", "work"); res == nil || res.Status != "fail" {
		t.Fatalf("401 probe: %+v, want a failing check", res)
	}
	h, _ := store.GetProfile("codex", "work")
	if h == nil || h.ProviderRejection != "access_token_rejected_http_401" {
		t.Fatalf("401 not recorded: %+v", h)
	}
	if rows := runLsJSONForTest(t, "codex"); rows["work"].Health.LaunchUsable == nil || *rows["work"].Health.LaunchUsable {
		t.Errorf("ls after a rejected probe: %+v", rows["work"].Health)
	}

	status = http.StatusOK
	if res := probeVaultToken("codex", "work"); res != nil {
		t.Fatalf("200 probe: %+v, want no finding", res)
	}
	h, _ = store.GetProfile("codex", "work")
	if h == nil || !h.ProviderRejectedAt.IsZero() || h.LastVerifiedAt.IsZero() {
		t.Fatalf("200 did not clear the rejection: %+v", h)
	}
	if rows := runLsJSONForTest(t, "codex"); rows["work"].Health.Verification != "provider" || rows["work"].Health.LastVerifiedAt == "" {
		t.Errorf("ls after an accepted probe: %+v", rows["work"].Health)
	}

	status = http.StatusBadGateway
	before, _ := store.GetProfile("codex", "work")
	_ = probeVaultToken("codex", "work")
	after, _ := store.GetProfile("codex", "work")
	if !after.LastVerifiedAt.Equal(before.LastVerifiedAt) || !after.ProviderRejectedAt.IsZero() {
		t.Errorf("a 5xx changed the record: before %+v after %+v", before, after)
	}
}

// A vault copy routinely holds a lapsed access token beside a working refresh
// token. doctor's /v1/me probe answers 401 for it, and that must not mark the
// account "Login required" (review of c742360, GH #108): nothing clears such a
// false rejection until a refresh or probe succeeds, and the refresh daemon
// never refreshes an already-expired token.
func TestProbeVaultToken401ForExpiredAccessTokenRecordsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	oldURL := refresh.CodexVerifyURL
	refresh.CodexVerifyURL = server.URL
	defer func() { refresh.CodexVerifyURL = oldURL }()

	for name, exp := range map[string]time.Duration{
		"expired":    -2 * time.Hour,
		"clock-skew": 2 * time.Minute,
	} {
		vaultDir, store := setupCodexVerificationVault(t)
		writeCodexVaultAuth(t, vaultDir, "work", "rt-work", time.Now().Add(exp))
		_ = probeVaultToken("codex", "work")
		if h, _ := store.GetProfile("codex", "work"); h != nil && !h.ProviderRejectedAt.IsZero() {
			t.Errorf("%s access token: 401 recorded as a provider rejection: %+v", name, h)
		}
	}
}

// Robot output for a provider-rejected profile named no reason and told
// agents to "investigate errors" (review of c742360, GH #108).
func TestRobotReasonForProviderRejection(t *testing.T) {
	ph := &health.ProfileHealth{
		TokenExpiresAt:        time.Now().Add(9 * 24 * time.Hour),
		TokenRenewable:        true,
		CredentialFingerprint: "fp",
		ProviderRejectedAt:    time.Now().Add(-time.Minute),
		ProviderRejection:     "refresh_token_expired",
		RejectedFingerprint:   "fp",
	}
	reason := getHealthReason(ph, health.CalculateStatus(ph))
	if !strings.Contains(reason, "refresh_token_expired") || !strings.Contains(reason, "login required") {
		t.Fatalf("reason = %q", reason)
	}
	rec := generateRecommendation(RobotProfileInfo{Health: RobotHealthInfo{Status: "critical", Reason: reason}})
	if rec != "log in again" {
		t.Errorf("recommendation = %q, want %q", rec, "log in again")
	}
}

// GH #108's exact scenario: the provider refused the refresh token, yet the
// freshly minted access token beside it keeps working for days. A doctor
// probe or a `caam limits` read only exercises that access token, so its
// acceptance must not clear the refresh-token rejection (review of b3ff27e);
// otherwise the dead account reads healthy until the access token lapses and
// then stays that way. A new login still clears it.
func TestAccessTokenAcceptanceKeepsRefreshTokenRejection(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	revoked := writeCodexVaultAuth(t, vaultDir, "work", "rt-revoked", time.Now().Add(9*24*time.Hour))
	if err := store.RecordProviderVerification("codex", "work", health.ProviderVerification{
		Reason: "refresh_token_invalidated", Fingerprint: health.CodexCredentialFingerprint(revoked),
		At: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	oldURL := refresh.CodexVerifyURL
	refresh.CodexVerifyURL = server.URL
	defer func() { refresh.CodexVerifyURL = oldURL }()

	if res := probeVaultToken("codex", "work"); res != nil {
		t.Fatalf("200 probe: %+v, want no finding", res)
	}
	recordCodexUsageVerdicts(vaultDir, "codex", []usage.ProfileUsage{
		{ProfileName: "work", Usage: &usage.UsageInfo{FetchedAt: time.Now()}},
	})
	h, _ := store.GetProfile("codex", "work")
	if !h.ProviderRejected() || h.ProviderRejection != "refresh_token_invalidated" {
		t.Fatalf("access-token acceptance cleared a refresh-token rejection: %+v", h)
	}
	if rows := runLsJSONForTest(t, "codex"); rows["work"].Health.LoginRequired == nil || !*rows["work"].Health.LoginRequired {
		t.Errorf("ls: revoked credential not login_required: %+v", rows["work"].Health)
	}

	// A new login mints a different credential; its acceptance clears it.
	writeCodexVaultAuth(t, vaultDir, "work", "rt-new-login", time.Now().Add(9*24*time.Hour))
	if res := probeVaultToken("codex", "work"); res != nil {
		t.Fatalf("200 probe after login: %+v", res)
	}
	h, _ = store.GetProfile("codex", "work")
	if h.ProviderRejected() || !h.ProviderRejectedAt.IsZero() || h.LastVerifiedAt.IsZero() {
		t.Errorf("acceptance of a new login did not clear the rejection: %+v", h)
	}
}
