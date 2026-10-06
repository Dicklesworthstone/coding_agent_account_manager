package claudesettings

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// SharedPath resolves the settings file used by a non-isolated Claude session.
// An explicit Claude config directory is authoritative, even when its settings
// file is absent; do not silently borrow authentication from a different home.
func SharedPath(home string) string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// CheckPrivate rejects shared aliases before a profile file is used as an
// authentication source. Atomic rename protects a symlink's referent on write,
// but by itself does not prevent reading another account's auth through it.
func CheckPrivate(path, sharedPath string) error {
	for _, candidate := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Claude profile settings path is a symlink: %s", candidate)
		}
		if candidate == path && !info.Mode().IsRegular() {
			return fmt.Errorf("Claude profile settings is not a regular file: %s", path)
		}
	}
	private, privateErr := os.Stat(path)
	shared, sharedErr := os.Stat(sharedPath)
	if privateErr == nil && sharedErr == nil && os.SameFile(private, shared) {
		return fmt.Errorf("Claude profile settings aliases the shared file: %s", path)
	}
	return nil
}

// PrepareIsolatedSettings refreshes all settings locations for ONE isolated
// profile. Existing files keep their own auth. A missing location can be seeded
// from another existing location of that same profile (legacy/XDG repair), never
// from the real home's auth. An existing {} is not missing and stays auth-free.
// All inputs are read and validated before any writes are returned to callers.
func PrepareIsolatedSettings(sharedPath string, paths []string, p Policy) ([]*Update, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	shared, err := Read(sharedPath)
	if err != nil {
		return nil, fmt.Errorf("read shared Claude settings: %w", err)
	}
	if _, err := object(shared); err != nil {
		return nil, fmt.Errorf("shared Claude settings: %w", err)
	}
	current := make([][]byte, len(paths))
	var seed []byte
	seen := make(map[string]bool)
	for i, path := range paths {
		if path == "" || seen[filepath.Clean(path)] {
			return nil, fmt.Errorf("invalid or duplicate Claude settings destination: %q", path)
		}
		seen[filepath.Clean(path)] = true
		if err := CheckPrivate(path, sharedPath); err != nil {
			return nil, err
		}
		current[i], err = Read(path)
		if err != nil {
			return nil, fmt.Errorf("read profile Claude settings: %w", err)
		}
		if _, err := object(current[i]); err != nil {
			return nil, fmt.Errorf("profile Claude settings %s: %w", path, err)
		}
		if seed == nil && current[i] != nil {
			seed = current[i]
		}
	}
	updates := make([]*Update, 0, len(paths))
	for i, path := range paths {
		account := current[i]
		if account == nil {
			account = seed
		}
		after, err := Merge(shared, account, p)
		if err != nil {
			return nil, err
		}
		updates = append(updates, &Update{path: path, before: current[i], after: after})
	}
	return updates, nil
}

// ApplyUpdates applies preflighted settings writes without rewriting unchanged
// documents on every launch. Even a no-op checks its input for concurrent edits.
func ApplyUpdates(updates []*Update) error {
	for _, update := range updates {
		current, err := Read(update.path)
		if err != nil {
			return err
		}
		if (current == nil) != (update.before == nil) || !bytes.Equal(current, update.before) {
			return fmt.Errorf("Claude settings changed during activation; retry")
		}
	}
	for _, update := range updates {
		if !bytes.Equal(update.before, update.after) {
			if err := update.Apply(); err != nil {
				return err
			}
		}
	}
	return nil
}
