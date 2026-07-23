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

On error (unknown provider, missing profile, etc.) this command writes a
diagnostic to stderr AND emits a failing shell command ('false') to stdout, so
'eval "$(caam env ...)"' aborts loudly instead of silently keeping the parent
shell's environment. With 'set -e' the script stops; otherwise check $? after
the eval.

Use --unset to print unset commands instead of export commands.
Use --export-prefix to change the export syntax (default: "export").`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		prov, ok := registry.Get(tool)
		if !ok {
			emitEvalFailure()
			return fmt.Errorf("unknown provider: %s (supported: %s)", tool, supportedToolsList())
		}

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			emitEvalFailure()
			return err
		}

		ctx := context.Background()
		envVars, err := prov.Env(ctx, prof)
		if err != nil {
			emitEvalFailure()
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

// emitEvalFailure prints a shell command that makes `eval "$(caam env ...)"`
// fail loudly instead of silently succeeding. Command substitution discards the
// child process's exit status, so an empty stdout on error is indistinguishable
// from `eval ""` (a no-op that returns 0) — which silently leaves the parent
// shell's HOME/CLAUDE_CONFIG_DIR/etc. in place and defeats profile isolation
// (issue #58). Emitting `false` (portable across bash/zsh/fish) propagates a
// non-zero status through the eval. The human-readable cause is still written
// to stderr by cobra. This mirrors how direnv/asdf/nvm behave at this boundary.
func emitEvalFailure() {
	fmt.Println("false  # caam env: failed to resolve profile environment — see error on stderr above")
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.Flags().Bool("unset", false, "print unset commands instead of export")
	envCmd.Flags().String("export-prefix", "export", "export syntax prefix (default: export)")
	envCmd.Flags().Bool("fish", false, "use fish shell syntax")
}
