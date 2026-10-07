//go:build windows

package authfile

import (
	"os"

	"golang.org/x/sys/windows"
)

// renameBackupNoReplace uses a same-volume move without replacement or copy
// flags. An existing destination, including an empty directory, is preserved.
func renameBackupNoReplace(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	destination, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	if err := windows.MoveFileEx(source, destination, 0); err != nil {
		// Windows may report access denied for an existing directory. This
		// inspection only classifies a failed move; it never authorizes one.
		if _, statErr := os.Lstat(to); statErr == nil {
			err = os.ErrExist
		}
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}
