// Package keepalive maintains native OAuth grants in the homes that own them.
// A saved vault credential is never an executable grant.
package keepalive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	claudeprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	grokprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/grok"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
)

// AccountIdentity comes from the live credential or its own Claude account
// state, never from a profile name or vault metadata.
type AccountIdentity struct {
	AccountID string `json:"account_id,omitempty"`
	Email     string `json:"email,omitempty"`
}

// SameAccount uses a known account ID as authority. Email is a fallback only
// when at least one side lacks an ID. Unknown identities never compare equal.
func SameAccount(a, b AccountIdentity) bool {
	if a.AccountID != "" && b.AccountID != "" {
		return a.AccountID == b.AccountID
	}
	return a.Email != "" && b.Email != "" && strings.EqualFold(a.Email, b.Email)
}

// Grant identifies one native live credential owner. A grant with a
// BlockedReason is diagnostic only and must never launch a native command.
type Grant struct {
	Provider              string            `json:"provider"`
	Name                  string            `json:"name,omitempty"`
	Kind                  string            `json:"kind"`
	Home                  string            `json:"home"`
	AuthPath              string            `json:"auth_path,omitempty"`
	IdentityPath          string            `json:"identity_path,omitempty"`
	Env                   map[string]string `json:"-"`
	Scrub                 []string          `json:"-"`
	Identity              AccountIdentity   `json:"identity"`
	ExpiresAt             time.Time         `json:"expires_at,omitempty"`
	CredentialFingerprint string            `json:"-"`
	RefreshFingerprint    string            `json:"-"`
	BlockedReason         string            `json:"blocked_reason,omitempty"`
	Owner                 string            `json:"owner,omitempty"`
	source                *grantSource
}

// Ref is the unambiguous selector used by keepalive's human and JSON output.
func (g Grant) Ref() string {
	ref := g.Kind + ":" + g.Provider
	if g.Kind != "host" {
		ref += "/" + g.Name
	}
	return ref
}

type grantSource struct {
	provider, name, kind, home, authPath, identityPath string
	canonicalAuth, canonicalIdentity                   string
	blockedReason, owner                               string
	identity                                           AccountIdentity
	env                                                map[string]string
	scrub                                              []string
	vaultPaths                                         []string
	discovery                                          *DiscoverOptions
}

// CredentialSnapshot is a read of one exact native file. Secret material is
// private to this package so results and diagnostics cannot serialize it.
type CredentialSnapshot struct {
	Identity            AccountIdentity `json:"identity"`
	ExpiresAt           time.Time       `json:"expires_at,omitempty"`
	HasRefreshToken     bool            `json:"has_refresh_token"`
	Fingerprint         string          `json:"-"`
	RefreshFingerprint  string          `json:"-"`
	IdentityFingerprint string          `json:"-"`
	data                []byte
	identityData        []byte
}

// DiscoverOptions locates CAAM's existing live homes. Empty paths use the same
// defaults as shallow-spawn and isolated profiles. Provider may be empty,
// "claude", or "grok". No directories, mirrors, or symlinks are created.
type DiscoverOptions struct {
	Home         string
	ShallowBase  string
	ProfilesPath string
	VaultPath    string
	Provider     string
}

