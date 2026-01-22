package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

var aliasCmd = &cobra.Command{
	Use:   "alias [tool] [profile] [alias]",
	Short: "Manage profile aliases",
	Long: `Create and manage short aliases for profiles.

Aliases make it easier to reference profiles with long names:
  caam alias claude work-account-1 work
  caam activate claude work  # Now uses "work-account-1"

Examples:
  caam alias claude work-account-1 work   # Create alias
  caam alias --list                       # List all aliases
  caam alias --remove work                # Remove alias
  caam alias claude work-account-1        # Show aliases for profile`,
	RunE: runAlias,
}

func init() {
	rootCmd.AddCommand(aliasCmd)
	aliasCmd.Flags().Bool("list", false, "list all aliases")
	aliasCmd.Flags().StringP("remove", "r", "", "remove an alias")
	aliasCmd.Flags().Bool("json", false, "output in JSON format")
}

func runAlias(cmd *cobra.Command, args []string) error {
	listFlag, _ := cmd.Flags().GetBool("list")
	removeFlag, _ := cmd.Flags().GetString("remove")
	jsonFlag, _ := cmd.Flags().GetBool("json")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// List all aliases
	if listFlag {
		return listAliases(cfg, jsonFlag)
	}

	// Remove an alias
	if removeFlag != "" {
		return removeAlias(cfg, removeFlag, jsonFlag)
	}

	// Need at least tool and profile to add or show aliases
	if len(args) < 2 {
		return fmt.Errorf("usage: caam alias <tool> <profile> [alias]")
	}

	tool := args[0]
	profile := args[1]

	// Validate tool
	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	// Validate profile exists
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}
	profiles, err := vault.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	profileExists := false
	for _, p := range profiles {
		if p == profile {
			profileExists = true
			break
		}
	}
	if !profileExists {
		return fmt.Errorf("profile %s/%s not found; run 'caam ls %s' to see available profiles", tool, profile, tool)
	}

	// If no alias provided, show current aliases
	if len(args) == 2 {
		return showProfileAliases(cfg, tool, profile, jsonFlag)
	}

	// Add alias
	alias := args[2]
	return addAlias(cfg, tool, profile, alias, jsonFlag)
}

