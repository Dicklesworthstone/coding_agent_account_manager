// Package cmd implements the CLI commands for caam.
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// useCmd sets the default profile for a provider.
var useCmd = &cobra.Command{
	Use:   "use <provider> <profile>",
	Short: "Set default profile for a provider",
	Long: `Sets the default profile for a provider in the configuration.

After setting a default, commands that operate on a provider's profile
will use the default when no profile is explicitly specified.

Examples:
  caam use codex work-1      # Set work-1 as default for codex
  caam use claude personal   # Set personal as default for claude

Use 'caam which' to see current defaults.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider := strings.ToLower(args[0])
		profileName := args[1]

		// Validate provider
		if _, ok := tools[provider]; !ok {
			return fmt.Errorf("unknown provider: %s (supported: codex, claude, gemini, cursor)", provider)
		}

		// Check if vault profile exists
		profiles, err := vault.List(provider)
		if err != nil {
			return fmt.Errorf("list profiles: %w", err)
		}

		found := false
		for _, p := range profiles {
			if p == profileName {
				found = true
				break
			}
		}

		// Also check isolated profiles
		if !found {
			isolatedProfiles, err := profileStore.List(provider)
			if err == nil {
				for _, p := range isolatedProfiles {
					if p.Name == profileName {
						found = true
						break
					}
				}
			}
			// Note: we don't fail on error here - isolated profiles are optional
			// If profileStore.List fails, we just won't find a match in isolated profiles
		}

		if !found {
			return fmt.Errorf("profile '%s' not found for %s\nHint: Use 'caam ls %s' or 'caam profile ls %s' to see available profiles",
				profileName, provider, provider, provider)
		}

		// Update config
		cfg.SetDefault(provider, profileName)
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		fmt.Printf("Set default %s profile to '%s'\n", provider, profileName)
		return nil
	},
}

// whichCmd shows the default profiles.
var whichCmd = &cobra.Command{
	Use:   "which [provider]",
	Short: "Show default profiles",
	Long: `Shows the default profile for each provider (or a specific provider).

The default profile is used when a command needs a profile but none is specified.

Examples:
  caam which           # Show defaults for all providers
  caam which codex     # Show default for codex only

Use 'caam use <provider> <profile>' to set defaults.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		providers := []string{"codex", "claude", "gemini"}

		if len(args) > 0 {
			provider := strings.ToLower(args[0])
			if _, ok := tools[provider]; !ok {
				return fmt.Errorf("unknown provider: %s", provider)
			}
			providers = []string{provider}
		}

		hasDefaults := false
		for _, provider := range providers {
			defaultProfile := cfg.GetDefault(provider)
			if defaultProfile != "" {
				fmt.Printf("%s: %s\n", provider, defaultProfile)
				hasDefaults = true
			} else {
				fmt.Printf("%s: (none)\n", provider)
			}
		}

		if !hasDefaults && len(args) == 0 {
			fmt.Println("\nNo defaults set.")
			fmt.Println("Use 'caam use <provider> <profile>' to set a default.")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(useCmd)
	rootCmd.AddCommand(whichCmd)
}
