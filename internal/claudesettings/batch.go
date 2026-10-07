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
	staged     string
	backup     string
	installed  os.FileInfo
	data       []byte
	keepBackup bool
}

// rename is injected per invocation for deterministic I/O-failure tests, never
// via a mutable package global that could affect concurrent activations.
func applyUpdatesWithRename(updates []*Update, rename func(string, string) error) error {
	seen := make(map[string]bool, len(updates))
	for _, update := range updates {
		if update == nil {
			return fmt.Errorf("nil Claude settings update")
		}
		if update.path == "" {
			return fmt.Errorf("Claude settings destination is required")
		}
		path, err := filepath.Abs(update.path)
		if err != nil {
			return err
		}
		if seen[path] {
			return fmt.Errorf("duplicate Claude settings destination: %s", path)
		}
		seen[path] = true
		if err := update.checkUnchanged(); err != nil {
			return err
		}
	}

	writes := make([]*batchWrite, 0, len(updates))
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
	for _, update := range updates {
		write := &batchWrite{update: update}
		writes = append(writes, write)
		if err := write.stage(); err != nil {
			return fmt.Errorf("stage Claude settings %s: %w", update.path, err)
		}
	}
	// Staging can block on I/O. Revalidate the entire read set, not only the
	// first destination, before committing anything staged from old inputs.
	for _, update := range updates {
		if err := update.checkUnchanged(); err != nil {
			return err
		}
	}

	var applied []*batchWrite
	rollback := func(cause error) error {
		for i := len(applied) - 1; i >= 0; i-- {
			if err := applied[i].rollback(rename); err != nil {
				cause = errors.Join(cause, err)
			}
		}
		return cause
	}
	for _, write := range writes {
		if err := write.update.checkUnchanged(); err != nil {
			return rollback(err)
		}
		if write.staged == "" {
			continue // No-op validation still runs, but does not rewrite a file.
		}
		info, err := os.Lstat(write.staged)
		if err != nil {
			return rollback(err)
		}
		if err := rename(write.staged, write.update.path); err != nil {
			return rollback(fmt.Errorf("install Claude settings %s: %w", write.update.path, err))
		}
		write.staged = ""
		write.installed = info
		applied = append(applied, write)
	}
	return nil
}

func (write *batchWrite) stage() error {
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

func (write *batchWrite) rollback(rename func(string, string) error) error {
	path := write.update.path
	failure := func(err error) error {
		write.keepBackup = write.backup != ""
		if write.keepBackup {
			return fmt.Errorf("rollback Claude settings %s: %w; original preserved at %s", path, err, write.backup)
		}
		return fmt.Errorf("rollback newly created Claude settings %s: %w", path, err)
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
		if err := os.Remove(path); err != nil {
			return failure(err)
		}
		return nil
	}
	if err := rename(write.backup, path); err != nil {
		return failure(err)
	}
	write.backup = ""
	return nil
}
