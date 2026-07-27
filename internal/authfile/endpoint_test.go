package authfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndReadEndpointProfile_Ollama(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveEndpointProfile("ollama", "local", "http://127.0.0.1:11434", "", "cli"); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}

	token, meta, err := v.ReadTokenProfile("ollama", "local")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty (ollama has no auth)", token)
	}
	if meta.Type != ProfileTypeEndpoint {
		t.Errorf("meta.Type = %q, want %q", meta.Type, ProfileTypeEndpoint)
	}
	if meta.Endpoint != "http://127.0.0.1:11434" {
		t.Errorf("meta.Endpoint = %q, want stored URL", meta.Endpoint)
	}
	if meta.Provider != "ollama" || meta.Name != "local" {
		t.Errorf("meta = %+v, want provider/name set", meta)
	}

	// No token file must exist for an auth-less endpoint profile.
	if _, err := os.Stat(filepath.Join(v.BasePath(), "ollama", "local", TokenFileName)); !os.IsNotExist(err) {
		t.Errorf("token file exists for ollama endpoint profile (stat err = %v)", err)
	}
}

func TestSaveEndpointProfile_OllamaRejectsToken(t *testing.T) {
	v := newTestVault(t)
	if err := v.SaveEndpointProfile("ollama", "local", "http://127.0.0.1:11434", "some-token", ""); err == nil {
		t.Error("expected error: ollama endpoint profiles take no token")
	}
}

func TestSaveEndpointProfile_QuickRequiresToken(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveEndpointProfile("quick", "desktop", "ws://localhost:8771", "", ""); err == nil {
		t.Error("expected error: quick endpoint profiles require a token")
	}

	if err := v.SaveEndpointProfile("quick", "desktop", "ws://localhost:8771", "instance-token-abc123", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}
	token, meta, err := v.ReadTokenProfile("quick", "desktop")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "instance-token-abc123" {
		t.Errorf("token = %q, want stored bearer", token)
	}
	if meta.Endpoint != "ws://localhost:8771" {
		t.Errorf("meta.Endpoint = %q", meta.Endpoint)
	}
}

func TestSaveEndpointProfile_ClaudeAnthropicCompatible(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveEndpointProfile("claude", "glm", "https://api.z.ai/api/anthropic", "glm-issued-token.abc", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}
	token, meta, err := v.ReadTokenProfile("claude", "glm")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "glm-issued-token.abc" {
		t.Errorf("token = %q", token)
	}
	if meta.Type != ProfileTypeEndpoint {
		t.Errorf("meta.Type = %q, want %q", meta.Type, ProfileTypeEndpoint)
	}

	// Endpoint profiles are part of the env-injection family.
	if !v.IsTokenProfile("claude", "glm") {
		t.Error("IsTokenProfile(endpoint profile) = false, want true")
	}
	// And activatable as the run/exec default like any token profile.
	if err := v.SetActiveTokenProfile("claude", "glm"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}
	if got, _ := v.ActiveTokenProfile("claude"); got != "glm" {
		t.Errorf("ActiveTokenProfile = %q, want glm", got)
	}
}

func TestSaveEndpointProfile_UnsupportedProvider(t *testing.T) {
	v := newTestVault(t)
	if err := v.SaveEndpointProfile("deepseek", "x", "https://api.deepseek.com", "sk-abc", ""); err == nil {
		t.Error("expected error for provider without endpoint support")
	}
}

func TestSaveEndpointProfile_RemovesStaleTokenFile(t *testing.T) {
	v := newTestVault(t)

	// Simulate a stale credential left in the profile dir.
	dir := filepath.Join(v.BasePath(), "ollama", "local")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokenFileName), []byte("stale\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := v.SaveEndpointProfile("ollama", "local", "http://127.0.0.1:11434", "", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, TokenFileName)); !os.IsNotExist(err) {
		t.Errorf("stale token file survived re-save (stat err = %v)", err)
	}
}

