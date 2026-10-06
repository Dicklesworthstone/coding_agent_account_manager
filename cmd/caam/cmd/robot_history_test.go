package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
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
	var stdout, stderr bytes.Buffer
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

	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "claude")
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
	out, err := runRobotCredentialCommand(t, robotPrecheckCmd, "claude")
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
			home := setupRobotCredentialEnv(t)
			live := map[string]string{
				filepath.Join(home, ".claude", ".credentials.json"): `{"claudeAiOauth":{"accessToken":"SYNTHETIC-LIVE-ACCESS","refreshToken":"SYNTHETIC-LIVE-REFRESH"}}`,
				filepath.Join(home, ".claude.json"):                 `{"oauthAccount":{"emailAddress":"current@example.invalid","accountUuid":"current-account"},"theme":"light","hasCompletedOnboarding":true}`,
				filepath.Join(home, ".claude", "settings.json"):     `{"permissions":{"allow":["Read"]},"env":{"SHARED_SETTING":"keep-me"}}`,
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
			home := setupRobotCredentialEnv(t)
			livePath := filepath.Join(home, ".claude", ".credentials.json")
			writeNativeTestCredential(t, livePath, `{"claudeAiOauth":{"accessToken":"SYNTHETIC-CURRENT-ACCESS"}}`)
			writeRobotCredentialProfile(t, "claude", "candidate", tc.files)
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

	out, err = runRobotCredentialCommand(t, robotPrecheckCmd, "codex")
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
	home := setupRobotCredentialEnv(t)
	livePath := filepath.Join(home, ".claude", ".credentials.json")
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
	out, err = runRobotCredentialCommand(t, robotPrecheckCmd, "claude")
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