// Discover returns eligible and blocked live homes, with duplicate ownership
// resolved before the caller applies any name/ref filters.
func Discover(ctx context.Context, opts DiscoverOptions) ([]Grant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Provider != "" && opts.Provider != "claude" && opts.Provider != "grok" {
		return nil, fmt.Errorf("keepalive supports claude and grok live homes")
	}
	home := opts.Home
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve live HOME: %w", err)
		}
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve live HOME: %w", err)
	}
	if opts.ProfilesPath == "" {
		opts.ProfilesPath = profile.DefaultStorePath()
	}
	opts.Home = home
	vaultPaths := []string{authfile.DefaultVaultPath()}
	if opts.VaultPath != "" {
		vaultPaths = append(vaultPaths, opts.VaultPath)
	}
	var grants []Grant
	if opts.Provider == "" || opts.Provider == "claude" {
		cfg := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
		authPath := filepath.Join(home, ".claude", ".credentials.json")
		identityPath := filepath.Join(home, ".claude.json")
		env := map[string]string{"HOME": home}
		blockedReason := ""
		if cfg != "" {
			cfg, err = filepath.Abs(cfg)
			if err != nil {
				return nil, fmt.Errorf("resolve CLAUDE_CONFIG_DIR: %w", err)
			}
			env["CLAUDE_CONFIG_DIR"] = cfg
			authPath = filepath.Join(cfg, ".credentials.json")
			identityPath = filepath.Join(cfg, ".claude.json")
		} else {
			xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
			if xdg == "" {
				xdg = filepath.Join(home, ".config")
			}
			xdg, err = filepath.Abs(xdg)
			if err != nil {
				return nil, fmt.Errorf("resolve XDG_CONFIG_HOME: %w", err)
			}
			xdgConfig := filepath.Join(xdg, "claude-code")
			xdgAuth := filepath.Join(xdgConfig, ".credentials.json")
			_, legacyErr := os.Lstat(authPath)
			_, xdgErr := os.Lstat(xdgAuth)
			if !errors.Is(xdgErr, os.ErrNotExist) {
				if !errors.Is(legacyErr, os.ErrNotExist) && !samePhysicalFile(authPath, xdgAuth) {
					blockedReason = "both legacy and XDG Claude credential files exist; set CLAUDE_CONFIG_DIR to choose the live owner before keepalive"
				} else {
					// Pin the discovered XDG source even if the caller's
					// environment changes before the native command starts.
					env["XDG_CONFIG_HOME"], env["CLAUDE_CONFIG_DIR"] = xdg, xdgConfig
					authPath, identityPath = xdgAuth, filepath.Join(xdgConfig, ".claude.json")
				}
			}
		}
		if _, statErr := os.Lstat(authPath); !errors.Is(statErr, os.ErrNotExist) || cfg != "" || hasClaudeKeychain(home) {
			grants = append(grants, inspectGrant(Grant{Provider: "claude", Kind: "host", Home: home,
				AuthPath: authPath, IdentityPath: identityPath, Env: env, Scrub: []string{"CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME"},
				BlockedReason: blockedReason}, vaultPaths))
		}
		mgr, err := shallow.NewManager(opts.ShallowBase, home)
		if err != nil {
			return nil, err
		}
		opts.ShallowBase = mgr.BaseDir()
		profiles, err := mgr.List()
		if err != nil {
			return nil, err
		}
		for _, prof := range profiles {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if authfile.IsSystemProfile(prof.Name) {
				continue
			}
			providerID, err := mgr.ResolveProvider(prof.Name)
			if err != nil {
				grants = append(grants, Grant{Provider: "unknown", Kind: "shallow", Name: prof.Name,
					Home: prof.Path, BlockedReason: "cannot determine the shallow profile's native provider"})
				continue
			}
			if providerID != "claude" {
				continue
			}
			authPath, err := mgr.CredentialPath(prof.Name)
			if err != nil {
				return nil, err
			}
			env, scrub := shallow.SpawnEnv(providerID, prof.Path, prof.Name, false, false)
			// Shallow Claude owns the legacy HOME source. An ambient XDG
			// override must not redirect this bounded native invocation.
			scrub = append(scrub, "XDG_CONFIG_HOME")
			grants = append(grants, inspectGrant(Grant{Provider: "claude", Name: prof.Name, Kind: "shallow",
				Home: prof.Path, AuthPath: authPath, IdentityPath: filepath.Join(prof.Path, ".claude.json"),
				Env: env, Scrub: scrub}, vaultPaths))
		}
	}
	if opts.Provider == "" || opts.Provider == "grok" {
		grokHome := strings.TrimSpace(os.Getenv("GROK_HOME"))
		if grokHome == "" {
			grokHome = filepath.Join(home, ".grok")
		}
		grokHome, err = filepath.Abs(grokHome)
		if err != nil {
			return nil, fmt.Errorf("resolve GROK_HOME: %w", err)
		}
		authPath := filepath.Join(grokHome, "auth.json")
		if _, statErr := os.Lstat(authPath); !errors.Is(statErr, os.ErrNotExist) || strings.TrimSpace(os.Getenv("GROK_HOME")) != "" {
			grants = append(grants, inspectGrant(Grant{Provider: "grok", Kind: "host", Home: home,
				AuthPath: authPath, Env: map[string]string{"HOME": home, "GROK_HOME": grokHome}}, vaultPaths))
		}
	}
	for _, providerID := range []string{"claude", "grok"} {
		if opts.Provider != "" && opts.Provider != providerID {
			continue
		}
		isolated, err := discoverIsolated(ctx, opts.ProfilesPath, providerID, vaultPaths)
		if err != nil {
			return nil, err
		}
		grants = append(grants, isolated...)
	}
	resolveOwners(grants)
	for i := range grants {
		freezeGrant(&grants[i])
		if grants[i].source != nil {
			bound := opts
			grants[i].source.discovery = &bound
		}
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Ref() < grants[j].Ref() })
	return grants, nil
}

