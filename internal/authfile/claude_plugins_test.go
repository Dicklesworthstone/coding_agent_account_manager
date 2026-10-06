package authfile

// Regressions for issues #55 and #115: plugin enablement and other live
// workflow policy must survive switches, while account authentication changes.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// claudeSettingsFixture builds a vault with a claude profile whose snapshot
// contains vaultSettings as settings.json, plus a live HOME whose
// .claude/settings.json contains liveSettings (skipped when empty). It returns
// the vault, the file set, and the live settings path.
func claudeSettingsFixture(t *testing.T, vaultSettings, liveSettings string) (*Vault, AuthFileSet, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	vaultDir := filepath.Join(tmp, "vault")
	home := filepath.Join(tmp, "home")

	profileDir := filepath.Join(vaultDir, "claude", "bob")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"bob-at","refreshToken":"bob-rt"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(vaultSettings), 0600); err != nil {
		t.Fatal(err)
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(claudeDir, "settings.json")
	if liveSettings != "" {
		if err := os.WriteFile(livePath, []byte(liveSettings), 0600); err != nil {
			t.Fatal(err)
		}
	}

	fileSet := AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: filepath.Join(claudeDir, ".credentials.json"), Required: true},
			{Tool: "claude", Path: livePath, Required: false},
		},
	}
	return NewVault(vaultDir), fileSet, livePath
}

func readJSONMap(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v (%q)", path, err, raw)
	}
	return m
}

func TestRestorePreservesLiveEnabledPlugins(t *testing.T) {
	// Bob's snapshot predates any plugin install. Both live plugin enablement
	// and live model choice must survive, but bob's helper must replace auth.
	vault, fileSet, livePath := claudeSettingsFixture(t,
		`{"model":"opus-bob","apiKeyHelper":"/bin/bob-helper"}`,
		`{"model":"opus-alice","enabledPlugins":{"frontend-design@official":true,"playwright@official":false}}`,
	)

	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got := readJSONMap(t, livePath)
	if got["model"] != "opus-alice" {
		t.Errorf("model = %v, want the live value", got["model"])
	}
	if got["apiKeyHelper"] != "/bin/bob-helper" {
		t.Errorf("apiKeyHelper = %v, want bob's snapshot value", got["apiKeyHelper"])
	}
	plugins, ok := got["enabledPlugins"].(map[string]interface{})
	if !ok {
		t.Fatalf("enabledPlugins missing after restore: %v", got)
	}
	if plugins["frontend-design@official"] != true || plugins["playwright@official"] != false {
		t.Errorf("enabledPlugins = %v, want the LIVE machine's map preserved", plugins)
	}
}

func TestRestoreDropsSnapshotPluginsWhenLiveHasNone(t *testing.T) {
	// Live-wins applies to absence too: if the live settings has no
	// enabledPlugins key (plugins since removed), a stale snapshot must not
	// resurrect it.
	vault, fileSet, livePath := claudeSettingsFixture(t,
		`{"model":"opus-bob","enabledPlugins":{"stale@official":true}}`,
		`{"model":"opus-alice"}`,
	)

	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got := readJSONMap(t, livePath)
	if _, exists := got["enabledPlugins"]; exists {
		t.Errorf("enabledPlugins resurrected from stale snapshot: %v", got)
	}
	if got["model"] != "opus-alice" {
		t.Errorf("model = %v, want the live value", got["model"])
	}
}

func TestRestoreSettingsVerbatimWhenNoLiveFile(t *testing.T) {
	vault, fileSet, livePath := claudeSettingsFixture(t,
		`{"model":"opus-bob","enabledPlugins":{"kept@official":true}}`,
		"", // no live settings.json
	)

	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got := readJSONMap(t, livePath)
	plugins, ok := got["enabledPlugins"].(map[string]interface{})
	if !ok || plugins["kept@official"] != true {
		t.Errorf("with no live file the snapshot bootstraps settings, got %v", got)
	}
}

func TestRestoreRejectsUnparseableLiveSettings(t *testing.T) {
	vault, fileSet, livePath := claudeSettingsFixture(t,
		`{"model":"opus-bob"}`,
		`{not json`,
	)

	if err := vault.Restore(fileSet, "bob"); err == nil {
		t.Fatal("expected malformed live settings to stop activation")
	}
	if raw, err := os.ReadFile(livePath); err != nil || string(raw) != `{not json` {
		t.Fatalf("live settings changed on failure: %q, %v", raw, err)
	}
	if _, err := os.Stat(fileSet.Files[0].Path); !os.IsNotExist(err) {
		t.Fatalf("credentials were touched before settings validation: %v", err)
	}
}

