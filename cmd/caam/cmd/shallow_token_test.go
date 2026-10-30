//go:build unix

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A raw duplicate transfers ownership to the production reader without leaving
// an os.File finalizer that could close a subsequently reused descriptor.
func shallowTokenLaunchTestFD(t *testing.T, payload string) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.WriteString(w, payload); err != nil {
		w.Close()
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(r.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var original unix.Stat_t
	if err := unix.Fstat(fd, &original); err != nil {
		unix.Close(fd)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var current unix.Stat_t
		if unix.Fstat(fd, &current) == nil && current.Dev == original.Dev && current.Ino == original.Ino {
			unix.Close(fd)
		}
	})
	return fd
}

func shallowTokenLaunchTestProfile(t *testing.T, name, tool string) {
	t.Helper()
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", name, "--tool", tool, "--json"); err != nil {
		t.Fatal(err)
	}
}

func shallowTokenLaunchTestEnvironment(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	return values
}

func TestShallowTokenLaunchExplicitIsOneLaunchOnly(t *testing.T) {
	base, realHome := shallowEnv(t)
	shallowTokenLaunchTestProfile(t, "alice", "claude")
	shallowTokenLaunchTestProfile(t, "bob", "claude")
	credential := filepath.Join(base, "alice", ".claude", ".credentials.json")
	profileCredential := `{"claudeAiOauth":{"accessToken":"synthetic-profile-account","refreshToken":"synthetic-profile-refresh"}}`
	if err := os.WriteFile(credential, []byte(profileCredential), 0o600); err != nil {
		t.Fatal(err)
	}
	const ambient = "synthetic-inherited-account"
	const explicit = "synthetic-selected-setup-token"
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_SESSION_ACCESS_TOKEN"} {
		t.Setenv(key, ambient)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "987654")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(realHome, ".claude"))
	t.Setenv("CAAM_TEST_WORKFLOW", "preserved")
	beforeProfiles := snapshotShallowTestTree(t, base)
	beforeHost := snapshotShallowTestTree(t, realHome)
	beforeVault := snapshotShallowTestTree(t, os.Getenv("CAAM_HOME"))
	beforeEnvironment := os.Environ()
	fd := shallowTokenLaunchTestFD(t, explicit+"\r\n")

	type launch struct {
		args []string
		env  map[string]string
	}
	var launches []launch
	oldExec := spawnExec
	spawnExec = func(_ string, args, env []string) error {
		if len(launches) == 0 {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
				t.Fatalf("token descriptor was not closed before execution: %v", err)
			}
		}
		launches = append(launches, launch{slices.Clone(args), shallowTokenLaunchTestEnvironment(env)})
		return nil
	}
	t.Cleanup(func() { spawnExec = oldExec })

	stdout, stderr, err := runCmdCaptured(t, "shallow-spawn", "alice", "--no-sync-config", "--claude-oauth-token-fd", strconv.Itoa(fd), "--", "claude", "auth", "status", "--json")
	if err != nil {
		t.Fatalf("explicit launch: %v", err)
	}
	if len(launches) != 1 {
		t.Fatalf("executed %d launches, want 1", len(launches))
	}
	first := launches[0]
	if first.env["CLAUDE_CODE_OAUTH_TOKEN"] != explicit {
		t.Fatal("selected setup token did not reach the intended child")
	}
	if first.env["HOME"] != filepath.Join(base, "alice") || first.env["SHALLOW_PROFILE"] != "alice" || first.env["CAAM_TEST_WORKFLOW"] != "preserved" {
		t.Fatal("explicit credential changed the selected profile or workflow environment")
	}
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_SESSION_ACCESS_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CONFIG_DIR"} {
		if _, present := first.env[key]; present {
			t.Errorf("explicit launch retained competing inherited %s", key)
		}
	}
	if !slices.Equal(first.args, []string{"claude", "auth", "status", "--json"}) {
		t.Fatal("token input changed the child arguments")
	}
	if strings.Contains(stdout+stderr+strings.Join(first.args, " "), explicit) {
		t.Fatal("setup token appeared in output or child arguments")
	}
	for _, name := range []string{"alice", "bob"} {
		if _, _, err := runCmdCaptured(t, "shallow-spawn", name, "--no-sync-config"); err != nil {
			t.Fatal(err)
		}
		if _, present := launches[len(launches)-1].env["CLAUDE_CODE_OAUTH_TOKEN"]; present {
			t.Fatalf("one-launch authorization carried into subsequent profile %s", name)
		}
	}
	if !slices.Equal(os.Environ(), beforeEnvironment) {
		t.Fatal("explicit launch mutated the parent environment")
	}
	if !maps.Equal(beforeProfiles, snapshotShallowTestTree(t, base)) || !maps.Equal(beforeHost, snapshotShallowTestTree(t, realHome)) || !maps.Equal(beforeVault, snapshotShallowTestTree(t, os.Getenv("CAAM_HOME"))) {
		t.Fatal("ephemeral credential launch changed profile, host, or vault files")
	}
}

