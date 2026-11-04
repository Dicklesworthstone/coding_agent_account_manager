package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

// robotHistoryResponse mirrors the RobotOutput wrapper for the history command
// so tests can assert on the decoded JSON payload.
type robotHistoryResponse struct {
	Success bool             `json:"success"`
	Command string           `json:"command"`
	Data    RobotHistoryData `json:"data"`
	Error   *RobotError      `json:"error"`
}

func newRobotHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Int("days", 7, "")
	cmd.Flags().Int("limit", 50, "")
	cmd.Flags().String("provider", "", "")
	return cmd
}

// TestRobotHistory_ReturnsLoggedEvents is a regression test for issue #51:
// `caam robot history` returned count:0/events:[] even when activity_log had
// rows, because its query referenced a nonexistent `notes` column. The fix
// mirrors the working `caam history` column set (details, not notes).
func TestRobotHistory_ReturnsLoggedEvents(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}

	// Insert several activity_log rows across providers/profiles.
	events := []caamdb.Event{
		{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"},
		{Type: caamdb.EventRefresh, Provider: "claude", ProfileName: "personal"},
		{Type: caamdb.EventActivate, Provider: "claude", ProfileName: "personal",
			Details: map[string]any{"reason": "rotation"}},
	}
	for _, ev := range events {
		if err := db.LogEvent(ev); err != nil {
			t.Fatalf("LogEvent(%+v) error = %v", ev, err)
		}
	}
	db.Close()

	cmd := newRobotHistoryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runRobotHistory(cmd, []string{}); err != nil {
		t.Fatalf("runRobotHistory() error = %v", err)
	}

	var resp robotHistoryResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode robot history output: %v\noutput: %s", err, buf.String())
	}

	if resp.Error != nil {
		t.Fatalf("robot history returned error: %+v", resp.Error)
	}
	if !resp.Success {
		t.Fatalf("robot history success=false, output: %s", buf.String())
	}

	// The core regression assertion: the real events are returned, not empty.
	if resp.Data.Count != len(events) {
		t.Fatalf("robot history count = %d, want %d (events: %+v)",
			resp.Data.Count, len(events), resp.Data.Events)
	}
	if len(resp.Data.Events) != len(events) {
		t.Fatalf("robot history returned %d events, want %d",
			len(resp.Data.Events), len(events))
	}

	// The activity-log detail context surfaces in the Notes field (sourced
	// from the `details` column, since there is no `notes` column).
	var foundRotation bool
	for _, e := range resp.Data.Events {
		if e.Provider == "" || e.Profile == "" || e.Event == "" {
			t.Errorf("event missing required fields: %+v", e)
		}
		if e.Notes != "" && strings.Contains(e.Notes, "rotation") {
			foundRotation = true
		}
	}
	if !foundRotation {
		t.Errorf("expected the details/notes context to surface in output, events: %+v", resp.Data.Events)
	}
}

// TestRobotHistory_ConsistentWithNonRobotHistory asserts the robot and
// non-robot history paths agree on event counts for the same DB, so a future
// schema drift that breaks one but not the other is caught.
func TestRobotHistory_ConsistentWithNonRobotHistory(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	inserted := []caamdb.Event{
		{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"},
		{Type: caamdb.EventActivate, Provider: "gemini", ProfileName: "main"},
		{Type: caamdb.EventError, Provider: "claude", ProfileName: "personal"},
	}
	for _, ev := range inserted {
		if err := db.LogEvent(ev); err != nil {
			t.Fatalf("LogEvent error = %v", err)
		}
	}
	db.Close()

	// Robot path count.
	robotCmd := newRobotHistoryCmd()
	var robotBuf bytes.Buffer
	robotCmd.SetOut(&robotBuf)
	if err := runRobotHistory(robotCmd, []string{}); err != nil {
		t.Fatalf("runRobotHistory() error = %v", err)
	}
	var robotResp robotHistoryResponse
	if err := json.Unmarshal(robotBuf.Bytes(), &robotResp); err != nil {
		t.Fatalf("decode robot history: %v", err)
	}

	// Non-robot path count (JSON mode).
	nonRobotCmd := &cobra.Command{}
	nonRobotCmd.Flags().IntP("limit", "n", 100, "")
	nonRobotCmd.Flags().String("provider", "", "")
	nonRobotCmd.Flags().String("profile", "", "")
	nonRobotCmd.Flags().String("type", "", "")
	nonRobotCmd.Flags().String("since", "", "")
	nonRobotCmd.Flags().Bool("json", true, "")
	_ = nonRobotCmd.Flags().Set("json", "true")
	var nonRobotBuf bytes.Buffer
	nonRobotCmd.SetOut(&nonRobotBuf)
	if err := runHistory(nonRobotCmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}
	var nonRobotResp historyOutput
	if err := json.Unmarshal(nonRobotBuf.Bytes(), &nonRobotResp); err != nil {
		t.Fatalf("decode non-robot history: %v\noutput: %s", err, nonRobotBuf.String())
	}

	if robotResp.Data.Count != len(inserted) {
		t.Fatalf("robot count = %d, want %d", robotResp.Data.Count, len(inserted))
	}
	if robotResp.Data.Count != nonRobotResp.Count {
		t.Fatalf("robot count (%d) != non-robot count (%d) for identical DB",
			robotResp.Data.Count, nonRobotResp.Count)
	}
}

type robotCredentialResponse struct {
	Success     bool            `json:"success"`
	Command     string          `json:"command"`
	Data        json.RawMessage `json:"data"`
	Error       *RobotError     `json:"error"`
	Suggestions []string        `json:"suggestions"`
}

// Give each handler its real Cobra argument check and RunE, with independent
// flags and output buffers so these tests do not mutate the global commands.
func runRobotCredentialCommand(t *testing.T, source *cobra.Command, args ...string) (robotCredentialResponse, error) {
	t.Helper()
	return runRobotCredentialCommandContext(t, context.Background(), source, args...)
}

func runRobotCredentialCommandContext(t *testing.T, ctx context.Context, source *cobra.Command, args ...string) (robotCredentialResponse, error) {
	t.Helper()
	cmd := &cobra.Command{
		Use:           source.Use,
		Args:          source.Args,
		RunE:          source.RunE,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.Flags().Bool("active", false, "")
	cmd.Flags().Bool("compact", false, "")
	cmd.Flags().String("strategy", "smart", "")
	cmd.Flags().Bool("include-cooldown", false, "")
	cmd.Flags().Bool("no-fetch", false, "")
	cmd.Flags().Bool("forecast", false, "")
	cmd.Flags().String("model", "", "")
	cmd.Flags().Duration("timeout", 30*time.Second, "")
	var stdout, stderr bytes.Buffer
	cmd.SetContext(ctx)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()

	var out robotCredentialResponse
	decoder := json.NewDecoder(&stdout)
	if decodeErr := decoder.Decode(&out); decodeErr != nil {
		t.Fatalf("%s %v did not return JSON: %v (handler error: %v, stderr: %s)", source.Name(), args, decodeErr, err, stderr.String())
	}
	if decodeErr := decoder.Decode(new(any)); decodeErr != io.EOF {
		t.Fatalf("%s returned extra output after its JSON result: %v", source.Name(), decodeErr)
	}
	encoded, encodeErr := json.Marshal(out)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if strings.Contains(string(encoded)+stderr.String(), "SYNTHETIC-") {
		t.Fatalf("%s exposed synthetic credential material in output", source.Name())
	}
	return out, err
}

func setupRobotCredentialEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldVault, oldTools, oldProfiles, oldHealth, oldDB := vault, tools, profileStore, healthStore, globalDB
	t.Cleanup(func() {
		if globalDB != nil {
			globalDB.Close()
		}
		vault, tools, profileStore, healthStore, globalDB = oldVault, oldTools, oldProfiles, oldHealth, oldDB
	})
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"CAAM_HOME":         filepath.Join(home, "caam"),
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".config", "claude-code"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"GROK_HOME":         filepath.Join(home, ".grok"),
		"CURSOR_CONFIG_DIR": filepath.Join(home, "cursor"),
	} {
		t.Setenv(key, value)
	}
	vault = authfile.NewVault(authfile.DefaultVaultPath())
	profileStore = nil
	healthStore = health.NewStorage(filepath.Join(home, "health.json"))
	globalDB = nil
	tools = map[string]func() authfile.AuthFileSet{
		"claude":   authfile.ClaudeAuthFiles,
		"codex":    authfile.CodexAuthFiles,
		"grok":     authfile.GrokAuthFiles,
		"cursor":   authfile.CursorAuthFiles,
		"opencode": authfile.OpenCodeAuthFiles,
	}
	return home
}

