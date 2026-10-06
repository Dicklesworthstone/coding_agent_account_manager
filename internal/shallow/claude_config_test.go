package shallow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

// Regression coverage for the .claude.json key policy: seeding an empty
// profile must not inherit the real account's identity/usage (issue #92), and
// the on-spawn refresh must copy only shared preferences, never identity or
// session state (issue #93).

// realClaudeJSONFixture is a realistic real-HOME .claude.json: identity and
// usage caches, preferences, and a project with approvals plus session state.
const realClaudeJSONFixture = `{
  "oauthAccount": {"accountUuid": "uuid-real", "emailAddress": "real@example.com", "organizationName": "Real Org"},
  "userID": "install-id",
  "cachedUsageUtilization": {"five_hour": 0.9},
  "modelAccessCache": ["claude-x"],
  "orgModelDefaultCache": "claude-x",
  "passesEligibilityCache": {"uuid-real": true},
  "passesLastSeenRemaining": 3,
  "cachedExtraUsageDisabledReason": "none",
  "hasCompletedOnboarding": true,
  "theme": "dark",
  "editorMode": "vim",
  "preferredNotifChannel": "iterm2",
  "mcpServers": {"global": {"command": "srv"}},
  "numStartups": 42,
  "projects": {
    "/work/app": {
      "allowedTools": ["Bash(go test:*)"],
      "hasTrustDialogAccepted": true,
      "mcpServers": {"proj": {"command": "psrv"}},
      "history": [{"display": "real prompt"}],
      "lastCost": 1.5
    }
  }
}`

func parseJSONObject(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse: %v\n%s", err, raw)
	}
	return m
}

func readProfileClaudeJSON(t *testing.T, home string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	return parseJSONObject(t, raw)
}

