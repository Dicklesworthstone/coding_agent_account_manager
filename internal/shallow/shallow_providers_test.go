package shallow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// multiHome lays down a real HOME that already contains identity-bearing files
// for claude, codex, AND agy, plus some non-auth state under each provider dir.
// This lets the security tests prove that a shallow profile for one provider
// never re-shares that provider's REAL identity (its dir must be real/private,
// not a symlink back here), while still passing non-auth state through.
func multiHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()

	mustWrite := func(rel, body string) {
		full := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// Shared dotfiles (symlink passthroughs).
	mustWrite(".bashrc", "# bashrc\n")
	mustWrite(".gitconfig", "# git\n")

	// Real claude identity + history.
	mustWrite(".claude/.credentials.json", `{"real":"claude-identity"}`)
	mustWrite(".claude/projects/marker", "claude-projects")
	mustWrite(".claude.json", `{"real":"claude-json"}`)

	// Real codex identity + non-auth session history.
	mustWrite(".codex/auth.json", `{"real":"codex-identity"}`)
	mustWrite(".codex/sessions/marker", "codex-sessions")

	// Real agy/gemini identity + non-auth state.
	mustWrite(".gemini/antigravity-cli/antigravity-oauth-token", "REAL-AGY-TOKEN")
	mustWrite(".gemini/google_accounts.json", `{"real":"google"}`)
	mustWrite(".gemini/history/marker", "gemini-history")
	mustWrite(".gemini/antigravity-cli/cache/marker", "agy-cache")

	return home
}

func mustNotSymlink(t *testing.T, path, what string) os.FileInfo {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s (%s): %v", path, what, err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s MUST be real (not a symlink): %s", what, path)
	}
	return st
}

func mustSymlink(t *testing.T, path, what string) string {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s (%s): %v", path, what, err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s should be a symlink: %s", what, path)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	return target
}

func TestLayoutFor(t *testing.T) {
	if _, err := layoutFor("nope"); err == nil {
		t.Fatalf("expected error for unknown provider")
	}
	for _, p := range []string{"", "claude", "CODEX", " agy "} {
		if _, err := layoutFor(p); err != nil {
			t.Fatalf("layoutFor(%q) unexpected error: %v", p, err)
		}
	}
	l, _ := layoutFor("")
	if l.provider != "claude" {
		t.Fatalf("empty provider should map to claude, got %q", l.provider)
	}
}

