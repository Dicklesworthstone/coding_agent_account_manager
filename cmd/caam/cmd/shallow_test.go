package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/spf13/cobra"
)

// fakeShallowHome lays down a representative real HOME inside t.TempDir() and
// returns its path. Mirrors internal/shallow's helper but local to this pkg.
func fakeShallowHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, f := range []string{".bashrc", ".zshrc", ".gitconfig"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("# "+f+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}
	for _, d := range []string{".ssh", ".config"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// shallowEnv sets up isolated CAAM_HOME, real HOME, and shallow base dir for
// a single test, returning the shallow base path and the fake real HOME path.
func shallowEnv(t *testing.T) (basePath, realHome string) {
	t.Helper()
	realHome = fakeShallowHome(t)
	basePath = filepath.Join(t.TempDir(), "orch-homes")
	caamHome := t.TempDir()
	t.Setenv("CAAM_HOME", caamHome)
	t.Setenv("HOME", realHome)
	t.Setenv("CAAM_SHALLOW_HOMES_DIR", basePath)
	// Reinitialize the package-level vault so resolveVaultCredential can read it.
	vault = authfile.NewVault(authfile.DefaultVaultPath())
	return basePath, realHome
}

// runCmdCaptured executes a single subcommand path against a fresh root command
// (we can't reuse the package-level rootCmd because cobra retains flag state
// between invocations and other tests already wire it). It returns stdout/stderr.
func runCmdCaptured(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	// Build a minimal command tree exposing our shallow surface. Cobra trees
	// are cheap and this avoids cross-test flag pollution.
	root := newShallowTestRoot()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// newShallowTestRoot builds a fresh command tree that mirrors how root.go
// wires the shallow commands, but without the rest of caam's persistent
// pre-run hooks (which would try to migrate data, register providers, etc.).
func newShallowTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "caam-test"}
	// Re-create the same surface; we reuse the existing var pointers' fields
	// only via a small clone helper to keep flag state pristine per call.
	// Easiest: build a fresh tree from scratch using the same RunE funcs.
	create := &cobra.Command{
		Use:  "create <name>",
		Args: shallowProfileCreateCmd.Args,
		RunE: shallowProfileCreateCmd.RunE,
	}
	create.Flags().String("tool", "", "")
	create.Flags().String("from-vault", "", "")
	create.Flags().String("from-file", "", "")
	create.Flags().String("from-claude-json", "", "")
	create.Flags().Bool("force", false, "")
	create.Flags().Bool("json", false, "")

	list := &cobra.Command{
		Use:  "list",
		RunE: shallowProfileListCmd.RunE,
	}
	list.Flags().Bool("json", false, "")

	del := &cobra.Command{
		Use:  "delete <name>",
		Args: shallowProfileDeleteCmd.Args,
		RunE: shallowProfileDeleteCmd.RunE,
	}
	del.Flags().Bool("force", false, "")
	del.Flags().Bool("json", false, "")

	parent := &cobra.Command{Use: "shallow-profile"}
	parent.PersistentFlags().String("base", "", "")
	parent.AddCommand(create, list, del)

	spawn := &cobra.Command{
		Use:  "shallow-spawn <name> -- <cmd>",
		Args: shallowSpawnCmd.Args,
		RunE: shallowSpawnCmd.RunE,
	}
	spawn.Flags().String("base", "", "")
	spawn.Flags().Bool("print-env", false, "")
	spawn.Flags().Bool("reload-daemon", false, "")
	spawn.Flags().Bool("allow-agent-view", false, "")
	spawn.Flags().String("effort", "", "")
	spawn.Flags().String("tool", "claude", "")
	spawn.Flags().Bool("force", false, "")

	root.AddCommand(parent)
	root.AddCommand(spawn)
	return root
}

func TestShallowCreateAndList_JSON(t *testing.T) {
	base, _ := shallowEnv(t)

	// Stage a vault profile so --from-vault works.
	caamHome := os.Getenv("CAAM_HOME")
	vaultDir := filepath.Join(caamHome, "data", "vault", "claude", "alice")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credBody := `{"claudeAiOauth":{"accessToken":"fake","refreshToken":"r"}}`
	if err := os.WriteFile(filepath.Join(vaultDir, ".credentials.json"), []byte(credBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create the shallow profile via the CLI surface.
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--from-vault", "claude/alice", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var createResp struct {
		Success        bool   `json:"success"`
		Name           string `json:"name"`
		Path           string `json:"path"`
		CredentialFrom string `json:"credential_from"`
	}
	if err := json.Unmarshal([]byte(stdout), &createResp); err != nil {
		t.Fatalf("unmarshal create JSON %q: %v", stdout, err)
	}
	if !createResp.Success || createResp.Name != "alice" {
		t.Fatalf("unexpected response: %+v", createResp)
	}
	if createResp.CredentialFrom != "vault:claude/alice" {
		t.Fatalf("CredentialFrom %q", createResp.CredentialFrom)
	}
	wantPath := filepath.Join(base, "alice")
	if createResp.Path != wantPath {
		t.Fatalf("path %q != %q", createResp.Path, wantPath)
	}

	// Verify credentials file contents and perms.
	credDst := filepath.Join(wantPath, ".claude", ".credentials.json")
	gotBody, err := os.ReadFile(credDst)
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	if string(gotBody) != credBody {
		t.Fatalf("creds mismatch: %q", gotBody)
	}
	if st, err := os.Stat(credDst); err != nil {
		t.Fatal(err)
	} else if st.Mode().Perm() != 0o600 {
		t.Fatalf("creds perm %v", st.Mode().Perm())
	}

	// List should include alice.
	stdout, _, err = runCmdCaptured(t, "shallow-profile", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listResp struct {
		BaseDir  string `json:"base_dir"`
		Count    int    `json:"count"`
		Profiles []struct {
			Name           string `json:"name"`
			Path           string `json:"path"`
			CredentialFrom string `json:"credential_from"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(stdout), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if listResp.Count != 1 || len(listResp.Profiles) != 1 {
		t.Fatalf("unexpected list: %+v", listResp)
	}
	if listResp.Profiles[0].Name != "alice" {
		t.Fatalf("listed name %q", listResp.Profiles[0].Name)
	}
	if listResp.BaseDir != base {
		t.Fatalf("listed BaseDir %q != %q", listResp.BaseDir, base)
	}
}

func TestShallowCreateFromFile(t *testing.T) {
	_, _ = shallowEnv(t)

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "creds.json")
	if err := os.WriteFile(src, []byte(`{"from":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "bob", "--from-file", src, "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, `"success": true`) {
		t.Fatalf("expected success in %q", stdout)
	}
}

func TestShallowCreateRejectsBothCredentialSources(t *testing.T) {
	_, _ = shallowEnv(t)
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice",
		"--from-vault", "claude/alice", "--from-file", "/tmp/nope", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, "mutually exclusive") {
		t.Fatalf("expected mutual-exclusivity error, got %q", stdout)
	}
}

func TestShallowCreateUnknownVaultProfile(t *testing.T) {
	_, _ = shallowEnv(t)
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice",
		"--from-vault", "claude/missing", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, "missing .credentials.json") {
		t.Fatalf("expected missing-creds error, got %q", stdout)
	}
}

func TestShallowDelete(t *testing.T) {
	base, _ := shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "alice")); err != nil {
		t.Fatalf("expected profile dir: %v", err)
	}
	if _, _, err := runCmdCaptured(t, "shallow-profile", "delete", "alice", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile dir still exists after delete: err=%v", err)
	}
}

func TestShallowDeleteNotFound(t *testing.T) {
	_, _ = shallowEnv(t)
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "delete", "ghost", "--force", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "does not exist") {
		t.Fatalf("expected not-exist error, got %q", stdout)
	}
}

