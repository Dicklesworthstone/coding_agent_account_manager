package cursor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// Fixtures are synthetic. They contain no real token, auth id, or account.

const (
	cursorTokenA = `{"accessToken":"synthetic-cursor-token-a","refreshToken":"synthetic-cursor-refresh-a","label":"profile-a"}`
	cursorTokenB = `{"accessToken":"synthetic-cursor-token-b","refreshToken":"synthetic-cursor-refresh-b","label":"profile-b"}`
	cursorGlobal = `{"accessToken":"synthetic-cursor-token-global","refreshToken":"synthetic-cursor-refresh-global","label":"global-leak"}`
	cursorInfo   = `{"authInfo":{"email":"should-not-count@example.com","displayName":"Meta"},"version":1}`
)

func testProfile(t *testing.T, name string) *profile.Profile {
	t.Helper()
	return &profile.Profile{
		Name:     name,
		Provider: "cursor",
		BasePath: t.TempDir(),
	}
}

func writeCursorAuth(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEnvIsolatesHomeAndXDG(t *testing.T) {
	global := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", global)
	t.Setenv("HOME", t.TempDir())

	prof := testProfile(t, "work")
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	env, err := p.Env(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if env["HOME"] != prof.HomePath() {
		t.Fatalf("HOME = %q, want profile home %q", env["HOME"], prof.HomePath())
	}
	if env["XDG_CONFIG_HOME"] != prof.XDGConfigPath() {
		t.Fatalf("XDG_CONFIG_HOME = %q, want %q", env["XDG_CONFIG_HOME"], prof.XDGConfigPath())
	}
	if env["XDG_CONFIG_HOME"] == global {
		t.Fatal("profile XDG_CONFIG_HOME is the machine-global config")
	}
	merged := mergeEnv(env)
	joined := strings.Join(merged, "\n")
	if strings.Contains(joined, "XDG_CONFIG_HOME="+global) {
		t.Fatal("merged env still contains the global XDG_CONFIG_HOME")
	}
	homeCount := 0
	xdgCount := 0
	for _, kv := range merged {
		key, val, _ := strings.Cut(kv, "=")
		switch key {
		case "HOME":
			homeCount++
			if val != prof.HomePath() {
				t.Fatalf("merged HOME = %q", val)
			}
		case "XDG_CONFIG_HOME":
			xdgCount++
			if val != prof.XDGConfigPath() {
				t.Fatalf("merged XDG_CONFIG_HOME = %q", val)
			}
		}
	}
	if homeCount != 1 || xdgCount != 1 {
		t.Fatalf("HOME count %d, XDG count %d, want 1 each", homeCount, xdgCount)
	}
}

func TestAuthInfoAloneIsNotALogin(t *testing.T) {
	prof := testProfile(t, "meta")
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	infoPath := filepath.Join(prof.XDGConfigPath(), "cursor", "cli-config.json")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte(cursorInfo), 0600); err != nil {
		t.Fatal(err)
	}

	res, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid {
		t.Fatal("passive validation accepted authInfo without an access token")
	}
}

func TestDetectExistingAuthPrefersXDGToken(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeCursorAuth(t, filepath.Join(xdg, "cursor"), cursorTokenA)
	// Legacy metadata must not become the primary detection.
	legacy := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "cli-config.json"), []byte(cursorInfo), 0600); err != nil {
		t.Fatal(err)
	}

	det, err := New().DetectExistingAuth()
	if err != nil {
		t.Fatal(err)
	}
	if !det.Found || det.Primary == nil {
		t.Fatal("expected a credential")
	}
	if !strings.HasSuffix(det.Primary.Path, filepath.Join("xdg", "cursor", "auth.json")) && det.Primary.Path != filepath.Join(xdg, "cursor", "auth.json") {
		t.Fatalf("primary = %q, want XDG auth.json", det.Primary.Path)
	}
}

