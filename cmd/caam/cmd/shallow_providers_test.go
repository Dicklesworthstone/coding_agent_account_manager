package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
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

// TestShallowSpawnScrubsForeignProviderHomes: a shallow session of ANY
// provider must drop every inherited provider-home override (issue #106). A
// claude shallow spawn launched from inside a codex shallow session used to
// carry the outer CODEX_HOME into nested tools (ccusage, codex, a nested
// shallow-spawn), letting them read the outer profile's state. The session's
// own provider still gets its variable re-pinned inside the shallow HOME.
func TestShallowSpawnScrubsForeignProviderHomes(t *testing.T) {
	base, _ := shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "alice", "--json"); err != nil {
		t.Fatal(err)
	}
	stageVaultFile(t, "codex", "bob", "auth.json", `{}`)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "codex-bob",
		"--from-vault", "codex/bob", "--json"); err != nil {
		t.Fatal(err)
	}
	stageVaultFile(t, "agy", "carol", "antigravity-oauth-token", "T")
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "agy-carol",
		"--from-vault", "agy/carol", "--json"); err != nil {
		t.Fatal(err)
	}

	// Simulate being launched from inside another shallow session (or any
	// leaky parent shell) that exported every provider home.
	leaks := map[string]string{
		"CLAUDE_CONFIG_DIR": "/outer/profile/.claude",
		"CODEX_HOME":        "/outer/profile/.codex",
		"GEMINI_HOME":       "/outer/profile/.gemini",
	}
	for k, v := range leaks {
		t.Setenv(k, v)
	}

	var gotEnv []string
	orig := spawnExec
	spawnExec = func(_ string, _ []string, env []string) error { gotEnv = env; return nil }
	t.Cleanup(func() { spawnExec = orig })

	cases := []struct {
		profile string
		pinned  map[string]string
	}{
		{"alice", nil},
		{"codex-bob", map[string]string{"CODEX_HOME": filepath.Join(base, "codex-bob", ".codex")}},
		{"agy-carol", map[string]string{"GEMINI_HOME": filepath.Join(base, "agy-carol", ".gemini")}},
	}
	for _, tc := range cases {
		gotEnv = nil
		if _, _, err := runCmdCaptured(t, "shallow-spawn", tc.profile, "--", "sh", "-c", "true"); err != nil {
			t.Fatalf("%s: spawn: %v", tc.profile, err)
		}
		env := make(map[string]string, len(gotEnv))
		for _, e := range gotEnv {
			if i := strings.IndexByte(e, '='); i > 0 {
				env[e[:i]] = e[i+1:]
			}
		}
		for k, leaked := range leaks {
			want, pinned := tc.pinned[k]
			got, present := env[k]
			switch {
			case pinned && got != want:
				t.Errorf("%s: %s = %q, want pinned %q", tc.profile, k, got, want)
			case !pinned && present:
				t.Errorf("%s: foreign %s=%q leaked into the shallow session (parent had %q)", tc.profile, k, got, leaked)
			}
		}
		if env["HOME"] != filepath.Join(base, tc.profile) {
			t.Errorf("%s: HOME = %q", tc.profile, env["HOME"])
		}
	}
}

func TestShallowSpawnUsesSelectedCredentialsDespiteAmbientKeys(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		profile string
		keys    []string
	}{
		{"claude", "alice", []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK"}},
		{"codex", "bob", []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			base, _ := shallowEnv(t)
			args := []string{"shallow-profile", "create", tc.profile, "--json"}
			if tc.tool == "codex" {
				stageVaultFile(t, "codex", tc.profile, "auth.json", `{"tokens":{"access_token":"synthetic-selected","refresh_token":"synthetic-refresh"}}`)
				args = append(args, "--from-vault", "codex/"+tc.profile)
			}
			if _, _, err := runCmdCaptured(t, args...); err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.keys {
				t.Setenv(key, "synthetic-parent-credential")
			}
			t.Setenv("CAAM_TEST_WORKFLOW", "keep")
			var got []string
			original := spawnExec
			spawnExec = func(_ string, _ []string, env []string) error {
				got = env
				return nil
			}
			t.Cleanup(func() { spawnExec = original })
			if _, _, err := runCmdCaptured(t, "shallow-spawn", tc.profile, "--", "sh", "-c", "true"); err != nil {
				t.Fatal(err)
			}
			values := make(map[string]string)
			for _, entry := range got {
				key, value, ok := strings.Cut(entry, "=")
				if ok {
					values[key] = value
				}
			}
			for _, key := range tc.keys {
				if _, present := values[key]; present {
					t.Errorf("spawn inherited %s instead of using the selected profile", key)
				}
			}
			if values["HOME"] != filepath.Join(base, tc.profile) || values["CAAM_TEST_WORKFLOW"] != "keep" {
				t.Fatal("spawn lost the selected home or unrelated workflow environment")
			}
		})
	}
}

