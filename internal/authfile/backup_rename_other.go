//go:build !linux && !darwin && !windows

package authfile

import (
	"errors"
	"os"
)

// A portable check followed by os.Rename cannot guarantee no replacement.
// Platforms without an exclusive directory rename must leave both paths alone.
func renameBackupNoReplace(from, to string) error {
	return &os.LinkError{Op: "rename without replacement", Old: from, New: to, Err: errors.ErrUnsupported}
}
