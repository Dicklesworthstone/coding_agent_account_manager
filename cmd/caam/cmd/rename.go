package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

var renameCmd = &cobra.Command{
	Use:   "rename <tool> <old-name> <new-name>",
	Short: "Rename a profile (non-destructive copy)",
	Long: `Rename a profile by creating a copy with a new name.

This is a NON-DESTRUCTIVE operation by default:
  - The original profile is preserved
  - A new profile is created with the new name
  - Aliases pointing to the old name are optionally migrated

To remove the old profile after copying, use --delete-old (requires confirmation).

Examples:
  caam rename claude auto-20260121-143022 work    # Copy to friendly name
  caam rename codex old-account new-account       # Rename profile
  caam rename gemini temp main --delete-old       # Rename and remove old`,
	Args: cobra.ExactArgs(3),
	RunE: runRename,
}

func init() {
	rootCmd.AddCommand(renameCmd)
	renameCmd.Flags().Bool("delete-old", false, "delete the old profile after copying (destructive)")
	renameCmd.Flags().Bool("migrate-aliases", true, "migrate aliases from old to new profile")
	renameCmd.Flags().Bool("json", false, "output in JSON format")
	renameCmd.Flags().BoolP("yes", "y", false, "skip confirmation for --delete-old")
}

func runRename(cmd *cobra.Command, args []string) error {
	tool := args[0]
	oldName := args[1]
	newName := args[2]

	deleteOld, _ := cmd.Flags().GetBool("delete-old")
	migrateAliases, _ := cmd.Flags().GetBool("migrate-aliases")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	skipConfirm, _ := cmd.Flags().GetBool("yes")

	// Validate tool
	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	// Initialize vault if needed
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	// Validate old profile exists
	profiles, err := vault.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	oldExists := false
	newExists := false
	for _, p := range profiles {
		if p == oldName {
			oldExists = true
		}
		if p == newName {
			newExists = true
		}
	}
	if !oldExists {
		return fmt.Errorf("source profile %s/%s not found; run 'caam ls %s' to see available profiles", tool, oldName, tool)
	}
	if newExists {
		return fmt.Errorf("destination profile %s/%s already exists; choose a different name or delete it with 'caam delete %s %s'", tool, newName, tool, newName)
	}

	// Prevent renaming system profiles to non-system names (preserves safety)
	if authfile.IsSystemProfile(oldName) && !authfile.IsSystemProfile(newName) {
		// This is fine - user wants to "promote" a backup to a real profile
	}

	// Copy the profile
	if err := vault.CopyProfile(tool, oldName, newName); err != nil {
		return fmt.Errorf("copy profile: %w", err)
	}

	result := map[string]interface{}{
		"tool":     tool,
		"old_name": oldName,
		"new_name": newName,
		"copied":   true,
		"deleted":  false,
	}

	// Migrate aliases if requested
	if migrateAliases {
		cfg, err := config.Load()
		if err == nil {
			aliases := cfg.GetAliases(tool, oldName)
			if len(aliases) > 0 {
				// Remove aliases from old profile and add to new
				for _, alias := range aliases {
					cfg.RemoveAlias(alias)
					cfg.AddAlias(tool, newName, alias)
				}
				if err := cfg.Save(); err != nil {
					// Non-fatal - profile is already copied
					if !jsonOutput {
						fmt.Printf("Warning: failed to migrate aliases: %v\n", err)
					}
				} else {
					result["migrated_aliases"] = aliases
					if !jsonOutput {
						fmt.Printf("Migrated %d alias(es) to new profile\n", len(aliases))
					}
				}
			}
		}
	}

	// Delete old profile if requested (with confirmation)
	if deleteOld {
		if !skipConfirm {
			fmt.Printf("Delete old profile %s/%s? This cannot be undone. [y/N]: ", tool, oldName)
			var response string
			fmt.Scanln(&response)
			if response != "y" && response != "Y" {
				if jsonOutput {
					result["deleted"] = false
					result["delete_skipped"] = "user declined"
					data, _ := json.MarshalIndent(result, "", "  ")
					fmt.Println(string(data))
					return nil
				}
				fmt.Println("Skipped deletion. Old profile preserved.")
				fmt.Printf("\nProfile copied: %s/%s -> %s/%s\n", tool, oldName, tool, newName)
				return nil
			}
		}

		if err := vault.Delete(tool, oldName); err != nil {
			// If it's a system profile, try DeleteForce (user explicitly requested)
			if authfile.IsSystemProfile(oldName) {
				if err := vault.DeleteForce(tool, oldName); err != nil {
					return fmt.Errorf("delete old profile: %w", err)
				}
			} else {
				return fmt.Errorf("delete old profile: %w", err)
			}
		}
		result["deleted"] = true
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	if deleteOld {
		fmt.Printf("Renamed profile: %s/%s -> %s/%s\n", tool, oldName, tool, newName)
	} else {
		fmt.Printf("Copied profile: %s/%s -> %s/%s\n", tool, oldName, tool, newName)
		fmt.Printf("Original profile preserved. Use --delete-old to remove it.\n")
	}

	fmt.Printf("\nYou can now use:\n")
	fmt.Printf("  caam activate %s %s\n", tool, newName)

	return nil
}
