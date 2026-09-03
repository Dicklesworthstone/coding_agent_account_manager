//go:build darwin

package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// itemNotFoundExit is errSecItemNotFound, the exit status `security` uses for
// a search or delete that matched nothing.
const itemNotFoundExit = 44

// commandTimeout bounds every `security` call. A locked keychain makes the
// tool wait on a GUI unlock prompt; in a headless or agent context that would
// hang caam indefinitely, so treat a stall as "keychain unavailable" and let
// the caller fall back to the plaintext file.
const commandTimeout = 20 * time.Second

// securityBin resolves the `security` CLI, honouring EnvBin for tests.
func securityBin() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvBin)); override != "" {
		return override, nil
	}
	path, err := exec.LookPath("security")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return path, nil
}

// Available reports whether keychain access is usable right now: darwin, the
// `security` CLI on PATH, and the bridge not switched off.
func Available() bool {
	if disabled() {
		return false
	}
	_, err := securityBin()
	return err == nil
}

func run(args ...string) ([]byte, error) {
	bin, err := securityBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// `security` prompts on the controlling terminal when the keychain is
	// locked. Detach stdin so it fails fast instead of blocking on input that
	// an agent or a script can never supply.
	cmd.Stdin = nil

	err = cmd.Run()
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		return nil, fmt.Errorf("security %s: timed out after %s (keychain locked?)", args[0], commandTimeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == itemNotFoundExit {
			return nil, ErrNotFound
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("security %s: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

// Get returns the secret stored for service/account, or ErrNotFound.
func Get(service, account string) ([]byte, error) {
	if disabled() {
		return nil, ErrUnsupported
	}
	if service == "" || account == "" {
		return nil, ErrNotFound
	}
	out, err := run("find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return nil, err
	}
	// -w prints the secret followed by a newline that is not part of it.
	return bytes.TrimSuffix(out, []byte("\n")), nil
}

// Set stores data for service/account, replacing any existing item.
//
// The secret travels as an argv element, which is visible to `ps` for the
// lifetime of the call. That is how Claude Code writes the same item, and the
// `security` CLI offers no way to hand a generic password over a pipe
// non-interactively.
func Set(service, account string, data []byte) error {
	if disabled() {
		return ErrUnsupported
	}
	if service == "" || account == "" {
		return errors.New("keychain: service and account are required")
	}
	_, err := run("add-generic-password", "-U", "-s", service, "-a", account, "-w", string(data))
	return err
}

// Delete removes the item for service/account. A missing item is not an error.
func Delete(service, account string) error {
	if disabled() {
		return ErrUnsupported
	}
	if service == "" || account == "" {
		return nil
	}
	_, err := run("delete-generic-password", "-s", service, "-a", account)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