func TestShallowPrintEnvMatchesLaunchWithoutMutation(t *testing.T) {
	for _, tool := range []string{"claude", "codex", "agy"} {
		t.Run(tool, func(t *testing.T) {
			base, realHome := shallowEnv(t)
			if runtime.GOOS != "windows" {
				// These characters must remain literal data through both the
				// emitted shell program and the environment of its descendants.
				base = filepath.Join(filepath.Dir(base), "profile homes ' \" $(touch caam-export-injected)\n;touch caam-export-injected; #")
				t.Setenv("CAAM_SHALLOW_HOMES_DIR", base)
			}
			if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "seat", "--tool", tool, "--json"); err != nil {
				t.Fatal(err)
			}
			polluted := []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_HOME", "XDG_DATA_HOME"}
			switch tool {
			case "claude":
				polluted = append(polluted, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_SESSION_ACCESS_TOKEN")
			case "codex":
				polluted = append(polluted, "OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL")
			}
			for _, key := range polluted {
				t.Setenv(key, "synthetic-export-secret-"+key)
			}
			t.Setenv("CAAM_TEST_EXPORT_WORKFLOW", "preserved")
			beforeEnv := shallowExportEnvironment(os.Environ())
			roots := []string{base, realHome, os.Getenv("CAAM_HOME")}
			before := make([]map[string]string, len(roots))
			for i, path := range roots {
				before[i] = snapshotShallowTestTree(t, path)
			}

			outputs := make(map[string]string)
			for _, mode := range []string{"posix", "json"} {
				args := []string{"shallow-spawn", "seat", "--print-env"}
				if mode != "posix" {
					args = append(args, "--"+mode)
				}
				stdout, stderr, err := runCmdCaptured(t, args...)
				if err != nil {
					t.Fatalf("%s export: %v", mode, err)
				}
				if strings.Contains(stdout+stderr, "synthetic-export-secret") {
					t.Fatalf("%s export exposed an inherited credential", mode)
				}
				outputs[mode] = stdout
			}
			var changes provider.EnvironmentChanges
			if err := json.Unmarshal([]byte(outputs["json"]), &changes); err != nil {
				t.Fatalf("JSON export is not environment data: %v", err)
			}
			if changes.Set["HOME"] != filepath.Join(base, "seat") || changes.Set["SHALLOW_PROFILE"] != "seat" {
				t.Fatal("export did not select the requested shallow HOME")
			}
			for _, key := range append(polluted, "CAAM_HOME") {
				if !slices.Contains(changes.Unset, key) || !strings.Contains(outputs["posix"], "unset "+key) {
					t.Errorf("export does not remove inherited %s in every format", key)
				}
			}
			for i, path := range roots {
				if !maps.Equal(before[i], snapshotShallowTestTree(t, path)) {
					t.Fatalf("environment export changed fixture tree %d", i)
				}
			}
			if !maps.Equal(beforeEnv, shallowExportEnvironment(os.Environ())) {
				t.Fatal("environment export changed the caller's environment")
			}

			var launched map[string]string
			oldExec, oldCheck := spawnExec, shallowCodexDaemonCheck
			t.Cleanup(func() { spawnExec, shallowCodexDaemonCheck = oldExec, oldCheck })
			spawnExec = func(_ string, _ []string, env []string) error {
				launched = shallowExportEnvironment(env)
				return nil
			}
			shallowCodexDaemonCheck = func(string, bool, string) codexDaemonWarning { return codexDaemonWarning{} }
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := runCmdCaptured(t, "shallow-spawn", "seat", "--no-sync-config", "--", binary); err != nil {
				t.Fatal(err)
			}
			expected := maps.Clone(beforeEnv)
			for _, key := range changes.Unset {
				delete(expected, key)
			}
			for key, value := range changes.Set {
				expected[key] = value
			}
			if !maps.Equal(launched, expected) {
				t.Fatal("exported changes differ from the actual launch environment")
			}
			keys := append([]string{"CAAM_TEST_EXPORT_WORKFLOW"}, changes.Unset...)
			for key := range changes.Set {
				keys = append(keys, key)
			}
			assertShallowExportShell(t, outputs["posix"], launched, keys)
		})
	}
}

func shallowExportEnvironment(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	return values
}

