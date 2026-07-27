// Package envonly implements a generic provider adapter for tools whose auth
// is managed exclusively through caam's env-injection profiles (token or
// endpoint profiles in the vault) rather than swappable auth files.
//
// One implementation serves every such provider — DeepSeek (DEEPSEEK_API_KEY),
// Ollama (OLLAMA_HOST), Amazon Quick (VITE_AGENT_WS_URL + VITE_INSTANCE_TOKEN)
// — parameterized by a Spec. There are no auth files to back up, restore,
// detect, or import; profiles are created with `caam token add` and injected
// into the environment by run/exec.
package envonly

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Spec parameterizes an env-injection-only provider.
type Spec struct {
	// ID is the provider identifier (e.g. "deepseek", "ollama", "quick").
	ID string

	// DisplayName is the human-friendly provider name.
	DisplayName string

	// Bin is the default binary the provider wraps (may not exist on every
	// system; `caam config set-bin` can override it as with any provider).
	Bin string

	// AuthModes are the auth modes surfaced in provider listings.
	AuthModes []provider.AuthMode

	// AddHint names the command that creates a usable profile; used in error
	// messages from flows that do not apply to env-only providers.
	AddHint string
}

// Provider is a generic adapter for env-injection-only providers.
type Provider struct {
	spec Spec
}

// New creates an env-injection-only provider from its spec.
func New(spec Spec) *Provider {
	if spec.AddHint == "" {
		spec.AddHint = fmt.Sprintf("caam token add %s <name>", spec.ID)
	}
	return &Provider{spec: spec}
}

// ID returns the provider identifier.
func (p *Provider) ID() string { return p.spec.ID }

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string { return p.spec.DisplayName }

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string { return p.spec.Bin }

// SupportedAuthModes returns the supported auth modes.
func (p *Provider) SupportedAuthModes() []provider.AuthMode { return p.spec.AuthModes }

// AuthFiles returns nil: env-only providers have no auth files.
func (p *Provider) AuthFiles() []provider.AuthFileSpec { return nil }

// PrepareProfile creates the profile's base directory (used for locking).
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	if err := os.MkdirAll(prof.HomePath(), 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	return nil
}

// Env returns no variables of its own: run/exec inject the profile's env via
// authfile.ProfileEnv on top of the global environment (UseGlobalEnv).
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return map[string]string{}, nil
}

// Login is not applicable; profiles are created with `caam token add`.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	return fmt.Errorf("%s has no login flow; add a profile with '%s'", p.spec.ID, p.spec.AddHint)
}

// Logout is a no-op: there is no ambient credential state to clear.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error { return nil }

// Status reports on the vault-backed env-injection profile of the same name.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}
	v := authfile.NewVault(authfile.DefaultVaultPath())
	if v.IsTokenProfile(p.spec.ID, prof.Name) {
		if _, meta, err := v.ReadTokenProfile(p.spec.ID, prof.Name); err == nil {
			status.LoggedIn = true
			if meta.Endpoint != "" {
				status.AccountID = meta.Endpoint
			}
		}
	}
	if !status.LoggedIn {
		status.Error = fmt.Sprintf("no env-injection profile; create one with '%s'", p.spec.AddHint)
	}
	return status, nil
}

// ValidateProfile checks that the profile directory exists.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	if _, err := os.Stat(prof.HomePath()); os.IsNotExist(err) {
		return fmt.Errorf("home directory missing")
	}
	return nil
}

// DetectExistingAuth finds nothing: there are no auth files to detect.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	return &provider.AuthDetection{
		Provider:  p.spec.ID,
		Locations: []provider.AuthLocation{},
	}, nil
}

// ImportAuth is not applicable to env-only providers.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	return nil, fmt.Errorf("%s has no auth files to import; add a profile with '%s'", p.spec.ID, p.spec.AddHint)
}

// ValidateToken passively validates the vault-backed env-injection profile of
// the same name. Active probing is handled by `caam validate --active`, which
// owns the endpoint reachability checks.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.spec.ID,
		Profile:   prof.Name,
		Method:    "passive",
		CheckedAt: time.Now(),
	}
	v := authfile.NewVault(authfile.DefaultVaultPath())
	token, meta, err := v.ReadTokenProfile(p.spec.ID, prof.Name)
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		return result, nil
	}
	if err := authfile.ValidateProfileToken(p.spec.ID, token, meta); err != nil {
		result.Valid = false
		result.Error = err.Error()
		return result, nil
	}
	result.Valid = true
	return result, nil
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)

// DeepSeek returns the DeepSeek provider (API-key token profiles injected as
// DEEPSEEK_API_KEY).
func DeepSeek() *Provider {
	return New(Spec{
		ID:          "deepseek",
		DisplayName: "DeepSeek",
		Bin:         "deepseek",
		AuthModes:   []provider.AuthMode{provider.AuthModeAPIKey},
	})
}

// Ollama returns the Ollama provider (endpoint profiles injected as
// OLLAMA_HOST; no auth).
func Ollama() *Provider {
	return New(Spec{
		ID:          "ollama",
		DisplayName: "Ollama",
		Bin:         "ollama",
		AuthModes:   nil, // no authentication
		AddHint:     "caam token add ollama <name> --endpoint http://127.0.0.1:11434",
	})
}

// Quick returns the Amazon Quick provider (endpoint+bearer profiles for the
// local desktop agent, injected as VITE_AGENT_WS_URL + VITE_INSTANCE_TOKEN).
func Quick() *Provider {
	return New(Spec{
		ID:          "quick",
		DisplayName: "Amazon Quick",
		Bin:         "quick",
		AuthModes:   []provider.AuthMode{provider.AuthModeAPIKey},
		AddHint:     "caam token add quick <name> --endpoint ws://localhost:8771",
	})
}
