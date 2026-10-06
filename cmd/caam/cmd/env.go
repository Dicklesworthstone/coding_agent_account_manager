// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
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

In shell mode, on error (unknown provider, missing profile, etc.) this command writes a
diagnostic to stderr AND emits a failing shell command ('false') to stdout, so
'eval "$(caam env ...)"' aborts loudly instead of silently keeping the parent
shell's environment. With 'set -e' the script stops; otherwise check $? after
the eval.

Use --unset to print unset commands instead of export commands.
Use --json to print a data-only object with set and unset fields.
Use --export-prefix to change the export syntax (default: "export").`,
	Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(2)(cmd, args); err != nil {
			if cmd != nil {
				emitEvalFailure(cmd)
			}
			return err
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) (runErr error) {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		defer func() {
			if runErr != nil {
				emitEvalFailure(cmd)
			}
		}()
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

		envVars, err := prov.Env(cmd.Context(), prof)
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
		out := cmd.OutOrStdout()
		if jsonOutput {
			result := struct {
				Set   map[string]string `json:"set"`
				Unset []string          `json:"unset"`
			}{Set: envVars, Unset: []string{}}
			if unset {
				result.Set = map[string]string{}
				result.Unset = keys
			}
			return json.NewEncoder(out).Encode(result)
		}

		// Print environment variables
		for _, k := range keys {
			if unset {
				if fishMode {
					fmt.Fprintf(out, "set -e %s\n", k)
				} else {
					fmt.Fprintf(out, "unset %s\n", k)
				}
			} else {
				if fishMode {
					fmt.Fprintf(out, "set -gx %s %s\n", k, fishQuote(envVars[k]))
				} else {
					fmt.Fprintf(out, "%s %s=%s\n", exportPrefix, k, shellQuote(envVars[k]))
				}
			}
		}

		// Add a helpful comment
		if !unset {
			fmt.Fprintf(out, "# Environment set for %s profile '%s'\n", tool, name)
			fmt.Fprintf(out, "# Run 'eval \"$(caam env %s %s --unset)\"' to unset\n", tool, name)
		} else {
			fmt.Fprintf(out, "# Environment unset for %s profile '%s'\n", tool, name)
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
func emitEvalFailure(cmd *cobra.Command) {
	// Cobra prints usage through the root output writer; keep it out of both
	// evaluable shell output and data-only JSON errors.
	cmd.SilenceUsage = true
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "false  # caam env: failed to resolve profile environment — see error on stderr above")
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.Flags().Bool("unset", false, "print unset commands instead of export")
	envCmd.Flags().String("export-prefix", "export", "export syntax prefix (default: export)")
	envCmd.Flags().Bool("fish", false, "use fish shell syntax")
	envCmd.Flags().Bool("json", false, "print environment changes as JSON without shell commands")
}
