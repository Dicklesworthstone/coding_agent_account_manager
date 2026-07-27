package authfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Token profiles are a first-class profile type that stores a single
// long-lived token in the vault instead of a snapshot of auth files. At run
// time the token is injected into the wrapped tool's environment (e.g.
// CLAUDE_CODE_OAUTH_TOKEN for Claude Code) rather than swapping files on disk.
//
// This is the primary mechanism for Claude multi-account switching on macOS:
// the Keychain credential slot is single-slot and non-configurable, so
// file-swap profiles cannot capture a working Claude auth state there. Tokens
// minted with `claude setup-token` sidestep the Keychain entirely and are
// parallel-safe (no login/logout races between concurrent sessions).
//
// On-disk layout (inside the regular vault namespace, so token profiles show
// up in List/ls/rotation like any other profile):
//
//	vault/<tool>/<name>/token      the raw token, mode 0600
//	vault/<tool>/<name>/meta.json  TokenMeta with Type: "token", mode 0600
//
// The active token profile (the default for `caam run`/`caam exec`) is
// recorded in vault/<tool>/.active-token. File-swap profiles cannot use the
// live-file-comparison ActiveProfile mechanism for token profiles because
// token profiles never touch the live auth files.
const (
	// TokenFileName is the vault file holding the raw token.
	TokenFileName = "token"

	// TokenMetaFileName is the vault file marking a profile as token-type.
	TokenMetaFileName = "meta.json"

	// activeTokenFileName records the default token profile for a tool.
	activeTokenFileName = ".active-token"

	// ProfileTypeToken is the meta.json type marker for token profiles.
	ProfileTypeToken = "token"
)

// TokenMeta describes a token profile. Stored as meta.json in the profile's
// vault directory.
type TokenMeta struct {
	// Type is the profile type marker; always ProfileTypeToken.
	Type string `json:"type"`

	// Provider is the tool this token authenticates (claude, ...).
	Provider string `json:"provider"`

	// Name is the profile name.
	Name string `json:"name"`

	// Source records where the token came from (e.g. an imported file path,
	// or "stdin"). Informational only.
	Source string `json:"source,omitempty"`

	// CreatedAt is when the token profile was created or last updated.
	CreatedAt time.Time `json:"created_at"`
}

// SaveTokenProfile creates (or overwrites) a token profile in the vault.
// The token file is written with mode 0600.
func (v *Vault) SaveTokenProfile(tool, name, token, source string) error {
	dir, err := v.safeProfileDir(tool, name)
	if err != nil {
		return err
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is empty")
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create token profile dir: %w", err)
	}

	meta := TokenMeta{
		Type:      ProfileTypeToken,
		Provider:  strings.ToLower(strings.TrimSpace(tool)),
		Name:      name,
		Source:    source,
		CreatedAt: time.Now().UTC(),
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token meta: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, TokenFileName), []byte(token+"\n"), 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokenMetaFileName), metaData, 0600); err != nil {
		return fmt.Errorf("write token meta: %w", err)
	}
	return nil
}

// IsTokenProfile reports whether the named vault profile is a token profile
// (has a meta.json with type "token").
func (v *Vault) IsTokenProfile(tool, name string) bool {
	dir, err := v.safeProfileDir(tool, name)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, TokenMetaFileName))
	if err != nil {
		return false
	}
	var meta TokenMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.Type == ProfileTypeToken
}

// ReadTokenProfile returns the stored token and metadata for a token profile.
func (v *Vault) ReadTokenProfile(tool, name string) (string, *TokenMeta, error) {
	dir, err := v.safeProfileDir(tool, name)
	if err != nil {
		return "", nil, err
	}

	metaData, err := os.ReadFile(filepath.Join(dir, TokenMetaFileName))
	if err != nil {
		return "", nil, fmt.Errorf("read token meta: %w", err)
	}
	var meta TokenMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return "", nil, fmt.Errorf("parse token meta: %w", err)
	}
	if meta.Type != ProfileTypeToken {
		return "", nil, fmt.Errorf("%s/%s is not a token profile (type %q)", tool, name, meta.Type)
	}

	tokenData, err := os.ReadFile(filepath.Join(dir, TokenFileName))
	if err != nil {
		return "", nil, fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenData))
	if token == "" {
		return "", nil, fmt.Errorf("token file for %s/%s is empty", tool, name)
	}
	return token, &meta, nil
}

// ListTokenProfiles returns the names of all token profiles for a tool.
func (v *Vault) ListTokenProfiles(tool string) ([]string, error) {
	all, err := v.List(tool)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, name := range all {
		if v.IsTokenProfile(tool, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// SetActiveTokenProfile records the named token profile as the default for
// `caam run`/`caam exec` env injection.
func (v *Vault) SetActiveTokenProfile(tool, name string) error {
	if !v.IsTokenProfile(tool, name) {
		return fmt.Errorf("%s/%s is not a token profile", tool, name)
	}
	toolDir, err := v.safeToolDir(tool)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(toolDir, 0700); err != nil {
		return fmt.Errorf("create tool dir: %w", err)
	}
	return os.WriteFile(filepath.Join(toolDir, activeTokenFileName), []byte(name+"\n"), 0600)
}

// ActiveTokenProfile returns the default token profile for a tool, or "" if
// none is set (or the recorded profile no longer exists as a token profile).
func (v *Vault) ActiveTokenProfile(tool string) (string, error) {
	toolDir, err := v.safeToolDir(tool)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(toolDir, activeTokenFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" || !v.IsTokenProfile(tool, name) {
		return "", nil
	}
	return name, nil
}

// ClearActiveTokenProfile removes the default token profile marker for a tool.
// Clearing when no marker exists is not an error.
func (v *Vault) ClearActiveTokenProfile(tool string) error {
	toolDir, err := v.safeToolDir(tool)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(toolDir, activeTokenFileName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// TokenEnv returns the environment variables that inject a token profile into
// the wrapped tool. For Claude Code:
//
//	CLAUDE_CODE_OAUTH_TOKEN  the token itself
//	CLAUDE_CONFIG_DIR        $HOME/.claude-<name> (per-profile settings and
//	                         history isolation; parallel-safe)
//
// Other providers gain token/env support in later workstreams; unknown tools
// return an error so callers fail loudly instead of running unauthenticated.
func TokenEnv(tool, name, token string) (map[string]string, error) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "claude":
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": token,
			"CLAUDE_CONFIG_DIR":       filepath.Join(homeDir, ".claude-"+name),
		}, nil
	default:
		return nil, fmt.Errorf("token profiles are not supported for %s yet", tool)
	}
}

// ValidateTokenFormat performs a passive, offline sanity check of a token's
// shape for the given tool. It never makes network calls. An empty error means
// the token looks plausible; it does NOT guarantee the token is live (use an
// active probe for that).
func ValidateTokenFormat(tool, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "claude":
		// Long-lived tokens from `claude setup-token` look like
		// sk-ant-oat01-... ; API keys look like sk-ant-api03-... . Accept the
		// sk-ant- family and reject obviously foreign strings.
		if !strings.HasPrefix(token, "sk-ant-") {
			return fmt.Errorf("claude tokens start with sk-ant- (got %q...)", truncateToken(token))
		}
		if len(token) < 20 {
			return fmt.Errorf("token too short to be a claude token")
		}
	}
	return nil
}

// truncateToken returns a short non-sensitive prefix of a token for error
// messages.
func truncateToken(token string) string {
	const n = 6
	if len(token) <= n {
		return token
	}
	return token[:n]
}
