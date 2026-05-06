package shallow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeHome lays down a realistic-looking real HOME inside t.TempDir() and
// returns its path. It includes a representative slice of dotfiles (real
// files), dot-directories, and the .claude/ structure (with conversation
// history that the shallow profile must continue to share).
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()

	// Top-level real files (will become symlinks in shallow profiles).
	for _, f := range []string{".bashrc", ".zshrc", ".gitconfig"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("# "+f+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}
	// Top-level real dirs (will become symlinks in shallow profiles).
	for _, d := range []string{".ssh", ".config", ".cargo", ".bun"} {
		full := filepath.Join(home, d)
		if err := os.MkdirAll(full, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		if err := os.WriteFile(filepath.Join(full, "marker"), []byte(d), 0o600); err != nil {
			t.Fatalf("seed marker in %s: %v", d, err)
		}
	}
	// .claude/ structure with the auth files plus conversation history.
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	for _, sub := range []string{"projects", "todos", "shell-snapshots"} {
		full := filepath.Join(claudeDir, sub)
		if err := os.MkdirAll(full, 0o700); err != nil {
			t.Fatalf("mkdir .claude/%s: %v", sub, err)
		}
		if err := os.WriteFile(filepath.Join(full, "marker"), []byte(sub), 0o600); err != nil {
			t.Fatalf("seed marker in .claude/%s: %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatalf("seed .claude.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"placeholder":true}`), 0o600); err != nil {
		t.Fatalf("seed .credentials.json: %v", err)
	}
	return home
}

// credSource creates a temp file containing valid-looking credential JSON
// and returns its path.
func credSource(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write cred source: %v", err)
	}
	return path
}

func TestDefaultBaseDirPrecedence(t *testing.T) {
	t.Setenv("CAAM_SHALLOW_HOMES_DIR", "/explicit/override")
	t.Setenv("CAAM_HOME", "/some/caam")
	if got := DefaultBaseDir("/home/u"); got != "/explicit/override" {
		t.Fatalf("override should win, got %q", got)
	}

	t.Setenv("CAAM_SHALLOW_HOMES_DIR", "")
	if got := DefaultBaseDir("/home/u"); got != "/some/caam/shallow-homes" {
		t.Fatalf("CAAM_HOME path expected, got %q", got)
	}

	t.Setenv("CAAM_HOME", "")
	if got := DefaultBaseDir("/home/u"); got != "/home/u/orch-homes" {
		t.Fatalf("default expected, got %q", got)
	}
}

func TestNewManagerRejectsBaseDirEqualToHome(t *testing.T) {
	home := t.TempDir()
	_, err := NewManager(home, home)
	if err == nil {
		t.Fatalf("expected error when baseDir == realHome")
	}
}

func TestCreateBuildsSymlinkFarmAndRealAuthFiles(t *testing.T) {
	home := fakeHome(t)
	base := filepath.Join(t.TempDir(), "orch-homes")
	mgr, err := NewManager(base, home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	credPath := credSource(t, `{"alice":true}`)
	got, err := mgr.Create("alice", CreateOptions{
		CredentialSource:    credPath,
		CredentialFromLabel: "vault:claude/alice",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got != filepath.Join(base, "alice") {
		t.Fatalf("unexpected home path %q", got)
	}

	// .claude is a real directory.
	st, err := os.Lstat(filepath.Join(got, ".claude"))
	if err != nil {
		t.Fatalf("stat .claude: %v", err)
	}
	if !st.IsDir() || (st.Mode()&os.ModeSymlink) != 0 {
		t.Fatalf(".claude must be a real directory, got mode %v", st.Mode())
	}

	// .credentials.json is a real file with our copied contents.
	credDst := filepath.Join(got, ".claude", ".credentials.json")
	credSt, err := os.Lstat(credDst)
	if err != nil {
		t.Fatalf("stat creds: %v", err)
	}
	if (credSt.Mode() & os.ModeSymlink) != 0 {
		t.Fatalf(".credentials.json must be a real file, not a symlink")
	}
	body, err := os.ReadFile(credDst)
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	if string(body) != `{"alice":true}` {
		t.Fatalf("creds contents wrong: %q", body)
	}
	// Permissions tight (0600).
	if credSt.Mode().Perm() != 0o600 {
		t.Fatalf("creds perm not 0600: %v", credSt.Mode().Perm())
	}

	// .credentials.lock is a real (empty) file.
	lockSt, err := os.Lstat(filepath.Join(got, ".claude", ".credentials.lock"))
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if (lockSt.Mode() & os.ModeSymlink) != 0 {
		t.Fatalf(".credentials.lock must be a real file")
	}

	// .claude.json is a real file (seeded from real HOME).
	claudeJSONSt, err := os.Lstat(filepath.Join(got, ".claude.json"))
	if err != nil {
		t.Fatalf("stat .claude.json: %v", err)
	}
	if (claudeJSONSt.Mode() & os.ModeSymlink) != 0 {
		t.Fatalf(".claude.json must be a real file")
	}

	// Top-level dotfiles must be symlinks pointing back to real HOME.
	for _, name := range []string{".bashrc", ".zshrc", ".gitconfig", ".ssh", ".config", ".cargo", ".bun"} {
		dst := filepath.Join(got, name)
		linkInfo, err := os.Lstat(dst)
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if (linkInfo.Mode() & os.ModeSymlink) == 0 {
			t.Fatalf("%s should be a symlink, mode=%v", name, linkInfo.Mode())
		}
		target, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("readlink %s: %v", name, err)
		}
		want := filepath.Join(home, name)
		if target != want {
			t.Fatalf("%s symlink target %q != %q", name, target, want)
		}
	}

	// Inner .claude entries (other than .credentials*) must be symlinks
	// pointing at the corresponding entries under real ~/.claude.
	for _, name := range []string{"projects", "todos", "shell-snapshots"} {
		dst := filepath.Join(got, ".claude", name)
		st, err := os.Lstat(dst)
		if err != nil {
			t.Fatalf("stat .claude/%s: %v", name, err)
		}
		if (st.Mode() & os.ModeSymlink) == 0 {
			t.Fatalf(".claude/%s should be a symlink", name)
		}
		target, err := os.Readlink(dst)
		if err != nil {
			t.Fatalf("readlink .claude/%s: %v", name, err)
		}
		if target != filepath.Join(home, ".claude", name) {
			t.Fatalf(".claude/%s target wrong: %q", name, target)
		}
	}

	// Conversation-history passthrough: reading via shallow HOME yields the
	// same contents as reading via real HOME.
	for _, sub := range []string{"projects", "todos", "shell-snapshots"} {
		body, err := os.ReadFile(filepath.Join(got, ".claude", sub, "marker"))
		if err != nil {
			t.Fatalf("read passthrough .claude/%s/marker: %v", sub, err)
		}
		if string(body) != sub {
			t.Fatalf("passthrough mismatch for .claude/%s/marker: %q", sub, body)
		}
	}

	// Metadata sidecar exists and round-trips.
	metaPath := filepath.Join(got, ProfileMetaFilename)
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	if meta.Name != "alice" {
		t.Fatalf("meta name %q", meta.Name)
	}
	if meta.CredentialFrom != "vault:claude/alice" {
		t.Fatalf("meta credential_from %q", meta.CredentialFrom)
	}
	if meta.RealHome != home {
		t.Fatalf("meta real home %q != %q", meta.RealHome, home)
	}
	if meta.CreatedAt.IsZero() {
		t.Fatalf("meta created_at is zero")
	}
}

func TestCreateSmartFallbackForMissingSources(t *testing.T) {
	// Real HOME has only .bashrc; everything else is missing.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("# shell\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Create("bob", CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// .bashrc is a symlink.
	if st, err := os.Lstat(filepath.Join(got, ".bashrc")); err != nil || (st.Mode()&os.ModeSymlink) == 0 {
		t.Fatalf(".bashrc should be a symlink (err=%v, mode=%v)", err, st.Mode())
	}
	// No symlink for .ssh because it doesn't exist in real HOME.
	if _, err := os.Lstat(filepath.Join(got, ".ssh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".ssh should NOT exist (smart fallback), err=%v", err)
	}
	// .credentials.json was created empty (no credential source given).
	if body, err := os.ReadFile(filepath.Join(got, ".claude", ".credentials.json")); err != nil {
		t.Fatalf("read creds: %v", err)
	} else if len(body) != 0 {
		t.Fatalf("expected empty creds, got %q", body)
	}
	// .claude.json got the skeleton because real HOME has no .claude.json.
	if body, err := os.ReadFile(filepath.Join(got, ".claude.json")); err != nil {
		t.Fatalf("read claude.json: %v", err)
	} else if !strings.HasPrefix(string(body), "{") {
		t.Fatalf("expected JSON skeleton, got %q", body)
	}
}

func TestCreateRefusesDuplicateWithoutForce(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{}); err == nil {
		t.Fatalf("expected error on duplicate create without --force")
	}
	if _, err := mgr.Create("alice", CreateOptions{Force: true}); err != nil {
		t.Fatalf("force overwrite: %v", err)
	}
}

func TestCreateRejectsBadName(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "..", ".", "../oops", "with/slash", "white space", "weird$char"} {
		if _, err := mgr.Create(name, CreateOptions{}); err == nil {
			t.Errorf("expected error for invalid name %q", name)
		}
	}
}

func TestListReturnsSortedProfiles(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"charlie", "alice", "bob"} {
		if _, err := mgr.Create(name, CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 profiles, got %d", len(got))
	}
	names := make([]string, len(got))
	for i, p := range got {
		names[i] = p.Name
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("profiles not sorted: %v", names)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Name < got[j].Name }) {
		t.Fatalf("profile slice not sorted")
	}
	for _, p := range got {
		if p.Meta == nil {
			t.Errorf("profile %q missing meta", p.Name)
		}
	}
}

func TestListEmptyAndMissingBaseDir(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "doesnotexist"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 profiles, got %d", len(got))
	}
}

func TestDeleteRemovesProfile(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete("alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := mgr.Get("alice"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
	// Real HOME is intact (delete must never traverse symlinks).
	for _, f := range []string{".bashrc", ".gitconfig", ".claude/projects/marker"} {
		if _, err := os.Stat(filepath.Join(home, f)); err != nil {
			t.Fatalf("real HOME entry %q vanished: %v", f, err)
		}
	}
}

func TestDeleteUnknownProfile(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete("nope"); err == nil {
		t.Fatalf("expected error deleting unknown profile")
	}
}

// TestCredentialIsolation ensures that mutating one shallow profile's
// .credentials.json does NOT affect another profile or the real HOME.
func TestCredentialIsolation(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	src := credSource(t, `{"identity":"original"}`)
	for _, name := range []string{"alice", "bob"} {
		if _, err := mgr.Create(name, CreateOptions{CredentialSource: src}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	aliceCred, _ := mgr.CredentialPath("alice")
	bobCred, _ := mgr.CredentialPath("bob")
	realCred := filepath.Join(home, ".claude", ".credentials.json")

	// Mutate alice's credentials only.
	if err := os.WriteFile(aliceCred, []byte(`{"identity":"alice-rotated"}`), 0o600); err != nil {
		t.Fatalf("write alice creds: %v", err)
	}
	// Bob and real HOME must be unchanged.
	if body, err := os.ReadFile(bobCred); err != nil || string(body) != `{"identity":"original"}` {
		t.Fatalf("bob creds drifted: body=%q err=%v", body, err)
	}
	if body, err := os.ReadFile(realCred); err != nil || string(body) != `{"placeholder":true}` {
		t.Fatalf("real HOME creds drifted: body=%q err=%v", body, err)
	}
}

// TestCredentialsLockIsIndependent verifies that two shallow profiles each
// get their OWN .credentials.lock (not a symlink to the user's real lock),
// so two concurrent Claude sessions can each flock their own lock without
// blocking each other.
func TestCredentialsLockIsIndependent(t *testing.T) {
	home := fakeHome(t)
	// Pre-create a real ~/.claude/.credentials.lock to verify we don't symlink to it.
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.lock"), []byte("real-lock"), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := mgr.Create(name, CreateOptions{}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	aliceLock := filepath.Join(mgr.BaseDir(), "alice", ".claude", ".credentials.lock")
	bobLock := filepath.Join(mgr.BaseDir(), "bob", ".claude", ".credentials.lock")

	for _, p := range []string{aliceLock, bobLock} {
		st, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if (st.Mode() & os.ModeSymlink) != 0 {
			t.Fatalf("%s must NOT be a symlink", p)
		}
	}

	// Writing to one lock must not affect the other.
	if err := os.WriteFile(aliceLock, []byte("alice"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(bobLock); err != nil {
		t.Fatal(err)
	} else if string(body) == "alice" {
		t.Fatalf("bob lock contains alice contents: locks not independent")
	}
}

// TestSkipShallowBaseDirNestedInRealHome covers the case where the shallow
// base dir is itself under realHome (e.g. ~/orch-homes when realHome is ~).
// Without this guard, populateSymlinks would symlink ~/orch-homes back into
// the shallow profile, creating a nasty recursion.
func TestSkipShallowBaseDirNestedInRealHome(t *testing.T) {
	home := fakeHome(t)
	base := filepath.Join(home, "orch-homes")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(base, home)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Create("alice", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The shallow HOME must NOT contain a symlink named "orch-homes".
	if _, err := os.Lstat(filepath.Join(got, "orch-homes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shallow HOME should not contain orch-homes: err=%v", err)
	}
}

// TestPreservesExistingRealDir ensures that when a real directory already
// exists in the shallow HOME (e.g. .claude), populateSymlinks does NOT
// replace it with a symlink. This is what protects the per-profile
// credentials directory.
func TestPreservesExistingRealDir(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(filepath.Join(mgr.BaseDir(), "alice", ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	if (st.Mode()&os.ModeSymlink) != 0 || !st.IsDir() {
		t.Fatalf(".claude must remain a real directory, got mode=%v", st.Mode())
	}
}

// TestCustomClaudeJSONSource verifies the SourceClaudeJSON option.
func TestCustomClaudeJSONSource(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	src := credSource(t, `{"customSetting":42}`)
	if _, err := mgr.Create("alice", CreateOptions{SourceClaudeJSON: src}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(mgr.BaseDir(), "alice", ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"customSetting":42}` {
		t.Fatalf(".claude.json mismatch: %q", got)
	}
}

// TestGetReturnsMeta verifies Get loads metadata.
func TestGetReturnsMeta(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{CredentialFromLabel: "vault:claude/alice"}); err != nil {
		t.Fatal(err)
	}
	p, err := mgr.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	if p.Meta == nil {
		t.Fatalf("expected meta, got nil")
	}
	if p.Meta.CredentialFrom != "vault:claude/alice" {
		t.Fatalf("meta CredentialFrom %q", p.Meta.CredentialFrom)
	}
}

// TestCreateProducesNoBrokenSymlinks scans the shallow HOME and asserts
// every symlink resolves to an existing path.
func TestCreateProducesNoBrokenSymlinks(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(mgr.BaseDir(), "alice")
	err = filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if (info.Mode() & os.ModeSymlink) == 0 {
			return nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		// Resolve relative targets against the symlink's dir.
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		if _, err := os.Stat(target); err != nil {
			return fmt.Errorf("broken symlink %s -> %s: %w", path, target, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
