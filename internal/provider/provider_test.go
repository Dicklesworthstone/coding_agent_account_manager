// Package provider defines the interface and common types for AI CLI provider adapters.
package provider

import (
	"context"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// testProvider is a minimal Provider implementation for testing the Registry.
type testProvider struct {
	id          string
	displayName string
	defaultBin  string
}

func (p *testProvider) ID() string                      { return p.id }
func (p *testProvider) DisplayName() string             { return p.displayName }
func (p *testProvider) DefaultBin() string              { return p.defaultBin }
func (p *testProvider) SupportedAuthModes() []AuthMode  { return []AuthMode{AuthModeOAuth} }
func (p *testProvider) AuthFiles() []AuthFileSpec       { return nil }
func (p *testProvider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	return nil
}
func (p *testProvider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return nil, nil
}
func (p *testProvider) Login(ctx context.Context, prof *profile.Profile) error   { return nil }
func (p *testProvider) Logout(ctx context.Context, prof *profile.Profile) error  { return nil }
func (p *testProvider) Status(ctx context.Context, prof *profile.Profile) (*ProfileStatus, error) {
	return nil, nil
}
func (p *testProvider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	return nil
}

func TestAuthModeConstants(t *testing.T) {
	tests := []struct {
		mode AuthMode
		want string
	}{
		{AuthModeOAuth, "oauth"},
		{AuthModeAPIKey, "api-key"},
		{AuthModeVertexADC, "vertex-adc"},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			if string(tt.mode) != tt.want {
				t.Errorf("AuthMode = %q, want %q", tt.mode, tt.want)
			}
		})
	}
}

func TestAuthFileSpec(t *testing.T) {
	spec := AuthFileSpec{
		Path:        "/home/user/.codex/auth.json",
		Description: "Codex CLI OAuth token",
		Required:    true,
	}

	if spec.Path != "/home/user/.codex/auth.json" {
		t.Errorf("Path = %q, want %q", spec.Path, "/home/user/.codex/auth.json")
	}

	if spec.Description != "Codex CLI OAuth token" {
		t.Errorf("Description = %q, want %q", spec.Description, "Codex CLI OAuth token")
	}

	if !spec.Required {
		t.Error("Required should be true")
	}
}

func TestProfileStatus(t *testing.T) {
	status := ProfileStatus{
		LoggedIn:    true,
		AccountID:   "user@example.com",
		ExpiresAt:   "2024-12-31",
		LastUsed:    "2024-01-15",
		HasLockFile: false,
		Error:       "",
	}

	if !status.LoggedIn {
		t.Error("LoggedIn should be true")
	}

	if status.AccountID != "user@example.com" {
		t.Errorf("AccountID = %q, want %q", status.AccountID, "user@example.com")
	}

	if status.HasLockFile {
		t.Error("HasLockFile should be false")
	}

	if status.Error != "" {
		t.Errorf("Error should be empty, got %q", status.Error)
	}
}

func TestNewRegistry(t *testing.T) {
	reg := NewRegistry()

	if reg == nil {
		t.Fatal("NewRegistry() returned nil")
	}

	if reg.providers == nil {
		t.Error("providers map should not be nil")
	}

	if len(reg.providers) != 0 {
		t.Errorf("providers should be empty, got %d entries", len(reg.providers))
	}
}

func TestRegistryRegister(t *testing.T) {
	reg := NewRegistry()

	provider := &testProvider{
		id:          "test",
		displayName: "Test Provider",
		defaultBin:  "test-cli",
	}

	reg.Register(provider)

	if len(reg.providers) != 1 {
		t.Errorf("providers len = %d, want 1", len(reg.providers))
	}

	// Verify it's retrievable
	p, ok := reg.Get("test")
	if !ok {
		t.Error("Get() should return true for registered provider")
	}

	if p.ID() != "test" {
		t.Errorf("ID() = %q, want %q", p.ID(), "test")
	}
}