// TestShallowSpawnPrintEnv verifies that --print-env emits the right HOME
// without exec'ing anything.
func TestShallowSpawnPrintEnv(t *testing.T) {
	base, _ := shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--print-env")
	if err != nil {
		t.Fatalf("spawn print-env: %v", err)
	}
	wantHome := filepath.Join(base, "alice")
	if !strings.Contains(stdout, "HOME="+wantHome) {
		t.Fatalf("expected HOME assignment in %q", stdout)
	}
	if !strings.Contains(stdout, "SHALLOW_PROFILE=alice") {
		t.Fatalf("expected SHALLOW_PROFILE in %q", stdout)
	}
}

// TestShallowSpawnExecsCorrectHome injects a fake spawnExec to verify that
// runShallowSpawn sets HOME correctly and forwards args+bin.
func TestShallowSpawnExecsCorrectHome(t *testing.T) {
	base, _ := shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}

	// Pick a real binary (sh is universal on Unix). LookPath needs it on $PATH.
	type captured struct {
		bin  string
		args []string
		env  []string
	}
	var got captured
	origExec := spawnExec
	spawnExec = func(bin string, args []string, env []string) error {
		got = captured{bin: bin, args: args, env: env}
		return nil
	}
	t.Cleanup(func() { spawnExec = origExec })

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--", "sh", "-c", "echo hi"); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !strings.HasSuffix(got.bin, "/sh") && got.bin != "sh" {
		t.Fatalf("unexpected bin: %q", got.bin)
	}
	if len(got.args) != 3 || got.args[0] != "sh" || got.args[1] != "-c" || got.args[2] != "echo hi" {
		t.Fatalf("unexpected args: %v", got.args)
	}
	wantHome := "HOME=" + filepath.Join(base, "alice")
	wantProfile := "SHALLOW_PROFILE=alice"
	foundHome, foundProfile := false, false
	var sawClaudeCfg bool
	for _, e := range got.env {
		if e == wantHome {
			foundHome = true
		}
		if e == wantProfile {
			foundProfile = true
		}
		if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			sawClaudeCfg = true
		}
	}
	if !foundHome {
		t.Fatalf("env missing %s; got %v", wantHome, got.env)
	}
	if !foundProfile {
		t.Fatalf("env missing %s; got %v", wantProfile, got.env)
	}
	if sawClaudeCfg {
		t.Fatalf("CLAUDE_CONFIG_DIR should be stripped, got %v", got.env)
	}
}