func TestTwoProfilesDoNotReadGlobalAuth(t *testing.T) {
	globalXDG := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalXDG)
	t.Setenv("HOME", t.TempDir())
	writeCursorAuth(t, filepath.Join(globalXDG, "cursor"), cursorGlobal)

	binDir := t.TempDir()
	script := `#!/bin/sh
# Prints the label of whichever auth.json this environment can see.
# Prefers XDG, then HOME/.cursor. Never searches past those.
auth="${XDG_CONFIG_HOME:-$HOME/.config}/cursor/auth.json"
if [ ! -f "$auth" ]; then
  auth="$HOME/.cursor/auth.json"
fi
label=""
if [ -f "$auth" ]; then
  label=$(sed -n 's/.*"label"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$auth")
fi
if [ -n "$label" ]; then
  printf '{"isAuthenticated":true,"userInfo":{"email":"%s@example.com"}}\n' "$label"
  exit 0
fi
printf '{"isAuthenticated":false}\n'
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "cursor-agent"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	p := New()
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		body  string
		email string
	}{
		{"alpha", cursorTokenA, "profile-a@example.com"},
		{"beta", cursorTokenB, "profile-b@example.com"},
	} {
		prof := testProfile(t, tc.name)
		if err := p.PrepareProfile(ctx, prof); err != nil {
			t.Fatal(err)
		}
		writeCursorAuth(t, profileXDGCursor(prof), tc.body)
		st, err := p.Status(ctx, prof)
		if err != nil {
			t.Fatalf("%s status: %v", tc.name, err)
		}
		if !st.LoggedIn {
			t.Fatalf("%s not authenticated: %s", tc.name, st.Error)
		}
		if st.AccountID != tc.email {
			t.Fatalf("%s account = %q, want %q", tc.name, st.AccountID, tc.email)
		}
		if strings.Contains(st.AccountID, "global") {
			t.Fatalf("%s read the machine-global account", tc.name)
		}

		res, err := p.ValidateToken(ctx, prof, false)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Valid || res.Method != "active" {
			t.Fatalf("%s active validation = %+v", tc.name, res)
		}
	}
}

func TestEmptyProfileDoesNotInheritGlobal(t *testing.T) {
	globalXDG := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalXDG)
	writeCursorAuth(t, filepath.Join(globalXDG, "cursor"), cursorGlobal)

	binDir := t.TempDir()
	script := `#!/bin/sh
auth="${XDG_CONFIG_HOME:-$HOME/.config}/cursor/auth.json"
label=""
if [ -f "$auth" ]; then
  label=$(sed -n 's/.*"label"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$auth")
fi
if [ -n "$label" ]; then
  printf '{"isAuthenticated":true,"userInfo":{"email":"%s@example.com"}}\n' "$label"
  exit 0
fi
printf '{"isAuthenticated":false}\n'
`
	if err := os.WriteFile(filepath.Join(binDir, "cursor-agent"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prof := testProfile(t, "empty")
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	st, err := p.Status(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if st.LoggedIn {
		t.Fatalf("empty profile authenticated as %q", st.AccountID)
	}
}

func TestLogoutClearsProfileNotGlobal(t *testing.T) {
	globalXDG := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalXDG)
	writeCursorAuth(t, filepath.Join(globalXDG, "cursor"), cursorGlobal)

	prof := testProfile(t, "work")
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	writeCursorAuth(t, profileXDGCursor(prof), cursorTokenA)
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(profileXDGCursor(prof), "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("profile auth still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalXDG, "cursor", "auth.json")); err != nil {
		t.Fatalf("global auth was disturbed: %v", err)
	}
}

func TestImportLandsInProfileXDG(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "auth.json")
	if err := os.WriteFile(src, []byte(cursorTokenA), 0600); err != nil {
		t.Fatal(err)
	}
	prof := testProfile(t, "imported")
	copied, err := New().ImportAuth(context.Background(), src, prof)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 2 {
		t.Fatalf("copied %d files, want XDG and legacy", len(copied))
	}
	if !fileHasAccessToken(filepath.Join(profileXDGCursor(prof), "auth.json")) {
		t.Fatal("profile XDG auth.json has no token")
	}
}

func TestParseCursorStatus(t *testing.T) {
	st, err := parseCursorStatus([]byte(`{"isAuthenticated":true,"userInfo":{"email":"a@example.com","userId":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsAuthenticated || st.Email != "a@example.com" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := parseCursorStatus([]byte(`{"authInfo":{"email":"a@example.com"}}`)); err == nil {
		t.Fatal("authInfo payload was accepted as status")
	}
	if _, err := parseCursorStatus([]byte("not-json")); err == nil {
		t.Fatal("malformed status was accepted")
	}
}
