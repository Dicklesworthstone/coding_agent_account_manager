// Package shallow implements "shallow profile" mode for caam — a per-identity
// HOME directory where only the auth-bearing files are real and everything else
// is a symlink back to the user's real HOME.
//
// This enables concurrent multi-account multiplexing (N parallel sessions, each
// pinned to a different account) while preserving shared state — shell history,
// git config, ssh keys, Claude conversation history (~/.claude/projects/), etc.
//
// Layout for ~/orch-homes/<name>/:
//
//	.claude/                       (real directory)
//	  .credentials.json            (real file — copied from a vault profile)
//	  .credentials.lock            (real file — empty, prevents Claude from
//	                                touching the symlinked .claude folder above)
//	  projects/, todos/, ...       (symlinks to ~/.claude/projects, etc.)
//	.claude.json                   (real file — copy of the user's settings;
//	                                Claude rewrites this in-place during runtime)
//	.bashrc, .gitconfig, .ssh, ... (symlinks to ~/.bashrc, etc.)
//
// Why some HOME entries must be real:
//
//   - .claude/.credentials.json — the entire point: per-identity OAuth tokens.
//   - .claude/.credentials.lock — Claude Code's flock target. If this were a
//     symlink to ~/.claude/.credentials.lock, two concurrent sessions would
//     contend on the *same* lock and serialize each other.
//   - .claude.json — Claude Code rewrites this on every run (writing identity
//     and settings); a symlink would mutate the user's real ~/.claude.json
//     under the shallow session's identity.
//
// Everything else is symlinked, so shells, git, SSH, and Claude conversation
// history pass through unchanged.
package shallow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProfileMetaFilename is the JSON sidecar that records when/where the
// shallow profile was created and which credential source was used.
const ProfileMetaFilename = ".caam-shallow.json"

// Real (non-symlinked) entries inside the shallow HOME, given relative
// to that HOME. These are created/managed by caam directly.
//
// Anything *not* listed here is a candidate for symlinking back to the
// user's real HOME.
var realEntries = []string{
	".claude/.credentials.json",
	".claude/.credentials.lock",
	".claude.json",
	ProfileMetaFilename,
}

// realDirs are directories that must exist as real directories inside the
// shallow HOME so that real files can live in them. The symlink farm walks
// the user's real HOME and skips these names so the .claude/ directory
// itself isn't replaced by a symlink to ~/.claude (which would re-share
// the identity-bearing files).
var realDirs = map[string]bool{
	".claude": true,
}

// alwaysSkip lists top-level entries in the user's real HOME that should
// NEVER be symlinked. We never want to recursively expose the real HOME
// inside the shallow HOME, and we never want to capture leftover orch-homes
// or caam-managed state.
var alwaysSkip = map[string]bool{
	".":          true,
	"..":         true,
	"orch-homes": true, // user-style default location
}

// Meta is the JSON sidecar persisted at ~/orch-homes/<name>/.caam-shallow.json.
type Meta struct {
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
	CredentialFrom string    `json:"credential_from,omitempty"`
	RealHome       string    `json:"real_home"`
	Version        int       `json:"version"`
}

// Manager handles creation, listing, deletion, and inspection of shallow
// profiles. It is safe to construct cheaply and use across calls.
type Manager struct {
	baseDir  string // e.g. ~/orch-homes
	realHome string // e.g. /home/user
}

// NewManager creates a Manager rooted at baseDir, using realHome as the
// symlink target. If baseDir is empty, DefaultBaseDir() is used.
func NewManager(baseDir, realHome string) (*Manager, error) {
	if strings.TrimSpace(realHome) == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user home: %w", err)
		}
		realHome = h
	}
	if strings.TrimSpace(baseDir) == "" {
		baseDir = DefaultBaseDir(realHome)
	}
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve base dir: %w", err)
	}
	rabs, err := filepath.Abs(realHome)
	if err != nil {
		return nil, fmt.Errorf("resolve real home: %w", err)
	}
	if abs == rabs {
		return nil, fmt.Errorf("shallow base dir cannot be the user's real home")
	}
	return &Manager{baseDir: abs, realHome: rabs}, nil
}

// DefaultBaseDir returns the default location for shallow profiles.
// Order of precedence:
//  1. $CAAM_SHALLOW_HOMES_DIR (full override)
//  2. $CAAM_HOME/shallow-homes (if CAAM_HOME is set)
//  3. <realHome>/orch-homes (matches the "homemade" convention from issue #16)
func DefaultBaseDir(realHome string) string {
	if v := strings.TrimSpace(os.Getenv("CAAM_SHALLOW_HOMES_DIR")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CAAM_HOME")); v != "" {
		return filepath.Join(v, "shallow-homes")
	}
	return filepath.Join(realHome, "orch-homes")
}

// BaseDir returns the directory that holds all shallow profiles for this manager.
func (m *Manager) BaseDir() string { return m.baseDir }

// RealHome returns the symlink target HOME used for passthroughs.
func (m *Manager) RealHome() string { return m.realHome }

// HomeFor returns the absolute path of the shallow HOME for the named profile.
func (m *Manager) HomeFor(name string) (string, error) {
	clean, err := validateProfileName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.baseDir, clean), nil
}

