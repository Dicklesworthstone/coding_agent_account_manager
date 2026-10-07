package claudesettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PrepareCredentialReplacement stages an exact, already validated credential
// generation in the same batch as settings and removals. after must be either
// the captured snapshot or the captured live credential (freshness protection).
// sources includes the snapshot and any identity documents used to make that
// decision. All buffers are copied; caller mutation cannot change the plan.
// verify is read-only and guards an external authority before each write.
func PrepareCredentialReplacement(snapshot, path string, before, after []byte, sources map[string][]byte, verify func() error) (*Update, error) {
	if snapshot == "" || path == "" {
		return nil, fmt.Errorf("Claude credential replacement requires snapshot and destination paths")
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
	if err := CheckPrivate(path, snapshot); err != nil {
		return nil, err
	}
	inputs := make(settingsInputs, len(sources))
	for source, data := range sources {
		if source == "" {
			return nil, fmt.Errorf("Claude credential source path is required")
		}
		abs, err := filepath.Abs(source)
		if err != nil {
			return nil, err
		}
		if old, exists := inputs[abs]; exists && ((old == nil) != (data == nil) || !bytes.Equal(old, data)) {
			return nil, fmt.Errorf("conflicting captured Claude credential source")
		}
		inputs[abs] = bytes.Clone(data)
	}
	selected, ok := inputs[snapshot]
	if !ok || selected == nil || after == nil || (!bytes.Equal(after, selected) && !bytes.Equal(after, before)) {
		return nil, fmt.Errorf("Claude replacement must use a captured credential generation")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(selected, &obj) != nil || len(obj) == 0 {
		return nil, fmt.Errorf("Claude snapshot must contain a nonempty JSON object")
	}
	obj = nil
	if json.Unmarshal(after, &obj) != nil || len(obj) == 0 {
		return nil, fmt.Errorf("Claude replacement must contain a nonempty JSON object")
	}
	update, err := preparedUpdate(path, bytes.Clone(before), bytes.Clone(after))
	if err != nil {
		return nil, err
	}
	update.identity, err = os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	update.checkIdentity = true
	update.privateSources = []string{snapshot}
	update.verify = verify
	update.inputs = inputs
	if err := update.checkUnchanged(); err != nil {
		return nil, err
	}
	return update, nil
}

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
