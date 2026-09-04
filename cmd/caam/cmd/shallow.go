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

  caam shallow-spawn <name>            # runs the profile's own provider CLI
  caam shallow-spawn <name> -- <cmd>   # runs any other command

which sets HOME=~/orch-homes/<name> (plus CODEX_HOME/GEMINI_HOME for those
providers) and execs the command.`,
}

func init() {
	shallowProfileCmd.PersistentFlags().String("base", "", "shallow profiles base dir (default: $CAAM_SHALLOW_HOMES_DIR or ~/orch-homes)")
	shallowProfileCmd.AddCommand(shallowProfileCreateCmd)
	shallowProfileCmd.AddCommand(shallowProfileListCmd)
	shallowProfileCmd.AddCommand(shallowProfileDeleteCmd)
	shallowProfileCmd.AddCommand(shallowProfileSyncConfigCmd)
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
	fmt.Fprintf(cmd.OutOrStdout(), "  caam shallow-spawn %s              # opens %s under this profile\n", name, shallowSpawnHintBin(provider))
	fmt.Fprintf(cmd.OutOrStdout(), "  caam shallow-spawn %s -- <cmd>     # or any other command\n", name)
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
	Use:   "shallow-spawn <name> [-- <cmd> [args...]]",
	Short: "Open a shallow profile's provider CLI (or any command) under its HOME",
	Long: `Set HOME (and SHALLOW_PROFILE) to the named shallow profile and exec a
command under it. Concurrent invocations under different names hit
independent .credentials.json files and can run truly in parallel.

With no '-- <cmd>' section the profile's own provider CLI is run (claude for
a claude profile, codex for codex, agy for agy), so "open Claude as alice in
this terminal" is just 'caam shallow-spawn alice'. Pass '-- <cmd> [args...]'
to run anything else under the profile instead.

Each spawn also backfills missing symlinks for user-installed skills
(~/.claude/skills, ~/.codex/skills, ~/.gemini/skills) into the shallow
profile, so spawned sessions see the same skill library as direct ones.
Auth files stay real and private; nothing else is copied or overwritten.

Each spawn additionally refreshes the SHARED configuration of the profile
from your real HOME — the main lane is the source of truth for configuration:

  claude  .claude.json preferences (theme, editor mode, notification channel),
          user-scope mcpServers, and per-project trust / allowedTools / MCP
          settings. Identity (oauthAccount), usage cache and session state are
          never touched.
  codex   .codex/config.toml root settings and whole tables ([mcp_servers.*],
          [features], [skills], [hooks], [model_providers.*]). Each MCP server
          is replaced as one unit, so a stale command/args pair cannot survive
          beside a new url. Hook trust ([hooks.state]), workspace trust
          ([projects]) and dismissed notices ([notice]) stay the profile's own,
          and cli_auth_credentials_store = "file" is re-enforced.

Pass --no-sync-config to skip it, or run it on demand with
'caam shallow-profile sync-config <name>'.

An unknown name is an ERROR, not a new profile: a typo would otherwise become
a fresh empty identity and a login prompt for the wrong account. The error
names the closest existing profile. Pass --create to actually provision one
(with EMPTY credentials — nothing is copied from the vault, because two homes
sharing one refresh-token family invalidate each other); --tool picks the
layout. --print-env never creates anything.

Examples:
  caam shallow-spawn alice                     # open claude as alice
  caam shallow-spawn alice --create            # first run of a new identity: creates, then logs in
  caam shallow-spawn cx --create --tool codex  # same, with a codex layout
  caam shallow-spawn alice -- claude --print "explain this codebase"
  caam shallow-spawn alice -- bash -c 'echo $HOME'
  caam shallow-spawn alice --no-sync-config   # keep this profile's own theme etc.
  caam shallow-spawn codex-bob --reload-daemon -- codex
  caam shallow-spawn codex-bob --effort xhigh -- codex --model gpt-5.6-sol

