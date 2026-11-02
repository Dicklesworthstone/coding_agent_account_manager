// Package vaulttransfer provides captured, guarded profile transfers shared by
// archives and remote synchronization. It does not acquire vault locks: callers
// hold the destination vault's mutation lock for planning and publication.
package vaulttransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// Snapshot records a complete profile tree, including local file identity and
// content. It contains no credential bytes. ReadFile and Check reject changed
// generations, including replacement with identical contents.
type Snapshot struct {
	path      string
	entries   map[string]entry
	ancestors []ancestor
}

type entry struct {
	info     fs.FileInfo
	checksum string
}

// Ancestors determine which vault owns the captured profile. Sibling activity
// changes directory timestamps and sizes, so only identities and types are
// pinned. Existing trusted host aliases also pin their resolved directory.
type ancestor struct {
	path     string
	info     fs.FileInfo
	resolved fs.FileInfo
}

// Entry describes a captured path without exposing its contents. Checksums are
// transport integrity data, not account identities, and must not be logged.
type Entry struct {
	Name     string
	Mode     fs.FileMode
	Dir      bool
	Size     int64
	Checksum string
}

// ValidateProfileRef validates portable, user-selectable vault path segments.
// Reserved recovery directories can never become transfer destinations.
func ValidateProfileRef(provider, profile string) error {
	for _, segment := range []string{provider, profile} {
		if err := ValidatePathSegment(segment); err != nil {
			return fmt.Errorf("invalid profile reference")
		}
		for _, r := range segment {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
				(r < '0' || r > '9') && r != '_' && r != '-' && r != '.' && r != '@' && r != '+' {
				return fmt.Errorf("invalid profile reference")
			}
		}
	}
	if authfile.IsSystemProfile(profile) || strings.HasPrefix(profile, ".caam_import_") || strings.HasPrefix(profile, ".import_tmp_") {
		return fmt.Errorf("system and recovery profiles cannot be transferred")
	}
	return nil
}

// ProfilePath validates every component controlled by vault configuration or a
// profile reference. Trusted higher host ancestors may be aliases, as with
// macOS /var; a vault or provider directory must itself be a real directory.
func ProfilePath(vaultRoot, provider, profile string) (string, error) {
	if err := ValidateProfileRef(provider, profile); err != nil {
		return "", err
	}
	root, err := filepath.Abs(vaultRoot)
	if err != nil {
		return "", err
	}
	for _, path := range []string{root, filepath.Join(root, provider), filepath.Join(root, provider, profile)} {
		if err := validateDirectory(path, true); err != nil {
			return "", err
		}
	}
	return filepath.Join(root, provider, profile), nil
}

// ValidateRelativePath rejects path spellings that can escape or change
// meaning when a profile moves between Unix and Windows.
func ValidateRelativePath(name string) error {
	for _, part := range strings.Split(name, "/") {
		if err := ValidatePathSegment(part); err != nil {
			return fmt.Errorf("invalid relative profile path")
		}
	}
	return nil
}

// ValidatePathSegment rejects Windows aliases even when validation runs on
// Unix, so transferred filenames have one meaning on either host.
func ValidatePathSegment(name string) error {
	if name == "" || name == "." || name == ".." || strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return fmt.Errorf("expected a nonempty portable path segment")
	}
	for _, r := range name {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("invalid character in path segment")
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return fmt.Errorf("reserved device name")
	}
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")
		if len([]rune(suffix)) == 1 && strings.ContainsAny(suffix, "123456789¹²³") {
			return fmt.Errorf("reserved device name")
		}
	}
	return nil
}

// Capture records a profile, or its absence. File contents are hashed with a
// streaming read, so capturing large native policy files does not duplicate
// their contents in memory.
func Capture(path string) (*Snapshot, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{path: path, entries: make(map[string]entry)}
	snapshot.ancestors, err = captureAncestors(path)
	if err != nil {
		return nil, err
	}
	if err := validateDirectory(path, true); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return snapshot, snapshot.checkAncestors()
	} else if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(path, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular profile file: %s", current)
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		if rel != "." {
			if err := ValidateRelativePath(filepath.ToSlash(rel)); err != nil {
				return err
			}
		}
		captured := entry{info: info}
		if info.Mode().IsRegular() {
			h := sha256.New()
			if err := streamFile(current, info, h); err != nil {
				return err
			}
			captured.checksum = hex.EncodeToString(h.Sum(nil))
		}
		snapshot.entries[rel] = captured
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, snapshot.checkAncestors()
}

func captureAncestors(path string) ([]ancestor, error) {
	var ancestors []ancestor
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		captured := ancestor{path: parent}
		info, err := os.Lstat(parent)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			captured.info = info
			if info.Mode()&os.ModeSymlink != 0 {
				captured.resolved, err = os.Stat(parent)
				if err != nil {
					return nil, err
				}
				if !captured.resolved.IsDir() {
					return nil, fmt.Errorf("profile ancestor is not a directory: %s", parent)
				}
			} else if !info.IsDir() {
				return nil, fmt.Errorf("profile ancestor is not a directory: %s", parent)
			}
		}
		ancestors = append(ancestors, captured)
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return ancestors, nil
}

func sameObject(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode().Type() == b.Mode().Type()
}

func (s *Snapshot) checkAncestors() error {
	// Check from the root down so a replaced higher ancestor is rejected even
	// if the same profile directory has been moved below its replacement.
	for i := len(s.ancestors) - 1; i >= 0; i-- {
		before := s.ancestors[i]
		after, err := os.Lstat(before.path)
		if before.info == nil {
			if os.IsNotExist(err) {
				continue
			}
			// Publication may create missing parents, but never by introducing
			// a new alias into the captured path.
			if err != nil || !after.IsDir() {
				return changed(before.path)
			}
			continue
		}
		if err != nil || !sameObject(before.info, after) {
			return changed(before.path)
		}
		if before.resolved != nil {
			resolved, err := os.Stat(before.path)
			if err != nil || !sameObject(before.resolved, resolved) {
				return changed(before.path)
			}
		}
	}
	return nil
}

