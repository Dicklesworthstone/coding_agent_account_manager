package shallow

import (
	"os"
	"path/filepath"
	"testing"
)

// resolvesUnderRealHome reports whether path exists and (following any symlink)
// resolves to a location inside realHome — i.e. it is a live passthrough back to
// the user's real HOME. A withheld/absent entry returns false.
func resolvesUnderRealHome(t *testing.T, path, realHome string) bool {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		return false // absent == unreachable, which is the fail-closed outcome
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	realResolved, err := filepath.EvalSymlinks(realHome)
	if err != nil {
		realResolved = filepath.Clean(realHome)
	}
	rel, err := filepath.Rel(realResolved, resolved)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepathHasDotDotPrefix(rel))
}

func filepathHasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator
}

// TestCrossProviderAuthWithheld is the issue #40 regression test: a shallow
// profile for one provider must NOT symlink any OTHER provider's real auth root
// back into the "isolated" profile. With a real HOME signed into all three
// providers, a claude profile must withhold .codex and .gemini (and symmetric),
// while still passing through benign non-auth entries like .bashrc.
func TestCrossProviderAuthWithheld(t *testing.T) {
	home := multiHome(t)

	type providerRoots struct {
		provider string
		others   []string // the OTHER providers' real auth roots that must be withheld
	}
	cases := []providerRoots{
		{"claude", []string{".codex", ".gemini"}},
		{"codex", []string{".claude", ".gemini"}},
		{"agy", []string{".claude", ".codex"}},
	}

	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
			if err != nil {
				t.Fatal(err)
			}
			src := credSource(t, "SHALLOW-"+c.provider)
			got, err := mgr.Create("p", CreateOptions{Provider: c.provider, CredentialSource: src})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			// Each OTHER provider's auth root must be unreachable: not present as
			// a symlink resolving back to the real HOME's auth dir.
			for _, other := range c.others {
				p := filepath.Join(got, other)
				if resolvesUnderRealHome(t, p, home) {
					target, _ := os.Readlink(p)
					t.Fatalf("%s profile leaks other provider auth: %s resolves under real HOME (target=%q)", c.provider, p, target)
				}
			}

			// The intended passthrough still works: a benign non-auth dotfile is
			// symlinked back to the real HOME.
			bashrc := filepath.Join(got, ".bashrc")
			if !resolvesUnderRealHome(t, bashrc, home) {
				t.Fatalf("%s profile broke benign passthrough: %s is not a live symlink to real HOME", c.provider, bashrc)
			}
		})
	}
}

// vaultHome builds a real HOME whose caam vault (default location) holds several
// accounts' credentials, plus benign siblings under ~/.local that MUST still
// pass through (a per-app data dir and ~/.local/bin).
func vaultHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mustWrite := func(rel, body string) {
		full := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mustWrite(".bashrc", "# bashrc\n")
	mustWrite(".claude/.credentials.json", `{"real":"claude"}`)

	// The master vault: EVERY account's tokens.
	mustWrite(".local/share/caam/vault/claude/alice/.credentials.json", "ALICE-CLAUDE-TOKEN")
	mustWrite(".local/share/caam/vault/codex/bob/auth.json", "BOB-CODEX-TOKEN")

	// Benign siblings under ~/.local that must survive the carve-out.
	mustWrite(".local/bin/mytool", "#!/bin/sh\n")
	mustWrite(".local/share/opencode/config.json", "{}")
	return home
}

// TestVaultUnreachableFromProfile is the issue #41 regression test: with the
// default vault under $HOME/.local/share/caam/vault and stored accounts, a
// created profile must NOT expose the caam data subtree — while benign siblings
// under ~/.local (bin, other apps' data) still pass through.
func TestVaultUnreachableFromProfile(t *testing.T) {
	home := vaultHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	src := credSource(t, "SHALLOW-CLAUDE")
	got, err := mgr.Create("alice", CreateOptions{Provider: "claude", CredentialSource: src})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The caam data dir must be unreachable through the profile HOME.
	caamDir := filepath.Join(got, ".local", "share", "caam")
	if resolvesUnderRealHome(t, caamDir, home) {
		t.Fatalf("SECURITY: caam vault dir reachable through profile: %s resolves under real HOME", caamDir)
	}
	// The actual token files must not be readable through the profile.
	for _, leak := range []string{
		filepath.Join(got, ".local", "share", "caam", "vault", "claude", "alice", ".credentials.json"),
		filepath.Join(got, ".local", "share", "caam", "vault", "codex", "bob", "auth.json"),
	} {
		if body, err := os.ReadFile(leak); err == nil {
			t.Fatalf("SECURITY: read another account's token through profile at %s: %q", leak, body)
		}
	}

	// The carve-out must PRESERVE passthrough of benign ~/.local siblings, so
	// the fix narrows only the caam subtree (not the whole .local tree).
	binTool := filepath.Join(got, ".local", "bin", "mytool")
	if !resolvesUnderRealHome(t, binTool, home) {
		t.Fatalf("carve-out over-withheld: %s should still pass through to real HOME", binTool)
	}
	if body, err := os.ReadFile(binTool); err != nil || string(body) != "#!/bin/sh\n" {
		t.Fatalf("~/.local/bin passthrough broken: body=%q err=%v", body, err)
	}
	opencode := filepath.Join(got, ".local", "share", "opencode", "config.json")
	if !resolvesUnderRealHome(t, opencode, home) {
		t.Fatalf("carve-out over-withheld: %s should still pass through to real HOME", opencode)
	}
}

// TestVaultUnreachableViaXDGRelocation covers the issue #41 variant where the
// vault is relocated under $HOME via XDG_DATA_HOME: the caam subtree there must
// also be withheld.
func TestVaultUnreachableViaXDGRelocation(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdgdata")
	t.Setenv("XDG_DATA_HOME", xdg)

	mustWrite := func(rel, body string) {
		full := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mustWrite(".bashrc", "# bashrc\n")
	mustWrite(".claude/.credentials.json", `{"real":"claude"}`)
	mustWrite("xdgdata/caam/vault/codex/bob/auth.json", "BOB-CODEX-TOKEN")
	mustWrite("xdgdata/otherapp/state", "keepme")

	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Create("p", CreateOptions{Provider: "claude", CredentialSource: credSource(t, "x")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	caamDir := filepath.Join(got, "xdgdata", "caam")
	if resolvesUnderRealHome(t, caamDir, home) {
		t.Fatalf("SECURITY: XDG-relocated caam vault reachable through profile: %s", caamDir)
	}
	leak := filepath.Join(got, "xdgdata", "caam", "vault", "codex", "bob", "auth.json")
	if body, err := os.ReadFile(leak); err == nil {
		t.Fatalf("SECURITY: read token through XDG-relocated vault: %q", body)
	}
	// A non-caam sibling under XDG_DATA_HOME still passes through.
	other := filepath.Join(got, "xdgdata", "otherapp", "state")
	if !resolvesUnderRealHome(t, other, home) {
		t.Fatalf("carve-out over-withheld XDG sibling: %s should pass through", other)
	}
}
