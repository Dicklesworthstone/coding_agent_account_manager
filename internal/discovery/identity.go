package discovery

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
)

// ExtractIdentity attempts to extract user identity (email) from an auth file.
// Returns the identity string and whether extraction was successful.
func ExtractIdentity(tool Tool, authPath string) (string, bool) {
	data, err := os.ReadFile(authPath)
	if err != nil {
		return "", false
	}

	switch tool {
	case ToolClaude:
		return extractClaudeIdentity(data)
	case ToolCodex:
		return extractCodexIdentity(data)
	case ToolGemini:
		return extractGeminiIdentity(data)
	default:
		return "", false
	}
}

// extractClaudeIdentity parses Claude's .claude.json for identity info.
//
// IMPORTANT: Claude tokens are OPAQUE, not JWTs. Do not attempt to decode them.
// Current Claude auth files do NOT contain email or accountId fields.
// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-002)
func extractClaudeIdentity(data []byte) (string, bool) {
	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", false
	}

	// Check for direct email field (rare, but possible in older versions)
	if email, ok := auth["email"].(string); ok && email != "" {
		return email, true
	}

	// Check claudeAiOauth for identity fields
	if oauth, ok := auth["claudeAiOauth"].(map[string]interface{}); ok {
		// Check for email (no longer present in current versions)
		if email, ok := oauth["email"].(string); ok && email != "" {
			return email, true
		}
		// Check for accountId (no longer present in current versions)
		if accountID, ok := oauth["accountId"].(string); ok && accountID != "" {
			return accountID, true
		}
	}

	// Check for account ID at root level as fallback
	if accountID, ok := auth["accountId"].(string); ok && accountID != "" {
		return accountID, true
	}

	// NOTE: We intentionally do NOT attempt JWT decoding here.
	// Claude access tokens are opaque strings, not JWTs.
	// Attempting to decode them would fail silently and waste cycles.

	// Valid auth file but no identity available (expected for current Claude versions)
	return "", true
}

// extractCodexIdentity parses Codex's auth.json for identity info.
func extractCodexIdentity(data []byte) (string, bool) {
	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", false
	}

	// Check for user object with email (common pattern)
	if user, ok := auth["user"].(map[string]interface{}); ok {
		if email, ok := user["email"].(string); ok && email != "" {
			return email, true
		}
		if name, ok := user["name"].(string); ok && name != "" {
			return name, true
		}
	}

	// Check for direct email field
	if email, ok := auth["email"].(string); ok && email != "" {
		return email, true
	}

	// Try to decode JWT tokens
	tokenFields := []string{"access_token", "accessToken", "token", "id_token"}
	for _, field := range tokenFields {
		if token, ok := auth[field].(string); ok && token != "" {
			if email := extractEmailFromJWT(token); email != "" {
				return email, true
			}
		}
	}

	// Check for account/user ID as fallback
	if userID, ok := auth["user_id"].(string); ok && userID != "" {
		return userID, true
	}

	// Valid auth file but couldn't extract identity
	return "", true
}

// extractGeminiIdentity parses Gemini's settings.json for identity info.
func extractGeminiIdentity(data []byte) (string, bool) {
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", false
	}

	// Check for account object with email
	if account, ok := settings["account"].(map[string]interface{}); ok {
		if email, ok := account["email"].(string); ok && email != "" {
			return email, true
		}
	}

	// Check for user object
	if user, ok := settings["user"].(map[string]interface{}); ok {
		if email, ok := user["email"].(string); ok && email != "" {
			return email, true
		}
	}

	// Check for direct email field
	if email, ok := settings["email"].(string); ok && email != "" {
		return email, true
	}

	// Check for Google account ID
	if googleID, ok := settings["google_account_id"].(string); ok && googleID != "" {
		return googleID, true
	}

	// Valid settings file but couldn't extract identity
	return "", true
}

// extractEmailFromJWT attempts to decode a JWT and extract email claim.
// Returns empty string if the token is not a valid JWT or has no email.
func extractEmailFromJWT(token string) string {
	// JWT format: header.payload.signature
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}

	// Decode the payload (second part)
	payload := parts[1]

	// Add padding if needed
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Try standard encoding
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return ""
		}
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}

	// Check common email claim names
	emailFields := []string{"email", "preferred_username", "sub", "upn"}
	for _, field := range emailFields {
		if email, ok := claims[field].(string); ok && email != "" {
			// Validate it looks like an email for 'sub' field
			if field == "sub" && !strings.Contains(email, "@") {
				continue
			}
			return email
		}
	}

	return ""
}
