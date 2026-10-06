package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

func TestWorkspaceCommand(t *testing.T) {
	if workspaceCmd.Use != "workspace [name]" {
		t.Errorf("Expected Use 'workspace [name]', got %q", workspaceCmd.Use)
	}

	if workspaceCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

func TestWorkspaceCreateCommand(t *testing.T) {
	if workspaceCreateCmd.Use != "create <name>" {
		t.Errorf("Expected Use 'create <name>', got %q", workspaceCreateCmd.Use)
	}

	// Check flags exist
	flags := []string{"claude", "codex", "gemini"}
	for _, name := range flags {
		flag := workspaceCreateCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Flag %q not found", name)
		}
	}
}

func TestWorkspaceDeleteCommand(t *testing.T) {
	if workspaceDeleteCmd.Use != "delete <name>" {
		t.Errorf("Expected Use 'delete <name>', got %q", workspaceDeleteCmd.Use)
	}
}

func TestWorkspaceListCommand(t *testing.T) {
	if workspaceListCmd.Use != "list" {
		t.Errorf("Expected Use 'list', got %q", workspaceListCmd.Use)
	}

	// Check json flag
	flag := workspaceListCmd.Flags().Lookup("json")
	if flag == nil {
		t.Error("Expected --json flag")
	}
}

func TestWorkspaceConfigMethods(t *testing.T) {
	cfg := config.DefaultConfig()

	// Initially no workspaces
	workspaces := cfg.ListWorkspaces()
	if len(workspaces) != 0 {
		t.Errorf("Expected 0 workspaces, got %d", len(workspaces))
	}

	// Create workspace
	profiles := map[string]string{
		"claude": "work-claude",
		"codex":  "work-codex",
	}
	cfg.CreateWorkspace("work", profiles)

	// Verify workspace exists
	workspaces = cfg.ListWorkspaces()
	if len(workspaces) != 1 {
		t.Errorf("Expected 1 workspace, got %d", len(workspaces))
	}
	if workspaces[0] != "work" {
		t.Errorf("Expected workspace 'work', got %q", workspaces[0])
	}

	// Get workspace
	got := cfg.GetWorkspace("work")
	if got == nil {
		t.Fatal("GetWorkspace returned nil")
	}
	if got["claude"] != "work-claude" {
		t.Errorf("Expected claude=work-claude, got %q", got["claude"])
	}
	if got["codex"] != "work-codex" {
		t.Errorf("Expected codex=work-codex, got %q", got["codex"])
	}

	// Set current workspace
	cfg.SetCurrentWorkspace("work")
	if cfg.GetCurrentWorkspace() != "work" {
		t.Errorf("Expected current workspace 'work', got %q", cfg.GetCurrentWorkspace())
	}

	// Delete workspace
	if !cfg.DeleteWorkspace("work") {
		t.Error("DeleteWorkspace should return true for existing workspace")
	}

	// Verify deleted
	workspaces = cfg.ListWorkspaces()
	if len(workspaces) != 0 {
		t.Errorf("Expected 0 workspaces after delete, got %d", len(workspaces))
	}

	// Current workspace should be cleared
	if cfg.GetCurrentWorkspace() != "" {
		t.Errorf("Expected empty current workspace after delete, got %q", cfg.GetCurrentWorkspace())
	}

	// Delete non-existent should return false
	if cfg.DeleteWorkspace("nonexistent") {
		t.Error("DeleteWorkspace should return false for non-existent workspace")
	}
}

func TestWorkspaceListOrdering(t *testing.T) {
	cfg := config.DefaultConfig()

	// Create workspaces in non-alphabetical order
	cfg.CreateWorkspace("zebra", map[string]string{"claude": "z"})
	cfg.CreateWorkspace("alpha", map[string]string{"claude": "a"})
	cfg.CreateWorkspace("beta", map[string]string{"claude": "b"})

	workspaces := cfg.ListWorkspaces()
	if len(workspaces) != 3 {
		t.Fatalf("Expected 3 workspaces, got %d", len(workspaces))
	}

	// Should be sorted alphabetically
	expected := []string{"alpha", "beta", "zebra"}
	for i, name := range expected {
		if workspaces[i] != name {
			t.Errorf("Expected workspace[%d]=%q, got %q", i, name, workspaces[i])
		}
	}
}

func TestWorkspaceValidation(t *testing.T) {
	// Test that workspace names starting with _ are reserved
	invalidNames := []string{"_backup", "_system", "_reserved"}
	for _, name := range invalidNames {
		if name[0] != '_' {
			t.Errorf("Test case %q should start with underscore", name)
		}
	}

	validNames := []string{"work", "home", "personal", "team-1"}
	for _, name := range validNames {
		if name[0] == '_' {
			t.Errorf("Test case %q should not start with underscore", name)
		}
	}
}

