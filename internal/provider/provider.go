// Package provider defines the interface and common types for AI CLI provider adapters.
package provider

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// AuthMode represents the authentication method used by a profile.
type AuthMode string

const (
	AuthModeOAuth      AuthMode = "oauth"       // Browser-based OAuth flow (subscriptions)
	AuthModeAPIKey     AuthMode = "api-key"     // API key authentication
	AuthModeDeviceCode AuthMode = "device-code" // OAuth device code flow (RFC 8628)
	AuthModeVertexADC  AuthMode = "vertex-adc"  // Vertex AI Application Default Credentials
)

// EnvironmentChanges describes a profile's environment without mutating the
// caller's environment or credentials. Unset applies only to inherited values;
// explicit caller overrides may still supply a different credential.
type EnvironmentChanges struct {
	Set   map[string]string `json:"set"`
	Unset []string          `json:"unset"`
}

// ProfileEnvironment resolves the selected profile's paths and credential
// policy. Like Provider.Env, this is read-only; launch preparation is separate.
func ProfileEnvironment(ctx context.Context, prov Provider, prof *profile.Profile) (EnvironmentChanges, error) {
	env, err := prov.Env(ctx, prof)
	if err != nil {
		return EnvironmentChanges{}, err
	}
	return CredentialEnvironment(prov.ID(), AuthMode(prof.AuthMode), env), nil
}

// CredentialEnvironment removes inherited overrides for the selected
// provider, leaving other providers' keys available to tools. API-key profiles
// retain their documented ambient key inputs. Callers selecting a saved vault
// credential use OAuth mode here even if that file itself contains an API key:
// the saved file, rather than an ambient key, is the selected account.
// set may be nil for a global-home launch or contain shallow/isolated paths.
func CredentialEnvironment(providerID string, mode AuthMode, set map[string]string) EnvironmentChanges {
	return credentialEnvironmentForOS(providerID, mode, set, runtime.GOOS)
}

func credentialEnvironmentForOS(providerID string, mode AuthMode, set map[string]string, goos string) EnvironmentChanges {
	changes := EnvironmentChanges{Set: make(map[string]string, len(set)), Unset: []string{}}
	for key, value := range set {
		changes.Set[key] = value
	}
	var unset []string
	apiKey := mode == AuthModeAPIKey
	switch providerID {
	case "claude":
		unset = append(unset, "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
			"CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
			"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
			"ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID",
			"ANTHROPIC_IDENTITY_TOKEN_FILE")
		if !apiKey {
			unset = append(unset, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS")
		}
	case "codex":
		if !apiKey {
			unset = append(unset, "OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL")
		}
	case "gemini":
		unset = append(unset, "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GENAI_USE_GCA")
		if !apiKey {
			unset = append(unset, "GEMINI_API_KEY", "GOOGLE_API_KEY")
		}
	case "grok":
		unset = append(unset, "GROK_AUTH", "GROK_AUTH_PATH", "GROK_API_KEY", "GROK_DEPLOYMENT_KEY",
			"XAI_API_KEY", "XAI_API_TOKEN", "GROK_BASE_URL", "GROK_API_BASE_URL", "XAI_BASE_URL")
	case "cursor":
		if !apiKey {
			unset = append(unset, "CURSOR_API_KEY")
		}
	}
	if goos == "windows" {
		// Node and native Windows programs can prefer USERPROFILE to HOME.
		// Override it alongside an isolated HOME and discard legacy aliases.
		for key, value := range changes.Set {
			if strings.EqualFold(key, "HOME") && value != "" {
				changes.Set["USERPROFILE"] = value
				unset = append(unset, "HOMEDRIVE", "HOMEPATH")
				break
			}
		}
	}
	sort.Strings(unset)
	changes.Unset = append(changes.Unset, unset...)
	return changes
}

// MergeEnvironment applies inherited values, the selected profile's removals
// and settings, then explicit caller overrides. Sorting makes shell/process
// plans deterministic, and Windows names are compared case-insensitively.
func MergeEnvironment(inherited []string, changes EnvironmentChanges, explicit map[string]string) []string {
	return mergeEnvironmentForOS(inherited, changes, explicit, runtime.GOOS)
}

func mergeEnvironmentForOS(inherited []string, changes EnvironmentChanges, explicit map[string]string, goos string) []string {
	normalize := func(key string) string {
		if goos == "windows" {
			return strings.ToUpper(key)
		}
		return key
	}
	values := make(map[string]string, len(inherited)+len(changes.Set)+len(explicit))
	for _, entry := range inherited {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			values[normalize(key)] = value
		}
	}
	for _, key := range changes.Unset {
		delete(values, normalize(key))
	}
	apply := func(env map[string]string) {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			values[normalize(key)] = env[key]
		}
	}
	apply(changes.Set)
	apply(explicit)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

