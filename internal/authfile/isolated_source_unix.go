//go:build !windows

package authfile

import (
	"os"
	"syscall"
)

func openIsolatedSourceFile(path string) (*os.File, error) {
	// O_NONBLOCK prevents a concurrently substituted FIFO from hanging the
	// importer; the caller verifies the opened inode is still a regular file.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