func writeRobotCredentialProfile(t *testing.T, provider, name string, files map[string]string) {
	t.Helper()
	dir := vault.ProfilePath(provider, name)
	// Even a metadata-only profile is listed by the vault. That must never
	// become evidence of usable credentials on a robot surface (issue #113).
	writeNativeTestCredential(t, filepath.Join(dir, "meta.json"), `{}`)
	for filename, content := range files {
		writeNativeTestCredential(t, filepath.Join(dir, filename), content)
	}
}

type robotQuotaFileState struct {
	contents string
	mode     fs.FileMode
	modified time.Time
}

func snapshotRobotQuotaFiles(t *testing.T, roots ...string) map[string]robotQuotaFileState {
	t.Helper()
	files := make(map[string]robotQuotaFileState)
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			state := robotQuotaFileState{mode: info.Mode(), modified: info.ModTime()}
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				state.contents = string(data)
			}
			files[path] = state
			return nil
		}); err != nil {
			t.Fatalf("snapshot quota command inputs: %v", err)
		}
	}
	return files
}

func requireRobotQuotaFilesUnchanged(t *testing.T, before map[string]robotQuotaFileState, roots ...string) {
	t.Helper()
	if after := snapshotRobotQuotaFiles(t, roots...); !reflect.DeepEqual(before, after) {
		t.Error("read-only robot quota command changed saved profiles or live credentials")
	}
}

func TestRobotPrecheckRequiresMeasuredAvailableQuota(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			_, live := setupSubscriptionQuotaProfiles(t, provider, map[string]string{
				"a-exhausted":  subscriptionQuotaResponse(provider, 100),
				"b-ready":      subscriptionQuotaResponse(provider, 0),
				"c-unreadable": "",
				"_original":    subscriptionQuotaResponse(provider, 0),
			})
			liveCredential := `{"access_token":"SYNTHETIC-LIVE-CREDENTIAL"}`
			if provider == "claude" {
				liveCredential = `{"claudeAiOauth":{"accessToken":"SYNTHETIC-LIVE-CREDENTIAL"}}`
			}
			writeNativeTestCredential(t, live, liveCredential)
			roots := []string{vault.BasePath(), filepath.Dir(live)}
			before := snapshotRobotQuotaFiles(t, roots...)
			transport := http.DefaultTransport
			var requests atomic.Int32
			http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				return transport.RoundTrip(req)
			})

			out, err := runRobotCredentialCommand(t, robotPrecheckCmd, provider)
			if err != nil || !out.Success {
				t.Fatalf("weekly-only measured account was not usable: output=%+v error=%v", out, err)
			}
			var data RobotPrecheckData
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 3 || !data.QuotaChecked || data.Summary.Total != 3 || data.Summary.Ready != 1 || data.Recommended == nil || data.Recommended.Name != "b-ready" || len(data.Backups) != 0 || len(data.Blocked) != 2 {
				t.Fatalf("quota planner routed to unmeasured/exhausted/system profiles: requests=%d data=%+v", requests.Load(), data)
			}
			quota := data.Recommended.Quota
			if quota == nil || !quota.QuotaKnown || !quota.QuotaAvailable || quota.PrimaryPct != nil || quota.SecondaryPct == nil || *quota.SecondaryPct != 0 {
				t.Errorf("planner lost a real weekly zero without a session: %+v", quota)
			}
			if data.Commands.Activate != "caam robot act activate "+provider+" b-ready" {
				t.Errorf("activation command does not use the measured candidate: %q", data.Commands.Activate)
			}
			if data.Commands.Next != "caam robot precheck "+provider || data.Commands.Run != "caam run "+provider+" --precheck -- <args>" {
				t.Errorf("quota planning hints lost read-only planning or launch precheck: %+v", data.Commands)
			}
			for _, blocked := range data.Blocked {
				if blocked.Quota == nil || blocked.Quota.QuotaAvailable || blocked.Name == "b-ready" || len(blocked.Reasons) == 0 {
					t.Errorf("blocked quota lacks a restrictive verdict: %+v", blocked)
				}
			}
			requireRobotQuotaFilesUnchanged(t, before, roots...)
		})
	}
}

func TestRobotPrecheckAppliesRequestedModelAndGeneralQuota(t *testing.T) {
	const scoped = `{"seven_day":{"utilization":20},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`
	for _, tc := range []struct {
		name, model, body, want string
		ready                   int
	}{
		{"unknown model", "", scoped, "b-ready", 1},
		{"matching model", "fable", scoped, "b-ready", 1},
		{"unrelated model", "sonnet", scoped, "a-scoped", 2},
		{"general quota still applies", "sonnet", `{"seven_day":{"utilization":100},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Fable"}}}]}`, "b-ready", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			setupSubscriptionQuotaProfiles(t, "claude", map[string]string{
				"a-scoped": tc.body, "b-ready": subscriptionQuotaResponse("claude", 50),
			})
			before := snapshotRobotQuotaFiles(t, vault.BasePath())
			out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "claude", "--model", tc.model)
			if err != nil || !out.Success {
				t.Fatalf("model-aware precheck failed: %+v, %v", out, err)
			}
			var data RobotPrecheckData
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if !data.QuotaChecked || data.Summary.Ready != tc.ready || data.Recommended == nil || data.Recommended.Name != tc.want || len(data.Backups) != tc.ready-1 || len(data.Blocked) != 2-tc.ready {
				t.Fatalf("model scope or general quota lost during planning: %+v", data)
			}
			if data.Recommended.Quota == nil || !data.Recommended.Quota.QuotaAvailable {
				t.Error("recommended profile lacks measured applicable headroom")
			}
			if data.Model != tc.model || (tc.model != "" && (data.Commands.Next != "caam robot precheck claude --model "+tc.model || data.Commands.Run != "caam run claude --precheck -- --model "+tc.model+" <args>")) {
				t.Errorf("model choice was lost in quota-checked follow-up commands: model=%q commands=%+v", data.Model, data.Commands)
			}
			requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
		})
	}
}

