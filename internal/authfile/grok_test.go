package authfile

import (
	"os"
	"path/filepath"
	"testing"
)

// NOTE: every fixture in this file is SYNTHETIC. No test reads, writes, or
// copies the real ~/.grok credentials. "Tokens" here are non-secret literals.
//
// The synthetic auth.json mirrors the observed on-disk shape of the official
// Grok Build CLI (dynamic "<issuer>::<client-id>" top-level key), with fake
// values throughout.

const (
	grokFakeAuth   = `{"https://auth.x.ai::00000000-0000-0000-0000-000000000000":{"key":"SYNTHETIC-GROK-TOKEN-NOT-REAL","auth_mode":"sso","email":"grok-tester@example.com","user_id":"synthetic-user-1","refresh_token":"SYNTHETIC-REFRESH-NOT-REAL","expires_at":"2099-01-01T00:00:00Z"}}`
	grokFakeConfig = "[cli]\ninstaller = \"internal\"\nauto_update = true\n"
)

// grokFixtureHome lays down a synthetic Grok auth tree under GROK_HOME and
// returns the file set + the auth.json/config.toml paths.
func grokFixtureHome(t *testing.T) (AuthFileSet, string, string) {
	t.Helper()
	home := t.TempDir()
	grokHome := filepath.Join(home, ".grok")
	t.Setenv("GROK_HOME", grokHome)

	if err := os.MkdirAll(grokHome, 0700); err != nil {
		t.Fatal(err)
	}

	authPath := filepath.Join(grokHome, "auth.json")
	configPath := filepath.Join(grokHome, "config.toml")

	if err := os.WriteFile(authPath, []byte(grokFakeAuth), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(grokFakeConfig), 0600); err != nil {
		t.Fatal(err)
	}

	return GrokAuthFiles(), authPath, configPath
}

func TestGrokAuthFiles_Shape(t *testing.T) {
	t.Setenv("GROK_HOME", "/g/.grok")
	fs := GrokAuthFiles()
	if fs.Tool != "grok" {
		t.Errorf("Tool = %q, want grok", fs.Tool)
	}
	if len(fs.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(fs.Files))
	}
	if filepath.Base(fs.Files[0].Path) != "auth.json" || !fs.Files[0].Required {
		t.Errorf("first file = %q (required=%v), want required auth.json",
			filepath.Base(fs.Files[0].Path), fs.Files[0].Required)
	}
	if filepath.Base(fs.Files[1].Path) != "config.toml" || fs.Files[1].Required {
		t.Errorf("second file = %q (required=%v), want optional config.toml",
			filepath.Base(fs.Files[1].Path), fs.Files[1].Required)
	}
	if fs.AllowOptionalOnly {
		t.Error("AllowOptionalOnly should be false: a snapshot without auth.json is meaningless")
	}
	for _, f := range fs.Files {
		if filepath.Dir(f.Path) != "/g/.grok" {
			t.Errorf("file %q not under GROK_HOME override", f.Path)
		}
	}
}

func TestGrokAuthFiles_DefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", "")
	fs := GrokAuthFiles()
	want := filepath.Join(home, ".grok", "auth.json")
	if fs.Files[0].Path != want {
		t.Errorf("auth path = %q, want %q", fs.Files[0].Path, want)
	}
}

func TestGetAuthFileSet_GrokAlias(t *testing.T) {
	for _, name := range []string{"grok", "grok-build", "GROK"} {
		fs, ok := GetAuthFileSet(name)
		if !ok {
			t.Errorf("GetAuthFileSet(%q) not found", name)
			continue
		}
		if fs.Tool != "grok" {
			t.Errorf("GetAuthFileSet(%q).Tool = %q, want grok", name, fs.Tool)
		}
	}
}

func TestGrok_BackupRestoreRoundTrip(t *testing.T) {
	fs, authPath, configPath := grokFixtureHome(t)
	vault := NewVault(t.TempDir())

	if err := vault.Backup(fs, "tester"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Simulate switching away: clear the live files.
	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatal("auth.json should be removed after clear")
	}

	if err := vault.Restore(fs, "tester"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	gotAuth, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read restored auth.json: %v", err)
	}
	if string(gotAuth) != grokFakeAuth {
		t.Error("restored auth.json does not match original")
	}
	gotConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config.toml: %v", err)
	}
	if string(gotConfig) != grokFakeConfig {
		t.Error("restored config.toml does not match original")
	}
}

func TestGrok_BackupRequiresAuthJSON(t *testing.T) {
	home := t.TempDir()
	grokHome := filepath.Join(home, ".grok")
	t.Setenv("GROK_HOME", grokHome)
	if err := os.MkdirAll(grokHome, 0700); err != nil {
		t.Fatal(err)
	}
	// Only the optional config.toml exists.
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), []byte(grokFakeConfig), 0600); err != nil {
		t.Fatal(err)
	}

	vault := NewVault(t.TempDir())
	if err := vault.Backup(GrokAuthFiles(), "tester"); err == nil {
		t.Fatal("Backup should fail when the required auth.json is missing")
	}
}

func TestGrok_ActiveProfile(t *testing.T) {
	fs, authPath, _ := grokFixtureHome(t)
	vault := NewVault(t.TempDir())

	if err := vault.Backup(fs, "tester"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	active, err := vault.ActiveProfile(fs)
	if err != nil {
		t.Fatalf("ActiveProfile: %v", err)
	}
	if active != "tester" {
		t.Errorf("ActiveProfile = %q, want tester", active)
	}

	// A different credential must not match.
	other := `{"https://auth.x.ai::11111111-1111-1111-1111-111111111111":{"key":"OTHER-SYNTHETIC","email":"other@example.com"}}`
	if err := os.WriteFile(authPath, []byte(other), 0600); err != nil {
		t.Fatal(err)
	}
	active, err = vault.ActiveProfile(fs)
	if err != nil {
		t.Fatalf("ActiveProfile: %v", err)
	}
	if active != "" {
		t.Errorf("ActiveProfile = %q, want \"\" for unknown credential", active)
	}
}

// TestGrok_CommunityCLIFilesUntouched pins the ~/.grok collision behavior: the
// unaffiliated community grok-cli stores grok.db / user-settings.json in the
// same directory, and caam must never back up, restore over, or clear them.
func TestGrok_CommunityCLIFilesUntouched(t *testing.T) {
	fs, _, _ := grokFixtureHome(t)
	grokHome := os.Getenv("GROK_HOME")

	dbPath := filepath.Join(grokHome, "grok.db")
	settingsPath := filepath.Join(grokHome, "user-settings.json")
	if err := os.WriteFile(dbPath, []byte("community-db"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"apiKey":"synthetic"}`), 0600); err != nil {
		t.Fatal(err)
	}

	vault := NewVault(t.TempDir())
	if err := vault.Backup(fs, "tester"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// The vault snapshot must contain only official Grok Build files (+ meta).
	entries, err := os.ReadDir(vault.ProfilePath("grok", "tester"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "auth.json", "config.toml", "meta.json":
		default:
			t.Errorf("unexpected file in grok vault snapshot: %s", e.Name())
		}
	}

	// Clearing auth (logout) must leave the community CLI's files alone.
	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("grok.db should be untouched: %v", err)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Errorf("user-settings.json should be untouched: %v", err)
	}
}