func discoverIsolated(ctx context.Context, basePath, providerID string, vaultPaths []string) ([]Grant, error) {
	entries, err := os.ReadDir(filepath.Join(basePath, providerID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list isolated %s homes: %w", providerID, err)
	}
	var grants []Grant
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || authfile.IsSystemProfile(entry.Name()) {
			continue
		}
		base := filepath.Join(basePath, providerID, entry.Name())
		base, err = filepath.Abs(base)
		if err != nil {
			return nil, err
		}
		g := Grant{Provider: providerID, Name: entry.Name(), Kind: "isolated", Home: filepath.Join(base, "home")}
		data, err := readRegular(filepath.Join(base, "profile.json"))
		var prof profile.Profile
		if err != nil || json.Unmarshal(data, &prof) != nil || prof.Name != entry.Name() || prof.Provider != providerID ||
			(prof.BasePath != "" && canonicalPath(prof.BasePath) != canonicalPath(base)) {
			g.BlockedReason = "isolated profile metadata is missing, malformed, or points outside its own home"
			grants = append(grants, g)
			continue
		}
		prof.BasePath = base
		if prof.AuthMode != "" && prof.AuthMode != "oauth" {
			g.BlockedReason = "keepalive requires a native OAuth login"
			grants = append(grants, g)
			continue
		}
		switch providerID {
		case "claude":
			g.Env, err = claudeprovider.New().Env(ctx, &prof)
			g.AuthPath = filepath.Join(g.Env["CLAUDE_CONFIG_DIR"], ".credentials.json")
			g.IdentityPath = filepath.Join(g.Env["CLAUDE_CONFIG_DIR"], ".claude.json")
			legacy := filepath.Join(prof.HomePath(), ".claude", ".credentials.json")
			if _, statErr := os.Lstat(g.AuthPath); errors.Is(statErr, os.ErrNotExist) {
				// Legacy credentials must run with legacy path semantics, not
				// a pinned, empty XDG directory that could create another grant.
				g.AuthPath = legacy
				g.IdentityPath = filepath.Join(prof.HomePath(), ".claude.json")
				delete(g.Env, "CLAUDE_CONFIG_DIR")
				delete(g.Env, "XDG_CONFIG_HOME")
				g.Scrub = append(g.Scrub, "CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME")
			} else if legacyInfo, legacyErr := os.Stat(legacy); legacyErr == nil {
				if currentInfo, currentErr := os.Stat(g.AuthPath); currentErr == nil && !os.SameFile(legacyInfo, currentInfo) {
					g.BlockedReason = "both legacy and configured Claude credential files exist; choose one live owner before keepalive"
				}
			}
		case "grok":
			g.Env, err = grokprovider.New().Env(ctx, &prof)
			g.AuthPath = filepath.Join(g.Env["GROK_HOME"], "auth.json")
		}
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", g.Ref(), err)
		}
		grants = append(grants, inspectGrant(g, vaultPaths))
	}
	return grants, nil
}

func inspectGrant(g Grant, vaultPaths []string) Grant {
	g.source = &grantSource{provider: g.Provider, name: g.Name, kind: g.Kind, home: g.Home,
		authPath: g.AuthPath, identityPath: g.IdentityPath, canonicalAuth: canonicalPath(g.AuthPath),
		canonicalIdentity: canonicalPath(g.IdentityPath), blockedReason: g.BlockedReason, identity: g.Identity,
		env: maps.Clone(g.Env), scrub: slices.Clone(g.Scrub), vaultPaths: slices.Clone(vaultPaths)}
	if g.Provider == "claude" && hasClaudeKeychain(g.Home) {
		g.BlockedReason = "Claude uses this home's login keychain; use a file-backed shallow or isolated home so keepalive can verify its live grant"
		freezeGrant(&g)
		return g
	}
	snapshot, err := ReadCredential(g)
	// A malformed expiry or refresh field does not erase a verified account.
	// Its unusable copy must still participate in duplicate-owner checks.
	g.Identity, g.ExpiresAt = snapshot.Identity, snapshot.ExpiresAt
	g.CredentialFingerprint, g.RefreshFingerprint = snapshot.Fingerprint, snapshot.RefreshFingerprint
	if err != nil {
		if g.BlockedReason == "" {
			g.BlockedReason = err.Error()
		}
		freezeGrant(&g)
		return g
	}
	if g.Identity.AccountID == "" && g.Identity.Email == "" && g.BlockedReason == "" {
		g.BlockedReason = "live account identity is unavailable; sign in inside this live home before keepalive"
	}
	freezeGrant(&g)
	return g
}