func TestShallowTokenLaunchAmbientRejectedForEveryProvider(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "agy"} {
		t.Run(tool, func(t *testing.T) {
			base, _ := shallowEnv(t)
			shallowTokenLaunchTestProfile(t, "seat", tool)
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-outer-claude-seat")
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "987654")
			oldExec, oldCheck := spawnExec, shallowCodexDaemonCheck
			t.Cleanup(func() { spawnExec, shallowCodexDaemonCheck = oldExec, oldCheck })
			shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
			called := false
			spawnExec = func(_ string, _ []string, env []string) error {
				called = true
				values := shallowTokenLaunchTestEnvironment(env)
				for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"} {
					if _, present := values[key]; present {
						t.Errorf("%s shallow launch inherited the outer Claude %s", tool, key)
					}
				}
				if values["HOME"] != filepath.Join(base, "seat") {
					t.Error("selected profile HOME changed")
				}
				return nil
			}
			if _, _, err := runCmdCaptured(t, "shallow-spawn", "seat", "--no-sync-config"); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("launch did not execute")
			}
		})
	}
}

func TestShallowTokenLaunchRejectsWrongScopeWithoutReading(t *testing.T) {
	base, _ := shallowEnv(t)
	for _, tool := range []string{"claude", "codex", "agy"} {
		shallowTokenLaunchTestProfile(t, tool, tool)
	}
	oldExec := spawnExec
	spawnExec = func(string, []string, []string) error {
		t.Fatal("rejected token input executed a child")
		return nil
	}
	t.Cleanup(func() { spawnExec = oldExec })
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"codex-profile", []string{"codex", "--", "claude"}},
		{"agy-profile", []string{"agy", "--", "claude"}},
		{"codex-command", []string{"claude", "--", "codex"}},
		{"shell-wrapper", []string{"claude", "--", "sh", "-c", "exec claude"}},
		{"env-wrapper", []string{"claude", "--", "env", "claude"}},
		{"print-env", []string{"claude", "--print-env"}},
		{"print-env-create", []string{"missing", "--create", "--print-env"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "synthetic-unconsumed-token"
			fd := shallowTokenLaunchTestFD(t, secret)
			args := append([]string{"shallow-spawn", "--claude-oauth-token-fd", strconv.Itoa(fd)}, tc.args...)
			stdout, stderr, err := runCmdCaptured(t, args...)
			if err == nil {
				t.Fatal("accepted token input outside a direct Claude launch")
			}
			if stdout != "" || strings.Contains(stderr+err.Error(), secret) {
				t.Fatal("rejected token input printed an environment or secret")
			}
			buffer := make([]byte, len(secret)+1)
			n, readErr := unix.Read(fd, buffer)
			if readErr != nil || string(buffer[:n]) != secret {
				t.Fatalf("scope validation consumed or closed token descriptor: %v", readErr)
			}
			if _, err := os.Stat(filepath.Join(base, "missing")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected --print-env created a profile")
			}
		})
	}
}