// TestShallowSpawnBackfillsCodexSkills proves the end-to-end wiring of the
// skill-share repair (#56): a codex shallow profile whose shallow .codex/skills
// drifted into a real, system-only directory gets the real HOME's user skills
// symlinked back in during shallow-spawn, before the child is exec'd — while
// --print-env never mutates the profile.
func TestShallowSpawnBackfillsCodexSkills(t *testing.T) {
	base, realHome := shallowEnv(t)

	// Real codex identity + a user-installed (jsm-style) skill.
	codexDir := filepath.Join(realHome, ".codex")
	if err := os.MkdirAll(filepath.Join(codexDir, "skills", "user-skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"OPENAI_API_KEY":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "skills", "user-skill", "SKILL.md"), []byte("# user skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "cx", "--tool", "codex",
		"--from-file", filepath.Join(codexDir, "auth.json"), "--json"); err != nil {
		t.Fatal(err)
	}

	// Simulate the drift from issue #56: replace the create-time skills
	// passthrough with a REAL dir holding only codex's bundled .system skills.
	shallowSkills := filepath.Join(base, "cx", ".codex", "skills")
	if err := os.Remove(shallowSkills); err != nil {
		t.Fatalf("remove skills passthrough: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(shallowSkills, ".system", "builtin"), 0o700); err != nil {
		t.Fatal(err)
	}

	// --print-env must not repair (read-only contract).
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "cx", "--print-env"); err != nil {
		t.Fatalf("print-env: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(shallowSkills, "user-skill")); !os.IsNotExist(err) {
		t.Fatalf("--print-env must not mutate the profile (err=%v)", err)
	}

	// Stub the exec and the host daemon scan.
	origExec := spawnExec
	spawnExec = func(_ string, _ []string, _ []string) error { return nil }
	t.Cleanup(func() { spawnExec = origExec })
	origCheck := shallowCodexDaemonCheck
	shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
	t.Cleanup(func() { shallowCodexDaemonCheck = origCheck })

	_, stderr, err := runCmdCaptured(t, "shallow-spawn", "cx", "--", "sh", "-c", "true")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !strings.Contains(stderr, "linked 1 user skill") {
		t.Fatalf("expected skill-link note on stderr, got %q", stderr)
	}

	// The user skill is now visible inside the shallow profile...
	got, err := os.ReadFile(filepath.Join(shallowSkills, "user-skill", "SKILL.md"))
	if err != nil || string(got) != "# user skill\n" {
		t.Fatalf("user skill not shared into shallow profile: %q, err=%v", got, err)
	}
	// ...while the bundled .system dir stays real and profile-local, and the
	// auth file stays a real private file.
	if st, err := os.Lstat(filepath.Join(shallowSkills, ".system")); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".system must stay a real profile-local dir (err=%v)", err)
	}
	if st, err := os.Lstat(filepath.Join(base, "cx", ".codex", "auth.json")); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("auth.json must stay a real private file (err=%v)", err)
	}
}

// TestShallowSpawnAgentViewFlag proves the end-to-end wiring of the claude
// Agent View disable policy (#49) through the real command tree: the injected
// CLAUDE_CODE_DISABLE_AGENT_VIEW=1 appears in the exec env of a default claude
// shallow session, is suppressed by --allow-agent-view, and a pre-existing user
// value is preserved rather than overridden.
func TestShallowSpawnAgentViewFlag(t *testing.T) {
	const key = "CLAUDE_CODE_DISABLE_AGENT_VIEW"

	hasEnv := func(env []string, want string) bool {
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}
	hasKey := func(env []string, k string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, k+"=") {
				return true
			}
		}
		return false
	}

	t.Run("default injects disable flag", func(t *testing.T) {
		_, _ = shallowEnv(t)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
			t.Fatal(err)
		}
		var got []string
		origExec := spawnExec
		spawnExec = func(_ string, _ []string, e []string) error { got = e; return nil }
		t.Cleanup(func() { spawnExec = origExec })
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--", "sh", "-c", "true"); err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if !hasEnv(got, key+"=1") {
			t.Fatalf("default claude session must inject %s=1; got %v", key, got)
		}
	})

	t.Run("--allow-agent-view suppresses injection", func(t *testing.T) {
		_, _ = shallowEnv(t)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "bob", "--json"); err != nil {
			t.Fatal(err)
		}
		var env []string
		origExec := spawnExec
		spawnExec = func(_ string, _ []string, e []string) error { env = e; return nil }
		t.Cleanup(func() { spawnExec = origExec })
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "bob", "--allow-agent-view", "--", "sh", "-c", "true"); err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if hasKey(env, key) {
			t.Fatalf("--allow-agent-view must NOT inject %s; got %v", key, env)
		}
	})

	t.Run("preserves pre-existing user value", func(t *testing.T) {
		_, _ = shallowEnv(t)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "carol", "--json"); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, "0")
		var env []string
		origExec := spawnExec
		spawnExec = func(_ string, _ []string, e []string) error { env = e; return nil }
		t.Cleanup(func() { spawnExec = origExec })
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "carol", "--", "sh", "-c", "true"); err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if !hasEnv(env, key+"=0") {
			t.Fatalf("user %s=0 must be preserved; got %v", key, env)
		}
		if hasEnv(env, key+"=1") {
			t.Fatalf("must not override user %s to 1; got %v", key, env)
		}
	})
}

