package keychain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ClaudeService is the generic-password service name Claude Code files its
// OAuth blob under in the macOS login keychain.
const ClaudeService = "Claude Code-credentials"

// ReadClaude returns the Claude Code OAuth blob from the login keychain.
//
// It looks for the login user's item first and falls back to a service-only
// match, so a keychain written under a different account name (a migrated home
// directory, a renamed user) is still found.
func ReadClaude() ([]byte, error) {
	account := LoginAccount()
	secret, err := Get(ClaudeService, account)
	if errors.Is(err, ErrNotFound) && account != "" {
		secret, err = Get(ClaudeService, "")
	}
	if err != nil {
		return nil, err
	}
	if !validJSONObject(secret) {
		return nil, fmt.Errorf("keychain: item %q does not hold a JSON object", ClaudeService)
	}
	return secret, nil
}

// WriteClaude stores blob as the Claude Code OAuth item, replacing whatever is
// there. blob must be the JSON object Claude Code expects.
func WriteClaude(blob []byte) error {
	if !validJSONObject(blob) {
		return errors.New("keychain: refusing to store credentials that are not a JSON object")
	}
	account := LoginAccount()
	if account == "" {
		return errors.New("keychain: cannot determine the login user name")
	}
	return Set(ClaudeService, account, blob)
}

// DeleteClaude removes the Claude Code OAuth item. A missing item, or no
// keychain at all, is not an error.
func DeleteClaude() error {
	ForgetMirrors()
	if err := Delete(ClaudeService, LoginAccount()); err != nil {
		return err
	}
	// A service-only sweep catches an item filed under another account name.
	return Delete(ClaudeService, "")
}

// EnsureMirror refreshes credPath from the login keychain so the file-shaped
// code paths (hashing, identity, expiry) see the credentials that are actually
// in force. The file is written 0600, atomically, and only when its contents
// differ from the keychain.
//
// It reports whether the mirror was written. ErrNoKeychain and ErrNotFound are
// returned as-is: they mean "nothing to bridge here" (a non-darwin host, an
// isolated HOME, or a login that predates the keychain), and every caller but
// backup treats them as a no-op.
func EnsureMirror(credPath string) (bool, error) {
	if !Enabled() {
		return false, ErrNoKeychain
	}
	if err, ok := cachedMirror(credPath); ok {
		// The mirror was refreshed a moment ago, so nothing was written now.
		return false, err
	}
	wrote, err := ensureMirror(credPath)
	rememberMirror(credPath, err)
	return wrote, err
}

// mirrorTTL is how long a mirror refresh is assumed to still hold. A keychain
// lookup costs a few hundred milliseconds and a single command can reach
// HasAuthFiles and ActiveProfile many times over; without the memo a `caam
// status` would spend seconds in /usr/bin/security. Short enough that a
// long-lived daemon still sees a rotated token promptly.
const mirrorTTL = 3 * time.Second

type mirrorResult struct {
	at  time.Time
	err error
}

var (
	mirrorMu    sync.Mutex
	mirrorCache = map[string]mirrorResult{}
)

func cachedMirror(credPath string) (err error, ok bool) {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	res, hit := mirrorCache[credPath]
	if !hit || time.Since(res.at) > mirrorTTL {
		return nil, false
	}
	return res.err, true
}

func rememberMirror(credPath string, err error) {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	mirrorCache[credPath] = mirrorResult{at: time.Now(), err: err}
}

// forgetMirror drops the memo for credPath, so the next EnsureMirror re-reads
// the keychain. Called whenever caam itself changes the item.
func forgetMirror(credPath string) {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	delete(mirrorCache, credPath)
}

// ForgetMirrors clears every memoized mirror refresh. Tests call it so one
// test's fake keychain cannot answer for the next.
func ForgetMirrors() {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	mirrorCache = map[string]mirrorResult{}
}

func ensureMirror(credPath string) (bool, error) {
	blob, err := ReadClaude()
	if err != nil {
		return false, err
	}
	if existing, readErr := os.ReadFile(credPath); readErr == nil && bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(blob)) {
		return false, nil
	}
	if err := writeSecretFile(credPath, blob); err != nil {
		return false, err
	}
	return true, nil
}

// PushMirror writes credPath's contents back into the login keychain, making
// the restored profile the account Claude Code will actually use.
func PushMirror(credPath string) error {
	if !Enabled() {
		return ErrNoKeychain
	}
	blob, err := os.ReadFile(credPath)
	if err != nil {
		return fmt.Errorf("keychain: read %s: %w", credPath, err)
	}
	// caam is about to change the item, so the memoized refresh no longer
	// describes it.
	forgetMirror(credPath)
	return WriteClaude(bytes.TrimSpace(blob))
}

// writeSecretFile writes data to path atomically with 0600 permissions.
func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("keychain: create %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".credentials.json.tmp.*")
	if err != nil {
		return fmt.Errorf("keychain: create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("keychain: write %s: %w", tmp, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("keychain: chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("keychain: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("keychain: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("keychain: rename onto %s: %w", path, err)
	}
	return nil
}