Use 'caam shallow-spawn <name> --print-env' to print the environment that
WOULD be applied without executing anything (useful for shell wrappers).

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
	shallowSpawnCmd.Flags().Bool("no-sync-config", false, "for claude: do not refresh shared preferences (theme, editor mode, notification channel, user/project MCP servers, project trust and tool approvals) in the profile's .claude.json from your real ~/.claude.json before exec")
	shallowSpawnCmd.Flags().Bool("create", false, "create the shallow profile (with EMPTY credentials) if it does not exist yet, then start the session; without this a name that does not exist is an error, so a typo cannot silently become a new identity")
	shallowSpawnCmd.Flags().String("tool", "", "provider layout to use with --create: claude (default), codex, or agy. On an existing profile of a different provider it is an error, not a no-op.")
	shallowSpawnCmd.Flags().Bool("allow-agent-view", false, "for claude: keep Claude Code's Agent View / background supervisor enabled instead of injecting CLAUDE_CODE_DISABLE_AGENT_VIEW=1 (opts back into Agent View, accepting that its cross-session supervisor daemon can bypass per-identity auth isolation — see issue #49)")
}

// shallowProfileSyncConfigCmd reconciles a shallow profile's shared
// configuration with the real HOME's (issues #93, #103).
var shallowProfileSyncConfigCmd = &cobra.Command{
	Use:   "sync-config [name]",
	Short: "Refresh a shallow profile's shared configuration from your real HOME",
	Long: `Reconcile the SHARED settings of a shallow profile with your real HOME's,
without touching its credentials or its own runtime state.

A shallow profile's provider configuration is a real, private file — it has to
be, because the provider writes identity and per-home state into it — so it
diverges from your real HOME the moment you change something there. The most
common casualty is an MCP server: change a real-home entry from the stdio
transport to streamable HTTP and every codex profile keeps the old command/args
block, after which codex refuses to parse its config at all
("url is not supported for stdio in mcp_servers.<name>").

What is refreshed, per provider:

  claude (<home>/.claude.json)
      preferences (theme, editor mode, notification channel, autoUpdates …),
      user-scope mcpServers, and per-project trust / allowedTools / MCP settings

  codex (<home>/.codex/config.toml)
      root settings (model, reasoning effort, personality, notify, …) and whole
      tables: [mcp_servers.*], [features], [skills], [hooks], [model_providers.*]
      and the rest. Each MCP server is replaced as ONE unit, so a stale
      command/args pair can never survive beside a new url.

What is never touched:

  claude   oauthAccount, usage caches, prompt history, per-project session state
  codex    [hooks.state.*] (hook trust), [projects.*] (workspace trust),
           [notice.*] (dismissed notices), and auth.json
  both     the profile's credentials, and any setting the real HOME does not
           define — nothing is deleted

cli_auth_credentials_store = "file" is re-enforced on every codex sync.

Comments, key order and formatting are preserved: the edit is a structural
splice, not a rewrite, so untouched regions stay byte-identical. Running it
twice writes nothing the second time.

Examples:
  caam shallow-profile sync-config alice
  caam shallow-profile sync-config --all
  caam shallow-profile sync-config --all --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: runShallowProfileSyncConfig,
}

func init() {
	shallowProfileSyncConfigCmd.Flags().Bool("all", false, "sync every shallow profile")
	shallowProfileSyncConfigCmd.Flags().Bool("json", false, "output as JSON")
}

// shallowSyncConfigResult is one profile's outcome.
type shallowSyncConfigResult struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider,omitempty"`
	Changed  []string `json:"changed"`
	Error    string   `json:"error,omitempty"`
}

type shallowSyncConfigOutput struct {
	Profiles []shallowSyncConfigResult `json:"profiles"`
	Count    int                       `json:"count"`
}