// TestShallowSpawnUnknownProfile must produce a clear error.
func TestShallowSpawnUnknownProfile(t *testing.T) {
	_, _ = shallowEnv(t)
	_, _, err := runCmdCaptured(t, "shallow-spawn", "ghost", "--", "sh")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestShallowSpawnFailsClosedOnIndeterminateProvider verifies that shallow-spawn
// refuses to run (rather than silently assuming the Claude env-isolation policy)
// when a profile's provider can be neither read from metadata nor inferred from
// disk — the fail-closed behavior added for issue #43.
func TestShallowSpawnFailsClosedOnIndeterminateProvider(t *testing.T) {
	base, _ := shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	// Break the profile so its provider is indeterminate: remove the metadata
	// sidecar and the real .claude layout so on-disk inference has nothing to
	// latch onto.
	profHome := filepath.Join(base, "alice")
	if err := os.Remove(filepath.Join(profHome, ".caam-shallow.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(profHome, ".claude")); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--", "sh", "-c", "true")
	if err == nil {
		t.Fatalf("expected shallow-spawn to fail closed on an indeterminate provider")
	}
	if !strings.Contains(err.Error(), "refusing to spawn") {
		t.Fatalf("unexpected error (want fail-closed refusal): %v", err)
	}
}

// TestShallowSpawnNoCommand requires a command after the profile name.
func TestShallowSpawnNoCommand(t *testing.T) {
	_, _ = shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCmdCaptured(t, "shallow-spawn", "alice")
	if err == nil {
		t.Fatalf("expected error when no command provided")
	}
}

// TestInjectCodexEffort unit-tests the --effort → `-c model_reasoning_effort=`
// translation (issue #63): codex has no --effort CLI flag, so caam injects the
// config-key spelling right after the codex binary, fails closed for non-codex
// commands, and refuses to stack a second effort override on top of one the
// user already spelled out.
func TestInjectCodexEffort(t *testing.T) {
	t.Run("empty effort is a no-op", func(t *testing.T) {
		in := []string{"codex", "--model", "gpt-5.6-sol"}
		out, err := injectCodexEffort(in, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Join(out, " ") != strings.Join(in, " ") {
			t.Fatalf("argv changed without --effort: %v", out)
		}
	})

	t.Run("injects config override after codex binary", func(t *testing.T) {
		out, err := injectCodexEffort([]string{"codex", "--model", "gpt-5.6-sol", "--yolo"}, "xhigh")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "codex -c model_reasoning_effort=xhigh --model gpt-5.6-sol --yolo"
		if strings.Join(out, " ") != want {
			t.Fatalf("argv = %q, want %q", strings.Join(out, " "), want)
		}
	})

	t.Run("full codex path still recognized", func(t *testing.T) {
		out, err := injectCodexEffort([]string{"/usr/local/bin/codex", "exec", "hi"}, "high")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "/usr/local/bin/codex -c model_reasoning_effort=high exec hi"
		if strings.Join(out, " ") != want {
			t.Fatalf("argv = %q, want %q", strings.Join(out, " "), want)
		}
	})

	t.Run("non-codex command fails closed", func(t *testing.T) {
		if _, err := injectCodexEffort([]string{"claude", "--print", "hi"}, "high"); err == nil {
			t.Fatal("expected error for non-codex command")
		}
	})

	t.Run("explicit user override wins by erroring, not stacking", func(t *testing.T) {
		_, err := injectCodexEffort([]string{"codex", "-c", "model_reasoning_effort=low"}, "high")
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("expected conflict error, got %v", err)
		}
	})

	t.Run("literal --effort in child args gets a redirect hint", func(t *testing.T) {
		_, err := injectCodexEffort([]string{"codex", "--effort", "xhigh"}, "high")
		if err == nil || !strings.Contains(err.Error(), "codex has no --effort flag") {
			t.Fatalf("expected redirect hint, got %v", err)
		}
	})
}

// TestShallowSpawnEffortFlag proves the end-to-end wiring of --effort (#63)
// through the real command tree: a codex shallow-spawn with --effort xhigh
// execs codex with `-c model_reasoning_effort=xhigh` injected ahead of the
// user's own flags, and the caam-level flag itself is never forwarded.
func TestShallowSpawnEffortFlag(t *testing.T) {
	_, realHome := shallowEnv(t)

	// Real codex identity so `shallow-profile create --tool codex` works.
	codexDir := filepath.Join(realHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"OPENAI_API_KEY":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "cx", "--tool", "codex",
		"--from-file", filepath.Join(codexDir, "auth.json"), "--json"); err != nil {
		t.Fatal(err)
	}

	// A fake codex binary on PATH so exec.LookPath resolves.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Stub the exec and the host daemon scan.
	var got []string
	origExec := spawnExec
	spawnExec = func(_ string, args []string, _ []string) error { got = args; return nil }
	t.Cleanup(func() { spawnExec = origExec })
	origCheck := shallowCodexDaemonCheck
	shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
	t.Cleanup(func() { shallowCodexDaemonCheck = origCheck })

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "cx", "--effort", "xhigh", "--",
		"codex", "--model", "gpt-5.6-sol", "--yolo"); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	want := "codex -c model_reasoning_effort=xhigh --model gpt-5.6-sol --yolo"
	if strings.Join(got, " ") != want {
		t.Fatalf("exec argv = %q, want %q", strings.Join(got, " "), want)
	}

	// And --effort with a non-codex child refuses instead of silently dropping.
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "cx", "--effort", "xhigh", "--",
		"sh", "-c", "true"); err == nil {
		t.Fatal("expected --effort with non-codex child to fail closed")
	}
}

