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

	// ProfileTypeEndpoint is the meta.json type marker for endpoint profiles
	// (endpoint URL + optional bearer token; see endpoint.go). Both types form
	// the env-injection profile family and are handled uniformly by
	// IsTokenProfile / ReadTokenProfile / rotation / cooldowns.
	ProfileTypeEndpoint = "endpoint"
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

	// Endpoint is the service endpoint URL for endpoint profiles (Type
	// "endpoint"): the Ollama server, the Amazon Quick local agent WebSocket,
	// or an anthropic-compatible base URL. Empty for plain token profiles.
	Endpoint string `json:"endpoint,omitempty"`

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
	return writeTokenProfileFiles(dir, token, meta)
}

// writeTokenProfileFiles writes the token file (when token is non-empty) and
// meta.json for an env-injection profile, both mode 0600. A pre-existing
// token file is removed when token is empty so re-saving an endpoint profile
// without auth cannot leave a stale credential behind.
func writeTokenProfileFiles(dir, token string, meta TokenMeta) error {
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token meta: %w", err)
	}

	tokenPath := filepath.Join(dir, TokenFileName)
	if token != "" {
		if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
			return fmt.Errorf("write token file: %w", err)
		}
	} else if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale token file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokenMetaFileName), metaData, 0600); err != nil {
		return fmt.Errorf("write token meta: %w", err)
	}
	return nil
}

// IsTokenProfile reports whether the named vault profile is an env-injection
// profile (has a meta.json with type "token" or "endpoint"). Both family
// members behave identically at every integration point — activation records
// a default instead of swapping files, run/exec inject environment variables,
// rotation/cooldowns/ls/status treat them first-class — so callers need no
// finer distinction; ProfileEnv dispatches on the stored type.
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
	return meta.Type == ProfileTypeToken || meta.Type == ProfileTypeEndpoint
}

// ReadTokenProfile returns the stored token and metadata for an env-injection
// profile (token or endpoint type). For endpoint profiles whose provider needs
// no auth (e.g. ollama) the returned token is "" and no error is raised; for
// every other case a missing or empty token file is an error.
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
	if meta.Type != ProfileTypeToken && meta.Type != ProfileTypeEndpoint {
		return "", nil, fmt.Errorf("%s/%s is not a token profile (type %q)", tool, name, meta.Type)
	}

	tokenOptional := false
	if meta.Type == ProfileTypeEndpoint {
		if spec, ok := EndpointSpecFor(tool); ok && !spec.TokenRequired {
			tokenOptional = true
		}
	}

	tokenData, err := os.ReadFile(filepath.Join(dir, TokenFileName))
	if err != nil {
		if tokenOptional && os.IsNotExist(err) {
			return "", &meta, nil
		}
		return "", nil, fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenData))
	if token == "" && !tokenOptional {
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

// TokenEnv returns the environment variables that inject a plain token
// profile into the wrapped tool. It is the meta-less form of ProfileEnv; use
// ProfileEnv when the profile's TokenMeta is at hand so endpoint profiles
// resolve correctly.
func TokenEnv(tool, name, token string) (map[string]string, error) {
	return ProfileEnv(tool, name, token, nil)
}

// ProfileEnv returns the environment variables that inject an env-injection
// profile into the wrapped tool. meta may be nil (plain token profile).
//
// Token profiles:
//
//	claude    CLAUDE_CODE_OAUTH_TOKEN + CLAUDE_CONFIG_DIR=$HOME/.claude-<name>
//	          (per-profile settings/history isolation; parallel-safe)
//	deepseek  DEEPSEEK_API_KEY
//	grok      GROK_DEPLOYMENT_KEY (the CLI's documented env credential; it
//	          takes precedence over auth.json, which is exactly the injection
//	          semantics token profiles want)
//
// Endpoint profiles (meta.Type == "endpoint"):
//
//	claude    ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN + CLAUDE_CONFIG_DIR
//	          (anthropic-compatible endpoints: GLM, Moonshot/Kimi, ...)
//	ollama    OLLAMA_HOST (no auth)
//	quick     VITE_AGENT_WS_URL + VITE_INSTANCE_TOKEN (Amazon Quick local
//	          desktop agent; variable names match the agent's own protocol)
//
// Unknown tools return an error so callers fail loudly instead of running
// unauthenticated.
func ProfileEnv(tool, name, token string, meta *TokenMeta) (map[string]string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))

	if meta != nil && meta.Type == ProfileTypeEndpoint {
		if meta.Endpoint == "" {
			return nil, fmt.Errorf("endpoint profile %s/%s has no endpoint URL", tool, name)
		}
		switch tool {
		case "claude":
			configDir, err := claudeProfileConfigDir(name)
			if err != nil {
				return nil, err
			}
			return map[string]string{
				"ANTHROPIC_BASE_URL":   meta.Endpoint,
				"ANTHROPIC_AUTH_TOKEN": token,
				"CLAUDE_CONFIG_DIR":    configDir,
			}, nil
		case "ollama":
			return map[string]string{
				"OLLAMA_HOST": meta.Endpoint,
			}, nil
		case "quick":
			return map[string]string{
				"VITE_AGENT_WS_URL":   meta.Endpoint,
				"VITE_INSTANCE_TOKEN": token,
			}, nil
		default:
			return nil, fmt.Errorf("endpoint profiles are not supported for %s", tool)
		}
	}

	switch tool {
	case "claude":
		configDir, err := claudeProfileConfigDir(name)
		if err != nil {
			return nil, err
		}
		return map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": token,
			"CLAUDE_CONFIG_DIR":       configDir,
		}, nil
	case "deepseek":
		return map[string]string{
			"DEEPSEEK_API_KEY": token,
		}, nil
	case "grok":
		return map[string]string{
			"GROK_DEPLOYMENT_KEY": token,
		}, nil
	default:
		return nil, fmt.Errorf("token profiles are not supported for %s yet", tool)
	}
}

