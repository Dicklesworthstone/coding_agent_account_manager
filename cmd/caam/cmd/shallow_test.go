package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
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

// TestShallowSpawnDefaultsToProviderCLI: with no '-- <cmd>' section,
// shallow-spawn runs the profile's own provider CLI under the shallow HOME,
// and an explicit command still wins (PR #89).
func TestShallowSpawnDefaultsToProviderCLI(t *testing.T) {
	base, realHome := shallowEnv(t)

	// Fake provider binaries so exec.LookPath resolves them without ever
	// running a real CLI.
	binDir := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
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

	var gotBin string
	var gotArgs, gotEnv []string
	origExec := spawnExec
	spawnExec = func(bin string, args []string, env []string) error {
		gotBin, gotArgs, gotEnv = bin, args, env
		return nil
	}
	t.Cleanup(func() { spawnExec = origExec })
	origCheck := shallowCodexDaemonCheck
	shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
	t.Cleanup(func() { shallowCodexDaemonCheck = origCheck })

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice"); err != nil {
		t.Fatalf("spawn alice: %v", err)
	}
	if gotBin != filepath.Join(binDir, "claude") || len(gotArgs) != 1 || gotArgs[0] != "claude" {
		t.Fatalf("claude profile: bin=%q args=%v, want the claude stub with argv [claude]", gotBin, gotArgs)
	}
	wantHome := "HOME=" + filepath.Join(base, "alice")
	found := false
	for _, e := range gotEnv {
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
	if len(gotArgs) != 1 || gotArgs[0] != "codex" {
		t.Fatalf("codex profile: args=%v, want [codex]", gotArgs)
	}

	// --effort applies to the defaulted codex command too.
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "cx", "--effort", "high"); err != nil {
		t.Fatalf("spawn cx --effort: %v", err)
	}
	if strings.Join(gotArgs, " ") != "codex -c model_reasoning_effort=high" {
		t.Fatalf("codex --effort args=%v, want the injected override", gotArgs)
	}

	// An explicit command still wins over the default.
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--", "sh", "-c", "true"); err != nil {
		t.Fatalf("explicit spawn: %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[0] != "sh" {
		t.Fatalf("explicit args=%v, want [sh -c true]", gotArgs)
	}

	// --print-env is unaffected: it still prints and never execs.
	gotBin = ""
	stdout, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--print-env")
	if err != nil {
		t.Fatalf("print-env: %v", err)
	}
	if !strings.Contains(stdout, wantHome) || gotBin != "" {
		t.Fatalf("print-env stdout=%q bin=%q, want HOME line and no exec", stdout, gotBin)
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

// TestShallowSpawnSyncsClaudeConfig proves the end-to-end wiring of the
// shared-configuration refresh (#93): before exec, a claude shallow profile's
// private .claude.json picks up the allowlisted preference keys from the real
// ~/.claude.json while keeping its own identity; --no-sync-config and
// --print-env leave the profile untouched.
func TestShallowSpawnSyncsClaudeConfig(t *testing.T) {
	base, realHome := shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(base, "alice", ".claude.json")
	profileState := `{"oauthAccount":{"emailAddress":"alice@example.com"},"theme":"light"}`
	if err := os.WriteFile(profilePath, []byte(profileState), 0o600); err != nil {
		t.Fatal(err)
	}
	realState := `{"oauthAccount":{"emailAddress":"real@example.com"},"theme":"dark","editorMode":"vim"}`
	if err := os.WriteFile(filepath.Join(realHome, ".claude.json"), []byte(realState), 0o600); err != nil {
		t.Fatal(err)
	}

	origExec := spawnExec
	spawnExec = func(_ string, _ []string, _ []string) error { return nil }
	t.Cleanup(func() { spawnExec = origExec })

	readProfile := func() string {
		t.Helper()
		raw, err := os.ReadFile(profilePath)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	// --print-env and --no-sync-config are read-only for the profile file.
	if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice", "--print-env"); err != nil {
		t.Fatalf("print-env: %v", err)
	}
	if got := readProfile(); got != profileState {
		t.Fatalf("--print-env mutated the profile: %s", got)
	}
	if _, stderr, err := runCmdCaptured(t, "shallow-spawn", "alice", "--no-sync-config", "--", "sh", "-c", "true"); err != nil {
		t.Fatalf("spawn --no-sync-config: %v (stderr %q)", err, stderr)
	}
	if got := readProfile(); got != profileState {
		t.Fatalf("--no-sync-config mutated the profile: %s", got)
	}

	_, stderr, err := runCmdCaptured(t, "shallow-spawn", "alice", "--", "sh", "-c", "true")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !strings.Contains(stderr, "refreshed 2 shared Claude settings") {
		t.Fatalf("expected refresh note on stderr, got %q", stderr)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readProfile()), &got); err != nil {
		t.Fatal(err)
	}
	if string(got["theme"]) != `"dark"` || string(got["editorMode"]) != `"vim"` {
		t.Fatalf("preferences not refreshed: %s", readProfile())
	}
	if !strings.Contains(string(got["oauthAccount"]), "alice@example.com") {
		t.Fatalf("profile identity clobbered by the real HOME's: %s", got["oauthAccount"])
	}
	realRaw, _ := os.ReadFile(filepath.Join(realHome, ".claude.json"))
	if string(realRaw) != realState {
		t.Fatal("real ~/.claude.json was modified by shallow-spawn")
	}
}
