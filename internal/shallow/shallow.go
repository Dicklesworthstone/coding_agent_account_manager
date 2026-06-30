// Package shallow implements "shallow profile" mode for caam — a per-identity
// HOME directory where only the auth-bearing files are real and everything else
// is a symlink back to the user's real HOME.
//
// This enables concurrent multi-account multiplexing (N parallel sessions, each
// pinned to a different account) while preserving shared state — shell history,
// git config, ssh keys, conversation history, etc.
//
// The layout is provider-specific (see layoutFor): claude isolates
// ~/.claude/.credentials.json, codex isolates ~/.codex/{auth.json,config.toml},
// and agy isolates the ~/.gemini antigravity identity files. The Claude layout
// is shown below as the canonical example.
//
// Layout for ~/orch-homes/<name>/ (claude):
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

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
)

// ProfileMetaFilename is the JSON sidecar that records when/where the
// shallow profile was created and which credential source was used.
const ProfileMetaFilename = ".caam-shallow.json"

// alwaysSkip lists top-level entries in the user's real HOME that should
// NEVER be symlinked. We never want to recursively expose the real HOME
// inside the shallow HOME, and we never want to capture leftover orch-homes
// or caam-managed state.
var alwaysSkip = map[string]bool{
	".":          true,
	"..":         true,
	"orch-homes": true, // user-style default location
}

// providerLayout describes a provider's shallow HOME layout: which paths are
// REAL (private, never symlinked) versus symlinked passthroughs to the user's
// real HOME. This is the security boundary — any directory that can hold
// identity-bearing files MUST appear in realDirs so the top-level symlink farm
// never links it back to the real HOME (which would re-share the real identity
// into the "isolated" shallow session, collapsing accounts together).
//
// realEntries are individual files managed by caam directly; both the top-level
// symlink farm and the inner-symlink population skip them so they stay real and
// private (never symlinked to the real HOME's copy).
type providerLayout struct {
	provider          string
	realDirs          []string // relpaths created as real 0700 dirs, excluded from the farm
	realEntries       []string // relpaths of managed real files (never symlinked)
	innerSymlinkRoots []string // real dirs whose non-managed children are symlinked through
	primaryCredRel    string   // principal credential file (target of --from-file / vault primary)
}

// SupportedProviders lists the shallow-capable providers in display order.
func SupportedProviders() []string { return []string{"claude", "codex", "agy"} }

// NormalizeProvider lowercases/trims a provider id and maps "" to "claude" —
// the original single-provider default and the back-compat value for shallow
// profiles created before the provider was recorded in metadata.
func NormalizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "claude"
	}
	return p
}

