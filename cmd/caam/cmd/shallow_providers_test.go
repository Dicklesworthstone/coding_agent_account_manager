package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stageVaultFile writes a file into the isolated CAAM vault for a given
// tool/profile (CAAM_HOME/data/vault/<tool>/<profile>/<basename>).
func stageVaultFile(t *testing.T, tool, profile, basename, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("CAAM_HOME"), "data", "vault", tool, profile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir vault %s/%s: %v", tool, profile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, basename), []byte(body), 0o600); err != nil {
		t.Fatalf("write vault %s: %v", basename, err)
	}
}

func TestShallowCreateCodexFromVault(t *testing.T) {
	base, _ := shallowEnv(t)
	stageVaultFile(t, "codex", "bob", "auth.json", `{"codex":"bob"}`)

	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
		"--from-vault", "codex/bob", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var resp struct {
		Success        bool   `json:"success"`
		Provider       string `json:"provider"`
		Path           string `json:"path"`
		CredentialPath string `json:"credential_path"`
		CredentialFrom string `json:"credential_from"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if !resp.Success || resp.Provider != "codex" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.CredentialFrom != "vault:codex/bob" {
		t.Fatalf("credential_from %q", resp.CredentialFrom)
	}

	home := filepath.Join(base, "codex-bob")
	authDst := filepath.Join(home, ".codex", "auth.json")
	if resp.CredentialPath != authDst {
		t.Fatalf("credential_path %q != %q", resp.CredentialPath, authDst)
	}
	// auth.json is a real file with our vault contents.
	st, err := os.Lstat(authDst)
	if err != nil {
		t.Fatalf("stat auth: %v", err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".codex/auth.json must be a real file")
	}
	if body, _ := os.ReadFile(authDst); string(body) != `{"codex":"bob"}` {
		t.Fatalf("auth contents %q", body)
	}
	// config.toml enforces file store.
	if cfg, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatalf("read config.toml: %v", err)
	} else if !strings.Contains(string(cfg), `cli_auth_credentials_store = "file"`) {
		t.Fatalf("config.toml missing file-store: %q", cfg)
	}
}

func TestShallowCreateAgyFromVault(t *testing.T) {
	base, _ := shallowEnv(t)
	stageVaultFile(t, "agy", "carol", "antigravity-oauth-token", "CAROL-TOKEN")
	stageVaultFile(t, "agy", "carol", "google_accounts.json", `{"acct":"carol"}`)

	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "agy-carol",
		"--from-vault", "agy/carol", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var resp struct {
		Success  bool   `json:"success"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	if !resp.Success || resp.Provider != "agy" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	home := filepath.Join(base, "agy-carol")
	tok := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if body, _ := os.ReadFile(tok); string(body) != "CAROL-TOKEN" {
		t.Fatalf("token %q", body)
	}
	if st, err := os.Lstat(tok); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("token must be a real file (err=%v)", err)
	}
	// Optional google_accounts.json copied through.
	ga := filepath.Join(home, ".gemini", "google_accounts.json")
	if body, _ := os.ReadFile(ga); string(body) != `{"acct":"carol"}` {
		t.Fatalf("google_accounts.json %q", body)
	}
}

func TestShallowCreateMissingCodexAuth(t *testing.T) {
	_, _ = shallowEnv(t)
	// Stage the profile dir but no auth.json.
	stageVaultFile(t, "codex", "ghost", "placeholder", "x")
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-ghost",
		"--from-vault", "codex/ghost", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, "missing auth.json") {
		t.Fatalf("expected missing-auth error, got %q", stdout)
	}
}

func TestShallowCreateToolConflictsWithVault(t *testing.T) {
	_, _ = shallowEnv(t)
	stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "x",
		"--from-vault", "codex/bob", "--tool", "agy", "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, "conflicts with --from-vault") {
		t.Fatalf("expected tool conflict error, got %q", stdout)
	}
}

func TestShallowCreateUnknownTool(t *testing.T) {
	_, _ = shallowEnv(t)
	src := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmdCaptured(t, "shallow-profile", "create", "x",
		"--tool", "bogus", "--from-file", src, "--json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(stdout, "unsupported shallow provider") {
		t.Fatalf("expected unsupported-provider error, got %q", stdout)
	}
}

func TestShallowSpawnPrintEnvCodex(t *testing.T) {
	base, _ := shallowEnv(t)
	stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
		"--from-vault", "codex/bob", "--json"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmdCaptured(t, "shallow-spawn", "codex-bob", "--print-env")
	if err != nil {
		t.Fatalf("print-env: %v", err)
	}
	home := filepath.Join(base, "codex-bob")
	if !strings.Contains(stdout, "HOME="+home) {
		t.Fatalf("missing HOME in %q", stdout)
	}
	if !strings.Contains(stdout, "SHALLOW_PROFILE=codex-bob") {
		t.Fatalf("missing SHALLOW_PROFILE in %q", stdout)
	}
	if !strings.Contains(stdout, "CODEX_HOME="+filepath.Join(home, ".codex")) {
		t.Fatalf("missing CODEX_HOME in %q", stdout)
	}
}

func TestShallowSpawnPrintEnvAgy(t *testing.T) {
	base, _ := shallowEnv(t)
	stageVaultFile(t, "agy", "carol", "antigravity-oauth-token", "T")
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "agy-carol",
		"--from-vault", "agy/carol", "--json"); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmdCaptured(t, "shallow-spawn", "agy-carol", "--print-env")
	if err != nil {
		t.Fatalf("print-env: %v", err)
	}
	home := filepath.Join(base, "agy-carol")
	if !strings.Contains(stdout, "HOME="+home) || !strings.Contains(stdout, "SHALLOW_PROFILE=agy-carol") {
		t.Fatalf("missing base env in %q", stdout)
	}
	if !strings.Contains(stdout, "GEMINI_HOME="+filepath.Join(home, ".gemini")) {
		t.Fatalf("missing GEMINI_HOME in %q", stdout)
	}
}

// TestShallowSpawnCodexSetsCodexHome injects a fake spawnExec and verifies the
// codex shallow session gets CODEX_HOME pinned inside the shallow HOME, even
// when a parent CODEX_HOME is inherited (it must be overridden, not leaked).
func TestShallowSpawnCodexSetsCodexHome(t *testing.T) {
	base, _ := shallowEnv(t)
	stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
		"--from-vault", "codex/bob", "--json"); err != nil {
		t.Fatal(err)
	}

	// Simulate a leaky parent shell.
	t.Setenv("CODEX_HOME", "/real/home/.codex")

	var gotEnv []string
	orig := spawnExec
	spawnExec = func(_ string, _ []string, env []string) error { gotEnv = env; return nil }
	t.Cleanup(func() { spawnExec = orig })

	if _, _, err := runCmdCaptured(t, "shallow-spawn", "codex-bob", "--", "sh", "-c", "true"); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	want := "CODEX_HOME=" + filepath.Join(base, "codex-bob", ".codex")
	leaked := "CODEX_HOME=/real/home/.codex"
	var found, sawLeak bool
	for _, e := range gotEnv {
		if e == want {
			found = true
		}
		if e == leaked {
			sawLeak = true
		}
	}
	if !found {
		t.Fatalf("env missing %s; got %v", want, gotEnv)
	}
	if sawLeak {
		t.Fatalf("inherited real CODEX_HOME leaked into shallow session: %v", gotEnv)
	}
}
