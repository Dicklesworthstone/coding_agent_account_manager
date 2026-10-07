package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// Reusing a profile name must replace its authentication bundle, not accumulate
// files from every authentication mode that profile has used in the past.
func TestClaudeBackupReplacesObsoleteAuthenticationSources(t *testing.T) {
	t.Setenv("CAAM_KEYCHAIN", "0")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	files := ClaudeAuthFiles()
	v := NewVault(t.TempDir())
	credentials := filepath.Join(home, ".claude", ".credentials.json")
	settings := filepath.Join(home, ".claude", "settings.json")
	secondary := claudeFileSetPath(files, "auth.json")
	desktop := claudeDesktopConfigPath(home)
	const policy = `"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["tests"]},"hooks":{"Stop":[]},"mcpServers":{"local":{"command":"server"}}`
	authSourceTestWrite(t, credentials, keychainCreds("old-oauth"))
	authSourceTestWrite(t, secondary, `{"apiKey":"old-secondary"}`)
	authSourceTestWrite(t, settings, `{`+policy+`}`)
	authSourceTestWrite(t, desktop, `{"theme":"live","oauth:tokenCacheV2":"old-cache"}`)
	if err := v.Backup(files, "changing"); err != nil {
		t.Fatal(err)
	}
	if err := v.Backup(files, "untouched"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{credentials, secondary} {
		// Model the native CLI retiring these sources, retaining fixture data.
		if err := os.Rename(path, path+".previous"); err != nil {
			t.Fatal(err)
		}
	}
	authSourceTestWrite(t, desktop, `{"theme":"live","windowBounds":{"x":17}}`)
	for _, auth := range []string{`"apiKeyHelper":"selected-helper"`, `"env":{"ANTHROPIC_AUTH_TOKEN":"selected-env"}`} {
		authSourceTestWrite(t, settings, `{`+policy+`,`+auth+`}`)
		if err := v.Backup(files, "changing"); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".credentials.json", "auth.json", "config.json"} {
			if _, err := os.Lstat(filepath.Join(v.ProfilePath("claude", "changing"), name)); !os.IsNotExist(err) {
				t.Fatalf("obsolete %s remains in replaced profile: %v", name, err)
			}
			if _, err := os.Stat(filepath.Join(v.ProfilePath("claude", "untouched"), name)); err != nil {
				t.Fatalf("backup changed another account's %s: %v", name, err)
			}
		}
		// Round trip: resurrecting a stale vault file here would override the
		// newly selected helper/environment authentication at the next switch.
		if err := v.Restore(files, "untouched"); err != nil {
			t.Fatal(err)
		}
		if err := v.Restore(files, "changing"); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{credentials, secondary} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("restore revived an obsolete authentication source: %s, %v", path, err)
			}
		}
		got := authSourceTestObject(t, settings)
		if got["permissions"] == nil || got["autoMode"] == nil || got["mcpServers"] == nil || got["hooks"] == nil {
			t.Fatal("backup/restore discarded live workflow policy")
		}
		cache := authSourceTestObject(t, desktop)
		if cache["oauth:tokenCacheV2"] != nil || string(cache["theme"]) != `"live"` || cache["windowBounds"] == nil {
			t.Fatal("backup/restore retained obsolete Desktop auth or changed Desktop policy")
		}
	}
}

func TestClaudeBackupInvalidSourceDoesNotChangeExistingSnapshots(t *testing.T) {
	t.Setenv("CAAM_KEYCHAIN", "0")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	files := ClaudeAuthFiles()
	v := NewVault(t.TempDir())
	credentials := claudeFileSetPath(files, ".credentials.json")
	settings := claudeFileSetPath(files, "settings.json")
	desktop := claudeDesktopConfigPath(home)
	authSourceTestWrite(t, credentials, keychainCreds("original"))
	authSourceTestWrite(t, settings, `{"permissions":{"allow":["Read"]}}`)
	authSourceTestWrite(t, desktop, `{"oauth:tokenCacheV2":"original-cache"}`)
	if err := v.Backup(files, "kept"); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]string)
	entries, err := os.ReadDir(v.ProfilePath("claude", "kept"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(v.ProfilePath("claude", "kept"), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = string(data)
	}
	authSourceTestWrite(t, credentials, keychainCreds("changed"))
	authSourceTestWrite(t, settings, `{"permissions":{"allow":[]},"apiKeyHelper":"changed"}`)
	authSourceTestWrite(t, desktop, `{"oauth:tokenCacheV2":{"invalid":"synthetic-secret"}}`)
	if err := v.Backup(files, "kept"); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("bad source accepted or exposed secrets: %v", err)
	}
	for path, want := range before {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("failed backup changed a prior snapshot: %s, %v", path, err)
		}
	}
}

