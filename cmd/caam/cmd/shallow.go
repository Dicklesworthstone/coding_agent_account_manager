package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
	"github.com/spf13/cobra"
)

// =============================================================================
// SHALLOW PROFILE COMMANDS — concurrent multi-account multiplexing.
// =============================================================================
//
// "Shallow" because each profile shares everything with the user's real HOME
// EXCEPT the auth-bearing files (.claude/.credentials.json + .credentials.lock,
// .claude.json). See internal/shallow for the layout rationale.

// resolveShallowManager returns a shallow.Manager rooted at the path implied by
// (in priority order): --base flag, $CAAM_SHALLOW_HOMES_DIR, $CAAM_HOME/shallow-homes,
// or ~/orch-homes. The flag wins so tests and operators can isolate.
func resolveShallowManager(cmd *cobra.Command) (*shallow.Manager, error) {
	base, _ := cmd.Flags().GetString("base")
	return shallow.NewManager(strings.TrimSpace(base), "")
}

// shallowProfileCmd is the parent command for shallow profile management.
var shallowProfileCmd = &cobra.Command{
	Use:   "shallow-profile",
	Short: "Manage shallow profiles for concurrent multi-account use",
	Long: `Manage shallow profiles — per-identity HOME directories where ONLY the
auth files are real and everything else is a symlink back to your real HOME.

This enables N parallel sessions, each pinned to a different account, while
preserving shared state (shell history, git config, ssh keys, Claude
conversation history under ~/.claude/projects/). Unlike 'caam profile add'
which gives each profile a blank, fully-isolated HOME, shallow profiles only
isolate what MUST differ (the credentials).

Layout under ~/orch-homes/<name>/:

  .claude/.credentials.json       (real file — per-identity OAuth tokens)
  .claude/.credentials.lock       (real file — per-identity flock target)
  .claude.json                    (real file — Claude rewrites this on each run)
  .claude/projects, .claude/todos (symlinks → ~/.claude/projects, etc.)
  .bashrc, .gitconfig, .ssh, ...  (symlinks → ~/.bashrc, etc.)

Spawn under a shallow identity with:

  caam shallow-spawn <name> -- claude

which sets HOME=~/orch-homes/<name> and execs the command.`,
}

func init() {
	shallowProfileCmd.PersistentFlags().String("base", "", "shallow profiles base dir (default: $CAAM_SHALLOW_HOMES_DIR or ~/orch-homes)")
	shallowProfileCmd.AddCommand(shallowProfileCreateCmd)
	shallowProfileCmd.AddCommand(shallowProfileListCmd)
	shallowProfileCmd.AddCommand(shallowProfileDeleteCmd)
	rootCmd.AddCommand(shallowProfileCmd)
	rootCmd.AddCommand(shallowSpawnCmd)
}

// shallowProfileCreateCmd creates a new shallow profile.
var shallowProfileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new shallow profile",
	Long: `Create a new shallow profile. Provisions the symlink farm and copies
a credential file into <home>/.claude/.credentials.json.

Credential source (one of):
  --from-vault <tool>/<profile>   Use an existing caam vault profile's credentials
  --from-file <path>              Copy credentials from an arbitrary path
  (none)                          Leave credentials empty; populate later via login

Examples:
  caam shallow-profile create alice --from-vault claude/alice@example.com
  caam shallow-profile create bob   --from-file /tmp/bob.credentials.json
  caam shallow-profile create scratch                 # empty credentials
  caam shallow-profile create alice --json
  caam shallow-profile create alice --force           # overwrite existing
  caam shallow-profile create alice --base /tmp/test-orch-homes`,
	Args: cobra.ExactArgs(1),
	RunE: runShallowProfileCreate,
}

