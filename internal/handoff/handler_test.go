package handoff

import (
	"testing"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry() returned nil")
	}

	// Should have default handlers registered
	providers := r.Providers()
	if len(providers) < 3 {
		t.Errorf("expected at least 3 providers, got %d", len(providers))
	}

	t.Logf("[TEST] Registered providers: %v", providers)
}

func TestRegistry_Get(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		provider string
		wantNil  bool
	}{
		{"claude", false},
		{"codex", false},
		{"gemini", false},
		{"unknown", true},
	}

	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			h := r.Get(tc.provider)
			if tc.wantNil && h != nil {
				t.Errorf("Get(%q) = %v, want nil", tc.provider, h)
			}
			if !tc.wantNil && h == nil {
				t.Errorf("Get(%q) = nil, want non-nil", tc.provider)
			}
		})
	}
}

func TestGetHandler(t *testing.T) {
	// Test default registry access
	h := GetHandler("claude")
	if h == nil {
		t.Error("GetHandler(claude) returned nil")
	}

	if h.Provider() != "claude" {
		t.Errorf("Provider() = %q, want claude", h.Provider())
	}
}

func TestLoginState_String(t *testing.T) {
	tests := []struct {
		state LoginState
		want  string
	}{
		{LoginStateUnknown, "unknown"},
		{LoginStateIdle, "idle"},
		{LoginStateInProgress, "in_progress"},
		{LoginStateComplete, "complete"},
		{LoginStateFailed, "failed"},
	}

	for _, tc := range tests {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("LoginState(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestDetermineState(t *testing.T) {
	handler := &ClaudeLoginHandler{}

	tests := []struct {
		name   string
		output string
		want   LoginState
	}{
		{"idle", "Ready for input", LoginStateIdle},
		{"progress", "Opening browser for authentication...", LoginStateInProgress},
		{"complete", "Successfully logged in as user@example.com", LoginStateComplete},
		{"failed", "Authentication failed: invalid credentials", LoginStateFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DetermineState(handler, tc.output)
			if got != tc.want {
				t.Errorf("DetermineState(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestDetermineState_NilHandler(t *testing.T) {
	state := DetermineState(nil, "any output")
	if state != LoginStateUnknown {
		t.Errorf("DetermineState(nil, ...) = %v, want Unknown", state)
	}
}

// TestClaudeLoginHandler tests the Claude Code login handler.
func TestClaudeLoginHandler(t *testing.T) {
	h := &ClaudeLoginHandler{}

	t.Run("Provider", func(t *testing.T) {
		if h.Provider() != "claude" {
			t.Errorf("Provider() = %q, want claude", h.Provider())
		}
	})

	t.Run("LoginCommand", func(t *testing.T) {
		if h.LoginCommand() != "/login" {
			t.Errorf("LoginCommand() = %q, want /login", h.LoginCommand())
		}
	})

	t.Run("IsLoginInProgress", func(t *testing.T) {
		tests := []struct {
			output string
			want   bool
		}{
			{"Opening browser for authentication...", true},
			{"Please enter the device code: ABC-123", true},
			{"Waiting for authentication...", true},
			{"Ready for input", false},
			{"Successfully logged in", false},
		}

		for _, tc := range tests {
			got := h.IsLoginInProgress(tc.output)
			if got != tc.want {
				t.Errorf("IsLoginInProgress(%q) = %v, want %v", tc.output, got, tc.want)
			}
		}
	})

	t.Run("IsLoginComplete", func(t *testing.T) {
		tests := []struct {
			output string
			want   bool
		}{
			{"Successfully logged in as user@example.com", true},
			{"Authentication successful!", true},
			{"Logged in as user", true},
			{"Welcome back, user!", true},
			{"Opening browser...", false},
			{"Authentication failed", false},
		}

		for _, tc := range tests {
			got := h.IsLoginComplete(tc.output)
			if got != tc.want {
				t.Errorf("IsLoginComplete(%q) = %v, want %v", tc.output, got, tc.want)
			}
		}
	})

	t.Run("IsLoginFailed", func(t *testing.T) {
		tests := []struct {
			output  string
			wantFail bool
			wantMsg  string
		}{
			{"Authentication failed: bad credentials", true, "Authentication failed"},
			{"Login failed", true, "Login failed"},
			{"Operation timed out", true, "Login timed out"},
			{"User cancelled the operation", true, "Login cancelled"},
			{"Successfully logged in", false, ""},
			{"Ready for input", false, ""},
		}

		for _, tc := range tests {
			gotFail, gotMsg := h.IsLoginFailed(tc.output)
			if gotFail != tc.wantFail {
				t.Errorf("IsLoginFailed(%q) failed = %v, want %v", tc.output, gotFail, tc.wantFail)
			}
			if gotFail && gotMsg != tc.wantMsg {
				t.Errorf("IsLoginFailed(%q) msg = %q, want %q", tc.output, gotMsg, tc.wantMsg)
			}
		}
	})

	t.Run("ExpectedPatterns", func(t *testing.T) {
		patterns := h.ExpectedPatterns()
		if patterns["progress"] == "" {
			t.Error("missing progress pattern")
		}
		if patterns["success"] == "" {
			t.Error("missing success pattern")
		}
		if patterns["failure"] == "" {
			t.Error("missing failure pattern")
		}
	})
}

// TestCodexLoginHandler tests the Codex login handler.
func TestCodexLoginHandler(t *testing.T) {
	h := &CodexLoginHandler{}

	t.Run("Provider", func(t *testing.T) {
		if h.Provider() != "codex" {
			t.Errorf("Provider() = %q, want codex", h.Provider())
		}
	})

	t.Run("LoginCommand", func(t *testing.T) {
		if h.LoginCommand() != "codex login" {
			t.Errorf("LoginCommand() = %q, want 'codex login'", h.LoginCommand())
		}
	})

	t.Run("IsLoginInProgress", func(t *testing.T) {
		tests := []struct {
			output string
			want   bool
		}{
			{"Please log in to continue", true},
			{"Enter your API key:", true},
			{"Authenticate with OpenAI...", true},
			{"Ready", false},
			{"Logged in", false},
		}

		for _, tc := range tests {
			got := h.IsLoginInProgress(tc.output)
			if got != tc.want {
				t.Errorf("IsLoginInProgress(%q) = %v, want %v", tc.output, got, tc.want)
			}
		}
	})

	t.Run("IsLoginComplete", func(t *testing.T) {
		tests := []struct {
			output string
			want   bool
		}{
			{"Logged in successfully", true},
			{"API key saved", true},
			{"Authentication complete", true},
			{"Please login", false},
			{"Invalid API key", false},
		}

		for _, tc := range tests {
			got := h.IsLoginComplete(tc.output)
			if got != tc.want {
				t.Errorf("IsLoginComplete(%q) = %v, want %v", tc.output, got, tc.want)
			}
		}
	})

	t.Run("IsLoginFailed", func(t *testing.T) {
		tests := []struct {
			output   string
			wantFail bool
		}{
			{"Invalid API key", true},
			{"Authentication failed", true},
			{"Rate limit exceeded", true},
			{"Logged in", false},
		}

		for _, tc := range tests {
			gotFail, _ := h.IsLoginFailed(tc.output)
			if gotFail != tc.wantFail {
				t.Errorf("IsLoginFailed(%q) = %v, want %v", tc.output, gotFail, tc.wantFail)
			}
		}
	})
}

// TestGeminiLoginHandler tests the Gemini login handler.
func TestGeminiLoginHandler(t *testing.T) {
	h := &GeminiLoginHandler{}

	t.Run("Provider", func(t *testing.T) {
		if h.Provider() != "gemini" {
			t.Errorf("Provider() = %q, want gemini", h.Provider())
		}
	})

	t.Run("LoginCommand", func(t *testing.T) {
		if h.LoginCommand() != "/auth" {
			t.Errorf("LoginCommand() = %q, want /auth", h.LoginCommand())
		}
	})

	t.Run("IsLoginInProgress", func(t *testing.T) {
		tests := []struct {
			output string
			want   bool
		}{
			{"Sign in with Google to continue", true},
			{"Enter the authorization code:", true},
			{"Opening browser...", true},
			{"Ready", false},
			{"Authenticated", false},
		}

		for _, tc := range tests {
			got := h.IsLoginInProgress(tc.output)
			if got != tc.want {
				t.Errorf("IsLoginInProgress(%q) = %v, want %v", tc.output, got, tc.want)
			}
		}
	})

	t.Run("IsLoginComplete", func(t *testing.T) {
		tests := []struct {
			output string
			want   bool
		}{
			{"Successfully authenticated with Google", true},
			{"Logged in as user@gmail.com", true},
			{"Authentication complete", true},
			{"Sign in with Google", false},
			{"Access denied", false},
		}

		for _, tc := range tests {
			got := h.IsLoginComplete(tc.output)
			if got != tc.want {
				t.Errorf("IsLoginComplete(%q) = %v, want %v", tc.output, got, tc.want)
			}
		}
	})

	t.Run("IsLoginFailed", func(t *testing.T) {
		tests := []struct {
			output   string
			wantFail bool
		}{
			{"Access denied", true},
			{"Authentication failed", true},
			{"Invalid authorization code", true},
			{"Token expired", true},
			{"Authenticated", false},
		}

		for _, tc := range tests {
			gotFail, _ := h.IsLoginFailed(tc.output)
			if gotFail != tc.wantFail {
				t.Errorf("IsLoginFailed(%q) = %v, want %v", tc.output, gotFail, tc.wantFail)
			}
		}
	})
}

// TestRegistry_Register tests custom handler registration.
func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()

	// Create a custom handler for testing
	custom := &ClaudeLoginHandler{} // Reuse for simplicity

	// Override claude handler
	r.Register(custom)

	got := r.Get("claude")
	if got != custom {
		t.Error("custom handler not registered correctly")
	}
}
