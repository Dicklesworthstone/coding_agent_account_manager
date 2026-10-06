package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

func writeLifecycleFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func readLifecycleObject(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestPrepareRunClaudeSettingsAcrossProfiles(t *testing.T) {
	home := fakeRealHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	livePath := filepath.Join(home, ".claude", "settings.json")
	writeLifecycleFile(t, livePath, `{"apiKeyHelper":"real-helper","env":{"ANTHROPIC_AUTH_TOKEN":"real-token"},"permissions":{"deny":["Bash(rm *)"]},"autoMode":{"enabled":true},"hooks":{"PreToolUse":[]},"model":"opus","effortLevel":"high"}`)
	writeLifecycleFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"real@example.test"},"mcpServers":{"local":{"command":"mcp"}},"projects":{"/work":{"allowedTools":["Read"]}}}`)
	var profiles []*profile.Profile
	for _, name := range []string{"alpha", "beta", "oauth"} {
		prof := &profile.Profile{Name: name, Provider: "claude", BasePath: t.TempDir()}
		profiles = append(profiles, prof)
		if name != "oauth" {
			for _, path := range claudeSettingsPathsForProfile(prof) {
				writeLifecycleFile(t, path, `{"apiKeyHelper":"`+name+`","env":{"ANTHROPIC_AUTH_TOKEN":"`+name+`"},"permissions":{"allow":["stale"]}}`)
			}
		}
		writeLifecycleFile(t, filepath.Join(prof.HomePath(), ".claude.json"), `{"oauthAccount":{"emailAddress":"`+name+`@example.test"},"mcpServers":{"stale":{}}}`)
		writeLifecycleFile(t, filepath.Join(prof.HomePath(), ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"`+name+`"}}`)
		if err := New().PrepareRun(context.Background(), prof); err != nil {
			t.Fatal(err)
		}
		for _, path := range claudeSettingsPathsForProfile(prof) {
			got := readLifecycleObject(t, path)
			live := readLifecycleObject(t, livePath)
			for _, key := range []string{"permissions", "autoMode", "hooks", "model", "effortLevel"} {
				if !reflect.DeepEqual(got[key], live[key]) {
					t.Fatalf("%s %s drifted", name, key)
				}
			}
			if name == "oauth" {
				if got["apiKeyHelper"] != nil || got["env"] != nil {
					t.Fatalf("new OAuth profile inherited real auth: %#v", got)
				}
			} else if got["apiKeyHelper"] != name || got["env"].(map[string]interface{})["ANTHROPIC_AUTH_TOKEN"] != name {
				t.Fatalf("%s auth changed: %#v", name, got)
			}
		}
		legacy := readLifecycleObject(t, filepath.Join(prof.HomePath(), ".claude.json"))
		if legacy["oauthAccount"].(map[string]interface{})["emailAddress"] != name+"@example.test" {
			t.Fatal("legacy account identity changed")
		}
		if !reflect.DeepEqual(legacy["mcpServers"], readLifecycleObject(t, filepath.Join(home, ".claude.json"))["mcpServers"]) {
			t.Fatal("user MCP configuration not refreshed")
		}
	}
	writeLifecycleFile(t, livePath, `{}`)
	writeLifecycleFile(t, filepath.Join(home, ".claude.json"), `{}`)
	for _, prof := range profiles {
		if err := New().PrepareRun(context.Background(), prof); err != nil {
			t.Fatal(err)
		}
		for _, path := range claudeSettingsPathsForProfile(prof) {
			got := readLifecycleObject(t, path)
			for _, key := range []string{"permissions", "autoMode", "hooks", "model", "effortLevel"} {
				if got[key] != nil {
					t.Fatalf("deleted %s resurrected for %s", key, prof.Name)
				}
			}
		}
		legacy := readLifecycleObject(t, filepath.Join(prof.HomePath(), ".claude.json"))
		if legacy["mcpServers"] != nil || legacy["projects"] != nil {
			t.Fatal("deleted MCP/project policy resurrected")
		}
		creds := readLifecycleObject(t, filepath.Join(prof.HomePath(), ".claude", ".credentials.json"))
		if creds["claudeAiOauth"].(map[string]interface{})["accessToken"] != prof.Name {
			t.Fatal("profile credentials changed")
		}
	}
}

func TestPrepareRunReloadsPolicyAndPreflightsEveryFile(t *testing.T) {
	home := fakeRealHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	prof := &profile.Profile{Name: "alpha", Provider: "claude", BasePath: t.TempDir()}
	paths := claudeSettingsPathsForProfile(prof)
	writeLifecycleFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"live","hooks":{"shared":[]}}`)
	for _, path := range paths {
		writeLifecycleFile(t, path, `{"model":"private","hooks":{"private":[]}}`)
	}
	writeLifecycleFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"mode":"per-profile"}}`)
	if err := New().PrepareRun(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	if got := readLifecycleObject(t, paths[0]); got["model"] != "private" {
		t.Fatal("per-profile policy ignored")
	}
	writeLifecycleFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"profile_keys":["hooks"]}}`)
	if err := New().PrepareRun(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	if got := readLifecycleObject(t, paths[0]); got["model"] != "live" || got["hooks"].(map[string]interface{})["private"] == nil {
		t.Fatalf("updated isolation policy ignored: %#v", got)
	}
	before, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	writeLifecycleFile(t, paths[1], `not JSON`)
	if err := New().PrepareRun(context.Background(), prof); err == nil {
		t.Fatal("malformed second settings file accepted")
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("first settings changed before preflight completed: %v", err)
	}
	// Reading environment for inspection must not write settings or run preflight.
	if _, err := New().Env(context.Background(), prof); err != nil {
		t.Fatalf("read-only Env unexpectedly tried to apply policy: %v", err)
	}
}
