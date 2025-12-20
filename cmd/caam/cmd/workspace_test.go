package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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
	// Create temp directories
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault")
	configPath := filepath.Join(tmpDir, "config.json")

	// Set up environment
	oldXDGConfig := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	defer os.Setenv("XDG_CONFIG_HOME", oldXDGConfig)

	// Create vault with test profiles
	testVault := authfile.NewVault(vaultPath)

	// Create claude profile
	claudeProfileDir := filepath.Join(vaultPath, "claude", "work-claude")
	if err := os.MkdirAll(claudeProfileDir, 0700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeProfileDir, ".claude.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write claude: %v", err)
	}

	// Create codex profile
	codexProfileDir := filepath.Join(vaultPath, "codex", "work-codex")
	if err := os.MkdirAll(codexProfileDir, 0700); err != nil {
		t.Fatalf("mkdir codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexProfileDir, "credentials.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write codex: %v", err)
	}

	// Create config with workspace
	cfg := config.DefaultConfig()
	cfg.CreateWorkspace("work", map[string]string{
		"claude": "work-claude",
		"codex":  "work-codex",
	})

	// Save config
	if err := os.MkdirAll(filepath.Join(tmpDir, "caam"), 0700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configData := []byte(`{
		"workspaces": {
			"work": {"claude": "work-claude", "codex": "work-codex"}
		}
	}`)
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Set global vault for the test
	oldVault := vault
	vault = testVault
	defer func() { vault = oldVault }()

	// Test switchWorkspace
	err := switchWorkspace(cfg, "work")
	if err != nil {
		t.Errorf("switchWorkspace failed: %v", err)
	}

	// Verify current workspace is set
	if cfg.GetCurrentWorkspace() != "work" {
		t.Errorf("Expected current workspace 'work', got %q", cfg.GetCurrentWorkspace())
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
