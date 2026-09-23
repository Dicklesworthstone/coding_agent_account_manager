// Package cursor implements the provider adapter for Cursor Agent.
//
// Authentication mechanics (current Cursor Agent, verified against the
// 2026.09.10 client):
//   - Live credentials are $XDG_CONFIG_HOME/cursor/auth.json (access and
//     refresh tokens). When XDG_CONFIG_HOME is unset that is ~/.config/cursor/.
//   - $XDG_CONFIG_HOME/cursor/cli-config.json holds authInfo and settings.
//     A non-empty authInfo object is account metadata, not proof the
//     credential is present or that this profile is the one the CLI will use.
//   - Older installs also kept files under ~/.cursor/. Those are still
//     discovered and restored.
//   - The CLI is `cursor-agent` (aliases: a cursor-agent binary named `agent`,
//     or `cursor agent` when that shim is what is installed). An unrelated
//     `agent` binary on PATH is not used.
//   - Setting HOME without XDG_CONFIG_HOME leaves the process on the
//     machine-global account whenever XDG_CONFIG_HOME is already set. Every
//     profile therefore gets its own HOME and its own XDG_CONFIG_HOME.
//
// Active validation runs `cursor-agent status --format json` inside that
// environment. File metadata alone never counts as a working login.
package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Provider implements the Cursor Agent adapter.
type Provider struct{}

// New creates a new Cursor provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "cursor"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Cursor"
}

// DefaultBin returns the preferred binary name. Resolution at execution time
// prefers an installed cursor-agent and falls back to supported aliases.
func (p *Provider) DefaultBin() string {
	return "cursor-agent"
}

// SupportedAuthModes returns the authentication modes supported by Cursor.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,
		provider.AuthModeAPIKey,
	}
}

// cursorConfigDir is the live Cursor Agent config directory.
func cursorConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cursor")
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config", "cursor")
}

// legacyCursorDir is the pre-XDG location.
func legacyCursorDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".cursor")
}

// profileXDGCursor is the Cursor config directory inside a profile's isolated
// XDG_CONFIG_HOME.
func profileXDGCursor(prof *profile.Profile) string {
	return filepath.Join(prof.XDGConfigPath(), "cursor")
}

// profileLegacyCursor is the legacy tree inside a profile's isolated HOME.
func profileLegacyCursor(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".cursor")
}

// AuthFiles returns the auth file specifications for the machine-global
// Cursor install (discovery and the vault backup set's live paths live in
// authfile.CursorAuthFiles; this list is the provider-facing view).
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	xdg := cursorConfigDir()
	legacy := legacyCursorDir()
	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(xdg, "auth.json"),
			Description: "Cursor Agent credentials (XDG config)",
			Required:    false,
		},
		{
			Path:        filepath.Join(xdg, "cli-config.json"),
			Description: "Cursor Agent config and account metadata (XDG config)",
			Required:    false,
		},
		{
			Path:        filepath.Join(legacy, "auth.json"),
			Description: "Cursor CLI auth credentials (legacy ~/.cursor)",
			Required:    false,
		},
		{
			Path:        filepath.Join(legacy, "cli-config.json"),
			Description: "Cursor CLI config (legacy ~/.cursor)",
			Required:    false,
		},
		{
			Path:        filepath.Join(legacy, "settings.json"),
			Description: "Cursor CLI settings (legacy ~/.cursor)",
			Required:    false,
		},
	}
}

// PrepareProfile creates the isolated HOME, XDG config root, and both Cursor
// directories. Existing files are left in place.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	if err := prof.EnsureLayout(); err != nil {
		return err
	}
	for _, dir := range []string{profileXDGCursor(prof), profileLegacyCursor(prof)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create cursor dir: %w", err)
		}
	}
	return nil
}

// Env returns the environment for running Cursor as this profile.
// HOME and XDG_CONFIG_HOME are both profile-scoped so the CLI cannot fall
// back to the machine-global account.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return map[string]string{
		"HOME":            prof.HomePath(),
		"XDG_CONFIG_HOME": prof.XDGConfigPath(),
	}, nil
}

// Login starts `cursor-agent login` inside the profile environment.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	if err := p.PrepareProfile(ctx, prof); err != nil {
		return err
	}
	bin, prefix := resolveCursorBin()
	args := append(append([]string{}, prefix...), "login")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = mergeEnv(mustEnv(ctx, p, prof))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting Cursor login flow...")
	fmt.Println("Complete the login in the browser window that opens.")
	return cmd.Run()
}

// Logout removes this profile's Cursor credentials. It does not touch the
// machine-global XDG config or ~/.cursor.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	var errs []error
	for _, path := range profileCredentialPaths(prof) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove %s: %w", filepath.Base(path), err))
		}
	}
	return errors.Join(errs...)
}

