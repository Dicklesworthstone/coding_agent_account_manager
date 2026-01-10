package handoff

import (
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
)

// ClaudeLoginHandler handles login for Claude Code CLI.
// Claude Code uses /login command and supports browser-based OAuth
// and device code authentication flows.
type ClaudeLoginHandler struct{}

// Provider returns the provider ID.
func (h *ClaudeLoginHandler) Provider() string {
	return "claude"
}

// LoginCommand returns the command to trigger login.
func (h *ClaudeLoginHandler) LoginCommand() string {
	return "/login"
}

// TriggerLogin injects the login command into the PTY.
func (h *ClaudeLoginHandler) TriggerLogin(ctrl pty.Controller) error {
	return ctrl.InjectCommand("/login")
}

// IsLoginInProgress checks if a login flow has started.
// Claude shows various messages when waiting for authentication:
// - "Opening browser..." (browser auth)
// - "Enter the code" / "device code" (device code flow)
// - "Waiting for authentication" / "waiting for" patterns
func (h *ClaudeLoginHandler) IsLoginInProgress(output string) bool {
	lower := strings.ToLower(output)

	patterns := []string{
		"opening browser",
		"open your browser",
		"device code",
		"enter the code",
		"waiting for authentication",
		"waiting for auth",
		"please authenticate",
		"visit the url",
		"visit this url",
		"authorize this device",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// IsLoginComplete checks if login succeeded.
// Claude shows success messages like:
// - "Successfully logged in"
// - "Authentication successful"
// - "Logged in as"
// - "Welcome back"
func (h *ClaudeLoginHandler) IsLoginComplete(output string) bool {
	lower := strings.ToLower(output)

	patterns := []string{
		"successfully logged in",
		"logged in successfully",
		"authentication successful",
		"successfully authenticated",
		"logged in as",
		"welcome back",
		"login successful",
		"auth complete",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// IsLoginFailed checks if login failed and extracts an error message.
// Claude shows failure messages like:
// - "Authentication failed"
// - "Login failed"
// - "Invalid credentials"
// - "Timed out" / "timeout"
// - "Cancelled" / "canceled"
func (h *ClaudeLoginHandler) IsLoginFailed(output string) (bool, string) {
	lower := strings.ToLower(output)

	failurePatterns := []struct {
		pattern string
		message string
	}{
		{"authentication failed", "Authentication failed"},
		{"auth failed", "Authentication failed"},
		{"login failed", "Login failed"},
		{"invalid credentials", "Invalid credentials"},
		{"invalid token", "Invalid token"},
		{"expired token", "Token expired"},
		{"timed out", "Login timed out"},
		{"timeout", "Login timed out"},
		{"cancelled", "Login cancelled"},
		{"canceled", "Login cancelled"},
		{"access denied", "Access denied"},
		{"permission denied", "Permission denied"},
		{"unauthorized", "Unauthorized"},
		{"rate limit", "Rate limited"},
	}

	for _, fp := range failurePatterns {
		if strings.Contains(lower, fp.pattern) {
			return true, fp.message
		}
	}

	return false, ""
}

// ExpectedPatterns returns regex patterns for login states.
func (h *ClaudeLoginHandler) ExpectedPatterns() map[string]string {
	return map[string]string{
		"progress": `(?i)(opening browser|device code|waiting for|please authenticate)`,
		"success":  `(?i)(successfully logged in|authentication successful|logged in as)`,
		"failure":  `(?i)(authentication failed|login failed|timed out|cancelled)`,
	}
}

// Ensure ClaudeLoginHandler implements LoginHandler.
var _ LoginHandler = (*ClaudeLoginHandler)(nil)
