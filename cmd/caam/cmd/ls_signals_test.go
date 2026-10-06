package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/spf13/cobra"
)

// Issue #102, end to end: three live Codex profiles read `warning` in
// `caam ls` from an access-token expiry months in the past that the Codex CLI
// renews on next use. The verdict must be healthy, and the JSON must carry the
// three signals a controller routes on.
func TestLsJSONCarriesCredentialSignals(t *testing.T) {
	origVault, origTools, origStore := vault, tools, profileStore
	t.Cleanup(func() { vault, tools, profileStore = origVault, origTools, origStore })

	vaultDir := filepath.Join(t.TempDir(), "vault")
	vault = authfile.NewVault(vaultDir)
	profileStore = nil // no isolated profiles: read the vault snapshot

	tools = map[string]func() authfile.AuthFileSet{
		"codex": func() authfile.AuthFileSet {
			return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
		},
	}

	write := func(name string, body map[string]any) {
		dir := filepath.Join(vaultDir, "codex", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lapsed := time.Now().Add(-40 * 24 * time.Hour).Unix()
	// The reported shape: a long-expired access token beside a refresh token.
	write("live", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": lapsed})
	// The genuinely dead shape: nothing to renew from.
	write("dead", map[string]any{"access_token": "a", "expires_at": lapsed})

	cmd := &cobra.Command{RunE: runLs}
	cmd.Flags().Bool("no-color", false, "")
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().String("tag", "", "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runLs(cmd, []string{"codex"}); err != nil {
		t.Fatalf("runLs: %v", err)
	}

	var out struct {
		Profiles []struct {
			Name   string `json:"name"`
			Health struct {
				Status        string `json:"status"`
				RefreshDue    *bool  `json:"refresh_due"`
				LaunchUsable  *bool  `json:"launch_usable"`
				LoginRequired *bool  `json:"login_required"`
			} `json:"health"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	got := map[string]struct {
		status                 string
		refresh, launch, login *bool
	}{}
	for _, p := range out.Profiles {
		got[p.Name] = struct {
			status                 string
			refresh, launch, login *bool
		}{p.Health.Status, p.Health.RefreshDue, p.Health.LaunchUsable, p.Health.LoginRequired}
	}

	live, ok := got["live"]
	if !ok {
		t.Fatalf("profile 'live' missing from %q", buf.String())
	}
	if live.status != "healthy" {
		t.Errorf("renewable Codex profile: status = %q, want healthy", live.status)
	}
	if live.refresh == nil || !*live.refresh {
		t.Errorf("refresh_due = %v, want true: caam refreshes Codex and needs the signal", live.refresh)
	}
	if live.launch == nil || !*live.launch {
		t.Errorf("launch_usable = %v, want true: the account works", live.launch)
	}
	if live.login == nil || *live.login {
		t.Errorf("login_required = %v, want false: no human is needed", live.login)
	}

	dead, ok := got["dead"]
	if !ok {
		t.Fatalf("profile 'dead' missing from %q", buf.String())
	}
	if dead.status != "critical" {
		t.Errorf("unrenewable expired profile: status = %q, want critical", dead.status)
	}
	if dead.login == nil || !*dead.login {
		t.Errorf("login_required = %v, want true", dead.login)
	}
	if dead.launch == nil || *dead.launch {
		t.Errorf("launch_usable = %v, want false", dead.launch)
	}
}

// Cursor fixtures contain only synthetic credentials. The refreshToken is
// deliberately present even on session logins: it cannot renew those logins.
func cursorHealthCredential(t *testing.T, expires time.Time, apiKey bool) []byte {
	t.Helper()
	body := map[string]any{"refreshToken": "SYNTHETIC-NONRENEWING-SESSION"}
	if !expires.IsZero() {
		claims, err := json.Marshal(map[string]any{"exp": expires.Unix(), "sub": "synthetic-cursor-user"})
		if err != nil {
			t.Fatal(err)
		}
		body["accessToken"] = "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".SYNTHETIC"
	}
	if apiKey {
		body["apiKey"] = "SYNTHETIC-CURSOR-API-KEY"
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func setupCursorHealthVault(t *testing.T) authfile.CursorPaths {
	t.Helper()
	oldVault, oldTools, oldProfiles, oldHealth, oldDB := vault, tools, profileStore, healthStore, globalDB
	t.Cleanup(func() {
		if globalDB != nil {
			globalDB.Close()
		}
		vault, tools, profileStore, healthStore, globalDB = oldVault, oldTools, oldProfiles, oldHealth, oldDB
	})
	home := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"CAAM_HOME":         filepath.Join(home, "caam"),
		"XDG_CONFIG_HOME":   filepath.Join(home, "ambient-xdg"),
		"XDG_DATA_HOME":     filepath.Join(home, "data"),
		"APPDATA":           filepath.Join(home, "ambient-appdata"),
		"CURSOR_CONFIG_DIR": filepath.Join(home, "cursor-config"),
	} {
		t.Setenv(key, value)
	}
	vault = authfile.NewVault(authfile.DefaultVaultPath())
	profileStore = nil
	healthStore = health.NewStorage(filepath.Join(home, "health.json"))
	globalDB = nil
	tools = map[string]func() authfile.AuthFileSet{"cursor": authfile.CursorAuthFiles}
	return authfile.ResolveCursorPaths(home, runtime.GOOS, os.Getenv)
}

func assertCursorSignals(t *testing.T, signals health.Signals, login, launch bool) {
	t.Helper()
	if signals.RefreshDue == nil || *signals.RefreshDue {
		t.Errorf("refresh_due = %v, want false: caam cannot renew Cursor sessions or API keys", signals.RefreshDue)
	}
	if signals.LoginRequired == nil || *signals.LoginRequired != login {
		t.Errorf("login_required = %v, want %v", signals.LoginRequired, login)
	}
	if signals.LaunchUsable == nil || *signals.LaunchUsable != launch {
		t.Errorf("launch_usable = %v, want %v", signals.LaunchUsable, launch)
	}
}

// Issue #118: all reporting surfaces must carry the parsed session deadline,
// seven-day login warning and credential signals. API keys never inherit a
// cached JWT's expiry cliff, including when they have no JWT at all.
func TestCursorCredentialHealthAcrossCommands(t *testing.T) {
	setupCursorHealthVault(t)
	now := time.Now().UTC().Truncate(time.Second)
	cases := []struct {
		name                          string
		expiry                        time.Time
		apiKey                        bool
		status                        string
		login, launch, recommendLogin bool
	}{
		{"session", now.Add(8*24*time.Hour + time.Hour), false, "healthy", false, true, false},
		{"relogin-soon", now.Add(6 * 24 * time.Hour), false, "warning", false, true, true},
		{"last-minutes", now.Add(10 * time.Minute), false, "critical", false, true, true},
		{"expired", now.Add(-time.Hour), false, "critical", true, false, true},
		{"api-key", now.Add(-time.Hour), true, "healthy", false, true, false},
		{"api-key-only", time.Time{}, true, "healthy", false, true, false},
	}
	for _, tc := range cases {
		writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", tc.name), "auth.json"), string(cursorHealthCredential(t, tc.expiry, tc.apiKey)))
		if err := healthStore.SetTokenExpiry("cursor", tc.name, now.Add(-40*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	for _, args := range [][]string{{"cursor"}, nil} {
		cmd := &cobra.Command{}
		cmd.Flags().Bool("json", true, "")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := runLs(cmd, args); err != nil {
			t.Fatal(err)
		}
		var out lsOutput
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Profiles) != len(cases) {
			t.Fatalf("ls profiles = %d, want %d: %s", len(out.Profiles), len(cases), buf.String())
		}
		rows := make(map[string]lsProfile)
		for _, row := range out.Profiles {
			rows[row.Name] = row
		}
		for _, tc := range cases {
			row := rows[tc.name]
			if row.Health.Status != tc.status {
				t.Errorf("ls %v %s: status = %s, want %s", args, tc.name, row.Health.Status, tc.status)
			}
			wantExpiry := ""
			if !tc.expiry.IsZero() {
				wantExpiry = tc.expiry.Format(time.RFC3339)
			}
			if row.Health.ExpiresAt != wantExpiry {
				t.Errorf("ls %s expiry = %q, want %q", tc.name, row.Health.ExpiresAt, wantExpiry)
			}
			assertCursorSignals(t, row.Health.Signals, tc.login, tc.launch)
		}
	}

	verifyCmd := &cobra.Command{}
	verifyCmd.Flags().Bool("json", true, "")
	var verifyBuf bytes.Buffer
	verifyCmd.SetOut(&verifyBuf)
	if err := runVerify(verifyCmd, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	var verification VerifyOutput
	if err := json.Unmarshal(verifyBuf.Bytes(), &verification); err != nil {
		t.Fatal(err)
	}
	verified := make(map[string]VerifyProfileResult)
	for _, row := range verification.Profiles {
		verified[row.Profile] = row
	}

	robotCmd := &cobra.Command{}
	var robotBuf bytes.Buffer
	robotCmd.SetOut(&robotBuf)
	if err := runRobotStatus(robotCmd, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	var robot struct {
		Data        RobotStatusData `json:"data"`
		Suggestions []string        `json:"suggestions"`
	}
	if err := json.Unmarshal(robotBuf.Bytes(), &robot); err != nil {
		t.Fatal(err)
	}
	if len(robot.Data.Providers) != 1 || len(robot.Data.Providers[0].Profiles) != len(cases) {
		t.Fatalf("robot profile coverage: %s", robotBuf.String())
	}
	if robot.Data.Summary.AllProfilesBlocked || robot.Data.Summary.ExpiringSoon != 3 || robot.Data.Summary.ReloginSoon != 2 {
		t.Errorf("robot summary conflated expiry and launch availability: %+v", robot.Data.Summary)
	}
	robotRows := make(map[string]RobotProfileInfo)
	for _, row := range robot.Data.Providers[0].Profiles {
		robotRows[row.Name] = row
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := verified[tc.name]
			r := robotRows[tc.name]
			if v.Status != tc.status || r.Health.Status != tc.status {
				t.Errorf("verify/robot status = %s/%s, want %s", v.Status, r.Health.Status, tc.status)
			}
			assertCursorSignals(t, v.Signals, tc.login, tc.launch)
			assertCursorSignals(t, r.Health.Signals, tc.login, tc.launch)
			for _, recommendation := range []string{v.Recommendation, r.Recommendation} {
				if strings.Contains(recommendation, "caam refresh") || strings.Contains(recommendation, "caam login cursor "+tc.name) != tc.recommendLogin {
					t.Errorf("recommendation = %q, want login=%v and never caam refresh", recommendation, tc.recommendLogin)
				}
				if tc.recommendLogin && (!strings.Contains(recommendation, "cursor-agent login") || !strings.Contains(recommendation, "caam activate cursor "+tc.name) || !strings.Contains(recommendation, "caam backup cursor "+tc.name)) {
					t.Errorf("recommendation omitted working vault recovery steps: %q", recommendation)
				}
			}
			if tc.apiKey && (len(v.Issues) != 0 || r.Health.Reason != "") {
				t.Errorf("API key JWT expiry became a health issue: verify=%v robot=%q", v.Issues, r.Health.Reason)
			}
			for _, force := range []bool{false, true} {
				should, reason, err := shouldRefreshProfile("cursor", tc.name, time.Hour, force)
				if err != nil || should {
					t.Fatalf("Cursor refresh force=%v: should=%v error=%v", force, should, err)
				}
				if strings.Contains(reason, "caam login cursor "+tc.name) == tc.apiKey {
					t.Errorf("refresh skip reason = %q, api key=%v", reason, tc.apiKey)
				}
				if !tc.apiKey && (!strings.Contains(reason, "cursor-agent login") || !strings.Contains(reason, "caam backup cursor "+tc.name)) {
					t.Errorf("refresh skip omitted working vault recovery steps: %q", reason)
				}
			}
		})
	}
	for _, output := range []string{verifyBuf.String(), robotBuf.String()} {
		if strings.Contains(output, "caam refresh cursor") {
			t.Errorf("Cursor report recommended unsupported refresh: %s", output)
		}
	}

	tokenChecks := make(map[string]CheckResult)
	for _, check := range checkAuthFiles() {
		if strings.HasSuffix(check.Name, " token") {
			tokenChecks[check.Name] = check
		}
	}
	if len(tokenChecks) != len(cases) {
		t.Fatalf("doctor token checks = %+v, want %d profiles", tokenChecks, len(cases))
	}
	for _, tc := range cases {
		check := tokenChecks["cursor/"+tc.name+" token"]
		wantStatus := "pass"
		if tc.login {
			wantStatus = "fail"
		} else if tc.recommendLogin {
			wantStatus = "warn"
		}
		if check.Status != wantStatus {
			t.Errorf("doctor %s status = %q, want %q", tc.name, check.Status, wantStatus)
		}
		if tc.name == "session" && !strings.Contains(check.Message, "expires in 8 days") {
			t.Errorf("healthy doctor row hid the remaining login lifetime: %+v", check)
		}
		if strings.Contains(check.Details, "caam login cursor") != tc.recommendLogin || strings.Contains(check.Details, "caam refresh cursor") {
			t.Errorf("doctor advice = %q", check.Details)
		}
		if tc.recommendLogin && (!strings.Contains(check.Details, "cursor-agent login") || !strings.Contains(check.Details, "caam backup cursor "+tc.name)) {
			t.Errorf("doctor omitted working vault recovery steps: %q", check.Details)
		}
	}

	var validateBuf bytes.Buffer
	validateCmd := &cobra.Command{}
	validateCmd.SetOut(&validateBuf)
	if err := runRobotValidate(validateCmd, nil); err != nil {
		t.Fatal(err)
	}
	var validated struct {
		Data RobotValidateData `json:"data"`
	}
	if err := json.Unmarshal(validateBuf.Bytes(), &validated); err != nil {
		t.Fatal(err)
	}
	if validated.Data.Summary.Total != len(cases) || validated.Data.Summary.Invalid != 1 {
		t.Errorf("robot validate did not distinguish session expiry from cached API-key JWT expiry: %s", validateBuf.String())
	}
}

func TestCursorActiveReportsSurviveConfigChurn(t *testing.T) {
	paths := setupCursorHealthVault(t)
	expiry := time.Now().UTC().Add(6 * 24 * time.Hour).Truncate(time.Second)
	writeNativeTestCredential(t, paths.AuthFile, string(cursorHealthCredential(t, expiry, false)))
	configPath := filepath.Join(paths.ConfigDir, "cli-config.json")
	writeNativeTestCredential(t, configPath, `{"authInfo":{"email":"synthetic@example.invalid"},"permissions":{"allow":[]}}`)
	if err := vault.Backup(tools["cursor"](), "seat"); err != nil {
		t.Fatal(err)
	}
	writeNativeTestCredential(t, configPath, `{"authInfo":{"email":"synthetic@example.invalid"},"permissions":{"allow":["Read"]},"model":"new-model"}`)
	// The isolated profile with this name has an old login. Active vault
	// commands must use the matching machine-global credential instead.
	profileStore = profile.NewStore(filepath.Join(t.TempDir(), "profiles"))
	prof, err := profileStore.Create("cursor", "seat", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	isolatedPaths := authfile.ResolveCursorPaths(prof.HomePath(), runtime.GOOS, func(string) string { return "" })
	writeNativeTestCredential(t, isolatedPaths.AuthFile, string(cursorHealthCredential(t, expiry.Add(-40*24*time.Hour), false)))

	for _, args := range [][]string{{"cursor"}, nil} {
		cmd := &cobra.Command{}
		cmd.Flags().Bool("json", true, "")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := runLs(cmd, args); err != nil {
			t.Fatal(err)
		}
		var out lsOutput
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Profiles) != 1 || !out.Profiles[0].Active || out.Profiles[0].Health.ExpiresAt != expiry.Format(time.RFC3339) || out.Profiles[0].Health.Status != "warning" {
			t.Errorf("ls %v lost active profile after config churn: %s", args, buf.String())
		}
	}
	for _, args := range [][]string{{"cursor"}, nil} {
		cmd := &cobra.Command{}
		cmd.Flags().Bool("json", true, "")
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := runStatus(cmd, args); err != nil {
			t.Fatal(err)
		}
		var status statusOutput
		if err := json.Unmarshal(buf.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if len(status.Tools) != 1 || status.Tools[0].ActiveProfile != "seat" || status.Tools[0].Health == nil || status.Tools[0].Health.ExpiresAt != expiry.Format(time.RFC3339) {
			t.Fatalf("status %v lost live expiry after config churn: %s", args, buf.String())
		}
		assertCursorSignals(t, status.Tools[0].Health.Signals, false, true)
		if !strings.Contains(strings.Join(status.Recommendations, "\n"), "caam login cursor seat") {
			t.Errorf("status omitted advance login advice: %s", buf.String())
		}
	}
	verified := verifyProfile("cursor", "seat")
	if verified.TokenExpiry == nil || !verified.TokenExpiry.Equal(expiry) || verified.Status != "warning" {
		t.Errorf("verify did not use active live expiry: %+v", verified)
	}
	robot := buildProviderInfo("cursor", false)
	if robot.ActiveProfile != "seat" || len(robot.Profiles) != 1 || robot.Profiles[0].Health.ExpiresAt != expiry.Format(time.RFC3339) {
		t.Errorf("robot did not use active live expiry: %+v", robot)
	}
	for _, check := range checkAuthFiles() {
		if check.Name == "cursor/seat token" && check.Status != "warn" {
			t.Errorf("doctor used stale isolated expiry: %+v", check)
		}
	}
	var healthBuf bytes.Buffer
	healthCmd := &cobra.Command{}
	healthCmd.SetOut(&healthBuf)
	if err := runRobotHealth(healthCmd, nil); err != nil {
		t.Fatal(err)
	}
	var robotHealth struct {
		Data struct {
			Checks []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
			Suggestions []string `json:"suggestions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(healthBuf.Bytes(), &robotHealth); err != nil {
		t.Fatal(err)
	}
	foundCursor := false
	for _, check := range robotHealth.Data.Checks {
		if check.Name == "cursor" && check.Status == "warning" {
			foundCursor = true
		}
	}
	if !foundCursor || !strings.Contains(strings.Join(robotHealth.Data.Suggestions, "\n"), "caam login cursor seat") {
		t.Errorf("robot health omitted Cursor session warning: %s", healthBuf.String())
	}
	var robotBuf bytes.Buffer
	robotCmd := &cobra.Command{}
	robotCmd.SetOut(&robotBuf)
	if err := runRobotStatus(robotCmd, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Data RobotStatusData `json:"data"`
	}
	if err := json.Unmarshal(robotBuf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Summary.AllProfilesBlocked || out.Data.Summary.ReloginSoon != 1 {
		t.Errorf("a future session expiry blocked launching: %+v", out.Data.Summary)
	}
}

func TestCursorUnmatchedLiveLoginHealth(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		expiryOffset           time.Duration
		apiKey, unknown, login bool
		status, doctorStatus   string
	}{
		{"expired", -time.Hour, false, false, true, "critical", "fail"},
		{"soon", 6 * 24 * time.Hour, false, false, false, "warning", "warn"},
		{"unknown", 0, false, true, false, "warning", "warn"},
		{"api-key-only", 0, true, false, false, "healthy", "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := setupCursorHealthVault(t)
			expiry := time.Time{}
			if tc.expiryOffset != 0 {
				expiry = time.Now().Add(tc.expiryOffset).Truncate(time.Second)
			}
			data := cursorHealthCredential(t, expiry, tc.apiKey)
			if tc.unknown {
				data = []byte(`{"accessToken":"SYNTHETIC-OPAQUE-SESSION"}`)
			}
			writeNativeTestCredential(t, paths.AuthFile, string(data))
			// A different saved account must remain visible in saved_profiles,
			// without donating its name or credential state to this live login.
			writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "different"), "auth.json"), `{"apiKey":"SYNTHETIC-OTHER-ACCOUNT"}`)
			cmd := &cobra.Command{}
			cmd.Flags().Bool("json", true, "")
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			if err := runStatus(cmd, nil); err != nil {
				t.Fatal(err)
			}
			var out statusOutput
			if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if len(out.Tools) != 1 {
				t.Fatalf("missing live Cursor status: %s", buf.String())
			}
			row := out.Tools[0]
			if row.ActiveProfile != "" || row.SavedProfiles != 1 || !row.LoggedIn || row.Health == nil || row.Health.Status != tc.status {
				t.Fatalf("unmatched live credential lost its own health or saved profile metadata: %s", buf.String())
			}
			if tc.unknown {
				if row.Health.LoginRequired != nil || row.Health.RefreshDue != nil || row.Health.LaunchUsable != nil || row.Health.ExpiresAt != "" {
					t.Errorf("unknown live expiry was invented: %+v", row.Health)
				}
			} else {
				assertCursorSignals(t, row.Health.Signals, tc.login, !tc.login)
			}
			recommend := tc.expiryOffset != 0
			advice := strings.Join(out.Recommendations, "\n")
			if strings.Contains(advice, "cursor-agent login") != recommend || strings.Contains(advice, "caam login") || strings.Contains(advice, "caam backup") || strings.Contains(advice, "caam refresh") {
				t.Errorf("unbacked session advice invented a profile: %q", advice)
			}
			found := false
			for _, check := range checkAuthFiles() {
				if check.Name == "cursor live token" {
					found = true
					if check.Status != tc.doctorStatus || strings.Contains(check.Details, "cursor-agent login") != recommend || strings.Contains(check.Details, "caam login") {
						t.Errorf("doctor live session health/advice = %+v", check)
					}
				}
			}
			if !found {
				t.Fatal("doctor omitted unmatched live Cursor login")
			}
			textCmd := &cobra.Command{}
			output, err := captureStdout(t, func() error { return runStatus(textCmd, nil) })
			if err != nil || !strings.Contains(output, "Live login:") || strings.Contains(output, "cursor-agent login") != recommend {
				t.Errorf("plain status omitted live health: %q, error=%v", output, err)
			}
		})
	}
}