// Path is the original absolute captured path.
func (s *Snapshot) Path() string { return s.path }

// Exists distinguishes an absent profile from an existing empty directory.
func (s *Snapshot) Exists() bool { _, exists := s.entries["."]; return exists }

// Entries returns sorted paths, including the profile root (".").
func (s *Snapshot) Entries() []Entry {
	entries := make([]Entry, 0, len(s.entries))
	for name, captured := range s.entries {
		entries = append(entries, Entry{
			Name: name, Mode: captured.info.Mode(), Dir: captured.info.IsDir(),
			Size: captured.info.Size(), Checksum: captured.checksum,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// Files returns sorted, relative regular-file paths.
func (s *Snapshot) Files() []string {
	names := make([]string, 0, len(s.entries))
	for name, captured := range s.entries {
		if captured.info.Mode().IsRegular() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Digest identifies the portable contents and permissions of the complete
// captured tree. Local Check additionally verifies inode and modification
// time, which are not portable between hosts. Never log credential digests.
func (s *Snapshot) Digest() string {
	h := sha256.New()
	writeField(h, "caam-profile-snapshot-v1")
	for _, item := range s.Entries() {
		captured := s.entries[item.Name]
		writeField(h, filepath.ToSlash(item.Name))
		writeField(h, fmt.Sprintf("%o", captured.info.Mode()))
		writeField(h, captured.checksum)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = io.WriteString(h, value)
}

func sameFile(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime()) &&
		(a.IsDir() || a.Size() == b.Size())
}

func changed(path string) error {
	return fmt.Errorf("profile changed during transfer: %s; retry", path)
}

func streamFile(path string, expected fs.FileInfo, output io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !sameFile(expected, info) {
		return changed(path)
	}
	// A growing file must not turn a captured generation into an unbounded
	// read, even when its writer ignores CAAM's vault lock.
	n, err := io.Copy(output, io.LimitReader(file, expected.Size()+1))
	if err != nil {
		return err
	}
	if n != expected.Size() {
		return changed(path)
	}
	info, err = file.Stat()
	if err != nil || !sameFile(expected, info) {
		return changed(path)
	}
	current, err := os.Lstat(path)
	if err != nil || !sameFile(expected, current) {
		return changed(path)
	}
	return nil
}

// ReadFile reads only the captured generation. Missing files are distinct
// from present but invalid or concurrently replaced credential sources.
func (s *Snapshot) ReadFile(name string) ([]byte, bool, error) {
	var data bytes.Buffer
	exists, err := s.WriteFile(name, &data)
	if err != nil {
		return nil, exists, err
	}
	if !exists {
		return nil, false, nil
	}
	return data.Bytes(), true, nil
}

// WriteFile streams a captured regular file and validates its generation and
// checksum. Output must remain private and unpublished until this method and
// the enclosing snapshot's Check succeed; an error may follow partial output.
func (s *Snapshot) WriteFile(name string, output io.Writer) (bool, error) {
	captured, exists := s.entries[name]
	if err := s.checkAncestors(); err != nil {
		return exists, err
	}
	if !exists {
		return false, nil
	}
	if !captured.info.Mode().IsRegular() {
		return true, fmt.Errorf("profile credential is not a regular file: %s", name)
	}
	h := sha256.New()
	if err := streamFile(filepath.Join(s.path, name), captured.info, io.MultiWriter(output, h)); err != nil {
		return true, err
	}
	if hex.EncodeToString(h.Sum(nil)) != captured.checksum {
		return true, changed(s.path)
	}
	return true, s.checkAncestors()
}

// Check verifies that the entire captured generation remains in place.
func (s *Snapshot) Check() error { return s.CheckAt(s.path) }

// CheckAt also works after an owned directory rename, for recovery checks.
// Its original ancestor identities must remain in place; only the captured
// profile itself may move to the supplied recovery path.
func (s *Snapshot) CheckAt(path string) error {
	if err := s.checkAncestors(); err != nil {
		return err
	}
	current, err := Capture(path)
	if err != nil {
		return fmt.Errorf("recheck profile generation: %w", err)
	}
	if len(current.entries) != len(s.entries) {
		return changed(path)
	}
	for name, before := range s.entries {
		after, exists := current.entries[name]
		if !exists || !sameFile(before.info, after.info) || before.checksum != after.checksum {
			return changed(path)
		}
	}
	return s.checkAncestors()
}

func (s *Snapshot) copyTo(dst, omittedFile string) error {
	for _, item := range s.Entries() {
		name := item.Name
		if name == omittedFile {
			continue
		}
		captured := s.entries[name]
		path := filepath.Join(dst, name)
		if item.Dir {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		h := sha256.New()
		err = streamFile(filepath.Join(s.path, name), captured.info, io.MultiWriter(file, h))
		if err == nil && hex.EncodeToString(h.Sum(nil)) != captured.checksum {
			err = changed(s.path)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func validateDirectory(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if err != nil && (!allowMissing || !os.IsNotExist(err)) {
		return err
	}
	if err == nil && !info.IsDir() {
		return fmt.Errorf("profile path is not a real directory: %s", path)
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("profile parent is not a real directory: %s", parent)
			}
			return nil
		}
		if filepath.Dir(parent) == parent {
			return nil
		}
	}
}