func hasClaudeKeychain(home string) bool {
	return hasClaudeKeychainForOS(home, runtime.GOOS)
}

func hasClaudeKeychainForOS(home, goos string) bool {
	if goos != "darwin" {
		return false
	}
	// CAAM_KEYCHAIN=0 disables CAAM's bridge, not Claude's native keychain
	// lookup. Never trust a file mirror when this home's keychain may own it.
	for _, name := range []string{"login.keychain-db", "login.keychain"} {
		if _, err := os.Stat(filepath.Join(home, "Library", "Keychains", name)); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func freezeGrant(g *Grant) {
	if g.source != nil {
		g.source.blockedReason, g.source.owner, g.source.identity = g.BlockedReason, g.Owner, g.Identity
	}
}

// recheckGrantOwnership runs under the per-grant keepalive lock immediately
// before reading or invoking its native CLI. Earlier commands may have taken
// minutes, and a newly created or switched home can now share this account.
func recheckGrantOwnership(ctx context.Context, grant Grant) error {
	if err := validateGrantSource(grant); err != nil {
		return err
	}
	if grant.source.discovery == nil {
		// Package-internal fixtures can bind a single exact native source;
		// every grant returned by Discover records its full discovery scope.
		return nil
	}
	grants, err := Discover(ctx, *grant.source.discovery)
	if err != nil {
		return fmt.Errorf("cannot revalidate all live grant owners")
	}
	for _, current := range grants {
		if current.Ref() != grant.Ref() {
			continue
		}
		if current.BlockedReason != "" || current.Owner != "" {
			return fmt.Errorf("live account ownership is no longer unambiguous; run caam keepalive --dry-run to inspect owners")
		}
		if current.Home != grant.Home || current.AuthPath != grant.AuthPath || current.IdentityPath != grant.IdentityPath ||
			!maps.Equal(current.Env, grant.Env) || !slices.Equal(current.Scrub, grant.Scrub) || !SameAccount(current.Identity, grant.Identity) {
			return fmt.Errorf("live credential source or account changed since discovery")
		}
		return nil
	}
	return fmt.Errorf("live grant no longer appears in discovery")
}

// ReadCredential revalidates the discovery binding and rereads the exact live
// source. Caller-created or modified Grants cannot turn a vault copy into a
// native live owner. Native atomic replacement of the same path is allowed.
func ReadCredential(g Grant) (CredentialSnapshot, error) {
	if err := validateGrantSource(g); err != nil {
		return CredentialSnapshot{}, err
	}
	snapshot, err := readCredentialFiles(g.Provider, g.AuthPath, g.IdentityPath)
	if err := validateGrantSource(g); err != nil {
		return CredentialSnapshot{}, err
	}
	return snapshot, err
}

func validateGrantSource(g Grant) error {
	s := g.source
	if s == nil || g.Provider != s.provider || g.Name != s.name || g.Kind != s.kind || g.Home != s.home ||
		g.AuthPath != s.authPath || g.IdentityPath != s.identityPath || g.BlockedReason != s.blockedReason ||
		g.Owner != s.owner || g.Identity != s.identity || !maps.Equal(g.Env, s.env) || !slices.Equal(g.Scrub, s.scrub) {
		return fmt.Errorf("credential source is not an unchanged discovered live home")
	}
	if canonicalPath(g.AuthPath) != s.canonicalAuth || canonicalPath(g.IdentityPath) != s.canonicalIdentity {
		return fmt.Errorf("live credential source changed its filesystem target")
	}
	if g.Provider == "claude" && hasClaudeKeychain(g.Home) {
		return fmt.Errorf("Claude login keychain owns this home; use a file-backed shallow or isolated home")
	}
	for _, root := range s.vaultPaths {
		if pathWithin(root, g.AuthPath) || pathWithin(root, g.Home) || (g.IdentityPath != "" && pathWithin(root, g.IdentityPath)) {
			return fmt.Errorf("vault snapshots cannot be used as live keepalive credentials")
		}
		alias, err := aliasesVaultFile(root, g.Provider, g.AuthPath)
		if err != nil {
			return err
		}
		if alias {
			return fmt.Errorf("vault snapshots cannot be used as live keepalive credentials")
		}
	}
	info, err := os.Lstat(g.AuthPath)
	if err != nil {
		return fmt.Errorf("inspect native credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("native credential must be a regular file, not a file symlink or special file")
	}
	links, err := nativeFileLinkCount(g.AuthPath, info)
	if err != nil {
		return fmt.Errorf("cannot verify native credential has one file owner: %w", err)
	}
	if links != 1 {
		return fmt.Errorf("native credential has multiple hard links; native atomic rotation would fork its refresh-token family")
	}
	return nil
}

// Hard links have no path-level indication that their inode is a vault copy.
// Compare metadata only; discovery never opens a vault credential as input.
func aliasesVaultFile(root, providerID, authPath string) (bool, error) {
	info, err := os.Stat(authPath)
	if err != nil {
		// ReadCredential preserves the missing/unreadable source error later.
		return false, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, providerID))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot verify live source is independent of vault snapshots")
	}
	filename := "auth.json"
	if providerID == "claude" {
		filename = ".credentials.json"
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		saved, err := os.Stat(filepath.Join(root, providerID, entry.Name(), filename))
		if err == nil && os.SameFile(info, saved) {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("cannot verify live source is independent of vault snapshots")
		}
	}
	return false, nil
}

// readCredentialFiles also serves vault synchronization. It parses data only;
// it does not create a runnable Grant or confer ownership on a snapshot.
func readCredentialFiles(providerID, authPath, identityPath string) (CredentialSnapshot, error) {
	data, err := readRegular(authPath)
	if err != nil {
		return CredentialSnapshot{}, fmt.Errorf("read %s credential: %w", providerID, err)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return CredentialSnapshot{}, fmt.Errorf("%s credential must be a JSON object", providerID)
	}
	snapshot := CredentialSnapshot{data: data, Fingerprint: fingerprint(data)}
	var refresh string
	switch providerID {
	case "claude":
		var oauth map[string]json.RawMessage
		if json.Unmarshal(root["claudeAiOauth"], &oauth) != nil || oauth == nil {
			return CredentialSnapshot{}, fmt.Errorf("Claude credential has no OAuth grant")
		}
		snapshot.Identity, err = identityFields(oauth, "accountId", "email")
		if err != nil {
			return CredentialSnapshot{}, err
		}
		if identityPath != "" {
			identityData, readErr := readRegular(identityPath)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return CredentialSnapshot{}, fmt.Errorf("read paired Claude account state: %w", readErr)
			}
			if readErr == nil {
				var state struct {
					OAuthAccount map[string]json.RawMessage `json:"oauthAccount"`
				}
				if json.Unmarshal(identityData, &state) != nil {
					return CredentialSnapshot{}, fmt.Errorf("paired Claude account state is malformed")
				}
				paired, err := identityFields(state.OAuthAccount, "accountUuid", "emailAddress")
				if err != nil {
					return CredentialSnapshot{}, err
				}
				if knownIdentity(snapshot.Identity) && knownIdentity(paired) && !SameAccount(snapshot.Identity, paired) {
					return CredentialSnapshot{}, fmt.Errorf("Claude credential and paired account state disagree")
				}
				if snapshot.Identity.AccountID == "" {
					snapshot.Identity.AccountID = paired.AccountID
				}
				if snapshot.Identity.Email == "" {
					snapshot.Identity.Email = paired.Email
				}
				snapshot.identityData = identityData
				snapshot.IdentityFingerprint = fingerprint(identityData)
			}
		}
		access, err := credentialString(oauth, "accessToken")
		if err != nil || access == "" {
			return snapshot, fmt.Errorf("Claude access token is missing or malformed")
		}
		refresh, err = credentialString(oauth, "refreshToken")
		if err != nil {
			return snapshot, err
		}
		snapshot.HasRefreshToken = refresh != ""
		if refresh != "" {
			snapshot.RefreshFingerprint = fingerprint([]byte(refresh))
		}
		snapshot.ExpiresAt, err = credentialExpiry(oauth, "expiresAt")
		if err != nil {
			return snapshot, err
		}
	case "grok":
		var entries []map[string]json.RawMessage
		if hasGrokCredential(root) {
			entries = append(entries, root)
		}
		for _, raw := range root {
			var entry map[string]json.RawMessage
			if json.Unmarshal(raw, &entry) == nil && hasGrokCredential(entry) {
				entries = append(entries, entry)
			}
		}
		if len(entries) != 1 {
			return CredentialSnapshot{}, fmt.Errorf("Grok credential must contain exactly one unambiguous OAuth grant")
		}
		entry := entries[0]
		snapshot.Identity, err = identityFields(entry, "user_id", "email")
		if err != nil {
			return CredentialSnapshot{}, err
		}
		access, err := credentialString(entry, "key", "access_token", "accessToken", "token")
		if err != nil || access == "" {
			return snapshot, fmt.Errorf("Grok access token is missing or malformed")
		}
		refresh, err = credentialString(entry, "refresh_token", "refreshToken")
		if err != nil {
			return snapshot, err
		}
		snapshot.HasRefreshToken = refresh != ""
		if refresh != "" {
			snapshot.RefreshFingerprint = fingerprint([]byte(refresh))
		}
		snapshot.ExpiresAt, err = credentialExpiry(entry, "expires_at", "expiresAt", "expiry")
		if err != nil {
			return snapshot, err
		}
	default:
		return CredentialSnapshot{}, fmt.Errorf("unsupported keepalive provider")
	}
	return snapshot, nil
}

