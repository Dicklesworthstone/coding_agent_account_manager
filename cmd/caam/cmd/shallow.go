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

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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
preserving shared state (shell history, git config, ssh keys, conversation
history). Unlike 'caam profile add' which gives each profile a blank,
fully-isolated HOME, shallow profiles only isolate what MUST differ (the
provider's identity files).

Supported providers (--tool, or inferred from --from-vault): claude, codex, agy.
Each provider keeps only its own identity files real and private; everything
else symlinks back to your real HOME.

Layout under ~/orch-homes/<name>/ (claude shown):

  .claude/.credentials.json       (real file — per-identity OAuth tokens)
  .claude/.credentials.lock       (real file — per-identity flock target)
  .claude.json                    (real file — Claude rewrites this on each run)
  .claude/projects, .claude/todos (symlinks → ~/.claude/projects, etc.)
  .bashrc, .gitconfig, .ssh, ...  (symlinks → ~/.bashrc, etc.)

  codex: .codex/auth.json + .codex/config.toml are real (CODEX_HOME is pinned).
  agy:   .gemini/antigravity-cli/antigravity-oauth-token (+ optional Google
         identity files) are real (GEMINI_HOME is pinned).

Spawn under a shallow identity with:

  caam shallow-spawn <name>            # runs that profile's own provider CLI
  caam shallow-spawn <name> -- <cmd>   # runs something else instead

which sets HOME=~/orch-homes/<name> (plus CODEX_HOME/GEMINI_HOME for those
providers) and execs the command. shallow-spawn also creates the profile if it
does not exist yet, so this command is only needed when you want a specific
credential source (--from-vault / --from-file).`,
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
	Long: `Create a new shallow profile. Provisions the symlink farm and copies the
provider's primary credential into the shallow HOME (claude .credentials.json,
codex auth.json, or the agy antigravity-oauth-token).

Provider:
  --tool claude|codex|agy   Selects the layout. Inferred from --from-vault.
                            Defaults to claude when neither is given.

Credential source (one of):
  --from-vault <tool>/<profile>   Use an existing caam vault profile (infers --tool)
  --from-file <path>              Copy the primary auth file from a path (needs --tool
                                  for non-claude providers)
  (none)                          Leave the credential empty; populate later via login

