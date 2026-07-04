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

// reservedDirPrefix marks caam's own transient working directories (build
// staging and swap backups) inside the shallow base dir. Entries with this
// prefix are internal scaffolding, never a user profile: List() hides them and
// Create() refuses to mint a profile whose name would collide with them.
const reservedDirPrefix = ".caam-tmp-"

const (
	stagingDirPrefix = reservedDirPrefix + "staging-"
	backupDirPrefix  = reservedDirPrefix + "backup-"
)

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
	// caam's own vault-locating variables are scrubbed for EVERY provider. A
	// spawned caam process inherits the parent environment wholesale, so an
	// inherited CAAM_HOME / XDG_DATA_HOME pointing at the real caam home would let
	// it resolve the REAL vault — the master store of every account's tokens —
	// even after the symlink farm is hardened (issue #41). With them scrubbed,
	// caam resolves its vault under the shallow HOME, where the caam data subtree
	// is deliberately withheld, so the spawned process sees no other identities.
	scrub = []string{"CAAM_HOME", "XDG_DATA_HOME"}
	switch NormalizeProvider(provider) {
	case "claude":
		// A stale CLAUDE_CONFIG_DIR from a parent shell would pin auth.json
		// outside the shallow HOME, re-sharing the user's real identity.
		scrub = append(scrub, "CLAUDE_CONFIG_DIR")
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
	// Profiles created before this field existed have it empty; resolveProvider
	// ForHome recovers the provider for such legacy metadata (from the credential
	// label, else the claude default — issue #50) rather than readMeta rewriting
	// this field.
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
//
// Create is crash- and failure-safe with respect to an existing profile of the
// same name (--force): it builds the entire replacement in a staging directory
// FIRST and only swaps it into place once it is fully built. A failure at any
// step (bad --from-claude-json, an unreadable real HOME, a source that vanished
// mid-run, …) therefore leaves the pre-existing profile completely intact — the
// live per-identity OAuth token is never destroyed before its replacement
// exists (issue #42). It also refuses to touch anything that is, or contains,
// the user's real HOME (issue #38).
func (m *Manager) Create(name string, opts CreateOptions) (string, error) {
	layout, err := layoutFor(opts.Provider)
	if err != nil {
		return "", err
	}

	clean, err := validateProfileName(name)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(clean, reservedDirPrefix) {
		return "", fmt.Errorf("invalid profile name %q: the %q prefix is reserved for caam's internal use", name, reservedDirPrefix)
	}
	home := filepath.Join(m.baseDir, clean)

	// Hard safety invariant: a shallow profile's home must live strictly inside
	// the shallow base dir and must never be (or contain) the user's real HOME.
	// This is checked BEFORE anything is created or removed so a misconfigured
	// --base / $CAAM_SHALLOW_HOMES_DIR (e.g. --base /home with profile "alice"
	// resolving to the real /home/alice) can never lead to destroying real data.
	if err := m.assertSafeProfileHome(home); err != nil {
		return "", err
	}

	exists := false
	if _, err := os.Lstat(home); err == nil {
		if !opts.Force {
			return "", fmt.Errorf("shallow profile %q already exists at %s (use --force to overwrite)", name, home)
		}
		// --force only overwrites something that actually looks like a shallow
		// profile (has the metadata sidecar). This stops a stray --base that
		// happens to collide with an unrelated real directory from being wiped.
		if err := assertLooksLikeShallowProfile(home); err != nil {
			return "", err
		}
		exists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat profile dir: %w", err)
	}

	// Ensure the base dir exists so the staging dir and the final rename happen
	// on the same filesystem (rename is only atomic within one filesystem).
	if err := os.MkdirAll(m.baseDir, 0o700); err != nil {
		return "", fmt.Errorf("create base dir: %w", err)
	}

	// Build the whole profile in a staging dir first; nothing destructive has
	// happened to an existing profile at this point.
	staging, err := os.MkdirTemp(m.baseDir, stagingDirPrefix+clean+"-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	if err := m.buildProfile(staging, name, layout, opts); err != nil {
		_ = m.safeRemoveAll(staging)
		return "", err
	}

	// Swap the fully-built staging profile into place. Only here, and only after
	// the new profile is complete, is the previous profile removed.
	if err := m.swapIntoPlace(staging, home, clean, exists); err != nil {
		_ = m.safeRemoveAll(staging)
		return "", err
	}

	return home, nil
}

// buildProfile provisions a complete shallow profile into dir (typically a
// staging directory). It performs every fallible step — real dirs, the symlink
// farm, inner symlinks, the real credential/config files, and the metadata
// sidecar — so callers can build-then-swap without ever leaving a live profile
// in a half-rebuilt state.
func (m *Manager) buildProfile(dir, name string, layout *providerLayout, opts CreateOptions) error {
	// Create base directories with restrictive perms (auth tokens live here).
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	for _, dirName := range layout.realDirs {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(dirName)), 0o700); err != nil {
			return fmt.Errorf("create real dir %s: %w", dirName, err)
		}
	}

	// Lay down the symlink farm before writing real files. (Symlink creation
	// will skip names that conflict with the provider's realDirs/realEntries.)
	if err := m.populateSymlinks(dir, layout); err != nil {
		return fmt.Errorf("populate symlinks: %w", err)
	}

	// For each real-dir root, also lay down inner symlinks so that non-managed
	// subdirectories (e.g. .claude/projects, .codex/sessions, .gemini history)
	// pass through to the user's real HOME.
	for _, dirName := range layout.innerSymlinkRoots {
		if err := m.populateInnerSymlinks(dir, dirName, layout); err != nil {
			return fmt.Errorf("populate %s symlinks: %w", dirName, err)
		}
	}

	// Write the provider's real (private) files.
	if err := m.writeRealFiles(dir, layout, opts); err != nil {
		return err
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
	if err := writeMeta(dir, &meta); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

// swapIntoPlace atomically installs a freshly-built staging profile at home.
//
// When no previous profile exists, it is a single rename. When --force is
// replacing an existing profile, the old profile is first moved aside to a
// backup, the new one is renamed into place, and only THEN is the backup
// removed; if the final rename fails, the backup is restored so the user is
// never left without their original profile (issue #42). The symlink farm
// survives the moves because its targets are absolute real-HOME paths.
func (m *Manager) swapIntoPlace(staging, home, clean string, exists bool) error {
	if !exists {
		if err := os.Rename(staging, home); err != nil {
			return fmt.Errorf("activate new profile: %w", err)
		}
		return nil
	}

	// Reserve a unique backup path, then free the name so we can rename the old
	// profile onto it. (Renaming a directory onto an *existing* directory is not
	// portable — some filesystems reject it even when the target is empty — so
	// we rename onto a fresh, non-existent name instead.)
	backup, err := os.MkdirTemp(m.baseDir, backupDirPrefix+clean+"-")
	if err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("prepare backup slot: %w", err)
	}
	if err := os.Rename(home, backup); err != nil {
		return fmt.Errorf("move existing profile aside: %w", err)
	}

	if err := os.Rename(staging, home); err != nil {
		// Roll back: restore the original profile so nothing is lost.
		if rbErr := os.Rename(backup, home); rbErr != nil {
			return fmt.Errorf("activate rebuilt profile: %w (original profile preserved at %s; restore failed: %v)", err, backup, rbErr)
		}
		return fmt.Errorf("activate rebuilt profile: %w (original profile restored)", err)
	}

	// New profile is live; the old one can go. A failure here is non-fatal —
	// the new profile is already in place — so leave the backup rather than
	// error out on a successful create.
	_ = m.safeRemoveAll(backup)
	return nil
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
		// Seed the shallow config.toml from the user's real ~/.codex/config.toml
		// (preserving [mcp_servers.*] and every other user section), THEN force
		// file-based credential storage on top of it. Without the seed, the
		// shallow .codex starts empty and EnsureFileCredentialStore would write a
		// stub config containing only the credential-store line, silently dropping
		// the user's MCP configuration so MCP tools stop loading (issue #46).
		if err := m.writeCodexConfig(home); err != nil {
			return err
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

// writeCodexConfig provisions <home>/.codex/config.toml for a shallow codex
// profile. It carries the user's real ~/.codex/config.toml over verbatim (so
// MCP server sections and every other user setting survive) and then enforces
// file-based credential storage on top of it. The real config.toml is NOT
// symlinked (it is a managed realEntry, so a spawned session writing to it can't
// mutate the user's real one); seeding a copy is what preserves the content
// (issue #46).
//
// If the real config.toml is absent, we fall back to EnsureFileCredentialStore's
// minimal stub — there is simply nothing to preserve. If a config already exists
// in the (staging) shallow HOME, it is kept and only the credential-store line
// is reconciled.
func (m *Manager) writeCodexConfig(home string) error {
	shallowCodex := filepath.Join(home, ".codex")
	shallowConfig := filepath.Join(shallowCodex, "config.toml")

	// Only seed when we have not already written a shallow config (we never
	// clobber an existing one) and the real config exists.
	if _, err := os.Stat(shallowConfig); errors.Is(err, os.ErrNotExist) {
		realConfig := filepath.Join(m.realHome, ".codex", "config.toml")
		if _, rerr := os.Stat(realConfig); rerr == nil {
			if cerr := copyFileMode(realConfig, shallowConfig, 0o600); cerr != nil {
				return fmt.Errorf("seed .codex/config.toml from real HOME: %w", cerr)
			}
		} else if !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("stat real codex config: %w", rerr)
		}
	} else if err != nil {
		return fmt.Errorf("stat shallow codex config: %w", err)
	}

	// Force file-based credential storage (replaces a keychain setting if the
	// seeded config had one; appends the line otherwise; no-op if already file).
	if err := codex.EnsureFileCredentialStore(shallowCodex); err != nil {
		return fmt.Errorf("configure codex credential store: %w", err)
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

// allProviderReservedTops returns the set of top-level HOME components reserved
// by EVERY registered provider layout — its real dirs and its managed real
// files. A provider's auth root must never be symlinked into ANY profile, not
// just the one that owns it: otherwise a claude profile happily symlinks
// .codex -> ~/.codex and .gemini -> ~/.gemini straight back to the user's real
// Codex / Antigravity credentials, collapsing exactly the accounts the feature
// keeps apart (issue #40). The owning provider still MkdirAlls its own tops as
// real dirs and writes its managed files; for every OTHER provider the top is
// simply withheld from the farm, so the wrong harness fails closed ("not signed
// in") instead of silently using the real account.
func allProviderReservedTops() map[string]bool {
	tops := map[string]bool{}
	for _, p := range SupportedProviders() {
		layout, err := layoutFor(p)
		if err != nil {
			continue
		}
		for _, d := range layout.realDirs {
			tops[topComponent(d)] = true
		}
		for _, e := range layout.realEntries {
			tops[topComponent(e)] = true
		}
	}
	return tops
}

// caamOwnedRoots returns absolute, cleaned paths of caam's own state roots that
// must NEVER be reachable from inside a shallow profile — chiefly the credential
// vault's data dir, the master store of EVERY stored account's tokens. If any of
// these were symlinked into a profile, an agent pinned to identity A could read
// and exfiltrate identities B and C through its own HOME (issue #41).
//
// The default vault lives at <realHome>/.local/share/caam/vault; we withhold the
// whole <...>/caam data dir so the entire caam subtree — not just the vault — is
// unreachable. The CAAM_HOME / XDG_DATA_HOME candidates mirror the precedence in
// authfile.DefaultVaultPath so the relocated-vault variants are covered too.
// Callers compare these both lexically and symlink-resolved (see mirrorEntry) so
// a symlinked HOME component cannot hide the nesting.
func (m *Manager) caamOwnedRoots() []string {
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	// Default location, rooted at THIS manager's real HOME (which is what the
	// farm mirrors) rather than os.UserHomeDir.
	add(filepath.Join(m.realHome, ".local", "share", "caam"))
	// Relocated-vault variants. We withhold ALL candidate caam roots regardless
	// of precedence: whichever one actually holds the vault must be hidden, and
	// over-withholding an empty caam dir is harmless.
	if caamHome := strings.TrimSpace(os.Getenv("CAAM_HOME")); caamHome != "" {
		add(caamHome)
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		add(filepath.Join(xdg, "caam"))
	}
	return roots
}

// rootRel classifies how a real-HOME path relates to caam's own state roots.
type rootRel int

const (
	rootUnrelated rootRel = iota // symlink wholesale (the normal passthrough)
	rootEquals                   // path IS a caam root — withhold entirely
	rootContains                 // path CONTAINS a caam root — carve it out
)

// classifyAgainstRoots reports how srcAbs relates to the caam-owned roots,
// comparing both lexically and with symlinks resolved (a symlinked HOME
// component must not hide the nesting — e.g. ~/.local -> /data/local while the
// vault sits at /data/local/share/caam).
func classifyAgainstRoots(srcAbs string, caamRoots []string) rootRel {
	srcVariants := []string{filepath.Clean(srcAbs), canonical(srcAbs)}
	rel := rootUnrelated
	for _, root := range caamRoots {
		for _, r := range []string{root, canonical(root)} {
			for _, s := range srcVariants {
				if s == r {
					return rootEquals // strongest verdict
				}
				if isEqualOrAncestor(s, r) {
					// s is a strict ancestor of a caam root: it must be carved.
					rel = rootContains
				}
			}
		}
	}
	return rel
}

// mirrorEntry places the real-HOME entry srcAbs into the profile at dstAbs.
// Normally this is a single wholesale symlink (the "everything else is
// symlinked" design). But a wholesale symlink of a directory that is, or
// contains, one of caam's own state roots would expose the credential vault
// through the profile HOME (issue #41), so:
//   - if srcAbs IS a caam-owned root, it is withheld entirely (no symlink);
//   - if srcAbs merely CONTAINS a caam-owned root, srcAbs is recreated as a real
//     directory and its children are mirrored one level deeper, carving out the
//     caam subtree while every sibling (e.g. ~/.local/bin, ~/.local/share/<app>)
//     still passes through as a symlink.
func (m *Manager) mirrorEntry(srcAbs, dstAbs string, caamRoots []string) error {
	switch classifyAgainstRoots(srcAbs, caamRoots) {
	case rootEquals:
		return nil // withhold caam-owned root entirely
	case rootContains:
		return m.mirrorInto(srcAbs, dstAbs, caamRoots)
	default:
		return atomicSymlink(srcAbs, dstAbs)
	}
}

// mirrorInto recreates srcDir as a real directory at dstDir and mirrors each of
// its children via mirrorEntry. Recursion is bounded: it only runs for a dir
// that strictly contains a caam root, and each step descends one level toward
// the finite set of roots, bottoming out at rootEquals (withheld) or
// rootUnrelated (symlinked).
func (m *Manager) mirrorInto(srcDir, dstDir string, caamRoots []string) error {
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("create carve-out dir %s: %w", dstDir, err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcDir, err)
	}
	for _, e := range entries {
		childSrc := filepath.Join(srcDir, e.Name())
		childDst := filepath.Join(dstDir, e.Name())
		if _, err := os.Lstat(childSrc); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // vanished mid-iteration
			}
			return fmt.Errorf("stat %s: %w", childSrc, err)
		}
		if err := m.mirrorEntry(childSrc, childDst, caamRoots); err != nil {
			return err
		}
	}
	return nil
}