// Issue #80: a claude --from-vault source must prefer the vault profile's own
// saved .claude.json (the state that MATCHES those credentials) over the real
// HOME's, while an explicit --from-claude-json still wins over both.
func TestShallowCreateVaultPrefersVaultClaudeJSON(t *testing.T) {
	base, _ := shallowEnv(t)

	caamHome := os.Getenv("CAAM_HOME")
	vaultDir := filepath.Join(caamHome, "data", "vault", "claude", "alice")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"fake"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	vaultState := `{"hasCompletedOnboarding":true,"fromVaultSnapshot":true}`
	if err := os.WriteFile(filepath.Join(vaultDir, ".claude.json"), []byte(vaultState), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice",
		"--from-vault", "claude/alice", "--json"); err != nil {
		t.Fatalf("create: %v", err)
	}

	staged, err := os.ReadFile(filepath.Join(base, "alice", ".claude.json"))
	if err != nil {
		t.Fatalf("read staged .claude.json: %v", err)
	}
	if string(staged) != vaultState {
		t.Fatalf("staged .claude.json should come from the vault snapshot; got %s", staged)
	}

	// Explicit --from-claude-json outranks the vault snapshot.
	explicit := filepath.Join(t.TempDir(), "explicit.json")
	explicitState := `{"hasCompletedOnboarding":true,"fromExplicitFlag":true}`
	if err := os.WriteFile(explicit, []byte(explicitState), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice2",
		"--from-vault", "claude/alice", "--from-claude-json", explicit, "--json"); err != nil {
		t.Fatalf("create with --from-claude-json: %v", err)
	}
	staged2, err := os.ReadFile(filepath.Join(base, "alice2", ".claude.json"))
	if err != nil {
		t.Fatalf("read staged .claude.json: %v", err)
	}
	if string(staged2) != explicitState {
		t.Fatalf("--from-claude-json should outrank the vault snapshot; got %s", staged2)
	}
}

// Issue #80: an authenticated vault source WITHOUT a saved .claude.json falls
// back to the real HOME state and still gets the onboarding marker merged in.
func TestShallowCreateVaultWithoutSnapshotMergesOnboarding(t *testing.T) {
	base, _ := shallowEnv(t)

	caamHome := os.Getenv("CAAM_HOME")
	vaultDir := filepath.Join(caamHome, "data", "vault", "claude", "bob")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"fake"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "bob",
		"--from-vault", "claude/bob", "--json"); err != nil {
		t.Fatalf("create: %v", err)
	}

	staged, err := os.ReadFile(filepath.Join(base, "bob", ".claude.json"))
	if err != nil {
		t.Fatalf("read staged .claude.json: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(staged, &m); err != nil {
		t.Fatalf("staged .claude.json invalid: %v\n%s", err, staged)
	}
	if string(m["hasCompletedOnboarding"]) != "true" {
		t.Fatalf("onboarding marker not merged; staged = %s", staged)
	}
	// The real HOME's {"x":1} content (from fakeShallowHome) must survive.
	if string(m["x"]) != "1" {
		t.Fatalf("real HOME state fields dropped; staged = %s", staged)
	}
}

// -----------------------------------------------------------------------------
// shallow-spawn ergonomics: default command, create-on-first-use, double-spend.
// -----------------------------------------------------------------------------

