package envonly

import (
	"context"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

func TestSpecs(t *testing.T) {
	tests := []struct {
		p       *Provider
		id      string
		display string
		bin     string
		hasAuth bool
	}{
		{DeepSeek(), "deepseek", "DeepSeek", "deepseek", true},
		{Ollama(), "ollama", "Ollama", "ollama", false},
		{Quick(), "quick", "Amazon Quick", "quick", true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if tt.p.ID() != tt.id {
				t.Errorf("ID = %q, want %q", tt.p.ID(), tt.id)
			}
			if tt.p.DisplayName() != tt.display {
				t.Errorf("DisplayName = %q, want %q", tt.p.DisplayName(), tt.display)
			}
			if tt.p.DefaultBin() != tt.bin {
				t.Errorf("DefaultBin = %q, want %q", tt.p.DefaultBin(), tt.bin)
			}
			if got := len(tt.p.SupportedAuthModes()) > 0; got != tt.hasAuth {
				t.Errorf("SupportedAuthModes non-empty = %v, want %v", got, tt.hasAuth)
			}
			if tt.p.AuthFiles() != nil {
				t.Error("AuthFiles must be nil for env-only providers")
			}
		})
	}
}

func TestLoginRejectsWithHint(t *testing.T) {
	p := Ollama()
	prof := &profile.Profile{Name: "x", Provider: "ollama", BasePath: t.TempDir()}
	err := p.Login(context.Background(), prof)
	if err == nil {
		t.Fatal("expected Login to be unsupported")
	}
	if want := "caam token add ollama"; !strings.Contains(err.Error(), want) {
		t.Errorf("Login error %q does not hint at %q", err, want)
	}
}

func TestDetectExistingAuth_Empty(t *testing.T) {
	det, err := DeepSeek().DetectExistingAuth()
	if err != nil {
		t.Fatalf("DetectExistingAuth error: %v", err)
	}
	if det.Found || len(det.Locations) != 0 {
		t.Errorf("DetectExistingAuth = %+v, want empty", det)
	}
}

func TestImplementsProvider(t *testing.T) {
	var _ provider.Provider = DeepSeek()
	var _ provider.Provider = Ollama()
	var _ provider.Provider = Quick()
}