Examples:
  caam shallow-profile create alice --from-vault claude/alice@example.com
  caam shallow-profile create codex-bob --from-vault codex/bob --json
  caam shallow-profile create agy-carol --from-vault agy/carol --json
  caam shallow-profile create codex-x --tool codex --from-file /tmp/auth.json
  caam shallow-profile create scratch                 # empty credentials (claude)
  caam shallow-profile create alice --force           # overwrite existing
  caam shallow-profile create alice --base /tmp/test-orch-homes`,
	Args: cobra.ExactArgs(1),
	RunE: runShallowProfileCreate,
}

func init() {
	shallowProfileCreateCmd.Flags().String("tool", "", "provider for this shallow profile: claude (default), codex, or agy. Inferred from --from-vault <tool>/<profile>.")
	shallowProfileCreateCmd.Flags().String("from-vault", "", "credential source: <tool>/<profile> from caam's vault (e.g. claude/alice@example.com, codex/bob, agy/carol)")
	shallowProfileCreateCmd.Flags().String("from-file", "", "credential source: arbitrary path to the provider's primary auth file (requires --tool for non-claude)")
	shallowProfileCreateCmd.Flags().String("from-claude-json", "", "optional path to copy as <home>/.claude.json (claude only; defaults to ~/.claude.json)")
	shallowProfileCreateCmd.Flags().Bool("force", false, "overwrite an existing shallow profile")
	shallowProfileCreateCmd.Flags().Bool("json", false, "output as JSON")
}

type shallowCreateOutput struct {
	Success        bool   `json:"success"`
	Name           string `json:"name"`
	Provider       string `json:"provider,omitempty"`
	Path           string `json:"path"`
	CredentialPath string `json:"credential_path,omitempty"`
	CredentialFrom string `json:"credential_from,omitempty"`
	Error          string `json:"error,omitempty"`
}

func runShallowProfileCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	jsonOut, _ := cmd.Flags().GetBool("json")
	force, _ := cmd.Flags().GetBool("force")
	tool, _ := cmd.Flags().GetString("tool")
	tool = strings.ToLower(strings.TrimSpace(tool))
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

	// Determine the provider and resolve the credential source(s).
	provider := tool
	switch {
	case fromVault != "":
		vaultProvider, primary, extras, vaultClaudeJSON, label, err := resolveVaultProvider(fromVault)
		if err != nil {
			return emit(err)
		}
		if tool != "" && tool != vaultProvider {
			return emit(fmt.Errorf("--tool %q conflicts with --from-vault tool %q", tool, vaultProvider))
		}
		provider = vaultProvider
		opts.CredentialSource = primary
		opts.ExtraSources = extras
		opts.CredentialFromLabel = label
		// Prefer the vault profile's own saved .claude.json (which carries the
		// matching onboarding/readiness state for these credentials) over the
		// real HOME's, unless the user overrode it with --from-claude-json
		// (issue #80).
		if opts.SourceClaudeJSON == "" && vaultClaudeJSON != "" {
			opts.SourceClaudeJSON = vaultClaudeJSON
		}
	case fromFile != "":
		if provider == "" {
			provider = "claude"
		}
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
		if provider == "" {
			provider = "claude"
		}
		// No credential source — terse stderr nudge unless json.
		if !jsonOut {
			fmt.Fprintln(cmd.ErrOrStderr(), "note: no --from-vault/--from-file given; the primary credential will be empty.")
			fmt.Fprintln(cmd.ErrOrStderr(), "      Populate it before running 'shallow-spawn' (e.g. by signing in inside the shallow HOME).")
		}
	}
	opts.Provider = provider
	output.Provider = provider

	home, err := mgr.Create(name, opts)
	if err != nil {
		return emit(fmt.Errorf("create shallow profile: %w", err))
	}

	output.Path = home
	output.CredentialFrom = opts.CredentialFromLabel
	if credPath, perr := mgr.CredentialPath(name); perr == nil {
		output.CredentialPath = credPath
	}
	if jsonOut {
		output.Success = true
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Created shallow profile %q (provider: %s)\n", name, provider)
	fmt.Fprintf(cmd.OutOrStdout(), "  Path: %s\n", home)
	if opts.CredentialFromLabel != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Credentials: %s\n", opts.CredentialFromLabel)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nNext steps:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  caam shallow-spawn %s        # opens %s under this profile\n", name, shallowSpawnHintBin(provider))
	return nil
}

// shallowSpawnHintBin maps a provider to the binary a user most likely wants to
// run under the shallow profile, for the "Next steps" hint.
func shallowSpawnHintBin(provider string) string {
	switch shallow.NormalizeProvider(provider) {
	case "codex":
		return "codex"
	case "agy":
		return "agy"
	default:
		return "claude"
	}
}

// resolveVaultProvider parses a "<tool>/<profile>" --from-vault spec and returns
// the provider id, the absolute path to the profile's PRIMARY credential file,
// a map of optional extra source files (dest-relpath -> source-path) for
// multi-file providers, the path to the vault profile's saved .claude.json
// state file (claude only, "" when absent — issue #80), and a human-readable
// label. The primary file must exist; optional files are included only when
// present.
func resolveVaultProvider(spec string) (provider, primary string, extras map[string]string, claudeState, label string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", nil, "", "", fmt.Errorf("--from-vault requires <tool>/<profile>")
	}
	parts := strings.SplitN(spec, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", nil, "", "", fmt.Errorf("--from-vault must be in the form <tool>/<profile>, got %q", spec)
	}
	tool := strings.ToLower(strings.TrimSpace(parts[0]))
	prof := strings.TrimSpace(parts[1])
	if vault == nil {
		return "", "", nil, "", "", fmt.Errorf("vault not initialized")
	}
	dir := vault.ProfilePath(tool, prof)
	label = "vault:" + tool + "/" + prof

	switch tool {
	case "claude":
		primary = filepath.Join(dir, ".credentials.json")
		if _, err := os.Stat(primary); err != nil {
			return "", "", nil, "", "", fmt.Errorf("vault profile %s/%s missing .credentials.json: %w", tool, prof, err)
		}
		// The vault also snapshots ~/.claude.json alongside the credentials.
		// That snapshot carries the onboarding/readiness state that MATCHES
		// these credentials, so prefer it as the shallow profile's state seed
		// when it exists (issue #80). Optional: older vault profiles may lack
		// it, in which case the real-HOME/skeleton fallback still applies.
		if st := filepath.Join(dir, ".claude.json"); fileIsRegular(st) {
			claudeState = st
		}
	case "codex":
		primary = filepath.Join(dir, "auth.json")
		if _, err := os.Stat(primary); err != nil {
			return "", "", nil, "", "", fmt.Errorf("vault profile %s/%s missing auth.json: %w", tool, prof, err)
		}
	case "agy":
		primary = filepath.Join(dir, "antigravity-oauth-token")
		if _, err := os.Stat(primary); err != nil {
			return "", "", nil, "", "", fmt.Errorf("vault profile %s/%s missing antigravity-oauth-token: %w", tool, prof, err)
		}
		// Optional Google identity files, mapped to their shallow destinations.
		optional := map[string]string{
			"google_accounts.json": ".gemini/google_accounts.json",
			"oauth_creds.json":     ".gemini/oauth_creds.json",
			"settings.json":        ".gemini/antigravity-cli/settings.json",
		}
		extras = map[string]string{}
		for vaultBase, destRel := range optional {
			src := filepath.Join(dir, vaultBase)
			if _, e := os.Stat(src); e == nil {
				extras[destRel] = src
			}
		}
	default:
		return "", "", nil, "", "", fmt.Errorf("--from-vault does not support tool %q for shallow profiles (supported: %s)", tool, strings.Join(shallow.SupportedProviders(), ", "))
	}
	return tool, primary, extras, claudeState, label, nil
}

// fileIsRegular reports whether path exists and is a regular file (following
// symlinks). Used for optional vault-side files where absence is fine.
func fileIsRegular(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
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
	Name     string `json:"name"`
	Path     string `json:"path"`
	Provider string `json:"provider,omitempty"`
	// LoggedIn reports whether the profile's private credential file holds a
	// credential right now; Identity names the account (claude only) from
	// the profile's own .claude.json. Both are read live, unlike
	// CredentialFrom, which only records where the credential came from at
	// creation time and stays "(none)" for a profile that logged in later.
	LoggedIn       bool      `json:"logged_in"`
	Identity       string    `json:"identity,omitempty"`
	CredentialFrom string    `json:"credential_from,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

