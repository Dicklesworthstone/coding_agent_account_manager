// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env <tool> <profile>",
	Short: "Print environment variables for shell eval",
	Long: `Prints environment variables that can be eval'd in your shell.

This allows you to set up the environment once and run multiple commands
with the same profile, instead of using 'caam exec' wrapper each time.

The output is valid shell syntax (bash/zsh compatible).

Examples:
  # Set up environment for codex work profile
  eval "$(caam env codex work)"
  codex "implement feature X"
  codex "add tests"

  # Set up environment for claude personal profile
  eval "$(caam env claude personal)"
  claude

  # Unset the variables when done
  eval "$(caam env codex work --unset)"

Use --unset to print unset commands instead of export commands.
Use --export-prefix to change the export syntax (default: "export").`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		prov, ok := registry.Get(tool)
		if !ok {
			return fmt.Errorf("unknown provider: %s (supported: %s)", tool, supportedToolsList())
		}

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		ctx := context.Background()
		envVars, err := prov.Env(ctx, prof)
		if err != nil {
			return fmt.Errorf("get environment: %w", err)
		}

		unset, _ := cmd.Flags().GetBool("unset")
		exportPrefix, _ := cmd.Flags().GetString("export-prefix")
		fishMode, _ := cmd.Flags().GetBool("fish")

		// Sort keys for consistent output
		keys := make([]string, 0, len(envVars))
		for k := range envVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Print environment variables
		for _, k := range keys {
			if unset {
				if fishMode {
					fmt.Printf("set -e %s\n", k)
				} else {
					fmt.Printf("unset %s\n", k)
				}
			} else {
				if fishMode {
					fmt.Printf("set -gx %s %q\n", k, envVars[k])
				} else {
					fmt.Printf("%s %s=%q\n", exportPrefix, k, envVars[k])
				}
			}
		}

		// Add a helpful comment
		if !unset {
			fmt.Printf("# Environment set for %s profile '%s'\n", tool, name)
			fmt.Printf("# Run 'eval \"$(caam env %s %s --unset)\"' to unset\n", tool, name)
		} else {
			fmt.Printf("# Environment unset for %s profile '%s'\n", tool, name)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.Flags().Bool("unset", false, "print unset commands instead of export")
	envCmd.Flags().String("export-prefix", "export", "export syntax prefix (default: export)")
	envCmd.Flags().Bool("fish", false, "use fish shell syntax")
}
