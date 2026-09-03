package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// fakeSecurityScript emulates the subset of /usr/bin/security that
// internal/keychain drives, backed by one file per item in a temp directory.
// It reproduces the two behaviours the real tool's callers depend on: `-w`
// prints the secret followed by a newline, and a missing item exits 44
// (errSecItemNotFound).
const fakeSecurityScript = `#!/bin/sh
dir="$CAAM_TEST_KEYCHAIN_DIR"
cmd="$1"
shift
svc=""
acct=""
pw=""
while [ $# -gt 0 ]; do
  case "$1" in
    -s) svc="$2"; shift 2 ;;
    -a) acct="$2"; shift 2 ;;
    -w)
      if [ "$cmd" = "add-generic-password" ] && [ $# -ge 2 ]; then
        pw="$2"; shift 2
      else
        shift
      fi
      ;;
    *) shift ;;
  esac
done
file="$dir/$(printf '%s' "${svc}__${acct}" | tr '/ ' '__')"
case "$cmd" in
  find-generic-password)
    [ -f "$file" ] || exit 44
    cat "$file"
    echo
    ;;
  add-generic-password)
    mkdir -p "$dir"
    printf '%s' "$pw" > "$file"
    ;;
  delete-generic-password)
    [ -f "$file" ] || exit 44
    rm -f "$file"
    ;;
  *) exit 1 ;;
esac
`

// FakeKeychain points internal/keychain at a stub `security` binary backed by
// a temp directory and switches the bridge on for the duration of the test.
// It skips on non-darwin, where the bridge is compiled out.
//
// Tests need this because the real keychain is machine-global: IsolateEnv
// disables the bridge outright so no test can read or overwrite the
// developer's actual Claude Code tokens.
func FakeKeychain(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("keychain bridge is darwin-only")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "security")
	if err := os.WriteFile(script, []byte(fakeSecurityScript), 0700); err != nil {
		t.Fatalf("write fake security: %v", err)
	}

	t.Setenv("CAAM_TEST_KEYCHAIN_DIR", filepath.Join(dir, "items"))
	t.Setenv(keychain.EnvBin, script)
	t.Setenv(keychain.EnvDisable, "1")
}

// SetKeychainItem stores a secret in the fake keychain prepared by
// FakeKeychain.
func SetKeychainItem(t *testing.T, service, account, secret string) {
	t.Helper()
	if err := keychain.Set(service, account, []byte(secret)); err != nil {
		t.Fatalf("seed keychain item %s/%s: %v", service, account, err)
	}
}

// KeychainItem returns the secret stored for service/account, and whether an
// item exists at all.
func KeychainItem(t *testing.T, service, account string) (string, bool) {
	t.Helper()
	data, err := keychain.Get(service, account)
	if err != nil {
		return "", false
	}
	return string(data), true
}
