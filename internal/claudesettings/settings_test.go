package claudesettings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	decode := func(data []byte) interface{} {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var obj interface{}
		if err := dec.Decode(&obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	if !reflect.DeepEqual(decode(got), decode([]byte(want))) {
		t.Fatalf("settings mismatch\ngot: %s\nwant: %s", got, want)
	}
}

func TestMergePolicyAndAuthentication(t *testing.T) {
	live := []byte(`{
		"permissions":{"allow":["Read","Bash(go test:*)"],"defaultMode":"auto"},
		"autoMode":{"allow":["Run tests"]},"model":"opus","effortLevel":"high",
		"mcpServers":{"local":{"command":"new-mcp"}},"hooks":{"Stop":[{"command":"new-hook"}]},
		"enabledPlugins":{"kept@local":true},"dialogAccepted":true,"futureCounter":9007199254740993,
		"apiKeyHelper":"alice-helper","awsAuthRefresh":"alice-sso",
		"forceLoginOrgUUID":"alice-org","env":{"ANTHROPIC_API_KEY":"alice-key","EDITOR":"new-editor"}
	}`)
	account := []byte(`{
		"permissions":{"allow":["Bash(*)"]},"autoMode":{"allow":["stale"]},"model":"stale",
		"mcpServers":{"stale":{}},"hooks":{"Stop":[]},"deletedFlag":true,
		"apiKeyHelper":"bob-helper","awsCredentialExport":"bob-export","forceLoginOrgUUID":"bob-org",
		"env":{"ANTHROPIC_API_KEY":"bob-key","AWS_PROFILE":"bob","EDITOR":"stale-editor"}
	}`)
	got, err := Merge(live, account, Policy{SharedEnvKeys: []string{"EDITOR"}})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, `{
		"permissions":{"allow":["Read","Bash(go test:*)"],"defaultMode":"auto"},
		"autoMode":{"allow":["Run tests"]},"model":"opus","effortLevel":"high",
		"mcpServers":{"local":{"command":"new-mcp"}},"hooks":{"Stop":[{"command":"new-hook"}]},
		"enabledPlugins":{"kept@local":true},"dialogAccepted":true,"futureCounter":9007199254740993,
		"apiKeyHelper":"bob-helper","awsCredentialExport":"bob-export","forceLoginOrgUUID":"bob-org",
		"env":{"ANTHROPIC_API_KEY":"bob-key","AWS_PROFILE":"bob","EDITOR":"new-editor"}
	}`)
}

