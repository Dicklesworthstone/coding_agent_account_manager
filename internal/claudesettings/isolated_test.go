package claudesettings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeIsolatedSettings(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func readIsolatedSettings(t *testing.T, path string) map[string]interface{} {
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

func TestIsolatedSettingsMultiProfileLifecycle(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "real", "settings.json")
	policy := Policy{SharedEnvKeys: []string{"EDITOR"}}
	profiles := make(map[string][]string)
	for _, name := range []string{"alpha", "beta", "oauth"} {
		paths := []string{filepath.Join(root, name, "legacy", "settings.json"), filepath.Join(root, name, "xdg", "settings.json")}
		profiles[name] = paths
		for _, path := range paths {
			account := `{"apiKeyHelper":"` + name + `","env":{"ANTHROPIC_AUTH_TOKEN":"` + name + `"},"permissions":{"allow":["stale"]},"hooks":{"stale":[]}}`
			if name == "oauth" {
				account = `{}`
			}
			writeIsolatedSettings(t, path, account)
		}
	}
	for _, live := range []string{
		`{"apiKeyHelper":"real-secret-helper","env":{"ANTHROPIC_AUTH_TOKEN":"real-secret","EDITOR":"vim"},"permissions":{"deny":["Bash(rm *)"]},"autoMode":{"enabled":true},"mcpServers":{"local":{"command":"mcp"}},"hooks":{"PreToolUse":[]},"model":"opus","effortLevel":"high","enabledPlugins":{"example":true}}`,
		`{"permissions":{"allow":["Read"]},"autoMode":{"enabled":false},"env":{"EDITOR":"nano"}}`,
		`{}`,
	} {
		writeIsolatedSettings(t, shared, live)
		var want map[string]interface{}
		if err := json.Unmarshal([]byte(live), &want); err != nil {
			t.Fatal(err)
		}
		for name, paths := range profiles {
			updates, err := PrepareIsolatedSettings(shared, paths, policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyUpdates(updates); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				got := readIsolatedSettings(t, path)
				for _, key := range []string{"permissions", "autoMode", "mcpServers", "hooks", "model", "effortLevel", "enabledPlugins"} {
					if !reflect.DeepEqual(got[key], want[key]) {
						t.Fatalf("%s: %s = %#v, want %#v", name, key, got[key], want[key])
					}
				}
				env, _ := got["env"].(map[string]interface{})
				if name == "oauth" {
					if got["apiKeyHelper"] != nil || env["ANTHROPIC_AUTH_TOKEN"] != nil {
						t.Fatalf("OAuth profile inherited settings auth: %#v", got)
					}
				} else if got["apiKeyHelper"] != name || env["ANTHROPIC_AUTH_TOKEN"] != name {
					t.Fatalf("%s lost private auth: %#v", name, got)
				}
				wantEnv, _ := want["env"].(map[string]interface{})
				if env["EDITOR"] != wantEnv["EDITOR"] {
					t.Fatalf("%s: shared environment drifted", name)
				}
			}
		}
		unchanged, err := os.ReadFile(shared)
		if err != nil || string(unchanged) != live {
			t.Fatalf("refresh modified the real settings: %v", err)
		}
	}
}

func TestIsolatedSettingsScopesAndMissingLocations(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "real", "settings.json")
	legacy := filepath.Join(root, "legacy", "settings.json")
	xdg := filepath.Join(root, "xdg", "settings.json")
	writeIsolatedSettings(t, shared, `{"apiKeyHelper":"real","hooks":{"shared":[]},"mcpServers":{"shared":{}},"model":"live"}`)
	writeIsolatedSettings(t, legacy, `{"apiKeyHelper":"private","hooks":{"private":[]},"mcpServers":{"private":{}},"model":"saved"}`)
	p := Policy{ProfileKeys: []string{"hooks", "mcpServers"}}
	updates, err := PrepareIsolatedSettings(shared, []string{legacy, xdg}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates(updates); err != nil {
		t.Fatal(err)
	}
	got := readIsolatedSettings(t, xdg)
	if got["apiKeyHelper"] != nil || got["model"] != "live" || got["hooks"] != nil || got["mcpServers"] != nil {
		t.Fatalf("missing XDG settings must receive policy without introducing another auth store: %#v", got)
	}
	selected := readIsolatedSettings(t, legacy)
	if selected["apiKeyHelper"] != "private" || selected["hooks"].(map[string]interface{})["private"] == nil {
		t.Fatalf("selected legacy settings lost account-scoped fields: %#v", selected)
	}
	if _, ok := selected["mcpServers"].(map[string]interface{})["private"]; !ok {
		t.Fatal("selected legacy settings lost profile-scoped MCP settings")
	}
	writeIsolatedSettings(t, xdg, `{}`)
	updates, err = PrepareIsolatedSettings(shared, []string{legacy, xdg}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates(updates); err != nil {
		t.Fatal(err)
	}
	if got := readIsolatedSettings(t, xdg); got["apiKeyHelper"] != nil || got["hooks"] != nil {
		t.Fatalf("explicitly empty settings resurrected account fields: %#v", got)
	}
	p.Mode = "per-profile"
	writeIsolatedSettings(t, shared, `{"model":"new-global-model"}`)
	updates, err = PrepareIsolatedSettings(shared, []string{legacy}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates(updates); err != nil {
		t.Fatal(err)
	}
	if got := readIsolatedSettings(t, legacy); got["apiKeyHelper"] != "private" || got["model"] != "live" {
		t.Fatalf("per-profile refresh changed the existing document: %#v", got)
	}
}

func TestIsolatedSettingsPreflightAndConcurrentEdit(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "real", "settings.json")
	first := filepath.Join(root, "legacy", "settings.json")
	second := filepath.Join(root, "xdg", "settings.json")
	writeIsolatedSettings(t, shared, `{"model":"live"}`)
	writeIsolatedSettings(t, first, `{"apiKeyHelper":"private"}`)
	writeIsolatedSettings(t, second, `broken`)
	before, _ := os.ReadFile(first)
	if _, err := PrepareIsolatedSettings(shared, []string{first, second}, Policy{}); err == nil {
		t.Fatal("corrupt second destination was accepted")
	}
	after, _ := os.ReadFile(first)
	if !bytes.Equal(before, after) {
		t.Fatal("preflight failure changed the first destination")
	}
	writeIsolatedSettings(t, second, `{}`)
	updates, err := PrepareIsolatedSettings(shared, []string{first, second}, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	writeIsolatedSettings(t, second, `{"permissions":{"deny":["Bash"]}}`)
	if err := ApplyUpdates(updates); err == nil {
		t.Fatal("concurrent second-file edit was overwritten")
	}
	after, _ = os.ReadFile(first)
	if !bytes.Equal(before, after) {
		t.Fatal("batch preflight failed to protect the first destination")
	}
}

func TestIsolatedSettingsRejectsSharedAliases(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			shared := filepath.Join(root, "real", "settings.json")
			path := filepath.Join(root, "profile", "settings.json")
			writeIsolatedSettings(t, shared, `{"apiKeyHelper":"real-secret"}`)
			if kind != "directory-symlink" {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(shared, path)
			case "hardlink":
				err = os.Link(shared, path)
			default:
				err = os.Symlink(filepath.Dir(shared), filepath.Dir(path))
			}
			if err != nil {
				t.Skipf("link unsupported: %v", err)
			}
			if _, err := PrepareIsolatedSettings(shared, []string{path}, Policy{}); err == nil {
				t.Fatal("shared auth alias was accepted as private profile auth")
			}
		})
	}
}

func TestSharedSettingsPathOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := SharedPath(home); got != filepath.Join(home, ".claude", "settings.json") {
		t.Fatal(got)
	}
	custom := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", custom)
	if got := SharedPath(home); got != filepath.Join(custom, "settings.json") {
		t.Fatal(got)
	}
}