func TestRobotPrecheckUnavailableQuotaCannotBecomeReady(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
	}{
		{"exhausted weekly", "claude", `{"five_hour":null,"seven_day":{"utilization":100}}`},
		{"missing general quota", "claude", `{"five_hour":null,"seven_day":null}`},
		{"model only", "claude", `{"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Fable"}}}]}`},
		{"unknown measured kind", "claude", `{"seven_day":{"utilization":0},"limits":[{"kind":"future_general","percent":50}]}`},
		{"malformed percentage", "claude", `{"seven_day":{"utilization":false}}`},
		{"failed request", "claude", ""},
		{"codex null measurement", "codex", `{"rate_limit":{"secondary_window":{"used_percent":null}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			setupSubscriptionQuotaProfiles(t, tc.provider, map[string]string{"only": tc.body})
			before := snapshotRobotQuotaFiles(t, vault.BasePath())
			out, err := runRobotCredentialCommand(t, robotPrecheckCmd, tc.provider)
			if err == nil || out.Success {
				t.Fatalf("unavailable quota was advertised as ready: %+v, error=%v", out, err)
			}
			var data RobotPrecheckData
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if !data.QuotaChecked || data.Summary.Ready != 0 || data.Recommended != nil || len(data.Backups) != 0 || len(data.Blocked) != 1 || data.Commands.Activate != "" || data.Commands.Run != "" {
				t.Fatalf("quota failure fell back to credential-health routing: %+v", data)
			}
			if quota := data.Blocked[0].Quota; quota == nil || quota.QuotaAvailable {
				t.Errorf("blocked profile lost quota refusal: %+v", quota)
			}
			requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
		})
	}
}

func TestRobotPrecheckNoFetchIsExplicitOfflineAdvice(t *testing.T) {
	setupRobotCredentialEnv(t)
	setupSubscriptionQuotaProfiles(t, "claude", map[string]string{"only": ""})
	var requests atomic.Int32
	http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, fmt.Errorf("offline precheck must not request quota")
	})
	before := snapshotRobotQuotaFiles(t, vault.BasePath())
	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "claude", "--no-fetch", "--timeout=0s")
	if err != nil || !out.Success {
		t.Fatalf("explicit offline credential advice failed: %+v, %v", out, err)
	}
	var data RobotPrecheckData
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || data.QuotaChecked || data.Summary.Ready != 1 || data.Recommended == nil || data.Recommended.Name != "only" || data.Recommended.Quota != nil {
		t.Errorf("offline advice claimed or fetched quota: requests=%d, data=%+v", requests.Load(), data)
	}
	requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
}

func TestRobotLimitsUsesMeasuredQuota(t *testing.T) {
	pct := func(value float64) *float64 { return &value }
	const scoped = `{"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`
	for _, tc := range []struct {
		name, provider, body, model string
		known, available, scoped    bool
		primary, secondary, binding *float64
	}{
		{name: "weekly zero without session", provider: "claude", body: `{"seven_day":{"utilization":0}}`, known: true, available: true, secondary: pct(0), binding: pct(0)},
		{name: "null session", provider: "claude", body: `{"five_hour":null,"seven_day":{"utilization":17.5}}`, known: true, available: true, secondary: pct(17.5), binding: pct(17.5)},
		{name: "null session measurement", provider: "claude", body: `{"five_hour":{"utilization":null},"seven_day":{"utilization":25}}`, known: true, available: true, secondary: pct(25), binding: pct(25)},
		{name: "null session entry", provider: "claude", body: `{"limits":[{"kind":"session","percent":null},{"kind":"weekly_all","percent":0}]}`, known: true, available: true, secondary: pct(0), binding: pct(0)},
		{name: "conventional session", provider: "claude", body: `{"five_hour":{"utilization":42},"seven_day":{"utilization":10}}`, known: true, available: true, primary: pct(42), secondary: pct(10), binding: pct(42)},
		{name: "weekly exhausted beside measured session zero", provider: "claude", body: `{"five_hour":{"utilization":0},"seven_day":{"utilization":100}}`, known: true, primary: pct(0), secondary: pct(100), binding: pct(100)},
		{name: "unknown model keeps every restriction", provider: "claude", body: scoped, known: true, scoped: true, secondary: pct(0), binding: pct(100)},
		{name: "matching model exhausted", provider: "claude", body: scoped, model: "fable", known: true, scoped: true, secondary: pct(0), binding: pct(100)},
		{name: "unrelated model available", provider: "claude", body: scoped, model: "sonnet", known: true, available: true, scoped: true, secondary: pct(0), binding: pct(0)},
		{name: "model only remains unknown", provider: "claude", body: `{"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Fable"}}}]}`, scoped: true},
		{name: "unrepresented general constraint", provider: "claude", body: `{"seven_day":{"utilization":0},"limits":[{"kind":"future_general","percent":50}]}`},
		{name: "malformed measurement", provider: "claude", body: `{"seven_day":{"utilization":"0"}}`},
		{name: "missing quota", provider: "claude", body: `{}`},
		{name: "null document", provider: "claude", body: `null`},
		{name: "provider failure", provider: "claude"},
		{name: "codex weekly zero", provider: "codex", body: `{"rate_limit":{"secondary_window":{"used_percent":0}}}`, known: true, available: true, secondary: pct(0), binding: pct(0)},
		{name: "codex credits without quota", provider: "codex", body: `{"credits":{"has_credits":true,"balance":"12.50"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			_, live := setupSubscriptionQuotaProfiles(t, tc.provider, map[string]string{
				"candidate": tc.body, "_original": subscriptionQuotaResponse(tc.provider, 0),
			})
			liveCredential := `{"access_token":"SYNTHETIC-LIVE-CREDENTIAL"}`
			if tc.provider == "claude" {
				liveCredential = `{"claudeAiOauth":{"accessToken":"SYNTHETIC-LIVE-CREDENTIAL"}}`
			}
			writeNativeTestCredential(t, live, liveCredential)
			credentialFile, shadow := "auth.json", `{"access_token":"SYNTHETIC-OTHER-VAULT"}`
			if tc.provider == "claude" {
				credentialFile, shadow = ".credentials.json", `{"claudeAiOauth":{"accessToken":"SYNTHETIC-OTHER-VAULT"}}`
			}
			writeNativeTestCredential(t, filepath.Join(authfile.DefaultVaultPath(), tc.provider, "candidate", credentialFile), shadow)
			roots := []string{vault.BasePath(), authfile.DefaultVaultPath(), filepath.Dir(live)}
			before := snapshotRobotQuotaFiles(t, roots...)
			transport := http.DefaultTransport
			var requests atomic.Int32
			http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
				requests.Add(1)
				return transport.RoundTrip(req)
			})

			out, err := runRobotCredentialCommand(t, robotLimitsCmd, tc.provider, "--forecast", "--model", tc.model)
			if out.Success != tc.known || (err == nil) != tc.known {
				t.Fatalf("limits success=%v error=%v; quota known=%v", out.Success, err, tc.known)
			}
			if !tc.known && (out.Error == nil || out.Error.Code != "QUOTA_UNAVAILABLE") {
				t.Errorf("unmeasured limits need an explicit error: %+v", out.Error)
			}
			if requests.Load() != 1 {
				t.Errorf("usage requests=%d, want exactly the selected user profile", requests.Load())
			}
			var data RobotLimitsData
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if !data.QuotaChecked || len(data.Profiles) != 1 {
				t.Fatalf("limits omitted the requested check or admitted a system snapshot: %+v", data)
			}
			got := data.Profiles[0]
			if got.Name != "candidate" || got.QuotaKnown != tc.known || got.QuotaAvailable != tc.available {
				t.Errorf("quota verdict disagrees with provider measurements: %+v", got)
			}
			if !reflect.DeepEqual(got.PrimaryPct, tc.primary) || !reflect.DeepEqual(got.SecondaryPct, tc.secondary) || !reflect.DeepEqual(got.BindingPct, tc.binding) {
				t.Errorf("optional or measured-zero quota was lost: primary=%v secondary=%v binding=%v", got.PrimaryPct, got.SecondaryPct, got.BindingPct)
			}
			var raw struct {
				Profiles []map[string]json.RawMessage `json:"profiles"`
			}
			if err := json.Unmarshal(out.Data, &raw); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]*float64{"primary_percent": tc.primary, "secondary_percent": tc.secondary, "binding_percent": tc.binding} {
				_, present := raw.Profiles[0][key]
				if present != (want != nil) {
					t.Errorf("JSON %s presence=%v, want %v; unknown must be omitted and measured zero retained", key, present, want != nil)
				}
			}
			if !tc.known && (got.AvailScore != 0 || got.Error == "") {
				t.Errorf("unknown quota was promoted to an availability score: %+v", got)
			}
			if !tc.available && strings.Contains(got.Recommendation, "ready to use") {
				t.Errorf("unavailable quota received ready advice: %+v", got)
			}
			if got.Usage == nil || got.Usage.Provider != tc.provider {
				t.Fatalf("nested provider evidence is missing: %+v", got)
			}
			if tc.scoped && (got.Usage.ModelWindows["Fable"] == nil || len(got.Usage.ScopedWindows) == 0) {
				t.Error("model filtering discarded the underlying model quota evidence")
			}
			if tc.name == "codex credits without quota" && (got.Usage.Credits == nil || got.Usage.Credits.Balance == nil || *got.Usage.Credits.Balance != 12.5) {
				t.Error("unknown numeric quota discarded the credit balance")
			}
			if got.DepletesIn != "" || got.BurnRate != "" {
				t.Errorf("--forecast invented a rate or ETA from percentages alone: %+v", got)
			}
			if tc.known && tc.binding != nil && *tc.binding < 100 && (got.ForecastError == "" || got.Error != "") {
				t.Errorf("missing forecast data invalidated a measured quota or concealed uncertainty: %+v", got)
			}
			requireRobotQuotaFilesUnchanged(t, before, roots...)
		})
	}
}

func TestRobotQuotaFailuresAndCancellationStayUnknown(t *testing.T) {
	for _, source := range []*cobra.Command{robotLimitsCmd, robotPrecheckCmd} {
		for _, provider := range []string{"claude", "codex"} {
			for _, failure := range []string{"unauthorized", "network", "timeout", "caller canceled"} {
				t.Run(source.Name()+"/"+provider+"/"+failure, func(t *testing.T) {
					setupRobotCredentialEnv(t)
					setupSubscriptionQuotaProfiles(t, provider, map[string]string{"only": subscriptionQuotaResponse(provider, 0)})
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var requests atomic.Int32
					var contextObserved atomic.Bool
					http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
						requests.Add(1)
						switch failure {
						case "unauthorized":
							return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":"invalid token"}`)), Header: make(http.Header), Request: req}, nil
						case "network":
							return nil, errors.New("quota transport unavailable")
						case "timeout":
							deadline, ok := req.Context().Deadline()
							if !ok || time.Until(deadline) > 200*time.Millisecond {
								return nil, errors.New("requested quota timeout was not propagated")
							}
							<-req.Context().Done()
							contextObserved.Store(errors.Is(req.Context().Err(), context.DeadlineExceeded))
							return nil, req.Context().Err()
						default:
							cancel()
							contextObserved.Store(errors.Is(req.Context().Err(), context.Canceled))
							return nil, errors.New("quota request canceled")
						}
					})
					before := snapshotRobotQuotaFiles(t, vault.BasePath())
					out, err := runRobotCredentialCommandContext(t, ctx, source, provider, "--timeout=50ms")
					if err == nil || out.Success || out.Error == nil || requests.Load() != 1 {
						t.Fatalf("failed quota request was not refused: requests=%d output=%+v error=%v", requests.Load(), out, err)
					}
					if (failure == "timeout" || failure == "caller canceled") && !contextObserved.Load() {
						t.Error("quota transport did not receive the caller cancellation or requested deadline")
					}
					if failure == "caller canceled" && out.Error.Code != "REQUEST_CANCELED" {
						t.Errorf("caller cancellation was hidden: %+v", out.Error)
					}
					var quota *RobotProfileLimits
					if source == robotLimitsCmd {
						var data RobotLimitsData
						if err := json.Unmarshal(out.Data, &data); err != nil {
							t.Fatal(err)
						}
						if !data.QuotaChecked || len(data.Profiles) != 1 {
							t.Fatalf("failed quota request lost its diagnostic row: %+v", data)
						}
						quota = &data.Profiles[0]
					} else {
						var data RobotPrecheckData
						if err := json.Unmarshal(out.Data, &data); err != nil {
							t.Fatal(err)
						}
						if !data.QuotaChecked || data.Summary.Ready != 0 || data.Recommended != nil || len(data.Backups) != 0 || len(data.Blocked) != 1 || data.Commands.Activate != "" || data.Commands.Run != "" {
							t.Fatalf("failed quota request still supplied a launch recommendation: %+v", data)
						}
						quota = data.Blocked[0].Quota
					}
					if quota == nil || quota.QuotaKnown || quota.QuotaAvailable || quota.AvailScore != 0 || quota.PrimaryPct != nil || quota.SecondaryPct != nil || quota.BindingPct != nil || quota.Error == "" {
						t.Errorf("failed request became numeric quota or readiness: %+v", quota)
					}
					requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
				})
			}
		}
	}
}