// layoutFor returns the shallow layout for a provider. An unknown provider is a
// hard error: we must NEVER silently fall back to a Claude layout for, say,
// codex, because that would write the codex auth into the wrong place and could
// leak or mishandle the identity.
func layoutFor(provider string) (*providerLayout, error) {
	switch NormalizeProvider(provider) {
	case "claude":
		return &providerLayout{
			provider:          "claude",
			realDirs:          []string{".claude"},
			realEntries:       []string{".claude/.credentials.json", ".claude/.credentials.lock", ".claude.json", ProfileMetaFilename},
			innerSymlinkRoots: []string{".claude"},
			primaryCredRel:    ".claude/.credentials.json",
		}, nil
	case "codex":
		return &providerLayout{
			provider:          "codex",
			realDirs:          []string{".codex"},
			realEntries:       []string{".codex/auth.json", ".codex/config.toml", ProfileMetaFilename},
			innerSymlinkRoots: []string{".codex"},
			primaryCredRel:    ".codex/auth.json",
		}, nil
	case "agy":
		return &providerLayout{
			provider: "agy",
			realDirs: []string{".gemini", ".gemini/antigravity-cli"},
			realEntries: []string{
				".gemini/antigravity-cli/antigravity-oauth-token",
				".gemini/antigravity-cli/settings.json",
				".gemini/google_accounts.json",
				".gemini/oauth_creds.json",
				ProfileMetaFilename,
			},
			innerSymlinkRoots: []string{".gemini", ".gemini/antigravity-cli"},
			primaryCredRel:    ".gemini/antigravity-cli/antigravity-oauth-token",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported shallow provider %q (supported: %s)", provider, strings.Join(SupportedProviders(), ", "))
	}
}

// topComponent returns the first path element of a slash- or OS-separated
// relpath (e.g. ".gemini/antigravity-cli/x" -> ".gemini").
func topComponent(rel string) string {
	rel = filepath.ToSlash(rel)
	return strings.SplitN(rel, "/", 2)[0]
}

// childUnder returns the first path component of rel strictly below dir
// (childUnder(".gemini", ".gemini/antigravity-cli/x") == "antigravity-cli"),
// or "" when rel is not under dir.
func childUnder(dir, rel string) string {
	dir = filepath.ToSlash(dir)
	rel = filepath.ToSlash(rel)
	prefix := dir + "/"
	if !strings.HasPrefix(rel, prefix) {
		return ""
	}
	return strings.SplitN(rel[len(prefix):], "/", 2)[0]
}

// SpawnEnv returns the environment overrides to set and the inherited variable
// names to scrub when running a command under a shallow profile of the given
// provider. This is the second half of the identity boundary: HOME is repointed
// at the shallow home, and any provider-specific "home" override a parent shell
// might have exported (which would otherwise pull the real identity back in) is
// either pinned to the shallow location or scrubbed.
//
// SpawnEnv is a pure function so the env-isolation policy is unit-testable.
func SpawnEnv(provider, home, name string) (set map[string]string, scrub []string) {
	set = map[string]string{
		"HOME":            home,
		"SHALLOW_PROFILE": name,
	}
	switch NormalizeProvider(provider) {
	case "claude":
		// A stale CLAUDE_CONFIG_DIR from a parent shell would pin auth.json
		// outside the shallow HOME, re-sharing the user's real identity.
		scrub = []string{"CLAUDE_CONFIG_DIR"}
	case "codex":
		// Pin CODEX_HOME inside the shallow HOME, overriding any inherited value
		// so a parent CODEX_HOME cannot leak the real ~/.codex/auth.json.
		set["CODEX_HOME"] = filepath.Join(home, ".codex")
	case "agy":
		// Pin GEMINI_HOME inside the shallow HOME so the antigravity token and
		// Google identity files resolve to the per-identity copies, overriding
		// any inherited GEMINI_HOME that could point back at the real ~/.gemini.
		set["GEMINI_HOME"] = filepath.Join(home, ".gemini")
	}
	return set, scrub
}

// Meta is the JSON sidecar persisted at ~/orch-homes/<name>/.caam-shallow.json.
type Meta struct {
	Name string `json:"name"`
	// Provider is the shallow layout this profile uses (claude, codex, agy).
	// Profiles created before this field existed have it empty; readMeta then
	// defaults them to "claude" (the only layout that existed at the time).
	Provider       string    `json:"provider,omitempty"`
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
	// Provider selects the shallow layout (claude, codex, agy). Empty == claude
	// for backward compatibility.
	Provider string

	// CredentialSource is a path to a file whose contents will be copied to the
	// provider's PRIMARY credential file (claude .claude/.credentials.json,
	// codex .codex/auth.json, agy .gemini/antigravity-cli/antigravity-oauth-token).
	// If empty, the credential file is created empty and the caller must populate
	// it (e.g., by signing in inside the shallow HOME).
	CredentialSource string

	// ExtraSources maps additional managed real-file destination relpaths (within
	// the shallow HOME) to source file paths. Used for multi-file providers — the
	// agy optional google_accounts.json / oauth_creds.json / settings.json. Each
	// destination MUST be in the provider's realEntries set; each source is copied
	// mode 0600 if it exists and skipped if absent.
	ExtraSources map[string]string

	// SourceClaudeJSON is an optional path whose contents will be copied to
	// <home>/.claude.json (claude only). If empty, a minimal skeleton is written.
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
	layout, err := layoutFor(opts.Provider)
	if err != nil {
		return "", err
	}

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
	for _, dirName := range layout.realDirs {
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(dirName)), 0o700); err != nil {
			return "", fmt.Errorf("create real dir %s: %w", dirName, err)
		}
	}

	// Lay down the symlink farm before writing real files. (Symlink creation
	// will skip names that conflict with the provider's realDirs/realEntries.)
	if err := m.populateSymlinks(home, layout); err != nil {
		return "", fmt.Errorf("populate symlinks: %w", err)
	}

	// For each real-dir root, also lay down inner symlinks so that non-managed
	// subdirectories (e.g. .claude/projects, .codex/sessions, .gemini history)
	// pass through to the user's real HOME.
	for _, dirName := range layout.innerSymlinkRoots {
		if err := m.populateInnerSymlinks(home, dirName, layout); err != nil {
			return "", fmt.Errorf("populate %s symlinks: %w", dirName, err)
		}
	}

	// Write the provider's real (private) files.
	if err := m.writeRealFiles(home, layout, opts); err != nil {
		return "", err
	}

	// Persist metadata sidecar.
	meta := Meta{
		Name:           name,
		Provider:       layout.provider,
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

// writeRealFiles writes a provider's managed real (non-symlinked) files into the
// shallow HOME: the principal credential file plus any provider-specific extras
// (Claude's lock + .claude.json, Codex's file-store config.toml, agy's optional
// Google identity files). These paths are all in the provider's realEntries set
// so the symlink farm leaves them real and private.
func (m *Manager) writeRealFiles(home string, layout *providerLayout, opts CreateOptions) error {
	// Principal credential file (every provider has exactly one).
	primary := filepath.Join(home, filepath.FromSlash(layout.primaryCredRel))
	if opts.CredentialSource != "" {
		if err := copyFileMode(opts.CredentialSource, primary, 0o600); err != nil {
			return fmt.Errorf("copy credentials: %w", err)
		}
	} else {
		// Empty placeholder so the file exists with tight perms; the tool will
		// overwrite it on first login inside the shallow HOME.
		if err := writeFileAtomic(primary, []byte(""), 0o600); err != nil {
			return fmt.Errorf("write empty credentials: %w", err)
		}
	}

	switch layout.provider {
	case "claude":
		// Per-identity flock target so two concurrent Claude sessions don't
		// serialize on a shared lock.
		lockPath := filepath.Join(home, ".claude", ".credentials.lock")
		if err := writeFileAtomic(lockPath, []byte(""), 0o600); err != nil {
			return fmt.Errorf("create credentials lock: %w", err)
		}
		if err := m.writeClaudeJSON(home, opts); err != nil {
			return err
		}
	case "codex":
		// Enforce file-based credential storage so codex reads our auth.json
		// rather than an OS keychain. Reuses the provider's own helper so the
		// exact config setting stays in one place.
		if err := codex.EnsureFileCredentialStore(filepath.Join(home, ".codex")); err != nil {
			return fmt.Errorf("configure codex credential store: %w", err)
		}
	case "agy":
		if err := m.writeExtraSources(home, layout, opts); err != nil {
			return err
		}
	}
	return nil
}

// writeClaudeJSON writes <home>/.claude.json. Prefer an explicit source; fall
// back to the user's real ~/.claude.json (automatic onboarding); otherwise emit
// a minimal skeleton. (Claude rewrites this file in place at runtime.)
func (m *Manager) writeClaudeJSON(home string, opts CreateOptions) error {
	claudeJSONPath := filepath.Join(home, ".claude.json")
	if opts.SourceClaudeJSON != "" {
		if err := copyFileMode(opts.SourceClaudeJSON, claudeJSONPath, 0o600); err != nil {
			return fmt.Errorf("copy .claude.json: %w", err)
		}
		return nil
	}
	realClaudeJSON := filepath.Join(m.realHome, ".claude.json")
	if _, err := os.Stat(realClaudeJSON); err == nil {
		if cerr := copyFileMode(realClaudeJSON, claudeJSONPath, 0o600); cerr != nil {
			return fmt.Errorf("seed .claude.json from real HOME: %w", cerr)
		}
		return nil
	}
	if err := writeFileAtomic(claudeJSONPath, []byte("{}\n"), 0o600); err != nil {
		return fmt.Errorf("write skeleton .claude.json: %w", err)
	}
	return nil
}

// writeExtraSources copies opts.ExtraSources into the shallow HOME. Each
// destination MUST be one of the provider's managed realEntries — this is a
// hard security guard so a caller can never coax Create into writing outside the
// managed real-file set (which could clobber a symlinked passthrough or escape
// the shallow HOME). Missing sources are skipped (the files are optional).
func (m *Manager) writeExtraSources(home string, layout *providerLayout, opts CreateOptions) error {
	allowed := map[string]bool{}
	for _, e := range layout.realEntries {
		allowed[filepath.ToSlash(e)] = true
	}
	dests := make([]string, 0, len(opts.ExtraSources))
	for d := range opts.ExtraSources {
		dests = append(dests, d)
	}
	sort.Strings(dests) // deterministic order
	for _, destRel := range dests {
		rel := filepath.ToSlash(destRel)
		if !allowed[rel] {
			return fmt.Errorf("refusing to write unmanaged shallow file %q (not in the %s real-file set)", destRel, layout.provider)
		}
		src := opts.ExtraSources[destRel]
		if src == "" {
			continue
		}
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat extra source %s: %w", src, err)
		}
		if err := copyFileMode(src, filepath.Join(home, filepath.FromSlash(rel)), 0o600); err != nil {
			return fmt.Errorf("copy %s: %w", destRel, err)
		}
	}
	return nil
}

