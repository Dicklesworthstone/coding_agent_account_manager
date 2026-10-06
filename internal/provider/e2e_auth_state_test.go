package provider_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/gemini"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// =============================================================================
// E2E Tests: Provider Authentication State
// =============================================================================

// TestE2E_ClaudeAuthState tests Claude provider authentication state detection
func TestE2E_ClaudeAuthState(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	// Create profile structure
	profileDir := h.SubDir("profiles/claude/test")
	homeDir := filepath.Join(profileDir, "home")
	xdgConfigDir := filepath.Join(profileDir, "xdg_config")

	if err := os.MkdirAll(homeDir, 0700); err != nil {
		t.Fatalf("Failed to create home dir: %v", err)
	}
	if err := os.MkdirAll(xdgConfigDir, 0700); err != nil {
		t.Fatalf("Failed to create xdg_config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(xdgConfigDir, "claude-code"), 0700); err != nil {
		t.Fatalf("Failed to create claude-code dir: %v", err)
	}

	prof := &profile.Profile{
		Name:     "test",
		Provider: "claude",
		AuthMode: "oauth",
		BasePath: profileDir,
	}

	prov := claude.New()
	ctx := context.Background()

	h.Log.SetStep("test_no_auth")

	// Test status with no auth files
	status, err := prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if status.LoggedIn {
		t.Errorf("Expected LoggedIn=false with no auth files")
	}
	if status.HasLockFile {
		t.Errorf("Expected HasLockFile=false")
	}

	h.Log.Info("No auth: LoggedIn correctly false")

	h.Log.SetStep("test_credentials_json")

	// Create .claude/.credentials.json (primary OAuth credentials)
	claudeDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatalf("Failed to create .claude dir: %v", err)
	}
	credentialsPath := filepath.Join(claudeDir, ".credentials.json")
	credentialsContent := `{
		"claudeAiOauth": {
			"accessToken": "test-access-token-abc",
			"refreshToken": "test-refresh-token-def",
			"expiresAt": 4102444800000
		}
	}`
	if err := os.WriteFile(credentialsPath, []byte(credentialsContent), 0600); err != nil {
		t.Fatalf("Failed to write .credentials.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with .credentials.json")
	}

	h.Log.Info("With .credentials.json: LoggedIn correctly true")

	// Remove credentials before testing legacy paths
	os.Remove(credentialsPath)

	h.Log.SetStep("test_claude_json")

	// A retained workflow/identity document alone must not count as a login.
	claudeJsonPath := filepath.Join(homeDir, ".claude.json")
	if err := os.WriteFile(claudeJsonPath, []byte(`{"model":"opus","oauthAccount":{"emailAddress":"profile@example.test"}}`), 0600); err != nil {
		t.Fatalf("Failed to write policy-only .claude.json: %v", err)
	}
	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.LoggedIn {
		t.Error("Expected LoggedIn=false with only workflow policy and account identity")
	}

	// Create legacy .claude.json with a supported session credential.
	claudeJsonContent := `{
		"sessionKey": "test-session-token-12345",
		"expires_at": "2099-12-31T23:59:59Z"
	}`
	if err := os.WriteFile(claudeJsonPath, []byte(claudeJsonContent), 0600); err != nil {
		t.Fatalf("Failed to write .claude.json: %v", err)
	}

	// Test status with .claude.json only
	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with .claude.json")
	}
	env, err := prov.Env(ctx, prof)
	if err != nil || env["CLAUDE_CONFIG_DIR"] != claudeDir {
		t.Fatalf("Legacy session must select its native directory: env=%v, error=%v", env, err)
	}

	h.Log.Info("With .claude.json: LoggedIn correctly true")

	h.Log.SetStep("test_auth_json")

	// Remove .claude.json, add auth.json
	os.Remove(claudeJsonPath)

	authJsonPath := filepath.Join(xdgConfigDir, "claude-code", "auth.json")
	authJsonContent := `{
		"access_token": "test-access-token",
		"token_type": "Bearer"
	}`
	if err := os.WriteFile(authJsonPath, []byte(authJsonContent), 0600); err != nil {
		t.Fatalf("Failed to write auth.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with auth.json")
	}
	env, err = prov.Env(ctx, prof)
	if err != nil || env["CLAUDE_CONFIG_DIR"] != filepath.Dir(authJsonPath) {
		t.Fatalf("XDG authentication must select its native directory: env=%v, error=%v", env, err)
	}

	h.Log.Info("With auth.json: LoggedIn correctly true")

	h.Log.SetStep("test_both_files")

	// Add .claude.json back (both files present)
	if err := os.WriteFile(claudeJsonPath, []byte(claudeJsonContent), 0600); err != nil {
		t.Fatalf("Failed to write .claude.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with both auth files")
	}

	h.Log.Info("With both files: LoggedIn correctly true")

	h.Log.SetStep("test_lock_detection")

	// Create lock file
	lockPath := filepath.Join(profileDir, ".lock")
	if err := os.WriteFile(lockPath, []byte(`{"pid": 12345}`), 0600); err != nil {
		t.Fatalf("Failed to write lock file: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.HasLockFile {
		t.Errorf("Expected HasLockFile=true with lock file present")
	}

	h.Log.Info("Lock detection: HasLockFile correctly true")

	h.Log.Info("Claude auth state tests complete")
}

