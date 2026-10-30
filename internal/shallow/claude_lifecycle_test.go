package shallow

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

func TestShallowLifecycleThreeProfilesUseCommonPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	mgr, realHome := onboardingEnv(t, `{}`)
	sharedSettings := filepath.Join(realHome, ".claude", "settings.json")
	sharedLegacy := filepath.Join(realHome, ".claude.json")
	policy := `"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["tests"]},"mcpServers":{"current":{"command":"server"}},"hooks":{"Stop":[]},"model":"latest","effortLevel":"high"`
	writeFile(t, sharedSettings, `{`+policy+`,"apiKeyHelper":"host-helper","env":{"ANTHROPIC_API_KEY":"host-key"}}`)
	writeFile(t, sharedLegacy, `{`+policy+`,"oauthAccount":{"accountUuid":"host"},"projects":{"/repo":{"allowedTools":["Read"],"history":["host-private"],"lastSessionId":"host"}}}`)
	accounts := map[string]string{
		"alice": `{"apiKeyHelper":"alice-helper","env":{"AWS_PROFILE":"alice"}}`,
		"bob":   `{"env":{"ANTHROPIC_AUTH_TOKEN":"bob-token"}}`,
		"oauth": `{}`,
	}
	for name, account := range accounts {
		source := writeTempFile(t, name+"-settings.json", account)
		home, err := mgr.Create(name, CreateOptions{ExtraSources: map[string]string{".claude/settings.json": source}})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"`+name+`"},"projects":{"/repo":{"allowedTools":["Bash(*)"],"history":["`+name+`-private"],"lastSessionId":"`+name+`"}}}`)
	}
	for _, name := range []string{"alice", "bob", "oauth", "alice"} {
		if _, err := mgr.SyncClaudeConfig(name); err != nil {
			t.Fatal(err)
		}
		home, err := mgr.HomeFor(name)
		if err != nil {
			t.Fatal(err)
		}
		settings := parseJSONObject(t, []byte(readFile(t, filepath.Join(home, ".claude", "settings.json"))))
		legacy := readProfileClaudeJSON(t, home)
		for key, want := range parseJSONObject(t, []byte(`{`+policy+`}`)) {
			for document, got := range map[string]json.RawMessage{"settings.json": settings[key], ".claude.json": legacy[key]} {
				if !rawJSONEqual(got, want) {
					t.Fatalf("%s %s lost %s: got %s, want %s", name, document, key, got, want)
				}
			}
		}
		account := parseJSONObject(t, []byte(accounts[name]))
		for _, key := range []string{"apiKeyHelper", "env"} {
			if !rawJSONEqual(settings[key], account[key]) {
				t.Fatalf("%s changed account-scoped %s: %s", name, key, settings[key])
			}
		}
		if !rawJSONEqual(legacy["oauthAccount"], json.RawMessage(`{"accountUuid":"`+name+`"}`)) {
			t.Fatalf("%s inherited another identity: %s", name, legacy["oauthAccount"])
		}
		wantProjects := json.RawMessage(`{"/repo":{"allowedTools":["Read"],"history":["` + name + `-private"],"lastSessionId":"` + name + `"}}`)
		if !rawJSONEqual(legacy["projects"], wantProjects) {
			t.Fatalf("%s project policy/session boundary: %s", name, legacy["projects"])
		}
	}
	// Removing a shared permission, auto rule, hook and MCP registration is
	// authoritative for every profile, not just the currently selected one.
	writeFile(t, sharedSettings, `{"permissions":{"allow":[]}}`)
	writeFile(t, sharedLegacy, `{"permissions":{"allow":[]},"projects":{}}`)
	for name := range accounts {
		if _, err := mgr.SyncClaudeConfig(name); err != nil {
			t.Fatal(err)
		}
		home, err := mgr.HomeFor(name)
		if err != nil {
			t.Fatal(err)
		}
		settings := parseJSONObject(t, []byte(readFile(t, filepath.Join(home, ".claude", "settings.json"))))
		legacy := readProfileClaudeJSON(t, home)
		for _, state := range []map[string]json.RawMessage{settings, legacy} {
			for _, key := range []string{"autoMode", "mcpServers", "hooks", "model", "effortLevel"} {
				if _, exists := state[key]; exists {
					t.Fatalf("%s resurrected deleted policy %s", name, key)
				}
			}
		}
		wantProjects := json.RawMessage(`{"/repo":{"history":["` + name + `-private"],"lastSessionId":"` + name + `"}}`)
		if !rawJSONEqual(legacy["projects"], wantProjects) {
			t.Fatalf("%s retained revoked approval or lost history: %s", name, legacy["projects"])
		}
	}
}

func TestShallowLifecyclePreflightsBothDocuments(t *testing.T) {
	for _, badDocument := range []string{"settings.json", ".claude.json"} {
		t.Run(badDocument, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, realHome := onboardingEnv(t, `{}`)
			home, err := mgr.Create("alice", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			settingsPath := filepath.Join(home, ".claude", "settings.json")
			legacyPath := filepath.Join(home, ".claude.json")
			settingsBefore := `{"model":"private-before","apiKeyHelper":"alice"}`
			legacyBefore := `{"model":"private-before","oauthAccount":{"accountUuid":"alice"}}`
			writeFile(t, settingsPath, settingsBefore)
			writeFile(t, legacyPath, legacyBefore)
			sharedSettings := filepath.Join(realHome, ".claude", "settings.json")
			sharedLegacy := filepath.Join(realHome, ".claude.json")
			writeFile(t, sharedSettings, `{"model":"new-policy"}`)
			writeFile(t, sharedLegacy, `{"model":"new-policy"}`)
			if badDocument == "settings.json" {
				writeFile(t, sharedSettings, `{"env":[]}`)
			} else {
				writeFile(t, sharedLegacy, `{"projects":{"/repo":5}}`)
			}
			if _, err := mgr.SyncClaudeConfig("alice"); err == nil {
				t.Fatal("accepted malformed canonical document")
			}
			if readFile(t, settingsPath) != settingsBefore || readFile(t, legacyPath) != legacyBefore {
				t.Fatal("preflight failure partially refreshed the profile")
			}
		})
	}
}

func TestShallowLegacyPreparationRejectsInterveningEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	mgr, _ := onboardingEnv(t, `{"permissions":{"allow":["Read"]}}`)
	home, err := mgr.Create("alice", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude.json")
	writeFile(t, path, `{"oauthAccount":{"accountUuid":"alice"}}`)
	update, _, err := mgr.prepareClaudeJSON(home, claudesettings.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	const edited = `{"oauthAccount":{"accountUuid":"alice"},"permissions":{"allow":[]}}`
	writeFile(t, path, edited)
	if err := update.Apply(); err == nil {
		t.Fatal("overwrote an edit made after launch preparation")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != edited {
		t.Fatalf("concurrent edit was not retained: %s, %v", got, err)
	}
}

func TestValidateClaudeConfigBoundaryReadOnly(t *testing.T) {
	for _, shape := range []string{"private", "missing settings", "missing state", "missing both", "missing directory", "invalid shared policy"} {
		t.Run(shape, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, realHome := onboardingEnv(t, `{}`)
			home, err := mgr.Create("alice", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			settings := filepath.Join(home, ".claude", "settings.json")
			legacy := filepath.Join(home, ".claude.json")
			writeFile(t, settings, "{\"apiKeyHelper\":\"synthetic-private-helper\", \"model\":\"private\"}\n")
			writeFile(t, legacy, "{\"oauthAccount\":{\"accountUuid\":\"synthetic-private-identity\"}}\n")
			moveAside := func(path string) {
				t.Helper()
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			switch shape {
			case "missing settings":
				moveAside(settings)
			case "missing state":
				moveAside(legacy)
			case "missing both":
				moveAside(settings)
				moveAside(legacy)
			case "missing directory":
				moveAside(filepath.Join(home, ".claude"))
			case "invalid shared policy":
				// Validation must not refresh from, or parse, the host's policy.
				writeFile(t, filepath.Join(realHome, ".claude", "settings.json"), `{broken-synthetic-host-secret`)
				writeFile(t, filepath.Join(realHome, ".claude.json"), `{broken-synthetic-host-secret`)
			}
			beforeProfile := snapshotClaudeLibraryTree(t, home)
			beforeHost := snapshotClaudeLibraryTree(t, realHome)
			originals := make(map[string]os.FileInfo)
			for _, path := range []string{settings, legacy} {
				if info, err := os.Lstat(path); err == nil {
					originals[path] = info
				}
			}
			if err := mgr.ValidateClaudeConfigBoundary("alice"); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(beforeProfile, snapshotClaudeLibraryTree(t, home)) || !maps.Equal(beforeHost, snapshotClaudeLibraryTree(t, realHome)) {
				t.Fatal("configuration validation changed a profile or host file")
			}
			for path, before := range originals {
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("configuration validation rewrote a private document")
				}
			}
		})
	}
}

func TestValidateClaudeConfigBoundaryRejectsUnsafeDocuments(t *testing.T) {
	for _, document := range []string{"settings.json", ".claude.json"} {
		for _, shape := range []string{"shared symlink", "dangling symlink", "shared hardlink", "configured hardlink", "directory", "empty", "malformed", "non-object", "invalid env"} {
			t.Run(document+"/"+shape, func(t *testing.T) {
				if runtime.GOOS == "windows" && strings.Contains(shape, "link") {
					t.Skip("link creation may require elevated Windows privileges")
				}
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				mgr, realHome := onboardingEnv(t, `{}`)
				home, err := mgr.Create("alice", CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(home, ".claude.json")
				shared := filepath.Join(realHome, ".claude.json")
				if document == "settings.json" {
					path = filepath.Join(home, ".claude", document)
					shared = filepath.Join(realHome, ".claude", document)
				}
				const secret = "synthetic-private-secret-do-not-print"
				writeFile(t, shared, `{"apiKey":"`+secret+`"}`)
				if _, err := os.Lstat(path); os.IsNotExist(err) {
					writeFile(t, path, "{}\n")
				} else if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				switch shape {
				case "shared symlink", "dangling symlink":
					target := shared
					if shape == "dangling symlink" {
						target += ".missing"
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				case "shared hardlink", "configured hardlink":
					if shape == "configured hardlink" {
						dir := filepath.Join(realHome, "selected-claude-config")
						t.Setenv("CLAUDE_CONFIG_DIR", dir)
						shared = filepath.Join(dir, document)
						writeFile(t, shared, `{"apiKey":"`+secret+`"}`)
					}
					if err := os.Link(shared, path); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "empty":
					writeFile(t, path, "")
				case "malformed":
					writeFile(t, path, `{"apiKey":"`+secret)
				case "non-object":
					writeFile(t, path, `["`+secret+`"]`)
				case "invalid env":
					writeFile(t, path, `{"apiKey":"`+secret+`","env":[]}`)
				}
				beforeProfile := snapshotClaudeLibraryTree(t, home)
				beforeHost := snapshotClaudeLibraryTree(t, realHome)
				err = mgr.ValidateClaudeConfigBoundary("alice")
				if err == nil || !strings.Contains(err.Error(), "caam shallow-spawn alice -- true") {
					t.Fatalf("unsafe configuration needs an actionable refusal: %v", err)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatal("validation error disclosed credential bytes")
				}
				if !maps.Equal(beforeProfile, snapshotClaudeLibraryTree(t, home)) || !maps.Equal(beforeHost, snapshotClaudeLibraryTree(t, realHome)) {
					t.Fatal("refused configuration validation changed a file or link")
				}
			})
		}
	}
}

