package claude

// Regression tests for GitHub issue #90: the shared user assets (skills,
// plugins, commands, agents) were linked only into the legacy home/.claude,
// while the profile env points CLAUDE_CONFIG_DIR at xdg_config/claude-code —
// the only directory an XDG-aware Claude Code build reads them from.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// fakeRealHome points HOME at a fresh directory holding a real ~/.claude with
// the given asset entries, and returns it.
func fakeRealHome(t *testing.T, entries ...string) string {
	t.Helper()
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	for _, name := range entries {
		dir := filepath.Join(realHome, ".claude", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return realHome
}

func assertLinkedAsset(t *testing.T, dir, name, realHome string) {
	t.Helper()
	linkPath := filepath.Join(dir, name)
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("%s is not a symlink: %v", linkPath, err)
	}
	want := filepath.Join(realHome, ".claude", name)
	if target != want {
		t.Fatalf("%s -> %s, want %s", linkPath, target, want)
	}
	data, err := os.ReadFile(filepath.Join(linkPath, "marker"))
	if err != nil || string(data) != name {
		t.Fatalf("%s does not resolve to the real asset: data=%q err=%v", linkPath, data, err)
	}
}

func TestPrepareProfileSharesAssetsIntoEveryClaudeConfigDir(t *testing.T) {
	realHome := fakeRealHome(t, "skills", "plugins", "commands", "agents")
	prof := &profile.Profile{Name: "acct", Provider: "claude", BasePath: t.TempDir()}

	if err := New().PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}

	env, err := New().Env(context.Background(), prof)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	cfgDir := env["CLAUDE_CONFIG_DIR"]
	if cfgDir != claudeConfigDirForProfile(prof) {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", cfgDir, claudeConfigDirForProfile(prof))
	}

	for _, dir := range []string{cfgDir, filepath.Join(prof.HomePath(), ".claude")} {
		for _, name := range sharedClaudeAssetEntries {
			assertLinkedAsset(t, dir, name, realHome)
		}
	}
}

func TestPrepareProfileSkipsAssetsMissingFromRealHome(t *testing.T) {
	fakeRealHome(t, "skills")
	prof := &profile.Profile{Name: "acct", Provider: "claude", BasePath: t.TempDir()}

	if err := New().PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}
	for _, dir := range claudeConfigDirsForProfile(prof) {
		if _, err := os.Lstat(filepath.Join(dir, "plugins")); !os.IsNotExist(err) {
			t.Fatalf("%s/plugins should not exist when the real home has none (err=%v)", dir, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "skills")); err != nil {
			t.Fatalf("%s/skills missing: %v", dir, err)
		}
	}
}

// TestRefreshProfileHealsExistingProfile covers the exec path: a profile
// prepared before the XDG config dir received the links (simulated by
// removing the link and by installing a new asset afterwards) is repaired by
// RefreshProfile without touching credentials or the profile's own files.
func TestRefreshProfileHealsExistingProfile(t *testing.T) {
	realHome := fakeRealHome(t, "skills")
	prof := &profile.Profile{Name: "acct", Provider: "claude", BasePath: t.TempDir()}
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}

	cfgDir := claudeConfigDirForProfile(prof)

	// Old layout: the XDG dir has no skills link at all.
	if err := os.Remove(filepath.Join(cfgDir, "skills")); err != nil {
		t.Fatal(err)
	}
	// Account state the refresh must leave alone.
	credPath := filepath.Join(cfgDir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real (profile-owned) commands dir must not be replaced by a link.
	ownCommands := filepath.Join(cfgDir, "commands")
	if err := os.MkdirAll(ownCommands, 0o700); err != nil {
		t.Fatal(err)
	}
	// The user installs plugins after the profile was created.
	if err := os.MkdirAll(filepath.Join(realHome, ".claude", "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realHome, ".claude", "plugins", "marker"), []byte("plugins"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realHome, ".claude", "commands"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := p.RefreshProfile(context.Background(), prof); err != nil {
		t.Fatalf("RefreshProfile: %v", err)
	}

	assertLinkedAsset(t, cfgDir, "skills", realHome)
	assertLinkedAsset(t, cfgDir, "plugins", realHome)
	assertLinkedAsset(t, filepath.Join(prof.HomePath(), ".claude"), "plugins", realHome)

	if info, err := os.Lstat(ownCommands); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("profile-owned commands dir must stay a real dir (info=%v err=%v)", info, err)
	}
	if data, err := os.ReadFile(credPath); err != nil || !strings.Contains(string(data), "claudeAiOauth") {
		t.Fatalf("credentials disturbed: data=%q err=%v", data, err)
	}

	// Idempotent.
	if err := p.RefreshProfile(context.Background(), prof); err != nil {
		t.Fatalf("second RefreshProfile: %v", err)
	}
	assertLinkedAsset(t, cfgDir, "skills", realHome)
}

func TestRefreshProfileIsNoopForGlobalHome(t *testing.T) {
	// A profile whose pseudo-home IS the real home must not link ~/.claude
	// entries onto themselves.
	base := t.TempDir()
	prof := &profile.Profile{Name: "global", Provider: "claude", BasePath: base}
	realHome := prof.HomePath()
	if err := os.MkdirAll(filepath.Join(realHome, ".claude", "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", realHome)
	if err := New().RefreshProfile(context.Background(), prof); err != nil {
		t.Fatalf("RefreshProfile: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(realHome, ".claude", "skills")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("real skills dir was disturbed: info=%v err=%v", info, err)
	}
}

var _ provider.ProfileRefresher = (*Provider)(nil)

func TestAPIKeySettingsLandInEveryClaudeConfigDir(t *testing.T) {
	fakeRealHome(t)
	prof := &profile.Profile{
		Name:     "acct",
		Provider: "claude",
		AuthMode: string(provider.AuthModeAPIKey),
		BasePath: t.TempDir(),
	}
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}

	paths := claudeSettingsPathsForProfile(prof)
	if len(paths) != 2 {
		t.Fatalf("settings paths = %v, want legacy + XDG", paths)
	}
	for _, settingsPath := range paths {
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("%s not written: %v", settingsPath, err)
		}
		if !strings.Contains(string(data), "apiKeyHelper") {
			t.Fatalf("%s lacks apiKeyHelper: %s", settingsPath, data)
		}
	}

	// Status must recognise the XDG-side settings alone.
	if err := os.Remove(filepath.Join(prof.HomePath(), ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	status, err := p.Status(context.Background(), prof)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.LoggedIn {
		t.Fatal("Status should report logged in from the XDG settings.json")
	}

	// Logout clears the XDG-side settings too.
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	for _, settingsPath := range paths {
		if _, err := os.Lstat(settingsPath); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed by Logout (err=%v)", settingsPath, err)
		}
	}
}