func TestCreateCodexProfile(t *testing.T) {
	home := multiHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	src := credSource(t, `{"codex":"alice"}`)
	got, err := mgr.Create("codex-alice", CreateOptions{Provider: "codex", CredentialSource: src, CredentialFromLabel: "vault:codex/alice"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// .codex is a REAL directory, not a symlink to the real ~/.codex.
	st := mustNotSymlink(t, filepath.Join(got, ".codex"), ".codex dir")
	if !st.IsDir() {
		t.Fatalf(".codex should be a directory")
	}

	// auth.json is a real 0600 file with OUR contents (not the real identity).
	authDst := filepath.Join(got, ".codex", "auth.json")
	st = mustNotSymlink(t, authDst, ".codex/auth.json")
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json perm %v, want 0600", st.Mode().Perm())
	}
	if body, _ := os.ReadFile(authDst); string(body) != `{"codex":"alice"}` {
		t.Fatalf("auth.json contents = %q, want our source (NOT the real identity)", body)
	}

	// config.toml enforces file-based credential store.
	cfg, err := os.ReadFile(filepath.Join(got, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	if !strings.Contains(string(cfg), `cli_auth_credentials_store = "file"`) {
		t.Fatalf("config.toml missing file-store enforcement: %q", cfg)
	}

	// Non-auth codex state (sessions) passes through as a symlink.
	target := mustSymlink(t, filepath.Join(got, ".codex", "sessions"), ".codex/sessions")
	if target != filepath.Join(home, ".codex", "sessions") {
		t.Fatalf(".codex/sessions target %q", target)
	}
	if body, _ := os.ReadFile(filepath.Join(got, ".codex", "sessions", "marker")); string(body) != "codex-sessions" {
		t.Fatalf("sessions passthrough broken")
	}

	// Metadata records the provider; CredentialPath resolves to the codex auth.
	p, err := mgr.Get("codex-alice")
	if err != nil || p.Meta == nil || p.Meta.Provider != "codex" {
		t.Fatalf("meta provider not codex: %+v (err=%v)", p, err)
	}
	if cp, _ := mgr.CredentialPath("codex-alice"); cp != authDst {
		t.Fatalf("CredentialPath = %q, want %q", cp, authDst)
	}
}

// TestCreateCodexProfilePreservesMCPConfig is the #46 property: a shallow codex
// profile must carry the user's real ~/.codex/config.toml (including
// [mcp_servers.*] sections) into the shallow HOME, while still forcing
// file-based credential storage — instead of writing a stub that only contains
// the credential-store line and silently drops MCP config.
func TestCreateCodexProfilePreservesMCPConfig(t *testing.T) {
	home := multiHome(t)

	// Give the real HOME a codex config with an MCP section AND a non-file
	// credential store, so we prove BOTH preservation and enforcement.
	realConfig := "cli_auth_credentials_store = \"keychain\"\n\n" +
		"[mcp_servers.tavily]\n" +
		"command = \"npx\"\n" +
		"args = [\"-y\", \"tavily-mcp\"]\n\n" +
		"[mcp_servers.filesystem]\n" +
		"command = \"mcp-fs\"\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(realConfig), 0o600); err != nil {
		t.Fatalf("write real codex config: %v", err)
	}

	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Create("codex-mcp", CreateOptions{Provider: "codex", CredentialSource: credSource(t, `{"codex":"x"}`), CredentialFromLabel: "vault:codex/x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The shallow config.toml is a REAL file (never a symlink back to the real
	// config — a spawned session rewrites it), and it carries the MCP sections.
	shallowCfgPath := filepath.Join(got, ".codex", "config.toml")
	mustNotSymlink(t, shallowCfgPath, ".codex/config.toml")
	cfg, err := os.ReadFile(shallowCfgPath)
	if err != nil {
		t.Fatalf("read shallow config.toml: %v", err)
	}
	body := string(cfg)
	for _, want := range []string{"[mcp_servers.tavily]", "[mcp_servers.filesystem]", `command = "npx"`, `command = "mcp-fs"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("shallow config.toml dropped user section %q:\n%s", want, body)
		}
	}
	// Credential storage must be forced to file, replacing the keychain setting.
	if !strings.Contains(body, `cli_auth_credentials_store = "file"`) {
		t.Fatalf("shallow config.toml did not enforce file credential store:\n%s", body)
	}
	if strings.Contains(body, `"keychain"`) {
		t.Fatalf("shallow config.toml kept the keychain credential store:\n%s", body)
	}

	// Editing the shallow config must NOT mutate the user's real config.
	if err := os.WriteFile(shallowCfgPath, []byte("mutated = true\n"), 0o600); err != nil {
		t.Fatalf("rewrite shallow config: %v", err)
	}
	realAfter, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if string(realAfter) != realConfig {
		t.Fatalf("real ~/.codex/config.toml was mutated through the shallow profile:\n%s", realAfter)
	}
}

func TestCreateAgyProfile(t *testing.T) {
	home := multiHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	token := credSource(t, "ALICE-AGY-TOKEN")
	googleSrc := credSource(t, `{"acct":"alice@example.com"}`)
	got, err := mgr.Create("agy-alice", CreateOptions{
		Provider:         "agy",
		CredentialSource: token,
		ExtraSources: map[string]string{
			".gemini/google_accounts.json": googleSrc,
		},
		CredentialFromLabel: "vault:agy/alice",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// .gemini and .gemini/antigravity-cli are REAL directories.
	mustNotSymlink(t, filepath.Join(got, ".gemini"), ".gemini dir")
	mustNotSymlink(t, filepath.Join(got, ".gemini", "antigravity-cli"), ".gemini/antigravity-cli dir")

	// Required token: real 0600 file with OUR token (not the real one).
	tokDst := filepath.Join(got, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	st := mustNotSymlink(t, tokDst, "antigravity-oauth-token")
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("token perm %v, want 0600", st.Mode().Perm())
	}
	if body, _ := os.ReadFile(tokDst); string(body) != "ALICE-AGY-TOKEN" {
		t.Fatalf("token = %q, want our source", body)
	}

	// Optional google_accounts.json copied as a real file (not a symlink).
	gaDst := filepath.Join(got, ".gemini", "google_accounts.json")
	mustNotSymlink(t, gaDst, "google_accounts.json")
	if body, _ := os.ReadFile(gaDst); string(body) != `{"acct":"alice@example.com"}` {
		t.Fatalf("google_accounts.json = %q", body)
	}

	// Non-auth gemini history passes through as a symlink.
	if target := mustSymlink(t, filepath.Join(got, ".gemini", "history"), ".gemini/history"); target != filepath.Join(home, ".gemini", "history") {
		t.Fatalf(".gemini/history target %q", target)
	}
	// Non-auth content under the antigravity-cli real dir passes through too.
	if target := mustSymlink(t, filepath.Join(got, ".gemini", "antigravity-cli", "cache"), "antigravity-cli/cache"); target != filepath.Join(home, ".gemini", "antigravity-cli", "cache") {
		t.Fatalf("antigravity-cli/cache target %q", target)
	}

	p, _ := mgr.Get("agy-alice")
	if p.Meta == nil || p.Meta.Provider != "agy" {
		t.Fatalf("meta provider not agy: %+v", p)
	}
	if cp, _ := mgr.CredentialPath("agy-alice"); cp != tokDst {
		t.Fatalf("CredentialPath = %q, want %q", cp, tokDst)
	}
}

// TestNoIdentityLeak is the core security test: a shallow profile must NEVER
// expose the user's real provider identity. The provider's identity directory
// must be a real, private directory (not a symlink to the real HOME), and its
// auth files must be real copies — so the real on-disk identity is never read
// through the shallow profile, and mutating the shallow copy never touches the
// real one.
func TestNoIdentityLeak(t *testing.T) {
	home := multiHome(t)

	type tc struct {
		provider string
		idDir    string // dir that must be real, not symlinked to real HOME
		authRel  string // primary auth file (real, copied)
		realAuth string // the REAL identity file under home
		realBody string
	}
	cases := []tc{
		{"codex", ".codex", ".codex/auth.json", ".codex/auth.json", `{"real":"codex-identity"}`},
		{"agy", ".gemini", ".gemini/antigravity-cli/antigravity-oauth-token", ".gemini/antigravity-cli/antigravity-oauth-token", "REAL-AGY-TOKEN"},
	}

	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
			if err != nil {
				t.Fatal(err)
			}
			src := credSource(t, "SHALLOW-IDENTITY-"+c.provider)
			got, err := mgr.Create("p", CreateOptions{Provider: c.provider, CredentialSource: src})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			// The identity dir must not be a symlink at all (so it can never
			// point back at the real HOME's identity dir).
			st := mustNotSymlink(t, filepath.Join(got, c.idDir), c.idDir)
			if !st.IsDir() {
				t.Fatalf("%s should be a directory", c.idDir)
			}

			// The auth file must be a real file holding OUR identity, never the
			// real one.
			authDst := filepath.Join(got, filepath.FromSlash(c.authRel))
			mustNotSymlink(t, authDst, c.authRel)
			if body, _ := os.ReadFile(authDst); string(body) != "SHALLOW-IDENTITY-"+c.provider {
				t.Fatalf("auth leaked: %q != our shallow identity", body)
			}

			// Mutating the shallow auth must not touch the real identity file.
			if err := os.WriteFile(authDst, []byte("ROTATED"), 0o600); err != nil {
				t.Fatal(err)
			}
			realAuthPath := filepath.Join(home, filepath.FromSlash(c.realAuth))
			if body, _ := os.ReadFile(realAuthPath); string(body) != c.realBody {
				t.Fatalf("real %s drifted to %q (expected %q) — identity leak!", c.realAuth, body, c.realBody)
			}
		})
	}
}

// TestCrossProfileIsolation proves two profiles of the same non-claude provider
// have fully independent auth files.
func TestCrossProfileIsolation(t *testing.T) {
	home := multiHome(t)

	for _, c := range []struct {
		provider string
		authRel  string
	}{
		{"codex", ".codex/auth.json"},
		{"agy", ".gemini/antigravity-cli/antigravity-oauth-token"},
	} {
		t.Run(c.provider, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
			if err != nil {
				t.Fatal(err)
			}
			a := credSource(t, "A")
			b := credSource(t, "B")
			if _, err := mgr.Create("a", CreateOptions{Provider: c.provider, CredentialSource: a}); err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Create("b", CreateOptions{Provider: c.provider, CredentialSource: b}); err != nil {
				t.Fatal(err)
			}
			aAuth := filepath.Join(mgr.BaseDir(), "a", filepath.FromSlash(c.authRel))
			bAuth := filepath.Join(mgr.BaseDir(), "b", filepath.FromSlash(c.authRel))
			if err := os.WriteFile(aAuth, []byte("A-ROTATED"), 0o600); err != nil {
				t.Fatal(err)
			}
			if body, _ := os.ReadFile(bAuth); string(body) != "B" {
				t.Fatalf("profile b auth drifted to %q — not isolated", body)
			}
		})
	}
}

func TestSpawnEnvPerProvider(t *testing.T) {
	home := "/orch/p"

	hasScrub := func(scrub []string, want string) bool {
		for _, s := range scrub {
			if s == want {
				return true
			}
		}
		return false
	}

	claudeSet, claudeScrub := SpawnEnv("claude", home, "p")
	if claudeSet["HOME"] != home || claudeSet["SHALLOW_PROFILE"] != "p" {
		t.Fatalf("claude base env wrong: %v", claudeSet)
	}
	if _, ok := claudeSet["CODEX_HOME"]; ok {
		t.Fatalf("claude must not set CODEX_HOME")
	}
	if !hasScrub(claudeScrub, "CLAUDE_CONFIG_DIR") {
		t.Fatalf("claude must scrub CLAUDE_CONFIG_DIR, got %v", claudeScrub)
	}

	// Every provider must scrub caam's own vault-locating vars so a spawned caam
	// process cannot resolve the real vault via the inherited environment (#41).
	for _, provider := range []string{"claude", "codex", "agy"} {
		_, scrub := SpawnEnv(provider, home, "p")
		if !hasScrub(scrub, "CAAM_HOME") || !hasScrub(scrub, "XDG_DATA_HOME") {
			t.Fatalf("%s must scrub CAAM_HOME and XDG_DATA_HOME, got %v", provider, scrub)
		}
	}

	codexSet, _ := SpawnEnv("codex", home, "p")
	if codexSet["CODEX_HOME"] != filepath.Join(home, ".codex") {
		t.Fatalf("codex CODEX_HOME = %q", codexSet["CODEX_HOME"])
	}

	agySet, _ := SpawnEnv("agy", home, "p")
	if agySet["GEMINI_HOME"] != filepath.Join(home, ".gemini") {
		t.Fatalf("agy GEMINI_HOME = %q", agySet["GEMINI_HOME"])
	}
}

// TestExtraSourcesRejectsUnmanaged proves the hard guard against writing outside
// the managed real-file set (a path-escape / clobber defense).
func TestExtraSourcesRejectsUnmanaged(t *testing.T) {
	home := multiHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	token := credSource(t, "tok")
	evil := credSource(t, "evil")
	_, err = mgr.Create("agy-bad", CreateOptions{
		Provider:         "agy",
		CredentialSource: token,
		ExtraSources:     map[string]string{"../escape.json": evil},
	})
	if err == nil {
		t.Fatalf("expected error writing an unmanaged extra source")
	}
	if !strings.Contains(err.Error(), "unmanaged shallow file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestProviderProfilesHaveNoBrokenSymlinks scans codex and agy shallow homes.
func TestProviderProfilesHaveNoBrokenSymlinks(t *testing.T) {
	home := multiHome(t)
	for _, provider := range []string{"codex", "agy"} {
		t.Run(provider, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
			if err != nil {
				t.Fatal(err)
			}
			src := credSource(t, "x")
			root, err := mgr.Create("p", CreateOptions{Provider: provider, CredentialSource: src})
			if err != nil {
				t.Fatal(err)
			}
			err = filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				info, err := os.Lstat(path)
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink == 0 {
					return nil
				}
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(path), target)
				}
				if _, err := os.Stat(target); err != nil {
					t.Fatalf("broken symlink %s -> %s: %v", path, target, err)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
		})
	}
}