func TestValidateClaudeConfigBoundaryRejectsUnsafeParents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	for _, shape := range []string{"home symlink", "config symlink", "missing config target", "config file"} {
		t.Run(shape, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, realHome := onboardingEnv(t, `{}`)
			home, err := mgr.Create("alice", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".claude")
			if shape == "home symlink" {
				path = home
			}
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			if shape == "config file" {
				writeFile(t, path, "synthetic-not-a-directory")
			} else {
				target := path + ".saved"
				if shape == "missing config target" {
					target = path + ".missing"
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			beforeProfile := snapshotClaudeLibraryTree(t, mgr.BaseDir())
			beforeHost := snapshotClaudeLibraryTree(t, realHome)
			if err := mgr.ValidateClaudeConfigBoundary("alice"); err == nil {
				t.Fatal("accepted an unsafe configuration parent")
			}
			if !maps.Equal(beforeProfile, snapshotClaudeLibraryTree(t, mgr.BaseDir())) || !maps.Equal(beforeHost, snapshotClaudeLibraryTree(t, realHome)) {
				t.Fatal("parent validation modified a profile or host path")
			}
		})
	}
}

func TestValidateClaudeConfigBoundaryDoesNotApplyToOtherProviders(t *testing.T) {
	for _, provider := range []string{"codex", "agy"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, realHome := onboardingEnv(t, `{}`)
			home, err := mgr.Create("alice", CreateOptions{Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			// Other providers intentionally keep Claude directories as shared
			// passthroughs; this validator must not apply Claude's layout to them.
			writeFile(t, filepath.Join(realHome, ".claude", "settings.json"), `{malformed`)
			before := snapshotClaudeLibraryTree(t, home)
			if err := mgr.ValidateClaudeConfigBoundary("alice"); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(before, snapshotClaudeLibraryTree(t, home)) {
				t.Fatal("validation changed another provider's layout")
			}
		})
	}
}
