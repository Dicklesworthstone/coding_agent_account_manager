package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func cursorSession(expiry time.Time, apiKey bool) string {
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix()))) + ".SYNTHETIC"
	key := ""
	if apiKey {
		key = "SYNTHETIC-API-KEY"
	}
	return fmt.Sprintf(`{"accessToken":%q,"refreshToken":%q,"apiKey":%q}`, jwt, jwt, key)
}

func TestCursorStatusAndValidationUseAuthoritativeCredential(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			withGOOS(t, platform)
			for _, tc := range []struct {
				name, auth string
				valid      bool
				expires    bool
			}{
				{"future_session", cursorSession(now.Add(3*24*time.Hour), false), true, true},
				{"expired_session", cursorSession(now.Add(-time.Hour), false), false, true},
				{"expiry_boundary", cursorSession(now, false), false, true},
				{"renewable_lapsed_token", cursorSession(now.Add(-time.Hour), true), true, false},
				{"api_key_only", `{"apiKey":"SYNTHETIC-KEY"}`, true, false},
				{"opaque_access", `{"accessToken":"SYNTHETIC-OPAQUE"}`, true, false},
				{"null_optional_key", `{"accessToken":"SYNTHETIC-OPAQUE","apiKey":null}`, true, false},
				{"null_optional_access", `{"accessToken":null,"apiKey":"SYNTHETIC-KEY"}`, true, false},
				{"empty_object", `{}`, false, false},
				{"empty_access", `{"accessToken":"  "}`, false, false},
				{"refresh_alias_only", `{"refreshToken":"SYNTHETIC-SESSION"}`, false, false},
				{"null", `null`, false, false},
				{"malformed", `{"accessToken":`, false, false},
				{"invalid_access_type", `{"accessToken":42}`, false, false},
				{"invalid_key_type", `{"accessToken":"SYNTHETIC","apiKey":{}}`, false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					prof := newProfile(t)
					paths := profilePaths(prof)
					writeFile(t, filepath.Join(paths.ConfigDir, "cli-config.json"), `{"authInfo":{"email":"stale@example.invalid"}}`)
					if legacyAuthPath(prof) != paths.AuthFile {
						writeFile(t, legacyAuthPath(prof), cursorSession(now.Add(30*24*time.Hour), true))
					}
					writeFile(t, paths.AuthFile, tc.auth)
					p := New()
					result, err := p.ValidateToken(context.Background(), prof, true)
					if err != nil || result.Valid != tc.valid || result.ExpiresAt.IsZero() == tc.expires {
						t.Fatalf("validation = %+v, %v; want valid=%v known expiry=%v", result, err, tc.valid, tc.expires)
					}
					status, err := p.Status(context.Background(), prof)
					if err != nil || status.LoggedIn != tc.valid || (status.ExpiresAt != "") != tc.expires {
						t.Fatalf("status = %+v, %v", status, err)
					}
					if !tc.valid && result.Error == "" {
						t.Fatal("invalid credential has no diagnostic")
					}
					if tc.expires && !tc.valid && (!strings.Contains(result.Error, "log in again") || strings.Contains(result.Error, "caam refresh")) {
						t.Fatalf("expired session advice = %q", result.Error)
					}
				})
			}
		})
	}
}

func TestCursorMetadataFallbackRequiresAbsentCredential(t *testing.T) {
	withGOOS(t, "linux")
	for _, tc := range []struct {
		name, config string
		nonregular   bool
		valid        bool
	}{
		{"keychain_metadata", `{"authInfo":{"email":"native@example.invalid"}}`, false, true},
		{"workflow_only", `{"model":"synthetic","permissions":{"allow":[]}}`, false, false},
		{"empty_metadata", `{"authInfo":{}}`, false, false},
		{"nonregular_canonical", `{"authInfo":{"email":"native@example.invalid"}}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof := newProfile(t)
			paths := profilePaths(prof)
			writeFile(t, filepath.Join(paths.ConfigDir, "cli-config.json"), tc.config)
			if tc.nonregular {
				if err := os.MkdirAll(paths.AuthFile, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result, err := New().ValidateToken(context.Background(), prof, true)
			if err != nil || result.Valid != tc.valid {
				t.Fatalf("validation = %+v, %v; want %v", result, err, tc.valid)
			}
		})
	}
}

func TestCursorDetectionPrefersCanonicalCredentials(t *testing.T) {
	withGOOS(t, "linux")
	for _, auth := range []string{`{"accessToken":"SYNTHETIC"}`, `{}`, `{"accessToken":`} {
		t.Run(auth, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
			t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(home, "config"))
			paths := userPaths()
			writeFile(t, filepath.Join(paths.ConfigDir, "cli-config.json"), `{"authInfo":{"email":"stale@example.invalid"}}`)
			writeFile(t, paths.AuthFile, auth)
			detection, err := New().DetectExistingAuth()
			valid := strings.Contains(auth, "SYNTHETIC")
			if err != nil || detection.Found != valid || (valid && (detection.Primary == nil || detection.Primary.Path != paths.AuthFile)) || (!valid && detection.Primary != nil) {
				t.Fatalf("detection = %+v, %v", detection, err)
			}
		})
	}
}

func TestCursorLogoutClearsMetadataAndPreservesWorkflow(t *testing.T) {
	withGOOS(t, "linux")
	prof := newProfile(t)
	paths := profilePaths(prof)
	configPath := filepath.Join(paths.ConfigDir, "cli-config.json")
	writeFile(t, configPath, `{"authInfo":{"email":"native@example.invalid"},"model":"synthetic-model","permissions":{"allow":["Read"]}}`)
	writeFile(t, paths.AuthFile, `{"accessToken":"SYNTHETIC-ACCESS"}`)
	settingsPath := filepath.Join(prof.HomePath(), ".cursor", "settings.json")
	const settings = `{"theme":"unchanged"}`
	writeFile(t, settingsPath, settings)
	p := New()
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	status, err := p.Status(context.Background(), prof)
	if err != nil || status.LoggedIn {
		t.Fatalf("logged out profile = %+v, %v", status, err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if _, exists := config["authInfo"]; exists || config["model"] != "synthetic-model" {
		t.Fatalf("logout retained metadata or lost model setting: %s", data)
	}
	allow := config["permissions"].(map[string]any)["allow"].([]any)
	if len(allow) != 1 || allow[0] != "Read" {
		t.Fatalf("logout changed permissions: %s", data)
	}
	info, err := os.Stat(configPath)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("logout config is not private: %v, %v", info, err)
	}
	data, err = os.ReadFile(settingsPath)
	if err != nil || string(data) != settings {
		t.Fatalf("logout changed unrelated settings: %q, %v", data, err)
	}
}