func runShallowProfileSyncConfig(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	jsonOut, _ := cmd.Flags().GetBool("json")

	if all == (len(args) == 1) {
		return fmt.Errorf("give exactly one of <name> or --all")
	}

	mgr, err := resolveShallowManager(cmd)
	if err != nil {
		return fmt.Errorf("init shallow manager: %w", err)
	}

	names := args
	if all {
		profiles, lerr := mgr.List()
		if lerr != nil {
			return fmt.Errorf("list shallow profiles: %w", lerr)
		}
		names = nil
		for _, p := range profiles {
			names = append(names, p.Name)
		}
		sort.Strings(names)
	}

	out := shallowSyncConfigOutput{Profiles: []shallowSyncConfigResult{}}
	var firstErr error
	for _, name := range names {
		res := shallowSyncConfigResult{Name: name, Changed: []string{}}
		provider, perr := mgr.ResolveProvider(name)
		if perr != nil {
			res.Error = perr.Error()
			if firstErr == nil && !all {
				firstErr = perr
			}
			out.Profiles = append(out.Profiles, res)
			continue
		}
		res.Provider = provider

		var changed []string
		var serr error
		switch shallow.NormalizeProvider(provider) {
		case "claude":
			changed, serr = mgr.SyncClaudeConfig(name)
		case "codex":
			changed, serr = mgr.SyncCodexConfig(name)
		default:
			// agy keeps no shared configuration file of its own yet.
		}
		if serr != nil {
			res.Error = serr.Error()
			if firstErr == nil && !all {
				firstErr = serr
			}
		}
		res.Changed = append(res.Changed, changed...)
		out.Profiles = append(out.Profiles, res)
	}
	out.Count = len(out.Profiles)

	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if eerr := enc.Encode(out); eerr != nil {
			return eerr
		}
		return firstErr
	}

	for _, res := range out.Profiles {
		switch {
		case res.Error != "":
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s): error: %s\n", res.Name, res.Provider, res.Error)
		case len(res.Changed) == 0:
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s): already in sync\n", res.Name, res.Provider)
		default:
			// Names only — a value could be a token, a header or a path.
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s): refreshed %d setting%s: %s\n",
				res.Name, res.Provider, len(res.Changed),
				map[bool]string{true: "", false: "s"}[len(res.Changed) == 1],
				strings.Join(res.Changed, ", "))
		}
	}
	return firstErr
}

// mustString reads a string flag, returning "" when the flag is absent (test
// command trees build a reduced flag set).
func mustString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// shallowMissingProfileError is what a bare `caam shallow-spawn <typo>` gets.
//
// The whole hazard of creating on first use is that a mistyped name looks
// exactly like a new identity, so the error carries the two things that tell
// them apart: the closest existing profile, and the flag that would have meant
// "yes, really make a new one".
func shallowMissingProfileError(mgr *shallow.Manager, name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "shallow profile %q does not exist", name)
	if suggestion := nearestShallowProfile(mgr, name); suggestion != "" {
		fmt.Fprintf(&b, "; did you mean %q?", suggestion)
	}
	fmt.Fprintf(&b, "\n  create it and start a session:  caam shallow-spawn %s --create [--tool claude|codex|agy]", name)
	fmt.Fprintf(&b, "\n  or set it up explicitly:        caam shallow-profile create %s", name)
	return fmt.Errorf("%s", b.String())
}