func assertShallowExportShell(t *testing.T, script string, expected map[string]string, keys []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	var assertions strings.Builder
	env := os.Environ()
	for i, key := range keys {
		if value, present := expected[key]; present {
			want := fmt.Sprintf("CAAM_TEST_EXPORT_EXPECT_%d", i)
			env = append(env, want+"="+value)
			fmt.Fprintf(&assertions, "test \"${%s+x}\" = x && test \"$%s\" = \"$%s\" || exit 91\n", key, key, want)
		} else {
			fmt.Fprintf(&assertions, "test \"${%s+x}\" != x || exit 92\n", key)
		}
	}
	// A second shell proves HOME and the other assignments were exported,
	// rather than merely becoming variables in the evaluating shell.
	child := exec.Command(shell, "-c", script+"\nexec \"$CAAM_TEST_EXPORT_SHELL\" -c \"$CAAM_TEST_EXPORT_ASSERTIONS\"")
	child.Env = append(env, "CAAM_TEST_EXPORT_SHELL="+shell, "CAAM_TEST_EXPORT_ASSERTIONS="+assertions.String())
	child.Dir = t.TempDir()
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("evaluating export in sh: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(child.Dir, "caam-export-injected")); !os.IsNotExist(err) {
		t.Fatalf("a profile path executed shell code: %v", err)
	}
}

