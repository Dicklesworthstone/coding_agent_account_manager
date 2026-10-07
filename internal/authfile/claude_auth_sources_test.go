package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

func authSourceTestWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func authSourceTestObject(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func TestClaudeAuthSourceTransitionsPreservePolicyWithoutOutgoingFallbacks(t *testing.T) {
	for _, entry := range []string{"restore", "switch"} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("CAAM_KEYCHAIN", "0")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			home := t.TempDir()
			credentials := filepath.Join(home, ".claude", ".credentials.json")
			secondary := filepath.Join(home, ".config", "claude-code", "auth.json")
			settings := filepath.Join(home, ".claude", "settings.json")
			legacy := filepath.Join(home, ".claude.json")
			desktop := claudeDesktopConfigPath(home)
			files := AuthFileSet{Tool: "claude", AllowOptionalOnly: true, Files: []AuthFileSpec{
				{Path: credentials, Required: true}, {Path: legacy}, {Path: secondary}, {Path: settings}, {Path: desktop},
			}}
			v := NewVault(t.TempDir())
			const policy = `"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["tests"]},"mcpServers":{"current":{"command":"server"}},"hooks":{"Stop":[]},"model":"latest","effortLevel":"high"`
			authSourceTestWrite(t, credentials, keychainCreds("outgoing"))
			authSourceTestWrite(t, secondary, `{"apiKey":"outgoing-secondary"}`)
			authSourceTestWrite(t, settings, `{`+policy+`,"apiKeyHelper":"outgoing-helper"}`)
			authSourceTestWrite(t, legacy, `{`+policy+`,"oauthAccount":{"accountUuid":"outgoing"}}`)
			authSourceTestWrite(t, desktop, `{"theme":"live","windowBounds":{"x":7},"oauth:tokenCache":"outgoing-v1","oauth:tokenCacheV2":"outgoing-v2"}`)
			expectedPolicy := authSourceTestObject(t, settings)
			delete(expectedPolicy, "apiKeyHelper")
			for name, body := range map[string]string{
				"helper": `{"apiKeyHelper":"selected-helper","model":"stale"}`,
				"env":    `{"env":{"ANTHROPIC_AUTH_TOKEN":"selected-env"},"model":"stale"}`,
				"oauth":  `{"model":"stale"}`,
			} {
				authSourceTestWrite(t, filepath.Join(v.ProfilePath("claude", name), "settings.json"), body)
			}
			oauthDir := v.ProfilePath("claude", "oauth")
			authSourceTestWrite(t, filepath.Join(oauthDir, ".credentials.json"), keychainCreds("selected-oauth"))
			authSourceTestWrite(t, filepath.Join(oauthDir, ".claude.json"), `{"oauthAccount":{"accountUuid":"selected-oauth"}}`)
			authSourceTestWrite(t, filepath.Join(oauthDir, "config.json"), `{"theme":"stale","oauth:tokenCacheV2":"selected-cache"}`)

			for _, name := range []string{"helper", "env", "oauth", "helper"} {
				var err error
				if entry == "restore" {
					err = v.Restore(files, name)
				} else {
					_, err = v.Switch(files, name, SwitchOptions{})
				}
				if err != nil {
					t.Fatalf("%s %s: %v", entry, name, err)
				}
				got := authSourceTestObject(t, settings)
				for key, expected := range expectedPolicy {
					var actualValue, expectedValue any
					if err := json.Unmarshal(got[key], &actualValue); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(expected, &expectedValue); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(actualValue, expectedValue) {
						t.Fatalf("%s lost live %s policy", name, key)
					}
				}
				if name == "helper" {
					if string(got["apiKeyHelper"]) != `"selected-helper"` || got["env"] != nil {
						t.Fatalf("helper identity mismatch: %s", got)
					}
				} else if got["apiKeyHelper"] != nil {
					t.Fatal("outgoing helper survived an account-source transition")
				}
				if name == "env" {
					if !strings.Contains(string(got["env"]), "selected-env") {
						t.Fatal("target environment authentication was not installed")
					}
				} else if got["env"] != nil {
					t.Fatal("outgoing environment authentication survived")
				}
				if name == "oauth" {
					if data, err := os.ReadFile(credentials); err != nil || string(data) != keychainCreds("selected-oauth") {
						t.Fatalf("selected OAuth credential was not restored: %v", err)
					}
				} else if _, err := os.Stat(credentials); !os.IsNotExist(err) {
					t.Fatalf("outgoing OAuth credential survived %s: %v", name, err)
				}
				if _, err := os.Stat(secondary); !os.IsNotExist(err) {
					t.Fatalf("outgoing secondary auth survived %s: %v", name, err)
				}
				cache := authSourceTestObject(t, desktop)
				if string(cache["theme"]) != `"live"` || cache["windowBounds"] == nil || cache["oauth:tokenCache"] != nil {
					t.Fatalf("Desktop policy reverted or obsolete cache retained: %s", cache)
				}
				if name == "oauth" {
					if string(cache["oauth:tokenCacheV2"]) != `"selected-cache"` {
						t.Fatal("selected Desktop cache was not restored")
					}
				} else if cache["oauth:tokenCacheV2"] != nil {
					t.Fatal("outgoing Desktop token cache survived helper/env activation")
				}
			}
		})
	}
}

func TestClaudeHelperRestoreRetiresKeychainWithoutRehydratingOutgoingAuth(t *testing.T) {
	for _, diskMirror := range []bool{false, true} {
		t.Run(map[bool]string{false: "keychain-only", true: "stale-disk-mirror"}[diskMirror], func(t *testing.T) {
			f := newKeychainFixture(t)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			settings := filepath.Join(filepath.Dir(f.credPath), "settings.json")
			f.fileSet.Files = append(f.fileSet.Files, AuthFileSpec{Path: settings})
			f.storeToken(keychainCreds("outgoing-keychain"))
			if diskMirror {
				authSourceTestWrite(t, f.credPath, keychainCreds("stale-disk"))
			}
			authSourceTestWrite(t, f.statePath, keychainState("outgoing@example.invalid"))
			authSourceTestWrite(t, settings, `{"permissions":{"allow":[]}}`)
			authSourceTestWrite(t, filepath.Join(f.vault.ProfilePath("claude", "helper"), "settings.json"), `{"apiKeyHelper":"selected-helper"}`)
			keychain.ForgetMirrors()
			if err := f.vault.Restore(f.fileSet, "helper"); err != nil {
				t.Fatal(err)
			}
			if _, ok := f.storedToken(); ok {
				t.Fatal("helper activation retained the outgoing login-keychain item")
			}
			if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
				t.Fatalf("helper activation retained the OAuth mirror: %v", err)
			}
			if err := pullClaudeKeychain(f.fileSet); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
				t.Fatal("a later keychain read resurrected outgoing authentication")
			}
		})
	}
}