// populateSymlinks reads top-level entries in realHome and creates a symlink
// in home for each, skipping names that collide with the provider's
// realEntries/realDirs and names in alwaysSkip. Two security narrowings apply:
// every provider's auth root is withheld (not just the active one — issue #40),
// and caam's own vault/data roots are carved out of the farm (issue #41).
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
	// Withhold EVERY provider's auth roots, not just the active one, so a claude
	// profile never symlinks .codex/.gemini back to the real identity (issue #40).
	for t := range allProviderReservedTops() {
		skip[t] = true
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

	caamRoots := m.caamOwnedRoots()

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
		// Normally a wholesale symlink; mirrorEntry carves out caam's own state
		// roots so the profile cannot read the master vault through its HOME.
		if err := m.mirrorEntry(src, dst, caamRoots); err != nil {
			return fmt.Errorf("mirror %s -> %s: %w", dst, src, err)
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
		// Skip caam's own transient staging/backup scaffolding — these are not
		// user profiles (they only exist mid-Create or after a crashed swap).
		if strings.HasPrefix(name, reservedDirPrefix) {
			continue
		}
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
	// Robust guard (shared with Create's --force path): never RemoveAll the real
	// HOME, an ancestor of it, the filesystem root, or anything outside the
	// shallow base dir — comparing both lexically and with symlinks resolved.
	return m.safeRemoveAll(home)
}

// CredentialPath returns the absolute path to a profile's principal credential
// file (.claude/.credentials.json, .codex/auth.json, or the agy token). The
// provider is resolved via ResolveProvider, so — like shallow-spawn — it fails
// closed rather than silently assuming claude when the metadata is unreadable
// (which would point callers at the wrong provider's credential file).
func (m *Manager) CredentialPath(name string) (string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return "", err
	}
	provider, err := m.ResolveProvider(name)
	if err != nil {
		return "", err
	}
	layout, err := layoutFor(provider)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, filepath.FromSlash(layout.primaryCredRel)), nil
}

