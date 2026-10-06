package authfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

func writeClaudeSettingsTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func saveClaudeSettingsTestPolicy(t *testing.T, p claudesettings.Policy) {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{"claude_settings": p})
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeSettingsTestFile(t, claudesettings.CAAMConfigPath(), string(data))
}

// A selected account must contain a credential before restore changes live
// settings or auth. Identity metadata is especially misleading after a macOS
// backup failed to capture the login keychain (issue #113).
func TestClaudeCredentialPreflightRejectsIncompleteSnapshots(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  error
	}{
		{"metadata only", nil, ErrNoCredentials},
		{"account identity only", map[string]string{".claude.json": `{"oauthAccount":{"emailAddress":"target@example.com","accountUuid":"target"}}`}, ErrNoCredentials},
		{"shared policy only", map[string]string{".claude.json": `{"mcpServers":{},"permissions":{"allow":["Read"]}}`}, ErrNoCredentials},
		{"login selection only", map[string]string{"settings.json": `{"forceLoginMethod":"claudeai","forceLoginOrgUUID":"target"}`}, ErrNoCredentials},
		{"unrelated environment", map[string]string{"settings.json": `{"env":{"EDITOR":"vim","ANTHROPIC_BASE_URL":"https://example.invalid"}}`}, ErrNoCredentials},
		{"empty helper", map[string]string{"settings.json": `{"apiKeyHelper":"  "}`}, ErrNoCredentials},
		{"empty API key", map[string]string{"settings.json": `{"env":{"ANTHROPIC_API_KEY":""}}`}, ErrNoCredentials},
		{"unrecognized token field", map[string]string{".claude.json": `{"token":"looks-like-auth"}`}, ErrNoCredentials},
		{"empty primary object", map[string]string{".credentials.json": `{}`}, ErrNoCredentials},
		{"refresh without access", map[string]string{".credentials.json": `{"claudeAiOauth":{"refreshToken":"target-refresh"}}`}, ErrNoCredentials},
		{"expiry without access", map[string]string{".credentials.json": `{"claudeAiOauth":{"expiresAt":1893456000000}}`}, ErrNoCredentials},
		{"blank access", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":" "}}`}, ErrNoCredentials},
		{"null primary", map[string]string{".credentials.json": `null`}, ErrInvalidCredentials},
		{"array primary", map[string]string{".credentials.json": `[]`}, ErrInvalidCredentials},
		{"empty primary file", map[string]string{".credentials.json": ``}, ErrInvalidCredentials},
		{"malformed primary", map[string]string{".credentials.json": `{"claudeAiOauth":`}, ErrInvalidCredentials},
		{"null OAuth block", map[string]string{".credentials.json": `{"claudeAiOauth":null}`}, ErrInvalidCredentials},
		{"null access", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":null}}`}, ErrInvalidCredentials},
		{"numeric access", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":123}}`}, ErrInvalidCredentials},
		{"null refresh", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"opaque","refreshToken":null}}`}, ErrInvalidCredentials},
		{"numeric refresh", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"opaque","refreshToken":123}}`}, ErrInvalidCredentials},
		{"null expiry", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"opaque","expiresAt":null}}`}, ErrInvalidCredentials},
		{"malformed expiry", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"opaque","expiresAt":"broken"}}`}, ErrInvalidCredentials},
		{"negative expiry", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"opaque","expiresAt":-1}}`}, ErrInvalidCredentials},
		{"overflow expiry", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"opaque","expiresAt":1e100}}`}, ErrInvalidCredentials},
		{"null primary cannot fall through to helper", map[string]string{".credentials.json": `null`, "settings.json": `{"apiKeyHelper":"target-helper"}`}, ErrInvalidCredentials},
		{"empty primary cannot fall through to helper", map[string]string{".credentials.json": `{}`, "settings.json": `{"apiKeyHelper":"target-helper"}`}, ErrNoCredentials},
		{"null legacy token", map[string]string{".claude.json": `{"oauthToken":null}`}, ErrInvalidCredentials},
		{"null helper", map[string]string{"settings.json": `{"apiKeyHelper":null}`}, ErrInvalidCredentials},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".config", "claude-code"))
			t.Setenv("CAAM_KEYCHAIN", "0")
			fileSet := ClaudeAuthFiles()
			vault := NewVault(filepath.Join(home, "vault"))
			profileDir := vault.ProfilePath("claude", "target")
			writeClaudeSettingsTestFile(t, filepath.Join(profileDir, "meta.json"), `{"identity":"target@example.com"}`)
			for name, body := range tt.files {
				writeClaudeSettingsTestFile(t, filepath.Join(profileDir, name), body)
			}
			live := map[string]string{
				fileSet.Files[0].Path: `{"claudeAiOauth":{"accessToken":"live-access","refreshToken":"live-refresh"}}`,
				fileSet.Files[1].Path: `{"oauthAccount":{"accountUuid":"live"},"mcpServers":{"live":{}}}`,
				fileSet.Files[3].Path: `{"apiKeyHelper":"live-helper","permissions":{"allow":["Read"]}}`,
			}
			for path, body := range live {
				writeClaudeSettingsTestFile(t, path, body)
			}
			if err := vault.ValidateProfileCredentials(fileSet, "target"); !errors.Is(err, tt.want) {
				t.Fatalf("ValidateProfileCredentials() = %v, want %v", err, tt.want)
			}
			if err := vault.Restore(fileSet, "target"); !errors.Is(err, tt.want) {
				t.Fatalf("Restore() = %v, want %v", err, tt.want)
			}
			for path, body := range live {
				if got, err := os.ReadFile(path); err != nil || string(got) != body {
					t.Fatalf("failed activation changed live %s: %v", filepath.Base(path), err)
				}
			}
			for name, body := range tt.files {
				if got, err := os.ReadFile(filepath.Join(profileDir, name)); err != nil || string(got) != body {
					t.Fatalf("validation/restore changed snapshot %s: %v", name, err)
				}
			}
		})
	}
}