func TestRobotQuotaHonorsSavedCredentialSelection(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		files          map[string]string
	}{
		{"malformed primary blocks legacy", "claude", map[string]string{
			".credentials.json": `{"claudeAiOauth":`,
			".claude.json":      `{"oauthToken":"SYNTHETIC-UNSELECTED-LEGACY"}`,
		}},
		{"settings select helper instead of cached OAuth", "claude", map[string]string{
			"settings.json":     `{"apiKeyHelper":"SYNTHETIC-HELPER-MUST-NOT-RUN"}`,
			".credentials.json": `{"claudeAiOauth":{"accessToken":"SYNTHETIC-UNSELECTED-OAUTH"}}`,
		}},
		{"API key mode blocks cached subscription OAuth", "codex", map[string]string{
			"auth.json": `{"auth_mode":"apikey","OPENAI_API_KEY":"SYNTHETIC-SELECTED-KEY","tokens":{"access_token":"SYNTHETIC-UNSELECTED-OAUTH"}}`,
		}},
	} {
		for _, source := range []*cobra.Command{robotLimitsCmd, robotPrecheckCmd} {
			t.Run(tc.name+"/"+source.Name(), func(t *testing.T) {
				setupRobotCredentialEnv(t)
				writeRobotCredentialProfile(t, tc.provider, "only", tc.files)
				oldTransport := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = oldTransport })
				var requests atomic.Int32
				http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
					requests.Add(1)
					return nil, errors.New("unselected credential must not be queried")
				})
				before := snapshotRobotQuotaFiles(t, vault.BasePath())
				out, err := runRobotCredentialCommand(t, source, tc.provider)
				if err == nil || out.Success || requests.Load() != 0 {
					t.Fatalf("unselected credential established subscription availability: requests=%d output=%+v error=%v", requests.Load(), out, err)
				}
				if source == robotLimitsCmd {
					var data RobotLimitsData
					if err := json.Unmarshal(out.Data, &data); err != nil {
						t.Fatal(err)
					}
					if len(data.Profiles) != 1 || data.Profiles[0].QuotaKnown || data.Profiles[0].QuotaAvailable || data.Profiles[0].AvailScore != 0 || data.Profiles[0].Error == "" {
						t.Errorf("unreadable subscription credentials were silently omitted or counted as available: %+v", data)
					}
				} else {
					var data RobotPrecheckData
					if err := json.Unmarshal(out.Data, &data); err != nil {
						t.Fatal(err)
					}
					if !data.QuotaChecked || data.Summary.Ready != 0 || data.Recommended != nil || len(data.Backups) != 0 || len(data.Blocked) != 1 || data.Commands.Activate != "" || data.Commands.Run != "" {
						t.Errorf("credential selection refusal fell back to unchecked planning: %+v", data)
					}
				}
				requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
			})
		}
	}
}

func TestRobotQuotaUnsupportedProviderNeedsExplicitOfflineAdvice(t *testing.T) {
	setupRobotCredentialEnv(t)
	writeRobotCredentialProfile(t, "opencode", "only", map[string]string{
		"auth.json": `{"anthropic":{"type":"api","key":"SYNTHETIC-OPENCODE-KEY"}}`,
	})
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var requests atomic.Int32
	http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unsupported provider must not be queried")
	})
	before := snapshotRobotQuotaFiles(t, vault.BasePath())
	for _, source := range []*cobra.Command{robotLimitsCmd, robotPrecheckCmd} {
		out, err := runRobotCredentialCommand(t, source, "opencode")
		if err == nil || out.Success {
			t.Errorf("%s claimed unsupported quota was available: %+v, %v", source.Name(), out, err)
		}
		if source == robotPrecheckCmd {
			var data RobotPrecheckData
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if !data.QuotaChecked || data.Summary.Ready != 0 || data.Recommended != nil || len(data.Blocked) != 1 || data.Commands.Activate != "" || data.Commands.Run != "" || data.Commands.Next != "caam robot precheck opencode" {
				t.Errorf("unsupported quota was disguised as guarded launch advice: %+v", data)
			}
		}
	}
	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "opencode", "--no-fetch")
	if err != nil || !out.Success {
		t.Fatalf("explicit credential-only planning was not retained: %+v, %v", out, err)
	}
	var data RobotPrecheckData
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.QuotaChecked || data.Summary.Ready != 1 || data.Recommended == nil || data.Recommended.Quota != nil || requests.Load() != 0 {
		t.Errorf("offline advice claimed provider quota: requests=%d data=%+v", requests.Load(), data)
	}
	requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
}