// fakeToolBin puts no-op executables for the named tools on PATH so
// exec.LookPath resolves them without ever running a real provider CLI.
func fakeToolBin(t *testing.T, names ...string) string {
	t.Helper()
	binDir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(binDir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", n, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir
}

// captureSpawn swaps in a recording spawnExec and returns a pointer to the argv
// it saw (nil-length until a spawn happens).
func captureSpawn(t *testing.T) *[]string {
	t.Helper()
	got := new([]string)
	orig := spawnExec
	spawnExec = func(_ string, args []string, _ []string) error { *got = args; return nil }
	t.Cleanup(func() { spawnExec = orig })
	return got
}

// writeClaudeIdentity writes a .claude.json carrying a specific oauthAccount.
func writeClaudeIdentity(t *testing.T, path, uuid, email string) {
	t.Helper()
	body := fmt.Sprintf(`{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":%q,"emailAddress":%q}}`, uuid, email)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestShallowSpawnDefaultsToProviderCLI: `caam shallow-spawn <name>` with no
// `-- <cmd>` section execs the profile's OWN provider CLI, so "open Claude as
// alice in this terminal" is one command with no wrapper script.
func TestShallowSpawnDefaultsToProviderCLI(t *testing.T) {
	base, realHome := shallowEnv(t)
	fakeToolBin(t, "claude", "codex")

	codexDir := filepath.Join(realHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"OPENAI_API_KEY":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "cx", "--tool", "codex",
		"--from-file", filepath.Join(codexDir, "auth.json"), "--json"); err != nil {
		t.Fatal(err)
	}

	origCheck := shallowCodexDaemonCheck
	shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
	t.Cleanup(func() { shallowCodexDaemonCheck = origCheck })

	var envSeen []string
	origExec := spawnExec
	var argvSeen []string
	spawnExec = func(_ string, args []string, env []string) error {
		argvSeen, envSeen = args, env
		return nil
	}
	t.Cleanup(func() { spawnExec = origExec })

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice"); err != nil {
		t.Fatalf("spawn alice: %v", err)
	}
	if len(argvSeen) != 1 || argvSeen[0] != "claude" {
		t.Fatalf("claude profile default argv = %v, want [claude]", argvSeen)
	}
	wantHome := "HOME=" + filepath.Join(base, "alice")
	found := false
	for _, e := range envSeen {
		if e == wantHome {
			found = true
		}
	}
	if !found {
		t.Fatalf("default spawn did not set %s", wantHome)
	}

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "cx"); err != nil {
		t.Fatalf("spawn cx: %v", err)
	}
	if len(argvSeen) != 1 || argvSeen[0] != "codex" {
		t.Fatalf("codex profile default argv = %v, want [codex]", argvSeen)
	}

	// An explicit command still wins over the default.
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--", "sh", "-c", "true"); err != nil {
		t.Fatalf("explicit spawn: %v", err)
	}
	if len(argvSeen) != 3 || argvSeen[0] != "sh" {
		t.Fatalf("explicit argv = %v, want sh -c true", argvSeen)
	}
}

// TestShallowSpawnCreatesMissingProfile: naming a profile that does not exist
// creates it as an EMPTY shallow profile and continues to spawn, so the first
// run of a new identity is a login instead of an error.
func TestShallowSpawnCreatesMissingProfile(t *testing.T) {
	base, _ := shallowEnv(t)
	fakeToolBin(t, "claude", "codex")
	argv := captureSpawn(t)

	_, stderr, err := runCmdCaptured(t, "shallow-spawn", "newbie")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !strings.Contains(stderr, `created shallow profile "newbie" (empty)`) {
		t.Fatalf("expected create notice on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "log in") {
		t.Fatalf("create notice should explain the first run logs in, got %q", stderr)
	}
	if len(*argv) != 1 || (*argv)[0] != "claude" {
		t.Fatalf("argv after create-on-first-use = %v, want [claude]", *argv)
	}
	// The profile is a real, EMPTY claude shallow profile.
	cred := filepath.Join(base, "newbie", ".claude", ".credentials.json")
	body, err := os.ReadFile(cred)
	if err != nil {
		t.Fatalf("read new profile credentials: %v", err)
	}
	if strings.TrimSpace(string(body)) != "" {
		t.Fatalf("first-use profile should have EMPTY credentials, got %q", body)
	}

	// A second spawn reuses the profile without re-announcing creation.
	_, stderr2, err := runCmdCaptured(t, "shallow-spawn", "newbie")
	if err != nil {
		t.Fatalf("second spawn: %v", err)
	}
	if strings.Contains(stderr2, "created shallow profile") {
		t.Fatalf("second spawn should not recreate, stderr=%q", stderr2)
	}
}

// TestShallowSpawnCreatesMissingProfileWithTool: --tool picks the layout for a
// create-on-first-use spawn.
func TestShallowSpawnCreatesMissingProfileWithTool(t *testing.T) {
	base, _ := shallowEnv(t)
	fakeToolBin(t, "claude", "codex")
	argv := captureSpawn(t)
	origCheck := shallowCodexDaemonCheck
	shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
	t.Cleanup(func() { shallowCodexDaemonCheck = origCheck })

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "cxnew", "--tool", "codex"); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(*argv) != 1 || (*argv)[0] != "codex" {
		t.Fatalf("argv = %v, want [codex]", *argv)
	}
	if _, err := os.Stat(filepath.Join(base, "cxnew", ".codex", "auth.json")); err != nil {
		t.Fatalf("expected a codex layout: %v", err)
	}
}

// TestShallowSpawnPrintEnvDoesNotCreate: --print-env is a dry run. It never
// provisions a profile; a missing name is an error that names the fix.
func TestShallowSpawnPrintEnvDoesNotCreate(t *testing.T) {
	base, _ := shallowEnv(t)
	_, _, err := runCmdCaptured(t, "shallow-spawn", "ghost", "--print-env")
	if err == nil {
		t.Fatal("expected --print-env on a missing profile to fail")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, "ghost")); !os.IsNotExist(statErr) {
		t.Fatalf("--print-env must not create the profile (err=%v)", statErr)
	}
}