// TestE2E_CodexAuthState tests Codex provider authentication state detection
func TestE2E_CodexAuthState(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	// Create profile structure
	profileDir := h.SubDir("profiles/codex/test")
	codexHomeDir := filepath.Join(profileDir, "codex_home")

	if err := os.MkdirAll(codexHomeDir, 0700); err != nil {
		t.Fatalf("Failed to create codex_home dir: %v", err)
	}

	prof := &profile.Profile{
		Name:     "test",
		Provider: "codex",
		AuthMode: "oauth",
		BasePath: profileDir,
	}

	prov := codex.New()
	ctx := context.Background()

	h.Log.SetStep("test_no_auth")

	// Test status with no auth files
	status, err := prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if status.LoggedIn {
		t.Errorf("Expected LoggedIn=false with no auth files")
	}

	h.Log.Info("No auth: LoggedIn correctly false")

	h.Log.SetStep("test_valid_auth")

	// Create auth.json
	authPath := filepath.Join(codexHomeDir, "auth.json")
	authContent := `{
		"access_token": "test-codex-token",
		"refresh_token": "test-codex-refresh",
		"token_type": "Bearer",
		"expires_in": 3600
	}`
	if err := os.WriteFile(authPath, []byte(authContent), 0600); err != nil {
		t.Fatalf("Failed to write auth.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with auth.json")
	}

	h.Log.Info("With auth.json: LoggedIn correctly true")

	h.Log.SetStep("test_empty_auth")

	// Test with empty auth file
	if err := os.WriteFile(authPath, []byte(""), 0600); err != nil {
		t.Fatalf("Failed to write empty auth.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	// File exists so LoggedIn should be true (content not validated)
	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true even with empty auth.json (file exists)")
	}

	h.Log.Info("With empty auth.json: LoggedIn correctly true (file exists)")

	h.Log.SetStep("test_corrupted_auth")

	// Test with corrupted auth file (invalid JSON)
	if err := os.WriteFile(authPath, []byte("not valid json{{{"), 0600); err != nil {
		t.Fatalf("Failed to write corrupted auth.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	// File exists so LoggedIn should be true (content validation is caller's responsibility)
	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with corrupted auth.json (file exists)")
	}

	h.Log.Info("With corrupted auth.json: LoggedIn correctly true (file exists)")

	h.Log.Info("Codex auth state tests complete")
}

// TestE2E_GeminiAuthState tests Gemini provider authentication state detection
func TestE2E_GeminiAuthState(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	// Create profile structure
	profileDir := h.SubDir("profiles/gemini/test")
	homeDir := filepath.Join(profileDir, "home")
	geminiDir := filepath.Join(homeDir, ".gemini")

	if err := os.MkdirAll(geminiDir, 0700); err != nil {
		t.Fatalf("Failed to create .gemini dir: %v", err)
	}

	prof := &profile.Profile{
		Name:     "test",
		Provider: "gemini",
		AuthMode: "oauth",
		BasePath: profileDir,
	}

	prov := gemini.New()
	ctx := context.Background()

	h.Log.SetStep("test_no_auth")

	// Test status with no auth files
	status, err := prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if status.LoggedIn {
		t.Errorf("Expected LoggedIn=false with no auth files")
	}

	h.Log.Info("No auth: LoggedIn correctly false")

	h.Log.SetStep("test_oauth_config")

	// For OAuth mode, Gemini provider Status() checks settings.json
	settingsPath := filepath.Join(geminiDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"oauth": {}}`), 0600); err != nil {
		t.Fatalf("Failed to write settings.json: %v", err)
	}

	status, err = prov.Status(ctx, prof)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with settings.json")
	}

	h.Log.Info("With settings.json: LoggedIn correctly true")

	h.Log.SetStep("test_api_key_mode")

	// Test API key mode
	apiKeyProf := &profile.Profile{
		Name:     "test-apikey",
		Provider: "gemini",
		AuthMode: string(provider.AuthModeAPIKey),
		BasePath: profileDir,
	}

	// API key mode checks for .env file
	envPath := filepath.Join(geminiDir, ".env")
	envContent := "GEMINI_API_KEY=test-api-key-12345"
	if err := os.WriteFile(envPath, []byte(envContent), 0600); err != nil {
		t.Fatalf("Failed to write .env: %v", err)
	}

	status, err = prov.Status(ctx, apiKeyProf)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	if !status.LoggedIn {
		t.Errorf("Expected LoggedIn=true with .env file for API key mode")
	}

	h.Log.Info("With .env file (API key mode): LoggedIn correctly true")

	h.Log.Info("Gemini auth state tests complete")
}

// TestE2E_ProviderLogout tests logout functionality across providers
func TestE2E_ProviderLogout(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup_claude")

	// Set up Claude profile
	claudeProfileDir := h.SubDir("profiles/claude/logout-test")
	claudeHomeDir := filepath.Join(claudeProfileDir, "home")
	claudeXdgConfig := filepath.Join(claudeProfileDir, "xdg_config")

	for _, dir := range []string{
		claudeHomeDir,
		filepath.Join(claudeXdgConfig, "claude-code"),
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("Failed to create dir: %v", err)
		}
	}

	// Create Claude auth files
	claudeJsonPath := filepath.Join(claudeHomeDir, ".claude.json")
	authJsonPath := filepath.Join(claudeXdgConfig, "claude-code", "auth.json")
	settingsPath := filepath.Join(claudeXdgConfig, "claude-code", "settings.json")

	if err := os.WriteFile(claudeJsonPath, []byte(`{"sessionKey":"test-session","oauthAccount":{"accountUuid":"test-account"},"model":"opus","mcpServers":{"workflow":{"command":"synthetic-mcp"}}}`), 0600); err != nil {
		t.Fatalf("Failed to write .claude.json: %v", err)
	}
	if err := os.WriteFile(authJsonPath, []byte(`{"access_token": "test"}`), 0600); err != nil {
		t.Fatalf("Failed to write auth.json: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"apiKeyHelper":"synthetic-helper","env":{"ANTHROPIC_API_KEY":"synthetic-key"},"permissions":{"deny":["Bash(rm *)"]}}`), 0600); err != nil {
		t.Fatalf("Failed to write settings.json: %v", err)
	}

	claudeProf := &profile.Profile{
		Name:     "logout-test",
		Provider: "claude",
		AuthMode: "oauth",
		BasePath: claudeProfileDir,
	}

	claudeProv := claude.New()
	ctx := context.Background()

	h.Log.SetStep("test_claude_logout")

	// Verify logged in
	status, _ := claudeProv.Status(ctx, claudeProf)
	if !status.LoggedIn {
		t.Errorf("Expected Claude to be logged in before logout")
	}

	// Logout
	if err := claudeProv.Logout(ctx, claudeProf); err != nil {
		t.Fatalf("Claude logout failed: %v", err)
	}

	// Verify logged out
	status, _ = claudeProv.Status(ctx, claudeProf)
	if status.LoggedIn {
		t.Errorf("Expected Claude to be logged out after logout")
	}

	// Mixed settings retain workflow policy while account fields are removed.
	for _, path := range []string{claudeJsonPath, settingsPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Logout removed workflow policy at %s: %v", path, err)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("Invalid retained policy at %s: %v", path, err)
		}
		if document["sessionKey"] != nil || document["oauthAccount"] != nil || document["apiKeyHelper"] != nil {
			t.Errorf("Logout retained account fields at %s: %v", path, document)
		}
		if path == claudeJsonPath {
			wantMCP := map[string]any{"workflow": map[string]any{"command": "synthetic-mcp"}}
			if document["model"] != "opus" || !reflect.DeepEqual(document["mcpServers"], wantMCP) {
				t.Errorf("Logout changed legacy workflow policy: %v", document)
			}
		} else {
			if env, ok := document["env"].(map[string]any); ok && env["ANTHROPIC_API_KEY"] != nil {
				t.Errorf("Logout retained settings API key: %v", document)
			}
			wantPermissions := map[string]any{"deny": []any{"Bash(rm *)"}}
			if !reflect.DeepEqual(document["permissions"], wantPermissions) {
				t.Errorf("Logout changed workflow permissions: %v", document)
			}
		}
	}
	if !h.FileNotExists(authJsonPath) {
		t.Errorf("auth.json should be removed after logout")
	}

	h.Log.Info("Claude logout: authentication removed and workflow policy retained")

	h.Log.SetStep("setup_codex")

	// Set up Codex profile
	codexProfileDir := h.SubDir("profiles/codex/logout-test")
	codexHomeDir := filepath.Join(codexProfileDir, "codex_home")

	if err := os.MkdirAll(codexHomeDir, 0700); err != nil {
		t.Fatalf("Failed to create codex_home: %v", err)
	}

	codexAuthPath := filepath.Join(codexHomeDir, "auth.json")
	if err := os.WriteFile(codexAuthPath, []byte(`{"access_token": "test"}`), 0600); err != nil {
		t.Fatalf("Failed to write codex auth.json: %v", err)
	}

	codexProf := &profile.Profile{
		Name:     "logout-test",
		Provider: "codex",
		AuthMode: "oauth",
		BasePath: codexProfileDir,
	}

	codexProv := codex.New()

	h.Log.SetStep("test_codex_logout")

	// Verify logged in
	status, _ = codexProv.Status(ctx, codexProf)
	if !status.LoggedIn {
		t.Errorf("Expected Codex to be logged in before logout")
	}

	// Logout
	if err := codexProv.Logout(ctx, codexProf); err != nil {
		t.Fatalf("Codex logout failed: %v", err)
	}

	// Verify logged out
	status, _ = codexProv.Status(ctx, codexProf)
	if status.LoggedIn {
		t.Errorf("Expected Codex to be logged out after logout")
	}

	h.Log.Info("Codex logout: auth correctly removed")

	h.Log.Info("Provider logout tests complete")
}

