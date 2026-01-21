package discovery

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAllTools(t *testing.T) {
	tools := AllTools()
	if len(tools) != 3 {
		t.Errorf("AllTools() returned %d tools, want 3", len(tools))
	}

	expected := map[Tool]bool{
		ToolClaude: true,
		ToolCodex:  true,
		ToolGemini: true,
	}

	for _, tool := range tools {
		if !expected[tool] {
			t.Errorf("Unexpected tool: %s", tool)
		}
	}
}

func TestExtractEmailFromJWT(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]interface{}
		wantEmail string
	}{
		{
			name: "email claim",
			payload: map[string]interface{}{
				"email": "user@example.com",
				"sub":   "12345",
			},
			wantEmail: "user@example.com",
		},
		{
			name: "preferred_username claim",
			payload: map[string]interface{}{
				"preferred_username": "user@domain.org",
				"sub":                "67890",
			},
			wantEmail: "user@domain.org",
		},
		{
			name: "sub with email format",
			payload: map[string]interface{}{
				"sub": "test@gmail.com",
			},
			wantEmail: "test@gmail.com",
		},
		{
			name: "sub without email format",
			payload: map[string]interface{}{
				"sub": "user-id-12345",
			},
			wantEmail: "",
		},
		{
			name:      "empty payload",
			payload:   map[string]interface{}{},
			wantEmail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build a fake JWT
			payloadBytes, _ := json.Marshal(tt.payload)
			payloadB64 := base64.URLEncoding.EncodeToString(payloadBytes)
			// Remove padding for URL encoding
			payloadB64 = trimBase64Padding(payloadB64)
			token := "eyJhbGciOiJIUzI1NiJ9." + payloadB64 + ".signature"

			got := extractEmailFromJWT(token)
			if got != tt.wantEmail {
				t.Errorf("extractEmailFromJWT() = %q, want %q", got, tt.wantEmail)
			}
		})
	}
}

func trimBase64Padding(s string) string {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	return s
}

func TestExtractEmailFromJWT_InvalidTokens(t *testing.T) {
	tests := []string{
		"",
		"not-a-jwt",
		"only.two.parts.extra",
		"invalid.base64!.signature",
	}

	for _, token := range tests {
		got := extractEmailFromJWT(token)
		if got != "" {
			t.Errorf("extractEmailFromJWT(%q) = %q, want empty", token, got)
		}
	}
}

func TestExtractClaudeIdentity(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]interface{}
		wantID    string
		wantValid bool
	}{
		{
			name: "direct email field",
			data: map[string]interface{}{
				"email": "claude@example.com",
			},
			wantID:    "claude@example.com",
			wantValid: true,
		},
		{
			name: "claudeAiOauth with email",
			data: map[string]interface{}{
				"claudeAiOauth": map[string]interface{}{
					"email":     "oauth@example.com",
					"accountId": "acc-123",
				},
			},
			wantID:    "oauth@example.com",
			wantValid: true,
		},
		{
			name: "claudeAiOauth accountId only",
			data: map[string]interface{}{
				"claudeAiOauth": map[string]interface{}{
					"accountId": "acc-456",
				},
			},
			wantID:    "acc-456",
			wantValid: true,
		},
		{
			name: "accountId fallback at root",
			data: map[string]interface{}{
				"accountId": "acc-12345",
			},
			wantID:    "acc-12345",
			wantValid: true,
		},
		{
			// Current Claude format (2026+): no email/accountId, opaque tokens.
			// JWT decoding should NOT be attempted - tokens are opaque.
			// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-002)
			name: "current format - opaque token no identity",
			data: map[string]interface{}{
				"claudeAiOauth": map[string]interface{}{
					"accessToken":      "sk-ant-oat01-opaque-token",
					"refreshToken":     "sk-ant-ort01-refresh-token",
					"expiresAt":        1737500000000,
					"subscriptionType": "claude_pro_2025",
					// NOTE: email and accountId are NOT present
				},
			},
			wantID:    "", // Empty - no identity available
			wantValid: true,
		},
		{
			// Verify we don't crash or return misleading data from fake "JWT"
			name: "opaque token that looks like JWT",
			data: map[string]interface{}{
				"claudeAiOauth": map[string]interface{}{
					"accessToken": "eyJhbGciOiJub25lIn0.eyJlbWFpbCI6ImZha2VAZXhhbXBsZS5jb20ifQ.",
				},
			},
			wantID:    "", // Should NOT decode JWT - Claude tokens are opaque
			wantValid: true,
		},
		{
			name:      "no identity info",
			data:      map[string]interface{}{},
			wantID:    "",
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.data)
			gotID, gotValid := extractClaudeIdentity(data)
			if gotID != tt.wantID {
				t.Errorf("extractClaudeIdentity() id = %q, want %q", gotID, tt.wantID)
			}
			if gotValid != tt.wantValid {
				t.Errorf("extractClaudeIdentity() valid = %v, want %v", gotValid, tt.wantValid)
			}
		})
	}
}

func TestExtractClaudeIdentity_InvalidJSON(t *testing.T) {
	_, valid := extractClaudeIdentity([]byte("not json"))
	if valid {
		t.Error("extractClaudeIdentity() should return false for invalid JSON")
	}
}