// CreateOptions controls how a shallow profile is provisioned.
type CreateOptions struct {
	// CredentialSource is a path to a file whose contents will be copied
	// to <home>/.claude/.credentials.json. If empty, the credentials file
	// is left empty and the caller must populate it (e.g., via `caam login`).
	CredentialSource string

	// SourceClaudeJSON is an optional path whose contents will be copied
	// to <home>/.claude.json. If empty, a minimal skeleton is written.
	SourceClaudeJSON string

	// CredentialFromLabel is recorded in the metadata file and surfaced by
	// `shallow-profile list`. It is purely descriptive (e.g. "vault:claude/alice").
	CredentialFromLabel string

	// Force overwrites an existing shallow profile of the same name.
	// Without this, Create returns an error if the directory already exists.
	Force bool
}

// Create provisions a new shallow profile.
func (m *Manager) Create(name string, opts CreateOptions) (string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return "", err
	}

	if _, err := os.Lstat(home); err == nil {
		if !opts.Force {
			return "", fmt.Errorf("shallow profile %q already exists at %s (use --force to overwrite)", name, home)
		}
		if err := os.RemoveAll(home); err != nil {
			return "", fmt.Errorf("remove existing profile: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat profile dir: %w", err)
	}

	// Create base directories with restrictive perms (auth tokens live here).
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create profile dir: %w", err)
	}
	for dirName := range realDirs {
		if err := os.MkdirAll(filepath.Join(home, dirName), 0o700); err != nil {
			return "", fmt.Errorf("create real dir %s: %w", dirName, err)
		}
	}

	// Lay down the symlink farm before writing real files. (Symlink creation
	// will skip names that conflict with realDirs/realEntries.)
	if err := m.populateSymlinks(home); err != nil {
		return "", fmt.Errorf("populate symlinks: %w", err)
	}

	// For each top-level real-dir, also lay down inner symlinks so that
	// subdirectories like .claude/projects, .claude/todos, .claude/shell-snapshots
	// pass through to the user's real HOME.
	for dirName := range realDirs {
		if err := m.populateInnerSymlinks(home, dirName); err != nil {
			return "", fmt.Errorf("populate %s symlinks: %w", dirName, err)
		}
	}

	// Write the credential file.
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	if opts.CredentialSource != "" {
		if err := copyFileMode(opts.CredentialSource, credPath, 0o600); err != nil {
			return "", fmt.Errorf("copy credentials: %w", err)
		}
	} else {
		// Empty placeholder so the file exists with the right perms; Claude
		// will overwrite on first login.
		if err := writeFileAtomic(credPath, []byte(""), 0o600); err != nil {
			return "", fmt.Errorf("write empty credentials: %w", err)
		}
	}

	// Always create an empty .credentials.lock so flock has its own target.
	lockPath := filepath.Join(home, ".claude", ".credentials.lock")
	if err := writeFileAtomic(lockPath, []byte(""), 0o600); err != nil {
		return "", fmt.Errorf("create credentials lock: %w", err)
	}

	// Write .claude.json (settings/state). Prefer an explicit source; fall
	// back to the user's real ~/.claude.json (so onboarding is automatic);
	// otherwise emit a minimal skeleton.
	claudeJSONPath := filepath.Join(home, ".claude.json")
	switch {
	case opts.SourceClaudeJSON != "":
		if err := copyFileMode(opts.SourceClaudeJSON, claudeJSONPath, 0o600); err != nil {
			return "", fmt.Errorf("copy .claude.json: %w", err)
		}
	default:
		realClaudeJSON := filepath.Join(m.realHome, ".claude.json")
		if _, err := os.Stat(realClaudeJSON); err == nil {
			if cerr := copyFileMode(realClaudeJSON, claudeJSONPath, 0o600); cerr != nil {
				return "", fmt.Errorf("seed .claude.json from real HOME: %w", cerr)
			}
		} else {
			if err := writeFileAtomic(claudeJSONPath, []byte("{}\n"), 0o600); err != nil {
				return "", fmt.Errorf("write skeleton .claude.json: %w", err)
			}
		}
	}

	// Persist metadata sidecar.
	meta := Meta{
		Name:           name,
		CreatedAt:      time.Now().UTC(),
		CredentialFrom: opts.CredentialFromLabel,
		RealHome:       m.realHome,
		Version:        1,
	}
	if err := writeMeta(home, &meta); err != nil {
		return "", fmt.Errorf("write metadata: %w", err)
	}

	return home, nil
}

