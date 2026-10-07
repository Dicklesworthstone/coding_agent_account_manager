package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamexec "github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/cursor"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/wrap"
	"github.com/creack/pty"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type runInvocation struct {
	Account string   `json:"account"`
	Input   string   `json:"input"`
	Args    []string `json:"args"`
	WorkDir string   `json:"work_dir"`
}

// TestHelperProcess_Run is the entry point for the mock process for run tests.
func TestHelperProcess_Run(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	data, err := os.ReadFile(os.Getenv("MOCK_AUTH_PATH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	var auth struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.OAuth.AccessToken == "" {
		os.Exit(91)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(92)
	}
	wd, err := os.Getwd()
	if err != nil {
		os.Exit(93)
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	logFile, err := os.OpenFile(os.Getenv("MOCK_RUN_LOG"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		os.Exit(94)
	}
	err = json.NewEncoder(logFile).Encode(runInvocation{auth.OAuth.AccessToken, string(input), args, wd})
	closeErr := logFile.Close()
	if err != nil || closeErr != nil {
		os.Exit(95)
	}
	switch os.Getenv("MOCK_RUN_MODE") {
	case "success":
		fmt.Println("Success: this document explains rate limit handling.")
		os.Exit(0)
	case "normal-failure":
		fmt.Fprintln(os.Stderr, "Error: compilation failed")
		os.Exit(23)
	case "failover":
		if auth.OAuth.AccessToken != "z-active" {
			fmt.Println("Command success after failover")
			os.Exit(0)
		}
	case "all-limited":
	default:
		os.Exit(96)
	}
	fmt.Fprintln(os.Stderr, "Error: rate limit exceeded")
	os.Exit(42)
}

// This helper is reached through Runner's real os/exec path, including the
// Cursor fallback that does not use SmartRunner's injectable command factory.
func TestHelperProcess_InteractiveRun(t *testing.T) {
	if os.Getenv("GO_WANT_INTERACTIVE_RUN_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("INTERACTIVE_RUN_MARKER"), []byte("launched"), 0600); err != nil {
		os.Exit(91)
	}
	os.Exit(0)
}

type interactiveRunProvider struct {
	provider.Provider
	commandCalls int
}

func (p *interactiveRunProvider) DefaultBin() string {
	p.commandCalls++
	return os.Args[0]
}

func setupInteractiveRun(t *testing.T, native provider.Provider) (*cobra.Command, *interactiveRunProvider, string) {
	t.Helper()
	oldRegistry, oldRunner, oldGetWd := registry, runner, getWd
	t.Cleanup(func() { registry, runner, getWd = oldRegistry, oldRunner, oldGetWd })
	prov := &interactiveRunProvider{Provider: native}
	registry = provider.NewRegistry()
	registry.Register(prov)
	runner = caamexec.NewRunner(registry)
	profileStore = profile.NewStore(filepath.Join(os.Getenv("CAAM_HOME"), "profiles"))
	getWd = func() (string, error) { return os.Getenv("HOME"), nil }
	require.NoError(t, config.DefaultConfig().Save())
	terminal, input, err := pty.Open()
	if err != nil {
		t.Skipf("interactive regression requires a PTY: %v", err)
	}
	t.Cleanup(func() { terminal.Close(); input.Close() })
	marker := filepath.Join(t.TempDir(), "interactive-child")
	t.Setenv("GO_WANT_INTERACTIVE_RUN_HELPER", "1")
	t.Setenv("INTERACTIVE_RUN_MARKER", marker)
	cmd := &cobra.Command{Use: "run <tool>", RunE: runWrap, SilenceErrors: true, SilenceUsage: true}
	addRunFlags(cmd)
	cmd.SetIn(input)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd, prov, marker
}

func TestInteractiveRunChecksActiveCursorCredential(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, algorithm := range []string{"smart", "round_robin", "random"} {
		for _, tc := range []struct {
			name       string
			expiry     time.Time
			apiKey     bool
			opaque     bool
			rejection  string
			cooldown   bool
			wantReason string
		}{
			{name: "expired session", expiry: now.Add(-time.Hour), wantReason: "log in again"},
			{name: "expiry boundary", expiry: now, wantReason: "log in again"},
			{name: "valid session", expiry: now.Add(time.Hour)},
			{name: "renewable expired token", expiry: now.Add(-time.Hour), apiKey: true},
			{name: "API key without token", apiKey: true},
			{name: "opaque expiry stays unknown", opaque: true},
			{name: "current provider rejection", expiry: now.Add(time.Hour), apiKey: true, rejection: "current", wantReason: "log in again"},
			{name: "replacement clears old rejection", expiry: now.Add(time.Hour), rejection: "previous"},
			{name: "current account in cooldown", expiry: now.Add(time.Hour), cooldown: true, wantReason: "cooldown"},
		} {
			t.Run(algorithm+"/"+tc.name, func(t *testing.T) {
				paths := setupCursorHealthVault(t)
				cmd, prov, marker := setupInteractiveRun(t, cursor.New())
				// Ambient keys are scrubbed by global execution and cannot rescue
				// an expired browser login from a different account.
				t.Setenv("CURSOR_API_KEY", "SYNTHETIC-AMBIENT-KEY")
				data := cursorHealthCredential(t, tc.expiry, tc.apiKey)
				if tc.opaque {
					data = []byte(`{"accessToken":"SYNTHETIC-OPAQUE-SESSION"}`)
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(paths.AuthFile), 0700))
				require.NoError(t, os.WriteFile(paths.AuthFile, data, 0600))
				configPath := filepath.Join(paths.ConfigDir, "cli-config.json")
				require.NoError(t, os.MkdirAll(paths.ConfigDir, 0700))
				require.NoError(t, os.WriteFile(configPath, []byte(`{"theme":"light"}`), 0600))
				require.NoError(t, vault.Backup(authfile.CursorAuthFiles(), "active"))
				changedConfig := []byte(`{"theme":"dark","lastUpdateCheck":123}`)
				require.NoError(t, os.WriteFile(configPath, changedConfig, 0600))
				current, err := vault.CurrentProfile(authfile.CursorAuthFiles())
				require.NoError(t, err)
				require.Equal(t, "active", current, "config churn must not hide the active credential")
				require.NoError(t, healthStore.SetTokenExpiry("cursor", "active", now.Add(-90*24*time.Hour)))
				if tc.rejection != "" {
					fingerprint := "previous-replaced-credential"
					if tc.rejection == "current" {
						info, err := health.ParseCursorExpiry(paths.AuthFile)
						require.NoError(t, err)
						fingerprint = info.Fingerprint
					}
					require.NoError(t, healthStore.RecordProviderVerification("cursor", "active", health.ProviderVerification{
						Reason: "access_token_rejected", Fingerprint: fingerprint,
					}))
				}
				if tc.cooldown {
					db, err := getDB()
					require.NoError(t, err)
					_, err = db.SetCooldown("cursor", "active", time.Now(), time.Hour, "synthetic regression")
					require.NoError(t, err)
				}
				before := snapshotKeepaliveCLIFiles(t, vault.BasePath())
				cmd.SetArgs([]string{"cursor", "--quiet", "--algorithm", algorithm, "--", "-test.run=^TestHelperProcess_InteractiveRun$"})
				err = cmd.Execute()
				if tc.wantReason != "" {
					require.ErrorContains(t, err, tc.wantReason)
					require.NotContains(t, err.Error(), "caam refresh")
					require.Zero(t, prov.commandCalls, "blocked credential reached command construction")
					require.NoFileExists(t, marker)
					require.NoDirExists(t, profileStore.ProfilePath("cursor", "active"), "blocked launch created a transient profile")
				} else {
					require.NoError(t, err)
					require.Equal(t, 1, prov.commandCalls)
					require.FileExists(t, marker, "eligible credential did not reach the real child path")
				}
				after, err := os.ReadFile(paths.AuthFile)
				require.NoError(t, err)
				require.Equal(t, data, after, "launch check changed native credentials")
				afterConfig, err := os.ReadFile(configPath)
				require.NoError(t, err)
				require.Equal(t, changedConfig, afterConfig)
				require.Equal(t, before, snapshotKeepaliveCLIFiles(t, vault.BasePath()), "launch check changed the vault")
			})
		}
	}
}

func TestInteractiveRunUsesLiveClaudeGrant(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name        string
		savedExpiry time.Time
		liveExpiry  time.Time
		refresh     bool
		rejectSaved bool
		wantLogin   bool
	}{
		{name: "expired live grant with healthy snapshot", savedExpiry: now.Add(time.Hour), liveExpiry: now.Add(-time.Hour), wantLogin: true},
		{name: "healthy live grant with expired snapshot", savedExpiry: now.Add(-time.Hour), liveExpiry: now.Add(time.Hour)},
		{name: "renewable expired live grant", savedExpiry: now.Add(time.Hour), liveExpiry: now.Add(-time.Hour), refresh: true},
		{name: "new live grant replaces rejected snapshot", savedExpiry: now.Add(-time.Hour), liveExpiry: now.Add(time.Hour), rejectSaved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupCursorHealthVault(t)
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			tools = map[string]func() authfile.AuthFileSet{"claude": authfile.ClaudeAuthFiles}
			cmd, prov, marker := setupInteractiveRun(t, claude.New())
			authPath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))
			credential := func(token string, expiry time.Time) []byte {
				oauth := map[string]any{"accessToken": token, "expiresAt": expiry.UnixMilli(), "accountId": "synthetic-account"}
				if tc.refresh {
					oauth["refreshToken"] = "SYNTHETIC-REFRESH-" + token
				}
				data, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
				require.NoError(t, err)
				return data
			}
			saved := credential("SYNTHETIC-SAVED", tc.savedExpiry)
			live := credential("SYNTHETIC-LIVE", tc.liveExpiry)
			require.NoError(t, os.WriteFile(authPath, saved, 0600))
			require.NoError(t, vault.Backup(authfile.ClaudeAuthFiles(), "active"))
			if tc.rejectSaved {
				info, err := health.ParseClaudeExpiry(os.Getenv("CLAUDE_CONFIG_DIR"))
				require.NoError(t, err)
				require.NoError(t, healthStore.RecordProviderVerification("claude", "active", health.ProviderVerification{
					Reason: "access_token_rejected", Fingerprint: info.Fingerprint,
				}))
			}
			require.NoError(t, os.WriteFile(authPath, live, 0600))
			current, err := vault.CurrentProfile(authfile.ClaudeAuthFiles())
			require.NoError(t, err)
			require.Equal(t, "active", current, "rotated grant must remain owned by the same account")
			before := snapshotKeepaliveCLIFiles(t, vault.BasePath())
			cmd.SetArgs([]string{"claude", "--quiet", "--", "-test.run=^TestHelperProcess_InteractiveRun$"})
			err = cmd.Execute()
			if tc.wantLogin {
				require.ErrorContains(t, err, "log in again")
				require.Zero(t, prov.commandCalls)
				require.NoFileExists(t, marker)
				require.NoDirExists(t, profileStore.ProfilePath("claude", "active"))
			} else {
				require.NoError(t, err)
				require.FileExists(t, marker)
			}
			after, err := os.ReadFile(authPath)
			require.NoError(t, err)
			require.Equal(t, live, after)
			require.Equal(t, before, snapshotKeepaliveCLIFiles(t, vault.BasePath()))
		})
	}
}

