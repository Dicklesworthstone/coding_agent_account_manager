package keychain_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

const sampleBlob = `{"claudeAiOauth":{"accessToken":"at-1","refreshToken":"rt-1","expiresAt":1893456000000}}`

func TestDisabledBridgeIsInert(t *testing.T) {
	t.Setenv("CAAM_KEYCHAIN", "0")
	t.Setenv("CAAM_KEYCHAIN_BIN", "")

	if keychain.Enabled() {
		t.Fatal("Enabled() is true with CAAM_KEYCHAIN=0")
	}
	if _, err := keychain.ReadClaude(); !errors.Is(err, keychain.ErrNoKeychain) {
		t.Fatalf("ReadClaude() = %v, want ErrNoKeychain", err)
	}
	if err := keychain.WriteClaude([]byte(sampleBlob)); !errors.Is(err, keychain.ErrNoKeychain) {
		t.Fatalf("WriteClaude() = %v, want ErrNoKeychain", err)
	}
	if err := keychain.DeleteClaude(); err != nil {
		t.Fatalf("DeleteClaude() with the bridge off = %v, want nil", err)
	}

	credPath := filepath.Join(t.TempDir(), ".credentials.json")
	if _, err := keychain.EnsureMirror(credPath); !errors.Is(err, keychain.ErrNoKeychain) {
		t.Fatalf("EnsureMirror() = %v, want ErrNoKeychain", err)
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Fatal("EnsureMirror() wrote a file with the bridge off")
	}
}

func TestClaudeRoundTrip(t *testing.T) {
	items := testutil.FakeKeychain(t)

	if _, err := keychain.ReadClaude(); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("ReadClaude() on an empty keychain = %v, want ErrNotFound", err)
	}
	if err := keychain.WriteClaude([]byte(sampleBlob)); err != nil {
		t.Fatalf("WriteClaude(): %v", err)
	}
	got, err := keychain.ReadClaude()
	if err != nil {
		t.Fatalf("ReadClaude(): %v", err)
	}
	if string(got) != sampleBlob {
		t.Fatalf("ReadClaude() = %q, want %q", got, sampleBlob)
	}
	if stored, ok := testutil.FakeKeychainRead(t, items, keychain.ClaudeService); !ok || stored != sampleBlob {
		t.Fatalf("item in keychain = %q (present=%v)", stored, ok)
	}

	// -U replaces the payload in place rather than failing on a duplicate.
	updated := `{"claudeAiOauth":{"accessToken":"at-2"}}`
	if err := keychain.WriteClaude([]byte(updated)); err != nil {
		t.Fatalf("WriteClaude() over an existing item: %v", err)
	}
	got, err = keychain.ReadClaude()
	if err != nil {
		t.Fatalf("ReadClaude() after update: %v", err)
	}
	if string(got) != updated {
		t.Fatalf("ReadClaude() after update = %q, want %q", got, updated)
	}

	if err := keychain.DeleteClaude(); err != nil {
		t.Fatalf("DeleteClaude(): %v", err)
	}
	if _, ok := testutil.FakeKeychainRead(t, items, keychain.ClaudeService); ok {
		t.Fatal("DeleteClaude() left the item behind")
	}
	if err := keychain.DeleteClaude(); err != nil {
		t.Fatalf("DeleteClaude() on a missing item = %v, want nil", err)
	}
}

func TestLockedKeychainIsDenied(t *testing.T) {
	testutil.FakeKeychain(t)
	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")

	_, err := keychain.ReadClaude()
	if !errors.Is(err, keychain.ErrDenied) {
		t.Fatalf("ReadClaude() against a locked keychain = %v, want ErrDenied", err)
	}
}

func TestReadClaudeRejectsNonJSON(t *testing.T) {
	items := testutil.FakeKeychain(t)
	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), "not json")

	if _, err := keychain.ReadClaude(); err == nil {
		t.Fatal("ReadClaude() accepted a non-JSON item")
	}
	if err := keychain.WriteClaude([]byte("not json")); err == nil {
		t.Fatal("WriteClaude() accepted a non-JSON blob")
	}
}

func TestReadClaudeFallsBackToServiceOnlyMatch(t *testing.T) {
	items := testutil.FakeKeychain(t)
	// An item filed under a different account name, as a migrated or renamed
	// home directory leaves behind.
	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, "someone-else", sampleBlob)

	got, err := keychain.ReadClaude()
	if err != nil {
		t.Fatalf("ReadClaude(): %v", err)
	}
	if string(got) != sampleBlob {
		t.Fatalf("ReadClaude() = %q, want %q", got, sampleBlob)
	}
}

