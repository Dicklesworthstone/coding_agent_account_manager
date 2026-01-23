package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/watcher"
	"github.com/charmbracelet/x/ansi"
)

func TestModel_loadProfiles_LoadsFromVault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	vaultDir := filepath.Join(tmpDir, "caam", "vault")
	if err := os.MkdirAll(filepath.Join(vaultDir, "claude", "alice@example.com"), 0700); err != nil {
		t.Fatalf("mkdir alice: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(vaultDir, "claude", "bob@example.com"), 0700); err != nil {
		t.Fatalf("mkdir bob: %v", err)
	}

	m := New()
	msg := m.loadProfiles()
	loaded, ok := msg.(profilesLoadedMsg)
	if !ok {
		t.Fatalf("loadProfiles() msg type = %T, want profilesLoadedMsg", msg)
	}

	got := loaded.profiles["claude"]
	if len(got) != 2 {
		t.Fatalf("claude profiles len = %d, want 2", len(got))
	}
	if got[0].Name != "alice@example.com" {
		t.Fatalf("profiles[0].Name = %q, want %q", got[0].Name, "alice@example.com")
	}
	if got[1].Name != "bob@example.com" {
		t.Fatalf("profiles[1].Name = %q, want %q", got[1].Name, "bob@example.com")
	}
}

func TestModel_BADGES_AddAndExpire(t *testing.T) {
	m := New()

	// Seed a profile so syncProfilesPanel can render it.
	model, _ := m.Update(profilesLoadedMsg{profiles: map[string][]Profile{
		"claude": {{Name: "alice@example.com", Provider: "claude"}},
	}})
	m = model.(Model)

	model, _ = m.Update(profilesChangedMsg{event: watcher.Event{
		Type:     watcher.EventProfileAdded,
		Provider: "claude",
		Profile:  "alice@example.com",
	}})
	m = model.(Model)

	if got := ansi.Strip(m.badgeFor("claude", "alice@example.com")); got != "NEW" {
		t.Fatalf("badgeFor() = %q, want %q", got, "NEW")
	}

	model, _ = m.Update(badgeExpiredMsg{key: badgeKey("claude", "alice@example.com")})
	m = model.(Model)

	if got := m.badgeFor("claude", "alice@example.com"); got != "" {
		t.Fatalf("badgeFor() after expiry = %q, want empty", got)
	}
}
