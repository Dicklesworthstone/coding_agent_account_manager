package authfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

// A present OAuth snapshot is part of the same recoverable operation as mixed
// settings, not a subsequent unguarded copy. The initial disk bytes and the
// captured authoritative mirror are the only accepted pre-install generations.
type claudeCredentialRestore struct {
	snapshot, live string
	before, target []byte
	mirror         []byte
	readMirror     func() ([]byte, error)
	sources        map[string][]byte
	keepLive       func() bool
}

func prepareClaudeCredentialRestore(snapshot, live string, sources map[string][]byte, mirror func() ([]byte, error), keepLive func() bool) (*claudeCredentialRestore, error) {
	if snapshot == "" || live == "" {
		return nil, fmt.Errorf("Claude credential restore requires snapshot and live paths")
	}
	var err error
	snapshot, err = filepath.Abs(snapshot)
	if err != nil {
		return nil, err
	}
	live, err = filepath.Abs(live)
	if err != nil {
		return nil, err
	}
	if err := claudesettings.CheckPrivate(live, snapshot); err != nil {
		return nil, err
	}
	target, err := readRetiredClaudeCredential(snapshot)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, fmt.Errorf("Claude credential snapshot disappeared during preparation")
	}
	before, err := readRetiredClaudeCredential(live)
	if err != nil {
		return nil, err
	}
	plan := &claudeCredentialRestore{
		snapshot: snapshot, live: live, before: before, target: target,
		readMirror: mirror, keepLive: keepLive, sources: make(map[string][]byte, len(sources)+1),
	}
	for path, data := range sources {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		plan.sources[abs] = bytes.Clone(data)
	}
	plan.sources[snapshot] = bytes.Clone(target)
	if mirror != nil {
		plan.mirror, err = mirror()
		if err != nil {
			return nil, err
		}
		plan.mirror = bytes.Clone(plan.mirror)
	}
	return plan, nil
}

func (plan *claudeCredentialRestore) checkUnchanged() error {
	for path, before := range plan.sources {
		current, err := claudesettings.Read(path)
		if err != nil {
			return err
		}
		if (current == nil) != (before == nil) || !bytes.Equal(current, before) {
			return fmt.Errorf("Claude credential or identity source changed during activation; retry")
		}
	}
	current, err := readRetiredClaudeCredential(plan.live)
	if err != nil {
		return err
	}
	unchanged := (current == nil) == (plan.before == nil) && bytes.Equal(current, plan.before)
	mirrored := plan.mirror != nil && current != nil && sameClaudeAuthority(current, plan.mirror)
	if !unchanged && !mirrored {
		return fmt.Errorf("live Claude credential changed before replacement; retry")
	}
	if plan.readMirror != nil {
		mirror, err := plan.readMirror()
		if err != nil {
			return err
		}
		if !sameClaudeAuthority(mirror, plan.mirror) {
			return fmt.Errorf("Claude keychain changed before credential replacement; retry")
		}
	}
	return nil
}

func (plan *claudeCredentialRestore) prepareUpdate() (*claudesettings.Update, []byte, error) {
	if err := plan.checkUnchanged(); err != nil {
		return nil, nil, err
	}
	current, err := readRetiredClaudeCredential(plan.live)
	if err != nil {
		return nil, nil, err
	}
	after := plan.target
	if plan.keepLive != nil && plan.keepLive() {
		after = current
	}
	update, err := claudesettings.PrepareCredentialReplacement(plan.snapshot, plan.live, current, after, plan.sources, plan.checkUnchanged)
	if err != nil {
		return nil, nil, err
	}
	return update, bytes.Clone(after), nil
}

func (plan *claudeCredentialRestore) Apply() error {
	update, _, err := plan.prepareUpdate()
	if err != nil {
		return err
	}
	return claudesettings.ApplyUpdates([]*claudesettings.Update{update})
}

// claudeRestoreAuthority publishes the prepared primary credential to an
// external authority after the files are installed. The native keychain has no
// compare-and-swap API: detect changes, never knowingly replace a native login,
// and preserve an explicit recovery copy whenever the outcome is uncertain.
// Function injection is per operation; no global mutable test hooks are used.
type claudeRestoreAuthority struct {
	path         string
	before, want []byte
	read         func() ([]byte, error)
	write        func([]byte) error // nil removes the item
	backup       string
	keepBackup   bool
}

func prepareClaudeRestoreAuthority(path string, read func() ([]byte, error), write func([]byte) error) (*claudeRestoreAuthority, error) {
	if path == "" || read == nil || write == nil {
		return nil, fmt.Errorf("Claude credential publication requires a path, reader and writer")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	before, err := read()
	if err != nil {
		return nil, err
	}
	return &claudeRestoreAuthority{path: path, before: bytes.Clone(before), read: read, write: write}, nil
}

func sameClaudeAuthority(a, b []byte) bool {
	return (a == nil) == (b == nil) && bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
}

func (a *claudeRestoreAuthority) checkUnchanged() error {
	current, err := a.read()
	if err != nil {
		return err
	}
	if !sameClaudeAuthority(current, a.before) {
		return fmt.Errorf("Claude keychain changed before publication; retry")
	}
	return nil
}

func (a *claudeRestoreAuthority) stage() error {
	if err := a.checkUnchanged(); err != nil {
		return err
	}
	if a.before == nil || sameClaudeAuthority(a.before, a.want) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(a.path), "keychain.rollback.*")
	if err != nil {
		return err
	}
	a.backup = file.Name()
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(a.before); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func (a *claudeRestoreAuthority) cleanup() {
	if a.backup != "" && !a.keepBackup {
		_ = os.Remove(a.backup)
	}
}

func (a *claudeRestoreAuthority) uncertain(cause error) error {
	a.keepBackup = a.backup != ""
	if a.keepBackup {
		return fmt.Errorf("%w; outgoing keychain credential preserved at %s", cause, a.backup)
	}
	return fmt.Errorf("%w; keychain recovery could not be confirmed", cause)
}

func (a *claudeRestoreAuthority) publish() error {
	// Do not publish a native replacement from the mirror by mistake. want is
	// the exact result selected before any file in this batch was changed.
	current, err := readRetiredClaudeCredential(a.path)
	if err != nil {
		return err
	}
	if !sameClaudeAuthority(current, a.want) {
		return fmt.Errorf("Claude credential changed before keychain publication; left untouched")
	}
	if err := a.checkUnchanged(); err != nil {
		return err
	}
	if sameClaudeAuthority(a.before, a.want) {
		return nil
	}
	writeErr := a.write(a.want)
	observed, readErr := a.read()
	if writeErr == nil && readErr == nil && sameClaudeAuthority(observed, a.want) {
		return nil
	}
	cause := errors.Join(fmt.Errorf("Claude keychain publication failed"), writeErr, readErr)
	if readErr != nil {
		return a.uncertain(cause)
	}
	if sameClaudeAuthority(observed, a.before) {
		return cause // Publication did not change the outgoing item.
	}
	if !sameClaudeAuthority(observed, a.want) {
		return a.uncertain(errors.Join(cause, fmt.Errorf("keychain changed after publication; left untouched")))
	}
	// A failed command may nevertheless have installed our selected item.
	// Restore only that recognized result, then verify the recovery itself.
	restoreErr := a.write(a.before)
	recovered, verifyErr := a.read()
	if restoreErr != nil || verifyErr != nil || !sameClaudeAuthority(recovered, a.before) {
		return a.uncertain(errors.Join(cause, restoreErr, verifyErr))
	}
	return cause
}