// AuthFileSpec describes where a tool stores authentication credentials.
type AuthFileSpec struct {
	Path        string // Absolute path to the auth file
	Description string // Human-readable description
	Required    bool   // Whether this file must exist for auth to work
}

// AuthLocation represents a detected auth file location with metadata.
type AuthLocation struct {
	Path            string    // Absolute path to auth file
	Exists          bool      // File exists
	LastModified    time.Time // Modification time
	FileSize        int64     // Size in bytes
	IsValid         bool      // Basic format validation passed
	ValidationError string    // If IsValid=false, why
	Description     string    // Human-readable description of this auth file
}

// AuthDetection represents detected existing auth in the system.
type AuthDetection struct {
	Provider  string         // e.g., "claude", "codex", "gemini"
	Found     bool           // Whether any auth was detected
	Locations []AuthLocation // All detected auth file locations
	Primary   *AuthLocation  // Recommended/most recent location
	Warning   string         // Any issues (e.g., "multiple auth files found")
}

// ValidationResult represents the result of token validation.
type ValidationResult struct {
	Provider  string    // Provider ID (e.g., "claude", "codex", "gemini")
	Profile   string    // Profile name
	Valid     bool      // Whether the token is valid
	Method    string    // "passive" (no network) or "active" (API call)
	ExpiresAt time.Time // When token expires (if known)
	Error     string    // If validation failed, the reason why
	CheckedAt time.Time // When this validation was performed
}

// DeviceCodeProvider extends Provider with device code flow support.
// Providers that do not support this flow do not need to implement this interface.
type DeviceCodeProvider interface {
	Provider

	// SupportsDeviceCode returns true if this provider supports device code flow.
	SupportsDeviceCode() bool
	// LoginWithDeviceCode initiates device code authentication.
	LoginWithDeviceCode(ctx context.Context, p *profile.Profile) error
}

// ProfileRefresher is an optional extension for providers whose isolated
// profile layout mirrors parts of the real home (shared skills, plugins,
// slash commands, ...). The runner calls RefreshProfile best-effort before
// every isolated run so profiles created by an older caam, or before the user
// installed new tooling, are healed in place instead of requiring re-creation.
// Implementations must be idempotent and must never touch account state.
type ProfileRefresher interface {
	RefreshProfile(ctx context.Context, p *profile.Profile) error
}

// ProfileStatus represents the current authentication state of a profile.
type ProfileStatus struct {
	LoggedIn    bool   // Whether the profile has valid auth credentials
	AccountID   string // Account identifier (email, API key prefix, etc.)
	ExpiresAt   string // Expiration time if applicable
	LastUsed    string // Last time this profile was used
	HasLockFile bool   // Whether a session is currently active
	Error       string // Error message if status check failed
}