func init() {
	shallowProfileCreateCmd.Flags().String("from-vault", "", "credential source: <tool>/<profile> from caam's vault (e.g. claude/alice@example.com)")
	shallowProfileCreateCmd.Flags().String("from-file", "", "credential source: arbitrary path to a .credentials.json")
	shallowProfileCreateCmd.Flags().String("from-claude-json", "", "optional path to copy as <home>/.claude.json (defaults to ~/.claude.json)")
	shallowProfileCreateCmd.Flags().Bool("force", false, "overwrite an existing shallow profile")
	shallowProfileCreateCmd.Flags().Bool("json", false, "output as JSON")
}

type shallowCreateOutput struct {
	Success        bool   `json:"success"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	CredentialFrom string `json:"credential_from,omitempty"`
	Error          string `json:"error,omitempty"`
}

func runShallowProfileCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	jsonOut, _ := cmd.Flags().GetBool("json")
	force, _ := cmd.Flags().GetBool("force")
	fromVault, _ := cmd.Flags().GetString("from-vault")
	fromFile, _ := cmd.Flags().GetString("from-file")
	fromClaudeJSON, _ := cmd.Flags().GetString("from-claude-json")

	output := shallowCreateOutput{Name: name}
	emit := func(err error) error {
		if jsonOut {
			output.Success = err == nil
			if err != nil {
				output.Error = err.Error()
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(output)
		}
		return err
	}

	if fromVault != "" && fromFile != "" {
		return emit(fmt.Errorf("--from-vault and --from-file are mutually exclusive"))
	}

	mgr, err := resolveShallowManager(cmd)
	if err != nil {
		return emit(fmt.Errorf("init shallow manager: %w", err))
	}

	opts := shallow.CreateOptions{
		Force:            force,
		SourceClaudeJSON: fromClaudeJSON,
	}

	switch {
	case fromVault != "":
		credPath, label, err := resolveVaultCredential(fromVault)
		if err != nil {
			return emit(err)
		}
		opts.CredentialSource = credPath
		opts.CredentialFromLabel = label
	case fromFile != "":
		abs, err := filepath.Abs(fromFile)
		if err != nil {
			return emit(fmt.Errorf("resolve --from-file: %w", err))
		}
		if _, err := os.Stat(abs); err != nil {
			return emit(fmt.Errorf("--from-file: %w", err))
		}
		opts.CredentialSource = abs
		opts.CredentialFromLabel = "file:" + abs
	default:
		// No credential source — terse stderr nudge unless json.
		if !jsonOut {
			fmt.Fprintln(cmd.ErrOrStderr(), "note: no --from-vault/--from-file given; .credentials.json will be empty.")
			fmt.Fprintln(cmd.ErrOrStderr(), "      Populate it before running 'shallow-spawn' (e.g. by signing in inside the shallow HOME).")
		}
	}

	home, err := mgr.Create(name, opts)
	if err != nil {
		return emit(fmt.Errorf("create shallow profile: %w", err))
	}

	output.Path = home
	output.CredentialFrom = opts.CredentialFromLabel
	if jsonOut {
		output.Success = true
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Created shallow profile %q\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "  Path: %s\n", home)
	if opts.CredentialFromLabel != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Credentials: %s\n", opts.CredentialFromLabel)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nNext steps:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  caam shallow-spawn %s -- claude\n", name)
	return nil
}

// resolveVaultCredential takes a "tool/profile" string and returns the absolute
// path to that vault profile's .credentials.json, plus a human-readable label.
func resolveVaultCredential(spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("--from-vault requires <tool>/<profile>")
	}
	parts := strings.SplitN(spec, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("--from-vault must be in the form <tool>/<profile>, got %q", spec)
	}
	tool := strings.ToLower(strings.TrimSpace(parts[0]))
	profile := strings.TrimSpace(parts[1])
	if tool != "claude" {
		// Today, shallow profiles target Claude Code (the only tool whose
		// auth file lives at ~/.claude/.credentials.json). Other tools have
		// different layouts; their support is tracked separately.
		return "", "", fmt.Errorf("--from-vault currently supports tool=claude only (got %q)", tool)
	}
	if vault == nil {
		return "", "", fmt.Errorf("vault not initialized")
	}
	credPath := filepath.Join(vault.ProfilePath(tool, profile), ".credentials.json")
	if _, err := os.Stat(credPath); err != nil {
		return "", "", fmt.Errorf("vault profile %s/%s missing .credentials.json: %w", tool, profile, err)
	}
	return credPath, "vault:" + tool + "/" + profile, nil
}

// shallowProfileListCmd lists existing shallow profiles.
var shallowProfileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List shallow profiles",
	Long: `List shallow profiles under the configured base dir.

Examples:
  caam shallow-profile list
  caam shallow-profile list --json`,
	RunE: runShallowProfileList,
}

func init() {
	shallowProfileListCmd.Flags().Bool("json", false, "output as JSON")
}

type shallowListItem struct {
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	CredentialFrom string    `json:"credential_from,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

type shallowListOutput struct {
	BaseDir  string            `json:"base_dir"`
	Profiles []shallowListItem `json:"profiles"`
	Count    int               `json:"count"`
}

func runShallowProfileList(cmd *cobra.Command, _ []string) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	mgr, err := resolveShallowManager(cmd)
	if err != nil {
		return fmt.Errorf("init shallow manager: %w", err)
	}
	profiles, err := mgr.List()
	if err != nil {
		return fmt.Errorf("list shallow profiles: %w", err)
	}

	if jsonOut {
		out := shallowListOutput{BaseDir: mgr.BaseDir(), Count: len(profiles)}
		for _, p := range profiles {
			item := shallowListItem{Name: p.Name, Path: p.Path}
			if p.Meta != nil {
				item.CredentialFrom = p.Meta.CredentialFrom
				item.CreatedAt = p.Meta.CreatedAt
			}
			out.Profiles = append(out.Profiles, item)
		}
		// Stable order for deterministic output.
		sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].Name < out.Profiles[j].Name })
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if len(profiles) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No shallow profiles in %s\n", mgr.BaseDir())
		fmt.Fprintln(cmd.OutOrStdout(), "Create one with: caam shallow-profile create <name> --from-vault claude/<profile>")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Shallow profiles (base: %s)\n", mgr.BaseDir())
	fmt.Fprintf(cmd.OutOrStdout(), "%-22s  %-32s  %s\n", "NAME", "CREDENTIALS", "CREATED")
	for _, p := range profiles {
		credFrom := "(none)"
		created := "?"
		if p.Meta != nil {
			if p.Meta.CredentialFrom != "" {
				credFrom = p.Meta.CredentialFrom
			}
			if !p.Meta.CreatedAt.IsZero() {
				created = p.Meta.CreatedAt.Local().Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-22s  %-32s  %s\n", p.Name, credFrom, created)
	}
	return nil
}

// shallowProfileDeleteCmd deletes a shallow profile.
var shallowProfileDeleteCmd = &cobra.Command{
	Use:     "delete <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a shallow profile",
	Long: `Delete a shallow profile. Removes the entire ~/orch-homes/<name>/ tree.
Symlinks inside it are removed without following them, so your real HOME is safe.

Examples:
  caam shallow-profile delete alice
  caam shallow-profile delete alice --force
  caam shallow-profile delete alice --json`,
	Args: cobra.ExactArgs(1),
	RunE: runShallowProfileDelete,
}

func init() {
	shallowProfileDeleteCmd.Flags().Bool("force", false, "skip confirmation prompt")
	shallowProfileDeleteCmd.Flags().Bool("json", false, "output as JSON")
}

type shallowDeleteOutput struct {
	Success bool   `json:"success"`
	Name    string `json:"name"`
	Error   string `json:"error,omitempty"`
}

func runShallowProfileDelete(cmd *cobra.Command, args []string) error {
	name := args[0]
	jsonOut, _ := cmd.Flags().GetBool("json")
	force, _ := cmd.Flags().GetBool("force")

	emit := func(err error) error {
		if jsonOut {
			out := shallowDeleteOutput{Name: name, Success: err == nil}
			if err != nil {
				out.Error = err.Error()
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		return err
	}

	mgr, err := resolveShallowManager(cmd)
	if err != nil {
		return emit(fmt.Errorf("init shallow manager: %w", err))
	}

	if _, err := mgr.Get(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emit(fmt.Errorf("shallow profile %q does not exist", name))
		}
		return emit(err)
	}

	if !force && !jsonOut {
		fmt.Fprintf(cmd.OutOrStdout(), "Delete shallow profile %q? [y/N]: ", name)
		var confirm string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &confirm)
		if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
	}

	if err := mgr.Delete(name); err != nil {
		return emit(fmt.Errorf("delete shallow profile: %w", err))
	}

	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(shallowDeleteOutput{Name: name, Success: true})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Deleted shallow profile %q\n", name)
	return nil
}

// shallowSpawnCmd sets HOME=<orch-homes>/<name> and execs the requested command.
var shallowSpawnCmd = &cobra.Command{
	Use:   "shallow-spawn <name> -- <cmd> [args...]",
	Short: "Run a command under a shallow profile's HOME",
	Long: `Set HOME (and SHALLOW_PROFILE) to the named shallow profile and exec
the given command. Concurrent invocations under different names hit
independent .credentials.json files and can run truly in parallel.

Examples:
  caam shallow-spawn alice -- claude
  caam shallow-spawn alice -- claude --print "explain this codebase"
  caam shallow-spawn alice -- bash -c 'echo $HOME'

Use 'caam shallow-spawn <name> --print-env' to print the environment that
WOULD be applied without executing anything (useful for shell wrappers).`,
	Args: cobra.MinimumNArgs(1),
	RunE: runShallowSpawn,
}

func init() {
	shallowSpawnCmd.Flags().String("base", "", "shallow profiles base dir")
	shallowSpawnCmd.Flags().Bool("print-env", false, "print HOME=... assignments and exit (no exec)")
}

func runShallowSpawn(cmd *cobra.Command, args []string) error {
	name := args[0]
	rest := args[1:]
	printEnv, _ := cmd.Flags().GetBool("print-env")

	mgr, err := resolveShallowManager(cmd)
	if err != nil {
		return fmt.Errorf("init shallow manager: %w", err)
	}

	prof, err := mgr.Get(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("shallow profile %q does not exist (try `caam shallow-profile create %s`)", name, name)
		}
		return fmt.Errorf("load shallow profile: %w", err)
	}

	if printEnv {
		fmt.Fprintf(cmd.OutOrStdout(), "HOME=%s\n", prof.Path)
		fmt.Fprintf(cmd.OutOrStdout(), "SHALLOW_PROFILE=%s\n", name)
		return nil
	}

	if len(rest) == 0 {
		return fmt.Errorf("missing command after %q (use `caam shallow-spawn %s -- claude`)", name, name)
	}

	binPath, err := exec.LookPath(rest[0])
	if err != nil {
		return fmt.Errorf("lookup %q: %w", rest[0], err)
	}

	// Build environment: inherit, then override HOME and add SHALLOW_PROFILE.
	envMap := make(map[string]string, len(os.Environ())+2)
	for _, e := range os.Environ() {
		idx := strings.IndexByte(e, '=')
		if idx <= 0 {
			continue
		}
		envMap[e[:idx]] = e[idx+1:]
	}
	envMap["HOME"] = prof.Path
	envMap["SHALLOW_PROFILE"] = name
	// Defensive: prevent stale CLAUDE_CONFIG_DIR from a parent shell pinning the
	// active session to a path outside the shallow HOME (which would silently
	// re-share the user's real ~/.config/claude-code/auth.json).
	delete(envMap, "CLAUDE_CONFIG_DIR")
	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	// On Unix, exec the target so signals/exit propagate naturally and we
	// don't add a stray caam process to the tree.
	return spawnExec(binPath, rest, envSlice)
}

// spawnExec replaces the current process image with the target on Unix.
// Wrapped in a function so tests can inject a fake.
var spawnExec = func(binPath string, args []string, env []string) error {
	return syscall.Exec(binPath, args, env)
}