func credentialString(fields map[string]json.RawMessage, names ...string) (string, error) {
	var selected string
	for _, name := range names {
		raw, exists := fields[name]
		if !exists {
			continue
		}
		var value string
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
			return "", fmt.Errorf("credential field %s must be a string", name)
		}
		value = strings.TrimSpace(value)
		if selected != "" && value != "" && value != selected {
			return "", fmt.Errorf("credential contains conflicting %s fields", names[0])
		}
		if value != "" {
			selected = value
		}
	}
	return selected, nil
}

func credentialExpiry(fields map[string]json.RawMessage, names ...string) (time.Time, error) {
	var selected time.Time
	for _, name := range names {
		raw, exists := fields[name]
		if !exists {
			continue
		}
		value := string(raw)
		if strings.HasPrefix(value, `"`) {
			if json.Unmarshal(raw, &value) != nil {
				return time.Time{}, fmt.Errorf("credential expiry is malformed")
			}
		}
		expiry, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			epoch, numErr := strconv.ParseInt(value, 10, 64)
			if numErr != nil || epoch <= 0 {
				return time.Time{}, fmt.Errorf("credential expiry must be an absolute positive timestamp")
			}
			if epoch >= 1_000_000_000_000 {
				expiry = time.UnixMilli(epoch)
			} else {
				expiry = time.Unix(epoch, 0)
			}
		}
		if expiry.Year() < 1970 || expiry.Year() > 9999 {
			return time.Time{}, fmt.Errorf("credential expiry is out of range")
		}
		if !selected.IsZero() && !selected.Equal(expiry) {
			return time.Time{}, fmt.Errorf("credential contains conflicting expiry fields")
		}
		selected = expiry.UTC()
	}
	return selected, nil
}