// ResolveProvider determines a profile's shallow provider for the purpose of
// env-isolation on spawn and credential resolution. It NEVER silently defaults
// to claude when the answer is unknown: mis-labelling a codex/agy profile as
// claude skips the CODEX_HOME/GEMINI_HOME pinning that keeps the real
// ~/.codex / ~/.gemini identity out of the session (issue #43). Resolution
// order:
//
//  1. If the metadata sidecar is readable and records an explicit provider,
//     trust it.
//  2. If the metadata is readable but the provider is blank (legacy pre-provider
//     metadata), recover it from a well-formed vault:<provider>/<name> credential
//     label; fall back to claude only for a genuinely blank label; fail closed on
//     a present-but-unrecognized label (issue #50).
//  3. If the metadata is missing or corrupt, infer the provider from the
//     on-disk layout (which is the ground truth for what is actually isolated).
//     Only an unambiguous match is accepted.
//  4. Otherwise fail closed with an actionable error.
//
// This is the single shared resolution path used by shallow-spawn and
// CredentialPath (and any future shallow-profile doctor), so all commands agree
// on a profile's provider.
func (m *Manager) ResolveProvider(name string) (string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return "", err
	}
	provider, err := m.resolveProviderForHome(home, name)
	if err != nil {
		return "", err
	}
	// The resolved provider must map to a known layout; refuse otherwise rather
	// than falling through to a permissive (no-pin) default at spawn time.
	if _, err := layoutFor(provider); err != nil {
		return "", fmt.Errorf("shallow profile %q records an unknown provider %q; refusing to proceed — recreate it with `caam shallow-profile create %s --force --tool <provider> ...`", name, provider, name)
	}
	return provider, nil
}