// populateSymlinks reads top-level entries in realHome and creates a symlink
// in home for each, skipping names that collide with realEntries/realDirs and
// names in alwaysSkip.
func (m *Manager) populateSymlinks(home string) error {
	entries, err := os.ReadDir(m.realHome)
	if err != nil {
		return fmt.Errorf("read real home %s: %w", m.realHome, err)
	}

	skip := map[string]bool{}
	for _, p := range realEntries {
		// Only the top-level component matters here (nested files live in
		// directories that are already in realDirs).
		top := strings.SplitN(p, string(os.PathSeparator), 2)[0]
		skip[top] = true
	}
	for d := range realDirs {
		skip[d] = true
	}
	for k := range alwaysSkip {
		skip[k] = true
	}

	// Skip the shallow base dir itself if it's nested under realHome
	// (e.g. ~/orch-homes when realHome is ~).
	if rel, err := filepath.Rel(m.realHome, m.baseDir); err == nil && !strings.HasPrefix(rel, "..") {
		top := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		if top != "" && top != "." {
			skip[top] = true
		}
	}

	for _, e := range entries {
		name := e.Name()
		if skip[name] {
			continue
		}
		src := filepath.Join(m.realHome, name)
		dst := filepath.Join(home, name)
		// If the source has vanished mid-iteration, skip silently.
		if _, err := os.Lstat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat source %s: %w", src, err)
		}
		if err := atomicSymlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dst, src, err)
		}
	}
	return nil
}

// populateInnerSymlinks creates per-entry symlinks inside a real directory
// (e.g. inside .claude/) for everything that exists in the corresponding real
// HOME directory and isn't on the realEntries list.
//
// Smart fallback: if the source ~/<dirName> doesn't exist, this is a no-op.
func (m *Manager) populateInnerSymlinks(home, dirName string) error {
	srcDir := filepath.Join(m.realHome, dirName)
	dstDir := filepath.Join(home, dirName)

	if _, err := os.Stat(srcDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // smart fallback: no source, no symlinks needed
		}
		return fmt.Errorf("stat %s: %w", srcDir, err)
	}

	skip := map[string]bool{}
	for _, p := range realEntries {
		parts := strings.SplitN(p, string(os.PathSeparator), 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == dirName {
			skip[parts[1]] = true
		}
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcDir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if skip[name] {
			continue
		}
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(dstDir, name)
		if err := atomicSymlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", dst, src, err)
		}
	}
	return nil
}

// Profile is a lightweight summary returned by List.
type Profile struct {
	Name string
	Path string
	Meta *Meta // may be nil if metadata is missing/corrupt
}

// List returns all shallow profiles under the manager's base dir, sorted by name.
func (m *Manager) List() ([]Profile, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read base dir: %w", err)
	}
	var out []Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, err := validateProfileName(name); err != nil {
			continue
		}
		home := filepath.Join(m.baseDir, name)
		p := Profile{Name: name, Path: home}
		if meta, err := readMeta(home); err == nil {
			p.Meta = meta
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get loads a single profile by name. Returns os.ErrNotExist if missing.
func (m *Manager) Get(name string) (*Profile, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(home)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", home)
	}
	p := &Profile{Name: name, Path: home}
	if meta, err := readMeta(home); err == nil {
		p.Meta = meta
	}
	return p, nil
}

// Delete removes a shallow profile and all its files. It is safe even if
// the directory contains symlinks: os.RemoveAll never traverses them.
func (m *Manager) Delete(name string) error {
	home, err := m.HomeFor(name)
	if err != nil {
		return err
	}
	st, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", home)
	}
	// Sanity guard: never delete the user's real HOME by accident.
	if abs, err := filepath.Abs(home); err == nil && abs == m.realHome {
		return fmt.Errorf("refusing to delete real HOME (%s)", abs)
	}
	return os.RemoveAll(home)
}

// CredentialPath returns the absolute path to a profile's .credentials.json.
func (m *Manager) CredentialPath(name string) (string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

// validateProfileName enforces a small, safe character set for profile names
// (matching the regex used elsewhere in caam) and rejects path-traversal.
func validateProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("profile name cannot be empty")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("invalid profile name: %q", name)
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '@' || r == '+') {
			return "", fmt.Errorf("invalid profile name: %q (only alphanumeric, _, -, ., @, + allowed)", name)
		}
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("invalid profile name: %q", name)
	}
	if strings.Contains(name, string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid profile name: %q (path separator not allowed)", name)
	}
	return name, nil
}

// atomicSymlink replaces (or creates) dst as a symlink pointing at src.
// If dst exists as a symlink or non-directory, it is replaced. If it exists
// as a real directory, the call is a no-op (we don't clobber real data).
func atomicSymlink(src, dst string) error {
	if info, err := os.Lstat(dst); err == nil {
		if info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
			return nil // preserve existing real directory
		}
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove existing %s: %w", dst, err)
		}
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create parent: %w", err)
	}
	return os.Symlink(src, dst)
}

// copyFileMode copies src to dst, then chmods dst to mode. The destination
// is written via a temp file in the same directory and renamed in place.
func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create parent: %w", err)
	}

	tmp, err := os.CreateTemp(parent, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		if _, err := os.Stat(tmpName); err == nil {
			cleanup()
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	tmp = nil // prevent the deferred Close from racing rename
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to path with mode, atomically.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create parent: %w", err)
	}
	tmp, err := os.CreateTemp(parent, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, err := os.Stat(tmpName); err == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return os.Rename(tmpName, path)
}

func writeMeta(home string, m *Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(filepath.Join(home, ProfileMetaFilename), data, 0o600)
}

func readMeta(home string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(home, ProfileMetaFilename))
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