// TestE2E_ProviderEnvIsolation tests environment variable isolation
func TestE2E_ProviderEnvIsolation(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	// Create profiles for each provider
	profilesDir := h.SubDir("profiles")

	providers := []struct {
		id       string
		provider provider.Provider
	}{
		{"claude", claude.New()},
		{"codex", codex.New()},
		{"gemini", gemini.New()},
	}

	ctx := context.Background()

	for _, p := range providers {
		h.Log.SetStep("test_" + p.id)

		profDir := filepath.Join(profilesDir, p.id, "env-test")
		homeDir := filepath.Join(profDir, "home")
		xdgConfig := filepath.Join(profDir, "xdg_config")
		codexHome := filepath.Join(profDir, "codex_home")

		for _, dir := range []string{homeDir, xdgConfig, codexHome} {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatalf("Failed to create dir for %s: %v", p.id, err)
			}
		}

		prof := &profile.Profile{
			Name:     "env-test",
			Provider: p.id,
			AuthMode: "oauth",
			BasePath: profDir,
		}

		env, err := p.provider.Env(ctx, prof)
		if err != nil {
			t.Fatalf("%s Env() failed: %v", p.id, err)
		}

		h.Log.Info("Got environment variables", map[string]interface{}{
			"provider": p.id,
			"env":      env,
		})

		// Verify HOME is set to profile directory
		if home, ok := env["HOME"]; ok {
			if home != homeDir {
				t.Errorf("%s: expected HOME=%s, got %s", p.id, homeDir, home)
			}
		}

		// Provider-specific checks
		switch p.id {
		case "claude":
			if xdg, ok := env["XDG_CONFIG_HOME"]; ok {
				if xdg != xdgConfig {
					t.Errorf("claude: expected XDG_CONFIG_HOME=%s, got %s", xdgConfig, xdg)
				}
			}
			if cfg, ok := env["CLAUDE_CONFIG_DIR"]; ok {
				expected := filepath.Join(xdgConfig, "claude-code")
				if cfg != expected {
					t.Errorf("claude: expected CLAUDE_CONFIG_DIR=%s, got %s", expected, cfg)
				}
			}
		case "codex":
			if ch, ok := env["CODEX_HOME"]; ok {
				if ch != codexHome {
					t.Errorf("codex: expected CODEX_HOME=%s, got %s", codexHome, ch)
				}
			}
		case "gemini":
			if gh, ok := env["GEMINI_HOME"]; ok {
				expectedGemini := filepath.Join(homeDir, ".gemini")
				if gh != expectedGemini {
					t.Errorf("gemini: expected GEMINI_HOME=%s, got %s", expectedGemini, gh)
				}
			}
		}
	}

	h.Log.Info("Provider env isolation tests complete")
}