// providerFromLegacyLabel recovers a shallow provider from a legacy credential
// label of the STRICT form "vault:<supported-provider>/<profile-name>" (issue
// #50). It returns ok=false for anything else — a blank/missing label, a
// non-vault scheme (file:..., an email address, a filesystem path, a child
// command name), an unsupported/unknown provider, a whitespace-only provider
// token, or a missing profile-name component — so callers fail closed instead
// of inferring an identity from an arbitrary string.
func providerFromLegacyLabel(label string) (string, bool) {
	label = strings.TrimSpace(label)
	const scheme = "vault:"
	if !strings.HasPrefix(label, scheme) {
		return "", false
	}
	rest := label[len(scheme):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash >= len(rest)-1 {
		// Need a non-empty provider token AND a non-empty profile-name component.
		return "", false
	}
	rawProv := strings.TrimSpace(rest[:slash])
	if rawProv == "" {
		return "", false
	}
	if strings.TrimSpace(rest[slash+1:]) == "" {
		return "", false
	}
	prov := NormalizeProvider(rawProv)
	// Only KNOWN shallow providers are inferable; never accept an unsupported one.
	if _, err := layoutFor(prov); err != nil {
		return "", false
	}
	return prov, true
}

func (m *Manager) resolveProviderForHome(home, name string) (string, error) {
	meta, err := readMeta(home)
	if err == nil {
		if raw := strings.TrimSpace(meta.Provider); raw != "" {
			return NormalizeProvider(raw), nil
		}
		// Explicit provider is blank: legacy pre-provider metadata. Recover it
		// from a well-formed vault:<provider>/<name> credential label when present
		// (issue #50). A genuinely blank label is the original pre-provider case
		// and defaults to claude (the only layout that existed then). A
		// present-but-unrecognized label fails closed rather than guessing an
		// identity from an arbitrary string.
		if prov, ok := providerFromLegacyLabel(meta.CredentialFrom); ok {
			return prov, nil
		}
		if strings.TrimSpace(meta.CredentialFrom) == "" {
			return "claude", nil
		}
		return "", fmt.Errorf("shallow profile %q has a blank provider and an unrecognized credential label %q (not a vault:<provider>/<name> form for a supported provider: %s); refusing to guess the identity — recreate it with `caam shallow-profile create %s --force --tool <provider> ...`", name, meta.CredentialFrom, strings.Join(SupportedProviders(), ", "), name)
	}
	// Metadata unreadable (missing or corrupt). Try to recover the provider from
	// the on-disk layout, but only accept an unambiguous answer.
	if inferred, ierr := m.inferProviderFromDisk(home); ierr == nil {
		return inferred, nil
	}
	reason := "has no metadata"
	if !errors.Is(err, os.ErrNotExist) {
		reason = fmt.Sprintf("has unreadable/corrupt metadata (%v)", err)
	}
	return "", fmt.Errorf("shallow profile %q %s and its provider cannot be determined from disk; refusing to spawn to avoid leaking the wrong identity — recreate it with `caam shallow-profile create %s --force --tool <provider> ...`", name, reason, name)
}

// inferProviderFromDisk determines a profile's provider by inspecting which
// provider's PRIVATE credential dir/file is real (a non-symlink) inside the
// shallow HOME. Every other provider's dir is either absent or a passthrough
// symlink, so exactly the isolated provider matches. Returns an error unless
// exactly one provider matches.
func (m *Manager) inferProviderFromDisk(home string) (string, error) {
	var matches []string
	for _, p := range SupportedProviders() {
		layout, err := layoutFor(p)
		if err != nil {
			continue
		}
		if layoutIsRealOnDisk(home, layout) {
			matches = append(matches, p)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", fmt.Errorf("provider is ambiguous on disk (matched %v)", matches)
}

// layoutIsRealOnDisk reports whether a provider's private credential file and
// each of its real dirs exist as REAL (non-symlink) entries in the shallow
// HOME — the fingerprint of a profile that isolates exactly this provider. A
// passthrough symlink to another provider's real dir fails the realDirs check,
// so it cannot masquerade as an isolated identity.
func layoutIsRealOnDisk(home string, layout *providerLayout) bool {
	// Each of the provider's real dirs must be a real (non-symlink) directory.
	for _, d := range layout.realDirs {
		st, err := os.Lstat(filepath.Join(home, filepath.FromSlash(d)))
		if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return false
		}
	}
	// The primary credential must be a real (non-symlink) regular file.
	st, err := os.Lstat(filepath.Join(home, filepath.FromSlash(layout.primaryCredRel)))
	if err != nil || st.Mode()&os.ModeSymlink != 0 || st.IsDir() {
		return false
	}
	return true
}

// assertSafeProfileHome verifies that home is a legitimate location for a
// shallow profile: strictly inside the shallow base dir, and neither the real
// HOME, an ancestor of it, nor the filesystem root. It is the single guard that
// stands between a misconfigured --base and catastrophic data loss (issue #38).
func (m *Manager) assertSafeProfileHome(home string) error {
	abs, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("resolve profile path %q: %w", home, err)
	}
	abs = filepath.Clean(abs)
	if isFilesystemRoot(abs) {
		return fmt.Errorf("refusing to use the filesystem root %q as a shallow profile home", abs)
	}
	if m.wouldTouchRealHome(abs) {
		return fmt.Errorf("refusing to use %q as a shallow profile home: it is, or contains, the real HOME (%s) — check your --base / $CAAM_SHALLOW_HOMES_DIR", abs, m.realHome)
	}
	if !m.isStrictlyInsideBase(abs) {
		return fmt.Errorf("refusing to use %q as a shallow profile home: it is not inside the shallow base dir (%s)", abs, m.baseDir)
	}
	return nil
}

// safeRemoveAll is os.RemoveAll with the same guard as assertSafeProfileHome:
// it refuses to remove the real HOME, an ancestor of it, the filesystem root,
// or anything outside the shallow base dir. Shared by Delete and Create's
// force-overwrite / staging paths so the protection cannot drift apart.
func (m *Manager) safeRemoveAll(target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", target, err)
	}
	abs = filepath.Clean(abs)
	if isFilesystemRoot(abs) {
		return fmt.Errorf("refusing to remove filesystem root %q", abs)
	}
	if m.wouldTouchRealHome(abs) {
		return fmt.Errorf("refusing to remove %q: it is, or contains, the real HOME (%s)", abs, m.realHome)
	}
	if !m.isStrictlyInsideBase(abs) {
		return fmt.Errorf("refusing to remove %q: it is outside the shallow base dir (%s)", abs, m.baseDir)
	}
	return os.RemoveAll(target)
}