func identityFields(fields map[string]json.RawMessage, idKey, emailKey string) (AccountIdentity, error) {
	id, err := credentialString(fields, idKey)
	if err != nil {
		return AccountIdentity{}, fmt.Errorf("account identity is malformed")
	}
	email, err := credentialString(fields, emailKey)
	if err != nil || strings.ContainsAny(id+email, "\r\n\x00") {
		return AccountIdentity{}, fmt.Errorf("account identity is malformed")
	}
	if email != "" && (!strings.Contains(email, "@") || len(email) > 200) {
		return AccountIdentity{}, fmt.Errorf("account email is malformed")
	}
	return AccountIdentity{AccountID: id, Email: email}, nil
}

func hasGrokCredential(fields map[string]json.RawMessage) bool {
	for _, name := range []string{"key", "access_token", "accessToken", "refresh_token", "refreshToken", "token"} {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
}

func knownIdentity(id AccountIdentity) bool { return id.AccountID != "" || id.Email != "" }

func readRegular(path string) ([]byte, error) {
	// Inspect before opening: opening a FIFO can block indefinitely before
	// the bounded read below gets a chance to reject its file mode.
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, fmt.Errorf("credential source must be a regular file smaller than 8 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, fmt.Errorf("credential source must be a regular file smaller than 8 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, fmt.Errorf("credential source is larger than 8 MiB")
	}
	return data, nil
}

func fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	// Resolve existing parents even when a missing file is later created.
	parent := filepath.Dir(path)
	if parent != path {
		return filepath.Join(canonicalPath(parent), filepath.Base(path))
	}
	return filepath.Clean(path)
}

