// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env <tool> <profile>",
	Short: "Print environment variables for shell eval",
	Long: `Prints environment variables that can be eval'd in your shell.

This allows you to set up the environment once and run multiple commands
with the same profile, instead of using 'caam exec' wrapper each time.

The output is valid shell syntax (bash/zsh compatible). It clears inherited
credentials that would override the selected profile's authentication mode.
API-key profiles keep the ambient key inputs supported by their provider.

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

Use --unset to clear the profile's path overrides; this does not restore the
previous environment or credentials removed when the profile was selected.
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

		changes, err := provider.ProfileEnvironment(cmd.Context(), prov, prof)
		if err != nil {
			return fmt.Errorf("get environment: %w", err)
		}
		envVars := changes.Set

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
		if unset {
			changes.Set = map[string]string{}
			changes.Unset = keys
		}
		if jsonOutput {
			return json.NewEncoder(out).Encode(changes)
		}

		if err := writeEnvironmentChanges(out, changes, exportPrefix, fishMode); err != nil {
			return err
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

// writeEnvironmentChanges renders the complete isolation policy, including
// removals. Assignment-only output would leave ambient credentials active in
// shell wrappers even though MergeEnvironment removes them for direct launches.
func writeEnvironmentChanges(out io.Writer, changes provider.EnvironmentChanges, exportPrefix string, fishMode bool) error {
	var commands []string
	unsets := append([]string(nil), changes.Unset...)
	sort.Strings(unsets)
	for i, k := range unsets {
		if i > 0 && unsets[i-1] == k {
			continue
		}
		if fishMode {
			commands = append(commands, fmt.Sprintf("set -e %s", k))
		} else {
			commands = append(commands, fmt.Sprintf("unset %s", k))
		}
	}
	keys := make([]string, 0, len(changes.Set))
	for k := range changes.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if fishMode {
			commands = append(commands, fmt.Sprintf("set -gx %s %s", k, fishQuote(changes.Set[k])))
		} else {
			commands = append(commands, fmt.Sprintf("%s %s=%s", exportPrefix, k, shellQuote(changes.Set[k])))
		}
	}
	if len(commands) == 0 {
		return nil
	}
	separator := "\n"
	if !fishMode {
		// A readonly ambient credential or HOME must make eval fail rather
		// than let a later successful assignment hide the isolation failure.
		separator = " &&\n"
	}
	script := strings.Join(commands, separator) + "\n"
	n, err := io.WriteString(out, script)
	if err == nil && n != len(script) {
		return io.ErrShortWrite
	}
	return err
}

// isEnvironmentExport also covers shallow exports so root initialization cannot
// migrate stores or mirror host credentials during a read-only inspection.
func isEnvironmentExport(cmd *cobra.Command) bool {
	if cmd.Name() == "env" {
		return true
	}
	printEnv, _ := cmd.Flags().GetBool("print-env")
	return cmd.Name() == "shallow-spawn" && printEnv
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
	fmt.Fprintln(cmd.OutOrStdout(), "false  # caam: failed to resolve profile environment — see error on stderr above")
}

// Flag parsing happens before Args and RunE. A parse error must also make eval
// fail, even when an invalid flag prevented a later --print-env from parsing.
func environmentFlagError(cmd *cobra.Command, err error) error {
	emitEvalFailure(cmd)
	return err
}

func init() {
	rootCmd.AddCommand(envCmd)
	envCmd.SetFlagErrorFunc(environmentFlagError)
	envCmd.Flags().Bool("unset", false, "print unset commands instead of export")
	envCmd.Flags().String("export-prefix", "export", "export syntax prefix (default: export)")
	envCmd.Flags().Bool("fish", false, "use fish shell syntax")
	envCmd.Flags().Bool("json", false, "print environment changes as JSON without shell commands")
}