// shallowLoginState reads a shallow profile's live login state: its provider,
// whether its private credential file holds a credential, and (claude only)
// which account that is.
func shallowLoginState(mgr *shallow.Manager, name, home string) (provider string, loggedIn bool, identity string) {
	provider, _ = mgr.ResolveProvider(name)
	credPath, err := mgr.CredentialPath(name)
	if err != nil {
		return provider, false, ""
	}
	loggedIn = shallowCredentialPresent(credPath)
	if loggedIn && shallow.NormalizeProvider(provider) == "claude" {
		identity = authfile.ClaudeIdentityLabel(authfile.ClaudeIdentityKeysFromFile(filepath.Join(home, ".claude.json")))
	}
	return provider, loggedIn, identity
}

// shallowCredentialPresent reports whether path holds a non-empty JSON
// object: the shape every provider's credential file takes once a login has
// happened, and what an empty placeholder written at creation lacks.
func shallowCredentialPresent(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(data, &obj) == nil && len(obj) > 0
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
			item.Provider, item.LoggedIn, item.Identity = shallowLoginState(mgr, p.Name, p.Path)
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
	fmt.Fprintf(cmd.OutOrStdout(), "%-22s  %-32s  %-24s  %s\n", "NAME", "LOGIN", "SOURCE", "CREATED")
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
		_, loggedIn, identity := shallowLoginState(mgr, p.Name, p.Path)
		login := "not logged in"
		switch {
		case identity != "":
			login = identity
		case loggedIn:
			login = "logged in"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-22s  %-32s  %-24s  %s\n", p.Name, login, credFrom, created)
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
	Use:   "shallow-spawn <name> [-- <cmd> [args...]]",
	Short: "Open a shallow profile's provider CLI (or any command) under its HOME",
	Long: `Set HOME (and SHALLOW_PROFILE) to the named shallow profile and exec a
command under it. Concurrent invocations under different names hit
independent .credentials.json files and can run truly in parallel.

  caam shallow-spawn alice

is the short form: with no '-- <cmd>' section it runs the profile's OWN
provider CLI (claude for a claude profile, codex for codex, agy for agy), so
"open Claude as alice in this terminal" is one command. Pass '-- <cmd>' to run
anything else instead; explicit commands behave exactly as before.

Create on first use: an unknown <name> is provisioned as an EMPTY shallow
profile (layout from --tool, default claude) and the session starts right
away, so the first run of a new identity is a login prompt rather than an
error. Credentials are never copied from the vault here — two profiles sharing
one refresh-token family invalidate each other (issue #19). Use
'caam shallow-profile create <name> --from-vault <tool>/<profile>' when a vault
copy really is what you want.

Double-spend guard (claude only): if the profile is already logged in as the
SAME account that is active in your real HOME, the spawn is refused, because
both sessions would draw down one subscription's quota. Pass --force to
override. The check is skipped when either side has no oauthAccount recorded,
and for codex/agy, which keep no comparable account identity on disk.

Each spawn also backfills missing symlinks for user-installed skills
(~/.claude/skills, ~/.codex/skills, ~/.gemini/skills) into the shallow
profile, so spawned sessions see the same skill library as direct ones.
Auth/config files stay real and private; nothing is copied or overwritten.

Examples:
  caam shallow-spawn alice                     # open Claude as alice (creating alice if new)
  caam shallow-spawn cx --tool codex           # first use: codex layout, then open codex
  caam shallow-spawn alice -- claude --print "explain this codebase"
  caam shallow-spawn alice -- bash -c 'echo $HOME'
  caam shallow-spawn alice --force             # open it even if alice is the live account
  caam shallow-spawn codex-bob --reload-daemon -- codex
  caam shallow-spawn codex-bob --effort xhigh -- codex --model gpt-5.6-sol

Use 'caam shallow-spawn <name> --print-env' to print the environment that
WOULD be applied without executing anything (useful for shell wrappers). It is
a strict dry run: it never creates a missing profile (an unknown name is an
error there) and the double-spend guard does not apply to it.

--reload-daemon (codex only) mirrors 'caam activate/next': after the on-disk
auth swap it SIGTERMs any running codex app-server/mcp-server daemon so the
new identity takes effect. It is a shallow-spawn flag (place it BEFORE '--'),
consumed here and never forwarded to the spawned command.

--effort <level> (codex only) sets the model reasoning effort for the spawned
codex command. Codex has no '--effort' CLI flag (that spelling is a Claude-ism);
its knob is the config key 'model_reasoning_effort', so caam translates
'--effort xhigh' into '-c model_reasoning_effort=xhigh' injected right after
the codex binary (issue #63). Like --reload-daemon it is a shallow-spawn flag:
place it BEFORE '--'; it errors when the spawned command is not codex.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runShallowSpawn,
}

func init() {
	shallowSpawnCmd.Flags().String("base", "", "shallow profiles base dir")
	shallowSpawnCmd.Flags().Bool("print-env", false, "print HOME=... assignments and exit (no exec)")
	shallowSpawnCmd.Flags().Bool("reload-daemon", false, "for codex: SIGTERM a running codex app-server/mcp-server daemon so the switched auth takes effect (it respawns on next use)")
	shallowSpawnCmd.Flags().String("effort", "", "for codex: model reasoning effort (e.g. minimal|low|medium|high|xhigh), injected as '-c model_reasoning_effort=<effort>' since codex has no --effort flag")
	shallowSpawnCmd.Flags().String("tool", "claude", "provider layout to use when creating a profile that does not exist yet: claude, codex, or agy")
	shallowSpawnCmd.Flags().Bool("force", false, "spawn even when this profile is the Claude account already active in your real HOME (double-spend guard override)")
	shallowSpawnCmd.Flags().Bool("allow-agent-view", false, "for claude: keep Claude Code's Agent View / background supervisor enabled instead of injecting CLAUDE_CODE_DISABLE_AGENT_VIEW=1 (opts back into Agent View, accepting that its cross-session supervisor daemon can bypass per-identity auth isolation — see issue #49)")
}

// shallowCodexDaemonCheck is the daemon detect/reload hook used by shallow-spawn.
// It defaults to checkCodexDaemonScoped and is a package var so tests can observe
// the (tool, reload, codexHome) arguments and stub out real host process scanning
// — mirroring the spawnExec seam below. shallow-spawn always passes the TARGET
// profile's CODEX_HOME so a reload only touches that profile's daemons (#47),
// unlike the host-wide activate/next path.
var shallowCodexDaemonCheck = checkCodexDaemonScoped

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
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load shallow profile: %w", err)
		}
		// Create on first use. --print-env is a strict dry run, so it reports
		// the missing profile instead of provisioning one behind the user's
		// back; a real spawn provisions and continues.
		if printEnv {
			return fmt.Errorf("shallow profile %q does not exist; --print-env is a dry run and never creates one — run `caam shallow-spawn %s` (add --tool codex|agy for a non-claude layout) to create it and start the session", name, name)
		}
		if prof, err = createShallowProfileForSpawn(cmd, mgr, name); err != nil {
			return err
		}
	}

	// Provider drives the env-isolation policy (which provider-home var to pin or
	// scrub). Resolve it fail-closed: if the metadata is unreadable and the
	// provider cannot be unambiguously inferred from disk, refuse to spawn rather
	// than silently assuming claude — which would skip CODEX_HOME/GEMINI_HOME
	// pinning and could leak the real ~/.codex / ~/.gemini identity (issue #43).
	provider, err := mgr.ResolveProvider(name)
	if err != nil {
		return err
	}
	// --tool only chooses a layout for a profile we are creating. Naming an
	// existing profile with a conflicting --tool is a mistake worth surfacing:
	// silently ignoring it would look like the layout had been changed.
	if cmd.Flags().Changed("tool") {
		wanted, _ := cmd.Flags().GetString("tool")
		if norm := shallow.NormalizeProvider(wanted); norm != shallow.NormalizeProvider(provider) {
			return fmt.Errorf("--tool %q conflicts with existing shallow profile %q (provider: %s); --tool only applies when the profile is created on first use", wanted, name, provider)
		}
	}
	// Agent View policy (#49): disable Claude Code's cross-session background
	// supervisor for shallow claude sessions unless the user opted back in with
	// --allow-agent-view or has already exported CLAUDE_CODE_DISABLE_AGENT_VIEW
	// themselves (an explicit user choice we never override).
	allowAgentView, _ := cmd.Flags().GetBool("allow-agent-view")
	_, disableAgentViewSet := os.LookupEnv("CLAUDE_CODE_DISABLE_AGENT_VIEW")
	set, scrub := shallow.SpawnEnv(provider, prof.Path, name, allowAgentView, disableAgentViewSet)

	if printEnv {
		// Print the variables that WOULD be set, as clean KEY=VALUE lines, with
		// HOME and SHALLOW_PROFILE first for stable output and the rest sorted.
		fmt.Fprintf(cmd.OutOrStdout(), "HOME=%s\n", set["HOME"])
		fmt.Fprintf(cmd.OutOrStdout(), "SHALLOW_PROFILE=%s\n", set["SHALLOW_PROFILE"])
		extra := make([]string, 0, len(set))
		for k := range set {
			if k == "HOME" || k == "SHALLOW_PROFILE" {
				continue
			}
			extra = append(extra, k)
		}
		sort.Strings(extra)
		for _, k := range extra {
			fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", k, set[k])
		}
		return nil
	}

	// Default command: with no `-- <cmd>` section, run the profile's own
	// provider CLI. This is the whole point of the short form — `caam
	// shallow-spawn alice` means "open Claude as alice, here, now".
	if len(rest) == 0 {
		rest = []string{shallowSpawnHintBin(provider)}
	}

	// Double-spend guard: refuse to open a claude profile that is already the
	// live account, unless the user insists with --force.
	force, _ := cmd.Flags().GetBool("force")
	if !force {
		if label, conflict := shallowSpawnDoubleSpend(provider, prof.Path); conflict {
			who := name
			if label != "" {
				who = fmt.Sprintf("%s (%s)", name, label)
			}
			return fmt.Errorf("%q is the account already active in your real HOME (~/.claude.json); running it here too would spend the same quota twice. Run %q directly in this terminal, or pass --force", who, shallowSpawnHintBin(provider))
		}
	}

	// Skill-share repair (#56): user-installed skills (e.g. ~/.codex/skills
	// populated by jsm) are workflow content, not identity state, but they can
	// drift out of a shallow profile — the real skills dir may postdate profile
	// creation, and codex materializes a REAL <shallow>/.codex/skills holding
	// only its bundled ".system" entries, hiding every user skill from spawned
	// sessions. Backfill missing skill symlinks before exec so shallow sessions
	// see the same skill library as direct ones. Best-effort: a repair failure
	// is reported but never blocks the spawn.
	if created, rerr := mgr.RepairSkillShare(name); rerr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not share user skills into shallow profile %q: %v\n", name, rerr)
	} else if len(created) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: linked %d user skill entr%s from your real HOME into shallow profile %q\n",
			len(created), map[bool]string{true: "y", false: "ies"}[len(created) == 1], name)
	}

	// Codex daemon caveat (#21, #45): a long-lived `codex app-server`/`mcp-server`
	// caches auth.json in memory, so a shallow codex session could be served by a
	// daemon attached to a DIFFERENT identity. By default we only warn (to stderr)
	// so the user knows to restart it; with --reload-daemon we SIGTERM it here so
	// it respawns with the shallow auth. --reload-daemon is a shallow-spawn flag
	// (parsed before `-- <cmd>`); it is consumed here and never forwarded to the
	// child, and it triggers daemon action only when the resolved provider is
	// codex (for non-codex profiles it is accepted but does nothing).
	reloadDaemon, _ := cmd.Flags().GetBool("reload-daemon")
	if shallow.NormalizeProvider(provider) == "codex" {
		// Scope the daemon check to THIS profile's CODEX_HOME so a reload only
		// affects daemons serving this profile, not a concurrent shallow codex
		// profile (issue #47). set["CODEX_HOME"] is pinned by SpawnEnv above.
		printCodexDaemonWarning(cmd.ErrOrStderr(), shallowCodexDaemonCheck("codex", reloadDaemon, set["CODEX_HOME"]))
	}

	// Reasoning-effort plumb-through (#63): codex has no `--effort` CLI flag
	// (that spelling comes from Claude-side muscle memory); its knob is the
	// `model_reasoning_effort` config key. Translate our --effort flag into the
	// `-c` form codex actually accepts, injected right after the binary so it
	// applies regardless of any codex subcommand that follows.
	effort, _ := cmd.Flags().GetString("effort")
	if rest, err = injectCodexEffort(rest, effort); err != nil {
		return err
	}

	binPath, err := exec.LookPath(rest[0])
	if err != nil {
		return fmt.Errorf("lookup %q: %w", rest[0], err)
	}

	// Build environment: inherit, then apply the provider's overrides and scrub
	// any inherited variables that could leak the real identity back in.
	envMap := make(map[string]string, len(os.Environ())+len(set))
	for _, e := range os.Environ() {
		idx := strings.IndexByte(e, '=')
		if idx <= 0 {
			continue
		}
		envMap[e[:idx]] = e[idx+1:]
	}
	for k, v := range set {
		envMap[k] = v
	}
	for _, k := range scrub {
		delete(envMap, k)
	}
	envMap["PATH"] = prependShallowLocalBin(envMap["PATH"], prof.Path)
	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	// On Unix, exec the target so signals/exit propagate naturally and we
	// don't add a stray caam process to the tree.
	return spawnExec(binPath, rest, envSlice)
}