func TestRobotPrecheckQuotaCannotOverrideCredentialOrCooldownBlocks(t *testing.T) {
	setupRobotCredentialEnv(t)
	setupSubscriptionQuotaProfiles(t, "codex", map[string]string{
		"a-rejected": subscriptionQuotaResponse("codex", 0),
		"b-cooled":   subscriptionQuotaResponse("codex", 0),
		"c-ready":    subscriptionQuotaResponse("codex", 20),
	})
	healthStore = health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
	healthStore.SetVaultPath(vault.BasePath())
	credential, err := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "a-rejected"), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := healthStore.RecordProviderVerification("codex", "a-rejected", health.ProviderVerification{
		Reason: "refresh_token_invalidated", Fingerprint: health.CodexCredentialFingerprint(credential),
	}); err != nil {
		t.Fatal(err)
	}
	db, err := caamdb.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, cooldownErr := db.SetCooldown("codex", "b-cooled", time.Now(), time.Hour, "synthetic provider restriction")
	closeErr := db.Close()
	if cooldownErr != nil || closeErr != nil {
		t.Fatalf("could not establish cooldown: %v, %v", cooldownErr, closeErr)
	}
	transport := http.DefaultTransport
	var requests atomic.Int32
	http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return transport.RoundTrip(req)
	})
	before := snapshotRobotQuotaFiles(t, vault.BasePath())
	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "codex")
	if err != nil || !out.Success {
		t.Fatalf("remaining usable account was not recommended: %+v, %v", out, err)
	}
	var data RobotPrecheckData
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	if !data.QuotaChecked || data.Summary.Total != 3 || data.Summary.Ready != 1 || data.Recommended == nil || data.Recommended.Name != "c-ready" || len(data.Backups) != 0 || len(data.Blocked) != 1 || data.Blocked[0].Name != "a-rejected" || len(data.InCooldown) != 1 || data.InCooldown[0].Name != "b-cooled" {
		t.Errorf("numeric headroom overrode credential rejection or cooldown: %+v", data)
	}
	if requests.Load() != 1 {
		t.Errorf("quota requested %d profiles; only the launchable candidate should be queried", requests.Load())
	}
	requireRobotQuotaFilesUnchanged(t, before, vault.BasePath())
}

func TestRobotLimitForecastKeepsModelAndResetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, model           string
		forecast, resetsFirst bool
		available, wantETA    bool
	}{
		{"forecast not requested", "sonnet", false, false, true, false},
		{"unrelated model leaves weekly forecast", "sonnet", true, false, true, true},
		{"matching model already exhausted", "fable", true, false, false, false},
		{"unspecified model keeps exhaustion", "", true, false, false, false},
		{"weekly reset ends forecast horizon", "sonnet", true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &usage.UsageInfo{
				Provider: "claude", ProfileName: "candidate",
				SecondaryWindow: &usage.UsageWindow{UsedPercent: 20},
				ModelWindows: map[string]*usage.UsageWindow{
					"fable": {Label: "Fable", UsedPercent: 100},
				},
				BurnRate: &usage.BurnRateInfo{PercentPerHour: 20, Confidence: 0.9, SampleSize: 10},
			}
			if tc.resetsFirst {
				info.SecondaryWindow.ResetsAt = time.Now().Add(time.Hour)
			}
			got := buildRobotProfileLimits("candidate", info, tc.model, tc.forecast)
			if !got.QuotaKnown || got.QuotaAvailable != tc.available || got.Error != "" || got.Usage != info || got.SecondaryPct == nil || *got.SecondaryPct != 20 {
				t.Fatalf("forecast handling altered the measured quota verdict: %+v", got)
			}
			if tc.wantETA {
				if got.DepletesIn != robotFormatDuration(4*time.Hour) || got.BurnRate != "20.00%/hour" || got.ForecastConfidence == nil || got.ForecastError != "" {
					t.Errorf("valid model-aware forecast missing: %+v", got)
				}
			} else if got.DepletesIn != "" || got.BurnRate != "" || got.ForecastConfidence != nil {
				t.Errorf("forecast advertised beyond the requested model/window: %+v", got)
			}
			if tc.resetsFirst && got.ForecastError == "" {
				t.Error("reset-before-depletion uncertainty was omitted")
			}
		})
	}
}

type robotClaudeCredentialCase struct {
	name         string
	files        map[string]string
	valid        bool
	renewable    bool
	expired      bool
	missingOrBad bool
}

