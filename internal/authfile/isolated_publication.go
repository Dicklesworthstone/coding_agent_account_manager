package authfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// SnapshotPublishedError means the complete snapshot was already atomically
// published, but the final directory flush failed. It is NOT a failed rename:
// retain the snapshot and report its location, rather than deleting credentials
// or automatically retrying under a new name after an uncertain commit.
type SnapshotPublishedError struct {
	Path string
	Err  error
}

func (e *SnapshotPublishedError) Error() string {
	return fmt.Sprintf("complete vault snapshot published at %s, but durability could not be confirmed: %v; inspect the saved entry before retrying", e.Path, e.Err)
}

func (e *SnapshotPublishedError) Unwrap() error { return e.Err }

// Remember the nearest existing ancestor BEFORE MkdirAll, so a first backup
// flushes every newly created vault directory and its parent, not just claude/.
// A configured vault may live beneath a symlink; retain the resolved directory
// identity rather than making the symlink itself a durability boundary.
func existingIsolatedBackupAncestor(path string) (isolatedSourceDir, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return isolatedSourceDir{}, err
	}
	for {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return isolatedSourceDir{}, fmt.Errorf("vault ancestor is not a directory: %s", path)
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return isolatedSourceDir{}, err
			}
			ancestor, err := inspectIsolatedSourceDir(resolved)
			if err != nil || !os.SameFile(info, ancestor.info) {
				return isolatedSourceDir{}, fmt.Errorf("vault ancestor changed while resolving: %s", path)
			}
			return ancestor, nil
		}
		if !os.IsNotExist(err) {
			return isolatedSourceDir{}, fmt.Errorf("inspect vault ancestor: %w", err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return isolatedSourceDir{}, fmt.Errorf("no existing vault ancestor: %w", err)
		}
		path = parent
	}
}

func publishIsolatedBackupDurably(stage, destination string, ancestor isolatedSourceDir, publish func() error) error {
	if runtime.GOOS == "windows" {
		// Windows does not expose POSIX directory fsync through os.File.Sync.
		// Keep its existing atomic no-replace publication and writer-side file
		// flushes; do not pretend this adds a Windows power-loss guarantee.
		return publish()
	}
	return publishIsolatedBackupWithSync(stage, destination, ancestor, publish, (*os.File).Sync)
}

// Sync is injected per operation (not globally) for deterministic I/O-failure
// tests. Production always uses File.Sync. The callback must recheck source and
// directory generations AFTER these potentially slow flushes, then invoke the
// existing no-replace snapshot publisher. No native authentication path is used.
func publishIsolatedBackupWithSync(stage, destination string, ancestor isolatedSourceDir, publish func() error, syncFile func(*os.File) error) error {
	if ancestor.info == nil {
		return fmt.Errorf("missing vault durability ancestor")
	}
	if err := ancestor.unchanged(); err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("resolve vault publication directory: %w", err)
	}
	rel, err := filepath.Rel(ancestor.path, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("vault publication directory moved outside its captured ancestor")
	}

	stageDir, err := openIsolatedPublicationDirectory(stage)
	if err != nil {
		return err
	}
	defer stageDir.Close()
	entries, err := stageDir.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("inspect prepared snapshot: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("prepared snapshot is empty")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		path := filepath.Join(stage, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("prepared snapshot contains a nonregular file: %s", entry.Name())
		}
		file, err := openIsolatedSourceFile(path)
		if err != nil {
			return fmt.Errorf("open prepared snapshot file: %w", err)
		}
		opened, err := file.Stat()
		if err != nil || !sameIsolatedFileInfo(info, opened) {
			_ = file.Close()
			return fmt.Errorf("prepared snapshot changed while opening: %s", entry.Name())
		}
		err = syncFile(file)
		closeErr := file.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return fmt.Errorf("flush prepared snapshot file %s: %w", entry.Name(), err)
		}
	}
	// Flushed file bytes do not make the snapshot's directory entries durable.
	if err := syncFile(stageDir); err != nil {
		return fmt.Errorf("flush prepared snapshot directory: %w", err)
	}

	var directories []*os.File
	defer func() {
		for _, dir := range directories {
			_ = dir.Close()
		}
	}()
	for path := parent; ; path = filepath.Dir(path) {
		dir, err := openIsolatedPublicationDirectory(path)
		if err != nil {
			return err
		}
		directories = append(directories, dir)
		if err := syncFile(dir); err != nil {
			return fmt.Errorf("flush vault ancestor before publication: %w", err)
		}
		if path == ancestor.path {
			break
		}
	}
	if err := ancestor.unchanged(); err != nil {
		return err
	}
	for _, dir := range append([]*os.File{stageDir}, directories...) {
		opened, err := dir.Stat()
		current, pathErr := os.Lstat(dir.Name())
		if err != nil || pathErr != nil || !current.IsDir() || !os.SameFile(opened, current) {
			return fmt.Errorf("snapshot directory changed before publication: %s", dir.Name())
		}
	}
	if err := publish(); err != nil {
		return err
	}
	// Keep the original destination directory handle across the rename. An I/O
	// error here is an uncertain durability outcome, not permission to roll back
	// a complete snapshot that another process may already be using.
	if err := syncFile(directories[0]); err != nil {
		return &SnapshotPublishedError{Path: destination, Err: err}
	}
	return nil
}

func openIsolatedPublicationDirectory(path string) (*os.File, error) {
	checked, err := inspectIsolatedSourceDir(path)
	if err != nil {
		return nil, err
	}
	file, err := openIsolatedSourceFile(path)
	if err != nil {
		return nil, fmt.Errorf("open snapshot directory: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(checked.info, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("snapshot directory changed while opening: %s", path)
	}
	return file, nil
}
