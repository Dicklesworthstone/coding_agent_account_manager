package authfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// A missing raw credential snapshot is an instruction to retire that source,
// not permission to reuse the outgoing account. These plans are read-only until
// Restore has validated the complete target, including helper-only profiles.
type claudeCredentialRetirement struct {
	snapshot   string
	live       string
	before     []byte
	mirror     []byte
	readMirror func() ([]byte, error)
}

func prepareClaudeCredentialRetirement(snapshot, live string, readMirror func() ([]byte, error)) (*claudeCredentialRetirement, error) {
	if snapshot == "" || live == "" {
		return nil, fmt.Errorf("Claude credential retirement requires snapshot and live paths")
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
	if snapshot == live {
		return nil, fmt.Errorf("Claude credential snapshot aliases the live path")
	}
	if _, err := os.Lstat(snapshot); err == nil {
		return nil, nil // Present snapshots follow the normal freshness-aware restore.
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	before, err := readRetiredClaudeCredential(live)
	if err != nil {
		return nil, err
	}
	plan := &claudeCredentialRetirement{snapshot: snapshot, live: live, before: before, readMirror: readMirror}
	if readMirror != nil {
		plan.mirror, err = readMirror()
		if err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// readRetiredClaudeCredential refuses aliases rather than deleting an unknown
// source. A hard link is safe to unlink: the referent's bytes are never changed.
func readRetiredClaudeCredential(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Claude credential to retire is not a private regular file: %s", path)
	}
	return os.ReadFile(path)
}

func (plan *claudeCredentialRetirement) Apply() error {
	if _, err := os.Lstat(plan.snapshot); err == nil {
		return fmt.Errorf("Claude credential snapshot appeared during activation; retry")
	} else if !os.IsNotExist(err) {
		return err
	}
	current, err := readRetiredClaudeCredential(plan.live)
	if err != nil {
		return err
	}
	unchanged := (current == nil) == (plan.before == nil) && bytes.Equal(current, plan.before)
	// Restore pulls the authoritative login keychain AFTER settings preflight.
	// Accept only that captured mirror as an alternative to the original disk
	// bytes, never an arbitrary native login that occurred during preparation.
	mirrored := plan.mirror != nil && current != nil && bytes.Equal(bytes.TrimSpace(current), bytes.TrimSpace(plan.mirror))
	if !unchanged && !mirrored {
		return fmt.Errorf("live Claude credential changed before retirement; retry")
	}
	if plan.readMirror != nil {
		mirror, err := plan.readMirror()
		if err != nil {
			return err
		}
		if (mirror == nil) != (plan.mirror == nil) || !bytes.Equal(bytes.TrimSpace(mirror), bytes.TrimSpace(plan.mirror)) {
			return fmt.Errorf("Claude keychain changed before credential retirement; retry")
		}
	}
	if err := os.Remove(plan.live); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("retire outgoing Claude credential %s: %w", plan.live, err)
	}
	return nil
}
