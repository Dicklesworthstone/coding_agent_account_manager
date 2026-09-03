package authfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// claudeKeychainHome prepares a temp HOME with a deterministic Claude file set
// and a fake keychain, the shape of a Mac where the CLI has logged in but left
// nothing on disk.
func claudeKeychainHome(t *testing.T) (home string, fs AuthFileSet) {
	t.Helper()
	testutil.FakeKeychain(t)

	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".config", "claude-code"))
	return home, ClaudeAuthFiles()
}

func seedKeychainLogin(t *testing.T, home, token, email string) string {
	t.Helper()
	blob := `{"claudeAiOauth":{"accessToken":"` + token + `","refreshToken":"r-` + token + `"}}`
	testutil.SetKeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount(), blob)

	settings := `{"oauthAccount":{"accountUuid":"uuid-` + token + `","emailAddress":"` + email + `"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(settings), 0600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
	return blob
}

// TestHasAuthFilesSeesKeychainOnlyLogin covers the macOS case where the only
// evidence of a login is the keychain item: caam used to report "logged out"
// and refuse to back the account up.
func TestHasAuthFilesSeesKeychainOnlyLogin(t *testing.T) {
	home, fs := claudeKeychainHome(t)

	if HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles = true before any login")
	}

	seedKeychainLogin(t, home, "A", "alice@example.com")

	if !HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles = false with credentials in the keychain")
	}
}

// TestBackupCapturesKeychainCredentials proves the snapshot is no longer empty
// on a Mac: the vault profile gets a real .credentials.json.
func TestBackupCapturesKeychainCredentials(t *testing.T) {
	home, fs := claudeKeychainHome(t)
	blob := seedKeychainLogin(t, home, "A", "alice@example.com")

	vault := NewVault(filepath.Join(t.TempDir(), "vault"))
	if err := vault.Backup(fs, "alice"); err != nil {
		t.Fatalf("backup: %v", err)
	}

	snapshot := filepath.Join(vault.ProfilePath("claude", "alice"), ".credentials.json")
	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("vault snapshot missing credentials: %v", err)
	}
	if string(data) != blob {
		t.Fatalf("snapshot = %q, want %q", data, blob)
	}
}

// TestRestorePushesCredentialsIntoKeychain is the switch itself: restoring a
// profile has to update the keychain, because that is where Claude Code reads
// from. Restoring the file alone leaves the previous account signed in.
func TestRestorePushesCredentialsIntoKeychain(t *testing.T) {
	home, fs := claudeKeychainHome(t)
	vault := NewVault(filepath.Join(t.TempDir(), "vault"))

	aliceBlob := seedKeychainLogin(t, home, "A", "alice@example.com")
	if err := vault.Backup(fs, "alice"); err != nil {
		t.Fatalf("backup alice: %v", err)
	}

	bobBlob := seedKeychainLogin(t, home, "B", "bob@example.com")
	if err := vault.Backup(fs, "bob"); err != nil {
		t.Fatalf("backup bob: %v", err)
	}

	if got, _ := testutil.KeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount()); got != bobBlob {
		t.Fatalf("precondition: keychain = %q, want bob", got)
	}

	if err := vault.Restore(fs, "alice"); err != nil {
		t.Fatalf("restore alice: %v", err)
	}

	got, ok := testutil.KeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount())
	if !ok || got != aliceBlob {
		t.Fatalf("keychain after restore = %q (ok=%v), want alice's credentials", got, ok)
	}
	live, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || string(live) != aliceBlob {
		t.Fatalf("live credentials file = %q (err=%v), want alice's credentials", live, err)
	}
}

// TestActiveProfileMatchesKeychainAccount checks identity detection, which
// hashes the credentials file and so saw nothing to compare on a Mac.
func TestActiveProfileMatchesKeychainAccount(t *testing.T) {
	home, fs := claudeKeychainHome(t)
	vault := NewVault(filepath.Join(t.TempDir(), "vault"))

	seedKeychainLogin(t, home, "A", "alice@example.com")
	if err := vault.Backup(fs, "alice"); err != nil {
		t.Fatalf("backup alice: %v", err)
	}
	seedKeychainLogin(t, home, "B", "bob@example.com")
	if err := vault.Backup(fs, "bob"); err != nil {
		t.Fatalf("backup bob: %v", err)
	}

	active, err := vault.ActiveProfile(fs)
	if err != nil {
		t.Fatalf("active profile: %v", err)
	}
	if active != "bob" {
		t.Fatalf("ActiveProfile = %q, want bob", active)
	}
}

// TestClearAuthFilesRemovesKeychainItem covers logout: leaving the item behind
// would let the CLI sign straight back in as the account just cleared.
func TestClearAuthFilesRemovesKeychainItem(t *testing.T) {
	home, fs := claudeKeychainHome(t)
	seedKeychainLogin(t, home, "A", "alice@example.com")

	if !HasAuthFiles(fs) {
		t.Fatal("precondition: expected a login")
	}
	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := testutil.KeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount()); ok {
		t.Fatal("keychain item survived logout")
	}
	if HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles = true after logout")
	}
}