func TestClaudeCredentialPreflightAcceptsSupportedSources(t *testing.T) {
	tests := []struct{ name, filename, body string }{
		{"opaque OAuth access without expiry", ".credentials.json", `{"claudeAiOauth":{"accessToken":"opaque"}}`},
		{"OAuth with refresh and expiry", ".credentials.json", `{"claudeAiOauth":{"accessToken":"opaque","refreshToken":"refresh","expiresAt":1893456000000}}`},
		{"expired OAuth remains locally restorable", ".credentials.json", `{"claudeAiOauth":{"accessToken":"opaque","refreshToken":"refresh","expiresAt":1000}}`},
		{"empty refresh is access-only", ".credentials.json", `{"claudeAiOauth":{"accessToken":"opaque","refreshToken":""}}`},
		{"legacy OAuth token", ".claude.json", `{"oauthToken":"opaque"}`},
		{"legacy session key", ".claude.json", `{"sessionKey":"opaque"}`},
		{"legacy API key", ".claude.json", `{"primaryApiKey":"test-key"}`},
		{"flat OAuth", "auth.json", `{"access_token":"opaque","refresh_token":"refresh"}`},
		{"API helper", "settings.json", `{"apiKeyHelper":"target-helper"}`},
		{"API key environment", "settings.json", `{"env":{"ANTHROPIC_API_KEY":"test-key"}}`},
		{"OAuth environment", "settings.json", `{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"opaque"}}`},
		{"encrypted desktop cache", "config.json", `{"oauth:tokenCacheV2":"encrypted-cache"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".config", "claude-code"))
			t.Setenv("CAAM_KEYCHAIN", "0")
			fileSet := ClaudeAuthFiles()
			vault := NewVault(filepath.Join(home, "vault"))
			snapshot := vault.BackupPath("claude", "target", tt.filename)
			writeClaudeSettingsTestFile(t, snapshot, tt.body)
			if err := vault.ValidateProfileCredentials(fileSet, "target"); err != nil {
				t.Fatalf("ValidateProfileCredentials() = %v", err)
			}
			// Preflight never materializes live credentials, even for a source
			// with no expiry or a helper which can only be checked by running it.
			for _, spec := range fileSet.Files {
				if _, err := os.Stat(spec.Path); !os.IsNotExist(err) {
					t.Fatalf("preflight wrote live %s: %v", spec.Path, err)
				}
			}
			if err := vault.Restore(fileSet, "target"); err != nil {
				t.Fatalf("Restore() = %v", err)
			}
			if !HasAuthFiles(fileSet) {
				t.Fatal("restored credential source is not recognized as auth")
			}
			if active, err := vault.ActiveProfile(fileSet); err != nil || active != "target" {
				t.Fatalf("ActiveProfile() = %q, %v; want target", active, err)
			}
		})
	}
}

func TestClaudeCredentiallessLiveStateCannotCreateProfile(t *testing.T) {
	for _, body := range []string{
		`{"oauthAccount":{"accountUuid":"identity-only"}}`,
		`{"mcpServers":{"local":{}},"projects":{}}`,
		`{"oauthToken":""}`,
	} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".config", "claude-code"))
			t.Setenv("CAAM_KEYCHAIN", "0")
			fileSet := ClaudeAuthFiles()
			writeClaudeSettingsTestFile(t, fileSet.Files[1].Path, body)
			writeClaudeSettingsTestFile(t, fileSet.Files[3].Path, `{"forceLoginMethod":"claudeai","env":{"EDITOR":"vim"}}`)
			vault := NewVault(filepath.Join(home, "vault"))
			if HasAuthFiles(fileSet) {
				t.Fatal("account labels or settings were reported as a live login")
			}
			if err := vault.Backup(fileSet, "empty"); !errors.Is(err, ErrNoCredentials) {
				t.Fatalf("Backup() = %v, want ErrNoCredentials", err)
			}
			if _, err := os.Stat(vault.ProfilePath("claude", "empty")); !os.IsNotExist(err) {
				t.Fatalf("failed backup created an empty profile: %v", err)
			}
		})
	}
}

func TestClaudeSettingsVaultMultiProfileLifecycle(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t, `{}`, `{}`)
	accounts := map[string]string{
		"alice": `"apiKeyHelper":"alice-helper","awsAuthRefresh":"alice-sso","forceLoginOrgUUID":"alice-org"`,
		"bob":   `"env":{"ANTHROPIC_AUTH_TOKEN":"bob-token","ANTHROPIC_BASE_URL":"https://bob.invalid","AWS_PROFILE":"bob"},"awsCredentialExport":"bob-export"`,
		"oauth": `"forceLoginMethod":"claudeai"`,
	}
	for name, fields := range accounts {
		writeClaudeSettingsTestFile(t, fileSet.Files[0].Path, fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q}}`, name+"-at", name+"-rt"))
		writeClaudeSettingsTestFile(t, live, `{`+fields+`,"permissions":{"allow":["Bash(*)"]},"autoMode":{"allow":["stale"]},"mcpServers":{"stale":{}},"hooks":{"Stop":[]},"model":"stale"}`)
		if err := vault.Backup(fileSet, name); err != nil {
			t.Fatal(err)
		}
	}
	const workflow = `{
		"permissions":{"allow":["Read","Bash(go test:*)"],"deny":["Bash(rm:*)"],"defaultMode":"auto"},
		"autoMode":{"allow":["Run tests"],"deny":["Delete files"]},
		"model":"opus","effortLevel":"high",
		"mcpServers":{"local":{"command":"mcp-server","args":["--latest"]}},
		"hooks":{"PostToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"format"}]}]},
		"enabledPlugins":{"kept@local":true},"unknownFuturePreference":{"enabled":true}
	}`
	writeClaudeSettingsTestFile(t, live, workflow)
	var wantPolicy map[string]interface{}
	if err := json.Unmarshal([]byte(workflow), &wantPolicy); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob", "oauth", "alice", "bob"} {
		before, err := os.ReadFile(vault.BackupPath("claude", name, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := vault.Restore(fileSet, name); err != nil {
			t.Fatalf("activate %s: %v", name, err)
		}
		got := readJSONMap(t, live)
		for key, want := range wantPolicy {
			if !reflect.DeepEqual(got[key], want) {
				t.Fatalf("%s: live %s reverted: got %#v, want %#v", name, key, got[key], want)
			}
		}
		var wantAccount map[string]interface{}
		if err := json.Unmarshal([]byte(`{`+accounts[name]+`}`), &wantAccount); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "forceLoginOrgUUID", "forceLoginMethod", "env"} {
			if !reflect.DeepEqual(got[key], wantAccount[key]) {
				t.Fatalf("%s: wrong %s; previous account leaked or target auth missing", name, key)
			}
		}
		if active, err := vault.ActiveProfile(fileSet); err != nil || active != name {
			t.Fatalf("active = %q, %v; want %s", active, err, name)
		}
		after, err := os.ReadFile(vault.BackupPath("claude", name, "settings.json"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("restore mutated snapshot: %v", err)
		}
	}

	// Deleting a whole policy key and its rules is authoritative too.
	writeClaudeSettingsTestFile(t, live, `{"permissions":{"allow":[]},"env":{"ANTHROPIC_AUTH_TOKEN":"bob-token"}}`)
	if err := vault.ResnapshotOutgoing(fileSet, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Restore(fileSet, "alice"); err != nil {
		t.Fatal(err)
	}
	got := readJSONMap(t, live)
	for _, key := range []string{"autoMode", "hooks", "mcpServers", "model", "enabledPlugins"} {
		if _, ok := got[key]; ok {
			t.Fatalf("deleted %s was resurrected", key)
		}
	}
	if !reflect.DeepEqual(got["permissions"], map[string]interface{}{"allow": []interface{}{}}) {
		t.Fatalf("deleted permission rules came back: %#v", got["permissions"])
	}
}

func TestClaudeSettingsMissingSnapshotClearsOutgoingAuth(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t, `{}`, `{"apiKeyHelper":"alice","env":{"ANTHROPIC_API_KEY":"alice"},"permissions":{"allow":["Read"]},"hooks":{}}`)
	if err := os.Remove(vault.BackupPath("claude", "bob", "settings.json")); err != nil {
		t.Fatal(err)
	}
	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatal(err)
	}
	got := readJSONMap(t, live)
	if got["apiKeyHelper"] != nil || got["env"] != nil || got["permissions"] == nil || got["hooks"] == nil {
		t.Fatalf("missing snapshot did not scrub auth and retain policy: %#v", got)
	}
}

