// Package keychain reads and writes generic-password items in the macOS login
// keychain.
//
// Why caam needs it: on macOS, Claude Code does not keep its OAuth tokens in
// ~/.claude/.credentials.json the way it does on Linux. It stores them in the
// login keychain as a generic password under the service
// "Claude Code-credentials", with the account set to the current login name,
// and only falls back to the plaintext file when the keychain is unreachable.
// A caam that swaps files alone therefore captures no token on a Mac: `caam
// backup` writes a vault profile with no .credentials.json, and `caam
// activate` restores dotfiles the CLI ignores while the keychain quietly
// re-asserts the previous account.
//
// The implementation shells out to /usr/bin/security, which is exactly what
// Claude Code itself does (`security add-generic-password -U -a ...`), so the
// items caam writes carry the same ownership and ACL as the ones the CLI
// writes and neither side triggers an authorization prompt for the other.
//
// Every entry point is a no-op returning ErrUnsupported off darwin, so callers
// can invoke them unconditionally.
package keychain

import (
	"errors"
	"os"
	"os/user"
	"strings"
)

var (
	// ErrUnsupported is returned when the platform has no keychain, or when
	// the keychain bridge is switched off with CAAM_KEYCHAIN=0.
	ErrUnsupported = errors.New("keychain: unsupported on this platform")

	// ErrNotFound is returned when no item matches the service/account pair.
	ErrNotFound = errors.New("keychain: item not found")
)

// EnvDisable turns the bridge off when set to a false-y value. The test
// harness sets it (see internal/testutil), because keychain state is
// machine-global and would otherwise leak the developer's real tokens into
// tests that run under an isolated HOME.
const EnvDisable = "CAAM_KEYCHAIN"

// EnvBin overrides the `security` binary. Tests point it at a stub.
const EnvBin = "CAAM_KEYCHAIN_BIN"

// disabled reports whether CAAM_KEYCHAIN switches the bridge off.
func disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDisable))) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// CurrentAccount returns the login name Claude Code uses as the keychain
// item's account attribute.
func CurrentAccount() string {
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return u.Username
	}
	for _, key := range []string{"USER", "LOGNAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