// nearestShallowProfile returns the existing profile name closest to want, or
// "" when nothing is close enough to be worth suggesting. The threshold scales
// with the name's length so short names do not attract unrelated suggestions.
func nearestShallowProfile(mgr *shallow.Manager, want string) string {
	profiles, err := mgr.List()
	if err != nil || len(profiles) == 0 {
		return ""
	}
	limit := 1 + len(want)/4
	if limit > 3 {
		limit = 3
	}
	best, bestDist := "", limit+1
	for _, p := range profiles {
		d := editDistance(strings.ToLower(want), strings.ToLower(p.Name))
		if d < bestDist || (d == bestDist && p.Name < best) {
			best, bestDist = p.Name, d
		}
	}
	if bestDist > limit {
		return ""
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// createShallowForSpawn provisions an EMPTY shallow profile for --create and
// tells the user, on stderr, exactly what it did.
//
// Credentials are deliberately never seeded from the vault here. Two homes
// sharing one refresh-token family invalidate each other (issue #19), so
// copying credentials into a new lane is a decision that stays explicit
// (`caam shallow-profile create --from-vault`). The first run of a
// --create'd profile is therefore a login prompt, which is the point.
func createShallowForSpawn(cmd *cobra.Command, mgr *shallow.Manager, name, tool string) error {
	supported := false
	for _, p := range shallow.SupportedProviders() {
		if p == shallow.NormalizeProvider(tool) {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("unknown --tool %q (want one of: %s)", tool, strings.Join(shallow.SupportedProviders(), ", "))
	}
	if _, err := mgr.Create(name, shallow.CreateOptions{Provider: tool}); err != nil {
		return fmt.Errorf("create shallow profile %q: %w", name, err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"note: created shallow profile %q (%s) with empty credentials; sign in inside this session.\n"+
			"      Credentials are never copied from the vault into a new lane — two homes sharing one\n"+
			"      refresh-token family invalidate each other. Use `caam shallow-profile create %s\n"+
			"      --from-vault %s/<profile>` if you meant to seed it from an existing account.\n",
		name, tool, name, tool)
	return nil
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

	wantTool := strings.ToLower(strings.TrimSpace(mustString(cmd, "tool")))
	createMissing, _ := cmd.Flags().GetBool("create")

	prof, err := mgr.Get(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Creating on a bare `shallow-spawn <name>` would turn a typo into
			// a brand-new empty profile plus a login prompt for the wrong
			// identity, with the mistyped profile then lingering on disk. So
			// creation is opt-in, and the error for the bare form does the
			// helpful part instead: it names the closest existing profile and
			// the flag that would have created this one.
			if !createMissing {
				return shallowMissingProfileError(mgr, name)
			}
			// --print-env stays a strict dry run: it must never create
			// anything, whatever other flags are present.
			if printEnv {
				return fmt.Errorf("shallow profile %q does not exist; --print-env never creates one (run `caam shallow-spawn %s --create` first)", name, name)
			}
			if wantTool == "" {
				wantTool = "claude"
			}
			if err := createShallowForSpawn(cmd, mgr, name, wantTool); err != nil {
				return err
			}
			if prof, err = mgr.Get(name); err != nil {
				return fmt.Errorf("load shallow profile after creating it: %w", err)
			}
		} else {
			return fmt.Errorf("load shallow profile: %w", err)
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
	// --tool names the layout to create; on a profile that already exists it
	// can only ever be a mistake, so say so rather than silently ignoring it
	// and spawning the wrong provider's CLI.
	if wantTool != "" && shallow.NormalizeProvider(wantTool) != shallow.NormalizeProvider(provider) {
		return fmt.Errorf("shallow profile %q is a %s profile; --tool %s does not apply (drop --tool, or create a separate profile with `caam shallow-profile create <name> --tool %s`)",
			name, provider, wantTool, wantTool)
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

	// Short form: no '-- <cmd>' means "open this profile's own provider CLI".
	if len(rest) == 0 {
		rest = []string{shallowSpawnHintBin(provider)}
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

	// Shared-configuration refresh (#93): a claude profile's .claude.json is a
	// real, private file because it holds the login identity, but it also
	// carries the operator's preferences and per-project approvals, which
	// silently diverge from the real HOME after creation. Copy the allowlisted
	// shared keys from the real ~/.claude.json into the profile before exec so
	// the main lane stays the source of truth for configuration; identity,
	// usage caches and session state stay the profile's own. Best-effort: a
	// failure warns and never blocks the spawn.
	//
	// Codex has the same problem in a sharper form (#103): its config.toml is
	// copied once at creation and never reconciled, so a real-home MCP entry
	// that switches transport leaves the profile with a stdio block beside a
	// url and codex refuses to parse the file at all. Same policy, same
	// opt-out; sections are replaced as units and the profile's own hook
	// trust, project trust and dismissed notices are left alone.
	if noSync, _ := cmd.Flags().GetBool("no-sync-config"); !noSync {
		var (
			changed  []string
			serr     error
			label    string
			normProv = shallow.NormalizeProvider(provider)
		)
		switch normProv {
		case "claude":
			label = "Claude"
			changed, serr = mgr.SyncClaudeConfig(name)
		case "codex":
			label = "Codex"
			changed, serr = mgr.SyncCodexConfig(name)
		}
		if serr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not refresh shared %s configuration in shallow profile %q: %v\n", label, name, serr)
		} else if len(changed) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: refreshed %d shared %s setting%s from your real HOME into shallow profile %q (--no-sync-config to skip)\n",
				len(changed), label, map[bool]string{true: "", false: "s"}[len(changed) == 1], name)
		}
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
	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, k+"="+v)
	}

	// On Unix, exec the target so signals/exit propagate naturally and we
	// don't add a stray caam process to the tree.
	return spawnExec(binPath, rest, envSlice)
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
