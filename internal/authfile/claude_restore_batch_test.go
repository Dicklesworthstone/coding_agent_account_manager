package authfile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

func restoreBatchWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRestoreBatchStagesSettingsBeforeRetiringOAuth(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires procfs for a deterministic staging failure")
	}
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("procfs unavailable")
	}
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "settings.json"), filepath.Join(dir, "snapshot.json")
	const original = `{"permissions":{"allow":["Read"]},"apiKeyHelper":"outgoing"}`
	restoreBatchWrite(t, live, original)
	restoreBatchWrite(t, snapshot, `{"apiKeyHelper":"incoming"}`)
	first, err := claudesettings.PrepareRestore(snapshot, live, claudesettings.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	last, err := claudesettings.PrepareRestore(snapshot, filepath.Join("/proc/self", fmt.Sprintf("caam-restore-%d", os.Getpid())), claudesettings.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(dir, ".credentials.json")
	const token = `{"accessToken":"outgoing-credential"}`
	restoreBatchWrite(t, credential, token)
	retirement, err := prepareClaudeCredentialRetirement(filepath.Join(dir, "absent"), credential, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := &claudeRestoreBatch{updates: []*claudesettings.Update{first, last}, retirements: []*claudeCredentialRetirement{retirement}}
	if err := batch.Apply(); err == nil {
		t.Fatal("expected staging failure")
	}
	for path, want := range map[string]string{live: original, credential: token} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("failed restore changed outgoing state at %s: %q, %v", path, got, err)
		}
	}
}

func TestClaudeRestoreBatchChecksEveryRetirementBeforeSettings(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "settings.json")
	const original = `{"apiKeyHelper":"outgoing"}`
	restoreBatchWrite(t, live, original)
	update, err := claudesettings.PrepareAPIKeyHelper(live, "incoming")
	if err != nil {
		t.Fatal(err)
	}
	var retirements []*claudeCredentialRetirement
	for _, name := range []string{"primary", "secondary"} {
		path := filepath.Join(dir, name)
		restoreBatchWrite(t, path, `{"token":"old"}`)
		r, err := prepareClaudeCredentialRetirement(path+".snapshot", path, nil)
		if err != nil {
			t.Fatal(err)
		}
		retirements = append(retirements, r)
	}
	restoreBatchWrite(t, retirements[1].live, `{"token":"new-native-login"}`)
	batch := &claudeRestoreBatch{updates: []*claudesettings.Update{update}, retirements: retirements}
	if err := batch.Apply(); err == nil {
		t.Fatal("accepted an intervening native login")
	}
	for path, want := range map[string]string{live: original, retirements[0].live: `{"token":"old"}`, retirements[1].live: `{"token":"new-native-login"}`} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("preflight partially switched authentication at %s: %q, %v", path, got, err)
		}
	}
}

func TestClaudeRestoreBatchAcceptsCapturedMirrorAndAppliesOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	live, credential := filepath.Join(dir, "settings.json"), filepath.Join(dir, ".credentials.json")
	restoreBatchWrite(t, live, `{"model":"live","apiKeyHelper":"outgoing"}`)
	update, err := claudesettings.PrepareAPIKeyHelper(live, "incoming")
	if err != nil {
		t.Fatal(err)
	}
	const mirror = `{"token":"captured-keychain"}`
	retirement, err := prepareClaudeCredentialRetirement(filepath.Join(dir, "snapshot"), credential, func() ([]byte, error) { return []byte(mirror), nil })
	if err != nil {
		t.Fatal(err)
	}
	// Restore's keychain pull happens between preparation and execution.
	restoreBatchWrite(t, credential, mirror)
	batch := &claudeRestoreBatch{updates: []*claudesettings.Update{update}, retirements: []*claudeCredentialRetirement{retirement}}
	if err := batch.Apply(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(live); err != nil || !strings.Contains(string(got), "incoming") || !strings.Contains(string(got), "live") {
		t.Fatalf("settings activation failed: %s, %v", got, err)
	}
	if _, err := os.Lstat(credential); !os.IsNotExist(err) {
		t.Fatalf("keychain mirror was not retired: %v", err)
	}
	// Every per-file visit references the same plan. A later visit cannot
	// reinstall settings or remove a credential created after the first one.
	restoreBatchWrite(t, credential, `{"token":"later-native-login"}`)
	if err := batch.Apply(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(credential); err != nil || string(got) != `{"token":"later-native-login"}` {
		t.Fatalf("repeated batch application removed native auth: %q, %v", got, err)
	}
}
