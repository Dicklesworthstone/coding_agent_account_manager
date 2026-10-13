package authfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// isolatedSourceFile records absence as well as presence. A settings file
// appearing during a login is as important as an existing token being replaced.
// No reader in this file follows a source-file symlink or writes to the source.
type isolatedSourceFile struct {
	path  string
	info  os.FileInfo
	data  []byte
	limit int64
}

func readIsolatedSourceFile(path string, required bool, limit int64) (*isolatedSourceFile, error) {
	s := &isolatedSourceFile{path: path, limit: limit}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !required {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read isolated source %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("isolated source must be a regular, non-symlink file: %s", path)
	}
	if limit <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("isolated source exceeds its read limit: %s", path)
	}
	f, err := openIsolatedSourceFile(path)
	if err != nil {
		return nil, fmt.Errorf("open isolated source %s: %w", path, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameIsolatedFileInfo(info, opened) {
		return nil, fmt.Errorf("isolated source changed while opening: %s", path)
	}
	// Check the logical path too: the opened handle alone cannot detect an
	// adoption link or a replaced parent redirecting a future read elsewhere.
	current, err := os.Lstat(path)
	if err != nil || !sameIsolatedFileInfo(opened, current) {
		return nil, fmt.Errorf("isolated source path changed while opening: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read isolated source %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("isolated source exceeds its read limit: %s", path)
	}
	after, err := f.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !sameIsolatedFileInfo(opened, after) ||
		!sameIsolatedFileInfo(opened, current) || int64(len(data)) != opened.Size() {
		return nil, fmt.Errorf("isolated source changed while reading: %s", path)
	}
	s.info, s.data = opened, data
	return s, nil
}

func sameIsolatedFileInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() &&
		os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (s *isolatedSourceFile) unchanged() error {
	current, err := readIsolatedSourceFile(s.path, false, s.limit)
	if err != nil {
		return err
	}
	if s.info == nil && current.info == nil {
		return nil
	}
	if !sameIsolatedFileInfo(s.info, current.info) || !bytes.Equal(s.data, current.data) {
		return fmt.Errorf("isolated source changed; finish login/refresh and retry: %s", s.path)
	}
	return nil
}

type isolatedSourceDir struct {
	path string
	info os.FileInfo
}

func inspectIsolatedSourceDir(path string) (isolatedSourceDir, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return isolatedSourceDir{}, fmt.Errorf("inspect isolated directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return isolatedSourceDir{}, fmt.Errorf("isolated source must use non-symlink directories: %s", path)
	}
	return isolatedSourceDir{path: path, info: info}, nil
}

func (s isolatedSourceDir) unchanged() error {
	current, err := inspectIsolatedSourceDir(s.path)
	if err != nil {
		return err
	}
	if !os.SameFile(s.info, current.info) {
		return fmt.Errorf("isolated source directory changed; retry: %s", s.path)
	}
	return nil
}

// validateIsolatedClaudeMetadata checks the registered source before any
// credential is read. A complete OAuth cache is not evidence that the profile
// selects OAuth: an API-key profile may retain credentials from an older login
// while caam exec deliberately permits API-key environment/helper inputs.
// Never reinterpret those leftovers as the account the user asked to save.
func validateIsolatedClaudeMetadata(data []byte, root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("isolated profile root must be absolute")
	}
	var metadata struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		AuthMode string `json:"auth_mode"`
		BasePath string `json:"base_path"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil ||
		metadata.Provider != "claude" || metadata.Name != filepath.Base(root) {
		return fmt.Errorf("isolated source metadata does not identify this Claude profile")
	}
	if metadata.AuthMode != "oauth" {
		return fmt.Errorf("isolated source must select OAuth (auth_mode=oauth); cached OAuth files in other profile modes cannot be imported")
	}
	if metadata.BasePath != "" {
		base, err := filepath.Abs(metadata.BasePath)
		if err != nil || base != root {
			return fmt.Errorf("isolated profile metadata points outside its store location")
		}
	}
	return nil
}