func listAliases(cfg *config.Config, jsonOutput bool) error {
	if cfg.Aliases == nil || len(cfg.Aliases) == 0 {
		if jsonOutput {
			fmt.Println("{}")
			return nil
		}
		fmt.Println("No aliases configured.")
		return nil
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(cfg.Aliases, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Println("Configured aliases:")
	for key, aliases := range cfg.Aliases {
		fmt.Printf("  %s:\n", key)
		for _, a := range aliases {
			fmt.Printf("    - %s\n", a)
		}
	}
	return nil
}

func removeAlias(cfg *config.Config, alias string, jsonOutput bool) error {
	if !cfg.RemoveAlias(alias) {
		return fmt.Errorf("alias %q not found", alias)
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if jsonOutput {
		result := map[string]any{
			"removed": alias,
			"success": true,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Removed alias: %s\n", alias)
	return nil
}

func showProfileAliases(cfg *config.Config, tool, profile string, jsonOutput bool) error {
	aliases := cfg.GetAliases(tool, profile)

	if jsonOutput {
		result := map[string]any{
			"tool":    tool,
			"profile": profile,
			"aliases": aliases,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	if len(aliases) == 0 {
		fmt.Printf("No aliases for %s/%s\n", tool, profile)
		return nil
	}

	fmt.Printf("Aliases for %s/%s:\n", tool, profile)
	for _, a := range aliases {
		fmt.Printf("  - %s\n", a)
	}
	return nil
}

func addAlias(cfg *config.Config, tool, profile, alias string, jsonOutput bool) error {
	// Check if alias already exists for different profile
	if existingProfile := cfg.ResolveAliasForProvider(tool, alias); existingProfile != "" {
		if existingProfile != profile {
			return fmt.Errorf("alias %q already used for %s/%s", alias, tool, existingProfile)
		}
		// Already exists for this profile
		if jsonOutput {
			result := map[string]any{
				"tool":    tool,
				"profile": profile,
				"alias":   alias,
				"status":  "already_exists",
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("Alias %q already exists for %s/%s\n", alias, tool, profile)
		return nil
	}

	cfg.AddAlias(tool, profile, alias)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if jsonOutput {
		result := map[string]any{
			"tool":    tool,
			"profile": profile,
			"alias":   alias,
			"status":  "created",
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Added alias: %s -> %s/%s\n", alias, tool, profile)
	fmt.Printf("\nYou can now use:\n")
	fmt.Printf("  caam activate %s %s\n", tool, alias)
	return nil
}

// favoriteCmd manages favorite profiles.
var favoriteCmd = &cobra.Command{
	Use:   "favorite <tool> [profiles...]",
	Short: "Set favorite profiles for a tool",
	Long: `Set the favorite profiles for a tool. Favorites are used in priority
order when rotating profiles.

Examples:
  caam favorite claude work personal     # Set favorites
  caam favorite claude                   # Show current favorites
  caam favorite --list                   # List all favorites
  caam favorite claude --clear           # Clear favorites`,
	RunE: runFavorite,
}

func init() {
	rootCmd.AddCommand(favoriteCmd)
	favoriteCmd.Flags().Bool("list", false, "list all favorites")
	favoriteCmd.Flags().Bool("clear", false, "clear favorites for the tool")
	favoriteCmd.Flags().Bool("json", false, "output in JSON format")
}

func runFavorite(cmd *cobra.Command, args []string) error {
	listFlag, _ := cmd.Flags().GetBool("list")
	clearFlag, _ := cmd.Flags().GetBool("clear")
	jsonFlag, _ := cmd.Flags().GetBool("json")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// List all favorites
	if listFlag {
		return listFavorites(cfg, jsonFlag)
	}

	if len(args) < 1 {
		return fmt.Errorf("usage: caam favorite <tool> [profiles...]")
	}

	tool := args[0]
	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	// Clear favorites
	if clearFlag {
		cfg.SetFavorites(tool, nil)
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		if jsonFlag {
			result := map[string]any{
				"tool":   tool,
				"status": "cleared",
			}
			data, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("Cleared favorites for %s\n", tool)
		return nil
	}

	// Show favorites if no profiles provided
	if len(args) == 1 {
		return showFavorites(cfg, tool, jsonFlag)
	}

	// Set favorites
	profiles := args[1:]
	return setFavorites(cfg, tool, profiles, jsonFlag)
}

func listFavorites(cfg *config.Config, jsonOutput bool) error {
	if cfg.Favorites == nil || len(cfg.Favorites) == 0 {
		if jsonOutput {
			fmt.Println("{}")
			return nil
		}
		fmt.Println("No favorites configured.")
		return nil
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(cfg.Favorites, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Println("Configured favorites:")
	for tool, profiles := range cfg.Favorites {
		fmt.Printf("  %s:\n", tool)
		for i, p := range profiles {
			fmt.Printf("    %d. %s\n", i+1, p)
		}
	}
	return nil
}

func showFavorites(cfg *config.Config, tool string, jsonOutput bool) error {
	favorites := cfg.GetFavorites(tool)

	if jsonOutput {
		result := map[string]any{
			"tool":      tool,
			"favorites": favorites,
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	if len(favorites) == 0 {
		fmt.Printf("No favorites for %s\n", tool)
		return nil
	}

	fmt.Printf("Favorites for %s:\n", tool)
	for i, p := range favorites {
		fmt.Printf("  %d. %s\n", i+1, p)
	}
	return nil
}

func setFavorites(cfg *config.Config, tool string, profiles []string, jsonOutput bool) error {
	// Validate profiles exist
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}
	existingProfiles, err := vault.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	profileSet := make(map[string]bool)
	for _, p := range existingProfiles {
		profileSet[p] = true
	}

	// Also check aliases
	for _, profile := range profiles {
		if !profileSet[profile] {
			// Try resolving as alias
			resolved := cfg.ResolveAliasForProvider(tool, profile)
			if resolved == "" {
				fmt.Fprintf(os.Stderr, "Warning: profile or alias %q not found for %s\n", profile, tool)
			}
		}
	}

	cfg.SetFavorites(tool, profiles)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if jsonOutput {
		result := map[string]any{
			"tool":      tool,
			"favorites": profiles,
			"status":    "updated",
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Set favorites for %s:\n", tool)
	for i, p := range profiles {
		fmt.Printf("  %d. %s\n", i+1, p)
	}
	return nil
}
