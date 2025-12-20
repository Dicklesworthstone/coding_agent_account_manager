package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace [name]",
	Short: "Manage profile workspaces",
	Long: `Switch between workspaces or manage workspace definitions.

A workspace is a named set of profiles (one per tool) that can be activated together.
This is useful for switching contexts (e.g., work vs personal) with a single command.

Examples:
  caam workspace              # List all workspaces
  caam workspace work         # Switch to the 'work' workspace
  caam workspace create work --claude=work-claude --codex=work-codex
  caam workspace delete old-workspace
  caam workspace list --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspace,
}

var workspaceCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new workspace",
	Long: `Create a workspace with profile mappings for each tool.

Examples:
  caam workspace create work --claude=work-claude --codex=work-codex --gemini=work-gemini
  caam workspace create home --claude=personal --codex=personal`,
	Args: cobra.ExactArgs(1),
	RunE: runWorkspaceCreate,
}

var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a workspace",
	Long: `Delete a workspace definition.

This does not affect the profiles themselves, only the workspace mapping.

Examples:
  caam workspace delete old-workspace`,
	Args: cobra.ExactArgs(1),
	RunE: runWorkspaceDelete,
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	Long: `List all defined workspaces and their profile mappings.

Examples:
  caam workspace list
  caam workspace list --json`,
	RunE: runWorkspaceList,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceDeleteCmd)
	workspaceCmd.AddCommand(workspaceListCmd)

	// Create command flags
	workspaceCreateCmd.Flags().String("claude", "", "Claude profile for this workspace")
	workspaceCreateCmd.Flags().String("codex", "", "Codex profile for this workspace")
	workspaceCreateCmd.Flags().String("gemini", "", "Gemini profile for this workspace")

	// List command flags
	workspaceListCmd.Flags().Bool("json", false, "Output in JSON format")
	workspaceCmd.Flags().Bool("json", false, "Output in JSON format (for list)")
}

func runWorkspace(cmd *cobra.Command, args []string) error {
	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// If no args, list workspaces
	if len(args) == 0 {
		return runWorkspaceList(cmd, args)
	}

	// Switch to workspace
	workspaceName := args[0]
	return switchWorkspace(cfg, workspaceName)
}

func runWorkspaceCreate(cmd *cobra.Command, args []string) error {
	workspaceName := args[0]

	// Validate workspace name
	if strings.HasPrefix(workspaceName, "_") {
		return fmt.Errorf("workspace names starting with '_' are reserved")
	}

	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Collect profile mappings from flags
	profiles := make(map[string]string)

	claude, _ := cmd.Flags().GetString("claude")
	if claude != "" {
		profiles["claude"] = claude
	}

	codex, _ := cmd.Flags().GetString("codex")
	if codex != "" {
		profiles["codex"] = codex
	}

	gemini, _ := cmd.Flags().GetString("gemini")
	if gemini != "" {
		profiles["gemini"] = gemini
	}

	if len(profiles) == 0 {
		return fmt.Errorf("at least one profile mapping is required (--claude, --codex, or --gemini)")
	}

	// Validate that profiles exist
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	for tool, profile := range profiles {
		existingProfiles, err := vault.List(tool)
		if err != nil {
			return fmt.Errorf("list %s profiles: %w", tool, err)
		}
		found := false
		for _, p := range existingProfiles {
			if p == profile {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("profile %s/%s does not exist", tool, profile)
		}
	}

	// Check if workspace already exists
	existing := cfg.GetWorkspace(workspaceName)
	if existing != nil {
		fmt.Printf("Updating existing workspace '%s'\n", workspaceName)
	}

	// Create workspace
	cfg.CreateWorkspace(workspaceName, profiles)

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Created workspace '%s':\n", workspaceName)
	for tool, profile := range profiles {
		fmt.Printf("  %s: %s\n", tool, profile)
	}

	return nil
}

func runWorkspaceDelete(cmd *cobra.Command, args []string) error {
	workspaceName := args[0]

	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if !cfg.DeleteWorkspace(workspaceName) {
		return fmt.Errorf("workspace '%s' not found", workspaceName)
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Deleted workspace '%s'\n", workspaceName)
	return nil
}

func runWorkspaceList(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	workspaces := cfg.ListWorkspaces()
	current := cfg.GetCurrentWorkspace()

	if jsonOutput {
		type workspaceInfo struct {
			Name     string            `json:"name"`
			Current  bool              `json:"current"`
			Profiles map[string]string `json:"profiles"`
		}
		var output []workspaceInfo
		for _, name := range workspaces {
			output = append(output, workspaceInfo{
				Name:     name,
				Current:  name == current,
				Profiles: cfg.GetWorkspace(name),
			})
		}
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	if len(workspaces) == 0 {
		fmt.Println("No workspaces defined.")
		fmt.Println()
		fmt.Println("Create one with:")
		fmt.Println("  caam workspace create work --claude=work-claude --codex=work-codex")
		return nil
	}

	fmt.Println("Workspaces:")
	for _, name := range workspaces {
		profiles := cfg.GetWorkspace(name)
		marker := "  "
		if name == current {
			marker = "* "
		}
		fmt.Printf("%s%s\n", marker, name)
		for tool, profile := range profiles {
			fmt.Printf("    %s: %s\n", tool, profile)
		}
	}

	if current != "" {
		fmt.Printf("\nCurrent: %s\n", current)
	}

	return nil
}

func switchWorkspace(cfg *config.Config, workspaceName string) error {
	profiles := cfg.GetWorkspace(workspaceName)
	if profiles == nil {
		return fmt.Errorf("workspace '%s' not found", workspaceName)
	}

	// Initialize vault if needed
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	fmt.Printf("Switching to workspace '%s'\n", workspaceName)

	// Activate each profile in the workspace
	var activated []string
	for tool, profile := range profiles {
		getFileSet, ok := tools[tool]
		if !ok {
			fmt.Printf("  Warning: unknown tool '%s', skipping\n", tool)
			continue
		}

		fileSet := getFileSet()

		// Backup original on first use
		if did, err := vault.BackupOriginal(fileSet); err != nil {
			fmt.Printf("  Warning: could not backup original %s auth: %v\n", tool, err)
		} else if did {
			fmt.Printf("  Backed up original %s auth\n", tool)
		}

		// Restore profile
		if err := vault.Restore(fileSet, profile); err != nil {
			fmt.Printf("  Error activating %s/%s: %v\n", tool, profile, err)
			continue
		}

		activated = append(activated, fmt.Sprintf("%s: %s", tool, profile))
	}

	// Update current workspace in config
	cfg.SetCurrentWorkspace(workspaceName)
	if err := cfg.Save(); err != nil {
		fmt.Printf("Warning: could not save current workspace: %v\n", err)
	}

	fmt.Println()
	fmt.Printf("Switched to workspace '%s':\n", workspaceName)
	for _, a := range activated {
		fmt.Printf("  %s\n", a)
	}

	return nil
}