func TestShallowPrintEnvFailuresAreReadOnlyAndFailClosed(t *testing.T) {
	for _, problem := range []string{"missing", "invalid-state", "shared-settings"} {
		t.Run(problem, func(t *testing.T) {
			if problem == "shared-settings" && runtime.GOOS == "windows" {
				t.Skip("fixture requires Unix symlinks")
			}
			base, realHome := shallowEnv(t)
			if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "seat", "--tool", "claude"); err != nil {
				t.Fatal(err)
			}
			name := "seat"
			switch problem {
			case "missing":
				name = "missing"
			case "invalid-state":
				if err := os.WriteFile(filepath.Join(base, name, ".claude.json"), []byte(`{"oauthToken":"synthetic-export-secret`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "shared-settings":
				host := filepath.Join(realHome, ".claude", "settings.json")
				if err := os.WriteFile(host, []byte(`{"apiKeyHelper":"echo synthetic-export-secret"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(base, name, ".claude", "settings.json")
				if err := os.Rename(path, path+".before-test"); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Symlink(host, path); err != nil {
					t.Fatal(err)
				}
			}
			beforeProfile, beforeHost := snapshotShallowTestTree(t, base), snapshotShallowTestTree(t, realHome)
			for _, mode := range []string{"posix", "json"} {
				args := []string{"shallow-spawn", name, "--print-env", "--create"}
				if mode != "posix" {
					args = append(args, "--"+mode)
				}
				stdout, stderr, err := runCmdCaptured(t, args...)
				if err == nil {
					t.Fatalf("%s accepted unsafe export", mode)
				}
				assertShallowExportFailure(t, mode, stdout)
				if strings.Contains(stdout+stderr+err.Error(), "synthetic-export-secret") {
					t.Fatal("export failure exposed credential contents")
				}
			}
			if !maps.Equal(beforeProfile, snapshotShallowTestTree(t, base)) || !maps.Equal(beforeHost, snapshotShallowTestTree(t, realHome)) {
				t.Fatal("failed environment export created or repaired a profile")
			}
		})
	}
}

func assertShallowExportFailure(t *testing.T, mode, stdout string) {
	t.Helper()
	if mode == "json" {
		if stdout != "" {
			t.Fatal("failed JSON export emitted partial data or shell code")
		}
		return
	}
	if !strings.HasPrefix(stdout, "false ") || strings.Contains(stdout, "export ") {
		t.Fatalf("failed %s export did not emit only a failing shell command: %q", mode, stdout)
	}
}

func TestShallowPrintEnvRejectsInvalidModesBeforeLaunch(t *testing.T) {
	base, _ := shallowEnv(t)
	oldExec := spawnExec
	t.Cleanup(func() { spawnExec = oldExec })
	spawnExec = func(string, []string, []string) error { t.Fatal("invalid export mode launched a process"); return nil }
	for _, flags := range [][]string{{"--json"}} {
		args := append([]string{"shallow-spawn", "missing", "--create"}, flags...)
		if stdout, _, err := runCmdCaptured(t, args...); err == nil || stdout != "" {
			t.Fatalf("invalid export flags did not fail without output: %v, %q", err, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "missing")); !os.IsNotExist(err) {
		t.Fatalf("invalid export flags created a profile: %v", err)
	}
}

func TestShallowPrintEnvReadonlyShellVariablesBlockLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires bash readonly variables")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	shallowEnv(t)
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "seat", "--tool", "claude"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-export-secret")
	script, stderr, err := runCmdCaptured(t, "shallow-spawn", "seat", "--print-env")
	if err != nil || strings.Contains(script+stderr, "synthetic-export-secret") {
		t.Fatalf("export failed or disclosed a secret: %v", err)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "HOME"} {
		t.Run(key, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "provider-launched")
			child := exec.Command(bash, "-c", "readonly "+key+"; eval \"$CAAM_TEST_EXPORT_SCRIPT\" && printf launched > \"$CAAM_TEST_EXPORT_MARKER\"")
			child.Env = append(os.Environ(), "CAAM_TEST_EXPORT_SCRIPT="+script, "CAAM_TEST_EXPORT_MARKER="+marker)
			output, err := child.CombinedOutput()
			if err == nil {
				t.Fatal("readonly identity variable did not make eval fail")
			}
			if strings.Contains(string(output), "synthetic-export-secret") {
				t.Fatal("shell failure exposed an inherited credential")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("provider launched after an incomplete environment change: %v", err)
			}
		})
	}
}

func preserveShallowExportRootGlobals(t *testing.T) {
	t.Helper()
	oldVault, oldStore, oldProject, oldHealth := vault, profileStore, projectStore, healthStore
	oldConfig, oldRunner, oldRegistry := cfg, runner, registry
	t.Cleanup(func() {
		vault, profileStore, projectStore, healthStore = oldVault, oldStore, oldProject, oldHealth
		cfg, runner, registry = oldConfig, oldRunner, oldRegistry
	})
}

func TestShallowPrintEnvPreRunIsReadOnly(t *testing.T) {
	preserveShallowExportRootGlobals(t)
	base, realHome := shallowEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	legacyData := t.TempDir()
	t.Setenv("XDG_DATA_HOME", legacyData)
	legacyPath := filepath.Join(legacyData, "caam", "migration-marker.txt")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("keep in legacy store"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "seat", "--tool", "claude"); err != nil {
		t.Fatal(err)
	}
	roots := []string{base, realHome, os.Getenv("CAAM_HOME"), legacyData}
	before := make([]map[string]string, len(roots))
	for i, path := range roots {
		before[i] = snapshotShallowTestTree(t, path)
	}
	for _, mode := range []string{"posix", "json"} {
		root := newShallowTestRoot()
		root.PersistentPreRunE = rootCmd.PersistentPreRunE
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		args := []string{"shallow-spawn", "seat", "--print-env"}
		if mode != "posix" {
			args = append(args, "--"+mode)
		}
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%s export with real pre-run: %v", mode, err)
		}
		cmd, _, err := root.Find([]string{"shallow-spawn"})
		if err != nil || shouldShowWarnings(cmd) || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("%s export did not bypass active credential warnings: %v", mode, err)
		}
	}
	for i, path := range roots {
		if !maps.Equal(before[i], snapshotShallowTestTree(t, path)) {
			t.Fatalf("export pre-run migrated or mutated fixture tree %d", i)
		}
	}
	if _, err := os.Stat(filepath.Join(config.DefaultDataPath(), "migration-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("export pre-run migrated legacy data: %v", err)
	}
}

func TestShallowPrintEnvPreflightFailureOutput(t *testing.T) {
	preserveShallowExportRootGlobals(t)
	shallowEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := config.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claude_settings":{"mode":"invalid"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []struct {
		name      string
		args      []string
		wantError string
	}{
		{"arguments", []string{"--print-env"}, "requires at least 1 arg"},
		{"config", []string{"seat", "--print-env"}, "load config"},
		{"missing-flag-value", []string{"seat", "--print-env", "--base"}, "flag needs an argument"},
		{"unknown-before-print-env", []string{"seat", "--unknown-export-flag", "--print-env"}, "unknown flag"},
		{"unknown-after-print-env", []string{"seat", "--print-env", "--unknown-export-flag"}, "unknown flag"},
	} {
		for _, mode := range []string{"posix", "json"} {
			t.Run(phase.name+"/"+mode, func(t *testing.T) {
				root := newShallowTestRoot()
				root.PersistentPreRunE = rootCmd.PersistentPreRunE
				var stdout, stderr bytes.Buffer
				root.SetOut(&stdout)
				root.SetErr(&stderr)
				args := []string{"shallow-spawn"}
				// JSON must be parsed before the failing flag: Cobra stops
				// at that flag and cannot know the output mode that follows.
				if mode != "posix" {
					args = append(args, "--"+mode)
				}
				args = append(args, phase.args...)
				root.SetArgs(args)
				if err := root.Execute(); err == nil || !strings.Contains(err.Error(), phase.wantError) {
					t.Fatalf("unexpected preflight failure: %v", err)
				}
				assertShallowExportFailure(t, mode, stdout.String())
			})
		}
	}
}