func TestClaudeBackupProjectsCapturedDesktopCaches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
		want     string
		invalid  bool
	}{
		{name: "empty caches", document: `{"theme":"live-policy","oauth:tokenCache":"","oauth:tokenCacheV2":""}`},
		{name: "whitespace caches", document: `{"theme":"live-policy","oauth:tokenCache":" \t ","oauth:tokenCacheV2":"\n"}`},
		{name: "valid cache with blank sibling", document: `{"theme":"live-policy","oauth:tokenCache":" ","oauth:tokenCacheV2":"synthetic-selected-cache","futureCounter":9007199254740993}`, want: `{"oauth:tokenCacheV2":"synthetic-selected-cache"}`},
		{name: "null cache", document: `{"theme":"live-policy","oauth:tokenCache":null,"oauth:tokenCacheV2":"synthetic-secret"}`, invalid: true},
		{name: "numeric cache", document: `{"theme":"live-policy","oauth:tokenCache":17,"oauth:tokenCacheV2":"synthetic-secret"}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_KEYCHAIN", "0")
			files := ClaudeAuthFiles()
			vault := NewVault(filepath.Join(t.TempDir(), "vault"))
			credentials := claudeFileSetPath(files, ".credentials.json")
			desktop := claudeDesktopConfigPath(home)
			authSourceTestWrite(t, credentials, keychainCreds("old-account"))
			authSourceTestWrite(t, desktop, `{"theme":"old-policy","oauth:tokenCacheV2":"synthetic-old-cache"}`)
			if err := vault.Backup(files, "work"); err != nil {
				t.Fatal(err)
			}
			profileDir := vault.ProfilePath("claude", "work")
			before, err := os.Stat(profileDir)
			if err != nil {
				t.Fatal(err)
			}
			previous := make(map[string]string)
			for _, name := range []string{".credentials.json", "config.json", "meta.json"} {
				previous[name] = readFixtureFile(t, filepath.Join(profileDir, name))
			}
			authSourceTestWrite(t, credentials, keychainCreds("new-account"))
			authSourceTestWrite(t, desktop, tc.document)
			err = vault.Backup(files, "work")
			if tc.invalid {
				if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatalf("malformed cache was accepted or exposed: %v", err)
				}
				after, statErr := os.Stat(profileDir)
				if statErr != nil || !os.SameFile(before, after) {
					t.Fatalf("malformed cache replaced the prior directory: %v", statErr)
				}
				for name, want := range previous {
					if got := readFixtureFile(t, filepath.Join(profileDir, name)); got != want {
						t.Fatalf("malformed cache changed previous %s", name)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				snapshot := filepath.Join(profileDir, "config.json")
				if tc.want == "" {
					if _, err := os.Lstat(snapshot); !os.IsNotExist(err) {
						t.Fatalf("blank Desktop cache survived in the new snapshot: %v", err)
					}
				} else if got := readFixtureFile(t, snapshot); got != tc.want {
					t.Fatalf("Desktop snapshot copied blank caches or policy: %s", got)
				}
				if err := vault.ValidateProfileCredentials(files, "work"); err != nil {
					t.Fatalf("new credential snapshot is unusable: %v", err)
				}
				metadata := readJSONMap(t, filepath.Join(profileDir, "meta.json"))
				retained, _ := metadata["previous_snapshot"].(string)
				if retained == "" || readFixtureFile(t, filepath.Join(retained, "config.json")) != previous["config.json"] {
					t.Fatal("previous Desktop credential is not recoverable")
				}
			}
			if got := readFixtureFile(t, desktop); got != tc.document {
				t.Fatal("backup changed live Desktop preferences or credentials")
			}
			if got := readFixtureFile(t, credentials); got != keychainCreds("new-account") {
				t.Fatal("backup changed the live Code credential")
			}
		})
	}
}

func TestClaudeBackupCapturesKeychainBeforeClassifyingMissingSources(t *testing.T) {
	f := newKeychainFixture(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	settings := filepath.Join(filepath.Dir(f.credPath), "settings.json")
	f.fileSet.Files = append(f.fileSet.Files, AuthFileSpec{Path: settings})
	authSourceTestWrite(t, settings, `{"permissions":{"allow":["Read"]}}`)
	for _, token := range []string{"first", "rotated"} {
		f.storeToken(keychainCreds(token))
		keychain.ForgetMirrors()
		if err := f.vault.Backup(f.fileSet, "keychain"); err != nil {
			t.Fatal(err)
		}
		saved := filepath.Join(f.vault.ProfilePath("claude", "keychain"), ".credentials.json")
		obj := authSourceTestObject(t, saved)
		var oauth struct {
			AccessToken string `json:"accessToken"`
		}
		if err := json.Unmarshal(obj["claudeAiOauth"], &oauth); err != nil || oauth.AccessToken != token {
			t.Fatalf("keychain-only credential missing or stale: %v", err)
		}
	}
}