func TestShallowTokenLaunchRejectsInvalidDescriptorWithoutEchoingInput(t *testing.T) {
	shallowEnv(t)
	shallowTokenLaunchTestProfile(t, "alice", "claude")
	oldExec := spawnExec
	spawnExec = func(string, []string, []string) error {
		t.Fatal("invalid descriptor executed a child")
		return nil
	}
	t.Cleanup(func() { spawnExec = oldExec })
	for _, value := range []string{"", "-1", "0", "1", "2", "2147483648", "999999999999999999999999999", "synthetic-secret-mistaken-for-fd"} {
		t.Run("fd-"+strconv.Itoa(len(value))+"-"+strings.ReplaceAll(value, "synthetic-secret-mistaken-for-fd", "redacted"), func(t *testing.T) {
			stdout, stderr, err := runCmdCaptured(t, "shallow-spawn", "alice", "--claude-oauth-token-fd="+value)
			if err == nil {
				t.Fatal("accepted invalid descriptor input")
			}
			if stdout != "" || strings.Contains(stderr+err.Error(), "synthetic-secret-mistaken-for-fd") {
				t.Fatal("descriptor parser exposed the supplied secret")
			}
		})
	}
}

func TestShallowTokenLaunchFailuresKeepSecretsPrivate(t *testing.T) {
	for _, mode := range []string{"lookup", "exec", "malformed-token"} {
		t.Run(mode, func(t *testing.T) {
			base, realHome := shallowEnv(t)
			shallowTokenLaunchTestProfile(t, "alice", "claude")
			const secret = "synthetic-launch-failure-secret"
			payload := secret
			if mode == "malformed-token" {
				payload += "\nsecond-line"
			}
			fd := shallowTokenLaunchTestFD(t, payload)
			beforeProfiles := snapshotShallowTestTree(t, base)
			beforeHost := snapshotShallowTestTree(t, realHome)
			oldExec := spawnExec
			t.Cleanup(func() { spawnExec = oldExec })
			called := false
			spawnExec = func(string, []string, []string) error {
				called = true
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
					t.Fatalf("failed exec retained token descriptor: %v", err)
				}
				return unix.EACCES
			}
			command := "claude"
			if mode == "lookup" {
				command = filepath.Join(t.TempDir(), "claude")
			}
			stdout, stderr, err := runCmdCaptured(t, "shallow-spawn", "alice", "--no-sync-config", "--claude-oauth-token-fd", strconv.Itoa(fd), "--", command)
			if err == nil {
				t.Fatal("launch failure was swallowed")
			}
			if mode == "exec" && (!called || !errors.Is(err, unix.EACCES)) {
				t.Fatalf("exec error was not propagated: %v", err)
			}
			if mode != "exec" && called {
				t.Fatal("invalid launch reached exec")
			}
			if strings.Contains(stdout+stderr+err.Error(), secret) {
				t.Fatal("launch failure exposed token contents")
			}
			if !maps.Equal(beforeProfiles, snapshotShallowTestTree(t, base)) || !maps.Equal(beforeHost, snapshotShallowTestTree(t, realHome)) {
				t.Fatal("failed launch persisted or changed profile files")
			}
		})
	}
}

// Re-executing this package exercises the real syscall.Exec path while keeping
// the existing TestMain isolation guard. No actual provider or account is used.
func TestShallowTokenLaunchProcessHelper(t *testing.T) {
	if os.Getenv("CAAM_TEST_SHALLOW_TOKEN_PROCESS") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		os.Exit(91)
	}
	root := newShallowTestRoot()
	root.SetArgs(os.Args[separator+1:])
	root.SetIn(os.Stdin)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	if err := root.Execute(); err != nil {
		os.Exit(ExitCode(err))
	}
	os.Exit(92) // A successful spawn replaces this process and cannot return.
}