// TestE2E_ProviderIdentity tests provider identity methods
func TestE2E_ProviderIdentity(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_identities")

	providers := []struct {
		prov        provider.Provider
		expectedID  string
		expectedBin string
	}{
		{claude.New(), "claude", "claude"},
		{codex.New(), "codex", "codex"},
		{gemini.New(), "gemini", "gemini"},
	}

	for _, p := range providers {
		if p.prov.ID() != p.expectedID {
			t.Errorf("Expected ID %q, got %q", p.expectedID, p.prov.ID())
		}
		if p.prov.DefaultBin() != p.expectedBin {
			t.Errorf("Expected DefaultBin %q, got %q", p.expectedBin, p.prov.DefaultBin())
		}
		if p.prov.DisplayName() == "" {
			t.Errorf("DisplayName should not be empty for %s", p.expectedID)
		}

		authModes := p.prov.SupportedAuthModes()
		if len(authModes) == 0 {
			t.Errorf("SupportedAuthModes should return at least one mode for %s", p.expectedID)
		}

		authFiles := p.prov.AuthFiles()
		if len(authFiles) == 0 {
			t.Errorf("AuthFiles should return at least one spec for %s", p.expectedID)
		}

		h.Log.Info("Provider identity verified", map[string]interface{}{
			"id":          p.prov.ID(),
			"displayName": p.prov.DisplayName(),
			"defaultBin":  p.prov.DefaultBin(),
			"authModes":   len(authModes),
			"authFiles":   len(authFiles),
		})
	}

	h.Log.Info("Provider identity tests complete")
}

// TestE2E_RegistryOperations tests provider registry functionality
func TestE2E_RegistryOperations(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_registry")

	// Create registry
	registry := provider.NewRegistry()

	// Register providers
	registry.Register(claude.New())
	registry.Register(codex.New())
	registry.Register(gemini.New())

	// Test Get
	for _, id := range []string{"claude", "codex", "gemini"} {
		prov, ok := registry.Get(id)
		if !ok {
			t.Errorf("Registry.Get(%q) should return provider", id)
		}
		if prov.ID() != id {
			t.Errorf("Expected provider ID %q, got %q", id, prov.ID())
		}
	}

	// Test Get unknown
	_, ok := registry.Get("unknown")
	if ok {
		t.Errorf("Registry.Get('unknown') should return false")
	}

	// Test All
	all := registry.All()
	if len(all) != 3 {
		t.Errorf("Expected 3 providers, got %d", len(all))
	}

	// Test IDs
	ids := registry.IDs()
	if len(ids) != 3 {
		t.Errorf("Expected 3 IDs, got %d", len(ids))
	}

	h.Log.Info("Registry operations verified", map[string]interface{}{
		"provider_count": len(all),
		"ids":            ids,
	})

	h.Log.Info("Registry operations tests complete")
}