func TestRunCommand_Extended(t *testing.T) {
	tests := []struct {
		name      string
		settings  string
		flags     []string
		mode      string
		wantCalls int
		wantExit  int
		wantLimit bool
		wantFinal string
	}{
		{"success keeps current account", `{}`, nil, "success", 1, 0, false, "z-active"},
		{"failed command preserves exit", `{}`, nil, "normal-failure", 1, 23, false, "z-active"},
		{"headless Claude fails over", `{"max_retries":1}`, nil, "failover", 2, 0, true, "a-ready"},
		{"global zero disables retry", `{"max_retries":0}`, nil, "all-limited", 1, 42, true, "z-active"},
		{"provider zero disables retry", `{"max_retries":4,"providers":{"claude":{"max_retries":0}}}`, nil, "all-limited", 1, 42, true, "z-active"},
		{"provider budget overrides global", `{"max_retries":0,"providers":{"claude":{"max_retries":2}}}`, nil, "all-limited", 3, 42, true, "m-other"},
		{"CLI zero overrides provider", `{"providers":{"claude":{"max_retries":2}}}`, []string{"--max-retries", "0"}, "all-limited", 1, 42, true, "z-active"},
		{"CLI enables retry over provider zero", `{"providers":{"claude":{"max_retries":0}}}`, []string{"--max-retries", "1"}, "all-limited", 2, 42, true, "a-ready"},
		{"exhausted accounts are not reused", `{"max_retries":10,"cooldown_duration":"0s"}`, nil, "all-limited", 3, 42, true, "m-other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, stdout, stderr, root, logPath := setupHeadlessRun(t, tc.settings, tc.mode)
			input := "first line\nsecond line: café\n"
			cmd.SetIn(strings.NewReader(input))
			args := append([]string{"claude", "--quiet", "--algorithm", "round_robin"}, tc.flags...)
			cmd.SetArgs(append(args, "--", "-p", "test prompt"))
			err := cmd.Execute()
			require.Equal(t, tc.wantExit, ExitCode(err), "error: %v; stderr: %s", err, stderr)
			if tc.wantExit != 0 {
				var exitErr *caamexec.ExitCodeError
				require.ErrorAs(t, err, &exitErr)
			}
			calls := readRunInvocations(t, logPath)
			require.Len(t, calls, tc.wantCalls)
			require.Equal(t, "z-active", calls[0].Account, "startup must retain the current account")
			seen := map[string]bool{}
			for _, call := range calls {
				require.False(t, seen[call.Account], "rate-limited account was reused")
				seen[call.Account] = true
				require.Equal(t, input, call.Input, "every launch must receive the full piped input")
				require.Equal(t, []string{"claude", "-p", "test prompt"}, call.Args)
				require.Equal(t, root, call.WorkDir)
			}
			current, err := vault.CurrentProfile(authfile.ClaudeAuthFiles())
			require.NoError(t, err)
			require.Equal(t, tc.wantFinal, current)
			db, err := getDB()
			require.NoError(t, err)
			sessions, err := db.GetWrapSessions("claude", time.Time{}, 10)
			require.NoError(t, err)
			require.Len(t, sessions, 1)
			require.Equal(t, tc.wantLimit, sessions[0].RateLimitHit)
			require.Equal(t, tc.wantExit, sessions[0].ExitCode)
			if tc.wantCalls > 1 {
				require.Equal(t, fmt.Sprintf("retries: %d", tc.wantCalls-1), sessions[0].Notes)
			}
			if tc.wantExit == 0 {
				require.Contains(t, stdout.String(), "uccess")
			}
			require.NotContains(t, stdout.String(), "Switching")
			require.NotContains(t, stdout.String(), "Waiting")
		})
	}
}