func robotClaudeCredentialCases(now time.Time) []robotClaudeCredentialCase {
	return []robotClaudeCredentialCase{
		{name: "metadata-only", missingOrBad: true},
		{name: "identity-only", files: map[string]string{".claude.json": `{"oauthAccount":{"emailAddress":"other@example.invalid","accountUuid":"other-account"}}`}, missingOrBad: true},
		{name: "settings-only", files: map[string]string{".claude.json": `{"theme":"dark","hasCompletedOnboarding":true}`}, missingOrBad: true},
		{name: "malformed-json", files: map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"SYNTHETIC-PARSE-SECRET"`}, missingOrBad: true},
		{name: "null-document", files: map[string]string{".credentials.json": `null`}, missingOrBad: true},
		{name: "empty-document", files: map[string]string{".credentials.json": `{}`}, missingOrBad: true},
		{name: "null-oauth", files: map[string]string{".credentials.json": `{"claudeAiOauth":null}`}, missingOrBad: true},
		{name: "empty-token", files: map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":" "}}`}, missingOrBad: true},
		{name: "wrong-token-type", files: map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":{"value":"SYNTHETIC-WRONG-TYPE"}}}`}, missingOrBad: true},
		{name: "expiry-without-token", files: map[string]string{".credentials.json": fmt.Sprintf(`{"claudeAiOauth":{"expiresAt":%d}}`, now.Add(time.Hour).UnixMilli())}, missingOrBad: true},
		{name: "opaque-access", files: map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"SYNTHETIC-OPAQUE-ACCESS"}}`}, valid: true},
		{name: "legacy-access", files: map[string]string{".claude.json": `{"oauthToken":"SYNTHETIC-LEGACY-ACCESS"}`}, valid: true},
		{name: "expired-renewable", files: map[string]string{".credentials.json": fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-EXPIRED-ACCESS","refreshToken":"SYNTHETIC-RENEWABLE","expiresAt":%d}}`, now.Add(-time.Hour).UnixMilli())}, valid: true, renewable: true, expired: true},
		{name: "expired-access-only", files: map[string]string{".credentials.json": fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-EXPIRED-ACCESS","expiresAt":%d}}`, now.Add(-time.Hour).UnixMilli())}, expired: true},
		{name: "expired-blank-refresh", files: map[string]string{".credentials.json": fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-EXPIRED-ACCESS","refreshToken":" \t\n ","expiresAt":%d}}`, now.Add(-time.Hour).UnixMilli())}, expired: true},
	}
}

func TestRobotCredentialValidateRequiresSavedCredentials(t *testing.T) {
	for _, tc := range robotClaudeCredentialCases(time.Now()) {
		t.Run(tc.name, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			writeRobotCredentialProfile(t, "claude", "candidate", tc.files)
			out, err := runRobotCredentialCommand(t, robotValidateCmd, "claude", "candidate")
			if tc.valid && err != nil {
				t.Fatalf("valid credential validation failed: %v", err)
			}
			var data RobotValidateData
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Method != "passive" || data.Summary.Total != 1 || len(data.Profiles) != 1 {
				t.Fatalf("unexpected validation result: %+v", data)
			}
			result := data.Profiles[0]
			if result.Provider != "claude" || result.Profile != "candidate" || result.Valid != tc.valid || out.Success != tc.valid {
				t.Fatalf("validation = %+v (success=%v), want valid=%v", result, out.Success, tc.valid)
			}
			wantValid := 0
			if tc.valid {
				wantValid = 1
			}
			if data.Summary.Valid != wantValid || data.Summary.Invalid != 1-wantValid {
				t.Errorf("summary disagrees with the credential verdict: %+v", data.Summary)
			}
			if !tc.valid {
				if result.Error == "" || result.LaunchUsable == nil || *result.LaunchUsable || result.LoginRequired == nil || !*result.LoginRequired || result.RefreshDue == nil || *result.RefreshDue {
					t.Errorf("invalid credential is not explicitly blocked: %+v", result)
				}
			} else if tc.renewable {
				if result.LaunchUsable == nil || !*result.LaunchUsable || result.LoginRequired == nil || *result.LoginRequired {
					t.Errorf("expired renewable credential should remain launchable: %+v", result)
				}
			} else if result.ExpiresAt != "" || result.ExpiresIn != "" || result.LaunchUsable != nil || result.LoginRequired != nil {
				t.Errorf("unknown expiry must remain unknown for a genuine credential: %+v", result)
			}
			if tc.expired && result.ExpiresIn != "expired" {
				t.Errorf("access-token expiry should remain visible: %+v", result)
			}
			plain, _ := validateVaultProfile("claude", "candidate")
			if plain.Valid != result.Valid {
				t.Errorf("plain validate and robot validate disagree: plain=%+v, robot=%+v", plain, result)
			}
		})
	}
}

func TestRobotCredentialValidateDoesNotClaimMissingOrActiveValidation(t *testing.T) {
	setupRobotCredentialEnv(t)
	writeRobotCredentialProfile(t, "claude", "saved", map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"SYNTHETIC-OPAQUE-ACCESS"}}`,
	})
	for _, tc := range []struct {
		name string
		args []string
		code string
	}{
		{"missing-profile", []string{"claude", "missing"}, "PROFILE_NOT_FOUND"},
		{"active-unavailable", []string{"claude", "saved", "--active"}, "ACTIVE_VALIDATION_UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runRobotCredentialCommand(t, robotValidateCmd, tc.args...)
			if err == nil || out.Success || out.Error == nil || out.Error.Code != tc.code {
				t.Fatalf("validation = %+v, error=%v; want explicit %s refusal", out, err, tc.code)
			}
		})
	}
}

func TestRobotCredentialSelectionExcludesUnusableProfiles(t *testing.T) {
	setupRobotCredentialEnv(t)
	valid := make(map[string]bool)
	for _, tc := range robotClaudeCredentialCases(time.Now()) {
		writeRobotCredentialProfile(t, "claude", tc.name, tc.files)
		if tc.valid {
			valid[tc.name] = true
		}
	}
	writeClaudeVaultProfile(t, "_original", 30*24*time.Hour, true)
	writeClaudeVaultProfile(t, "_backup_safety", 30*24*time.Hour, true)

	for _, args := range [][]string{{"claude"}, {"claude", "--include-cooldown"}} {
		out, err := runRobotCredentialCommand(t, robotNextCmd, args...)
		if err != nil || !out.Success {
			t.Fatalf("next failed with usable credentials: %+v, %v", out, err)
		}
		var data RobotNextData
		if err := json.Unmarshal(out.Data, &data); err != nil {
			t.Fatal(err)
		}
		if !valid[data.Profile] || data.AlternateChoice == nil || !valid[data.AlternateChoice.Profile] {
			t.Errorf("next suggested an unusable or system profile: %+v", data)
		}
	}

	// This test covers offline credential admission, independently of quota.
	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "claude", "--no-fetch")
	if err != nil || !out.Success {
		t.Fatalf("precheck failed with usable credentials: %+v, %v", out, err)
	}
	var data RobotPrecheckData
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Recommended == nil || !valid[data.Recommended.Name] || data.Summary.Ready != len(valid) {
		t.Fatalf("precheck advertised invalid ready profiles: %+v", data)
	}
	seen := map[string]bool{data.Recommended.Name: true}
	for _, backup := range data.Backups {
		if !valid[backup.Name] || seen[backup.Name] {
			t.Errorf("precheck backup is invalid or duplicated: %+v", backup)
		}
		seen[backup.Name] = true
	}
	if len(seen) != len(valid) {
		t.Errorf("precheck dropped a usable candidate: got %v, want %v", seen, valid)
	}
}

func TestRobotCredentialSelectionFailsWhenNoAccountCanLaunch(t *testing.T) {
	setupRobotCredentialEnv(t)
	for _, tc := range robotClaudeCredentialCases(time.Now()) {
		if !tc.valid {
			writeRobotCredentialProfile(t, "claude", tc.name, tc.files)
		}
	}
	writeClaudeVaultProfile(t, "_original", 30*24*time.Hour, true)
	for _, args := range [][]string{{"claude"}, {"claude", "--include-cooldown"}} {
		out, err := runRobotCredentialCommand(t, robotNextCmd, args...)
		if err == nil || out.Success || out.Error == nil || out.Error.Code != "ALL_BLOCKED" {
			t.Errorf("next should reject an unusable pool: %+v, error=%v", out, err)
		}
	}
	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "claude", "--no-fetch")
	if err == nil || out.Success {
		t.Errorf("precheck should report no launchable accounts: %+v, error=%v", out, err)
	}
	var data RobotPrecheckData
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Summary.Ready != 0 || data.Recommended != nil || len(data.Backups) != 0 || data.Commands.Activate != "" || len(data.Alerts) == 0 {
		t.Errorf("precheck fabricated a ready account or omitted the alert: %+v", data)
	}
}

func TestRobotCredentialActivateRefusalPreservesLiveAuthAndSettings(t *testing.T) {
	for _, tc := range robotClaudeCredentialCases(time.Now()) {
		if !tc.missingOrBad {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			configDir := os.Getenv("CLAUDE_CONFIG_DIR")
			live := map[string]string{
				filepath.Join(configDir, ".credentials.json"): `{"claudeAiOauth":{"accessToken":"SYNTHETIC-LIVE-ACCESS","refreshToken":"SYNTHETIC-LIVE-REFRESH"}}`,
				filepath.Join(configDir, ".claude.json"):      `{"oauthAccount":{"emailAddress":"current@example.invalid","accountUuid":"current-account"},"theme":"light","hasCompletedOnboarding":true}`,
				filepath.Join(configDir, "settings.json"):     `{"permissions":{"allow":["Read"]},"env":{"SHARED_SETTING":"keep-me"}}`,
			}
			for path, content := range live {
				writeNativeTestCredential(t, path, content)
			}
			writeRobotCredentialProfile(t, "claude", "candidate", tc.files)
			out, err := runRobotCredentialCommand(t, robotActCmd, "activate", "claude", "candidate")
			if err == nil || out.Success || out.Error == nil {
				t.Errorf("activation falsely succeeded: %+v, error=%v", out, err)
			}
			for path, before := range live {
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read live file after refused activation: %v", err)
				}
				if string(after) != before {
					t.Errorf("refused activation changed live file %s", path)
				}
			}
		})
	}
}

func TestRobotCredentialActivateSwitchesUsableCredentials(t *testing.T) {
	for _, tc := range robotClaudeCredentialCases(time.Now()) {
		if !tc.valid || tc.name == "legacy-access" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			livePath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
			const liveCredential = `{"claudeAiOauth":{"accessToken":"SYNTHETIC-CURRENT-ACCESS"}}`
			writeNativeTestCredential(t, livePath, liveCredential)
			writeRobotCredentialProfile(t, "claude", "candidate", tc.files)
			// Activation accepts these exact previously verified grants even
			// when expired; syntax or a renewable token alone proves no owner.
			for _, proof := range []struct {
				path, credentials, organization string
			}{
				{vault.BackupPath("claude", "candidate", "meta.json"), tc.files[".credentials.json"], "candidate-org"},
				{filepath.Join(filepath.Dir(livePath), identity.ClaudeIdentityCacheFile), liveCredential, "current-org"},
			} {
				binding := claudeCLIIdentityBinding(t, []byte(proof.credentials), proof.organization, "Robot fixture")
				metadata, err := json.Marshal(map[string]any{"claude_identity": binding})
				if err != nil {
					t.Fatal(err)
				}
				writeNativeTestCredential(t, proof.path, string(metadata))
			}
			out, err := runRobotCredentialCommand(t, robotActCmd, "activate", "claude", "candidate")
			if err != nil || !out.Success || out.Error != nil {
				t.Fatalf("usable credential activation failed: %+v, error=%v", out, err)
			}
			var data RobotActResult
			if err := json.Unmarshal(out.Data, &data); err != nil {
				t.Fatal(err)
			}
			if !data.Success || data.Action != "activate" || data.Provider != "claude" || data.Profile != "candidate" {
				t.Errorf("activation result did not identify the completed switch: %+v", data)
			}
			after, err := os.ReadFile(livePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != tc.files[".credentials.json"] {
				t.Error("successful activation did not install the selected credential")
			}
		})
	}
}

func TestRobotCredentialActivationPreservesOutgoingOwner(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		liveAccount string
		wantNamed   bool
		wantBackup  bool
	}{
		{"rotated-owner", "smart", "work", true, false},
		{"rotated-owner-without-system-backups", "never", "work", true, false},
		{"unsaved-login", "smart", "unsaved", false, true},
		{"explicitly-disabled-backups", "never", "unsaved", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupRobotCredentialEnv(t)
			writeNativeTestCredential(t, filepath.Join(home, "caam", "config.yaml"),
				fmt.Sprintf("version: 1\nsafety:\n  auto_backup_before_switch: %s\n  max_auto_backups: 5\n", tc.mode))
			now := time.Now().UTC().Truncate(time.Second)
			saved := codexSwitchTestCredential(t, "work", "SAVED-REFRESH", now.Add(-time.Hour))
			live := codexSwitchTestCredential(t, tc.liveAccount, "LIVE-REFRESH", now)
			target := codexSwitchTestCredential(t, "personal", "TARGET-REFRESH", now)
			writeRobotCredentialProfile(t, "codex", "work", map[string]string{"auth.json": saved})
			writeRobotCredentialProfile(t, "codex", "personal", map[string]string{"auth.json": target})
			livePath := filepath.Join(home, ".codex", "auth.json")
			writeNativeTestCredential(t, livePath, live)

			out, err := runRobotCredentialCommand(t, robotActCmd, "activate", "codex", "personal")
			if err != nil || !out.Success {
				t.Fatalf("robot activation failed: %+v, %v", out, err)
			}
			var result RobotActResult
			if err := json.Unmarshal(out.Data, &result); err != nil {
				t.Fatal(err)
			}
			if !result.Success || (result.AutoBackup != "") != tc.wantBackup {
				t.Fatalf("unexpected robot preservation result: %+v", result)
			}
			wantSaved := saved
			if tc.wantNamed {
				wantSaved = live
				if result.ResnapshottedProfile != "work" || result.OldProfile != "work" {
					t.Fatalf("robot did not report the saved outgoing owner: %+v", result)
				}
			} else if result.ResnapshottedProfile != "" {
				t.Fatalf("robot attributed an unknown login to a saved account: %+v", result)
			}
			requireSwitchCredential(t, vault.BackupPath("codex", "work", "auth.json"), wantSaved)
			requireSwitchCredential(t, vault.BackupPath("codex", "personal", "auth.json"), target)
			requireSwitchCredential(t, livePath, target)
			if tc.wantBackup {
				requireSwitchCredential(t, vault.BackupPath("codex", result.AutoBackup, "auth.json"), live)
			}
		})
	}
}

func TestRobotCredentialProviderRejectionBlocksRenewableAccount(t *testing.T) {
	setupRobotCredentialEnv(t)
	expiry := time.Now().Add(9 * 24 * time.Hour)
	revoked := writeCodexVaultAuth(t, vault.BasePath(), "revoked", "SYNTHETIC-REVOKED-REFRESH", expiry)
	writeCodexVaultAuth(t, vault.BasePath(), "healthy", "SYNTHETIC-HEALTHY-REFRESH", expiry)
	if err := healthStore.RecordProviderVerification("codex", "revoked", health.ProviderVerification{
		Reason:      "refresh_token_invalidated",
		Fingerprint: health.CodexCredentialFingerprint(revoked),
	}); err != nil {
		t.Fatal(err)
	}

	out, _ := runRobotCredentialCommand(t, robotValidateCmd, "codex", "revoked")
	var validated RobotValidateData
	if err := json.Unmarshal(out.Data, &validated); err != nil {
		t.Fatal(err)
	}
	if out.Success || len(validated.Profiles) != 1 {
		t.Fatalf("revoked profile falsely validated: %+v", validated)
	}
	result := validated.Profiles[0]
	if result.Valid || result.LaunchUsable == nil || *result.LaunchUsable || result.LoginRequired == nil || !*result.LoginRequired || !strings.Contains(result.Error, "provider rejected") {
		t.Errorf("refresh-token presence hid provider rejection: %+v", result)
	}

	out, err := runRobotCredentialCommand(t, robotNextCmd, "codex", "--include-cooldown")
	if err != nil {
		t.Fatal(err)
	}
	var selected RobotNextData
	if err := json.Unmarshal(out.Data, &selected); err != nil {
		t.Fatal(err)
	}
	if selected.Profile != "healthy" || selected.AlternateChoice != nil {
		t.Errorf("revoked credential remained a routing candidate: %+v", selected)
	}

	out, err = runRobotCredentialCommand(t, robotPrecheckCmd, "codex", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	var planned RobotPrecheckData
	if err := json.Unmarshal(out.Data, &planned); err != nil {
		t.Fatal(err)
	}
	if planned.Summary.Ready != 1 || planned.Recommended == nil || planned.Recommended.Name != "healthy" || len(planned.Backups) != 0 {
		t.Errorf("precheck counted a provider-rejected credential as ready: %+v", planned)
	}
}

func TestRobotCredentialUnnamedBackupPreservesNamedActiveAccount(t *testing.T) {
	setupRobotCredentialEnv(t)
	livePath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
	credential := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-NAMED-ACCOUNT","refreshToken":"SYNTHETIC-NAMED-REFRESH","expiresAt":%d}}`, time.Now().Add(24*time.Hour).UnixMilli())
	writeNativeTestCredential(t, livePath, credential)
	fileSet := authfile.ClaudeAuthFiles()
	if err := vault.Backup(fileSet, "work"); err != nil {
		t.Fatal(err)
	}

	out, err := runRobotCredentialCommand(t, robotActCmd, "backup", "claude")
	if err != nil || !out.Success {
		t.Fatalf("unnamed backup failed: %+v, error=%v", out, err)
	}
	var backup RobotActResult
	if err := json.Unmarshal(out.Data, &backup); err != nil {
		t.Fatal(err)
	}
	if !backup.Success || backup.Action != "backup" || !strings.HasPrefix(backup.Profile, "_backup_") || !authfile.IsSystemProfile(backup.Profile) {
		t.Fatalf("automatic backup is not a system snapshot: %+v", backup)
	}
	active, err := vault.ActiveProfile(fileSet)
	if err != nil || active != "work" {
		t.Errorf("safety snapshot replaced the named active account: active=%q, error=%v", active, err)
	}
	for _, path := range []string{livePath, filepath.Join(vault.ProfilePath("claude", backup.Profile), ".credentials.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != credential {
			t.Errorf("backup changed the credential at %s", path)
		}
	}

	out, err = runRobotCredentialCommand(t, robotNextCmd, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var next RobotNextData
	if err := json.Unmarshal(out.Data, &next); err != nil {
		t.Fatal(err)
	}
	if next.Profile != "work" || next.AlternateChoice != nil {
		t.Errorf("safety snapshot became a routing candidate: %+v", next)
	}
	out, err = runRobotCredentialCommand(t, robotPrecheckCmd, "claude", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	var precheck RobotPrecheckData
	if err := json.Unmarshal(out.Data, &precheck); err != nil {
		t.Fatal(err)
	}
	if precheck.Summary.Ready != 1 || precheck.Recommended == nil || precheck.Recommended.Name != "work" || len(precheck.Backups) != 0 {
		t.Errorf("safety snapshot appeared as a separate ready account: %+v", precheck)
	}
}

func TestRobotCredentialRefreshReportsNativeOwnership(t *testing.T) {
	for _, tc := range []struct {
		provider string
		filename string
		content  string
	}{
		{"claude", ".credentials.json", `{"claudeAiOauth":{"accessToken":"SYNTHETIC-CLAUDE-ACCESS","refreshToken":"SYNTHETIC-CLAUDE-REFRESH"}}`},
		{"grok", "auth.json", `{"https://auth.x.ai::synthetic-client":{"key":"SYNTHETIC-GROK-ACCESS","refresh_token":"SYNTHETIC-GROK-REFRESH"}}`},
		{"cursor", "auth.json", `{"apiKey":"SYNTHETIC-CURSOR-API-KEY"}`},
		{"opencode", "auth.json", `{"anthropic":{"type":"api","key":"SYNTHETIC-OPENCODE-API-KEY"}}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			setupRobotCredentialEnv(t)
			writeRobotCredentialProfile(t, tc.provider, "saved", map[string]string{tc.filename: tc.content})
			out, err := runRobotCredentialCommand(t, robotActCmd, "refresh", tc.provider, "saved")
			if err == nil || out.Success || out.Error == nil || out.Error.Code != "REFRESH_UNSUPPORTED" {
				t.Fatalf("native refresh must be recognized and refused explicitly: %+v, error=%v", out, err)
			}
			if out.Error.Details == "" || len(out.Suggestions) == 0 {
				t.Errorf("unsupported refresh omitted recovery guidance: %+v", out)
			}
			after, err := os.ReadFile(filepath.Join(vault.ProfilePath(tc.provider, "saved"), tc.filename))
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != tc.content {
				t.Error("unsupported refresh changed the saved credential")
			}
		})
	}
}

func TestRobotCredentialRefreshReportsActualCodexMutation(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(fmt.Sprintf("accepted=%t", accepted), func(t *testing.T) {
			home := setupRobotCredentialEnv(t)
			previousExpiry := time.Now().Add(2 * time.Minute).Unix()
			original := fmt.Sprintf(`{"access_token":"SYNTHETIC-OLD-ACCESS","refresh_token":"SYNTHETIC-OLD-REFRESH","expires_at":%d,"token_type":"Bearer"}`, previousExpiry)
			writeRobotCredentialProfile(t, "codex", "work", map[string]string{"auth.json": original})
			vaultPath := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
			livePath := filepath.Join(home, ".codex", "auth.json")
			writeNativeTestCredential(t, livePath, original)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body map[string]string
				if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["grant_type"] != "refresh_token" || body["refresh_token"] != "SYNTHETIC-OLD-REFRESH" {
					t.Error("refresh did not send the expected token exchange")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if accepted {
					_, _ = io.WriteString(w, `{"access_token":"SYNTHETIC-NEW-ACCESS","refresh_token":"SYNTHETIC-NEW-REFRESH","expires_in":3600,"token_type":"Bearer"}`)
				} else {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":"temporarily unavailable"}`)
				}
			}))
			t.Cleanup(server.Close)
			oldURL := refresh.CodexTokenURL
			refresh.CodexTokenURL = server.URL
			t.Cleanup(func() { refresh.CodexTokenURL = oldURL })

			out, err := runRobotCredentialCommand(t, robotActCmd, "refresh", "codex", "work")
			if requests.Load() != 1 {
				t.Fatalf("refresh made %d requests, want one token exchange", requests.Load())
			}
			if accepted {
				if err != nil || !out.Success || out.Error != nil {
					t.Fatalf("accepted token exchange reported failure: %+v, error=%v", out, err)
				}
				var action RobotActResult
				if err := json.Unmarshal(out.Data, &action); err != nil {
					t.Fatal(err)
				}
				if !action.Success || action.Action != "refresh" || action.Provider != "codex" || action.Profile != "work" {
					t.Errorf("refresh result did not identify the completed action: %+v", action)
				}
			} else if err == nil || out.Success || out.Error == nil || out.Error.Code != "REFRESH_FAILED" {
				t.Errorf("failed token exchange falsely succeeded: %+v, error=%v", out, err)
			}

			for _, path := range []string{vaultPath, livePath} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !accepted {
					if string(data) != original {
						t.Errorf("failed refresh changed credential at %s", path)
					}
					continue
				}
				var updated struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					ExpiresAt    int64  `json:"expires_at"`
				}
				if err := json.Unmarshal(data, &updated); err != nil {
					t.Fatal(err)
				}
				if updated.AccessToken != "SYNTHETIC-NEW-ACCESS" || updated.RefreshToken != "SYNTHETIC-NEW-REFRESH" || updated.ExpiresAt <= previousExpiry {
					t.Errorf("successful refresh did not rotate tokens and extend expiry at %s", path)
				}
			}
		})
	}
}

