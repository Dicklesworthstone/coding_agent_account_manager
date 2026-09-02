package shallow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestSyncClaudeConfigNoRealFileIsNoop(t *testing.T) {
	mgr, _ := onboardingEnv(t, "")
	home, err := mgr.Create("p", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := mgr.SyncClaudeConfig("p")
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed=%v err=%v, want no-op", changed, err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if string(raw) != "{}\n" {
		t.Fatalf("skeleton rewritten: %q", raw)
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
