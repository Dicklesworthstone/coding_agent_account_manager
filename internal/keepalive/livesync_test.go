package keepalive

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// liveSyncFixture builds a synthetic host Grok login and a user snapshot of it.
func liveSyncFixture(t *testing.T, savedOwner AccountIdentity, savedExpiry time.Duration) (home string, vault *authfile.Vault, savedPath string, saved []byte) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", "")
	owner := AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}
	liveExpiry := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)
	writeSyncFile(t, filepath.Join(home, ".grok", "auth.json"), syncCredentialJSON(t, "grok", "live-access", owner, liveExpiry))
	vault = authfile.NewVault(filepath.Join(root, "vault"))
	dir := vault.ProfilePath("grok", "live")
	savedPath = filepath.Join(dir, "auth.json")
	saved = syncCredentialJSON(t, "grok", "saved-access", savedOwner, liveExpiry.Add(savedExpiry))
	writeSyncFile(t, savedPath, saved)
	writeSyncFile(t, filepath.Join(dir, "meta.json"), []byte(`{"type":"user"}`))
	return home, vault, savedPath, saved
}

func TestSyncVaultFromLiveRefreshesStaleSnapshotOfTheSameAccount(t *testing.T) {
	home, vault, savedPath, _ := liveSyncFixture(t, AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, -4*time.Hour)
	results, err := SyncVaultFromLive(context.Background(), vault, DiscoverOptions{Home: home, ProfilesPath: filepath.Join(t.TempDir(), "profiles")})
	if err != nil {
		t.Fatal(err)
	}
	if Synced(results) != 1 {
		t.Fatalf("results = %+v; want one synced profile", results)
	}
	live := readSyncFile(t, filepath.Join(home, ".grok", "auth.json"))
	if got := readSyncFile(t, savedPath); !bytes.Equal(got, live) {
		t.Fatal("saved credential does not equal the live credential")
	}
	if info, err := os.Stat(savedPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("saved credential permissions = %v, %v; want 0600", info, err)
	}
	// A second pass has nothing newer to copy and must not rewrite the file.
	before, _ := os.Stat(savedPath)
	results, err = SyncVaultFromLive(context.Background(), vault, DiscoverOptions{Home: home, ProfilesPath: filepath.Join(t.TempDir(), "profiles")})
	if err != nil || Synced(results) != 0 {
		t.Fatalf("second pass = %+v, %v; want no sync", results, err)
	}
	if after, _ := os.Stat(savedPath); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("up-to-date snapshot was rewritten")
	}
}

func TestSyncVaultFromLiveLeavesTheVaultAloneWhenNothingIsProven(t *testing.T) {
	cases := []struct {
		name  string
		owner AccountIdentity
		age   time.Duration
		live  []byte // replaces the live file when set
	}{
		{name: "different account", owner: AccountIdentity{AccountID: "account-b", Email: "bob@example.test"}, age: -4 * time.Hour},
		{name: "saved copy is newer", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, age: time.Hour},
		{name: "live credential expired", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, age: -4 * time.Hour,
			live: []byte(`{"synthetic-issuer::client":{"key":"x","refresh_token":"y","user_id":"account-a","expires_at":"2020-01-01T00:00:00Z"}}`)},
		{name: "live credential unreadable", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, age: -4 * time.Hour, live: []byte(`not json`)},
		{name: "live credential has no refresh token", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, age: -4 * time.Hour,
			live: []byte(`{"synthetic-issuer::client":{"key":"x","user_id":"account-a","expires_at":"2099-01-01T00:00:00Z"}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, vault, savedPath, saved := liveSyncFixture(t, tc.owner, tc.age)
			if tc.live != nil {
				writeSyncFile(t, filepath.Join(home, ".grok", "auth.json"), tc.live)
			}
			results, _ := SyncVaultFromLive(context.Background(), vault, DiscoverOptions{Home: home, ProfilesPath: filepath.Join(t.TempDir(), "profiles")})
			if Synced(results) != 0 {
				t.Fatalf("results = %+v; want no sync", results)
			}
			if got := readSyncFile(t, savedPath); !bytes.Equal(got, saved) {
				t.Fatal("saved credential changed without a proven newer same-account login")
			}
		})
	}
}

func TestSyncVaultFromLiveWithoutVaultIsAnError(t *testing.T) {
	if _, err := SyncVaultFromLive(context.Background(), nil, DiscoverOptions{}); err == nil {
		t.Fatal("nil vault must be an error")
	}
}
