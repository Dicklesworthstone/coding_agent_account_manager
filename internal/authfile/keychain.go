package authfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// This file bridges the macOS login keychain into the file-shaped auth-file
// model. On macOS, Claude Code keeps its OAuth blob as a generic password and
// only falls back to ~/.claude/.credentials.json when the keychain is
// unreachable, so without the bridge `Backup` snapshots a profile with no
// token and `Restore` swaps files the CLI ignores (issue #98).
//
// The keychain is authoritative and the credentials file is its mirror: every
// existing path that hashes, dedupes, or expiry-checks the file keeps working
// untouched. On a host with no login keychain (non-darwin, an isolated HOME,
// CAAM_KEYCHAIN=0) each helper is inert.

// claudeKeychainPath returns the credentials file the login keychain should be
// bridged to, or "" when the bridge does not apply to this file set.
//
// The item `security` reaches lives in $HOME/Library/Keychains, so it belongs
// to the credentials file under the *current* HOME and to no other. A file set
// pointing somewhere else — a profile-scoped HOME, a fixture — is left to the
// files it names, which is also what makes shallow profiles keep working: they
// run under their own HOME, which has no login keychain.
func claudeKeychainPath(fileSet AuthFileSet) string {
	// Explicit config directories use their own native keychain namespace.
	// The default login item must never be mirrored into that account, even
	// when the override happens to name the legacy ~/.claude directory.
	if fileSet.Tool != "claude" || os.Getenv("CLAUDE_CONFIG_DIR") != "" || !keychain.Enabled() {
		return ""
	}
	credPath := claudeFileSetPath(fileSet, claudeCredentialsFile)
	if credPath == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if filepath.Clean(credPath) != filepath.Join(home, ".claude", claudeCredentialsFile) {
		return ""
	}
	return credPath
}

// pullClaudeKeychain refreshes the live ~/.claude/.credentials.json mirror
// from the login keychain before a caller reads it.
//
// It returns nil when there is nothing to bridge — no keychain, no item, or a
// file set that does not register the credentials file — and a descriptive
// error only when the keychain refused access or the mirror could not be
// written. Callers that merely inspect state ignore the error; the ones that
// capture or hand over credentials surface it.
func pullClaudeKeychain(fileSet AuthFileSet) error {
	credPath := claudeKeychainPath(fileSet)
	if credPath == "" {
		return nil
	}
	if _, err := keychain.EnsureMirror(credPath); err != nil {
		if errors.Is(err, keychain.ErrNoKeychain) || errors.Is(err, keychain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read Claude credentials from the macOS login keychain: %w", err)
	}
	return nil
}

// pushClaudeKeychain checks Restore's final publication postcondition. The
// recoverable batch now publishes the item itself; a second write here could
// overwrite a native rotation after the rollback copies have been released.
func pushClaudeKeychain(fileSet AuthFileSet) error {
	credPath := claudeKeychainPath(fileSet)
	if credPath == "" {
		return nil
	}
	live, err := readRetiredClaudeCredential(credPath)
	if err != nil {
		return err
	}
	current, err := readClaudeKeychainForPublication()
	if errors.Is(err, keychain.ErrNoKeychain) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameClaudeAuthority(live, current) {
		return fmt.Errorf("Claude keychain or credential changed after publication; left untouched")
	}
	return nil
}

func prepareClaudeKeychainPublication(fileSet AuthFileSet) (*claudeRestoreAuthority, error) {
	path := claudeKeychainPath(fileSet)
	if path == "" {
		return nil, nil
	}
	// The restore batch matches its credential destination against this path,
	// and that destination has its parent symlinks resolved (#120).
	authority, err := prepareClaudeRestoreAuthority(resolveLiveParent(path), readClaudeKeychainForPublication, func(data []byte) error {
		keychain.ForgetMirrors()
		if data == nil {
			return keychain.DeleteClaude()
		}
		return keychain.WriteClaude(data)
	})
	if errors.Is(err, keychain.ErrNoKeychain) {
		// An isolated HOME can use file-backed credentials without a login
		// keychain. Only initial preparation may omit the publication authority.
		keychain.ForgetMirrors()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Restore's subsequent pull must not reuse a status command's cached disk
	// mirror when the authoritative grant rotated since that inspection.
	keychain.ForgetMirrors()
	return authority, nil
}

// A missing item is an available, empty authority that can receive a login.
// Keep an unavailable keychain typed so later checks on a captured authority
// cannot mistake its disappearance for proof that the item is still absent.
func readClaudeKeychainForPublication() ([]byte, error) {
	data, err := keychain.ReadClaude()
	if errors.Is(err, keychain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Claude keychain for credential publication: %w", err)
	}
	return data, nil
}

// Retirement preflight reads, but does not mirror or mutate, the authoritative
// keychain. The callback also detects a native keychain rotation before Apply.
func claudeRetirementMirror(fileSet AuthFileSet, path string) func() ([]byte, error) {
	bridge := claudeKeychainPath(fileSet)
	if bridge == "" || filepath.Clean(bridge) != filepath.Clean(path) {
		return nil
	}
	return func() ([]byte, error) {
		data, err := keychain.ReadClaude()
		if errors.Is(err, keychain.ErrNoKeychain) || errors.Is(err, keychain.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read Claude keychain before credential retirement: %w", err)
		}
		return data, nil
	}
}

// clearClaudeKeychain removes the Claude item as part of a logout.
func clearClaudeKeychain(fileSet AuthFileSet) error {
	if claudeKeychainPath(fileSet) == "" {
		return nil
	}
	if err := keychain.DeleteClaude(); err != nil {
		return fmt.Errorf("remove Claude credentials from the macOS login keychain: %w", err)
	}
	return nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
