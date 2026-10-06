package claudesettings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestChangedKeysReportsDeletionsWithoutValuesOrPrecisionLoss(t *testing.T) {
	u := &Update{
		before: []byte(`{"apiKey":"secret-before","counter":9007199254740992,"projects":{"/repo":{"allowedTools":["Bash"],"history":["private"]},"/removed":{"mcpServers":{"secret-url":{}}}}}`),
		after:  []byte(`{"apiKey":"secret-after","counter":9007199254740993,"projects":{"/repo":{"history":["private"]},"/new":{"hasTrustDialogAccepted":true}}}`),
	}
	got, err := u.ChangedKeys()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apiKey", "counter", "projects./new.hasTrustDialogAccepted", "projects./removed.mcpServers", "projects./repo.allowedTools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

func TestLegacyRefreshPreservesUnchangedBytesAndChecksForEdits(t *testing.T) {
	for _, mode := range []string{"shared", "per-profile"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			shared, profile := filepath.Join(dir, "shared"), filepath.Join(dir, "profile")
			const before = `{"oauthAccount":{"id":"bob"},"theme":"dark","projects":{"/repo":{"history":["private"],"allowedTools":["Read"]}}}`
			const live = `{"theme":"dark","projects":{"/repo":{"allowedTools":["Read"]}}}`
			for path, content := range map[string]string{shared: live, profile: before} {
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			u, err := PrepareLegacyRefresh(shared, profile, Policy{Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			if u.Changed() {
				t.Fatal("formatting-only refresh was marked changed")
			}
			if err := u.Apply(); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(profile)
			if err != nil || string(got) != before {
				t.Fatalf("no-op rewrote private file: %s, %v", got, err)
			}
			if err := os.WriteFile(profile, []byte(`{"theme":"edited"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := u.Apply(); err == nil {
				t.Fatal("no-op ignored an intervening edit")
			}
		})
	}
}

func TestLegacySharedPolicyKeysCannotMutateClassification(t *testing.T) {
	keys := LegacySharedPolicyKeys()
	keys[0] = "oauthAccount"
	got, err := MergeLegacy([]byte(`{"oauthAccount":{"id":"alice"},"mcpServers":{"live":{}}}`), []byte(`{"oauthAccount":{"id":"bob"}}`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyProjectJSON(t, got, `{"oauthAccount":{"id":"bob"},"mcpServers":{"live":{}}}`)
}
