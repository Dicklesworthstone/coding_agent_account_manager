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
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/wrap"
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
