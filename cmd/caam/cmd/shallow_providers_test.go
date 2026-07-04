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

// TestShallowSpawnCodex covers the --reload-daemon flag added for issue #45:
//   - the flag is parsed/consumed by shallow-spawn and NOT forwarded to <cmd>,
//   - the codex daemon detect/reload hook runs (with reload=true) for a codex
//     profile,
//   - the hook is SKIPPED for a non-codex (claude) profile even when the flag
//     is passed,
//   - --print-env stays a non-exec, non-daemon path.
func TestShallowSpawnCodex(t *testing.T) {
	// daemonCall records one invocation of the daemon detect/reload hook.
	type daemonCall struct {
		tool      string
		reload    bool
		codexHome string
	}

	// installSeams swaps in observable spawnExec + daemon-check hooks and returns
	// pointers to the recorded child argv and the daemon-hook call log.
	installSeams := func(t *testing.T) (childArgs *[]string, calls *[]daemonCall, spawned *bool) {
		t.Helper()
		var gotArgs []string
		var gotCalls []daemonCall
		var didSpawn bool

		origExec := spawnExec
		spawnExec = func(_ string, args []string, _ []string) error {
			gotArgs = args
			didSpawn = true
			return nil
		}
		origCheck := shallowCodexDaemonCheck
		shallowCodexDaemonCheck = func(tool string, reload bool, codexHome string) codexDaemonWarning {
			gotCalls = append(gotCalls, daemonCall{tool: tool, reload: reload, codexHome: codexHome})
			// Return an empty (undetected) warning so printCodexDaemonWarning is a
			// no-op and the test does not depend on any real host daemon.
			return codexDaemonWarning{}
		}
		t.Cleanup(func() {
			spawnExec = origExec
			shallowCodexDaemonCheck = origCheck
		})
		return &gotArgs, &gotCalls, &didSpawn
	}

	t.Run("codex profile consumes flag and reloads daemon", func(t *testing.T) {
		base, _ := shallowEnv(t)
		stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
			"--from-vault", "codex/bob", "--json"); err != nil {
			t.Fatal(err)
		}

		childArgs, calls, spawned := installSeams(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "codex-bob",
			"--reload-daemon", "--", "sh", "-c", "true"); err != nil {
			t.Fatalf("spawn: %v", err)
		}

		if !*spawned {
			t.Fatalf("expected the command to be exec'd")
		}
		// The flag must be consumed by shallow-spawn, not forwarded to the child.
		wantChild := []string{"sh", "-c", "true"}
		if strings.Join(*childArgs, "\x00") != strings.Join(wantChild, "\x00") {
			t.Fatalf("child argv = %v, want %v (flag must not leak to child)", *childArgs, wantChild)
		}
		for _, a := range *childArgs {
			if a == "--reload-daemon" {
				t.Fatalf("--reload-daemon leaked into child argv: %v", *childArgs)
			}
		}
		// The codex daemon hook must have run exactly once, with reload=true.
		if len(*calls) != 1 {
			t.Fatalf("expected 1 daemon-check call, got %d: %+v", len(*calls), *calls)
		}
		if (*calls)[0].tool != "codex" || !(*calls)[0].reload {
			t.Fatalf("daemon-check call = %+v, want {codex true}", (*calls)[0])
		}
		// The reload must be SCOPED to this profile's CODEX_HOME (issue #47), not
		// a host-wide reload — otherwise it would disrupt concurrent codex
		// profiles.
		wantHome := filepath.Join(base, "codex-bob", ".codex")
		if (*calls)[0].codexHome != wantHome {
			t.Fatalf("daemon-check codexHome = %q, want %q (reload must be scoped to the target profile)", (*calls)[0].codexHome, wantHome)
		}
	})

	t.Run("codex profile without flag warns but does not reload", func(t *testing.T) {
		_, _ = shallowEnv(t)
		stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
			"--from-vault", "codex/bob", "--json"); err != nil {
			t.Fatal(err)
		}

		_, calls, _ := installSeams(t)
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "codex-bob",
			"--", "sh", "-c", "true"); err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if len(*calls) != 1 {
			t.Fatalf("expected 1 daemon-check call, got %d", len(*calls))
		}
		if (*calls)[0].reload {
			t.Fatalf("daemon-check must default to reload=false, got %+v", (*calls)[0])
		}
	})

	t.Run("non-codex profile accepts flag but skips daemon action", func(t *testing.T) {
		_, _ = shallowEnv(t)
		// A claude profile (default provider) with empty credentials.
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
			t.Fatal(err)
		}

		childArgs, calls, spawned := installSeams(t)
		// --reload-daemon must be ACCEPTED (no parse error) for a non-codex profile.
		if _, _, err := runCmdCaptured(t, "shallow-spawn", "alice",
			"--reload-daemon", "--", "sh", "-c", "true"); err != nil {
			t.Fatalf("spawn (non-codex must accept --reload-daemon): %v", err)
		}
		if !*spawned {
			t.Fatalf("expected the command to be exec'd")
		}
		for _, a := range *childArgs {
			if a == "--reload-daemon" {
				t.Fatalf("--reload-daemon leaked into child argv: %v", *childArgs)
			}
		}
		// ...but the codex daemon hook must NOT run for a non-codex provider.
		if len(*calls) != 0 {
			t.Fatalf("daemon-check must be skipped for non-codex provider, got %+v", *calls)
		}
	})

	t.Run("print-env stays non-exec and skips daemon check", func(t *testing.T) {
		base, _ := shallowEnv(t)
		stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
			"--from-vault", "codex/bob", "--json"); err != nil {
			t.Fatal(err)
		}

		_, calls, spawned := installSeams(t)
		// --print-env together with --reload-daemon must still be a pure env dump.
		stdout, _, err := runCmdCaptured(t, "shallow-spawn", "codex-bob",
			"--reload-daemon", "--print-env")
		if err != nil {
			t.Fatalf("print-env: %v", err)
		}
		if *spawned {
			t.Fatalf("--print-env must not exec anything")
		}
		if len(*calls) != 0 {
			t.Fatalf("--print-env must not trigger the daemon check, got %+v", *calls)
		}
		home := filepath.Join(base, "codex-bob")
		if !strings.Contains(stdout, "HOME="+home) {
			t.Fatalf("print-env missing HOME in %q", stdout)
		}
		if !strings.Contains(stdout, "CODEX_HOME="+filepath.Join(home, ".codex")) {
			t.Fatalf("print-env missing CODEX_HOME in %q", stdout)
		}
	})
}
