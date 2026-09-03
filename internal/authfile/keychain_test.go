package authfile

// Tests for issue #98: on macOS the live Claude OAuth blob is a login-keychain
// item, not a file, so backup captured a token-less profile and activate was a
// silent no-op. The keychain is bridged to ~/.claude/.credentials.json.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// keychainFixture is a Claude file set whose credentials file starts absent,
// as it is on a Mac, backed by a fake keychain.
type keychainFixture struct {
	t         *testing.T
	vault     *Vault
	vaultDir  string
	fileSet   AuthFileSet
	items     string
	credPath  string
	statePath string
}

func newKeychainFixture(t *testing.T) *keychainFixture {
	t.Helper()
	items := testutil.FakeKeychain(t)

	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	// The bridge only applies to the credentials file under the current HOME,
	// so the fixture has to own it.
	t.Setenv("HOME", home)
	f := &keychainFixture{
		t:         t,
		vaultDir:  filepath.Join(tmp, "vault"),
		items:     items,
		credPath:  filepath.Join(home, ".claude", ".credentials.json"),
		statePath: filepath.Join(home, ".claude.json"),
	}
	f.vault = NewVault(f.vaultDir)
	f.fileSet = AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: f.credPath, Required: true},
			{Tool: "claude", Path: f.statePath, Required: false},
		},
		AllowOptionalOnly: true,
	}
	return f
}

func (f *keychainFixture) storeToken(blob string) {
	f.t.Helper()
	testutil.FakeKeychainStore(f.t, f.items, keychain.ClaudeService, keychain.LoginAccount(), blob)
}

func (f *keychainFixture) storedToken() (string, bool) {
	f.t.Helper()
	return testutil.FakeKeychainRead(f.t, f.items, keychain.ClaudeService)
}

func keychainCreds(access string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"rt-` + access + `","expiresAt":1893456000000}}`
}

func keychainState(email string) string {
	raw, err := json.Marshal(map[string]any{
		"numStartups":  3,
		"oauthAccount": map[string]any{"emailAddress": email, "accountUuid": "acct-" + email},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// TestBackupCapturesKeychainToken is the headline of #98: with the token only
// in the keychain, the vault profile must still get a .credentials.json.
func TestBackupCapturesKeychainToken(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-live"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))

	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	saved := filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")
	if got := readFixtureFile(t, saved); got != keychainCreds("at-live") {
		t.Fatalf("vault credentials = %q, want the keychain payload", got)
	}
	// The mirror is left in place at 0600 so hashing and expiry keep working.
	info, err := os.Stat(f.credPath)
	if err != nil {
		t.Fatalf("stat mirror: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mirror mode = %o, want 600", perm)
	}
	// meta.json records the account identity, so `caam ls` is not "unknown".
	meta := readFixtureFile(t, filepath.Join(f.vaultDir, "claude", "alice", "meta.json"))
	if !strings.Contains(meta, "alice@example.com") {
		t.Fatalf("meta.json carries no identity: %s", meta)
	}
}

// TestBackupFailsWhenKeychainRefuses: a token-less profile is worse than a
// failed backup, so a locked keychain must be an error, not a silent success.
func TestBackupFailsWhenKeychainRefuses(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-live"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")

	err := f.vault.Backup(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Backup succeeded against a locked keychain")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("Backup error does not name the keychain: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")); statErr == nil {
		t.Fatal("Backup wrote a profile despite the keychain failure")
	}
}

// TestRestorePushesTokenToKeychain: activate only changes the account once the
// snapshot is back in the keychain.
func TestRestorePushesTokenToKeychain(t *testing.T) {
	f := newKeychainFixture(t)

	// Two accounts backed up while each was live.
	f.storeToken(keychainCreds("at-alice"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup alice: %v", err)
	}

	f.storeToken(keychainCreds("at-bob"))
	writeFixtureFile(t, f.statePath, keychainState("bob@example.com"))
	if err := f.vault.Backup(f.fileSet, "bob"); err != nil {
		t.Fatalf("Backup bob: %v", err)
	}

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore alice: %v", err)
	}
	stored, ok := f.storedToken()
	if !ok {
		t.Fatal("Restore left no keychain item")
	}
	if stored != keychainCreds("at-alice") {
		t.Fatalf("keychain holds %q after activating alice", stored)
	}
	if got := readFixtureFile(t, f.credPath); got != keychainCreds("at-alice") {
		t.Fatalf("mirror holds %q after activating alice", got)
	}
	if name := f.active(); name != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice", name)
	}
}

// TestRestoreFailsWhenKeychainRefuses: reporting a successful switch while the
// keychain still holds the previous account is exactly the bug.
func TestRestoreFailsWhenKeychainRefuses(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-alice"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")
	err := f.vault.Restore(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Restore reported success against a locked keychain")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("Restore error does not name the keychain: %v", err)
	}
}

// TestHasAuthFilesSeesKeychainOnlyLogin keeps callers from routing a logged-in
// Mac down the login path.
func TestHasAuthFilesSeesKeychainOnlyLogin(t *testing.T) {
	f := newKeychainFixture(t)

	if HasAuthFiles(f.fileSet) {
		t.Fatal("HasAuthFiles reported a login with nothing present")
	}
	f.storeToken(keychainCreds("at-live"))
	if !HasAuthFiles(f.fileSet) {
		t.Fatal("HasAuthFiles missed a keychain-only login")
	}
}

// TestClearAuthFilesRemovesKeychainItem: removing the mirror is not a logout
// while the keychain still holds the token Claude Code prefers.
func TestClearAuthFilesRemovesKeychainItem(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-live"))
	writeFixtureFile(t, f.credPath, keychainCreds("at-live"))

	if err := ClearAuthFiles(f.fileSet); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	if _, ok := f.storedToken(); ok {
		t.Fatal("ClearAuthFiles left the keychain item behind")
	}
	if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
		t.Fatal("ClearAuthFiles left the mirror behind")
	}
}

// TestBridgeIsInertWhenDisabled covers the Linux/shallow-profile path: with no
// login keychain, everything falls back to files exactly as before.
func TestBridgeIsInertWhenDisabled(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-keychain"))
	t.Setenv("CAAM_KEYCHAIN", "0")

	writeFixtureFile(t, f.credPath, keychainCreds("at-file"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	saved := filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")
	if got := readFixtureFile(t, saved); got != keychainCreds("at-file") {
		t.Fatalf("vault credentials = %q, want the on-disk file", got)
	}
	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if stored, _ := f.storedToken(); stored != keychainCreds("at-keychain") {
		t.Fatalf("disabled bridge wrote the keychain: %q", stored)
	}
}

func (f *keychainFixture) active() string {
	f.t.Helper()
	name, err := f.vault.ActiveProfile(f.fileSet)
	if err != nil {
		f.t.Fatalf("ActiveProfile: %v", err)
	}
	return name
}
