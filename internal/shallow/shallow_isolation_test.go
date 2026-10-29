package shallow

import (
	"os"
	"path/filepath"
	"runtime"
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

// Issue #130: a macOS Claude shallow profile must never expose the real
// login Keychain through the Library symlink farm.
func TestClaudeLibraryKeychainBoundary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS shallow layout")
	}
	home := t.TempDir()
	for _, p := range []string{"Library/Keychains/login.keychain-db", "Library/Application Support/app-state"} {
		full := filepath.Join(home, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("host-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"empty", "vault"} {
		t.Run(source, func(t *testing.T) {
			opts := CreateOptions{Provider: "claude"}
			if source == "vault" {
				opts.CredentialSource = credSource(t, "vault-only")
			}
			path, err := mgr.Create(source, opts)
			if err != nil {
				t.Fatal(err)
			}
			if resolvesUnderRealHome(t, filepath.Join(path, "Library", "Keychains"), home) {
				t.Fatal("host Keychain leaked into shallow Claude profile")
			}
			if _, err := os.ReadFile(filepath.Join(path, "Library", "Keychains", "login.keychain-db")); err == nil {
				t.Fatal("host login credential readable through shallow HOME")
			}
			if !resolvesUnderRealHome(t, filepath.Join(path, "Library", "Application Support"), home) {
				t.Fatal("noncredential Library resource no longer shared")
			}
			if err := mgr.RepairClaudeKeychainBoundary(source); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyClaudeLibraryMigrationDoesNotTouchHostKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS shallow layout")
	}
	home := t.TempDir()
	hostKeys := filepath.Join(home, "Library", "Keychains")
	if err := os.MkdirAll(hostKeys, 0o700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(hostKeys, "login.keychain-db")
	if err := os.WriteFile(original, []byte("immutable-host-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	path, err := mgr.Create("legacy", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(path, "Library")
	if err := os.Remove(lib); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "Library"), lib); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RepairClaudeKeychainBoundary("legacy"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(lib); err != nil || !info.IsDir() {
		t.Fatalf("Library not a private directory: %v %v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(lib, "Keychains")); !os.IsNotExist(err) {
		t.Fatalf("Keychains entry should be absent: %v", err)
	}
	if secret, err := os.ReadFile(original); err != nil || string(secret) != "immutable-host-secret" {
		t.Fatalf("host Keychain modified: %q %v", secret, err)
	}
	if err := os.Symlink(hostKeys, filepath.Join(lib, "Keychains")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RepairClaudeKeychainBoundary("legacy"); err == nil {
		t.Fatal("must refuse profile with manually reintroduced Keychain link")
	}
}

func claudeLibrarySourceFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "Library", "Keychains", "login.keychain-db"), "host-secret")
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	return mgr, filepath.Join(mgr.BaseDir(), "p")
}

func TestLibraryLayoutIsScopedToMacOSClaude(t *testing.T) {
	for _, provider := range SupportedProviders() {
		t.Run(provider, func(t *testing.T) {
			mgr, _ := claudeLibrarySourceFixture(t)
			profile, err := mgr.Create("p", CreateOptions{Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			library := filepath.Join(profile, "Library")
			info, err := os.Lstat(library)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "darwin" && provider == "claude" {
				if !info.IsDir() {
					t.Fatal("macOS Claude Library is not a private directory")
				}
				if _, err := os.Lstat(filepath.Join(library, "Keychains")); !os.IsNotExist(err) {
					t.Fatalf("macOS Claude Library exposes Keychains: %v", err)
				}
				return
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("Library passthrough changed for another provider or platform")
			}
			if got := readFile(t, filepath.Join(library, "Keychains", "login.keychain-db")); got != "host-secret" {
				t.Fatalf("Library passthrough no longer resolves to its existing source: %q", got)
			}
		})
	}
}

// These tests exercise the explicit private-Library filesystem policy on any
// OS supporting symlinks. They do not claim native macOS Keychain coverage.
func TestClaudeLibraryMirroringRejectsNewKeychainAliases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		chain  bool
	}{
		{"directory", "Keychains", false},
		{"descendant", "Keychains/login.keychain-db", false},
		{"chain", "Bridge", true},
		{"dangling", "missing", false},
		{"cycle", "Alias", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, profile := claudeLibrarySourceFixture(t)
			lib := filepath.Join(profile, "Library")
			if err := os.MkdirAll(lib, 0o700); err != nil {
				t.Fatal(err)
			}
			// Select a private Library explicitly instead of changing GOOS.
			layout := &providerLayout{provider: "claude", realEntries: []string{"Library/Keychains"}}
			if err := mgr.populateInnerSymlinks(profile, "Library", layout); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(mgr.RealHome(), "Library")
			if tc.chain {
				if err := os.Symlink("Keychains", filepath.Join(source, "Bridge")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.FromSlash(tc.target), filepath.Join(source, "Alias")); err != nil {
				t.Fatal(err)
			}
			// Reject the first backfill after the source changes, before the
			// new link can be admitted by a successful spawn preparation.
			if err := mgr.populateInnerSymlinks(profile, "Library", layout); err == nil {
				t.Error("accepted an unsafe new Library source")
			}
			if _, err := os.Lstat(filepath.Join(lib, "Alias")); !os.IsNotExist(err) {
				t.Errorf("unsafe source was linked into the private Library: %v", err)
			}
			if got := readFile(t, filepath.Join(source, "Keychains", "login.keychain-db")); got != "host-secret" {
				t.Fatalf("host Keychain changed: %q", got)
			}
		})
	}
}

func TestClaudeLibraryMirroringPreservesResources(t *testing.T) {
	t.Run("benign siblings", func(t *testing.T) {
		mgr, profile := claudeLibrarySourceFixture(t)
		for _, rel := range []string{"Preferences/app.plist", "KeychainsBackup/state"} {
			writeFile(t, filepath.Join(mgr.RealHome(), "Library", filepath.FromSlash(rel)), "shared")
		}
		if err := os.MkdirAll(filepath.Join(profile, "Library"), 0o700); err != nil {
			t.Fatal(err)
		}
		layout := &providerLayout{provider: "claude", realEntries: []string{"Library/Keychains"}}
		if err := mgr.populateInnerSymlinks(profile, "Library", layout); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{"Preferences/app.plist", "KeychainsBackup/state"} {
			if got := readFile(t, filepath.Join(profile, "Library", filepath.FromSlash(rel))); got != "shared" {
				t.Fatalf("benign Library resource changed: %q", got)
			}
		}
	})
	for _, directory := range []bool{false, true} {
		name := "local file"
		if directory {
			name = "local directory"
		}
		t.Run(name, func(t *testing.T) {
			mgr, profile := claudeLibrarySourceFixture(t)
			dest := filepath.Join(profile, "Library", "Alias")
			local := dest
			if directory {
				local = filepath.Join(dest, "state")
			}
			writeFile(t, local, "private")
			if err := os.Symlink("Keychains", filepath.Join(mgr.RealHome(), "Library", "Alias")); err != nil {
				t.Fatal(err)
			}
			layout := &providerLayout{provider: "claude", realEntries: []string{"Library/Keychains"}}
			if err := mgr.populateInnerSymlinks(profile, "Library", layout); err != nil {
				t.Fatalf("host alias shadowed by a private entry should not be admitted or inspected: %v", err)
			}
			if info, err := os.Lstat(dest); err != nil || info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("private entry replaced: %v %v", info, err)
			}
			if got := readFile(t, local); got != "private" {
				t.Fatalf("private resource changed: %q", got)
			}
		})
	}
	t.Run("missing host Library", func(t *testing.T) {
		mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		profile := filepath.Join(mgr.BaseDir(), "p")
		local := filepath.Join(profile, "Library", "local-state")
		writeFile(t, local, "private")
		if err := mgr.populateInnerSymlinks(profile, "Library", &providerLayout{provider: "claude"}); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, local); got != "private" {
			t.Fatalf("private resource changed: %q", got)
		}
	})
}

func TestClaudeLibraryCreateRejectsHostKeychainAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS shallow layout")
	}
	for _, force := range []bool{false, true} {
		name := "new profile"
		if force {
			name = "forced replacement"
		}
		t.Run(name, func(t *testing.T) {
			mgr, profile := claudeLibrarySourceFixture(t)
			var originalMeta string
			if force {
				if _, err := mgr.Create("p", CreateOptions{Provider: "claude", CredentialSource: credSource(t, "original")}); err != nil {
					t.Fatal(err)
				}
				originalMeta = readFile(t, filepath.Join(profile, ProfileMetaFilename))
			}
			if err := os.Symlink("Keychains", filepath.Join(mgr.RealHome(), "Library", "Alias")); err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Create("p", CreateOptions{Provider: "claude", Force: force, CredentialSource: credSource(t, "replacement")}); err == nil {
				t.Fatal("published a profile containing a host Keychain alias")
			}
			if force {
				if got := readFile(t, filepath.Join(profile, ".claude", ".credentials.json")); got != "original" {
					t.Fatalf("failed replacement changed the original credentials: %q", got)
				}
				if got := readFile(t, filepath.Join(profile, ProfileMetaFilename)); got != originalMeta {
					t.Fatal("failed replacement changed the original metadata")
				}
			} else if _, err := os.Lstat(profile); !os.IsNotExist(err) {
				t.Fatalf("failed creation published a profile: %v", err)
			}
			if got := readFile(t, filepath.Join(mgr.RealHome(), "Library", "Keychains", "login.keychain-db")); got != "host-secret" {
				t.Fatalf("host Keychain changed: %q", got)
			}
		})
	}
}
