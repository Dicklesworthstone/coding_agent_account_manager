package authfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveCursorPaths(t *testing.T) {
	home := filepath.Join("/h", "u")
	tests := []struct {
		name       string
		goos       string
		env        map[string]string
		wantConfig string
		wantAuth   string
	}{
		{
			name:       "linux defaults",
			goos:       "linux",
			wantConfig: filepath.Join(home, ".cursor"),
			wantAuth:   filepath.Join(home, ".config", "cursor", "auth.json"),
		},
		{
			name:       "linux XDG_CONFIG_HOME moves config and credentials",
			goos:       "linux",
			env:        map[string]string{"XDG_CONFIG_HOME": "/xdg"},
			wantConfig: filepath.Join("/xdg", "cursor"),
			wantAuth:   filepath.Join("/xdg", "cursor", "auth.json"),
		},
		{
			name:       "CURSOR_CONFIG_DIR wins for config only",
			goos:       "linux",
			env:        map[string]string{"CURSOR_CONFIG_DIR": "/cc", "XDG_CONFIG_HOME": "/xdg"},
			wantConfig: "/cc",
			wantAuth:   filepath.Join("/xdg", "cursor", "auth.json"),
		},
		{
			name:       "blank overrides are ignored for config",
			goos:       "linux",
			env:        map[string]string{"CURSOR_CONFIG_DIR": "  ", "XDG_CONFIG_HOME": ""},
			wantConfig: filepath.Join(home, ".cursor"),
			wantAuth:   filepath.Join(home, ".config", "cursor", "auth.json"),
		},
		{
			name:       "darwin keeps credentials under ~/.cursor even with XDG",
			goos:       "darwin",
			env:        map[string]string{"XDG_CONFIG_HOME": "/xdg"},
			wantConfig: filepath.Join("/xdg", "cursor"),
			wantAuth:   filepath.Join(home, ".cursor", "auth.json"),
		},
		{
			name:       "windows uses APPDATA",
			goos:       "windows",
			env:        map[string]string{"APPDATA": "/appdata"},
			wantConfig: filepath.Join(home, ".cursor"),
			wantAuth:   filepath.Join("/appdata", "Cursor", "auth.json"),
		},
		{
			name:       "windows without APPDATA",
			goos:       "windows",
			wantConfig: filepath.Join(home, ".cursor"),
			wantAuth:   filepath.Join(home, "AppData", "Roaming", "Cursor", "auth.json"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCursorPaths(home, tc.goos, envMap(tc.env))
			if got.ConfigDir != tc.wantConfig {
				t.Errorf("ConfigDir = %q, want %q", got.ConfigDir, tc.wantConfig)
			}
			if got.AuthFile != tc.wantAuth {
				t.Errorf("AuthFile = %q, want %q", got.AuthFile, tc.wantAuth)
			}
		})
	}
}

// CursorAuthFiles must back up the credential file cursor-agent actually
// reads, and a backup/restore round trip must land it back there.
func TestCursorAuthFilesBackupRestoreUsesLiveCredentialPath(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CURSOR_CONFIG_DIR", "")
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := ResolveCursorPaths(homeDir, runtime.GOOS, os.Getenv)
	set := CursorAuthFiles()
	paths := map[string]bool{}
	for _, f := range set.Files {
		paths[f.Path] = true
	}
	if !paths[want.AuthFile] {
		t.Fatalf("CursorAuthFiles %v does not include the live credential file %s", paths, want.AuthFile)
	}
	if !paths[filepath.Join(xdg, "cursor", "cli-config.json")] {
		t.Fatalf("CursorAuthFiles %v ignores XDG_CONFIG_HOME for cli-config.json", paths)
	}

	accountA := []byte(`{"accessToken":"a-access","refreshToken":"a-refresh"}`)
	if err := os.MkdirAll(filepath.Dir(want.AuthFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want.AuthFile, accountA, 0o600); err != nil {
		t.Fatal(err)
	}

	vault := NewVault(filepath.Join(t.TempDir(), "vault"))
	if err := vault.Backup(set, "a"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := os.WriteFile(want.AuthFile, []byte(`{"accessToken":"b-access"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vault.Restore(set, "a"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(want.AuthFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(accountA) {
		t.Fatalf("live credential after restore = %s, want %s", got, accountA)
	}
}