// Provider defines the interface that all AI CLI adapters must implement.
// Each provider manages authentication, environment setup, and execution
// for a specific AI CLI tool (Codex, Claude, Gemini).
type Provider interface {
	// ID returns the unique identifier for this provider (e.g., "codex", "claude", "gemini").
	ID() string

	// DisplayName returns a human-friendly name for the provider.
	DisplayName() string

	// DefaultBin returns the default binary name for the CLI (e.g., "codex", "claude", "gemini").
	DefaultBin() string

	// SupportedAuthModes returns the authentication modes this provider supports.
	SupportedAuthModes() []AuthMode

	// AuthFiles returns the auth file specifications for this provider.
	// This is the key method for auth file backup/restore functionality.
	AuthFiles() []AuthFileSpec

	// PrepareProfile sets up the profile directory structure with necessary files and symlinks.
	// This is called when a new profile is created.
	PrepareProfile(ctx context.Context, p *profile.Profile) error

	// Env returns the environment variables needed to run the CLI in this profile's context.
	// The returned map should contain all necessary overrides (HOME, XDG_CONFIG_HOME, etc.).
	Env(ctx context.Context, p *profile.Profile) (map[string]string, error)

	// Login initiates the authentication flow for the profile.
	// For OAuth flows, this may open a browser. For API key mode, it prompts for input.
	Login(ctx context.Context, p *profile.Profile) error

	// Logout clears authentication credentials for the profile.
	// Not all providers support explicit logout.
	Logout(ctx context.Context, p *profile.Profile) error

	// Status checks the current authentication state of the profile.
	Status(ctx context.Context, p *profile.Profile) (*ProfileStatus, error)

	// ValidateProfile checks if a profile is correctly configured.
	ValidateProfile(ctx context.Context, p *profile.Profile) error

	// DetectExistingAuth detects existing authentication files in standard system locations.
	// This is used for first-run experience to discover and import existing credentials.
	// Detection is read-only and never modifies original files.
	DetectExistingAuth() (*AuthDetection, error)

	// ImportAuth imports detected auth files into a profile directory.
	// The sourcePath specifies which auth file to import (should be from DetectExistingAuth).
	// The targetProfile is the profile to import into (its directories should already exist).
	// Returns list of files copied and any error.
	ImportAuth(ctx context.Context, sourcePath string, targetProfile *profile.Profile) ([]string, error)

	// ValidateToken validates that the authentication token actually works.
	// If passive=true, only checks token expiry and format (no network calls).
	// If passive=false, makes a minimal API call to verify the token is valid.
	// Active validation may incur minimal API costs.
	ValidateToken(ctx context.Context, p *profile.Profile, passive bool) (*ValidationResult, error)
}

// Registry holds all registered providers.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.providers[p.ID()] = p
}

// Get retrieves a provider by ID.
func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.providers[id]
	return p, ok
}

// All returns all registered providers.
func (r *Registry) All() []Provider {
	result := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

// IDs returns the IDs of all registered providers.
func (r *Registry) IDs() []string {
	result := make([]string, 0, len(r.providers))
	for id := range r.providers {
		result = append(result, id)
	}
	return result
}

// ProviderMeta holds static metadata about a provider.
// This is separate from the Provider interface to allow access without
// instantiating a provider (e.g., for TUI display, open command).
type ProviderMeta struct {
	ID          string // Provider identifier (e.g., "codex", "claude", "gemini")
	DisplayName string // Human-friendly name
	AccountURL  string // URL to the provider's account/console page
	Description string // Short description of the account page
}

// providerMetaRegistry holds static metadata for all known providers.
var providerMetaRegistry = map[string]ProviderMeta{
	"codex": {
		ID:          "codex",
		DisplayName: "Codex (OpenAI)",
		AccountURL:  "https://platform.openai.com/account",
		Description: "OpenAI Platform account settings",
	},
	"claude": {
		ID:          "claude",
		DisplayName: "Claude (Anthropic)",
		AccountURL:  "https://console.anthropic.com/",
		Description: "Anthropic Console dashboard",
	},
	"gemini": {
		ID:          "gemini",
		DisplayName: "Gemini (Google)",
		AccountURL:  "https://aistudio.google.com/",
		Description: "Google AI Studio dashboard",
	},
	"agy": {
		ID:          "agy",
		DisplayName: "Antigravity (Google)",
		AccountURL:  "https://antigravity.google/",
		Description: "Google Antigravity account",
	},
	"grok": {
		ID:          "grok",
		DisplayName: "Grok Build (xAI)",
		AccountURL:  "https://console.x.ai/",
		Description: "xAI Console",
	},
	"opencode": {
		ID:          "opencode",
		DisplayName: "OpenCode",
		AccountURL:  "https://opencode.ai/",
		Description: "OpenCode dashboard",
	},
	"cursor": {
		ID:          "cursor",
		DisplayName: "Cursor",
		AccountURL:  "https://cursor.com/settings",
		Description: "Cursor account settings",
	},
}

// GetProviderMeta returns metadata for a provider by ID.
// Returns the metadata and true if found, or zero value and false if not.
func GetProviderMeta(id string) (ProviderMeta, bool) {
	meta, ok := providerMetaRegistry[id]
	return meta, ok
}

// AllProviderMeta returns metadata for all known providers.
func AllProviderMeta() []ProviderMeta {
	result := make([]ProviderMeta, 0, len(providerMetaRegistry))
	for _, meta := range providerMetaRegistry {
		result = append(result, meta)
	}
	return result
}

// KnownProviderIDs returns the IDs of all known providers.
func KnownProviderIDs() []string {
	result := make([]string, 0, len(providerMetaRegistry))
	for id := range providerMetaRegistry {
		result = append(result, id)
	}
	return result
}
