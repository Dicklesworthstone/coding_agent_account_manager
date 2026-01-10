package handoff

import (
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
)

// GeminiLoginHandler handles login for Google Gemini CLI.
// Gemini uses /auth command for authentication, typically involving
// OAuth flow with Google accounts.
type GeminiLoginHandler struct{}

// Provider returns the provider ID.
func (h *GeminiLoginHandler) Provider() string {
	return "gemini"
}

// LoginCommand returns the command to trigger login.
func (h *GeminiLoginHandler) LoginCommand() string {
	return "/auth"
}

// TriggerLogin injects the login command into the PTY.
func (h *GeminiLoginHandler) TriggerLogin(ctrl pty.Controller) error {
	return ctrl.InjectCommand("/auth")
}

// IsLoginInProgress checks if a login flow has started.
// Gemini shows various messages when waiting for Google OAuth:
// - "Sign in with Google"
// - "Opening browser"
// - "Enter authorization code"
func (h *GeminiLoginHandler) IsLoginInProgress(output string) bool {
	lower := strings.ToLower(output)

	patterns := []string{
		"sign in with google",
		"google sign in",
		"google login",
		"opening browser",
		"open your browser",
		"authorization code",
		"enter the code",
		"waiting for authentication",
		"waiting for auth",
		"authenticate with google",
		"visit the url",
		"visit this url",
		"go to this url",
		"oauth",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// IsLoginComplete checks if login succeeded.
// Gemini shows success messages like:
// - "Successfully authenticated"
// - "Logged in as"
// - "Authentication complete"
func (h *GeminiLoginHandler) IsLoginComplete(output string) bool {
	lower := strings.ToLower(output)

	patterns := []string{
		"successfully authenticated",
		"authentication successful",
		"logged in as",
		"logged in successfully",
		"authentication complete",
		"auth complete",
		"signed in as",
		"welcome",
		"ready to use",
		"connected to google",
	}

	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// IsLoginFailed checks if login failed and extracts an error message.
// Gemini shows failure messages like:
// - "Authentication failed"
// - "Invalid credentials"
// - "Access denied"
func (h *GeminiLoginHandler) IsLoginFailed(output string) (bool, string) {
	lower := strings.ToLower(output)

	failurePatterns := []struct {
		pattern string
		message string
	}{
		{"authentication failed", "Authentication failed"},
		{"auth failed", "Authentication failed"},
		{"login failed", "Login failed"},
		{"invalid credentials", "Invalid credentials"},
		{"invalid code", "Invalid authorization code"},
		{"invalid authorization code", "Invalid authorization code"},
		{"access denied", "Access denied"},
		{"permission denied", "Permission denied"},
		{"unauthorized", "Unauthorized"},
		{"token expired", "Token expired"},
		{"expired token", "Token expired"},
		{"timed out", "Login timed out"},
		{"timeout", "Login timed out"},
		{"cancelled", "Login cancelled"},
		{"canceled", "Login cancelled"},
		{"quota exceeded", "Quota exceeded"},
		{"rate limit", "Rate limited"},
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
func (h *GeminiLoginHandler) ExpectedPatterns() map[string]string {
	return map[string]string{
		"progress": `(?i)(sign in with google|authorization code|waiting for|oauth)`,
		"success":  `(?i)(successfully authenticated|logged in as|authentication complete)`,
		"failure":  `(?i)(authentication failed|invalid credentials|access denied|timed out)`,
	}
}

// Ensure GeminiLoginHandler implements LoginHandler.
var _ LoginHandler = (*GeminiLoginHandler)(nil)