func TestRestoreRejectsUnparseableSnapshotSettings(t *testing.T) {
	const live = `{"enabledPlugins":{"x@y":true}}`
	vault, fileSet, livePath := claudeSettingsFixture(t,
		`corrupt-snapshot`, live,
	)

	if err := vault.Restore(fileSet, "bob"); err == nil {
		t.Fatal("expected malformed snapshot settings to stop activation")
	}
	if raw, err := os.ReadFile(livePath); err != nil || string(raw) != live {
		t.Fatalf("live settings changed on failure: %q, %v", raw, err)
	}
	if _, err := os.Stat(fileSet.Files[0].Path); !os.IsNotExist(err) {
		t.Fatalf("credentials were touched before settings validation: %v", err)
	}
}

func TestIsClaudeUserSettings(t *testing.T) {
	if !isClaudeUserSettings("claude", "/home/u/.claude/settings.json") {
		t.Error("expected ~/.claude/settings.json to match")
	}
	for _, tc := range []struct{ tool, path string }{
		{"gemini", "/home/u/.gemini/settings.json"},   // other tool
		{"cursor", "/home/u/.cursor/settings.json"},   // other tool
		{"claude", "/home/u/.claude/.claude.json"},    // other file
		{"claude", "/home/u/other-dir/settings.json"}, // wrong parent dir
	} {
		if isClaudeUserSettings(tc.tool, tc.path) {
			t.Errorf("isClaudeUserSettings(%q, %q) unexpectedly true", tc.tool, tc.path)
		}
	}
}

func TestHashClaudeUserSettingsIgnoresPluginDrift(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	a := write("a.json", `{"apiKeyHelper":"/bin/helper","enabledPlugins":{"x@y":true},"hooks":{"PostToolUse":[]}}`)
	b := write("b.json", `{"apiKeyHelper":"/bin/helper","enabledPlugins":{"z@w":false}}`)
	c := write("c.json", `{"apiKeyHelper":"/bin/OTHER-helper","enabledPlugins":{"x@y":true}}`)
	d := write("d.json", `{"env":{"ANTHROPIC_API_KEY":"sk-1"}}`)
	e := write("e.json", `{"env":{"ANTHROPIC_API_KEY":"sk-2"}}`)

	hash := func(p string) string {
		h, err := hashClaudeUserSettings(p)
		if err != nil {
			t.Fatalf("hash %s: %v", p, err)
		}
		return h
	}

	if hash(a) != hash(b) {
		t.Error("plugin/hook drift must not change the settings.json identity hash")
	}
	if hash(a) == hash(c) {
		t.Error("apiKeyHelper change must change the identity hash")
	}
	if hash(d) == hash(e) {
		t.Error("env change must change the identity hash")
	}
}

func TestActiveProfileSurvivesPluginDriftAPIKeyMode(t *testing.T) {
	// API-key-mode claude setups have no .credentials.json; ActiveProfile falls
	// back to optional-file matching, where settings.json identity hashing must
	// tolerate the plugin merge and everyday settings drift.
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(livePath, []byte(`{"apiKeyHelper":"/bin/helper","enabledPlugins":{"x@y":true}}`), 0600); err != nil {
		t.Fatal(err)
	}

	fileSet := AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: filepath.Join(claudeDir, ".credentials.json"), Required: true},
			{Tool: "claude", Path: livePath, Required: false},
		},
		AllowOptionalOnly: true,
	}

	vault := NewVault(filepath.Join(tmp, "vault"))
	if err := vault.Backup(fileSet, "apikey-acct"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Plugin state changes on the live machine after the backup.
	if err := os.WriteFile(livePath, []byte(`{"apiKeyHelper":"/bin/helper","enabledPlugins":{"x@y":true,"new@plugin":true}}`), 0600); err != nil {
		t.Fatal(err)
	}

	active, err := vault.ActiveProfile(fileSet)
	if err != nil {
		t.Fatalf("ActiveProfile: %v", err)
	}
	if active != "apikey-acct" {
		t.Errorf("ActiveProfile = %q, want %q (plugin drift must not break detection)", active, "apikey-acct")
	}
}
