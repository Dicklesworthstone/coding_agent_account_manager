package shallow

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		wantProjects := json.RawMessage(`{"/repo":{"allowedTools":["Read"],"history":["`+name+`-private"],"lastSessionId":"`+name+`"}}`)
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
		wantProjects := json.RawMessage(`{"/repo":{"history":["`+name+`-private"],"lastSessionId":"`+name+`"}}`)
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