func TestShallowTokenLaunchChildHelper(t *testing.T) {
	if os.Getenv("CAAM_TEST_SHALLOW_TOKEN_PROCESS") != "1" {
		return
	}
	expected := os.Getenv("CAAM_TEST_EXPECT_TOKEN")
	token, present := os.LookupEnv("CLAUDE_CODE_OAUTH_TOKEN")
	if token != expected || present != (expected != "") {
		os.Exit(81)
	}
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CONFIG_DIR"} {
		if _, present := os.LookupEnv(key); present {
			os.Exit(82)
		}
	}
	if os.Getenv("HOME") != os.Getenv("CAAM_TEST_EXPECT_HOME") || os.Getenv("SHALLOW_PROFILE") != "alice" {
		os.Exit(83)
	}
	for _, arg := range os.Args {
		if expected != "" && strings.Contains(arg, expected) {
			os.Exit(84)
		}
	}
	if expected != "" {
		// Runtime initialization may reuse the closed number; it must never
		// retain the inherited credential pipe itself.
		var current unix.Stat_t
		if unix.Fstat(3, &current) == nil && strconv.FormatUint(uint64(current.Ino), 10) == os.Getenv("CAAM_TEST_TOKEN_PIPE_INODE") && strconv.FormatUint(uint64(current.Dev), 10) == os.Getenv("CAAM_TEST_TOKEN_PIPE_DEVICE") {
			os.Exit(85)
		}
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "independent interactive input\n" {
		os.Exit(86)
	}
	code, err := strconv.Atoi(os.Getenv("CAAM_TEST_CHILD_EXIT"))
	if err != nil {
		os.Exit(87)
	}
	fmt.Fprintln(os.Stdout, "synthetic child verified")
	os.Exit(code)
}

func TestShallowTokenLaunchRealProcess(t *testing.T) {
	base, realHome := shallowEnv(t)
	shallowTokenLaunchTestProfile(t, "alice", "claude")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink(executable, claude); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		explicit bool
		exitCode int
	}{
		{"explicit-success", true, 0},
		{"explicit-child-failure", true, 23},
		{"ambient-rejected", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestShallowTokenLaunchProcessHelper$", "--", "shallow-spawn", "alice", "--base", base, "--no-sync-config"}
			if tc.explicit {
				args = append(args, "--claude-oauth-token-fd", "3")
			}
			args = append(args, "--", claude, "-test.run=^TestShallowTokenLaunchChildHelper$")
			child := exec.CommandContext(ctx, executable, args...)
			expected := ""
			if tc.explicit {
				expected = "synthetic-real-process-seat"
			}
			child.Env = append(os.Environ(),
				"CAAM_TEST_SHALLOW_TOKEN_PROCESS=1",
				"CAAM_TEST_EXPECT_TOKEN="+expected,
				"CAAM_TEST_EXPECT_HOME="+filepath.Join(base, "alice"),
				"CAAM_TEST_CHILD_EXIT="+strconv.Itoa(tc.exitCode),
				"CLAUDE_CODE_OAUTH_TOKEN=synthetic-ambient-other-account",
				"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR=987654",
				"ANTHROPIC_API_KEY=synthetic-ambient-api-key",
				"ANTHROPIC_AUTH_TOKEN=synthetic-ambient-bearer",
				"CLAUDE_CONFIG_DIR="+filepath.Join(realHome, ".claude"))
			if tc.explicit {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				if _, err := io.WriteString(w, expected+"\n"); err != nil {
					w.Close()
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				var original unix.Stat_t
				if err := unix.Fstat(int(r.Fd()), &original); err != nil {
					t.Fatal(err)
				}
				child.Env = append(child.Env,
					"CAAM_TEST_TOKEN_PIPE_INODE="+strconv.FormatUint(uint64(original.Ino), 10),
					"CAAM_TEST_TOKEN_PIPE_DEVICE="+strconv.FormatUint(uint64(original.Dev), 10))
				child.ExtraFiles = []*os.File{r}
			}
			child.Stdin = strings.NewReader("independent interactive input\n")
			output, err := child.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("launch did not complete: %v", ctx.Err())
			}
			var exitErr *exec.ExitError
			if tc.exitCode == 0 && err != nil || tc.exitCode != 0 && (!errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exitCode) {
				t.Fatalf("child exit mismatch (want %d): %v; output: %s", tc.exitCode, err, output)
			}
			if !strings.Contains(string(output), "synthetic child verified") {
				t.Fatalf("child did not verify its environment: %s", output)
			}
			if expected != "" && strings.Contains(string(output), expected) || strings.Contains(string(output), "synthetic-ambient-other-account") {
				t.Fatal("process output disclosed a credential")
			}
		})
	}
}
