package claudesettings

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// Removal is a prepared retirement of a credential absent from the selected
// snapshot. It participates in the same rollback batch as mixed settings, so a
// later settings failure cannot leave the outgoing account without its token.
type Removal struct {
	path     string
	snapshot string
	before   []byte
	info     os.FileInfo
	verify   func() error
}

// PrepareCredentialRemoval captures a private credential's identity and checks
// it against the caller's already validated bytes. Nil means an absent file,
// not an empty file. The snapshot must remain absent, including no dangling
// symlink. verify is an optional read-only guard for an external authority such
// as the login keychain; it is rechecked after staging and before retirement.
// No file is written or removed until ApplyUpdatesWithRemovals is called.
func PrepareCredentialRemoval(snapshot, path string, before []byte, verify func() error) (*Removal, error) {
	if snapshot == "" || path == "" {
		return nil, fmt.Errorf("Claude credential removal requires snapshot and destination paths")
	}
	var err error
	snapshot, err = filepath.Abs(snapshot)
	if err != nil {
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if snapshot == path {
		return nil, fmt.Errorf("Claude credential snapshot aliases the destination")
	}
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	removal := &Removal{path: path, snapshot: snapshot, before: bytes.Clone(before), info: info, verify: verify}
	if err := removal.checkUnchanged(); err != nil {
		return nil, err
	}
	return removal, nil
}

func (r *Removal) checkUnchanged() error {
	if r.verify != nil {
		if err := r.verify(); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(r.snapshot); err == nil {
		return fmt.Errorf("Claude credential snapshot appeared during activation; retry")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := CheckPrivate(r.path, ""); err != nil {
		return err
	}
	info, err := os.Lstat(r.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if (info == nil) != (r.info == nil) || (info != nil && !os.SameFile(info, r.info)) {
		return fmt.Errorf("Claude credential replaced before retirement; retry")
	}
	current, err := Read(r.path)
	if err != nil {
		return err
	}
	if (current == nil) != (r.before == nil) || !bytes.Equal(current, r.before) {
		return fmt.Errorf("Claude credential changed before retirement; retry")
	}
	return nil
}