// Status reports whether cursor-agent, run inside the profile environment,
// says it is authenticated. Identity metadata on disk is not consulted.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}
	st, err := queryCursorStatus(ctx, mustEnv(ctx, p, prof))
	if err != nil {
		status.LoggedIn = false
		status.Error = err.Error()
		return status, nil
	}
	status.LoggedIn = st.IsAuthenticated
	status.AccountID = st.Email
	return status, nil
}

// ValidateProfile checks if the profile directory layout exists.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	if _, err := os.Stat(prof.HomePath()); os.IsNotExist(err) {
		return fmt.Errorf("home directory missing")
	}
	if _, err := os.Stat(prof.XDGConfigPath()); os.IsNotExist(err) {
		return fmt.Errorf("xdg config directory missing")
	}
	return nil
}

// DetectExistingAuth finds Cursor credentials in the XDG config tree and the
// legacy ~/.cursor tree. A location is valid only when auth.json contains an
// access token. cli-config.json is reported, but authInfo alone is not valid.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider: p.ID(),
	}

	type candidate struct {
		path string
		desc string
		cred bool
	}
	candidates := []candidate{
		{filepath.Join(cursorConfigDir(), "auth.json"), "Cursor Agent credentials (XDG config)", true},
		{filepath.Join(cursorConfigDir(), "cli-config.json"), "Cursor Agent config (XDG config)", false},
		{filepath.Join(legacyCursorDir(), "auth.json"), "Cursor CLI auth credentials (legacy ~/.cursor)", true},
		{filepath.Join(legacyCursorDir(), "cli-config.json"), "Cursor CLI config (legacy ~/.cursor)", false},
	}

	for _, c := range candidates {
		loc := provider.AuthLocation{Path: c.path, Description: c.desc}
		info, err := os.Stat(c.path)
		if err != nil {
			if !os.IsNotExist(err) {
				loc.ValidationError = fmt.Sprintf("stat error: %v", err)
			}
			detection.Locations = append(detection.Locations, loc)
			continue
		}
		loc.Exists = true
		loc.LastModified = info.ModTime()
		loc.FileSize = info.Size()
		if c.cred && fileHasAccessToken(c.path) {
			loc.IsValid = true
		} else if c.cred {
			loc.ValidationError = "no access token"
		} else {
			loc.ValidationError = "account metadata is not a credential"
		}
		detection.Locations = append(detection.Locations, loc)
		if loc.IsValid && detection.Primary == nil {
			detection.Found = true
			locCopy := loc
			detection.Primary = &locCopy
		}
	}
	return detection, nil
}

// ImportAuth copies a detected auth file into the profile's isolated XDG
// config (and the legacy tree, so an older CLI inside the same profile still
// sees it). The machine-global files are not modified.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source auth file not found: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path is a directory, not a file")
	}
	if err := p.PrepareProfile(ctx, prof); err != nil {
		return nil, err
	}

	base := filepath.Base(sourcePath)
	var targets []string
	switch base {
	case "auth.json", "cli-config.json":
		targets = []string{
			filepath.Join(profileXDGCursor(prof), base),
			filepath.Join(profileLegacyCursor(prof), base),
		}
	default:
		targets = []string{filepath.Join(profileLegacyCursor(prof), base)}
	}

	var copied []string
	for _, dst := range targets {
		if err := copyFile(sourcePath, dst); err != nil {
			return copied, fmt.Errorf("copy %s: %w", base, err)
		}
		copied = append(copied, dst)
	}
	return copied, nil
}

// ValidateToken checks the profile's Cursor auth.
//
// Passive mode only checks that an access token file exists inside the
// profile. It does not treat cli-config authInfo as success.
//
// Active mode runs `cursor-agent status --format json` in the profile
// environment and believes only isAuthenticated.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		CheckedAt: time.Now(),
	}
	if passive {
		result.Method = "passive"
		if profileHasAccessToken(prof) {
			result.Valid = true
			return result, nil
		}
		result.Valid = false
		result.Error = "no Cursor access token in this profile (XDG and legacy auth.json)"
		return result, nil
	}

	result.Method = "active"
	st, err := queryCursorStatus(ctx, mustEnv(ctx, p, prof))
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		return result, nil
	}
	result.Valid = st.IsAuthenticated
	if !st.IsAuthenticated {
		result.Error = "cursor-agent status is not authenticated in this profile"
	}
	return result, nil
}

func mustEnv(ctx context.Context, p *Provider, prof *profile.Profile) map[string]string {
	env, err := p.Env(ctx, prof)
	if err != nil || env == nil {
		return map[string]string{}
	}
	return env
}

// profileCredentialPaths lists the auth files logout removes.
func profileCredentialPaths(prof *profile.Profile) []string {
	return []string{
		filepath.Join(profileXDGCursor(prof), "auth.json"),
		filepath.Join(profileXDGCursor(prof), "cli-config.json"),
		filepath.Join(profileLegacyCursor(prof), "auth.json"),
		filepath.Join(profileLegacyCursor(prof), "cli-config.json"),
		filepath.Join(profileLegacyCursor(prof), "settings.json"),
	}
}

