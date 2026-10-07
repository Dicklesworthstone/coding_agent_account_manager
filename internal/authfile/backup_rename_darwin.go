//go:build darwin

package authfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// renameBackupNoReplace refuses even an empty destination created after the
// caller's last check. RENAME_EXCL makes that condition atomic with publication.
func renameBackupNoReplace(from, to string) error {
	if err := unix.RenamexNp(from, to, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