func TestClaudeSettingsEmptyTargetDoesNotChangeLiveAccount(t *testing.T) {
	const original = `{"apiKeyHelper":"alice","permissions":{"allow":["Read"]}}`
	vault, fileSet, live := claudeSettingsFixture(t, `{"model":"policy-only"}`, original)
	if err := os.Remove(vault.BackupPath("claude", "bob", ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	fileSet.AllowOptionalOnly = true
	writeClaudeSettingsTestFile(t, fileSet.Files[0].Path, `{"claudeAiOauth":{"accessToken":"alice"}}`)
	if err := vault.Restore(fileSet, "bob"); err == nil {
		t.Fatal("accepted a policy-only target as authenticated")
	}
	if got, _ := os.ReadFile(live); string(got) != original {
		t.Fatal("failed activation changed live settings")
	}
	if got, _ := os.ReadFile(fileSet.Files[0].Path); string(got) != `{"claudeAiOauth":{"accessToken":"alice"}}` {
		t.Fatal("failed activation changed live credentials")
	}
}

func TestClaudeSettingsConfigOverridesApplyAtVaultBoundary(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t,
		`{"model":"snapshot","hooks":{"Stop":["bob"]},"apiKeyHelper":"bob","env":{"ANTHROPIC_API_KEY":"bob","EDITOR":"stale"}}`,
		`{"model":"live","hooks":{"Stop":["alice"]},"mcpServers":{"account-private":{}},"env":{"ANTHROPIC_API_KEY":"alice","EDITOR":"vim"}}`,
	)
	saveClaudeSettingsTestPolicy(t, claudesettings.Policy{ProfileKeys: []string{"hooks", "mcpServers"}, SharedEnvKeys: []string{"EDITOR"}})
	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatal(err)
	}
	got := readJSONMap(t, live)
	if got["model"] != "live" || got["mcpServers"] != nil || got["apiKeyHelper"] != "bob" {
		t.Fatalf("scope override not applied: %#v", got)
	}
	if !reflect.DeepEqual(got["hooks"], map[string]interface{}{"Stop": []interface{}{"bob"}}) {
		t.Fatal("profile-scoped hooks were not restored")
	}
	if !reflect.DeepEqual(got["env"], map[string]interface{}{"ANTHROPIC_API_KEY": "bob", "EDITOR": "vim"}) {
		t.Fatal("shared env override or auth isolation failed")
	}

	saveClaudeSettingsTestPolicy(t, claudesettings.Policy{Mode: "per-profile"})
	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readJSONMap(t, live), readJSONMap(t, vault.BackupPath("claude", "bob", "settings.json"))) {
		t.Fatal("explicit per-profile mode did not restore the snapshot")
	}
}

