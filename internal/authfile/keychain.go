package authfile

import (
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// --- macOS Keychain bridge for Claude Code ----------------------------------
//
// On macOS, Claude Code keeps its OAuth tokens in the login keychain
// ("Claude Code-credentials"), not in ~/.claude/.credentials.json. Without a
// bridge, every file-shaped operation in this package degrades on a Mac:
// Backup writes a vault profile with no credentials, ActiveProfile cannot tell
// two accounts apart, HasAuthFiles reads as logged out, and Restore swaps
// dotfiles the CLI ignores while the keychain re-asserts the previous account.
//
// The bridge keeps the keychain authoritative and the file a mirror of it:
//   - before reading live state, copy keychain -> ~/.claude/.credentials.json
//   - after restoring a profile, copy ~/.claude/.credentials.json -> keychain
//   - on clear, delete both
//
// Every helper is a no-op on platforms without a keychain, so the callers stay
// platform-neutral. See internal/keychain.

// claudeCredentialsLivePath returns the live credentials path in a Claude
// file set, or "" for any other tool.
func claudeCredentialsLivePath(fileSet AuthFileSet) string {
	if fileSet.Tool != "claude" {
		return ""
	}
	return claudeFileSetPath(fileSet, claudeCredentialsFile)
}

// syncClaudeKeychainIn refreshes the live credentials file from the keychain
// so the rest of this package sees the tokens Claude Code is actually using.
// Best-effort: a locked or empty keychain leaves whatever is on disk.
func syncClaudeKeychainIn(fileSet AuthFileSet) {
	if path := claudeCredentialsLivePath(fileSet); path != "" {
		keychain.MirrorClaudeCredentials(path)
	}
}

// syncClaudeKeychainOut pushes the restored credentials file into the
// keychain. This is the step that makes `caam activate` take effect on macOS.
func syncClaudeKeychainOut(fileSet AuthFileSet) error {
	path := claudeCredentialsLivePath(fileSet)
	if path == "" {
		return nil
	}
	return keychain.StoreClaudeCredentials(path)
}

// clearClaudeKeychain removes the keychain item during logout, so the CLI does
// not re-authenticate as the account whose file was just deleted.
func clearClaudeKeychain(fileSet AuthFileSet) error {
	if claudeCredentialsLivePath(fileSet) == "" {
		return nil
	}
	return keychain.ClearClaudeCredentials()
}