func TestEnsureMirrorWritesAndRefreshes(t *testing.T) {
	items := testutil.FakeKeychain(t)
	credPath := filepath.Join(t.TempDir(), ".claude", ".credentials.json")

	if _, err := keychain.EnsureMirror(credPath); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("EnsureMirror() with no item = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Fatal("EnsureMirror() wrote a mirror with no item to mirror")
	}

	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), sampleBlob)

	wrote, err := keychain.EnsureMirror(credPath)
	if err != nil {
		t.Fatalf("EnsureMirror(): %v", err)
	}
	if !wrote {
		t.Fatal("EnsureMirror() reported no write for a missing mirror")
	}
	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if string(data) != sampleBlob {
		t.Fatalf("mirror = %q, want %q", data, sampleBlob)
	}
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("stat mirror: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mirror mode = %o, want 600", perm)
	}

	// An unchanged keychain leaves the file alone.
	wrote, err = keychain.EnsureMirror(credPath)
	if err != nil {
		t.Fatalf("EnsureMirror() second call: %v", err)
	}
	if wrote {
		t.Fatal("EnsureMirror() rewrote an already-current mirror")
	}

	// A rotated keychain overwrites the stale mirror.
	rotated := `{"claudeAiOauth":{"accessToken":"at-rotated"}}`
	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), rotated)
	if wrote, err = keychain.EnsureMirror(credPath); err != nil || !wrote {
		t.Fatalf("EnsureMirror() after rotation = (%v, %v), want (true, nil)", wrote, err)
	}
	data, err = os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("read rotated mirror: %v", err)
	}
	if string(data) != rotated {
		t.Fatalf("rotated mirror = %q, want %q", data, rotated)
	}
}

// TestEnsureMirrorMemoizes: a keychain lookup costs a few hundred
// milliseconds and one command reaches the bridge many times, so a refresh
// holds briefly. Writing the fake item directly (rather than through
// FakeKeychainStore, which drops the memo) shows the memo in effect.
func TestEnsureMirrorMemoizes(t *testing.T) {
	items := testutil.FakeKeychain(t)
	credPath := filepath.Join(t.TempDir(), ".credentials.json")
	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), sampleBlob)

	if wrote, err := keychain.EnsureMirror(credPath); err != nil || !wrote {
		t.Fatalf("EnsureMirror() = (%v, %v), want (true, nil)", wrote, err)
	}

	rotated := `{"claudeAiOauth":{"accessToken":"at-rotated"}}`
	if err := os.WriteFile(filepath.Join(items, "Claude_Code_credentials.secret"), []byte(rotated), 0o600); err != nil {
		t.Fatalf("rotate item behind the memo: %v", err)
	}
	if wrote, err := keychain.EnsureMirror(credPath); err != nil || wrote {
		t.Fatalf("EnsureMirror() inside the memo window = (%v, %v), want (false, nil)", wrote, err)
	}
	if got := mustReadFile(t, credPath); got != sampleBlob {
		t.Fatalf("mirror = %q, want the memoized %q", got, sampleBlob)
	}

	keychain.ForgetMirrors()
	if wrote, err := keychain.EnsureMirror(credPath); err != nil || !wrote {
		t.Fatalf("EnsureMirror() after ForgetMirrors = (%v, %v), want (true, nil)", wrote, err)
	}
	if got := mustReadFile(t, credPath); got != rotated {
		t.Fatalf("mirror = %q, want %q", got, rotated)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestPushMirror(t *testing.T) {
	items := testutil.FakeKeychain(t)
	credPath := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(credPath, []byte(sampleBlob+"\n"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	if err := keychain.PushMirror(credPath); err != nil {
		t.Fatalf("PushMirror(): %v", err)
	}
	stored, ok := testutil.FakeKeychainRead(t, items, keychain.ClaudeService)
	if !ok {
		t.Fatal("PushMirror() stored nothing")
	}
	if stored != sampleBlob {
		t.Fatalf("stored = %q, want %q (trailing newline trimmed)", stored, sampleBlob)
	}

	if err := keychain.PushMirror(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("PushMirror() accepted a missing file")
	}
}
