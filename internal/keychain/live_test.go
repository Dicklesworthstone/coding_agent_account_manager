package keychain_test

// An opt-in round trip against the REAL login keychain, in the style of
// e2e_agy_live_test.go. It is skipped unless CAAM_KEYCHAIN_LIVE_TEST=1 so the
// normal suite never touches machine-global state.
//
//	CAAM_KEYCHAIN_LIVE_TEST=1 go test ./internal/keychain/ -run TestLive -v
//
// It only ever writes and deletes an item under a unique, obviously-disposable
// service name; it never reads, writes, or deletes Claude Code's own item.

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

func TestLiveKeychainRoundTrip(t *testing.T) {
	if os.Getenv("CAAM_KEYCHAIN_LIVE_TEST") != "1" {
		t.Skip("set CAAM_KEYCHAIN_LIVE_TEST=1 to run against the real login keychain")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the login keychain is macOS-only")
	}

	// The isolated test HOME has no login keychain, so point HOME back at the
	// real one for this test only.
	// user.Current() reads the passwd database, so it still points at the real
	// home under the isolated test HOME.
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("cannot resolve the real home directory")
	}
	realHome := u.HomeDir
	t.Setenv("HOME", realHome)
	t.Setenv("CAAM_KEYCHAIN", "1")
	t.Setenv("CAAM_KEYCHAIN_BIN", "")

	service := fmt.Sprintf("caam-keychain-live-test-%d", os.Getpid())
	account := keychain.LoginAccount()
	if account == "" {
		t.Fatal("cannot determine the login account name")
	}
	t.Cleanup(func() {
		if err := keychain.Delete(service, account); err != nil {
			t.Errorf("cleanup: delete %s: %v", service, err)
		}
	})

	if _, err := keychain.Get(service, account); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("Get() before the item exists = %v, want ErrNotFound", err)
	}

	secret := []byte(`{"caam":"live-test","secret":false}`)
	if err := keychain.Set(service, account, secret); err != nil {
		t.Fatalf("Set(): %v", err)
	}
	got, err := keychain.Get(service, account)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if string(got) != string(secret) {
		t.Fatalf("Get() = %q, want %q", got, secret)
	}

	// -U replaces the payload of an existing item without an error.
	updated := []byte(`{"caam":"live-test","round":2}`)
	if err := keychain.Set(service, account, updated); err != nil {
		t.Fatalf("Set() over an existing item: %v", err)
	}
	if got, err = keychain.Get(service, account); err != nil || string(got) != string(updated) {
		t.Fatalf("Get() after update = (%q, %v)", got, err)
	}

	if err := keychain.Delete(service, account); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if _, err := keychain.Get(service, account); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("Get() after delete = %v, want ErrNotFound", err)
	}
}