// createShallowProfileForSpawn provisions the EMPTY shallow profile that
// shallow-spawn creates on first use, using the same shallow.Manager.Create
// path as `caam shallow-profile create <name> --tool <tool>` with no credential
// source — the profile's private credential file is left empty so the first
// session logs in as the account the user actually wants there.
//
// It deliberately offers no --from-vault equivalent. Seeding a shallow profile
// from a vault snapshot duplicates one refresh-token family across two homes,
// and Claude rotates the refresh token on use, so whichever side refreshes
// second is logged out (issue #19). Copying a credential must stay an explicit
// `shallow-profile create --from-vault` decision, never an implicit side effect
// of opening a terminal.
func createShallowProfileForSpawn(cmd *cobra.Command, mgr *shallow.Manager, name string) (*shallow.Profile, error) {
	tool, _ := cmd.Flags().GetString("tool")
	provider := shallow.NormalizeProvider(tool)
	if _, err := mgr.Create(name, shallow.CreateOptions{Provider: provider}); err != nil {
		return nil, fmt.Errorf("create shallow profile %q on first use: %w", name, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"created shallow profile %q (empty, provider: %s): the first run will ask you to log in as that account\n",
		name, provider)
	prof, err := mgr.Get(name)
	if err != nil {
		return nil, fmt.Errorf("load shallow profile %q after creating it: %w", name, err)
	}
	return prof, nil
}

// shallowSpawnDoubleSpend reports whether spawning the shallow profile rooted
// at profileHome would run the SAME account that is already active in the real
// HOME — two live sessions drawing down one subscription's quota. It returns
// the profile's human-facing identity (email, else account uuid) alongside the
// verdict so the refusal can name the account.
//
// Claude only: codex and agy keep no account identity next to their
// credentials, so there is nothing to compare and we never invent one.
//
// A conflict requires BOTH of:
//
//  1. The profile is actually logged in — its private .credentials.json holds
//     a non-empty JSON object. This matters because a newly created shallow
//     profile seeds its .claude.json from the real HOME (that is where its
//     onboarding state comes from), so identity alone would flag every
//     brand-new profile as a double spend on its very first, login-only run.
//  2. The profile's oauthAccount and the live ~/.claude.json's oauthAccount
//     share an identity key — accountUuid, else emailAddress.
//
// Either side missing an oauthAccount yields no conflict: an unknown identity
// is never evidence of a match. The live path is resolved through
// authfile.ClaudeAuthFiles() so this agrees with every other caam command
// about which file holds the live identity.
func shallowSpawnDoubleSpend(provider, profileHome string) (label string, conflict bool) {
	if shallow.NormalizeProvider(provider) != "claude" {
		return "", false
	}
	if !shallowCredentialPresent(filepath.Join(profileHome, ".claude", ".credentials.json")) {
		return "", false
	}
	profileKeys := authfile.ClaudeIdentityKeysFromFile(filepath.Join(profileHome, ".claude.json"))
	liveKeys := authfile.ClaudeLiveIdentityKeys(authfile.ClaudeAuthFiles())
	if !authfile.IdentityKeysIntersect(profileKeys, liveKeys) {
		return "", false
	}
	return authfile.ClaudeIdentityLabel(profileKeys), true
}

// prependShallowLocalBin puts <home>/.local/bin first on PATH when that
// directory exists under the shallow HOME. It is a symlink to the real
// ~/.local/bin, so nothing new becomes runnable; but tools that check
// "$HOME/.local/bin is on PATH" as a literal string (Claude Code's system
// diagnostics warn about its native install otherwise) see the path they
// expect under the shallow HOME. A PATH that already lists it is returned
// unchanged.
func prependShallowLocalBin(path, home string) string {
	bin := filepath.Join(home, ".local", "bin")
	if st, err := os.Stat(bin); err != nil || !st.IsDir() {
		return path
	}
	for _, p := range filepath.SplitList(path) {
		if p == bin {
			return path
		}
	}
	if path == "" {
		return bin
	}
	return bin + string(os.PathListSeparator) + path
}

// injectCodexEffort translates shallow-spawn's --effort flag into the config
// override codex actually understands (`-c model_reasoning_effort=<effort>`),
// injected immediately after the codex binary in argv (issue #63).
//
// It is fail-closed on misuse: --effort with a non-codex command is an error
// (Claude & friends have no equivalent knob we could safely map), and an argv
// that already carries its own model_reasoning_effort override is left alone —
// the user's explicit spelling wins and we error rather than silently stacking
// two conflicting overrides. An empty effort is a no-op passthrough.
func injectCodexEffort(argv []string, effort string) ([]string, error) {
	effort = strings.TrimSpace(effort)
	if effort == "" || len(argv) == 0 {
		return argv, nil
	}
	if filepath.Base(argv[0]) != "codex" {
		return nil, fmt.Errorf("--effort is codex-only (it maps to codex's model_reasoning_effort config key), but the spawned command is %q", argv[0])
	}
	for _, a := range argv[1:] {
		if strings.Contains(a, "model_reasoning_effort") {
			return nil, fmt.Errorf("--effort %s conflicts with %q already present in the codex command; drop one of them", effort, a)
		}
		if a == "--effort" || strings.HasPrefix(a, "--effort=") {
			return nil, fmt.Errorf("codex has no --effort flag; use `caam shallow-spawn <name> --effort %s -- codex ...` (before the --) and caam will inject `-c model_reasoning_effort=%s` for you", effort, effort)
		}
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], "-c", "model_reasoning_effort="+effort)
	out = append(out, argv[1:]...)
	return out, nil
}

// spawnExec replaces the current process image with the target on Unix.
// Wrapped in a function so tests can inject a fake.
var spawnExec = func(binPath string, args []string, env []string) error {
	return syscall.Exec(binPath, args, env)
}
