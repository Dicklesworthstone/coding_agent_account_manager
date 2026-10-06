package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func setupWatchCommand(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	for key, value := range map[string]string{
		"HOME":              home,
		"CAAM_HOME":         filepath.Join(base, "caam"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"GEMINI_HOME":       filepath.Join(home, ".gemini"),
		"GROK_HOME":         filepath.Join(home, ".grok"),
		"CLAUDE_CONFIG_DIR": "",
		"CURSOR_CONFIG_DIR": "",
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"APPDATA":           filepath.Join(home, "AppData", "Roaming"),
	} {
		t.Setenv(key, value)
	}
	previousVault := vault
	vault = authfile.NewVault(filepath.Join(base, "vault"))
	t.Cleanup(func() { vault = previousVault })
	return home
}

func writeWatchCommandAuth(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func executeWatchCommand(ctx context.Context, args ...string) (string, string, error) {
	cmd := newWatchCmd()
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), stderr.String(), err
}

func TestWatchCommandRejectsInvalidOptionsBeforeScanning(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown provider", []string{"--provider", "not-a-provider"}, "unknown provider"},
		{"empty provider", []string{"--provider", " "}, "unknown provider"},
		{"zero poll interval", []string{"--poll-interval", "0s"}, "--poll-interval must be greater than zero"},
		{"negative poll interval", []string{"--poll-interval", "-1s"}, "--poll-interval must be greater than zero"},
		{"invalid poll interval", []string{"--poll-interval", "quickly"}, "invalid duration"},
		{"zero debounce", []string{"--debounce", "0s"}, "--debounce must be greater than zero"},
		{"negative debounce", []string{"--debounce", "-1ms"}, "--debounce must be greater than zero"},
		{"positional argument", []string{"unexpected"}, "unknown command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupWatchCommand(t)
			out, _, err := executeWatchCommand(context.Background(), append([]string{"--once"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if out != "" {
				t.Fatalf("invalid options started a scan: %q", out)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("invalid options created native auth directories: %v", err)
			}
		})
	}
}

func TestWatchCommandOnceCanonicalizesAndDeduplicatesProviders(t *testing.T) {
	home := setupWatchCommand(t)
	writeWatchCommandAuth(t, filepath.Join(home, ".grok", "auth.json"), `{"key":"test-grok-access","email":"grok@example.invalid"}`)
	writeWatchCommandAuth(t, filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"), "test-agy-access")

	out, _, err := executeWatchCommand(context.Background(), "--once", "--provider", "GROK,grok-build, Antigravity,agy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Saved 2 account(s)") || !strings.Contains(out, "grok/grok@example.invalid") || !strings.Contains(out, "agy/") {
		t.Fatalf("unexpected discovery output: %s", out)
	}
	for _, provider := range []string{"grok", "agy"} {
		profiles, err := vault.List(provider)
		if err != nil || len(profiles) != 1 {
			t.Fatalf("%s profiles = %v, error %v", provider, profiles, err)
		}
	}
}

func TestWatchCommandDefaultIncludesAntigravity(t *testing.T) {
	home := setupWatchCommand(t)
	writeWatchCommandAuth(t, filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"), "test-agy-access")
	out, _, err := executeWatchCommand(context.Background(), "--once")
	if err != nil || !strings.Contains(out, "agy/") {
		t.Fatalf("default scan omitted Antigravity: error %v, output %s", err, out)
	}
}

func TestWatchCommandOnceReportsPartialSuccessAndFailure(t *testing.T) {
	home := setupWatchCommand(t)
	writeWatchCommandAuth(t, filepath.Join(home, ".codex", "auth.json"), `{"tokens":`)
	writeWatchCommandAuth(t, filepath.Join(home, ".grok", "auth.json"), `{"key":"test-grok-access","email":"grok@example.invalid"}`)

	out, _, err := executeWatchCommand(context.Background(), "--once", "--provider", "codex,grok")
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("malformed provider was not reported: %v", err)
	}
	if !strings.Contains(out, "Saved 1 account(s)") || !strings.Contains(out, "grok/grok@example.invalid") || strings.Contains(out, "No new accounts") {
		t.Fatalf("partial successes were hidden: %s", out)
	}
	if profiles, err := vault.List("codex"); err != nil || len(profiles) != 0 {
		t.Fatalf("malformed credentials were saved: %v, error %v", profiles, err)
	}
}

type watchCommandEvents struct {
	bytes.Buffer
	ready chan struct{}
	saved chan struct{}
}

func (w *watchCommandEvents) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(string(p), "Watching providers:") {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	if strings.Contains(string(p), "Saved: codex/") {
		select {
		case w.saved <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestWatchCommandContinuesAfterPartialScanAndStopsCleanly(t *testing.T) {
	home := setupWatchCommand(t)
	codexAuth := filepath.Join(home, ".codex", "auth.json")
	writeWatchCommandAuth(t, codexAuth, `{"tokens":`)
	writeWatchCommandAuth(t, filepath.Join(home, ".grok", "auth.json"), `{"key":"test-grok-access","email":"grok@example.invalid"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newWatchCmd()
	out := &watchCommandEvents{ready: make(chan struct{}, 1), saved: make(chan struct{}, 1)}
	var stderr bytes.Buffer
	cmd.SetOut(out)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--provider", "codex,grok", "--poll-interval", "20ms", "--debounce", "20ms", "--watch-optional"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("watch command did not stop after cancellation")
		}
	})

	select {
	case <-out.ready:
	case err := <-done:
		done <- err
		t.Fatalf("initial malformed credentials stopped continuous discovery: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("watch command did not become ready")
	}

	writeWatchCommandAuth(t, codexAuth, `{"tokens":{"access_token":"test-codex-access","refresh_token":"test-codex-refresh"}}`)
	select {
	case <-out.saved:
	case err := <-done:
		done <- err
		t.Fatalf("watch command exited before saving repaired credentials: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("watch command did not capture repaired credentials")
	}

	cancel()
	select {
	case err := <-done:
		done <- err // Keep the cleanup barrier available without waiting twice.
		if err != nil {
			t.Fatalf("cancellation was not a clean shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch command did not stop")
	}
	if !strings.Contains(out.String(), "Initial scan saved 1 account(s)") || !strings.Contains(out.String(), "Watcher stopped.") {
		t.Fatalf("missing initial capture or shutdown output: %s", out.String())
	}
	if !strings.Contains(stderr.String(), "initial scan incomplete") || !strings.Contains(stderr.String(), "codex") {
		t.Fatalf("initial scan failure was hidden: %s", stderr.String())
	}
	if profiles, err := vault.List("codex"); err != nil || len(profiles) != 1 {
		t.Fatalf("repaired credential capture missing: %v, error %v", profiles, err)
	}
}

func TestWatchCommandCanceledContextDoesNotScan(t *testing.T) {
	home := setupWatchCommand(t)
	writeWatchCommandAuth(t, filepath.Join(home, ".grok", "auth.json"), `{"key":"test-grok-access","email":"grok@example.invalid"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, _, err := executeWatchCommand(ctx, "--provider", "grok")
	if !errors.Is(err, context.Canceled) || out != "" {
		t.Fatalf("canceled command started work: error %v, output %q", err, out)
	}
	if profiles, err := vault.List("grok"); err != nil || len(profiles) != 0 {
		t.Fatalf("canceled command saved credentials: %v, error %v", profiles, err)
	}
}
