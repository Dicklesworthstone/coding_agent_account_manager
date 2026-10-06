package claudesettings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func assertLegacyProjectJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var actual, expected interface{}
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

func TestLegacyProjectPolicyDoesNotCopyAnotherAccountsSessions(t *testing.T) {
	live := []byte(`{"oauthAccount":{"accountUuid":"alice"},"theme":"dark","projects":{"/repo":{"allowedTools":["Read"],"hasTrustDialogAccepted":true,"mcpServers":{"new":{}},"lastSessionId":"alice-session","lastCost":100,"history":["alice-secret"],"futurePrivateCache":"alice"},"/alice-only":{"history":["private"]}}}`)
	account := []byte(`{"oauthAccount":{"accountUuid":"bob"},"theme":"light","projects":{"/repo":{"allowedTools":["Bash"],"mcpServers":{"old":{}},"lastSessionId":"bob-session","lastCost":2,"history":["bob-secret"],"futurePrivateCache":"bob"},"/bob-only":{"lastSessionId":"private","allowedTools":["deleted"]},"/obsolete":{"mcpServers":{"removed":{}}}}}`)
	got, err := MergeLegacy(live, account, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyProjectJSON(t, got, `{"oauthAccount":{"accountUuid":"bob"},"theme":"dark","projects":{"/repo":{"allowedTools":["Read"],"hasTrustDialogAccepted":true,"mcpServers":{"new":{}},"lastSessionId":"bob-session","lastCost":2,"history":["bob-secret"],"futurePrivateCache":"bob"},"/bob-only":{"lastSessionId":"private"}}}`)

	// A second switch sees a deliberate revocation, not a union of old grants.
	live = []byte(`{"theme":"light","projects":{"/repo":{"hasTrustDialogAccepted":false}}}`)
	got, err = MergeLegacy(live, got, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyProjectJSON(t, got, `{"oauthAccount":{"accountUuid":"bob"},"theme":"light","projects":{"/repo":{"hasTrustDialogAccepted":false,"lastSessionId":"bob-session","lastCost":2,"history":["bob-secret"],"futurePrivateCache":"bob"},"/bob-only":{"lastSessionId":"private"}}}`)
}

func TestLegacyProjectPolicyRemovalAndProfileOverrides(t *testing.T) {
	account := []byte(`{"oauthAccount":{"accountUuid":"bob"},"mcpServers":{"private":{}},"projects":{"/repo":{"allowedTools":["Bash"],"mcpServers":{"private":{}},"lastSessionId":"bob"}}}`)
	for _, live := range []string{`{}`, `{"projects":{}}`, `{"projects":{"/repo":null}}`} {
		got, err := MergeLegacy([]byte(live), account, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		assertLegacyProjectJSON(t, got, `{"oauthAccount":{"accountUuid":"bob"},"projects":{"/repo":{"lastSessionId":"bob"}}}`)
	}
	for _, policy := range []Policy{{Mode: "per-profile"}, {ProfileKeys: []string{"projects", "mcpServers"}}} {
		got, err := MergeLegacy([]byte(`{"projects":{"/repo":{"mcpServers":{"shared":{}}}}}`), account, policy)
		if err != nil {
			t.Fatal(err)
		}
		assertLegacyProjectJSON(t, got, string(account))
	}
}

func TestLegacyProjectPolicyLogoutDoesNotRetainSessionCaches(t *testing.T) {
	live := []byte(`{"apiKey":"secret","projects":{"/repo":{"allowedTools":["Read"],"history":["secret"],"lastSessionId":"session"}}}`)
	got, err := MergeLegacy(live, nil, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyProjectJSON(t, got, `{"projects":{"/repo":{"allowedTools":["Read"]}}}`)
}

func TestLegacyProjectCorruptionFailsBeforeWriting(t *testing.T) {
	for _, invalid := range []string{`{"projects":[]}`, `{"projects":null}`, `{"projects":{"/repo":5}}`} {
		for _, invalidLive := range []bool{false, true} {
			dir := t.TempDir()
			livePath, snapshotPath := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
			live, account := []byte(`{"apiKey":"alice"}`), []byte(`{"apiKey":"bob"}`)
			if invalidLive {
				live = []byte(invalid)
			} else {
				account = []byte(invalid)
			}
			for path, content := range map[string][]byte{livePath: live, snapshotPath: account} {
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := PrepareLegacyRestore(snapshotPath, livePath, Policy{}); err == nil {
				t.Fatalf("accepted invalid project map: %s", invalid)
			}
			got, err := os.ReadFile(livePath)
			if err != nil || !bytes.Equal(got, live) {
				t.Fatalf("preflight changed live file: %s, %v", got, err)
			}
		}
	}
}