func TestCursorRobotHealthIncludesCooldown(t *testing.T) {
	setupCursorHealthVault(t)
	now := time.Now()
	for _, name := range []string{"session", "api-key", "expired"} {
		expiry := now.Add(8 * 24 * time.Hour)
		if name == "expired" {
			expiry = now.Add(-time.Hour)
		}
		writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", name), "auth.json"), string(cursorHealthCredential(t, expiry, name == "api-key")))
		db, err := getDB()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.SetCooldown("cursor", name, now, time.Hour, "synthetic rate limit"); err != nil {
			t.Fatal(err)
		}
	}
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runRobotHealth(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Data struct {
			Overall string `json:"overall"`
			Checks  []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"checks"`
			Suggestions []string `json:"suggestions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Overall != "degraded" {
		t.Fatalf("robot health ignored active Cursor cooldowns: %s", buf.String())
	}
	found := false
	for _, check := range out.Data.Checks {
		if check.Name == "cursor" && check.Status == "warning" {
			found = true
		}
	}
	if !found {
		t.Errorf("robot health missed capped Cursor profiles: %s", buf.String())
	}
	robot := buildProfileInfo("cursor", "expired", "", globalDB, false)
	if !strings.Contains(robot.Health.Reason, "rate limited") || !strings.Contains(robot.Health.Reason, "login required") || !strings.Contains(robot.Recommendation, "cursor-agent login") {
		t.Errorf("cooldown hid hard session expiry: %+v", robot)
	}
	assertCursorSignals(t, robot.Health.Signals, true, false)
}
