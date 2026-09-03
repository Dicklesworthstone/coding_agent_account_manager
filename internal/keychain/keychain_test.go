package keychain_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

const account = "tester"

func TestGetSetDeleteRoundTrip(t *testing.T) {
	testutil.FakeKeychain(t)

	if _, err := keychain.Get("svc", account); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("Get on empty keychain = %v, want ErrNotFound", err)
	}

	if err := keychain.Set("svc", account, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := keychain.Get("svc", account)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("Get = %q, want %q", got, `{"a":1}`)
	}

	// Set replaces in place rather than creating a second item.
	if err := keychain.Set("svc", account, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("Set (update): %v", err)
	}
	got, _ = keychain.Get("svc", account)
	if string(got) != `{"a":2}` {
		t.Fatalf("after update Get = %q, want %q", got, `{"a":2}`)
	}

	if err := keychain.Delete("svc", account); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := keychain.Get("svc", account); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
	// Deleting a missing item is not an error.
	if err := keychain.Delete("svc", account); err != nil {
		t.Fatalf("Delete (missing): %v", err)
	}
}

func TestDisabledBridgeIsInert(t *testing.T) {
	testutil.FakeKeychain(t)
	testutil.SetKeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount(), `{"claudeAiOauth":{"accessToken":"T"}}`)

	t.Setenv(keychain.EnvDisable, "0")

	if keychain.Available() {
		t.Fatal("Available = true with CAAM_KEYCHAIN=0")
	}
	if _, ok := keychain.ClaudeCredentials(); ok {
		t.Fatal("ClaudeCredentials returned a value with the bridge disabled")
	}
	path := filepath.Join(t.TempDir(), ".credentials.json")
	if keychain.MirrorClaudeCredentials(path) {
		t.Fatal("MirrorClaudeCredentials succeeded with the bridge disabled")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled bridge wrote %s", path)
	}
}

func TestMirrorClaudeCredentials(t *testing.T) {
	testutil.FakeKeychain(t)
	path := filepath.Join(t.TempDir(), ".claude", ".credentials.json")

	// Nothing in the keychain: nothing to mirror.
	if keychain.MirrorClaudeCredentials(path) {
		t.Fatal("mirrored from an empty keychain")
	}

	const blob = `{"claudeAiOauth":{"accessToken":"A","refreshToken":"R"}}`
	testutil.SetKeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount(), blob)

	if !keychain.MirrorClaudeCredentials(path) {
		t.Fatal("MirrorClaudeCredentials = false, want true")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if string(data) != blob {
		t.Fatalf("mirror = %q, want %q", data, blob)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat mirror: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("mirror mode = %o, want 600", perm)
	}

	// The keychain wins over a stale file: Claude Code rotates tokens there.
	const rotated = `{"claudeAiOauth":{"accessToken":"A2","refreshToken":"R2"}}`
	testutil.SetKeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount(), rotated)
	if !keychain.MirrorClaudeCredentials(path) {
		t.Fatal("re-mirror = false, want true")
	}
	data, _ = os.ReadFile(path)
	if string(data) != rotated {
		t.Fatalf("stale mirror survived: %q", data)
	}
}

func TestMirrorRejectsNonJSONItem(t *testing.T) {
	testutil.FakeKeychain(t)
	testutil.SetKeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount(), "not json")

	path := filepath.Join(t.TempDir(), ".credentials.json")
	if keychain.MirrorClaudeCredentials(path) {
		t.Fatal("mirrored a non-JSON keychain item")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("non-JSON item was written to %s", path)
	}
}

func TestStoreAndClearClaudeCredentials(t *testing.T) {
	testutil.FakeKeychain(t)

	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")

	// A missing file is not an error, and stores nothing.
	if err := keychain.StoreClaudeCredentials(path); err != nil {
		t.Fatalf("Store (missing file): %v", err)
	}
	if _, ok := keychain.ClaudeCredentials(); ok {
		t.Fatal("Store wrote an item for a missing file")
	}

	const blob = `{"claudeAiOauth":{"accessToken":"B"}}`
	if err := os.WriteFile(path, []byte(blob), 0600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	if err := keychain.StoreClaudeCredentials(path); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok := testutil.KeychainItem(t, keychain.ClaudeService, keychain.CurrentAccount())
	if !ok || got != blob {
		t.Fatalf("keychain item = %q (ok=%v), want %q", got, ok, blob)
	}

	if err := keychain.ClearClaudeCredentials(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := keychain.ClaudeCredentials(); ok {
		t.Fatal("item survived ClearClaudeCredentials")
	}
}

func TestStoreRejectsNonJSONFile(t *testing.T) {
	testutil.FakeKeychain(t)

	path := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(path, []byte("garbage"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := keychain.StoreClaudeCredentials(path); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, ok := keychain.ClaudeCredentials(); ok {
		t.Fatal("Store pushed a non-JSON file into the keychain")
	}
}
