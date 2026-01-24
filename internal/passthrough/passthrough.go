// Package passthrough manages symlinks from pseudo-HOME directories
// to the real HOME directory for development tooling.
//
// When running AI coding tools with an isolated HOME directory, many
// essential dev tools break because they can't find their configuration:
//   - SSH: ~/.ssh (keys, known_hosts)
//   - Git: ~/.gitconfig, ~/.gitignore_global
//   - GPG: ~/.gnupg
//   - AWS/GCP: ~/.aws, ~/.config/gcloud
//
// Passthrough symlinks solve this by linking these from the pseudo-HOME
// to the real HOME, allowing dev tools to work while keeping auth isolated.
package passthrough

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultPassthroughs returns the default set of paths to symlink.
// These are common dev tool configurations that should work across profiles.
var DefaultPassthroughs = []string{
	".ssh",              // SSH keys and config
	".gitconfig",        // Git global config
	".gitignore_global", // Git global ignore
	".gnupg",            // GPG keys
	".aws",              // AWS credentials (if using AWS-based services)
	".config/gcloud",    // GCloud config (for Vertex AI)
	".cargo",            // Rust tooling
	".npm",              // NPM config
	".local/bin",        // User binaries
}

// Status represents the state of a passthrough symlink.
type Status struct {
	Path         string // Relative path from HOME
	SourceExists bool   // Whether the source exists in real HOME
	LinkExists   bool   // Whether the symlink exists in pseudo-HOME
	LinkValid    bool   // Whether the symlink points to the correct target
	Error        string // Any error encountered
}

// Manager handles passthrough symlink creation and verification.
type Manager struct {
	passthroughs []string
	realHome     string
}

// NewManager creates a new passthrough manager with default paths.
func NewManager() (*Manager, error) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	return &Manager{
		passthroughs: DefaultPassthroughs,
		realHome:     realHome,
	}, nil
}

// NewManagerWithPaths creates a manager with custom passthrough paths.
func NewManagerWithPaths(paths []string) (*Manager, error) {
	realHome, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	return &Manager{
		passthroughs: paths,
		realHome:     realHome,
	}, nil
}

// SetupPassthroughs creates symlinks in the pseudo-HOME directory.
func (m *Manager) SetupPassthroughs(pseudoHome string) error {
	absPseudo, err := filepath.Abs(pseudoHome)
	if err != nil {
		return fmt.Errorf("resolve absolute path for pseudo home: %w", err)
	}

	for _, relPath := range m.passthroughs {
		realPath := filepath.Join(m.realHome, relPath)
		linkPath := filepath.Join(pseudoHome, relPath)

		// Security check: prevent path traversal
		absLink, err := filepath.Abs(linkPath)
		if err != nil {
			continue // Skip invalid paths
		}
		if !strings.HasPrefix(absLink, absPseudo+string(os.PathSeparator)) {
			// This path attempts to escape the pseudo home directory
			// Skip it to prevent overwriting files outside the profile
			continue
		}

		// Skip if source doesn't exist
		if _, err := os.Stat(realPath); os.IsNotExist(err) {
			continue
		}

		// Ensure parent directory exists
		linkParent := filepath.Dir(linkPath)
		if err := os.MkdirAll(linkParent, 0700); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", relPath, err)
		}

		// Remove existing file/symlink if present. If a real directory exists,
		// keep it to avoid destructive behavior in the profile home.
		if info, err := os.Lstat(linkPath); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				if err := os.Remove(linkPath); err != nil {
					return fmt.Errorf("remove existing %s: %w", relPath, err)
				}
			} else {
				continue
			}
		}

		// Create symlink
		if err := os.Symlink(realPath, linkPath); err != nil {
			return fmt.Errorf("create symlink for %s: %w", relPath, err)
		}
	}

	return nil
}

// VerifyPassthroughs checks the state of all passthrough symlinks.
func (m *Manager) VerifyPassthroughs(pseudoHome string) ([]Status, error) {
	absPseudo, err := filepath.Abs(pseudoHome)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path for pseudo home: %w", err)
	}

	var statuses []Status

	for _, relPath := range m.passthroughs {
		realPath := filepath.Join(m.realHome, relPath)
		linkPath := filepath.Join(pseudoHome, relPath)

		status := Status{Path: relPath}

		// Security check: prevent path traversal report
		absLink, err := filepath.Abs(linkPath)
		if err != nil {
			status.Error = fmt.Sprintf("invalid path: %v", err)
			statuses = append(statuses, status)
			continue
		}
		if !strings.HasPrefix(absLink, absPseudo+string(os.PathSeparator)) {
			status.Error = "invalid path: escapes profile directory"
			statuses = append(statuses, status)
			continue
		}

		// Check if source exists
		if _, err := os.Stat(realPath); err == nil {
			status.SourceExists = true
		}

		// Check if link exists and is valid
		linkInfo, err := os.Lstat(linkPath)
		if err == nil {
			status.LinkExists = true

			// Check if it's a symlink
			if linkInfo.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(linkPath)
				if err == nil && target == realPath {
					status.LinkValid = true
				} else if err != nil {
					status.Error = fmt.Sprintf("readlink failed: %v", err)
				} else {
					status.Error = fmt.Sprintf("points to %s instead of %s", target, realPath)
				}
			} else {
				status.Error = "exists but is not a symlink"
			}
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// RemovePassthroughs removes all passthrough symlinks from a pseudo-HOME.
func (m *Manager) RemovePassthroughs(pseudoHome string) error {
	for _, relPath := range m.passthroughs {
		linkPath := filepath.Join(pseudoHome, relPath)

		// Only remove if it's a symlink
		linkInfo, err := os.Lstat(linkPath)
		if err != nil {
			continue // Doesn't exist
		}

		if linkInfo.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(linkPath); err != nil {
				return fmt.Errorf("remove symlink %s: %w", relPath, err)
			}
		}
	}

	return nil
}

// AddPassthrough adds a path to the passthrough list.
func (m *Manager) AddPassthrough(relPath string) {
	// Check if already in list
	for _, p := range m.passthroughs {
		if p == relPath {
			return
		}
	}
	m.passthroughs = append(m.passthroughs, relPath)
}

// RemovePassthrough removes a path from the passthrough list.
func (m *Manager) RemovePassthrough(relPath string) {
	for i, p := range m.passthroughs {
		if p == relPath {
			m.passthroughs = append(m.passthroughs[:i], m.passthroughs[i+1:]...)
			return
		}
	}
}

// Passthroughs returns a copy of the current list of passthrough paths.
func (m *Manager) Passthroughs() []string {
	result := make([]string, len(m.passthroughs))
	copy(result, m.passthroughs)
	return result
}

// RealHome returns the real HOME directory.
func (m *Manager) RealHome() string {
	return m.realHome
}
