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

// isolatedXDGEntries are entry names under the XDG config/data/state roots
// that must NEVER be passed through into a profile. They are (or can contain)
// per-account state for the providers caam isolates — symlinking them would
// collapse the isolation the profile exists to provide — or they are caam's
// own data root (which contains the vault and every profile's credentials).
//
// The deny list intentionally covers ALL providers, not just the profile's
// own: a claude profile passing through the real "claude-code" config dir
// would expose the real account's auth.json inside the "isolated" profile.
var isolatedXDGEntries = map[string]bool{
	"caam":        true, // caam's own data (vault + all profiles' credentials)
	"claude":      true,
	"claude-code": true,
	"codex":       true,
	"gemini":      true,
	"antigravity": true,
	"agy":         true,
	"opencode":    true,
	"cursor":      true,
	"grok":        true,
}

// IsIsolatedXDGEntry reports whether an entry name under an XDG root is on
// the deny list and must not be passed through into a profile.
func IsIsolatedXDGEntry(name string) bool {
	return isolatedXDGEntries[strings.ToLower(name)]
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

// realXDGConfigHome returns the real user's XDG config directory, honoring
// XDG_CONFIG_HOME only when it points outside the profile being set up.
func (m *Manager) realXDGConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(m.realHome, ".config")
}

// realXDGDataHome returns the real user's XDG data directory.
func (m *Manager) realXDGDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(m.realHome, ".local", "share")
}

// realXDGStateHome returns the real user's XDG state directory.
func (m *Manager) realXDGStateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	return filepath.Join(m.realHome, ".local", "state")
}

// SetupXDGPassthroughs creates per-entry symlinks for the XDG config, data,
// and state directories so tools that keep their credentials under ~/.config,
// ~/.local/share, or ~/.local/state (gh, vercel, shopify, supabase, atuin,
// uv, ...) keep working inside a profile (issue #69).
//
// Profile isolation redirects HOME and XDG_CONFIG_HOME into the profile, which
// silently relocates all three XDG roots (data and state default to
// $HOME/.local/...). Before this, only a handful of $HOME dotfiles were passed
// through, so every XDG-based CLI lost its credentials — most visibly gh,
// whose absence surfaces as a baffling git "could not read Username" error.
//
// Entries are linked one at a time (never the whole root) so the provider auth
// directories caam deliberately isolates stay real, private directories inside
// the profile. Isolation rules, in order:
//   - deny-listed entries (all providers' auth dirs + caam's own data root)
//     are never linked;
//   - an entry that already exists in the profile as a real file/dir is left
//     untouched (the profile owns it);
//   - existing symlinks are refreshed to point at the current real path.
func (m *Manager) SetupXDGPassthroughs(pseudoHome, pseudoXDGConfig string) error {
	pairs := []struct{ realDir, targetDir string }{
		{m.realXDGConfigHome(), pseudoXDGConfig},
		{m.realXDGDataHome(), filepath.Join(pseudoHome, ".local", "share")},
		{m.realXDGStateHome(), filepath.Join(pseudoHome, ".local", "state")},
	}

	for _, pair := range pairs {
		if pair.targetDir == "" || pair.realDir == "" {
			continue
		}
		absTarget, err := filepath.Abs(pair.targetDir)
		if err != nil {
			continue
		}
		absReal, err := filepath.Abs(pair.realDir)
		if err != nil {
			continue
		}
		// Never link a root into itself (e.g. XDG_DATA_HOME already pointing
		// inside the profile).
		if absReal == absTarget || strings.HasPrefix(absReal, absTarget+string(os.PathSeparator)) {
			continue
		}

		entries, err := os.ReadDir(absReal)
		if err != nil {
			continue // Real root missing: nothing to pass through.
		}

		if err := os.MkdirAll(absTarget, 0700); err != nil {
			return fmt.Errorf("create %s: %w", absTarget, err)
		}

		for _, entry := range entries {
			name := entry.Name()
			if IsIsolatedXDGEntry(name) {
				continue
			}

			linkPath := filepath.Join(absTarget, name)
			// Path traversal guard (defensive; entry names come from ReadDir).
			if filepath.Dir(linkPath) != absTarget {
				continue
			}

			realPath := filepath.Join(absReal, name)
			if info, err := os.Lstat(linkPath); err == nil {
				if info.Mode()&os.ModeSymlink == 0 {
					// A real file/dir in the profile wins: it is either a
					// provider-managed dir or something the profile created.
					continue
				}
				if current, err := os.Readlink(linkPath); err == nil && current == realPath {
					continue // Already correct.
				}
				if err := os.Remove(linkPath); err != nil {
					return fmt.Errorf("refresh symlink %s: %w", linkPath, err)
				}
			}

			if err := os.Symlink(realPath, linkPath); err != nil {
				return fmt.Errorf("create symlink %s: %w", linkPath, err)
			}
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