func profileHasAccessToken(prof *profile.Profile) bool {
	for _, path := range []string{
		filepath.Join(profileXDGCursor(prof), "auth.json"),
		filepath.Join(profileLegacyCursor(prof), "auth.json"),
	} {
		if fileHasAccessToken(path) {
			return true
		}
	}
	return false
}

func fileHasAccessToken(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	for _, key := range []string{"accessToken", "access_token"} {
		raw, ok := parsed[key]
		if !ok {
			continue
		}
		var token string
		if err := json.Unmarshal(raw, &token); err != nil {
			continue
		}
		if strings.TrimSpace(token) != "" {
			return true
		}
	}
	return false
}

// cursorStatus is the subset of `cursor-agent status --format json` caam
// trusts. Other fields (user ids, names) are ignored.
type cursorStatus struct {
	IsAuthenticated bool
	Email           string
}

func queryCursorStatus(ctx context.Context, env map[string]string) (cursorStatus, error) {
	bin, prefix := resolveCursorBin()
	if _, err := exec.LookPath(bin); err != nil && !filepath.IsAbs(bin) {
		return cursorStatus{}, fmt.Errorf("cursor-agent is not installed (%v); refusing to treat on-disk metadata as a login", err)
	}
	args := append(append([]string{}, prefix...), "status", "--format", "json")

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Env = mergeEnv(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	st, parseErr := parseCursorStatus(stdout.Bytes())
	if parseErr != nil {
		if runErr != nil {
			return cursorStatus{}, fmt.Errorf("cursor-agent status failed: %w", runErr)
		}
		return cursorStatus{}, fmt.Errorf("cursor-agent status returned no authentication JSON")
	}
	return st, nil
}

// parseCursorStatus reads isAuthenticated and, when present, the account
// email. It does not accept authInfo from a config file. A payload that
// lacks isAuthenticated is not a successful status.
func parseCursorStatus(out []byte) (cursorStatus, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return cursorStatus{}, fmt.Errorf("empty status")
	}
	// The CLI prints one JSON document. If a banner precedes it, take the
	// last line that parses as an object.
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(out, &parsed); err != nil {
		lines := bytes.Split(out, []byte("\n"))
		parsed = nil
		for i := len(lines) - 1; i >= 0; i-- {
			line := bytes.TrimSpace(lines[i])
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			if err := json.Unmarshal(line, &parsed); err == nil {
				break
			}
			parsed = nil
		}
		if parsed == nil {
			return cursorStatus{}, fmt.Errorf("status was not JSON")
		}
	}
	raw, ok := parsed["isAuthenticated"]
	if !ok {
		return cursorStatus{}, fmt.Errorf("status JSON has no isAuthenticated field")
	}
	var authed bool
	if err := json.Unmarshal(raw, &authed); err != nil {
		return cursorStatus{}, fmt.Errorf("isAuthenticated was not a boolean")
	}
	st := cursorStatus{IsAuthenticated: authed}
	if info, ok := parsed["userInfo"]; ok {
		var user struct {
			Email string `json:"email"`
		}
		if err := json.Unmarshal(info, &user); err == nil {
			st.Email = strings.TrimSpace(user.Email)
		}
	}
	return st, nil
}

// resolveCursorBin finds the Cursor Agent executable.
//
// Order: `cursor-agent` on PATH; an `agent` whose resolved path is the
// cursor-agent install; the cursor-agent shim at the real ~/.local/bin/agent
// (the `cursor agent` wrapper execs that path, which breaks when HOME is a
// profile); finally `cursor` with an `agent` subcommand.
func resolveCursorBin() (string, []string) {
	if p, err := exec.LookPath("cursor-agent"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("agent"); err == nil && isCursorAgentBinary(p) {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".local", "bin", "agent")
		if isCursorAgentBinary(candidate) {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath("cursor"); err == nil {
		return p, []string{"agent"}
	}
	return "cursor-agent", nil
}

// isCursorAgentBinary reports whether path is the Cursor Agent install and
// not some other program that happens to be named agent (Grok's `agent` is
// the usual collision).
func isCursorAgentBinary(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	resolved := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		resolved = real
	}
	base := filepath.Base(resolved)
	if base == "cursor-agent" || strings.Contains(resolved, "cursor-agent") {
		return true
	}
	// A shell shim that execs cursor-agent names itself in the script.
	// Skip large binaries (another tool's `agent`) without reading them.
	if st.Size() > 64*1024 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(data)
	return strings.Contains(text, "cursor-agent") || strings.Contains(text, "CURSOR_INVOKED_AS")
}

// mergeEnv returns the process environment with overrides winning, including
// when the parent already set the same key. Appending a second HOME or
// XDG_CONFIG_HOME is not enough: getenv returns the first match.
func mergeEnv(overrides map[string]string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(overrides))
	skip := make(map[string]struct{}, len(overrides))
	for k := range overrides {
		skip[k] = struct{}{}
	}
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, drop := skip[key]; drop {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

// copyFile copies src to dst atomically.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	tmpPath := dst + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