func TestValidateEndpointURL(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		endpoint string
		wantErr  bool
	}{
		{"ollama http", "ollama", "http://127.0.0.1:11434", false},
		{"ollama https remote", "ollama", "https://gpu-box.example.com", false},
		{"ollama ws rejected", "ollama", "ws://127.0.0.1:11434", true},
		{"ollama no host", "ollama", "http://", true},
		{"ollama garbage", "ollama", "not a url", true},
		{"quick ws", "quick", "ws://localhost:8771", false},
		{"quick wss", "quick", "wss://localhost:8771", false},
		{"quick http", "quick", "http://localhost:8771", false},
		{"claude https", "claude", "https://api.moonshot.ai/anthropic", false},
		{"claude ws rejected", "claude", "ws://api.moonshot.ai", true},
		{"unsupported provider", "deepseek", "https://api.deepseek.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEndpointURL(tt.tool, tt.endpoint)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEndpointURL(%s, %s) error = %v, wantErr %v", tt.tool, tt.endpoint, err, tt.wantErr)
			}
		})
	}
}

func TestProfileEnv_EndpointProfiles(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("ollama", func(t *testing.T) {
		meta := &TokenMeta{Type: ProfileTypeEndpoint, Endpoint: "http://gpu-box:11434"}
		env, err := ProfileEnv("ollama", "gpu", "", meta)
		if err != nil {
			t.Fatalf("ProfileEnv error: %v", err)
		}
		if env["OLLAMA_HOST"] != "http://gpu-box:11434" {
			t.Errorf("OLLAMA_HOST = %q", env["OLLAMA_HOST"])
		}
		if len(env) != 1 {
			t.Errorf("env = %v, want only OLLAMA_HOST", env)
		}
	})

	t.Run("quick", func(t *testing.T) {
		meta := &TokenMeta{Type: ProfileTypeEndpoint, Endpoint: "ws://localhost:8771"}
		env, err := ProfileEnv("quick", "desktop", "tok-123", meta)
		if err != nil {
			t.Fatalf("ProfileEnv error: %v", err)
		}
		if env["VITE_AGENT_WS_URL"] != "ws://localhost:8771" {
			t.Errorf("VITE_AGENT_WS_URL = %q", env["VITE_AGENT_WS_URL"])
		}
		if env["VITE_INSTANCE_TOKEN"] != "tok-123" {
			t.Errorf("VITE_INSTANCE_TOKEN = %q", env["VITE_INSTANCE_TOKEN"])
		}
	})

	t.Run("claude anthropic-compatible", func(t *testing.T) {
		meta := &TokenMeta{Type: ProfileTypeEndpoint, Endpoint: "https://api.z.ai/api/anthropic"}
		env, err := ProfileEnv("claude", "glm", "glm-token", meta)
		if err != nil {
			t.Fatalf("ProfileEnv error: %v", err)
		}
		if env["ANTHROPIC_BASE_URL"] != "https://api.z.ai/api/anthropic" {
			t.Errorf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
		}
		if env["ANTHROPIC_AUTH_TOKEN"] != "glm-token" {
			t.Errorf("ANTHROPIC_AUTH_TOKEN = %q", env["ANTHROPIC_AUTH_TOKEN"])
		}
		if want := filepath.Join(home, ".claude-glm"); env["CLAUDE_CONFIG_DIR"] != want {
			t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", env["CLAUDE_CONFIG_DIR"], want)
		}
		if _, ok := env["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
			t.Error("endpoint profile must not inject CLAUDE_CODE_OAUTH_TOKEN")
		}
	})

	t.Run("endpoint without URL", func(t *testing.T) {
		meta := &TokenMeta{Type: ProfileTypeEndpoint}
		if _, err := ProfileEnv("ollama", "x", "", meta); err == nil {
			t.Error("expected error for endpoint profile without URL")
		}
	})

	t.Run("unsupported endpoint tool", func(t *testing.T) {
		meta := &TokenMeta{Type: ProfileTypeEndpoint, Endpoint: "http://x"}
		if _, err := ProfileEnv("codex", "x", "tok", meta); err == nil {
			t.Error("expected error for unsupported endpoint tool")
		}
	})
}