// populateSymlinks reads top-level entries in realHome and creates a symlink
// in home for each, skipping names that collide with the provider's
// realEntries/realDirs and names in alwaysSkip.
func (m *Manager) populateSymlinks(home string, layout *providerLayout) error {
	entries, err := os.ReadDir(m.realHome)
	if err != nil {
		return fmt.Errorf("read real home %s: %w", m.realHome, err)
	}

	skip := map[string]bool{}
	for _, p := range layout.realEntries {
		// Only the top-level component matters here (nested files live under
		// directories that are already in realDirs).
		skip[topComponent(p)] = true
	}
	for _, d := range layout.realDirs {
		skip[topComponent(d)] = true
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
// (e.g. inside .claude/ or .gemini/) for everything that exists in the
// corresponding real HOME directory and is neither a managed realEntry nor a
// nested realDir (those stay real/private).
//
// Smart fallback: if the source ~/<dirName> doesn't exist, this is a no-op.
func (m *Manager) populateInnerSymlinks(home, dirName string, layout *providerLayout) error {
	srcDir := filepath.Join(m.realHome, filepath.FromSlash(dirName))
	dstDir := filepath.Join(home, filepath.FromSlash(dirName))

	if _, err := os.Stat(srcDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // smart fallback: no source, no symlinks needed
		}
		return fmt.Errorf("stat %s: %w", srcDir, err)
	}

	skip := map[string]bool{}
	for _, p := range layout.realEntries {
		if c := childUnder(dirName, p); c != "" {
			skip[c] = true
		}
	}
	// Never symlink a nested realDir (e.g. .gemini/antigravity-cli inside
	// .gemini) — it must stay a real, private directory.
	for _, d := range layout.realDirs {
		if c := childUnder(dirName, d); c != "" {
			skip[c] = true
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

// CredentialPath returns the absolute path to a profile's principal credential
// file. The provider is read from the profile's metadata (defaulting to claude
// for legacy profiles and when metadata is unavailable), so this resolves to
// .claude/.credentials.json, .codex/auth.json, or the agy token as appropriate.
func (m *Manager) CredentialPath(name string) (string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return "", err
	}
	provider := "claude"
	if meta, err := readMeta(home); err == nil && meta.Provider != "" {
		provider = meta.Provider
	}
	layout, err := layoutFor(provider)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, filepath.FromSlash(layout.primaryCredRel)), nil
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
	// Back-compat: profiles created before the provider field existed are
	// Claude profiles (the only layout at the time).
	if strings.TrimSpace(m.Provider) == "" {
		m.Provider = "claude"
	}
	return &m, nil
}
