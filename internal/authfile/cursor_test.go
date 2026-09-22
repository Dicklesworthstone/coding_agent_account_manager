package authfile

import (
	"os"
	"path/filepath"
	"testing"
)

// Synthetic Cursor credentials. No real token or account id.

func TestCursorAuthFiles_UsesXDGAndDoesNotTreatMetadataAsLogin(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	fs := CursorAuthFiles()
	if fs.Tool != "cursor" {
		t.Fatalf("tool = %q", fs.Tool)
	}
	var sawXDGAuth, sawLegacy bool
	for _, f := range fs.Files {
		switch f.Name {
		case "xdg-auth.json":
			sawXDGAuth = true
			if f.Path != filepath.Join(xdg, "cursor", "auth.json") {
				t.Fatalf("xdg auth path = %q", f.Path)
			}
		case "auth.json":
			sawLegacy = true
			if f.Path != filepath.Join(home, ".cursor", "auth.json") {
				t.Fatalf("legacy auth path = %q", f.Path)
			}
		}
	}
	if !sawXDGAuth || !sawLegacy {
		t.Fatalf("missing xdg or legacy spec: %+v", fs.Files)
	}

	// authInfo without a token is not a login.
	if err := os.MkdirAll(filepath.Join(xdg, "cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	info := []byte(`{"authInfo":{"email":"meta@example.com"},"version":1}`)
	if err := os.WriteFile(filepath.Join(xdg, "cursor", "cli-config.json"), info, 0600); err != nil {
		t.Fatal(err)
	}
	if HasAuthFiles(CursorAuthFiles()) {
		t.Fatal("cli-config authInfo counted as a credential")
	}

	auth := []byte(`{"accessToken":"synthetic-cursor-token","refreshToken":"synthetic-cursor-refresh"}`)
	if err := os.WriteFile(filepath.Join(xdg, "cursor", "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	if !HasAuthFiles(CursorAuthFiles()) {
		t.Fatal("XDG auth.json with an access token was not detected")
	}
}

func TestCursorActiveProfileIgnoresDriftedSettings(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	auth := []byte(`{"accessToken":"synthetic-cursor-token","refreshToken":"synthetic-cursor-refresh"}`)
	if err := os.WriteFile(filepath.Join(xdg, "cursor", "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	// Settings that will drift after the backup. They must not hide the login.
	if err := os.WriteFile(filepath.Join(home, ".cursor", "cli-config.json"), []byte(`{"version":1,"model":"a"}`), 0600); err != nil {
		t.Fatal(err)
	}

	vault := NewVault(t.TempDir())
	fs := CursorAuthFiles()
	if err := vault.Backup(fs, "personal"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cursor", "cli-config.json"), []byte(`{"version":1,"model":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	active, err := vault.ActiveProfile(CursorAuthFiles())
	if err != nil {
		t.Fatal(err)
	}
	if active != "personal" {
		t.Fatalf("active = %q, want personal", active)
	}
}

func TestCursorBackupRestoreKeepsXDGAndLegacyApart(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	if err := os.MkdirAll(filepath.Join(xdg, "cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0700); err != nil {
		t.Fatal(err)
	}
	xdgAuth := []byte(`{"accessToken":"synthetic-xdg-token","refreshToken":"synthetic-xdg-refresh","label":"xdg"}`)
	legacyAuth := []byte(`{"accessToken":"synthetic-legacy-token","refreshToken":"synthetic-legacy-refresh","label":"legacy"}`)
	if err := os.WriteFile(filepath.Join(xdg, "cursor", "auth.json"), xdgAuth, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".cursor", "auth.json"), legacyAuth, 0600); err != nil {
		t.Fatal(err)
	}

	vault := NewVault(t.TempDir())
	fs := CursorAuthFiles()
	if err := vault.Backup(fs, "work"); err != nil {
		t.Fatal(err)
	}
	profileDir := vault.ProfilePath("cursor", "work")
	xdgCopy, err := os.ReadFile(filepath.Join(profileDir, "xdg-auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacyCopy, err := os.ReadFile(filepath.Join(profileDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(xdgCopy) != string(xdgAuth) || string(legacyCopy) != string(legacyAuth) {
		t.Fatal("vault copies collided or dropped a tree")
	}

	// Wipe the live trees and restore.
	if err := os.Remove(filepath.Join(xdg, "cursor", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".cursor", "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Restore(fs, "work"); err != nil {
		t.Fatal(err)
	}
	gotXDG, err := os.ReadFile(filepath.Join(xdg, "cursor", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	gotLegacy, err := os.ReadFile(filepath.Join(home, ".cursor", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotXDG) != string(xdgAuth) || string(gotLegacy) != string(legacyAuth) {
		t.Fatal("restore wrote the wrong tree")
	}
}