func TestProfileEnv_TokenProfiles(t *testing.T) {
	t.Run("deepseek", func(t *testing.T) {
		env, err := ProfileEnv("deepseek", "main", "sk-0123456789abcdef01234567", nil)
		if err != nil {
			t.Fatalf("ProfileEnv error: %v", err)
		}
		if env["DEEPSEEK_API_KEY"] != "sk-0123456789abcdef01234567" {
			t.Errorf("DEEPSEEK_API_KEY = %q", env["DEEPSEEK_API_KEY"])
		}
	})

	t.Run("grok", func(t *testing.T) {
		env, err := ProfileEnv("grok", "ent", "deployment-key-abc", nil)
		if err != nil {
			t.Fatalf("ProfileEnv error: %v", err)
		}
		if env["GROK_DEPLOYMENT_KEY"] != "deployment-key-abc" {
			t.Errorf("GROK_DEPLOYMENT_KEY = %q", env["GROK_DEPLOYMENT_KEY"])
		}
	})

	t.Run("claude via TokenEnv delegate", func(t *testing.T) {
		env, err := TokenEnv("claude", "work", "sk-ant-oat01-abc")
		if err != nil {
			t.Fatalf("TokenEnv error: %v", err)
		}
		if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-abc" {
			t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
		}
	})
}

func TestValidateProfileToken(t *testing.T) {
	endpointMeta := func(endpoint string) *TokenMeta {
		return &TokenMeta{Type: ProfileTypeEndpoint, Endpoint: endpoint}
	}
	tests := []struct {
		name    string
		tool    string
		token   string
		meta    *TokenMeta
		wantErr bool
	}{
		{"claude endpoint skips sk-ant check", "claude", "glm-issued.jwt-like", endpointMeta("https://api.z.ai/api/anthropic"), false},
		{"claude endpoint empty token", "claude", "", endpointMeta("https://api.z.ai/api/anthropic"), true},
		{"claude token profile still strict", "claude", "glm-issued.jwt-like", nil, true},
		{"ollama endpoint no token ok", "ollama", "", endpointMeta("http://127.0.0.1:11434"), false},
		{"quick endpoint token required", "quick", "", endpointMeta("ws://localhost:8771"), true},
		{"quick endpoint token ok", "quick", "tok", endpointMeta("ws://localhost:8771"), false},
		{"endpoint meta without URL", "ollama", "", endpointMeta(""), true},
		{"deepseek plain token", "deepseek", "sk-0123456789abcdef01234567", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProfileToken(tt.tool, tt.token, tt.meta)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProfileToken(%s) error = %v, wantErr %v", tt.tool, err, tt.wantErr)
			}
		})
	}
}

func TestValidateTokenFormat_DeepSeek(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"plausible key", "sk-" + strings.Repeat("a1", 16), false},
		{"wrong prefix", "ds-" + strings.Repeat("a1", 16), true},
		{"too short", "sk-abc", true},
		{"empty", "  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTokenFormat("deepseek", tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTokenFormat(deepseek, %q) error = %v, wantErr %v", tt.token, err, tt.wantErr)
			}
		})
	}
}

func TestEndpointSpecFor(t *testing.T) {
	if spec, ok := EndpointSpecFor("ollama"); !ok || spec.TokenRequired || spec.DefaultEndpoint == "" {
		t.Errorf("EndpointSpecFor(ollama) = %+v, %v", spec, ok)
	}
	if spec, ok := EndpointSpecFor("quick"); !ok || !spec.TokenRequired || spec.DefaultEndpoint == "" {
		t.Errorf("EndpointSpecFor(quick) = %+v, %v", spec, ok)
	}
	if spec, ok := EndpointSpecFor("claude"); !ok || !spec.TokenRequired || spec.DefaultEndpoint != "" {
		t.Errorf("EndpointSpecFor(claude) = %+v, %v (claude must have no default endpoint)", spec, ok)
	}
	if _, ok := EndpointSpecFor("codex"); ok {
		t.Error("EndpointSpecFor(codex) = ok, want unsupported")
	}
}

func TestListTokenProfiles_IncludesEndpointProfiles(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "work", "sk-ant-oat01-abcdefghij", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := v.SaveEndpointProfile("claude", "glm", "https://api.z.ai/api/anthropic", "glm-tok", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}

	names, err := v.ListTokenProfiles("claude")
	if err != nil {
		t.Fatalf("ListTokenProfiles error: %v", err)
	}
	if len(names) != 2 {
		t.Errorf("ListTokenProfiles = %v, want both token and endpoint profiles", names)
	}
}
