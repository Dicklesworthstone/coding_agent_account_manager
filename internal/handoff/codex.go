package handoff

import (
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
)

// CodexLoginHandler handles login for OpenAI Codex CLI.
// Codex may already be running and authenticated, or may need
// the "codex login" command for re-authentication.
type CodexLoginHandler struct{}

// Provider returns the provider ID.
func (h *CodexLoginHandler) Provider() string {
	return "codex"
}

// LoginCommand returns the command to trigger login.
// Note: Codex CLI may use different commands depending on version.
func (h *CodexLoginHandler) LoginCommand() string {
	return "codex login"
}

// TriggerLogin injects the login command into the PTY.
func (h *CodexLoginHandler) TriggerLogin(ctrl pty.Controller) error {
	return ctrl.InjectCommand("codex login")
}

// IsLoginInProgress checks if a login flow has started.
// Codex shows various messages when waiting for authentication:
// - "Please log in" patterns
// - API key prompts
// - Browser-based auth prompts
func (h *CodexLoginHandler) IsLoginInProgress(output string) bool {
	lower := strings.ToLower(output)

	patterns := []string{
		"please log in",
		"please login",
		"opening browser",
		"enter your api key",
		"api key:",
		"enter api key",
		"waiting for login",
		"authenticate with openai",
		"sign in to openai",
		"visit openai",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// IsLoginComplete checks if login succeeded.
// Codex shows success messages like:
// - "Logged in"
// - "API key saved"
// - "Authentication complete"
func (h *CodexLoginHandler) IsLoginComplete(output string) bool {
	lower := strings.ToLower(output)

	patterns := []string{
		"logged in",
		"login successful",
		"api key saved",
		"api key valid",
		"authentication complete",
		"authenticated successfully",
		"successfully authenticated",
		"connected to openai",
		"ready to use",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// IsLoginFailed checks if login failed and extracts an error message.
// Codex shows failure messages like:
// - "Invalid API key"
// - "Authentication failed"
// - "Rate limited"
func (h *CodexLoginHandler) IsLoginFailed(output string) (bool, string) {
	lower := strings.ToLower(output)

	failurePatterns := []struct {
		pattern string
		message string
	}{
		{"invalid api key", "Invalid API key"},
		{"api key invalid", "Invalid API key"},
		{"authentication failed", "Authentication failed"},
		{"auth failed", "Authentication failed"},
		{"login failed", "Login failed"},
		{"incorrect api key", "Incorrect API key"},
		{"unauthorized", "Unauthorized"},
		{"permission denied", "Permission denied"},
		{"access denied", "Access denied"},
		{"rate limit", "Rate limited"},
		{"quota exceeded", "Quota exceeded"},
		{"timed out", "Login timed out"},
		{"timeout", "Login timed out"},
		{"cancelled", "Login cancelled"},
		{"canceled", "Login cancelled"},
		{"connection refused", "Connection refused"},
		{"network error", "Network error"},
	}

	for _, fp := range failurePatterns {
		if strings.Contains(lower, fp.pattern) {
			return true, fp.message
		}
	}

	return false, ""
}

// ExpectedPatterns returns regex patterns for login states.
func (h *CodexLoginHandler) ExpectedPatterns() map[string]string {
	return map[string]string{
		"progress": `(?i)(please log ?in|api key:|waiting for|authenticate with openai)`,
		"success":  `(?i)(logged in|api key saved|authentication complete)`,
		"failure":  `(?i)(invalid api key|authentication failed|unauthorized|rate limit)`,
	}
}

// Ensure CodexLoginHandler implements LoginHandler.
var _ LoginHandler = (*CodexLoginHandler)(nil)