func pathWithin(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(canonicalPath(root), canonicalPath(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePhysicalFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if canonicalPath(a) == canonicalPath(b) {
		return true
	}
	ai, ae := os.Stat(a)
	bi, be := os.Stat(b)
	return ae == nil && be == nil && os.SameFile(ai, bi)
}

func resolveOwners(grants []Grant) {
	// An unidentified credential could be a rotated copy of any known
	// account. Distinct token strings cannot establish independent families.
	for _, providerID := range []string{"claude", "grok"} {
		for i, candidate := range grants {
			if candidate.Provider != providerID || candidate.CredentialFingerprint == "" || knownIdentity(candidate.Identity) {
				continue
			}
			for j := range grants {
				if i != j && grants[j].Provider == providerID && grants[j].CredentialFingerprint != "" &&
					!samePhysicalFile(candidate.AuthPath, grants[j].AuthPath) {
					grants[j].BlockedReason = "cannot establish independent account ownership while " + candidate.Ref() + " has no live identity"
				}
			}
		}
	}
	// Resolve connected components rather than pairs: an email-only record
	// bridging two different known IDs is ambiguous, not permission to choose.
	seen := make([]bool, len(grants))
	for start := range grants {
		if seen[start] || grants[start].AuthPath == "" {
			continue
		}
		group := []int{start}
		seen[start] = true
		for pos := 0; pos < len(group); pos++ {
			a := grants[group[pos]]
			for j, b := range grants {
				if seen[j] || a.Provider != b.Provider || b.AuthPath == "" {
					continue
				}
				if samePhysicalFile(a.AuthPath, b.AuthPath) || SameAccount(a.Identity, b.Identity) ||
					(a.RefreshFingerprint != "" && a.RefreshFingerprint == b.RefreshFingerprint) {
					seen[j] = true
					group = append(group, j)
				}
			}
		}
		if len(group) < 2 {
			continue
		}
		// Prefer the shallow reference when several names resolve to the same
		// physical file. It is one grant, not two independently renewable copies.
		sort.Slice(group, func(i, j int) bool {
			a, b := grants[group[i]], grants[group[j]]
			if (a.BlockedReason == "") != (b.BlockedReason == "") {
				return a.BlockedReason == ""
			}
			if a.Kind == "shallow" && b.Kind != "shallow" {
				return true
			}
			if b.Kind == "shallow" && a.Kind != "shallow" {
				return false
			}
			return a.Ref() < b.Ref()
		})
		owner := group[0]
		allSameFile := true
		for _, i := range group[1:] {
			// Directory aliases share the exact credential and its sibling
			// lock across native atomic replacement. File hard links do not.
			allSameFile = allSameFile && canonicalPath(grants[owner].AuthPath) == canonicalPath(grants[i].AuthPath)
			if knownIdentity(grants[owner].Identity) && knownIdentity(grants[i].Identity) && !SameAccount(grants[owner].Identity, grants[i].Identity) {
				allSameFile = false
			}
		}
		if allSameFile {
			for _, i := range group[1:] {
				if grants[i].BlockedReason != "" {
					continue
				}
				grants[i].BlockedReason = "another live reference owns this same physical credential file"
				grants[i].Owner = grants[owner].Ref()
			}
			continue
		}
		if len(group) == 2 && grants[owner].Provider == "claude" && grants[owner].Kind == "shallow" &&
			grants[group[1]].Kind == "host" && SameAccount(grants[owner].Identity, grants[group[1]].Identity) && grants[owner].BlockedReason == "" {
			host := group[1]
			grants[host].BlockedReason = "the shallow live home owns this Claude account; the host copy must not replay its grant"
			grants[host].Owner = grants[owner].Ref()
			continue
		}
		var refs []string
		for _, i := range group {
			refs = append(refs, grants[i].Ref())
		}
		sort.Strings(refs)
		for _, i := range group {
			grants[i].BlockedReason = "ambiguous live grant owners: " + strings.Join(refs, ", ") + "; sign in independently or keep only one owner"
		}
	}
}