func setupHeadlessRun(t *testing.T, settings, mode string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	oldVault, oldRegistry, oldRunner := vault, registry, runner
	oldStore, oldHealth, oldDB := profileStore, healthStore, globalDB
	oldExec, oldGetWd := wrap.ExecCommand, getWd
	t.Cleanup(func() {
		if globalDB != nil && globalDB != oldDB {
			require.NoError(t, globalDB.Close())
		}
		vault, registry, runner = oldVault, oldRegistry, oldRunner
		profileStore, healthStore, globalDB = oldStore, oldHealth, oldDB
		wrap.ExecCommand, getWd = oldExec, oldGetWd
	})
	globalDB = nil
	vault = authfile.NewVault(filepath.Join(root, "vault"))
	profileStore = profile.NewStore(filepath.Join(root, "profiles"))
	healthStore = health.NewStorage(filepath.Join(root, "health.json"))
	registry = provider.NewRegistry()
	registry.Register(claude.New())
	runner = caamexec.NewRunner(registry)
	getWd = func() (string, error) { return root, nil }
	global := config.DefaultConfig()
	global.Wrap.InitialDelay, global.Wrap.MaxDelay = 0, 0
	global.Wrap.Jitter = false
	require.NoError(t, json.Unmarshal([]byte(settings), &global.Wrap))
	require.NoError(t, global.Save())
	fileSet := authfile.ClaudeAuthFiles()
	authPath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))
	for _, name := range []string{"a-ready", "m-other", "z-active"} {
		data := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`, name, "synthetic-refresh-"+name, time.Now().Add(time.Hour).UnixMilli())
		require.NoError(t, os.WriteFile(authPath, []byte(data), 0600))
		require.NoError(t, vault.Backup(fileSet, name))
	}
	logPath := filepath.Join(root, "invocations.jsonl")
	wrap.ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		childArgs := append([]string{"-test.run=^TestHelperProcess_Run$", "--", name}, args...)
		child := exec.CommandContext(ctx, os.Args[0], childArgs...)
		child.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "MOCK_RUN_MODE="+mode, "MOCK_AUTH_PATH="+authPath, "MOCK_RUN_LOG="+logPath)
		return child
	}
	cmd := &cobra.Command{Use: "run <tool>", Args: cobra.MinimumNArgs(1), RunE: runWrap, SilenceErrors: true, SilenceUsage: true}
	addRunFlags(cmd)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetIn(strings.NewReader(""))
	return cmd, stdout, stderr, root, logPath
}

func readRunInvocations(t *testing.T, path string) []runInvocation {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var calls []runInvocation
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var call runInvocation
		err := decoder.Decode(&call)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		calls = append(calls, call)
	}
	return calls
}

func TestRunRejectsInvalidRetrySettingsBeforeActivation(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		flags          []string
	}{
		{"negative retries", `{"max_retries":-1}`, nil},
		{"negative provider delay", `{"providers":{"claude":{"initial_delay":"-1s"}}}`, nil},
		{"zero multiplier", `{"backoff_multiplier":0}`, nil},
		{"inverted delays", `{"initial_delay":"2s","max_delay":"1s"}`, nil},
		{"negative flag", `{}`, []string{"--max-retries", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _, _, _, logPath := setupHeadlessRun(t, tc.settings, "success")
			before, err := os.ReadFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"))
			require.NoError(t, err)
			args := append([]string{"claude"}, tc.flags...)
			cmd.SetArgs(append(args, "--", "-p", "test prompt"))
			require.ErrorContains(t, cmd.Execute(), "invalid retry settings")
			require.Empty(t, readRunInvocations(t, logPath))
			after, err := os.ReadFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

type cancelRunWaitWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelRunWaitWriter) Write(data []byte) (int, error) {
	if bytes.Contains(bytes.ToLower(data), []byte("waiting")) {
		w.cancel()
	}
	return w.Buffer.Write(data)
}

func TestRunCancellationDuringBackoffDoesNotSwitch(t *testing.T) {
	cmd, _, _, _, logPath := setupHeadlessRun(t, `{"initial_delay":"1h","max_delay":"1h"}`, "all-limited")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd.SetContext(ctx)
	cmd.SetErr(&cancelRunWaitWriter{cancel: cancel})
	cmd.SetArgs([]string{"claude", "--", "-p", "test prompt"})
	require.ErrorIs(t, cmd.Execute(), context.Canceled)
	calls := readRunInvocations(t, logPath)
	require.Len(t, calls, 1)
	current, err := vault.CurrentProfile(authfile.ClaudeAuthFiles())
	require.NoError(t, err)
	require.Equal(t, "z-active", current)
}

func TestRunNativeExecutionModes(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		args       []string
		terminal   bool
		headless   bool
	}{
		{"Claude TUI with prompt", "claude", []string{"explain code"}, true, false},
		{"Claude print", "claude", []string{"-p", "explain code"}, true, true},
		{"Claude long print", "claude", []string{"--print", "explain code"}, true, true},
		{"Claude literal print after delimiter", "claude", []string{"--", "--print"}, true, false},
		{"Codex execution", "codex", []string{"exec", "explain code"}, true, true},
		{"Codex alias", "codex", []string{"e", "explain code"}, true, true},
		{"Codex TUI prompt", "codex", []string{"explain code"}, true, false},
		{"Gemini headless", "gemini", []string{"--prompt=explain code"}, true, true},
		{"Gemini interactive prompt", "gemini", []string{"-i", "explain code"}, true, false},
		{"Gemini explicit interactive with redirected input", "gemini", []string{"-i", "explain code"}, false, false},
		{"Cursor print", "cursor", []string{"--print", "explain code"}, true, true},
		{"OpenCode run", "opencode", []string{"run", "explain code"}, true, true},
		{"redirected process", "grok", []string{"prompt"}, false, true},
		{"native interactive default", "grok", nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.headless, runUsesHeadless(tc.tool, tc.args, tc.terminal))
		})
	}
}
