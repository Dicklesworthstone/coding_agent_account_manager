// Package keychain bridges the macOS login keychain, where Claude Code keeps
// its OAuth credentials, into the file-shaped world the rest of caam works in.
//
// On macOS, Claude Code stores its OAuth blob as a generic password
// (service "Claude Code-credentials", account = the login user name) and only
// falls back to ~/.claude/.credentials.json when the keychain is unreachable.
// Every caam code path that hashes, dedupes, or expiry-checks a Claude login
// reads that file, so without this bridge `caam backup` snapshots a profile
// with no token in it and `caam activate` swaps files the CLI ignores.
//
// The keychain is authoritative here; the credentials file is kept as its
// 0600 mirror. That keeps every existing file-shaped path working unchanged.
//
// The item lives in $HOME/Library/Keychains/login.keychain-db: `security`
// derives the keychain search list from HOME, so under an isolated HOME (a
// shallow profile, or a test) there is no login keychain, every entry point
// reports ErrNoKeychain, and callers fall back to files.
package keychain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"
)

var (
	// ErrNotFound means the keychain exists but holds no such item.
	ErrNotFound = errors.New("keychain: item not found")
	// ErrNoKeychain means there is no login keychain to search (non-darwin,
	// an isolated HOME, or the bridge disabled via CAAM_KEYCHAIN=0).
	ErrNoKeychain = errors.New("keychain: no login keychain available")
	// ErrDenied means the keychain refused access: it is locked, the user
	// declined the access prompt, or the process may not prompt.
	ErrDenied = errors.New("keychain: access denied")
)

// defaultBinary is the system tool Claude Code itself shells out to, so items
// caam writes carry the same ownership and access-control list.
const defaultBinary = "/usr/bin/security"

// commandTimeout bounds a `security` invocation. A locked keychain puts up a
// GUI prompt; the budget is generous enough for a user to answer it and short
// enough that a headless run cannot hang forever.
const commandTimeout = 60 * time.Second

// Enabled reports whether the keychain bridge should be attempted at all.
//
// CAAM_KEYCHAIN=0 turns it off (tests set this, and it is the escape hatch for
// a user who would rather caam never touch the keychain). CAAM_KEYCHAIN_BIN
// points at a stand-in `security`, which is how tests exercise the bridge
// without a real keychain — and it makes the bridge available off darwin.
func Enabled() bool {
	if os.Getenv("CAAM_KEYCHAIN") == "0" {
		return false
	}
	if os.Getenv("CAAM_KEYCHAIN_BIN") != "" {
		return true
	}
	return runtime.GOOS == "darwin"
}

func binary() string {
	if bin := os.Getenv("CAAM_KEYCHAIN_BIN"); bin != "" {
		return bin
	}
	return defaultBinary
}

// run executes `security` with the given arguments and classifies its failure.
func run(args ...string) (stdout []byte, err error) {
	if !Enabled() {
		return nil, ErrNoKeychain
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary(), args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("keychain: `security %s` timed out after %s (an unanswered keychain prompt?)", args[0], commandTimeout)
	}
	if runErr == nil {
		return outBuf.Bytes(), nil
	}
	if errors.Is(runErr, exec.ErrNotFound) || os.IsNotExist(runErr) {
		return nil, ErrNoKeychain
	}
	return nil, classify(runErr, errBuf.String())
}

// classify turns `security`'s exit status and diagnostics into one of the
// sentinel errors. The message text is the primary signal: the exit codes are
// truncated OSStatus values and overlap between subcommands.
func classify(runErr error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	lower := strings.ToLower(msg)

	switch {
	// Must precede the item-not-found test: both say "could not be found".
	case strings.Contains(lower, "default keychain could not be found"),
		strings.Contains(lower, "no such keychain"):
		return ErrNoKeychain
	case strings.Contains(lower, "could not be found in the keychain"),
		strings.Contains(lower, "the specified item could not be found"):
		return ErrNotFound
	case strings.Contains(lower, "user interaction is not allowed"),
		strings.Contains(lower, "interaction required"),
		strings.Contains(lower, "denied"),
		strings.Contains(lower, "keychain is locked"),
		strings.Contains(lower, "authorization"),
		strings.Contains(lower, "authentication"):
		return fmt.Errorf("%w: %s", ErrDenied, msg)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 44 {
		// `security` reports errSecItemNotFound as 44 even when it says
		// nothing on stderr.
		return ErrNotFound
	}
	if msg == "" {
		return fmt.Errorf("keychain: security failed: %w", runErr)
	}
	return fmt.Errorf("keychain: security failed: %s", msg)
}

// Get returns the secret stored for (service, account). When account is empty
// the search matches on service alone.
func Get(service, account string) ([]byte, error) {
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, "-w")
	out, err := run(args...)
	if err != nil {
		return nil, err
	}
	// `security -w` terminates the secret with a newline it did not store.
	return bytes.TrimSuffix(out, []byte("\n")), nil
}

// Set stores secret for (service, account), replacing any existing item. The
// replacement is an in-place update (-U), so the item keeps the access-control
// list Claude Code gave it and neither tool prompts the other.
func Set(service, account string, secret []byte) error {
	if account == "" {
		return errors.New("keychain: account is required to write an item")
	}
	if bytes.IndexByte(secret, 0) >= 0 {
		return errors.New("keychain: secret contains a NUL byte")
	}
	_, err := run("add-generic-password", "-U", "-s", service, "-a", account, "-w", string(secret))
	return err
}

// Delete removes the item for (service, account). A missing item is not an
// error.
func Delete(service, account string) error {
	args := []string{"delete-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	_, err := run(args...)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoKeychain) {
		return nil
	}
	return err
}

// LoginAccount is the account name Claude Code files its item under: the login
// user name.
func LoginAccount() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, key := range []string{"USER", "LOGNAME", "USERNAME"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

// validJSONObject reports whether data is a JSON object, the shape both the
// keychain item and the credentials file hold. It is the guard that keeps a
// truncated read or an unrelated item from being mirrored onto disk.
func validJSONObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe map[string]json.RawMessage
	return json.Unmarshal(trimmed, &probe) == nil
}
