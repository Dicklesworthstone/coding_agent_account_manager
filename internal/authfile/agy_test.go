package authfile

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// NOTE: every fixture in this file is SYNTHETIC. No test reads, writes, or
// copies the real ~/.gemini credentials. "Tokens" here are non-secret literals.

const (
	agyFakeToken    = `{"auth_method":"oauth","token":"SYNTHETIC-AGY-TOKEN-NOT-REAL"}`
	agyFakeAccounts = `{"active":"work@example.com","old":["old@example.com"]}`
	agyFakeCreds    = `{"access_token":"synthetic","refresh_token":"synthetic"}`
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// agyFixtureHome lays down a synthetic agy auth tree under GEMINI_HOME and
// returns the file set + the token/accounts/creds paths.
func agyFixtureHome(t *testing.T) (AuthFileSet, string, string, string) {
	t.Helper()
	home := t.TempDir()
	gemHome := filepath.Join(home, ".gemini")
	t.Setenv("GEMINI_HOME", gemHome)

	antigravityDir := filepath.Join(gemHome, "antigravity-cli")
	if err := os.MkdirAll(antigravityDir, 0700); err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(antigravityDir, "antigravity-oauth-token")
	accountsPath := filepath.Join(gemHome, "google_accounts.json")
	credsPath := filepath.Join(gemHome, "oauth_creds.json")

	if err := os.WriteFile(tokenPath, []byte(agyFakeToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accountsPath, []byte(agyFakeAccounts), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credsPath, []byte(agyFakeCreds), 0600); err != nil {
		t.Fatal(err)
	}

	return AntigravityAuthFiles(), tokenPath, accountsPath, credsPath
}

func TestAntigravityAuthFiles_Shape(t *testing.T) {
	t.Setenv("GEMINI_HOME", "/g/.gemini")
	fs := AntigravityAuthFiles()
	if fs.Tool != "agy" {
		t.Errorf("Tool = %q, want agy", fs.Tool)
	}
	if len(fs.Files) == 0 || !fs.Files[0].Required {
		t.Fatal("first file (token) must be required")
	}
	if filepath.Base(fs.Files[0].Path) != "antigravity-oauth-token" {
		t.Errorf("first file = %q, want antigravity-oauth-token", filepath.Base(fs.Files[0].Path))
	}
	// No basename collisions in the vault.
	seen := map[string]bool{}
	for _, f := range fs.Files {
		b := filepath.Base(f.Path)
		if seen[b] {
			t.Errorf("duplicate vault basename %q", b)
		}
		seen[b] = true
	}
}

func TestGetAuthFileSet_AgyAlias(t *testing.T) {
	for _, name := range []string{"agy", "antigravity", "AGY"} {
		fs, ok := GetAuthFileSet(name)
		if !ok {
			t.Errorf("GetAuthFileSet(%q) not found", name)
			continue
		}
		if fs.Tool != "agy" {
			t.Errorf("GetAuthFileSet(%q).Tool = %q, want agy", name, fs.Tool)
		}
	}
}

// TestAgy_BackupRestoreRoundTrip is the core 14.2 round-trip: backup a synthetic
// agy auth tree to a vault, clear it, restore it, and assert every file is
// byte-identical. It compares SHA-256 hashes only; it never prints contents.
func TestAgy_BackupRestoreRoundTrip(t *testing.T) {
	fs, tokenPath, accountsPath, credsPath := agyFixtureHome(t)
	vaultDir := t.TempDir()
	v := NewVault(vaultDir)

	// Capture original hashes (no content printed).
	origHashes := map[string]string{}
	for _, p := range []string{tokenPath, accountsPath, credsPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		origHashes[filepath.Base(p)] = sha256Hex(data)
	}

	// Backup.
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	// The vault should contain the token (required) and optional files.
	if _, err := os.Stat(v.BackupPath("agy", "work", "antigravity-oauth-token")); err != nil {
		t.Fatalf("token not backed up: %v", err)
	}

	// Clear current auth (simulate logout / account switch away).
	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("ClearAuthFiles() error = %v", err)
	}
	if HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles should be false after clear")
	}

	// Restore.
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	// Every restored file must be byte-identical (hash match).
	for _, p := range []string{tokenPath, accountsPath, credsPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("restored read %s: %v", p, err)
		}
		if got := sha256Hex(data); got != origHashes[filepath.Base(p)] {
			t.Errorf("restored %s is NOT byte-identical (hash mismatch)", filepath.Base(p))
		}
		// Restored files must be 0600.
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0600 {
			t.Errorf("restored %s perms = %o, want 0600", filepath.Base(p), info.Mode().Perm())
		}
	}

	if !HasAuthFiles(fs) {
		t.Error("HasAuthFiles should be true after restore")
	}
}

// TestAgy_BackupRequiresToken proves the token file is genuinely required: a
// backup with only the optional files present must fail.
func TestAgy_BackupRequiresToken(t *testing.T) {
	home := t.TempDir()
	gemHome := filepath.Join(home, ".gemini")
	t.Setenv("GEMINI_HOME", gemHome)
	if err := os.MkdirAll(gemHome, 0700); err != nil {
		t.Fatal(err)
	}
	// Only the optional accounts file exists; no token.
	if err := os.WriteFile(filepath.Join(gemHome, "google_accounts.json"), []byte(agyFakeAccounts), 0600); err != nil {
		t.Fatal(err)
	}

	v := NewVault(t.TempDir())
	if err := v.Backup(AntigravityAuthFiles(), "broken"); err == nil {
		t.Error("Backup() should fail without the required token file")
	}
}

// TestAgy_ActiveProfile verifies account switching detection: after backing up
// two distinct accounts, restoring one makes ActiveProfile report that profile.
func TestAgy_ActiveProfile(t *testing.T) {
	fs, tokenPath, accountsPath, _ := agyFixtureHome(t)
	v := NewVault(t.TempDir())

	// Profile "work" = the current fixture.
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup(work) error = %v", err)
	}

	// Mutate to a different account ("personal") and back that up too.
	personalToken := `{"auth_method":"oauth","token":"SYNTHETIC-PERSONAL-TOKEN"}`
	personalAccounts := `{"active":"me@personal.example","old":[]}`
	if err := os.WriteFile(tokenPath, []byte(personalToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accountsPath, []byte(personalAccounts), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.Backup(fs, "personal"); err != nil {
		t.Fatalf("Backup(personal) error = %v", err)
	}

	// Switch back to "work" and confirm detection.
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore(work) error = %v", err)
	}
	active, err := v.ActiveProfile(fs)
	if err != nil {
		t.Fatalf("ActiveProfile() error = %v", err)
	}
	if active != "work" {
		t.Errorf("ActiveProfile() = %q, want work", active)
	}

	// Switch to "personal" and confirm.
	if err := v.Restore(fs, "personal"); err != nil {
		t.Fatalf("Restore(personal) error = %v", err)
	}
	active, _ = v.ActiveProfile(fs)
	if active != "personal" {
		t.Errorf("ActiveProfile() = %q, want personal", active)
	}
}

// TestAgy_ProfileIdentity verifies account-identification parsing from a vault
// profile (the active Google email from google_accounts.json).
func TestAgy_ProfileIdentity(t *testing.T) {
	fs, _, _, _ := agyFixtureHome(t)
	v := NewVault(t.TempDir())
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if got := v.ProfileIdentity("agy", "work"); got != "work@example.com" {
		t.Errorf("ProfileIdentity() = %q, want work@example.com", got)
	}
}

var _ = agyFakeCreds
