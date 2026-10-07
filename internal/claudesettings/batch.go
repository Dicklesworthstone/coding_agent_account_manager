package claudesettings

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// batchWrite owns the staged replacement and a private rollback copy. Rollback
// checks both the installed inode and bytes: matching bytes alone would allow
// us to overwrite a native CLI's replacement with older authentication.
type batchWrite struct {
	update     *Update
	removal    *Removal
	staged     string
	backup     string
	installed  os.FileInfo
	data       []byte
	keepBackup bool
}

type batchFileOps struct {
	rename func(string, string) error
	remove func(string) error
	link   func(string, string) error
}

// ApplyUpdatesWithRemovals stages settings and outgoing-credential recovery
// copies before changing anything. Credential removal is deliberately last;
// any returned failure rolls both kinds of change back where still safe.
// Whole vault/keychain switches and process crashes remain outside this batch.
func ApplyUpdatesWithRemovals(updates []*Update, removals []*Removal) error {
	return applyChanges(updates, removals, batchFileOps{os.Rename, os.Remove, os.Link})
}

// rename is injected per invocation for deterministic I/O-failure tests, never
// via a mutable package global that could affect concurrent activations.
func applyUpdatesWithRename(updates []*Update, rename func(string, string) error) error {
	return applyChanges(updates, nil, batchFileOps{rename, os.Remove, os.Link})
}

func applyChanges(updates []*Update, removals []*Removal, ops batchFileOps) error {
	writes := make([]*batchWrite, 0, len(updates)+len(removals))
	for _, update := range updates {
		if update == nil {
			return fmt.Errorf("nil Claude settings update")
		}
		writes = append(writes, &batchWrite{update: update})
	}
	for _, removal := range removals {
		if removal == nil {
			return fmt.Errorf("nil Claude credential removal")
		}
		writes = append(writes, &batchWrite{removal: removal})
	}
	seen := make(map[string]bool, len(writes))
	for _, write := range writes {
		if write.path() == "" {
			return fmt.Errorf("Claude settings destination is required")
		}
		path, err := filepath.Abs(write.path())
		if err != nil {
			return err
		}
		if seen[path] {
			return fmt.Errorf("duplicate Claude settings destination: %s", path)
		}
		seen[path] = true
		if err := write.checkUnchanged(); err != nil {
			return err
		}
	}

	defer func() {
		for _, write := range writes {
			if write.staged != "" {
				_ = os.Remove(write.staged)
			}
			if write.backup != "" && !write.keepBackup {
				_ = os.Remove(write.backup)
			}
		}
	}()
	for _, write := range writes {
		if err := write.stage(); err != nil {
			return fmt.Errorf("stage Claude settings %s: %w", write.path(), err)
		}
	}
	// Staging can block on I/O. Revalidate the entire read set, not only the
	// first destination, before committing anything staged from old inputs.
	for _, write := range writes {
		if err := write.checkUnchanged(); err != nil {
			return err
		}
	}

	var applied []*batchWrite
	rollback := func(cause error) error {
		for i := len(applied) - 1; i >= 0; i-- {
			if err := applied[i].rollbackWithOps(ops); err != nil {
				cause = errors.Join(cause, err)
			}
		}
		return cause
	}
	for _, write := range writes {
		if err := write.checkUnchanged(); err != nil {
			return rollback(err)
		}
		if write.removal != nil {
			if write.removal.before == nil {
				continue // Preserve absence without creating an empty credential.
			}
			if err := ops.remove(write.path()); err != nil {
				return rollback(fmt.Errorf("retire Claude credential %s: %w", write.path(), err))
			}
			applied = append(applied, write)
			continue
		}
		if write.staged == "" {
			continue // No-op validation still runs, but does not rewrite a file.
		}
		info, err := os.Lstat(write.staged)
		if err != nil {
			return rollback(err)
		}
		if err := ops.rename(write.staged, write.update.path); err != nil {
			return rollback(fmt.Errorf("install Claude settings %s: %w", write.update.path, err))
		}
		write.staged = ""
		write.installed = info
		applied = append(applied, write)
	}
	return nil
}

func (write *batchWrite) path() string {
	if write.removal != nil {
		return write.removal.path
	}
	return write.update.path
}

func (write *batchWrite) checkUnchanged() error {
	if write.removal != nil {
		return write.removal.checkUnchanged()
	}
	return write.update.checkUnchanged()
}

func (write *batchWrite) stage() error {
	if write.removal != nil {
		if write.removal.before == nil {
			return nil
		}
		var err error
		write.backup, err = stageSettingsFile(filepath.Dir(write.path()), "credentials.json.rollback.*", write.removal.before)
		return err
	}
	u := write.update
	info, err := os.Lstat(u.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	linked := err == nil && info.Mode()&os.ModeSymlink != 0
	if !u.Changed() && !linked {
		return nil
	}
	write.data = u.after
	if write.data == nil {
		if !linked {
			return nil
		}
		// Detach even a newly introduced dangling link. Leaving it in place
		// would expose another account if its target appears after this call.
		write.data = []byte("{}\n")
	}
	if info != nil && !linked && !info.Mode().IsRegular() {
		return fmt.Errorf("destination is not a regular file")
	}
	dir := filepath.Dir(u.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	write.staged, err = stageSettingsFile(dir, "settings.json.stage.*", write.data)
	if err != nil {
		return err
	}
	if linked {
		// Preserve the original link, including relative/dangling targets,
		// without ever writing through it. Both names live in the same dir.
		target, err := os.Readlink(u.path)
		if err != nil {
			return err
		}
		backup, err := os.CreateTemp(dir, "settings.json.rollback.*")
		if err != nil {
			return err
		}
		write.backup = backup.Name()
		if err := backup.Close(); err != nil {
			return err
		}
		if err := os.Remove(write.backup); err != nil {
			return err
		}
		return os.Symlink(target, write.backup)
	}
	if u.before != nil {
		write.backup, err = stageSettingsFile(dir, "settings.json.rollback.*", u.before)
	}
	return err
}

func stageSettingsFile(dir, pattern string, data []byte) (path string, err error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = file.Chmod(0600); err != nil {
		return path, err
	}
	if _, err = file.Write(data); err != nil {
		return path, err
	}
	if err = file.Sync(); err != nil {
		return path, err
	}
	err = file.Close()
	return path, err
}

func (write *batchWrite) rollbackWithOps(ops batchFileOps) error {
	path := write.path()
	failure := func(err error) error {
		write.keepBackup = write.backup != ""
		if write.keepBackup {
			return fmt.Errorf("rollback Claude settings %s: %w; original preserved at %s", path, err, write.backup)
		}
		return fmt.Errorf("rollback newly created Claude settings %s: %w", path, err)
	}
	if write.removal != nil {
		// Link publishes the private recovery copy only into an absent slot.
		// A rename could overwrite a native login created since retirement.
		if err := ops.link(write.backup, path); err != nil {
			return failure(fmt.Errorf("restore retired credential without replacing a new login: %w", err))
		}
		// Deferred cleanup removes the extra recovery name, not the live link.
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return failure(err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, write.installed) {
		return failure(fmt.Errorf("destination replaced after installation; left untouched"))
	}
	current, err := Read(path)
	if err != nil {
		return failure(err)
	}
	if !bytes.Equal(current, write.data) {
		return failure(fmt.Errorf("destination edited after installation; left untouched"))
	}
	if write.backup == "" {
		if err := ops.remove(path); err != nil {
			return failure(err)
		}
		return nil
	}
	if err := ops.rename(write.backup, path); err != nil {
		return failure(err)
	}
	write.backup = ""
	return nil
}