func TestClaudeSettingsInvalidConfigFailsBeforeCredentialsChange(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t, `{"apiKeyHelper":"bob"}`, `{"model":"live"}`)
	writeClaudeSettingsTestFile(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"mode":"typo"}}`)
	if err := vault.Restore(fileSet, "bob"); err == nil {
		t.Fatal("invalid policy did not stop activation")
	}
	if got, _ := os.ReadFile(live); string(got) != `{"model":"live"}` {
		t.Fatal("invalid policy changed live settings")
	}
	if _, err := os.Stat(fileSet.Files[0].Path); !os.IsNotExist(err) {
		t.Fatal("invalid policy changed credentials")
	}
}

func TestClaudeSettingsLogoutKeepsPolicyButNotAuthentication(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t, `{"apiKeyHelper":"bob"}`, `{"permissions":{"allow":["Read"]},"autoMode":{},"hooks":{},"mcpServers":{},"apiKeyHelper":"alice","env":{"ANTHROPIC_API_KEY":"alice","EDITOR":"vim"}}`)
	fileSet.AllowOptionalOnly = true
	saveClaudeSettingsTestPolicy(t, claudesettings.Policy{SharedEnvKeys: []string{"EDITOR"}})
	writeClaudeSettingsTestFile(t, fileSet.Files[0].Path, `{"claudeAiOauth":{"accessToken":"alice"}}`)
	if err := ClearAuthFiles(fileSet); err != nil {
		t.Fatal(err)
	}
	got := readJSONMap(t, live)
	if got["permissions"] == nil || got["autoMode"] == nil || got["hooks"] == nil || got["mcpServers"] == nil || got["apiKeyHelper"] != nil {
		t.Fatalf("logout removed policy or retained auth: %#v", got)
	}
	if !reflect.DeepEqual(got["env"], map[string]interface{}{"EDITOR": "vim"}) {
		t.Fatal("logout did not isolate shared env")
	}
	if HasAuthFiles(fileSet) {
		t.Fatal("retained policy was mistaken for authentication")
	}
	if active, err := vault.ActiveProfile(fileSet); err != nil || active != "" {
		t.Fatalf("policy-only state matched a profile: %q, %v", active, err)
	}
	if err := vault.Backup(fileSet, "logged-out"); err == nil {
		t.Fatal("policy-only state was backed up as authenticated")
	}
}

func TestClaudeSettingsBackupRemovesObsoleteAuthSnapshot(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t, `{"apiKeyHelper":"old-bob"}`, `{}`)
	writeClaudeSettingsTestFile(t, fileSet.Files[0].Path, `{"claudeAiOauth":{"accessToken":"bob-at","refreshToken":"bob-rt"}}`)
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := vault.Backup(fileSet, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(vault.BackupPath("claude", "bob", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("deleted settings snapshot survived backup: %v", err)
	}
	writeClaudeSettingsTestFile(t, live, `{"apiKeyHelper":"alice","model":"latest"}`)
	if err := vault.Restore(fileSet, "bob"); err != nil {
		t.Fatal(err)
	}
	got := readJSONMap(t, live)
	if got["apiKeyHelper"] != nil || got["model"] != "latest" {
		t.Fatalf("obsolete auth came back: %#v", got)
	}
}

func TestClaudeSettingsSharedEnvDriftKeepsAPIKeyProfileIdentity(t *testing.T) {
	vault, fileSet, live := claudeSettingsFixture(t, `{}`, `{"apiKeyHelper":"alice","env":{"EDITOR":"old"}}`)
	fileSet.AllowOptionalOnly = true
	saveClaudeSettingsTestPolicy(t, claudesettings.Policy{SharedEnvKeys: []string{"EDITOR"}})
	if err := vault.Backup(fileSet, "alice"); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettingsTestFile(t, live, `{"apiKeyHelper":"alice","env":{"EDITOR":"new"},"permissions":{"defaultMode":"auto"}}`)
	if active, err := vault.ActiveProfile(fileSet); err != nil || active != "alice" {
		t.Fatalf("shared env drift changed identity: %q, %v", active, err)
	}
}

func TestClaudeLegacyMCPRoundTripAndLogout(t *testing.T) {
	vault, fileSet, settings := claudeSettingsFixture(t, `{"apiKeyHelper":"bob"}`, `{"permissions":{"allow":["Read"]}}`)
	legacy := filepath.Join(filepath.Dir(filepath.Dir(settings)), ".claude.json")
	fileSet.Files = append(fileSet.Files, AuthFileSpec{Tool: "claude", Path: legacy})
	writeClaudeSettingsTestFile(t, legacy, `{"oauthAccount":{"accountUuid":"alice"},"mcpServers":{"live":{"command":"latest"}},"projects":{"/repo":{"mcpServers":{"fresh":{}}}}}`)
	writeClaudeSettingsTestFile(t, filepath.Join(vault.ProfilePath("claude", "bob"), ".claude.json"), `{"oauthAccount":{"accountUuid":"bob"},"mcpServers":{"stale":{}},"projects":{"/old":{}}}`)
	for _, name := range []string{"bob", "bob"} {
		if err := vault.Restore(fileSet, name); err != nil {
			t.Fatal(err)
		}
		got := readJSONMap(t, legacy)
		if got["oauthAccount"].(map[string]interface{})["accountUuid"] != "bob" {
			t.Fatal("legacy auth did not switch")
		}
		if _, ok := got["mcpServers"].(map[string]interface{})["live"]; !ok {
			t.Fatal("live MCP registration lost")
		}
		if _, ok := got["projects"].(map[string]interface{})["/repo"]; !ok {
			t.Fatal("project MCP registration lost")
		}
	}
	if err := ClearAuthFiles(fileSet); err != nil {
		t.Fatal(err)
	}
	if _, ok := readJSONMap(t, legacy)["oauthAccount"]; ok {
		t.Fatal("logout retained legacy auth")
	}
	if HasAuthFiles(fileSet) {
		t.Fatal("retained MCP/settings policy counted as auth")
	}
}