func TestMergeMissingAndDeletedSettings(t *testing.T) {
	for _, tc := range []struct {
		name          string
		live, account []byte
		want          string
	}{
		{"empty live removes stale policy", []byte(`{}`), []byte(`{"permissions":{"allow":["Bash(*)"]},"apiKeyHelper":"bob"}`), `{"apiKeyHelper":"bob"}`},
		{"missing snapshot removes outgoing auth", []byte(`{"model":"opus","apiKeyHelper":"alice","env":{"ANTHROPIC_AUTH_TOKEN":"alice"}}`), nil, `{"model":"opus"}`},
		{"OAuth profile removes all prior helpers", []byte(`{"apiKeyHelper":"alice","awsAuthRefresh":"alice","awsCredentialExport":"alice","forceLoginMethod":"console","forceLoginOrgUUID":"alice","otelHeadersHelper":"alice","env":{"UNKNOWN_SECRET":"alice"},"hooks":{}}`), []byte(`{}`), `{"hooks":{}}`},
		{"bootstrap only when live missing", nil, []byte(`{"model":"backup","apiKeyHelper":"bob"}`), `{"model":"backup","apiKeyHelper":"bob"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Merge(tc.live, tc.account, Policy{})
			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, got, tc.want)
		})
	}
	got, err := Merge(nil, nil, Policy{})
	if err != nil || got != nil {
		t.Fatalf("absent files should remain absent: %q, %v", got, err)
	}
}

func TestPolicyOverrides(t *testing.T) {
	live := []byte(`{"model":"live","mcpServers":{"live":{}},"hooks":{"live":[]},"customAuth":"alice","env":{"EDITOR":"live"}}`)
	account := []byte(`{"model":"snapshot","mcpServers":{"bob":{}},"env":{"EDITOR":"snapshot","CUSTOM_ACCOUNT":"bob"}}`)
	got, err := Merge(live, account, Policy{ProfileKeys: []string{"mcpServers", "hooks", "customAuth"}})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, `{"model":"live","mcpServers":{"bob":{}},"env":{"EDITOR":"snapshot","CUSTOM_ACCOUNT":"bob"}}`)
	got, err = Merge(live, account, Policy{Mode: "per-profile"})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, string(account))
	// Explicitly shared env values honor deletions rather than reviving the snapshot.
	got, err = Merge([]byte(`{}`), account, Policy{SharedEnvKeys: []string{"EDITOR"}})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, got, `{"env":{"CUSTOM_ACCOUNT":"bob"}}`)
}

func TestPolicyRejectsUnsafeConfiguration(t *testing.T) {
	for _, mode := range []string{"global", "Shared", "", "shared", "per-profile"} {
		err := (Policy{Mode: mode}).Validate()
		wantError := mode == "global" || mode == "Shared"
		if (err != nil) != wantError {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "AWS_PROFILE", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_ID", "CUSTOM_AUTH", "CUSTOM_SECRET", "HTTP_PROXY", "CUSTOM_HEADERS", "", " spaced ", "A=B"} {
		if err := (Policy{SharedEnvKeys: []string{key}}).Validate(); err == nil {
			t.Fatalf("unsafe shared variable accepted: %q", key)
		}
	}
	for _, key := range []string{"", " spaced ", "env"} {
		if err := (Policy{ProfileKeys: []string{key}}).Validate(); err == nil {
			t.Fatalf("invalid profile key accepted: %q", key)
		}
	}
}

func TestMalformedSettingsFailClosed(t *testing.T) {
	for _, bad := range []string{"", "null", "[]", `"text"`, `{not json`, `{"env":null}`, `{"env":[]}`, `{"env":"secret"}`} {
		for _, side := range []string{"live", "profile"} {
			t.Run(side+":"+bad, func(t *testing.T) {
				live, account := []byte(`{"permissions":{"allow":["Read"]}}`), []byte(`{"apiKeyHelper":"bob"}`)
				if side == "live" {
					live = []byte(bad)
				} else {
					account = []byte(bad)
				}
				if _, err := Merge(live, account, Policy{}); err == nil {
					t.Fatal("accepted malformed settings")
				}
			})
		}
	}
}

func TestIdentityIgnoresPolicyButIncludesAuth(t *testing.T) {
	p := Policy{SharedEnvKeys: []string{"EDITOR"}, ProfileKeys: []string{"customAuth"}}
	a, err := Identity([]byte(`{"permissions":{},"model":"opus","apiKeyHelper":"helper","env":{"TOKEN":"x","EDITOR":"a"},"customAuth":{"a":1,"b":2}}`), p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Identity([]byte(`{"hooks":{},"apiKeyHelper":"helper","env":{"EDITOR":"b","TOKEN":"x"},"customAuth":{"b":2, "a":1}}`), p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("policy drift changed identity: %s vs %s", a, b)
	}
	for _, auth := range []string{`{"awsAuthRefresh":"other"}`, `{"awsCredentialExport":"other"}`, `{"forceLoginOrgUUID":"other"}`, `{"env":{"TOKEN":"other"}}`, `{"customAuth":"other"}`} {
		c, err := Identity([]byte(auth), p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(a, c) {
			t.Fatal("different authentication must change identity")
		}
	}
	noAuth, err := Identity([]byte(`{"model":"opus","env":{"EDITOR":"a"}}`), p)
	if err != nil || string(noAuth) != "{}" {
		t.Fatalf("policy-only identity = %s: %v", noAuth, err)
	}
}

func TestThreeProfileLifecycle(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live", "settings.json")
	profiles := map[string]string{
		"alice": `{"apiKeyHelper":"alice","model":"stale-a","permissions":{"allow":["Bash(*)"]}}`,
		"bob":   `{"env":{"ANTHROPIC_AUTH_TOKEN":"bob"},"model":"stale-b"}`,
		"oauth": `{"model":"stale-c","autoMode":{"allow":["stale"]}}`,
	}
	for name, body := range profiles {
		writeSettings(t, filepath.Join(dir, name, "settings.json"), body)
	}
	policy := `"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["tests"]},"mcpServers":{"fresh":{}},"hooks":{"Stop":[]},"model":"opus","effortLevel":"high"`
	writeSettings(t, live, `{`+policy+`,"apiKeyHelper":"alice"}`)
	for _, name := range []string{"bob", "oauth", "alice", "bob"} {
		u, err := PrepareRestore(filepath.Join(dir, name, "settings.json"), live, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		if err := u.Apply(); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(live)
		if err != nil {
			t.Fatal(err)
		}
		auth := ""
		if name == "bob" {
			auth = `,"env":{"ANTHROPIC_AUTH_TOKEN":"bob"}`
		}
		if name == "alice" {
			auth = `,"apiKeyHelper":"alice"`
		}
		assertJSON(t, got, `{`+policy+auth+`}`)
		// Vault snapshots remain recovery artifacts, never overwritten by activation.
		before, _ := os.ReadFile(filepath.Join(dir, name, "settings.json"))
		if string(before) != profiles[name] {
			t.Fatal("activation changed snapshot")
		}
	}
	// Removing whole policy keys and rules remains authoritative on the next switch.
	writeSettings(t, live, `{"permissions":{"allow":[]},"env":{"ANTHROPIC_AUTH_TOKEN":"bob"}}`)
	u, err := PrepareRestore(filepath.Join(dir, "alice", "settings.json"), live, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(live)
	assertJSON(t, got, `{"permissions":{"allow":[]},"apiKeyHelper":"alice"}`)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(live)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("settings permissions = %v", info.Mode())
		}
	}
}

func TestRefreshAndClearKeepAuthIsolated(t *testing.T) {
	dir := t.TempDir()
	shared, profile := filepath.Join(dir, "shared.json"), filepath.Join(dir, "profile.json")
	writeSettings(t, shared, `{"model":"latest","hooks":{"Stop":[]},"apiKeyHelper":"real-home","env":{"ANTHROPIC_API_KEY":"real-home","EDITOR":"vim"}}`)
	writeSettings(t, profile, `{"apiKeyHelper":"profile-helper","env":{"ANTHROPIC_API_KEY":"profile-key"},"model":"stale"}`)
	p := Policy{SharedEnvKeys: []string{"EDITOR"}}
	u, err := PrepareRefresh(shared, profile, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(profile)
	assertJSON(t, got, `{"model":"latest","hooks":{"Stop":[]},"apiKeyHelper":"profile-helper","env":{"ANTHROPIC_API_KEY":"profile-key","EDITOR":"vim"}}`)
	for _, mode := range []string{"shared", "per-profile"} {
		p.Mode = mode
		u, err := PrepareClear(profile, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := u.Apply(); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(profile)
		assertJSON(t, got, `{"model":"latest","hooks":{"Stop":[]},"env":{"EDITOR":"vim"}}`)
	}
	newProfile := filepath.Join(dir, "new", "settings.json")
	u, err = PrepareRefresh(shared, newProfile, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(newProfile)
	assertJSON(t, got, `{"model":"latest","hooks":{"Stop":[]}}`)
}

func TestPreparedUpdateRejectsConcurrentEdit(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "settings.json")
	writeSettings(t, live, `{"model":"before"}`)
	u, err := PrepareRestore("", live, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, live, `{"model":"edited"}`)
	if err := u.Apply(); err == nil {
		t.Fatal("overwrote concurrent edit")
	}
	got, _ := os.ReadFile(live)
	assertJSON(t, got, `{"model":"edited"}`)
}

func TestReadErrorsAndInvalidPlansDoNotWrite(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "settings.json")
	writeSettings(t, live, `{"model":"kept"}`)
	if _, err := PrepareRestore(dir, live, Policy{}); err == nil {
		t.Fatal("directory accepted as snapshot")
	}
	if _, err := PrepareRefresh(dir, live, Policy{}); err == nil {
		t.Fatal("directory accepted as shared settings")
	}
	if _, err := PrepareRestore("", live, Policy{Mode: "typo"}); err == nil {
		t.Fatal("invalid policy accepted")
	}
	got, _ := os.ReadFile(live)
	assertJSON(t, got, `{"model":"kept"}`)
}
