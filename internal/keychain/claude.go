package keychain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ClaudeService is the generic-password service name Claude Code uses for its
// OAuth credentials on macOS. The account attribute is the login name (see
// CurrentAccount), and the secret is byte-for-byte the same JSON document that
// Claude Code writes to ~/.claude/.credentials.json on platforms without a
// keychain: {"claudeAiOauth":{"accessToken":...,"refreshToken":...}}.
const ClaudeService = "Claude Code-credentials"

// ClaudeCredentials returns the credential blob currently in the keychain.
// ok is false when the bridge is unavailable, no item exists, or the item does
// not hold JSON (a value caam must not copy into a credentials file).
func ClaudeCredentials() (data []byte, ok bool) {
	if !Available() {
		return nil, false
	}
	data, err := Get(ClaudeService, CurrentAccount())
	if err != nil || len(data) == 0 || !json.Valid(data) {
		return nil, false
	}
	return data, true
}

// MirrorClaudeCredentials copies the keychain item into path, the plaintext
// credentials file the rest of caam treats as the source of truth (hashing,
// identity extraction, active-profile detection, expiry checks).
//
// The keychain is authoritative on macOS: Claude Code rotates the tokens there
// in place, so the mirror is rewritten whenever the two differ, and a stale
// mirror never wins. Returns true when path now matches the keychain.
func MirrorClaudeCredentials(path string) bool {
	if path == "" {
		return false
	}
	data, ok := ClaudeCredentials()
	if !ok {
		return false
	}
	if existing, err := os.ReadFile(path); err == nil && bytesEqualJSON(existing, data) {
		return true
	}
	if err := writeSecretFile(path, data); err != nil {
		return false
	}
	return true
}

// StoreClaudeCredentials pushes the credentials file at path into the
// keychain, which is what makes an account switch visible to Claude Code on
// macOS: restoring the file alone changes nothing, because the CLI reads the
// keychain first.
//
// Returns nil when there is nothing to do (bridge unavailable, no such file,
// or the file is not JSON), and an error only when the keychain write itself
// fails — a case the caller must surface, since silently skipping it leaves
// the previous account live.
func StoreClaudeCredentials(path string) error {
	if !Available() || path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || !json.Valid(data) {
		return nil
	}
	if current, ok := ClaudeCredentials(); ok && bytesEqualJSON(current, data) {
		return nil
	}
	if err := Set(ClaudeService, CurrentAccount(), data); err != nil {
		return fmt.Errorf("store claude credentials in macOS keychain: %w", err)
	}
	return nil
}

// ClearClaudeCredentials removes the keychain item (logout). A missing item or
// an unavailable bridge is not an error.
func ClearClaudeCredentials() error {
	if !Available() {
		return nil
	}
	if err := Delete(ClaudeService, CurrentAccount()); err != nil {
		return fmt.Errorf("clear claude credentials from macOS keychain: %w", err)
	}
	return nil
}

// bytesEqualJSON compares two credential blobs ignoring trailing whitespace,
// so a mirror that only differs by a trailing newline is not rewritten.
func bytesEqualJSON(a, b []byte) bool {
	return string(trimSpace(a)) == string(trimSpace(b))
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// writeSecretFile writes data to path atomically with 0600 permissions.
func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".caam-cred-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