func TestSwitchWorkspace(t *testing.T) {
	home := setupRobotCredentialEnv(t)
	claudeLive := `{"claudeAiOauth":{"accessToken":"SYNTHETIC-ORIGINAL-CLAUDE"}}`
	claudeTarget := `{"claudeAiOauth":{"accessToken":"SYNTHETIC-WORK-CLAUDE"}}`
	codexLive := `{"access_token":"SYNTHETIC-ORIGINAL-CODEX"}`
	codexTarget := `{"access_token":"SYNTHETIC-WORK-CODEX"}`
	claudePath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
	codexPath := filepath.Join(home, ".codex", "auth.json")
	writeNativeTestCredential(t, claudePath, claudeLive)
	writeNativeTestCredential(t, codexPath, codexLive)
	writeRobotCredentialProfile(t, "claude", "work-claude", map[string]string{".credentials.json": claudeTarget})
	writeRobotCredentialProfile(t, "codex", "work-codex", map[string]string{"auth.json": codexTarget})
	cfg := config.DefaultConfig()
	cfg.CreateWorkspace("work", map[string]string{
		"claude": "work-claude",
		"codex":  "work-codex",
	})

	err := switchWorkspace(cfg, "work")
	if err != nil {
		t.Fatalf("switchWorkspace failed: %v", err)
	}
	if cfg.GetCurrentWorkspace() != "work" {
		t.Errorf("Expected current workspace 'work', got %q", cfg.GetCurrentWorkspace())
	}
	requireSwitchCredential(t, claudePath, claudeTarget)
	requireSwitchCredential(t, codexPath, codexTarget)
	requireSwitchCredential(t, vault.BackupPath("claude", "_original", ".credentials.json"), claudeLive)
	requireSwitchCredential(t, vault.BackupPath("codex", "_original", "auth.json"), codexLive)
	saved, err := config.Load()
	if err != nil || saved.GetCurrentWorkspace() != "work" {
		t.Fatalf("completed workspace was not persisted: %v", err)
	}
}

func TestSwitchWorkspaceFailureKeepsPreviousWorkspace(t *testing.T) {
	setupRobotCredentialEnv(t)
	live := `{"claudeAiOauth":{"accessToken":"SYNTHETIC-CURRENT"}}`
	livePath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json")
	writeNativeTestCredential(t, livePath, live)
	writeRobotCredentialProfile(t, "claude", "invalid", map[string]string{".claude.json": `{"theme":"dark"}`})
	writeRobotCredentialProfile(t, "codex", "work", map[string]string{"auth.json": `{"access_token":"SYNTHETIC-CODEX-WORK"}`})
	cfg := config.DefaultConfig()
	cfg.CreateWorkspace("work", map[string]string{"claude": "invalid", "codex": "work"})
	cfg.SetCurrentWorkspace("previous")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := switchWorkspace(cfg, "work"); err == nil {
		t.Fatal("partially activated workspace falsely reported success")
	}
	if cfg.GetCurrentWorkspace() != "previous" {
		t.Fatal("failed workspace activation replaced the previous workspace")
	}
	saved, err := config.Load()
	if err != nil || saved.GetCurrentWorkspace() != "previous" {
		t.Fatalf("failed workspace activation changed persisted workspace: %v", err)
	}
	requireSwitchCredential(t, livePath, live)
	profiles, err := vault.List("claude")
	if err != nil || len(profiles) != 1 || profiles[0] != "invalid" {
		t.Fatalf("invalid workspace target changed the vault: %v, %v", profiles, err)
	}
}

func TestSwitchWorkspaceNotFound(t *testing.T) {
	cfg := config.DefaultConfig()

	err := switchWorkspace(cfg, "nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent workspace")
	}
}

func TestWorkspaceUpdate(t *testing.T) {
	cfg := config.DefaultConfig()

	// Create initial workspace
	cfg.CreateWorkspace("work", map[string]string{
		"claude": "claude-1",
	})

	// Update workspace
	cfg.CreateWorkspace("work", map[string]string{
		"claude": "claude-2",
		"codex":  "codex-1",
	})

	// Verify update
	profiles := cfg.GetWorkspace("work")
	if profiles["claude"] != "claude-2" {
		t.Errorf("Expected claude=claude-2, got %q", profiles["claude"])
	}
	if profiles["codex"] != "codex-1" {
		t.Errorf("Expected codex=codex-1, got %q", profiles["codex"])
	}
}

func TestWorkspaceGetNonExistent(t *testing.T) {
	cfg := config.DefaultConfig()

	profiles := cfg.GetWorkspace("nonexistent")
	if profiles != nil {
		t.Errorf("Expected nil for non-existent workspace, got %v", profiles)
	}
}

func TestWorkspaceEmptyConfig(t *testing.T) {
	cfg := &config.Config{}

	// These should not panic on nil maps
	workspaces := cfg.ListWorkspaces()
	if workspaces != nil {
		t.Errorf("Expected nil workspaces, got %v", workspaces)
	}

	profiles := cfg.GetWorkspace("test")
	if profiles != nil {
		t.Errorf("Expected nil profiles, got %v", profiles)
	}

	if cfg.DeleteWorkspace("test") {
		t.Error("Expected DeleteWorkspace to return false on nil map")
	}
}