func TestRobotCredentialRefreshSkipsStaleCodexBeforeRequest(t *testing.T) {
	home := setupRobotCredentialEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	claims, err := json.Marshal(map[string]any{
		"sub": "synthetic-account", "email": "work@example.invalid", "exp": now.Add(2 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".SYNTHETIC"
	credential := func(refreshed time.Time, refreshToken string) string {
		return fmt.Sprintf(`{"tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q},"last_refresh":%q}`,
			jwt, jwt, refreshToken, refreshed.Format(time.RFC3339))
	}
	snapshot := credential(now.Add(-time.Hour), "SYNTHETIC-SPENT-REFRESH")
	live := credential(now, "SYNTHETIC-CURRENT-REFRESH")
	writeRobotCredentialProfile(t, "codex", "work", map[string]string{"auth.json": snapshot})
	vaultPath := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
	livePath := filepath.Join(home, ".codex", "auth.json")
	writeNativeTestCredential(t, livePath, live)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	oldURL := refresh.CodexTokenURL
	refresh.CodexTokenURL = server.URL
	t.Cleanup(func() { refresh.CodexTokenURL = oldURL })

	out, err := runRobotCredentialCommand(t, robotActCmd, "refresh", "codex", "work")
	if err == nil || out.Success || out.Error == nil || out.Error.Code != "REFRESH_SKIPPED" {
		t.Fatalf("stale credential was not explicitly skipped: %+v, error=%v", out, err)
	}
	if !strings.Contains(out.Error.Details, "caam backup codex work") {
		t.Errorf("stale skip omitted the safe recovery command: %+v", out.Error)
	}
	if requests.Load() != 0 {
		t.Errorf("stale snapshot replayed its refresh token in %d requests", requests.Load())
	}
	for path, before := range map[string]string{vaultPath: snapshot, livePath: live} {
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != before {
			t.Errorf("skipped refresh changed credential at %s", path)
		}
	}
}

func TestRobotCredentialMonitoringRejectsPersistedHealthyMetadata(t *testing.T) {
	setupRobotCredentialEnv(t)
	writeRobotCredentialProfile(t, "claude", "empty", nil)
	if err := healthStore.SetTokenExpiry("claude", "empty", time.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"claude"}, {"claude", "--compact"}} {
		out, err := runRobotCredentialCommand(t, robotStatusCmd, args...)
		if err != nil {
			t.Fatal(err)
		}
		var data RobotStatusData
		if err := json.Unmarshal(out.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Providers) != 1 || len(data.Providers[0].Profiles) != 1 {
			t.Fatalf("unexpected status profile inventory: %+v", data)
		}
		profile := data.Providers[0].Profiles[0]
		if profile.Health.Status != "critical" || profile.Health.ExpiresAt != "" || profile.Health.Reason == "" || profile.Health.LaunchUsable == nil || *profile.Health.LaunchUsable || profile.Health.LoginRequired == nil || !*profile.Health.LoginRequired {
			t.Errorf("stored expiry made a credential-less profile appear usable: %+v", profile)
		}
		if data.Summary.HealthyProfiles != 0 || !data.Summary.AllProfilesBlocked {
			t.Errorf("status summary counted a credential-less profile as healthy: %+v", data.Summary)
		}
	}

	out, err := runRobotCredentialCommand(t, robotHealthCmd)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Overall string `json:"overall"`
		Checks  []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Overall != "degraded" {
		t.Errorf("health monitoring called an unusable pool %q", data.Overall)
	}
	found := false
	for _, check := range data.Checks {
		if check.Name == "claude" {
			found = true
			if check.Status != "warning" || check.Message != "0/1 profiles healthy" {
				t.Errorf("health monitoring reused stale credential evidence: %+v", check)
			}
		}
	}
	if !found {
		t.Error("health monitoring omitted the unusable Claude provider")
	}

	// A real access credential without expiry is usable evidence for the pool
	// summary, while its per-profile launch certainty must remain unknown.
	writeRobotCredentialProfile(t, "claude", "opaque", map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"SYNTHETIC-OPAQUE-ACCESS"}}`,
	})
	out, err = runRobotCredentialCommand(t, robotStatusCmd, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var status RobotStatusData
	if err := json.Unmarshal(out.Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Summary.AllProfilesBlocked || len(status.Providers) != 1 || len(status.Providers[0].Profiles) != 2 {
		t.Fatalf("status ignored the usable opaque credential: %+v", status)
	}
	found = false
	for _, profile := range status.Providers[0].Profiles {
		if profile.Name == "opaque" {
			found = true
			if profile.Health.LaunchUsable != nil || profile.Health.LoginRequired != nil || profile.Health.ExpiresAt != "" {
				t.Errorf("status fabricated certainty for an opaque credential: %+v", profile)
			}
		}
	}
	if !found {
		t.Error("status omitted the usable opaque credential")
	}
}