func TestRegistryRegisterOverwrite(t *testing.T) {
	reg := NewRegistry()

	provider1 := &testProvider{
		id:          "test",
		displayName: "Test Provider 1",
		defaultBin:  "test1",
	}

	provider2 := &testProvider{
		id:          "test",
		displayName: "Test Provider 2",
		defaultBin:  "test2",
	}

	reg.Register(provider1)
	reg.Register(provider2)

	// Should overwrite
	if len(reg.providers) != 1 {
		t.Errorf("providers len = %d, want 1", len(reg.providers))
	}

	p, _ := reg.Get("test")
	if p.DisplayName() != "Test Provider 2" {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), "Test Provider 2")
	}
}

func TestRegistryGet(t *testing.T) {
	reg := NewRegistry()

	provider := &testProvider{
		id:          "codex",
		displayName: "Codex CLI",
		defaultBin:  "codex",
	}

	reg.Register(provider)

	t.Run("existing provider", func(t *testing.T) {
		p, ok := reg.Get("codex")
		if !ok {
			t.Error("Get() should return true for existing provider")
		}
		if p == nil {
			t.Error("Get() should return provider")
		}
		if p.ID() != "codex" {
			t.Errorf("ID() = %q, want %q", p.ID(), "codex")
		}
	})

	t.Run("non-existing provider", func(t *testing.T) {
		p, ok := reg.Get("nonexistent")
		if ok {
			t.Error("Get() should return false for non-existing provider")
		}
		if p != nil {
			t.Error("Get() should return nil for non-existing provider")
		}
	})
}

func TestRegistryAll(t *testing.T) {
	reg := NewRegistry()

	// Empty registry
	all := reg.All()
	if len(all) != 0 {
		t.Errorf("All() len = %d, want 0", len(all))
	}

	// Add providers
	providers := []*testProvider{
		{id: "codex", displayName: "Codex", defaultBin: "codex"},
		{id: "claude", displayName: "Claude", defaultBin: "claude"},
		{id: "gemini", displayName: "Gemini", defaultBin: "gemini"},
	}

	for _, p := range providers {
		reg.Register(p)
	}

	all = reg.All()
	if len(all) != 3 {
		t.Errorf("All() len = %d, want 3", len(all))
	}

	// Verify all providers are present (order may vary)
	ids := make(map[string]bool)
	for _, p := range all {
		ids[p.ID()] = true
	}

	for _, expected := range []string{"codex", "claude", "gemini"} {
		if !ids[expected] {
			t.Errorf("All() missing provider %q", expected)
		}
	}
}

func TestRegistryIDs(t *testing.T) {
	reg := NewRegistry()

	// Empty registry
	ids := reg.IDs()
	if len(ids) != 0 {
		t.Errorf("IDs() len = %d, want 0", len(ids))
	}

	// Add providers
	providers := []*testProvider{
		{id: "codex", displayName: "Codex", defaultBin: "codex"},
		{id: "claude", displayName: "Claude", defaultBin: "claude"},
	}

	for _, p := range providers {
		reg.Register(p)
	}

	ids = reg.IDs()
	if len(ids) != 2 {
		t.Errorf("IDs() len = %d, want 2", len(ids))
	}

	// Verify IDs (order may vary)
	idMap := make(map[string]bool)
	for _, id := range ids {
		idMap[id] = true
	}

	if !idMap["codex"] {
		t.Error("IDs() missing 'codex'")
	}

	if !idMap["claude"] {
		t.Error("IDs() missing 'claude'")
	}
}

func TestTestProviderImplementsInterface(t *testing.T) {
	// Compile-time check that testProvider implements Provider
	var _ Provider = (*testProvider)(nil)
}

func TestProviderInterfaceMethods(t *testing.T) {
	provider := &testProvider{
		id:          "test",
		displayName: "Test Provider",
		defaultBin:  "test-cli",
	}

	if provider.ID() != "test" {
		t.Errorf("ID() = %q, want %q", provider.ID(), "test")
	}

	if provider.DisplayName() != "Test Provider" {
		t.Errorf("DisplayName() = %q, want %q", provider.DisplayName(), "Test Provider")
	}

	if provider.DefaultBin() != "test-cli" {
		t.Errorf("DefaultBin() = %q, want %q", provider.DefaultBin(), "test-cli")
	}

	modes := provider.SupportedAuthModes()
	if len(modes) != 1 || modes[0] != AuthModeOAuth {
		t.Errorf("SupportedAuthModes() = %v, want [oauth]", modes)
	}
}