// TestShallowSpawnDoubleSpendGuard covers the quota guard: refuse to open a
// shallow claude profile that is the SAME account already active in the real
// HOME, since both sessions would draw down one subscription.
func TestShallowSpawnDoubleSpendGuard(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"

	// setupClaude builds a logged-in claude shallow profile "alice" whose
	// identity is (profUUID, profEmail), with the live real HOME logged in as
	// liveJSON (written verbatim; "" leaves the real HOME's default state).
	setupClaude := func(t *testing.T, profUUID, profEmail, liveJSON string) (base string) {
		t.Helper()
		base, realHome := shallowEnv(t)
		fakeToolBin(t, "claude")

		credSrc := filepath.Join(t.TempDir(), "creds.json")
		if err := os.WriteFile(credSrc, []byte(`{"claudeAiOauth":{"accessToken":"fake"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice",
			"--from-file", credSrc, "--json"); err != nil {
			t.Fatal(err)
		}
		writeClaudeIdentity(t, filepath.Join(base, "alice", ".claude.json"), profUUID, profEmail)
		if liveJSON == "" {
			writeClaudeIdentity(t, filepath.Join(realHome, ".claude.json"), uuid, "me@example.com")
		} else if err := os.WriteFile(filepath.Join(realHome, ".claude.json"), []byte(liveJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		return base
	}

	t.Run("refuses when the account is already live", func(t *testing.T) {
		setupClaude(t, uuid, "me@example.com", "")
		argv := captureSpawn(t)
		_, _, err := runCmdCaptured(t, "shallow-spawn", "alice")
		if err == nil {
			t.Fatal("expected the double-spend guard to refuse")
		}
		if !strings.Contains(err.Error(), "same quota twice") || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("unhelpful guard error: %v", err)
		}
		if len(*argv) != 0 {
			t.Fatalf("guard must block the exec, but argv=%v", *argv)
		}
	})

	t.Run("matches on email when the uuid is absent", func(t *testing.T) {
		base, realHome := shallowEnv(t)
		fakeToolBin(t, "claude")
		credSrc := filepath.Join(t.TempDir(), "creds.json")
		if err := os.WriteFile(credSrc, []byte(`{"claudeAiOauth":{"accessToken":"fake"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice",
			"--from-file", credSrc, "--json"); err != nil {
			t.Fatal(err)
		}
		writeClaudeIdentity(t, filepath.Join(base, "alice", ".claude.json"), "", "me@example.com")
		writeClaudeIdentity(t, filepath.Join(realHome, ".claude.json"), uuid, "me@example.com")
		captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice"); err == nil {
			t.Fatal("expected an email match to refuse")
		}
	})

	t.Run("--force overrides", func(t *testing.T) {
		setupClaude(t, uuid, "me@example.com", "")
		argv := captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--force"); err != nil {
			t.Fatalf("--force should override the guard: %v", err)
		}
		if len(*argv) != 1 || (*argv)[0] != "claude" {
			t.Fatalf("argv = %v, want [claude]", *argv)
		}
	})

	t.Run("allows a different account", func(t *testing.T) {
		setupClaude(t, "99999999-8888-7777-6666-555555555555", "other@example.com", "")
		argv := captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice"); err != nil {
			t.Fatalf("a different account must spawn: %v", err)
		}
		if len(*argv) != 1 {
			t.Fatalf("argv = %v", *argv)
		}
	})

	t.Run("skips when the live HOME has no oauthAccount", func(t *testing.T) {
		setupClaude(t, uuid, "me@example.com", `{"hasCompletedOnboarding":true}`)
		argv := captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice"); err != nil {
			t.Fatalf("no live identity means no guard: %v", err)
		}
		if len(*argv) != 1 {
			t.Fatalf("argv = %v", *argv)
		}
	})

	t.Run("skips when the profile has no oauthAccount", func(t *testing.T) {
		base := setupClaude(t, uuid, "me@example.com", "")
		if err := os.WriteFile(filepath.Join(base, "alice", ".claude.json"), []byte(`{"x":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		argv := captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice"); err != nil {
			t.Fatalf("no profile identity means no guard: %v", err)
		}
		if len(*argv) != 1 {
			t.Fatalf("argv = %v", *argv)
		}
	})

	// A profile that has never been logged in inherits the real HOME's
	// .claude.json (that is where its onboarding state comes from), so identity
	// alone would false-positive. An empty credential file means "not logged in
	// here yet" and must never be refused.
	t.Run("skips a profile that is not logged in yet", func(t *testing.T) {
		base, realHome := shallowEnv(t)
		fakeToolBin(t, "claude")
		writeClaudeIdentity(t, filepath.Join(realHome, ".claude.json"), uuid, "me@example.com")
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "fresh", "--json"); err != nil {
			t.Fatal(err)
		}
		// create copies the real HOME state, so the identities DO match.
		staged, err := os.ReadFile(filepath.Join(base, "fresh", ".claude.json"))
		if err != nil || !strings.Contains(string(staged), uuid) {
			t.Fatalf("precondition: expected the seeded state to carry the live uuid, got %q err=%v", staged, err)
		}
		argv := captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "fresh"); err != nil {
			t.Fatalf("an empty profile must still spawn (it needs to log in): %v", err)
		}
		if len(*argv) != 1 {
			t.Fatalf("argv = %v", *argv)
		}
	})

	t.Run("skips non-claude providers", func(t *testing.T) {
		_, realHome := shallowEnv(t)
		fakeToolBin(t, "codex")
		writeClaudeIdentity(t, filepath.Join(realHome, ".claude.json"), uuid, "me@example.com")
		codexDir := filepath.Join(realHome, ".codex")
		if err := os.MkdirAll(codexDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"OPENAI_API_KEY":null}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "cx", "--tool", "codex",
			"--from-file", filepath.Join(codexDir, "auth.json"), "--json"); err != nil {
			t.Fatal(err)
		}
		origCheck := shallowCodexDaemonCheck
		shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
		t.Cleanup(func() { shallowCodexDaemonCheck = origCheck })
		argv := captureSpawn(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "cx"); err != nil {
			t.Fatalf("codex has no identity file to compare: %v", err)
		}
		if len(*argv) != 1 {
			t.Fatalf("argv = %v", *argv)
		}
	})

	// --print-env is a dry run that spends nothing, so the guard never fires.
	t.Run("does not block --print-env", func(t *testing.T) {
		setupClaude(t, uuid, "me@example.com", "")
		stdout, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--print-env")
		if err != nil {
			t.Fatalf("--print-env must not be guarded: %v", err)
		}
		if !strings.Contains(stdout, "HOME=") {
			t.Fatalf("unexpected --print-env output %q", stdout)
		}
	})
}

// TestShallowSpawnHelpDocumentsErgonomics keeps the help text honest about the
// three ergonomics rules a user has to know before typing the short form.
func TestShallowSpawnHelpDocumentsErgonomics(t *testing.T) {
	help := shallowSpawnCmd.Long + "\n" + shallowSpawnCmd.Use + "\n" + shallowSpawnCmd.Short
	for _, want := range []string{
		"caam shallow-spawn alice",
		"--tool",
		"--force",
		"--print-env",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("shallow-spawn help does not mention %q", want)
		}
	}
	if !strings.Contains(help, "created") && !strings.Contains(help, "create") {
		t.Fatal("shallow-spawn help does not mention create-on-first-use")
	}
}

// TestShallowSpawnToolConflictsWithExistingProfile: --tool only chooses a
// layout for a profile being created, so naming an existing profile with a
// different provider must fail loudly instead of looking like a conversion.
func TestShallowSpawnToolConflictsWithExistingProfile(t *testing.T) {
	_, _ = shallowEnv(t)
	fakeToolBin(t, "claude", "codex")
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	argv := captureSpawn(t)
	_, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--tool", "codex")
	if err == nil {
		t.Fatal("expected --tool on an existing claude profile to fail")
	}
	if !strings.Contains(err.Error(), "conflicts with existing shallow profile") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*argv) != 0 {
		t.Fatalf("conflict must block the exec, argv=%v", *argv)
	}
	// The matching --tool is accepted.
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--tool", "claude"); err != nil {
		t.Fatalf("matching --tool must be accepted: %v", err)
	}
}

// TestPrependShallowLocalBin covers the PATH adjustment that keeps Claude
// Code's "~/.local/bin is not in your PATH" diagnostic quiet under a shallow
// HOME whose .local/bin is a symlink to the real one.
func TestPrependShallowLocalBin(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	sep := string(os.PathListSeparator)

	if got := prependShallowLocalBin("/usr/bin", home); got != "/usr/bin" {
		t.Fatalf("missing .local/bin: PATH changed to %q", got)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := prependShallowLocalBin("/usr/bin", home), bin+sep+"/usr/bin"; got != want {
		t.Fatalf("prepend: got %q, want %q", got, want)
	}
	if got, want := prependShallowLocalBin(bin+sep+"/usr/bin", home), bin+sep+"/usr/bin"; got != want {
		t.Fatalf("already present: got %q, want %q", got, want)
	}
	if got := prependShallowLocalBin("", home); got != bin {
		t.Fatalf("empty PATH: got %q, want %q", got, bin)
	}
}

// TestShallowList_ShowsLiveLoginState pins the LOGIN column: it reflects the
// profile's credential file and identity right now, not the creation-time
// source, which stays "(none)" for a profile that logged in later.
func TestShallowList_ShowsLiveLoginState(t *testing.T) {
	base, _ := shallowEnv(t)

	for _, name := range []string{"fresh", "logged"} {
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", name, "--tool", "claude"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	loggedHome := filepath.Join(base, "logged")
	if err := os.WriteFile(filepath.Join(loggedHome, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"t","refreshToken":"r"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loggedHome, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"u-logged","emailAddress":"logged@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCmdCaptured(t, "shallow-profile", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "LOGIN") || !strings.Contains(stdout, "logged@example.com") || !strings.Contains(stdout, "not logged in") {
		t.Fatalf("table lacks live login state:\n%s", stdout)
	}

	stdout, _, err = runCmdCaptured(t, "shallow-profile", "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var out struct {
		Profiles []struct {
			Name     string `json:"name"`
			LoggedIn bool   `json:"logged_in"`
			Identity string `json:"identity"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	got := map[string][2]any{}
	for _, p := range out.Profiles {
		got[p.Name] = [2]any{p.LoggedIn, p.Identity}
	}
	if got["fresh"] != [2]any{false, ""} {
		t.Fatalf("fresh: %v", got["fresh"])
	}
	if got["logged"] != [2]any{true, "logged@example.com"} {
		t.Fatalf("logged: %v", got["logged"])
	}
}