func TestCreateEmptyProfileStripsRealAccountKeys(t *testing.T) {
	mgr, _ := onboardingEnv(t, realClaudeJSONFixture)
	home, err := mgr.Create("fresh", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	got := readProfileClaudeJSON(t, home)
	for _, k := range claudeAccountKeys {
		if _, ok := got[k]; ok {
			t.Errorf("seeded .claude.json still carries account key %q", k)
		}
	}
	for _, k := range []string{"userID", "hasCompletedOnboarding", "theme", "editorMode", "mcpServers", "projects", "numStartups"} {
		if _, ok := got[k]; !ok {
			t.Errorf("seeded .claude.json lost shared key %q", k)
		}
	}
	if !strings.Contains(string(got["projects"]), "real prompt") {
		t.Errorf("project state (trust, tools, history) should be seeded for a fresh profile: %s", got["projects"])
	}
	// The real file is untouched.
	realRaw, err := os.ReadFile(filepath.Join(mgr.RealHome(), ".claude.json"))
	if err != nil || string(realRaw) != realClaudeJSONFixture {
		t.Fatalf("real ~/.claude.json was modified (err=%v)", err)
	}
}

// --from-file with a foreign credential and no --from-claude-json seeds from
// the real HOME too; it must not label those credentials with the real
// account either.
func TestCreateFromFileStripsRealAccountKeys(t *testing.T) {
	mgr, _ := onboardingEnv(t, realClaudeJSONFixture)
	cred := writeTempFile(t, "creds.json", validClaudeCred)
	home, err := mgr.Create("other", CreateOptions{Provider: "claude", CredentialSource: cred})
	if err != nil {
		t.Fatal(err)
	}
	got := readProfileClaudeJSON(t, home)
	if _, ok := got["oauthAccount"]; ok {
		t.Errorf("--from-file seed must not carry the real oauthAccount")
	}
	if string(got["hasCompletedOnboarding"]) != "true" {
		t.Errorf("hasCompletedOnboarding = %s, want true", got["hasCompletedOnboarding"])
	}
}

// An explicit source is the identity's home: copied verbatim.
func TestCreateExplicitSourceKeepsIdentityVerbatim(t *testing.T) {
	mgr, _ := onboardingEnv(t, realClaudeJSONFixture)
	src := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(src, []byte(`{"oauthAccount":{"emailAddress":"snap@example.com"},"hasCompletedOnboarding":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cred := writeTempFile(t, "creds.json", validClaudeCred)
	home, err := mgr.Create("snap", CreateOptions{Provider: "claude", CredentialSource: cred, SourceClaudeJSON: src})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "snap@example.com") {
		t.Fatalf("explicit source identity dropped: %s", raw)
	}
}

// A real .claude.json that is not a JSON object cannot carry an identity and
// is passed through unchanged.
func TestSeedNonObjectRealClaudeJSONIsVerbatim(t *testing.T) {
	mgr, _ := onboardingEnv(t, "[1, 2, 3]")
	home, err := mgr.Create("weird", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil || string(raw) != "[1, 2, 3]" {
		t.Fatalf("got %q (err=%v), want verbatim seed", raw, err)
	}
}

func TestAccountAndSharedKeySetsAreDisjoint(t *testing.T) {
	account := map[string]bool{}
	for _, k := range claudeAccountKeys {
		account[k] = true
	}
	for _, k := range claudeSharedPreferenceKeys {
		if account[k] {
			t.Errorf("%q is both an account key and a shared preference", k)
		}
	}
	if account["projects"] {
		t.Error("projects must not be an account key: its shared sub-keys are refreshed on spawn")
	}
}

func TestSyncClaudeConfigRefreshesSharedKeysOnly(t *testing.T) {
	mgr, realHome := onboardingEnv(t, realClaudeJSONFixture)
	cred := writeTempFile(t, "creds.json", validClaudeCred)
	home, err := mgr.Create("alice", CreateOptions{Provider: "claude", CredentialSource: cred})
	if err != nil {
		t.Fatal(err)
	}

	// The profile logs in as its own account, accumulates its own session
	// state, and the operator picks a different theme inside it.
	profile := readProfileClaudeJSON(t, home)
	profile["oauthAccount"] = json.RawMessage(`{"accountUuid":"uuid-alice","emailAddress":"alice@example.com"}`)
	profile["cachedUsageUtilization"] = json.RawMessage(`{"five_hour":0.1}`)
	profile["theme"] = json.RawMessage(`"light"`)
	profile["numStartups"] = json.RawMessage(`7`)
	profile["projects"] = json.RawMessage(`{
	  "/work/app": {"allowedTools": [], "history": [{"display": "alice prompt"}], "lastCost": 9},
	  "/work/alice-only": {"hasTrustDialogAccepted": true, "history": [{"display": "private"}]}
	}`)
	out, _ := marshalClaudeJSON(profile)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), out, 0o600); err != nil {
		t.Fatal(err)
	}

	// Meanwhile the real lane changed a preference and approved more tools.
	real := parseJSONObject(t, []byte(realClaudeJSONFixture))
	real["editorMode"] = json.RawMessage(`"emacs"`)
	real["projects"] = json.RawMessage(`{
	  "/work/app": {"allowedTools": ["Bash(go test:*)", "Bash(make:*)"], "hasTrustDialogAccepted": true, "history": [{"display": "real prompt"}], "lastCost": 1.5},
	  "/work/new": {"hasTrustDialogAccepted": true, "history": [{"display": "real new"}]}
	}`)
	realOut, _ := marshalClaudeJSON(real)
	if err := os.WriteFile(filepath.Join(realHome, ".claude.json"), realOut, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := mgr.SyncClaudeConfig("alice")
	if err != nil {
		t.Fatalf("SyncClaudeConfig: %v", err)
	}
	want := []string{
		"editorMode",
		"projects./work/alice-only.hasTrustDialogAccepted",
		"projects./work/app.allowedTools",
		"projects./work/app.hasTrustDialogAccepted",
		"projects./work/new.hasTrustDialogAccepted",
		"theme",
	}
	if strings.Join(changed, ",") != strings.Join(want, ",") {
		t.Fatalf("changed = %v, want %v", changed, want)
	}

	got := readProfileClaudeJSON(t, home)
	// Shared preferences follow the real HOME.
	if string(got["theme"]) != `"dark"` || string(got["editorMode"]) != `"emacs"` {
		t.Errorf("preferences not refreshed: theme=%s editorMode=%s", got["theme"], got["editorMode"])
	}
	// Identity, usage and other state stay the profile's own.
	if !strings.Contains(string(got["oauthAccount"]), "alice@example.com") {
		t.Errorf("identity clobbered: %s", got["oauthAccount"])
	}
	if !rawJSONEqual(got["cachedUsageUtilization"], json.RawMessage(`{"five_hour":0.1}`)) || string(got["numStartups"]) != "7" {
		t.Errorf("profile state clobbered: usage=%s numStartups=%s", got["cachedUsageUtilization"], got["numStartups"])
	}
	var projects map[string]map[string]json.RawMessage
	if err := json.Unmarshal(got["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	app := projects["/work/app"]
	if !strings.Contains(string(app["allowedTools"]), "make") || string(app["hasTrustDialogAccepted"]) != "true" {
		t.Errorf("project approvals not refreshed: %v", app)
	}
	if !strings.Contains(string(app["history"]), "alice prompt") || string(app["lastCost"]) != "9" {
		t.Errorf("project session state clobbered: history=%s lastCost=%s", app["history"], app["lastCost"])
	}
	if _, ok := projects["/work/alice-only"]; !ok {
		t.Error("profile-only project dropped")
	}
	if _, ok := projects["/work/alice-only"]["hasTrustDialogAccepted"]; ok {
		t.Error("profile-only project retained a shared approval absent from the real home")
	}
	newProj := projects["/work/new"]
	if string(newProj["hasTrustDialogAccepted"]) != "true" {
		t.Errorf("new real project approvals not carried: %v", newProj)
	}
	if _, ok := newProj["history"]; ok {
		t.Errorf("real lane's history leaked into the profile: %s", newProj["history"])
	}

	// A second sync is a no-op that does not rewrite the file.
	before, _ := os.Stat(filepath.Join(home, ".claude.json"))
	changed, err = mgr.SyncClaudeConfig("alice")
	if err != nil || len(changed) != 0 {
		t.Fatalf("second sync: changed=%v err=%v, want no-op", changed, err)
	}
	after, _ := os.Stat(filepath.Join(home, ".claude.json"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("no-op sync rewrote the file")
	}
}

func TestSyncClaudeConfigRemovesDeletedSharedPolicy(t *testing.T) {
	for name, real := range map[string]string{
		"removed fields":   `{"projects":{"/work/app":{"history":["host history"]}}}`,
		"removed project":  `{"projects":{"/work/other":{}}}`,
		"empty projects":   `{"projects":{}}`,
		"null projects":    `{"projects":null}`,
		"removed projects": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			mgr, realHome := onboardingEnv(t, real)
			home, err := mgr.Create("alice", CreateOptions{Provider: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			const original = `{"oauthAccount":{"emailAddress":"alice@example.com"},"cachedUsageUtilization":{"five_hour":0.2},"mcpServers":{"stale":{"command":"old"}},"theme":"light","projects":{"/work/app":{"allowedTools":["Bash(*)"],"hasTrustDialogAccepted":true,"mcpServers":{"stale":{"command":"old"}},"history":["private"],"lastCost":9},"/work/private":{"allowedTools":["Read(*)"],"lastSessionId":"private-session"}}}`
			path := filepath.Join(home, ".claude.json")
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			changed, err := mgr.SyncClaudeConfig("alice")
			if err != nil {
				t.Fatal(err)
			}
			wantChanged := "mcpServers,projects./work/app.allowedTools,projects./work/app.hasTrustDialogAccepted,projects./work/app.mcpServers,projects./work/private.allowedTools,theme"
			if strings.Join(changed, ",") != wantChanged {
				t.Fatalf("changed = %v, want %s", changed, wantChanged)
			}
			got := readProfileClaudeJSON(t, home)
			want := parseJSONObject(t, []byte(`{"oauthAccount":{"emailAddress":"alice@example.com"},"cachedUsageUtilization":{"five_hour":0.2},"projects":{"/work/app":{"history":["private"],"lastCost":9},"/work/private":{"lastSessionId":"private-session"}}}`))
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("shared deletion changed private state or retained stale policy: %s", gotJSON)
			}
			if changed, err := mgr.SyncClaudeConfig("alice"); err != nil || len(changed) != 0 {
				t.Fatalf("repeat sync = %v, %v; want no-op", changed, err)
			}
			if got, err := os.ReadFile(filepath.Join(realHome, ".claude.json")); err != nil || string(got) != real {
				t.Fatalf("canonical state was modified: %s, %v", got, err)
			}
		})
	}
}

func TestSyncClaudeConfigDeletionHonorsProfilePolicy(t *testing.T) {
	for name, policy := range map[string]claudesettings.Policy{
		"per profile":  {Mode: "per-profile"},
		"profile keys": {ProfileKeys: []string{"mcpServers", "projects"}},
	} {
		t.Run(name, func(t *testing.T) {
			mgr, _ := onboardingEnv(t, `{}`)
			home, err := mgr.Create("alice", CreateOptions{Provider: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			const original = `{"mcpServers":{"private":{}},"projects":{"/work/app":{"allowedTools":["Read(*)"],"history":["private"]}},"oauthAccount":{"emailAddress":"alice@example.com"}}`
			path := filepath.Join(home, ".claude.json")
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if changed, err := mgr.syncClaudeJSON(home, policy); err != nil || len(changed) != 0 {
				t.Fatalf("profile override changed: %v, %v", changed, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != original {
				t.Fatalf("profile override was rewritten: %s, %v", got, err)
			}
		})
	}
}

func TestSyncClaudeConfigNoRealFileIsNoop(t *testing.T) {
	mgr, _ := onboardingEnv(t, "")
	home, err := mgr.Create("p", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	const original = `{"mcpServers":{"private":{}},"projects":{"/work/app":{"allowedTools":["Read(*)"],"history":["private"]}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := mgr.SyncClaudeConfig("p")
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed=%v err=%v, want no-op", changed, err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if string(raw) != original {
		t.Fatalf("profile rewritten without a canonical file: %q", raw)
	}
}

func TestSyncClaudeConfigSkipsNonClaudeProfiles(t *testing.T) {
	mgr, realHome := onboardingEnv(t, realClaudeJSONFixture)
	if err := os.WriteFile(filepath.Join(realHome, ".codex-marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := mgr.Create("cx", CreateOptions{Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := mgr.SyncClaudeConfig("cx")
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed=%v err=%v, want no-op for codex", changed, err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("codex profile must not gain a .claude.json (err=%v)", err)
	}
}

// A malformed profile file is reported, never clobbered; a symlinked one is
// refused so the refresh can never write through to the real HOME.
func TestSyncClaudeConfigRefusesMalformedOrSymlinkedProfileFile(t *testing.T) {
	mgr, realHome := onboardingEnv(t, realClaudeJSONFixture)
	home, err := mgr.Create("p", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".claude.json")
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(realHome, ".claude.json")
	if err := os.WriteFile(realPath, []byte(`{"theme":"new","projects":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SyncClaudeConfig("p"); err == nil || !strings.Contains(err.Error(), "projects") {
		t.Fatalf("malformed canonical projects must fail before deleting policy: %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(before) {
		t.Fatalf("malformed canonical projects changed profile: %s, %v", got, err)
	}
	if err := os.WriteFile(realPath, []byte(realClaudeJSONFixture), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(target, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SyncClaudeConfig("p"); err == nil {
		t.Fatal("expected an error for a malformed profile .claude.json")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != "{not json" {
		t.Fatalf("malformed file was rewritten: %q", raw)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realHome, ".claude.json"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SyncClaudeConfig("p"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	realRaw, _ := os.ReadFile(filepath.Join(realHome, ".claude.json"))
	if string(realRaw) != realClaudeJSONFixture {
		t.Fatal("real ~/.claude.json was modified through the symlink")
	}
}

func TestShallowSettingsRefreshPreservesSelectedAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	mgr, realHome := onboardingEnv(t, `{}`)
	shared := filepath.Join(realHome, ".claude", "settings.json")
	writeFile(t, shared, `{"permissions":{"allow":["Read"]},"hooks":{"Stop":[]},"apiKeyHelper":"host-helper","env":{"ANTHROPIC_API_KEY":"synthetic-host"}}`)
	fresh, err := mgr.Create("fresh", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	freshSettings := parseJSONObject(t, []byte(readFile(t, filepath.Join(fresh, ".claude", "settings.json"))))
	if freshSettings["apiKeyHelper"] != nil || freshSettings["env"] != nil || freshSettings["permissions"] == nil {
		t.Fatalf("new profile did not separate shared policy from host auth: %v", freshSettings)
	}
	accountBody := `{"permissions":{"allow":["Bash(old)"]},"apiKeyHelper":"alice-helper","env":{"ANTHROPIC_API_KEY":"synthetic-alice"}}`
	account := writeTempFile(t, "alice-settings.json", accountBody)
	home, err := mgr.Create("alice", CreateOptions{ExtraSources: map[string]string{".claude/settings.json": account}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("profile settings are not private: %v", err)
	}
	latest := `{"permissions":{"allow":["Read(new)"]},"apiKeyHelper":"different-host","env":{"ANTHROPIC_API_KEY":"synthetic-host-rotated"}}`
	writeFile(t, shared, latest)
	if _, err := mgr.SyncClaudeConfig("alice"); err != nil {
		t.Fatal(err)
	}
	got := parseJSONObject(t, []byte(readFile(t, path)))
	if string(got["apiKeyHelper"]) != `"alice-helper"` || !rawJSONEqual(got["env"], json.RawMessage(`{"ANTHROPIC_API_KEY":"synthetic-alice"}`)) {
		t.Fatalf("account auth replaced by shared source: %v", got)
	}
	if !rawJSONEqual(got["permissions"], json.RawMessage(`{"allow":["Read(new)"]}`)) || got["hooks"] != nil {
		t.Fatalf("new policy or deliberate deletion did not propagate: %v", got)
	}
	if readFile(t, shared) != latest || readFile(t, account) != accountBody {
		t.Fatal("refresh modified a source document")
	}
	before, _ := os.Stat(path)
	changed, err := mgr.SyncClaudeConfig("alice")
	after, _ := os.Stat(path)
	if err != nil || len(changed) != 0 || !os.SameFile(before, after) {
		t.Fatalf("second refresh rewrote settings: changed=%v err=%v", changed, err)
	}
	preserved := readFile(t, path)
	writeFile(t, shared, `{broken`)
	if _, err := mgr.SyncClaudeConfig("alice"); err == nil || readFile(t, path) != preserved {
		t.Fatal("malformed shared policy was accepted or changed account settings")
	}
}

func TestShallowSettingsPolicyOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	mgr, realHome := onboardingEnv(t, `{}`)
	shared := filepath.Join(realHome, ".claude", "settings.json")
	writeFile(t, shared, `{"model":"host-model","hooks":{"Stop":["host"]},"env":{"EDITOR":"host-editor","ANTHROPIC_API_KEY":"synthetic-host"}}`)
	writeFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"profile_keys":["hooks"],"shared_env_keys":["EDITOR"]}}`)
	account := writeTempFile(t, "account.json", `{"model":"old-model","hooks":{"Stop":["own"]},"env":{"EDITOR":"old-editor","ANTHROPIC_API_KEY":"synthetic-own"}}`)
	home, err := mgr.Create("account", CreateOptions{ExtraSources: map[string]string{".claude/settings.json": account}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	got := parseJSONObject(t, []byte(readFile(t, path)))
	env := parseJSONObject(t, got["env"])
	if string(got["model"]) != `"host-model"` || !strings.Contains(string(got["hooks"]), `"own"`) || string(env["EDITOR"]) != `"host-editor"` || string(env["ANTHROPIC_API_KEY"]) != `"synthetic-own"` {
		t.Fatalf("field policy was not applied: %v", got)
	}
	before := readFile(t, path)
	writeFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"mode":"per-profile"}}`)
	writeFile(t, shared, `{"model":"changed-host"}`)
	if _, err := mgr.SyncClaudeConfig("account"); err != nil || readFile(t, path) != before {
		t.Fatalf("per-profile policy was overwritten: %v", err)
	}
}

func TestShallowSettingsMigrateRecognizedSharedLinks(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, realHome := onboardingEnv(t, `{}`)
			home, err := mgr.Create("old-profile", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			shared := filepath.Join(realHome, ".claude", "settings.json")
			path := filepath.Join(home, ".claude", "settings.json")
			body := "{\n  \"model\": \"shared\"\n}\n"
			if kind != "dangling" {
				if kind == "symlink" {
					body = `{"model":"shared","apiKeyHelper":"host-helper"}`
				}
				writeFile(t, shared, body)
			}
			if kind == "hardlink" {
				err = os.Link(shared, path)
			} else {
				err = os.Symlink(shared, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.EnsureClaudeSettingsPrivate("old-profile"); err != nil {
				t.Fatal(err)
			}
			private, err := os.Lstat(path)
			if err != nil || !private.Mode().IsRegular() {
				t.Fatalf("old shared link was not detached: %v", err)
			}
			if source, err := os.Stat(shared); err == nil && os.SameFile(source, private) {
				t.Fatal("settings still share the host inode")
			}
			got := parseJSONObject(t, []byte(readFile(t, path)))
			if got["apiKeyHelper"] != nil {
				t.Fatal("migration retained the host account helper")
			}
			if kind != "dangling" && readFile(t, shared) != body {
				t.Fatal("migration changed the real source")
			}
			privateBody := readFile(t, path)
			writeFile(t, shared, `{"apiKeyHelper":"later-host"}`)
			if readFile(t, path) != privateBody {
				t.Fatal("future host changes still cross the profile boundary")
			}
		})
	}
}

func TestShallowSettingsExplicitDirectoryDoesNotUseIgnoredLegacyPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	explicit := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", explicit)
	mgr, realHome := onboardingEnv(t, `{"theme":"ignored"}`)
	writeFile(t, filepath.Join(realHome, ".claude", "settings.json"), `{"model":"ignored","apiKeyHelper":"host"}`)
	home, err := mgr.Create("selected", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing explicit source borrowed ignored legacy settings: %v", err)
	}
	writeFile(t, filepath.Join(explicit, "settings.json"), `{"model":"selected","apiKeyHelper":"host"}`)
	writeFile(t, filepath.Join(explicit, ".claude.json"), `{"theme":"selected","oauthAccount":{"accountUuid":"host"}}`)
	if _, err := mgr.SyncClaudeConfig("selected"); err != nil {
		t.Fatal(err)
	}
	got := parseJSONObject(t, []byte(readFile(t, path)))
	if string(got["model"]) != `"selected"` || got["apiKeyHelper"] != nil {
		t.Fatalf("explicit source policy/auth boundary failed: %v", got)
	}
	state := readProfileClaudeJSON(t, home)
	if string(state["theme"]) != `"selected"` || state["oauthAccount"] != nil {
		t.Fatalf("explicit source state/auth boundary failed: %v", state)
	}
}
