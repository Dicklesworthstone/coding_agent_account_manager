package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// fakeSecurity is a stand-in for /usr/bin/security. It stores each generic
// password as a file under $CAAM_FAKE_KEYCHAIN_DIR named after the service and
// account, and reproduces the three behaviors caam depends on:
//
//   - find-generic-password -w prints the secret plus a newline
//   - a missing item exits 44 with the real diagnostic on stderr
//   - add-generic-password -U overwrites an existing item in place
//
// Setting CAAM_FAKE_KEYCHAIN_LOCKED=1 makes every lookup fail the way a locked
// keychain does, which is how the denied path is exercised.
const fakeSecurity = `#!/bin/sh
dir="$CAAM_FAKE_KEYCHAIN_DIR"
mkdir -p "$dir"

cmd="$1"
shift

service=""
account=""
secret=""
want_secret=0
while [ $# -gt 0 ]; do
  case "$1" in
    -s) service="$2"; shift 2 ;;
    -a) account="$2"; shift 2 ;;
    -w)
      if [ "$cmd" = "add-generic-password" ]; then
        secret="$2"
        shift 2
      else
        want_secret=1
        shift
      fi
      ;;
    -U|-g) shift ;;
    *) shift ;;
  esac
done

if [ "$CAAM_FAKE_KEYCHAIN_LOCKED" = "1" ]; then
  echo "security: SecKeychainSearchCopyNext: User interaction is not allowed." >&2
  exit 36
fi

# Items are keyed by service; the account is stored alongside so a
# service-only lookup still resolves one.
slug=$(printf '%s' "$service" | tr -c 'A-Za-z0-9' '_')
item="$dir/$slug.secret"
acctfile="$dir/$slug.account"

case "$cmd" in
  find-generic-password)
    if [ ! -f "$item" ]; then
      echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2
      exit 44
    fi
    if [ -n "$account" ] && [ -f "$acctfile" ] && [ "$account" != "$(cat "$acctfile")" ]; then
      echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2
      exit 44
    fi
    if [ "$want_secret" = "1" ]; then
      cat "$item"
      echo ""
    fi
    exit 0
    ;;
  add-generic-password)
    printf '%s' "$secret" > "$item"
    printf '%s' "$account" > "$acctfile"
    exit 0
    ;;
  delete-generic-password)
    if [ ! -f "$item" ]; then
      echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2
      exit 44
    fi
    if [ -n "$account" ] && [ -f "$acctfile" ] && [ "$account" != "$(cat "$acctfile")" ]; then
      echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2
      exit 44
    fi
    rm -f "$item" "$acctfile"
    exit 0
    ;;
esac

echo "security: unknown command $cmd" >&2
exit 1
`

// FakeKeychain enables the keychain bridge for one test against an in-tree
// stand-in for /usr/bin/security, so the developer's real login keychain is
// never read or written. It returns the directory holding the fake items.
//
// The stand-in is a shell script, so the helper skips on Windows.
func FakeKeychain(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("keychain bridge test needs a POSIX shell")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "security")
	if err := os.WriteFile(bin, []byte(fakeSecurity), 0o755); err != nil {
		t.Fatalf("write fake security: %v", err)
	}
	items := filepath.Join(dir, "items")
	if err := os.MkdirAll(items, 0o700); err != nil {
		t.Fatalf("create fake keychain dir: %v", err)
	}

	t.Setenv("CAAM_KEYCHAIN", "1")
	t.Setenv("CAAM_KEYCHAIN_BIN", bin)
	t.Setenv("CAAM_FAKE_KEYCHAIN_DIR", items)

	// EnsureMirror memoizes a refresh for a few seconds; without this one
	// test's keychain would answer for the next.
	keychain.ForgetMirrors()
	t.Cleanup(keychain.ForgetMirrors)
	return items
}

// FakeKeychainStore writes an item into the fake keychain as if the tool that
// owns it had put it there.
func FakeKeychainStore(t *testing.T, dir, service, account, secret string) {
	t.Helper()
	base := filepath.Join(dir, slugify(service))
	if err := os.WriteFile(base+".secret", []byte(secret), 0o600); err != nil {
		t.Fatalf("seed fake keychain item: %v", err)
	}
	if err := os.WriteFile(base+".account", []byte(account), 0o600); err != nil {
		t.Fatalf("seed fake keychain account: %v", err)
	}
	// The item changed behind caam's back, as a `claude` login would change it.
	keychain.ForgetMirrors()
}

// FakeKeychainRead returns the secret stored for service, and whether an item
// exists at all.
func FakeKeychainRead(t *testing.T, dir, service string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, slugify(service)+".secret"))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read fake keychain item: %v", err)
	}
	return string(data), true
}

// slugify mirrors the `tr -c 'A-Za-z0-9' '_'` the fake binary uses to turn a
// service name into a file name.
func slugify(s string) string {
	out := []byte(s)
	for i, c := range out {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		default:
			out[i] = '_'
		}
	}
	return string(out)
}
