package authfile

import "os"

func openIsolatedSourceFile(path string) (*os.File, error) {
	// The caller rejects reparse/symlink sources with Lstat and compares the
	// opened file identity with the checked path before reading any bytes.
	return os.Open(path)
}