// wouldTouchRealHome reports whether removing/replacing target could affect the
// user's real HOME — i.e. target IS the real HOME or an ANCESTOR of it. It
// compares both lexically and with symlinks resolved, so an aliased path (e.g.
// a symlinked /tmp or macOS /var -> /private/var) cannot slip past.
func (m *Manager) wouldTouchRealHome(target string) bool {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return true // fail safe: if we cannot reason about it, treat as dangerous
	}
	absReal, err := filepath.Abs(m.realHome)
	if err != nil {
		return true
	}
	if isEqualOrAncestor(filepath.Clean(absTarget), filepath.Clean(absReal)) {
		return true
	}
	return isEqualOrAncestor(canonical(absTarget), canonical(absReal))
}

// isStrictlyInsideBase reports whether abs is a proper descendant of the shallow
// base dir (never the base dir itself). Profile homes are always baseDir/<name>,
// so this holds by construction for legitimate callers and rejects anything else.
func (m *Manager) isStrictlyInsideBase(abs string) bool {
	absBase, err := filepath.Abs(m.baseDir)
	if err != nil {
		return false
	}
	return isStrictlyInside(filepath.Clean(absBase), abs)
}

// assertLooksLikeShallowProfile refuses a --force overwrite of a directory that
// does not carry the shallow metadata sidecar, so a --base that collides with an
// unrelated real directory is not silently wiped. A present-but-corrupt sidecar
// still counts (force is a legitimate way to repair a broken profile).
func assertLooksLikeShallowProfile(home string) error {
	metaPath := filepath.Join(home, ProfileMetaFilename)
	if _, err := os.Lstat(metaPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("refusing to overwrite %q with --force: it does not look like a caam shallow profile (no %s). Remove it by hand if you really mean to replace it", home, ProfileMetaFilename)
		}
		return fmt.Errorf("inspect existing profile %q: %w", home, err)
	}
	return nil
}

// isFilesystemRoot reports whether p is a filesystem root ("/" on Unix, or a
// volume root such as "C:\\" on Windows).
func isFilesystemRoot(p string) bool {
	return p == string(os.PathSeparator) || p == filepath.VolumeName(p)+string(os.PathSeparator)
}

// isEqualOrAncestor reports whether path is equal to, or a descendant of,
// ancestor (i.e. ancestor "contains" path).
func isEqualOrAncestor(ancestor, path string) bool {
	rel, err := filepath.Rel(ancestor, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// isStrictlyInside reports whether child is a proper descendant of parent
// (never equal to parent).
func isStrictlyInside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	if strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// canonical resolves symlinks in p when possible, falling back to a cleaned
// absolute path when p does not exist (or cannot be resolved).
func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
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
	// NOTE: the raw Provider is returned verbatim (a blank field is NOT forced to
	// "claude" here). Defaulting/inference for legacy blank-provider metadata is
	// centralized in resolveProviderForHome so it can consult the credential
	// label before falling back (issue #50).
	return &m, nil
}
