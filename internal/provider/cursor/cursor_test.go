package cursor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// withGOOS runs a test against another platform's Cursor layout.
func withGOOS(t *testing.T, os string) {
	t.Helper()
	prev := goos
	goos = os
	t.Cleanup(func() { goos = prev })
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newProfile(t *testing.T) *profile.Profile {
	t.Helper()
	return &profile.Profile{Name: "work", Provider: "cursor", BasePath: t.TempDir()}
}

func TestEnvPinsCursorConfigLocations(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/machine/global/xdg")
	t.Setenv("CURSOR_CONFIG_DIR", "/machine/global/cursor")
	prof := newProfile(t)

	env, err := New().Env(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	home := prof.HomePath()
	want := map[string]string{
		"HOME":              home,
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"CURSOR_CONFIG_DIR": filepath.Join(home, ".cursor"),
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
}

// On Linux cursor-agent keeps credentials in $XDG_CONFIG_HOME/cursor/auth.json.
// A profile logged in that way must be reported as logged in, and a global
// XDG_CONFIG_HOME login must not leak into the profile's status.
func TestStatusLinuxUsesProfileXDGCredentials(t *testing.T) {
	withGOOS(t, "linux")
	global := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", global)
	writeFile(t, filepath.Join(global, "cursor", "auth.json"), `{"accessToken":"global"}`)

	prof := newProfile(t)
	p := New()
	st, err := p.Status(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if st.LoggedIn {
		t.Fatal("machine-global Cursor login leaked into an empty profile")
	}

	writeFile(t, filepath.Join(prof.HomePath(), ".config", "cursor", "auth.json"), `{"accessToken":"profile"}`)
	st, err = p.Status(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LoggedIn {
		t.Fatal("profile credentials at ~/.config/cursor/auth.json were not detected")
	}
	res, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("ValidateToken = invalid (%s), want valid", res.Error)
	}
}

func TestStatusStillAcceptsLegacyProfileAuth(t *testing.T) {
	withGOOS(t, "linux")
	prof := newProfile(t)
	writeFile(t, filepath.Join(prof.HomePath(), ".cursor", "auth.json"), `{"accessToken":"old"}`)
	st, err := New().Status(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LoggedIn {
		t.Fatal("legacy ~/.cursor/auth.json in a profile is no longer detected")
	}
}

func TestLogoutRemovesPlatformAndLegacyCredentials(t *testing.T) {
	withGOOS(t, "linux")
	prof := newProfile(t)
	xdgAuth := filepath.Join(prof.HomePath(), ".config", "cursor", "auth.json")
	legacy := filepath.Join(prof.HomePath(), ".cursor", "auth.json")
	writeFile(t, xdgAuth, `{"accessToken":"x"}`)
	writeFile(t, legacy, `{"accessToken":"y"}`)

	if err := New().Logout(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{xdgAuth, legacy} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after logout (err=%v)", p, err)
		}
	}
}

func TestImportAuthPlacesCredentialsWhereCursorReadsThem(t *testing.T) {
	withGOOS(t, "linux")
	src := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, src, `{"accessToken":"imported"}`)
	prof := newProfile(t)

	got, err := New().ImportAuth(context.Background(), src, prof)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prof.HomePath(), ".config", "cursor", "auth.json")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ImportAuth wrote %v, want [%s]", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestDetectExistingAuthLinuxXDG(t *testing.T) {
	withGOOS(t, "linux")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CURSOR_CONFIG_DIR", "")
	writeFile(t, filepath.Join(xdg, "cursor", "auth.json"), `{"accessToken":"live"}`)

	det, err := New().DetectExistingAuth()
	if err != nil {
		t.Fatal(err)
	}
	if !det.Found || det.Primary == nil || det.Primary.Path != filepath.Join(xdg, "cursor", "auth.json") {
		t.Fatalf("detection = %+v, want primary at the XDG credential file", det)
	}
}

func TestEnvPinsAPPDATAOnWindows(t *testing.T) {
	withGOOS(t, "windows")
	t.Setenv("APPDATA", "/machine/global/appdata")
	prof := newProfile(t)

	env, err := New().Env(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prof.HomePath(), "AppData", "Roaming")
	if env["APPDATA"] != want {
		t.Fatalf("env[APPDATA] = %q, want %q", env["APPDATA"], want)
	}
	if got := profilePaths(prof).AuthFile; got != filepath.Join(want, "Cursor", "auth.json") {
		t.Fatalf("profile credential path = %q, want it under the pinned APPDATA", got)
	}
}
