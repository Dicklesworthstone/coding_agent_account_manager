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