// claudeProfileConfigDir returns the per-profile CLAUDE_CONFIG_DIR for a
// claude env-injection profile.
func claudeProfileConfigDir(name string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(homeDir, ".claude-"+name), nil
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
			return fmt.Errorf("claude tokens start with sk-ant- (got %s)", describeToken(token))
		}
		if len(token) < 20 {
			return fmt.Errorf("token too short to be a claude token")
		}
	case "deepseek":
		// DeepSeek API keys (platform.deepseek.com) look like sk-<hex/alnum>.
		if !strings.HasPrefix(token, "sk-") {
			return fmt.Errorf("deepseek API keys start with sk- (got %s)", describeToken(token))
		}
		if len(token) < 16 {
			return fmt.Errorf("token too short to be a deepseek API key")
		}
	}
	return nil
}

// ValidateProfileToken is the meta-aware form of ValidateTokenFormat: endpoint
// profiles skip the plain-token shape checks (an anthropic-compatible token
// for a claude endpoint profile is issued by GLM/Moonshot/etc., not
// Anthropic), enforcing only the provider's token-required rule. Passive,
// never makes network calls.
func ValidateProfileToken(tool, token string, meta *TokenMeta) error {
	if meta != nil && meta.Type == ProfileTypeEndpoint {
		spec, ok := EndpointSpecFor(tool)
		if !ok {
			return fmt.Errorf("endpoint profiles are not supported for %s", tool)
		}
		if meta.Endpoint == "" {
			return fmt.Errorf("endpoint profile has no endpoint URL")
		}
		if spec.TokenRequired && strings.TrimSpace(token) == "" {
			return fmt.Errorf("token is empty")
		}
		return nil
	}
	return ValidateTokenFormat(tool, token)
}

// describeToken returns a non-sensitive description of a token for error
// messages: its length and a coarse prefix class only. It never echoes any
// token bytes — a "token" that fails the format check may be a mispasted
// secret of some other kind, and even a short prefix can leak it.
func describeToken(token string) string {
	class := "unrecognized prefix"
	switch {
	case strings.HasPrefix(token, "sk-ant-"):
		class = "sk-ant-* prefix"
	case strings.HasPrefix(token, "sk-"):
		class = "sk-* prefix"
	}
	return fmt.Sprintf("%d chars, %s", len(token), class)
}
