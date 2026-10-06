package claudesettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportWithoutSettingsClearsPreviousAccount(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		dir := t.TempDir()
		destination := filepath.Join(dir, "settings.json")
		body := `{"apiKeyHelper":"old-account","env":{"ANTHROPIC_API_KEY":"synthetic-old"},"permissions":{"allow":["Read"]}}`
		if legacy {
			body = `{"oauthAccount":{"accountUuid":"old-account"},"apiKey":"synthetic-old","permissions":{"allow":["Read"]}}`
		}
		writeSettings(t, destination, body)
		prepare := PrepareImport
		if legacy {
			prepare = PrepareLegacyImport
		}
		update, err := prepare("", "", destination, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		if err := update.Apply(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		assertJSON(t, got, `{"permissions":{"allow":["Read"]}}`)
	}
}

func TestLegacyMCPPolicyFollowsLiveNotAccountSnapshot(t *testing.T) {
	live := []byte(`{"oauthAccount":{"accountUuid":"alice"},"sessionKey":"alice-session","mcpServers":{"new":{"command":"server"}},"projects":{"/repo":{"hasTrustDialogAccepted":true,"mcpServers":{"project":{}}}},"disabledMcpServers":["disabled"],"internalAccountCache":"alice"}`)
	account := []byte(`{"oauthAccount":{"accountUuid":"bob"},"mcpServers":{"old":{}},"projects":{"/stale":{}},"enabledMcpServers":["deleted"],"internalAccountCache":"bob"}`)
	got, err := MergeLegacy(live, account, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, `{"oauthAccount":{"accountUuid":"bob"},"mcpServers":{"new":{"command":"server"}},"projects":{"/repo":{"hasTrustDialogAccepted":true,"mcpServers":{"project":{}}}},"disabledMcpServers":["disabled"],"internalAccountCache":"bob"}`)
	got, err = MergeLegacy(live, account, Policy{ProfileKeys: []string{"mcpServers", "projects"}})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, `{"oauthAccount":{"accountUuid":"bob"},"mcpServers":{"old":{}},"projects":{"/stale":{}},"disabledMcpServers":["disabled"],"internalAccountCache":"bob"}`)
	got, err = MergeLegacy(live, nil, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, `{"mcpServers":{"new":{"command":"server"}},"projects":{"/repo":{"hasTrustDialogAccepted":true,"mcpServers":{"project":{}}}},"disabledMcpServers":["disabled"]}`)
	id, err := LegacyIdentity(got)
	if err != nil || string(id) != "{}" {
		t.Fatalf("retained MCP policy is not auth: %s, %v", id, err)
	}
}

func TestDirectAndLegacyAPIKeysAreProfileScoped(t *testing.T) {
	for _, key := range []string{"apiKey", "api_key"} {
		live := []byte(`{"` + key + `":"alice","model":"latest"}`)
		target := []byte(`{"` + key + `":"bob","model":"stale"}`)
		got, err := Merge(live, target, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		assertJSON(t, got, `{"`+key+`":"bob","model":"latest"}`)
		got, err = Merge(live, nil, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		assertJSON(t, got, `{"model":"latest"}`)
		for _, identity := range []func([]byte) ([]byte, error){LegacyIdentity, func(data []byte) ([]byte, error) { return Identity(data, Policy{}) }} {
			id, err := identity(target)
			if err != nil || string(id) == "{}" {
				t.Fatalf("missing API-key identity: %s, %v", id, err)
			}
		}
	}
}

func TestImportAndLegacyLifecyclePaths(t *testing.T) {
	dir := t.TempDir()
	shared, account, destination := filepath.Join(dir, "shared"), filepath.Join(dir, "account"), filepath.Join(dir, "destination")
	writeSettings(t, shared, `{"model":"latest","mcpServers":{"live":{}},"apiKey":"home-key"}`)
	writeSettings(t, account, `{"model":"stale","apiKey":"import-key"}`)
	for _, prepare := range []func(string, string, string, Policy) (*Update, error){PrepareImport, PrepareLegacyImport} {
		writeSettings(t, destination, `{"apiKey":"old-destination"}`)
		u, err := prepare(shared, account, destination, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		if err := u.Apply(); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(destination)
		assertJSON(t, got, `{"model":"latest","mcpServers":{"live":{}},"apiKey":"import-key"}`)
	}
	for _, prepare := range []func(string, string, Policy) (*Update, error){PrepareLegacyRestore, PrepareLegacyRefresh} {
		writeSettings(t, destination, `{"apiKey":"profile-key","mcpServers":{"profile":{}}}`)
		u, err := prepare(shared, destination, Policy{Mode: "per-profile"})
		if err != nil {
			t.Fatal(err)
		}
		if err := u.Apply(); err != nil {
			t.Fatal(err)
		}
	}
	u, err := PrepareLegacyClear(destination, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	id, err := LegacyIdentity(got)
	if err != nil || string(id) != "{}" {
		t.Fatalf("logout retained legacy auth: %s, %v", id, err)
	}
}

func TestLoadPolicyEveryTimeAndRejectCorruption(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := LoadPolicy()
	if err != nil || p.Mode != "" {
		t.Fatalf("default: %#v %v", p, err)
	}
	writeSettings(t, CAAMConfigPath(), `{"claude_settings":{"mode":"per-profile","profile_keys":["hooks"]}}`)
	p, err = LoadPolicy()
	if err != nil || p.Mode != "per-profile" || len(p.ProfileKeys) != 1 {
		t.Fatalf("config not loaded: %#v %v", p, err)
	}
	for _, invalid := range []string{`{broken`, `{"claude_settings":{"mode":"typo"}}`, `{"claude_settings":{"shared_env_keys":["ANTHROPIC_API_KEY"]}}`} {
		writeSettings(t, CAAMConfigPath(), invalid)
		if _, err := LoadPolicy(); err == nil {
			t.Fatal("bad configuration was ignored")
		}
	}
}
