package authfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestCursorActiveProfileIgnoresConfigChurn(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	config := filepath.Join(dir, "cli-config.json")
	settings := filepath.Join(dir, "settings.json")
	set := AuthFileSet{Tool: "cursor", AllowOptionalOnly: true, Files: []AuthFileSpec{
		{Path: config}, {Path: auth}, {Path: settings},
	}}
	for path, data := range map[string]string{auth: `{"accessToken":"session-a","refreshToken":"session-a"}`, config: `{"model":"a"}`, settings: `{"theme":"a"}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	vault := NewVault(filepath.Join(t.TempDir(), "vault"))
	if err := vault.Backup(set, "work"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config, settings} {
		if err := os.WriteFile(path, []byte(`{"changed":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := vault.ActiveProfile(set); err != nil || got != "work" {
		t.Fatalf("after churn = %q, %v", got, err)
	}
	if err := os.WriteFile(auth, []byte(`{"accessToken":"session-b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := vault.ActiveProfile(set); err != nil || got != "" {
		t.Fatalf("different login = %q, %v", got, err)
	}

	// A config-only/keychain profile still matches optional config files.
	if err := os.WriteFile(config, []byte(`{"authInfo":{"email":"keychain@example.invalid"},"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configOnly := AuthFileSet{Tool: "cursor", AllowOptionalOnly: true, Files: []AuthFileSpec{{Path: config}, {Path: settings}}}
	if err := vault.Backup(configOnly, "keychain"); err != nil {
		t.Fatal(err)
	}
	if got, err := vault.ActiveProfile(configOnly); err != nil || got != "keychain" {
		t.Fatalf("config-only = %q, %v", got, err)
	}
	if err := os.WriteFile(config, []byte(`{"changed":"again"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := vault.ActiveProfile(configOnly); err != nil || got != "" {
		t.Fatalf("config-only churn = %q, %v", got, err)
	}
}

func TestCursorCredentialValidationAndVaultRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, auth, config string
		valid              bool
	}{
		{"access", `{"accessToken":"SYNTHETIC-ACCESS"}`, `{"model":"irrelevant"}`, true},
		{"api_key", `{"apiKey":"SYNTHETIC-KEY"}`, "", true},
		{"keychain_metadata", "", `{"authInfo":{"email":"native@example.invalid"}}`, true},
		{"empty", `{}`, `{"authInfo":{"email":"stale@example.invalid"}}`, false},
		{"empty_token", `{"accessToken":" "}`, `{"authInfo":{"email":"stale@example.invalid"}}`, false},
		{"refresh_only", `{"refreshToken":"SYNTHETIC-ALIAS"}`, `{"authInfo":{"email":"stale@example.invalid"}}`, false},
		{"malformed", `{"accessToken":`, `{"authInfo":{"email":"stale@example.invalid"}}`, false},
		{"null", `null`, "", false},
		{"wrong_type", `{"apiKey":42}`, "", false},
		{"workflow_only", "", `{"model":"synthetic","permissions":{"allow":[]}}`, false},
		{"empty_auth_info", "", `{"authInfo":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := t.TempDir()
			set := AuthFileSet{Tool: "cursor", AllowOptionalOnly: true, Files: []AuthFileSpec{
				{Path: filepath.Join(live, "cli-config.json")}, {Path: filepath.Join(live, "auth.json")}, {Path: filepath.Join(live, "settings.json")},
			}}
			vault := NewVault(t.TempDir())
			for _, dir := range []string{live, vault.ProfilePath("cursor", "candidate")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for name, data := range map[string]string{"auth.json": tc.auth, "cli-config.json": tc.config, "settings.json": `{"theme":"keep"}`} {
					if data != "" {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if got := HasAuthFiles(set); got != tc.valid {
				t.Fatalf("HasAuthFiles = %v, want %v", got, tc.valid)
			}
			if err := vault.ValidateProfileCredentials(set, "candidate"); (err == nil) != tc.valid {
				t.Fatalf("vault credential validation = %v, want valid=%v", err, tc.valid)
			}
			if err := vault.Backup(set, "backup"); (err == nil) != tc.valid {
				t.Fatalf("Backup = %v, want valid=%v", err, tc.valid)
			}
			if tc.valid {
				return
			}
			if _, err := os.Stat(vault.ProfilePath("cursor", "backup")); !os.IsNotExist(err) {
				t.Fatalf("rejected backup created a snapshot: %v", err)
			}
			const existing = `{"accessToken":"SYNTHETIC-LIVE-ACCOUNT"}`
			if err := os.WriteFile(filepath.Join(live, "auth.json"), []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			if err := vault.Restore(set, "candidate"); err == nil {
				t.Fatal("authless candidate was restored")
			}
			data, err := os.ReadFile(filepath.Join(live, "auth.json"))
			if err != nil || string(data) != existing {
				t.Fatalf("refused restore changed live credentials: %v", err)
			}
		})
	}
}

func TestValidateCursorAuthFileDistinguishesMissingFromInvalid(t *testing.T) {
	root := t.TempDir()
	if err := ValidateCursorAuthFile(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing source = %v", err)
	}
	if err := ValidateCursorAuthFile(root); !errors.Is(err, ErrInvalidCredentials) || os.IsNotExist(err) {
		t.Fatalf("directory source = %v", err)
	}
	path := filepath.Join(root, "auth.json")
	if err := os.WriteFile(path, []byte(`{"refreshToken":"SYNTHETIC-ALIAS"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCursorAuthFile(path); !errors.Is(err, ErrNoCredentials) || os.IsNotExist(err) {
		t.Fatalf("authless source = %v", err)
	}
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

func TestCursorActiveProfileUsesAuthFile(t *testing.T) {
	const authA = `{"accessToken":"account-a-session","refreshToken":"account-a-session"}`
	const authB = `{"accessToken":"account-b-session","refreshToken":"account-b-session"}`
	const config = `{"authInfo":{"email":"a@example.com"},"model":"old"}`
	const changedConfig = `{"authInfo":{"email":"a@example.com"},"model":"new","lastUsedAt":42}`
	const settings = `{"theme":"light"}`
	const changedSettings = `{"theme":"dark"}`

	tests := []struct {
		name              string
		liveAuth          string
		savedAuth         string
		liveConfig        string
		savedConfig       string
		liveSettings      string
		savedSettings     string
		liveAuthDirectory bool
		want              string
	}{
		{
			name:     "config and settings churn does not hide session",
			liveAuth: authA, savedAuth: authA,
			liveConfig: changedConfig, savedConfig: config,
			liveSettings: changedSettings, savedSettings: settings,
			want: "work",
		},
		{
			name:     "new optional files do not hide session",
			liveAuth: authA, savedAuth: authA,
			liveConfig: config, liveSettings: settings,
			want: "work",
		},
		{
			name:     "missing optional files do not hide session",
			liveAuth: authA, savedAuth: authA,
			savedConfig: config, savedSettings: settings,
			want: "work",
		},
		{
			name:     "API key credentials ignore optional churn",
			liveAuth: `{"apiKey":"synthetic-api-key"}`, savedAuth: `{"apiKey":"synthetic-api-key"}`,
			liveConfig: changedConfig, savedConfig: config,
			liveSettings: changedSettings, savedSettings: settings,
			want: "work",
		},
		{
			name:     "different credentials cannot match identical config",
			liveAuth: authB, savedAuth: authA,
			liveConfig: config, savedConfig: config,
			liveSettings: settings, savedSettings: settings,
		},
		{
			name:       "missing saved credentials cannot match identical config",
			liveAuth:   authA,
			liveConfig: config, savedConfig: config,
			liveSettings: settings, savedSettings: settings,
		},
		{
			name:       "missing live credentials cannot match file-backed snapshot",
			savedAuth:  authA,
			liveConfig: config, savedConfig: config,
			liveSettings: settings, savedSettings: settings,
		},
		{
			name:              "unreadable credentials cannot fall back to identical config",
			liveAuthDirectory: true,
			liveConfig:        config, savedConfig: config,
			liveSettings: settings, savedSettings: settings,
		},
		{
			name:       "config-only keychain snapshot remains detectable",
			liveConfig: config, savedConfig: config,
			liveSettings: settings, savedSettings: settings,
			want: "work",
		},
		{
			name:       "config-only different login does not match",
			liveConfig: `{"authInfo":{"email":"b@example.com"},"model":"old"}`, savedConfig: config,
		},
		{
			name:      "no live files cannot match saved session",
			savedAuth: authA, savedConfig: config, savedSettings: settings,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
			t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(home, "separate-config"))
			t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))

			set := CursorAuthFiles()
			paths := ResolveCursorPaths(home, runtime.GOOS, os.Getenv)
			vault := NewVault(filepath.Join(t.TempDir(), "vault"))
			profileDir := vault.ProfilePath("cursor", "work")
			if err := os.MkdirAll(profileDir, 0o700); err != nil {
				t.Fatal(err)
			}

			files := map[string]string{
				paths.AuthFile: tc.liveAuth,
				filepath.Join(paths.ConfigDir, "cli-config.json"): tc.liveConfig,
				filepath.Join(home, ".cursor", "settings.json"):   tc.liveSettings,
				filepath.Join(profileDir, "auth.json"):            tc.savedAuth,
				filepath.Join(profileDir, "cli-config.json"):      tc.savedConfig,
				filepath.Join(profileDir, "settings.json"):        tc.savedSettings,
			}
			for path, content := range files {
				if content == "" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.liveAuthDirectory {
				// A directory is unreadable as a credential file even when tests
				// run as root, unlike a file with permission bits cleared.
				if err := os.MkdirAll(paths.AuthFile, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			got, err := vault.ActiveProfile(set)
			if err != nil {
				t.Fatalf("ActiveProfile: %v", err)
			}
			if got != tc.want {
				t.Errorf("ActiveProfile = %q, want %q", got, tc.want)
			}
		})
	}
}
