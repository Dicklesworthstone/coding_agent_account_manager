//go:build linux

package authfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameBackupNoReplace publishes a prepared directory only if its destination
// is absent at the rename itself. Unsupported filesystems fail without falling
// back to a check followed by a replacing rename.
func renameBackupNoReplace(from, to string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
