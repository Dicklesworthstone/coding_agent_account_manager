// Package provider defines the interface and common types for AI CLI provider adapters.
package provider

import (
	"context"

	"github.com/user/caam/internal/profile"
)

// AuthMode represents the authentication method used by a profile.
type AuthMode string

const (
	AuthModeOAuth      AuthMode = "oauth"       // Browser-based OAuth flow (subscriptions)
	AuthModeAPIKey     AuthMode = "api-key"     // API key authentication
	AuthModeVertexADC  AuthMode = "vertex-adc"  // Vertex AI Application Default Credentials
)

// AuthFileSpec describes where a tool stores authentication credentials.
type AuthFileSpec struct {
	Path        string // Absolute path to the auth file
	Description string // Human-readable description
	Required    bool   // Whether this file must exist for auth to work
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
