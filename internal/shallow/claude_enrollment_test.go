package shallow

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

func TestShallowEnrollmentAppliesPrivateExceptionsBeforeFirstLaunch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	const host = `{"userID":"install","hasCompletedOnboarding":true,"model":"shared-model","permissions":{"allow":["Read"]},"autoMode":{"allow":["tests"]},"mcpServers":{"host":{"headers":{"Authorization":"synthetic-host"}}},"hooks":{"Stop":["host-hook"]},"projects":{"/repo":{"mcpServers":{"private":{}}}},"customAuth":"synthetic-host","apiKeyHelper":"host-helper","oauthAccount":{"accountUuid":"host"},"env":{"CUSTOM_CREDENTIAL":"synthetic-host","EDITOR":"vim"}}`
	mgr, realHome := onboardingEnv(t, host)
	writeFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"profile_keys":["mcpServers","hooks","projects","customAuth"],"shared_env_keys":["EDITOR"]}}`)
	for _, name := range []string{"alice", "bob", "oauth"} {
		home, err := mgr.Create(name, CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		state := readProfileClaudeJSON(t, home)
		for _, key := range []string{"mcpServers", "hooks", "projects", "customAuth", "apiKeyHelper", "oauthAccount"} {
			if state[key] != nil {
				t.Fatalf("new profile %s inherited host-private %s", name, key)
			}
		}
		for _, key := range []string{"permissions", "autoMode", "model", "hasCompletedOnboarding"} {
			if state[key] == nil {
				t.Fatalf("new profile %s lost shared/readiness %s", name, key)
			}
		}
		env := parseJSONObject(t, state["env"])
		if len(env) != 1 || string(env["EDITOR"]) != `"vim"` {
			t.Fatalf("new profile %s inherited host credential environment: %v", name, env)
		}
		if err := mgr.EnsureClaudeSettingsPrivate(name); err != nil {
			t.Fatal(err)
		}
	}
	if readFile(t, filepath.Join(realHome, ".claude.json")) != host {
		t.Fatal("enrollment changed the canonical account")
	}
	writeFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"mode":"per-profile"}}`)
	home, err := mgr.Create("private-policy", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	state := readProfileClaudeJSON(t, home)
	if len(state) != 2 || state["userID"] == nil || state["hasCompletedOnboarding"] == nil {
		t.Fatalf("per-profile enrollment inherited host workflow or authentication: %v", state)
	}
}

func TestNoSyncRejectsLegacyStateBeforeRepairingSettingsLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link creation requires privileges on Windows")
	}
	for _, kind := range []string{"malformed", "symlink", "hardlink", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			const host = `{"oauthAccount":{"accountUuid":"host"},"apiKey":"synthetic-host"}`
			mgr, realHome := onboardingEnv(t, host)
			home, err := mgr.Create("alice", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			sharedSettings := filepath.Join(realHome, ".claude", "settings.json")
			writeFile(t, sharedSettings, `{"apiKeyHelper":"synthetic-host"}`)
			settings := filepath.Join(home, ".claude", "settings.json")
			if err := os.Symlink(sharedSettings, settings); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(home, ".claude.json")
			if kind == "malformed" {
				writeFile(t, legacy, `{broken`)
			} else {
				if err := os.Rename(legacy, legacy+".saved"); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(realHome, ".claude.json")
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				} else if kind == "dangling" {
					target = filepath.Join(realHome, "missing-identity.json")
				}
				if err := link(target, legacy); err != nil {
					t.Fatal(err)
				}
			}
			if err := mgr.EnsureClaudeSettingsPrivate("alice"); err == nil {
				t.Fatal("--no-sync-config allowed invalid or shared identity state")
			}
			info, err := os.Lstat(settings)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("failed legacy preflight partially repaired settings: %v", err)
			}
			if readFile(t, filepath.Join(realHome, ".claude.json")) != host {
				t.Fatal("launch preflight changed the host identity")
			}
		})
	}
}

func TestNoSyncValidatesBothPrivateDocumentsWithoutRefreshingEither(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	mgr, realHome := onboardingEnv(t, `{}`)
	home, err := mgr.Create("alice", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(home, ".claude", "settings.json"): "{\"model\":\"private\",\"apiKeyHelper\":\"synthetic-alice\"}\n",
		filepath.Join(home, ".claude.json"):             "{\"oauthAccount\":{\"accountUuid\":\"alice\"},\"permissions\":{\"allow\":[]}}\n",
	}
	before := make(map[string]os.FileInfo)
	for path, body := range files {
		writeFile(t, path, body)
		before[path], err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	// No-sync uses the private lane even while the canonical files are edited.
	writeFile(t, filepath.Join(realHome, ".claude", "settings.json"), `{broken`)
	writeFile(t, filepath.Join(realHome, ".claude.json"), `{broken`)
	if err := mgr.EnsureClaudeSettingsPrivate("alice"); err != nil {
		t.Fatal(err)
	}
	for path, body := range files {
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before[path], after) || !before[path].ModTime().Equal(after.ModTime()) || readFile(t, path) != body {
			t.Fatalf("no-sync rewrote private document %s: %v", path, err)
		}
	}
}