func TestExtractCodexIdentity(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]interface{}
		wantID    string
		wantValid bool
	}{
		{
			name: "user object with email",
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"email": "codex@openai.com",
					"name":  "Test User",
				},
			},
			wantID:    "codex@openai.com",
			wantValid: true,
		},
		{
			name: "user object with name only",
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"name": "Test User",
				},
			},
			wantID:    "Test User",
			wantValid: true,
		},
		{
			name: "direct email field",
			data: map[string]interface{}{
				"email": "direct@email.com",
			},
			wantID:    "direct@email.com",
			wantValid: true,
		},
		{
			name: "user_id fallback",
			data: map[string]interface{}{
				"user_id": "uid-67890",
			},
			wantID:    "uid-67890",
			wantValid: true,
		},
		{
			name:      "no identity info",
			data:      map[string]interface{}{},
			wantID:    "",
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.data)
			gotID, gotValid := extractCodexIdentity(data)
			if gotID != tt.wantID {
				t.Errorf("extractCodexIdentity() id = %q, want %q", gotID, tt.wantID)
			}
			if gotValid != tt.wantValid {
				t.Errorf("extractCodexIdentity() valid = %v, want %v", gotValid, tt.wantValid)
			}
		})
	}
}

func TestExtractGeminiIdentity(t *testing.T) {
	tests := []struct {
		name      string
		data      map[string]interface{}
		wantID    string
		wantValid bool
	}{
		{
			name: "account object with email",
			data: map[string]interface{}{
				"account": map[string]interface{}{
					"email": "gemini@google.com",
				},
			},
			wantID:    "gemini@google.com",
			wantValid: true,
		},
		{
			name: "user object with email",
			data: map[string]interface{}{
				"user": map[string]interface{}{
					"email": "user@gmail.com",
				},
			},
			wantID:    "user@gmail.com",
			wantValid: true,
		},
		{
			name: "google_account_id fallback",
			data: map[string]interface{}{
				"google_account_id": "1234567890",
			},
			wantID:    "1234567890",
			wantValid: true,
		},
		{
			name:      "no identity info",
			data:      map[string]interface{}{},
			wantID:    "",
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.data)
			gotID, gotValid := extractGeminiIdentity(data)
			if gotID != tt.wantID {
				t.Errorf("extractGeminiIdentity() id = %q, want %q", gotID, tt.wantID)
			}
			if gotValid != tt.wantValid {
				t.Errorf("extractGeminiIdentity() valid = %v, want %v", gotValid, tt.wantValid)
			}
		})
	}
}

func TestExtractIdentity_FileNotFound(t *testing.T) {
	id, valid := ExtractIdentity(ToolClaude, "/nonexistent/path/auth.json")
	if id != "" || valid {
		t.Errorf("ExtractIdentity() for nonexistent file = (%q, %v), want (\"\", false)", id, valid)
	}
}

func TestExtractIdentity_RealFile(t *testing.T) {
	// Create a temp file with auth data
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")

	authData := map[string]interface{}{
		"user": map[string]interface{}{
			"email": "test@example.com",
		},
	}
	data, _ := json.Marshal(authData)
	if err := os.WriteFile(authPath, data, 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	id, valid := ExtractIdentity(ToolCodex, authPath)
	if id != "test@example.com" {
		t.Errorf("ExtractIdentity() id = %q, want \"test@example.com\"", id)
	}
	if !valid {
		t.Error("ExtractIdentity() valid = false, want true")
	}
}

func TestScanTool_NoAuthFile(t *testing.T) {
	// This test relies on the current system not having auth files
	// at non-standard locations. We can't easily mock file system access
	// without significant changes, so this is a basic sanity check.
	result := ScanTool("nonexistent")
	if result != nil {
		t.Error("ScanTool() for nonexistent tool should return nil")
	}
}

func TestGetToolPaths(t *testing.T) {
	tests := []struct {
		tool     Tool
		minPaths int
	}{
		{ToolClaude, 1},
		{ToolCodex, 1},
		{ToolGemini, 1},
	}

	for _, tt := range tests {
		t.Run(string(tt.tool), func(t *testing.T) {
			paths := getToolPaths(tt.tool)
			if len(paths) < tt.minPaths {
				t.Errorf("getToolPaths(%s) returned %d paths, want at least %d", tt.tool, len(paths), tt.minPaths)
			}
		})
	}
}

func TestGetToolPaths_UnknownTool(t *testing.T) {
	paths := getToolPaths("unknown")
	if paths != nil {
		t.Errorf("getToolPaths(unknown) = %v, want nil", paths)
	}
}

func TestScan(t *testing.T) {
	// Basic test that Scan() returns a valid result structure
	result := Scan()

	if result == nil {
		t.Fatal("Scan() returned nil")
	}

	if result.ToolPaths == nil {
		t.Error("Scan() returned nil ToolPaths")
	}

	// Should have entries for all tools
	for _, tool := range AllTools() {
		if _, ok := result.ToolPaths[tool]; !ok {
			t.Errorf("Scan() missing ToolPaths for %s", tool)
		}
	}

	// Total of found + notFound should equal all tools
	if len(result.Found)+len(result.NotFound) != len(AllTools()) {
		t.Errorf("Scan() found=%d + notFound=%d != allTools=%d",
			len(result.Found), len(result.NotFound), len(AllTools()))
	}
}
