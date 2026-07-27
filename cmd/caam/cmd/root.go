// Package cmd implements the CLI commands for caam (Coding Agent Account Manager).
//
// caam manages auth files for AI coding CLIs to enable instant account switching
// for "all you can eat" subscription plans (GPT Pro, Claude Max, Gemini Ultra).
//
// Two modes of operation:
//  1. Auth file swapping (PRIMARY): backup/activate to instantly switch accounts
//  2. Profile isolation: run tools with isolated HOME/CODEX_HOME for simultaneous sessions
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/agy"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	cursorprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/cursor"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/envonly"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/gemini"
	grokprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/grok"
	opencodeprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/opencode"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/tui"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/warnings"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	vault        *authfile.Vault
	profileStore *profile.Store
	projectStore *project.Store
	healthStore  *health.Storage
	registry     *provider.Registry
	cfg          *config.Config
	runner       *exec.Runner
	globalDB     *caamdb.DB
)

// Tools supported for auth file swapping
var tools = map[string]func() authfile.AuthFileSet{
	"codex":    authfile.CodexAuthFiles,
	"claude":   authfile.ClaudeAuthFiles,
	"gemini":   authfile.GeminiAuthFiles,
	"agy":      authfile.AntigravityAuthFiles,
	"grok":     authfile.GrokAuthFiles,
	"opencode": authfile.OpenCodeAuthFiles,
	"cursor":   authfile.CursorAuthFiles,
	// Env-injection-only providers: no auth files to swap; profiles are token
	// or endpoint profiles in the vault (see `caam token add`).
	"deepseek": authfile.DeepSeekAuthFiles,
	"ollama":   authfile.OllamaAuthFiles,
	"quick":    authfile.QuickAuthFiles,
}

// supportedTools returns the auth-swap providers (the keys of the tools map),
// sorted for stable output. This is the single source of truth for "which
// providers does caam manage auth files for", so help and error messages don't
// drift from the actual tools map.
func supportedTools() []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// supportedToolsList returns the comma-separated supported providers for use in
// error messages such as "unknown tool: X (supported: ...)".
func supportedToolsList() string {
	return strings.Join(supportedTools(), ", ")
}

// getDB returns the global database connection, initializing it if necessary.
func getDB() (*caamdb.DB, error) {
	targetPath := filepath.Clean(caamdb.DefaultPath())
	if globalDB != nil {
		if globalDB.Path() == targetPath {
			return globalDB, nil
		}
		// Path changed (likely in tests), close old instance and reopen
		globalDB.Close()
		globalDB = nil
	}
	var err error
	globalDB, err = caamdb.Open()
	return globalDB, err
}

// rootCmd represents the base command.
var rootCmd = &cobra.Command{
	Use:     "caam",
	Version: version.Info(),
	Short:   "Coding Agent Account Manager - instant auth switching",
	Long: `caam (Coding Agent Account Manager) manages auth files for AI coding CLIs
to enable instant account switching for "all you can eat" subscription plans
(GPT Pro, Claude Max, Gemini Ultra).

When you hit usage limits on one account, switch to another in under a second:

  1. Login to each account once (using the tool's normal login flow)
  2. Backup the auth: caam backup claude my-account-1
  3. Later, switch instantly: caam activate claude my-account-2

No browser flows, no waiting. Just instant auth file swapping.

Supported tools:
  - codex    (OpenAI Codex CLI / GPT Pro)
  - claude   (Anthropic Claude Code / Claude Max)
  - gemini   (Google Gemini CLI / Gemini Ultra)
  - agy      (Antigravity CLI)
  - grok     (xAI Grok Build CLI)
  - opencode (OpenCode)
  - cursor   (Cursor CLI)

Advanced: Profile isolation for simultaneous sessions:
  caam profile add codex work
  caam login codex work
  caam exec codex work -- "implement feature X"

Run 'caam' without arguments to launch the interactive TUI.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// If called with no subcommand, launch TUI
		return tui.Run()
	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if _, err := config.MigrateDataToCAAMHome(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: data migration skipped: %v\n", err)
		}

		// Initialize vault
		vault = authfile.NewVault(authfile.DefaultVaultPath())

		// Initialize profile store
		profileStore = profile.NewStore(profile.DefaultStorePath())

		// Initialize project store (project-profile associations).
		projectStore = project.NewStore(project.DefaultPath())

		// Initialize health store (Smart Profile Management metadata).
		healthStore = health.NewStorage("")

		// Initialize provider registry
		registry = provider.NewRegistry()
		registry.Register(codex.New())
		registry.Register(claude.New())
		registry.Register(gemini.New())
		registry.Register(agy.New())
		registry.Register(grokprovider.New())
		registry.Register(opencodeprovider.New())
		registry.Register(cursorprovider.New())
		registry.Register(envonly.DeepSeek())
		registry.Register(envonly.Ollama())
		registry.Register(envonly.Quick())

		// Initialize runner
		runner = exec.NewRunner(registry)

		// Load config
		var err error
		cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		// Show token expiry warnings (skip for certain commands)
		if shouldShowWarnings(cmd) {
			showTokenWarnings(cmd.Context())
		}

		return nil
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if globalDB != nil {
			globalDB.Close()
		}
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// ExitCode maps an error returned by Execute to a process exit code. When the
// wrapped tool (caam exec / caam run) exits non-zero, its real exit code is
// propagated so callers branching on exit status see the tool's result rather
// than a flattened 1 (issue #36). All other errors map to 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitCodeError
	if errors.As(err, &exitErr) && exitErr.Code != 0 {
		return exitErr.Code
	}
	return 1
}

// shouldShowWarnings returns true if the current command should display token warnings.
// Some commands are excluded because they're:
// - Quick info commands (version, paths)
// - Already doing validation (validate, doctor)
// - JSON output mode (warnings would corrupt output)
func shouldShowWarnings(cmd *cobra.Command) bool {
	// Skip for commands that don't benefit from warnings
	skipCommands := map[string]bool{
		"version":    true, // Quick info command
		"paths":      true, // Quick info command
		"validate":   true, // Already doing token validation
		"doctor":     true, // Already includes validation
		"help":       true, // Help output only
		"completion": true, // Shell completion generation
	}

	if skipCommands[cmd.Name()] {
		return false
	}

	// Skip if --json flag is set (would corrupt JSON output)
	if jsonFlag := cmd.Flags().Lookup("json"); jsonFlag != nil {
		if jsonFlag.Value.String() == "true" {
			return false
		}
	}

	// Skip if not a terminal (likely being piped/scripted)
	if !isTerminal() {
		return false
	}

	return true
}

// showTokenWarnings checks for expiring tokens and prints warnings to stderr.
func showTokenWarnings(ctx context.Context) {
	if vault == nil || registry == nil {
		return
	}

	checker := warnings.NewChecker(vault, registry, profileStore)

	// Only check active profiles for speed
	warns := checker.CheckActive(ctx)

	// Filter to warning level and above
	warns = warnings.Filter(warns, warnings.LevelWarning)

	// Print to stderr so it doesn't interfere with command output
	warnings.PrintToStderr(warns, false)
}

// isTerminal returns true if stdout is a terminal.
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// getProfileHealth returns health info for a profile by parsing auth files and checking metadata.
func getProfileHealth(tool, profileName string) *health.ProfileHealth {
	ph, _ := getProfileHealthWithIdentity(tool, profileName)
	return ph
}

func buildProfileHealth(tool, profileName string) *health.ProfileHealth {
	// Start with stored health data (for error counts, penalties, and fallback expiry)
	ph := &health.ProfileHealth{}
	if healthStore != nil {
		stored, err := healthStore.GetProfile(tool, profileName)
		if err == nil && stored != nil {
			ph = stored
		}
	}

	// Get auth files from vault profile
	vaultPath := vault.ProfilePath(tool, profileName)

	// Try to parse expiry based on tool type
	var expInfo *health.ExpiryInfo
	var err error

	switch tool {
	case "claude":
		expInfo, err = health.ParseClaudeExpiry(vaultPath)
	case "codex":
		// Codex auth is in auth.json at vaultPath
		authPath := filepath.Join(vaultPath, "auth.json")
		expInfo, err = health.ParseCodexExpiry(authPath)
	case "gemini":
		// Migrate legacy vault filename before reading.
		_ = authfile.MigrateGeminiVaultDir(vaultPath)
		expInfo, err = health.ParseGeminiExpiry(vaultPath)
	}

	// If file parsing succeeds and provides an expiry, treat it as authoritative
	if err == nil && expInfo != nil && !expInfo.ExpiresAt.IsZero() {
		ph.TokenExpiresAt = expInfo.ExpiresAt
	}

	// Fallback: when the vault snapshot yields no usable expiry, derive health
	// from the profile's own live credential instead of leaving TokenExpiresAt
	// zero (which caps the verdict at 🟡 Warning forever, even for a perfectly
	// healthy live token — issue #60). Adopted profiles symlink their auth dir
	// to the live location, so this reads the real, current token.
	if ph.TokenExpiresAt.IsZero() {
		if liveExp := parseLiveProfileExpiry(tool, profileName); liveExp != nil && !liveExp.ExpiresAt.IsZero() {
			ph.TokenExpiresAt = liveExp.ExpiresAt
		}
	}

	return ph
}

// parseLiveProfileExpiry reads the token expiry from a profile's own auth
// directory (following adoption symlinks). Best-effort; returns nil on any
// failure.
func parseLiveProfileExpiry(tool, profileName string) *health.ExpiryInfo {
	if profileStore == nil {
		return nil
	}
	prof, err := profileStore.Load(tool, profileName)
	if err != nil {
		return nil
	}
	var info *health.ExpiryInfo
	switch tool {
	case "codex":
		info, err = health.ParseCodexExpiry(filepath.Join(prof.CodexHomePath(), "auth.json"))
	case "claude":
		info, err = health.ParseClaudeExpiry(filepath.Join(prof.HomePath(), ".claude"))
	case "gemini":
		info, err = health.ParseGeminiExpiry(filepath.Join(prof.HomePath(), ".gemini"))
	case "grok":
		info, err = health.ParseCodexExpiry(filepath.Join(prof.HomePath(), ".grok", "auth.json"))
	default:
		return nil
	}
	if err != nil {
		return nil
	}
	return info
}

func getProfileHealthWithIdentity(tool, profileName string) (*health.ProfileHealth, *identity.Identity) {
	ph := buildProfileHealth(tool, profileName)
	id := getVaultIdentity(tool, profileName)
	applyIdentityToHealth(tool, profileName, ph, id)
	return ph, id
}

func getVaultIdentity(tool, profileName string) *identity.Identity {
	if vault == nil {
		return nil
	}
	vaultPath := vault.ProfilePath(tool, profileName)

	switch tool {
	case "codex":
		id, err := identity.ExtractFromCodexAuth(filepath.Join(vaultPath, "auth.json"))
		if err != nil {
			return nil
		}
		normalizeIdentityPlan(id)
		return id
	case "claude":
		id, err := identity.ExtractFromClaudeCredentials(filepath.Join(vaultPath, ".credentials.json"))
		if err != nil {
			// Fallback: try .claude.json directly (vault may not have .credentials.json)
			id, err = identity.ExtractFromClaudeJSON(filepath.Join(vaultPath, ".claude.json"))
			if err != nil {
				return nil
			}
		}
		normalizeIdentityPlan(id)
		return id
	case "gemini":
		// Migrate legacy vault filename before reading.
		_ = authfile.MigrateGeminiVaultDir(vaultPath)
		// Prefer oauth_creds.json: it carries the id_token JWT with the
		// account email, while settings.json often has no identity fields.
		candidates := []string{
			filepath.Join(vaultPath, "oauth_creds.json"),
			filepath.Join(vaultPath, "settings.json"),
		}
		for _, path := range candidates {
			id, err := identity.ExtractFromGeminiConfig(path)
			if err != nil {
				continue
			}
			if id.Email != "" {
				normalizeIdentityPlan(id)
				return id
			}
		}
	case "grok":
		id, err := identity.ExtractFromGrokAuth(filepath.Join(vaultPath, "auth.json"))
		if err != nil {
			return nil
		}
		normalizeIdentityPlan(id)
		return id
	case "opencode":
		id, err := identity.ExtractFromGenericAuth(filepath.Join(vaultPath, "auth.json"))
		if err != nil {
			return nil
		}
		normalizeIdentityPlan(id)
		return id
	case "cursor":
		candidates := []string{
			filepath.Join(vaultPath, "auth.json"),
			filepath.Join(vaultPath, "settings.json"),
		}
		for _, path := range candidates {
			id, err := identity.ExtractFromGenericAuth(path)
			if err != nil {
				continue
			}
			normalizeIdentityPlan(id)
			return id
		}
	}

	return nil
}

func applyIdentityToHealth(tool, profileName string, ph *health.ProfileHealth, id *identity.Identity) {
	if ph == nil || id == nil {
		return
	}
	if id.PlanType == "" {
		return
	}
	normalized := normalizePlanType(id.PlanType)
	if normalized == "" {
		return
	}
	ph.PlanType = normalized
	if healthStore != nil {
		_ = healthStore.SetPlanType(tool, profileName, normalized)
	}
}

func normalizeIdentityPlan(id *identity.Identity) {
	if id == nil {
		return
	}
	normalized := normalizePlanType(id.PlanType)
	if normalized != "" {
		id.PlanType = normalized
	}
}

func primePlanTypes(tool string, profiles []string) {
	if healthStore == nil {
		return
	}
	for _, profileName := range profiles {
		id := getVaultIdentity(tool, profileName)
		if id == nil {
			continue
		}
		applyIdentityToHealth(tool, profileName, &health.ProfileHealth{}, id)
	}
}

func normalizePlanType(planType string) string {
	plan := strings.ToLower(strings.TrimSpace(planType))
	switch plan {
	case "max", "ultra", "plus", "premium":
		return "pro"
	case "enterprise", "team", "pro", "free":
		return plan
	default:
		return plan
	}
}

func formatIdentityDisplay(id *identity.Identity) (string, string) {
	email := "unknown"
	plan := "unknown"
	if id == nil {
		return email, plan
	}

	// For Claude, email/accountId are no longer available in current auth files.
	// Show "n/a" instead of "unknown" to indicate this is expected, not an error.
	// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-001, CLAUDE-002)
	if id.Provider == "claude" && strings.TrimSpace(id.Email) == "" {
		email = "n/a"
	} else if strings.TrimSpace(id.Email) != "" {
		email = id.Email
	}

	if strings.TrimSpace(id.PlanType) != "" {
		formatted := health.FormatPlanType(id.PlanType)
		if formatted != "" {
			plan = formatted
		}
	}
	return email, plan
}

// getCooldownString returns a formatted string showing cooldown remaining time.
// Returns empty string if no active cooldown or if db is unavailable.
func getCooldownString(provider, profile string, opts health.FormatOptions) string {
	db, err := getDB()
	if err != nil {
		return ""
	}

	now := time.Now()
	cooldown, err := db.ActiveCooldown(provider, profile, now)
	if err != nil || cooldown == nil {
		return ""
	}

	remaining := cooldown.CooldownUntil.Sub(now)
	if remaining <= 0 {
		return ""
	}

	// Format remaining time
	var timeStr string
	if remaining >= time.Hour {
		hours := int(remaining.Hours())
		mins := int(remaining.Minutes()) % 60
		timeStr = fmt.Sprintf("%dh %dm", hours, mins)
	} else {
		mins := int(remaining.Minutes())
		if mins < 1 {
			timeStr = "<1m"
		} else {
			timeStr = fmt.Sprintf("%dm", mins)
		}
	}

	// Format with color based on remaining time
	var cooldownStr string
	if opts.NoColor {
		cooldownStr = fmt.Sprintf("(cooldown: %s remaining)", timeStr)
	} else if remaining >= time.Hour {
		// Red for > 1hr
		cooldownStr = fmt.Sprintf("\033[31m(cooldown: %s remaining)\033[0m", timeStr)
	} else if remaining >= 30*time.Minute {
		// Yellow for 30min - 1hr
		cooldownStr = fmt.Sprintf("\033[33m(cooldown: %s remaining)\033[0m", timeStr)
	} else {
		// Green for < 30min (almost done)
		cooldownStr = fmt.Sprintf("\033[32m(cooldown: %s remaining)\033[0m", timeStr)
	}

	return cooldownStr
}

// checkAllProfilesCooldown checks if all profiles for a tool are in cooldown.
// Returns: allInCooldown (true if all profiles have active cooldowns),
// shortestRemaining (duration until first profile is available),
// bestProfile (name of the profile that will be available soonest).
func checkAllProfilesCooldown(tool string) (bool, time.Duration, string) {
	profiles, err := vault.List(tool)
	if err != nil || len(profiles) == 0 {
		return false, 0, ""
	}

	db, err := getDB()
	if err != nil {
		return false, 0, ""
	}

	now := time.Now()
	var shortestRemaining time.Duration
	var bestProfile string

	for _, profile := range profiles {
		cooldown, err := db.ActiveCooldown(tool, profile, now)
		if err != nil || cooldown == nil {
			// This profile is NOT in cooldown
			return false, 0, ""
		}

		remaining := cooldown.CooldownUntil.Sub(now)
		if remaining <= 0 {
			// Cooldown expired, not in cooldown
			return false, 0, ""
		}

		if shortestRemaining == 0 || remaining < shortestRemaining {
			shortestRemaining = remaining
			bestProfile = profile
		}
	}

	// If we reach here, all profiles are in cooldown (no early returns occurred)
	return true, shortestRemaining, bestProfile
}

// formatAllCooldownWarning formats the "all profiles in cooldown" warning.
func formatAllCooldownWarning(tool string, remaining time.Duration, nextProfile string, opts health.FormatOptions) string {
	var timeStr string
	if remaining >= time.Hour {
		hours := int(remaining.Hours())
		mins := int(remaining.Minutes()) % 60
		timeStr = fmt.Sprintf("%dh %dm", hours, mins)
	} else {
		mins := int(remaining.Minutes())
		if mins < 1 {
			timeStr = "<1m"
		} else {
			timeStr = fmt.Sprintf("%dm", mins)
		}
	}

	if opts.NoColor {
		return fmt.Sprintf("%s: ⚠️  ALL profiles in cooldown (next available: %s in %s)", tool, nextProfile, timeStr)
	}
	// Yellow warning
	return fmt.Sprintf("\033[33m%s: ⚠️  ALL profiles in cooldown (next available: %s in %s)\033[0m", tool, nextProfile, timeStr)
}

// truncateDescription truncates a description to maxLen characters, adding "..." if truncated.
func truncateDescription(desc string, maxLen int) string {
	if desc == "" {
		return ""
	}
	if len(desc) <= maxLen {
		return desc
	}
	if maxLen <= 3 {
		return desc[:maxLen]
	}
	return desc[:maxLen-3] + "..."
}

func init() {
	// Cobra wires `--version` / `-v` automatically once rootCmd.Version
	// is set. Override the default template (which prefixes with the
	// command name and produces "caam caam 0.1.x ...") so `caam --version`
	// emits exactly the same string as the legacy `caam version`
	// subcommand. Common probes (`<tool> --version`) need this — see
	// ntm's `deps -v` health check.
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	// Core commands (auth file swapping - PRIMARY)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(activateCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(importCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(pathsCmd)
	pathsCmd.Flags().Bool("json", false, "output as JSON")
	rootCmd.AddCommand(clearCmd)

	// Profile isolation commands
	rootCmd.AddCommand(profileCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(execCmd)
}

// versionCmd prints version information.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Info())
	},
}

// =============================================================================
// AUTH FILE SWAPPING COMMANDS (PRIMARY USE CASE)
// =============================================================================

// backupOutput is the JSON output structure for backup command.
type backupOutput struct {
	Success bool   `json:"success"`
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	Path    string `json:"path"`
	Error   string `json:"error,omitempty"`
}

// backupCmd saves current auth files to the vault.
var backupCmd = &cobra.Command{
	Use:   "backup <tool> <profile-name>",
	Short: "Backup current auth to vault",
	Long: `Saves the current auth files for a tool to the vault with the given profile name.

Use this after logging in to an account through the tool's normal login flow:
  1. Run: codex login (or claude with /login, or gemini)
  2. Run: caam backup codex my-gptpro-account-1

The auth files are copied to $CAAM_HOME/data/vault/<tool>/<profile>/ (if CAAM_HOME is set)
or ~/.local/share/caam/vault/<tool>/<profile>/

Examples:
  caam backup codex work-account
  caam backup claude personal-max
  caam backup gemini team-ultra
  caam backup codex work --json`,
	Args: cobra.ExactArgs(2),
	RunE: runBackup,
}

func init() {
	backupCmd.Flags().Bool("json", false, "output as JSON")
}

func runBackup(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	jsonOutput, _ := cmd.Flags().GetBool("json")

	output := backupOutput{
		Tool:    tool,
		Profile: profileName,
	}

	emitJSONError := func(err error) error {
		if jsonOutput {
			output.Success = false
			output.Error = err.Error()
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			_ = enc.Encode(output)
			// Keep the JSON error payload on stdout but exit non-zero so callers
			// branching on exit status see the failure (README agent contract).
			// Silence the Cobra usage dump and duplicate stderr error for runtime
			// failures.
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}
		return err
	}

	getFileSet, ok := tools[tool]
	if !ok {
		return emitJSONError(fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList()))
	}

	fileSet := getFileSet()

	// Check if auth files exist
	if !authfile.HasAuthFiles(fileSet) {
		return emitJSONError(fmt.Errorf("no auth files found for %s - login first using the tool's login command", tool))
	}

	// Backup to vault
	if err := vault.Backup(fileSet, profileName); err != nil {
		return emitJSONError(fmt.Errorf("backup failed: %w", err))
	}

	output.Success = true
	output.Path = vault.ProfilePath(tool, profileName)

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	fmt.Printf("Backed up %s auth to profile '%s'\n", tool, profileName)
	fmt.Printf("  Vault: %s\n", output.Path)
	return nil
}

// statusOutput is the JSON output structure for status command.
type statusOutput struct {
	Tools           []statusTool `json:"tools"`
	Warnings        []string     `json:"warnings,omitempty"`
	Recommendations []string     `json:"recommendations,omitempty"`
}

type statusTool struct {
	Tool          string `json:"tool"`
	LoggedIn      bool   `json:"logged_in"`
	ActiveProfile string `json:"active_profile,omitempty"`
	// ProfileType is "token" when the active profile is a token (env-injection)
	// profile; empty for file-swap profiles.
	ProfileType string `json:"profile_type,omitempty"`
	// SavedProfiles is the number of vault profiles saved for this tool. It is
	// populated when the tool is logged in but the live auth matches no saved
	// profile, so JSON consumers can reconcile `status` with `ls` (issue #20).
	SavedProfiles int                `json:"saved_profiles,omitempty"`
	Error         string             `json:"error,omitempty"`
	Health        *statusHealth      `json:"health,omitempty"`
	Identity      *identity.Identity `json:"identity,omitempty"`
}

type statusHealth struct {
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	ErrorCount        int    `json:"error_count"`
	CooldownRemaining string `json:"cooldown_remaining,omitempty"`
}

// statusCmd shows which profile is currently active.
var statusCmd = &cobra.Command{
	Use:   "status [tool]",
	Short: "Show active profiles with health status",
	Long: `Shows which vault profile (if any) matches the current auth state for each tool,
along with health status indicators and recommendations.

Examples:
  caam status           # Show all tools
  caam status claude    # Show just Claude
  caam status --no-color  # Without colors
  caam status --json      # Output as JSON`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().Bool("no-color", false, "disable colored output")
	statusCmd.Flags().Bool("json", false, "output as JSON")
}

func runStatus(cmd *cobra.Command, args []string) error {
	noColor, _ := cmd.Flags().GetBool("no-color")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	formatOpts := health.FormatOptions{NoColor: noColor || !isTerminal()}

	// The big three are always shown; any other supported tool joins the
	// default listing once it has saved profiles, so token/endpoint profiles
	// for the newer providers (grok, deepseek, ollama, quick, ...) are
	// first-class in status without forcing empty rows on everyone.
	toolsToCheck := []string{"codex", "claude", "gemini"}
	for _, tool := range supportedTools() {
		switch tool {
		case "codex", "claude", "gemini":
			continue
		}
		if profiles, err := vault.List(tool); err == nil && len(profiles) > 0 {
			toolsToCheck = append(toolsToCheck, tool)
		}
	}
	if len(args) > 0 {
		tool := strings.ToLower(args[0])
		if _, ok := tools[tool]; !ok {
			return fmt.Errorf("unknown tool: %s", tool)
		}
		toolsToCheck = []string{tool}
	}

	var output statusOutput
	var warnings []string
	var recommendations []string

	if !jsonOutput {
		fmt.Println("Active Profiles")
		fmt.Println("───────────────────────────────────────────────────")
		fmt.Printf("%-10s  %-20s  %-24s  %-10s  %s\n", "TOOL", "PROFILE", "EMAIL", "PLAN", "STATUS")
	}

	for _, tool := range toolsToCheck {
		fileSet := tools[tool]()
		hasAuth := authfile.HasAuthFiles(fileSet)

		// A token-profile default takes precedence: it is what run/exec will
		// use, regardless of the live auth files on disk.
		if tokenProfile, _ := vault.ActiveTokenProfile(tool); tokenProfile != "" {
			ph, status := tokenProfileHealth(tool, tokenProfile)

			profileType := authfile.ProfileTypeToken
			if _, meta, err := vault.ReadTokenProfile(tool, tokenProfile); err == nil && meta.Type != "" {
				profileType = meta.Type
			}

			if jsonOutput {
				st := statusTool{
					Tool:          tool,
					LoggedIn:      true,
					ActiveProfile: tokenProfile,
					ProfileType:   profileType,
					Health: &statusHealth{
						Status:     status.String(),
						ErrorCount: ph.ErrorCount1h,
					},
				}
				if cooldownStr := getCooldownString(tool, tokenProfile, health.FormatOptions{NoColor: true}); cooldownStr != "" {
					st.Health.CooldownRemaining = cooldownStr
				}
				output.Tools = append(output.Tools, st)
			} else {
				healthStr := health.FormatStatusWithReason(status, ph, formatOpts)
				if cooldownStr := getCooldownString(tool, tokenProfile, formatOpts); cooldownStr != "" {
					healthStr = healthStr + " " + cooldownStr
				}
				fmt.Printf("%-10s  %-20s  %-24s  %-10s  %s\n", tool, tokenProfile+" ("+profileType+")", "-", "-", healthStr)
			}

			if status == health.StatusWarning || status == health.StatusCritical {
				detailedStatus := health.FormatStatusWithReason(status, ph, health.FormatOptions{NoColor: true})
				warnings = append(warnings, fmt.Sprintf("%s/%s: %s", tool, tokenProfile, detailedStatus))
			}
			continue
		}

		if !hasAuth {
			if jsonOutput {
				output.Tools = append(output.Tools, statusTool{
					Tool:     tool,
					LoggedIn: false,
				})
			} else {
				fmt.Printf("%-10s  (not logged in)\n", tool)
			}
			continue
		}

		activeProfile, err := vault.ActiveProfile(fileSet)
		if err != nil {
			if jsonOutput {
				output.Tools = append(output.Tools, statusTool{
					Tool:     tool,
					LoggedIn: true,
					Error:    err.Error(),
				})
			} else {
				fmt.Printf("%-10s  (error: %v)\n", tool, err)
			}
			continue
		}

		if activeProfile == "" {
			// Live auth exists but doesn't match any saved vault profile.
			// Cross-reference the same saved-profile source `caam ls` uses so the
			// two commands agree: `status` saying "no matching profile" while
			// `ls` lists saved profiles for the tool is confusing (issue #20).
			savedProfiles, _ := vault.List(tool)
			savedCount := len(savedProfiles)
			if jsonOutput {
				output.Tools = append(output.Tools, statusTool{
					Tool:          tool,
					LoggedIn:      true,
					SavedProfiles: savedCount,
				})
			} else {
				if savedCount > 0 {
					noun := "profiles"
					if savedCount == 1 {
						noun = "profile"
					}
					fmt.Printf("%-10s  (logged in; live auth matches no saved profile — %d saved %s available, see `caam ls %s`)\n", tool, savedCount, noun, tool)
				} else {
					fmt.Printf("%-10s  (logged in, no matching profile; none saved — save one with `caam add %s <name>`)\n", tool, tool)
				}
			}
			continue
		}

		// Get health and identity info
		ph, id := getProfileHealthWithIdentity(tool, activeProfile)
		status := health.CalculateStatus(ph)

		if jsonOutput {
			st := statusTool{
				Tool:          tool,
				LoggedIn:      true,
				ActiveProfile: activeProfile,
				Identity:      id,
				Health: &statusHealth{
					Status:     status.String(),
					ErrorCount: ph.ErrorCount1h,
				},
			}
			if !ph.TokenExpiresAt.IsZero() {
				st.Health.ExpiresAt = ph.TokenExpiresAt.Format(time.RFC3339)
			}
			// Get cooldown info
			cooldownStr := getCooldownString(tool, activeProfile, health.FormatOptions{NoColor: true})
			if cooldownStr != "" {
				st.Health.CooldownRemaining = cooldownStr
			}
			output.Tools = append(output.Tools, st)
		} else {
			healthStr := health.FormatStatusWithReason(status, ph, formatOpts)
			email, plan := formatIdentityDisplay(id)

			// Check for active cooldown and append remaining time
			cooldownStr := getCooldownString(tool, activeProfile, formatOpts)
			if cooldownStr != "" {
				healthStr = healthStr + " " + cooldownStr
			}

			fmt.Printf("%-10s  %-20s  %-24s  %-10s  %s\n", tool, activeProfile, email, plan, healthStr)
		}

		// Collect warnings
		if status == health.StatusWarning || status == health.StatusCritical {
			detailedStatus := health.FormatStatusWithReason(status, ph, health.FormatOptions{NoColor: true})
			warnings = append(warnings, fmt.Sprintf("%s/%s: %s", tool, activeProfile, detailedStatus))
		}

		// Collect recommendations
		rec := health.FormatRecommendation(tool, activeProfile, ph)
		if rec != "" {
			recommendations = append(recommendations, rec)
		}

		// Check if ALL profiles for this tool are in cooldown
		allCooldown, nextAvail, nextProfile := checkAllProfilesCooldown(tool)
		if allCooldown {
			warning := formatAllCooldownWarning(tool, nextAvail, nextProfile, health.FormatOptions{NoColor: true})
			warnings = append(warnings, warning)
		}
	}

	if jsonOutput {
		output.Warnings = warnings
		output.Recommendations = recommendations
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Show warnings
	if len(warnings) > 0 {
		fmt.Println()
		fmt.Println("Warnings")
		fmt.Println("───────────────────────────────────────────────────")
		for _, w := range warnings {
			fmt.Printf("  %s\n", w)
		}
	}

	// Show recommendations
	if len(recommendations) > 0 {
		fmt.Println()
		fmt.Println("Recommendations")
		fmt.Println("───────────────────────────────────────────────────")
		for _, r := range recommendations {
			for _, line := range strings.Split(r, "\n") {
				fmt.Printf("  • %s\n", line)
			}
		}
	}

	return nil
}

// lsOutput is the JSON output structure for ls command.
type lsOutput struct {
	Profiles []lsProfile `json:"profiles"`
	Count    int         `json:"count"`
}

type lsProfile struct {
	Tool   string `json:"tool"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	System bool   `json:"system"`
	// Type is "token" for token (env-injection) profiles; empty for
	// file-swap profiles.
	Type     string             `json:"type,omitempty"`
	Health   lsHealth           `json:"health"`
	Identity *identity.Identity `json:"identity,omitempty"`
}

type lsHealth struct {
	Status     string `json:"status"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	ErrorCount int    `json:"error_count"`
}

// lsCmd lists all stored profiles.
var lsCmd = &cobra.Command{
	Use:     "ls [tool]",
	Aliases: []string{"list"},
	Short:   "List saved profiles",
	Long: `Lists all profiles stored in the vault with health status.

Examples:
  caam ls              # List all profiles
  caam ls claude       # List just Claude profiles
  caam ls --tag work   # List profiles with 'work' tag
  caam ls --no-color   # Without colors (for piping)
  caam ls --json       # Output as JSON`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLs,
}

func init() {
	lsCmd.Flags().Bool("no-color", false, "disable colored output")
	lsCmd.Flags().Bool("json", false, "output as JSON")
	lsCmd.Flags().String("tag", "", "filter profiles by tag")
}

func runLs(cmd *cobra.Command, args []string) error {
	noColor, _ := cmd.Flags().GetBool("no-color")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	tagFilter, _ := cmd.Flags().GetString("tag")
	formatOpts := health.FormatOptions{NoColor: noColor || !isTerminal()}

	// Helper to check if a profile has the specified tag
	hasTag := func(tool, profileName string) bool {
		if tagFilter == "" {
			return true // No filter, include all
		}
		if profileStore == nil {
			return false // No profile store, can't check tags
		}
		prof, err := profileStore.Load(tool, profileName)
		if err != nil {
			return false // Profile not in store, no tags
		}
		return prof.HasTag(tagFilter)
	}

	// Collect profiles for JSON output
	var output lsOutput

	if len(args) > 0 {
		tool := strings.ToLower(args[0])
		if _, ok := tools[tool]; !ok {
			return fmt.Errorf("unknown tool: %s", tool)
		}

		profiles, err := vault.List(tool)
		if err != nil {
			return err
		}

		// Filter by tag if specified
		if tagFilter != "" {
			var filtered []string
			for _, p := range profiles {
				if hasTag(tool, p) {
					filtered = append(filtered, p)
				}
			}
			profiles = filtered
		}

		if len(profiles) == 0 {
			if jsonOutput {
				output.Profiles = []lsProfile{}
				output.Count = 0
				return encodeLsJSON(cmd, output)
			}
			fmt.Printf("No profiles saved for %s\n", tool)
			return nil
		}

		if !jsonOutput {
			fmt.Printf("%-22s  %-24s  %-10s  %s\n", "PROFILE", "EMAIL", "PLAN", "STATUS")
		}

		// Check which is active
		fileSet := tools[tool]()
		activeProfile, _ := vault.ActiveProfile(fileSet)
		if tokenActive, _ := vault.ActiveTokenProfile(tool); tokenActive != "" {
			// A token-profile default overrides live-file matching: it is what
			// run/exec will actually use.
			activeProfile = tokenActive
		}

		for _, p := range profiles {
			ph, id := getProfileHealthWithIdentity(tool, p)
			isToken := vault.IsTokenProfile(tool, p)
			profileType := lsProfileType(tool, p, isToken)
			var status health.HealthStatus
			if isToken {
				ph, status = tokenProfileHealth(tool, p)
			} else {
				status = health.CalculateStatus(ph)
			}

			if jsonOutput {
				lp := lsProfile{
					Tool:   tool,
					Name:   p,
					Active: p == activeProfile,
					System: authfile.IsSystemProfile(p),
					Type:   profileType,
					Health: lsHealth{
						Status:     status.String(),
						ErrorCount: ph.ErrorCount1h,
					},
					Identity: id,
				}
				if !ph.TokenExpiresAt.IsZero() {
					lp.Health.ExpiresAt = ph.TokenExpiresAt.Format(time.RFC3339)
				}
				output.Profiles = append(output.Profiles, lp)
			} else {
				marker := "  "
				if p == activeProfile {
					marker = "● "
				}

				displayName := p
				if authfile.IsSystemProfile(p) {
					displayName = fmt.Sprintf("%s [system]", p)
				}
				if profileType != "" {
					displayName = fmt.Sprintf("%s [%s]", displayName, profileType)
				}

				email, plan := formatIdentityDisplay(id)
				healthStr := health.FormatHealthStatus(status, ph, formatOpts)
				fmt.Printf("%s%-20s  %-24s  %-10s  %s\n", marker, displayName, email, plan, healthStr)
			}
		}

		if jsonOutput {
			output.Count = len(output.Profiles)
			return encodeLsJSON(cmd, output)
		}
		return nil
	}

	// List all
	allProfiles, err := vault.ListAll()
	if err != nil {
		return err
	}

	// Filter by tag if specified
	if tagFilter != "" {
		filtered := make(map[string][]string)
		for tool, profiles := range allProfiles {
			var matching []string
			for _, p := range profiles {
				if hasTag(tool, p) {
					matching = append(matching, p)
				}
			}
			if len(matching) > 0 {
				filtered[tool] = matching
			}
		}
		allProfiles = filtered
	}

	if len(allProfiles) == 0 {
		if jsonOutput {
			output.Profiles = []lsProfile{}
			output.Count = 0
			return encodeLsJSON(cmd, output)
		}
		if tagFilter != "" {
			fmt.Printf("No profiles with tag '%s'\n", tagFilter)
		} else {
			fmt.Println("No profiles saved yet.")
			fmt.Println("\nTo save your first profile:")
			fmt.Println("  1. Login using the tool's command (codex login, /login in claude)")
			fmt.Println("  2. Run: caam backup <tool> <profile-name>")
		}
		return nil
	}

	for tool, profiles := range allProfiles {
		fileSet := tools[tool]()
		activeProfile, _ := vault.ActiveProfile(fileSet)
		if tokenActive, _ := vault.ActiveTokenProfile(tool); tokenActive != "" {
			// A token-profile default overrides live-file matching: it is what
			// run/exec will actually use.
			activeProfile = tokenActive
		}

		if !jsonOutput {
			fmt.Printf("%s:\n", tool)
			fmt.Printf("  %-20s  %-24s  %-10s  %s\n", "PROFILE", "EMAIL", "PLAN", "STATUS")
		}

		for _, p := range profiles {
			ph, id := getProfileHealthWithIdentity(tool, p)
			isToken := vault.IsTokenProfile(tool, p)
			profileType := lsProfileType(tool, p, isToken)
			var status health.HealthStatus
			if isToken {
				ph, status = tokenProfileHealth(tool, p)
			} else {
				status = health.CalculateStatus(ph)
			}

			if jsonOutput {
				lp := lsProfile{
					Tool:   tool,
					Name:   p,
					Active: p == activeProfile,
					System: authfile.IsSystemProfile(p),
					Type:   profileType,
					Health: lsHealth{
						Status:     status.String(),
						ErrorCount: ph.ErrorCount1h,
					},
					Identity: id,
				}
				if !ph.TokenExpiresAt.IsZero() {
					lp.Health.ExpiresAt = ph.TokenExpiresAt.Format(time.RFC3339)
				}
				output.Profiles = append(output.Profiles, lp)
			} else {
				marker := "  "
				if p == activeProfile {
					marker = "● "
				}

				displayName := p
				if authfile.IsSystemProfile(p) {
					displayName = fmt.Sprintf("%s [system]", p)
				}
				if profileType != "" {
					displayName = fmt.Sprintf("%s [%s]", displayName, profileType)
				}

				email, plan := formatIdentityDisplay(id)
				healthStr := health.FormatHealthStatus(status, ph, formatOpts)
				fmt.Printf("  %s%-20s  %-24s  %-10s  %s\n", marker, displayName, email, plan, healthStr)
			}
		}
	}

	if jsonOutput {
		output.Count = len(output.Profiles)
		return encodeLsJSON(cmd, output)
	}

	return nil
}

// lsProfileType returns the lsProfile.Type value: the env-injection profile
// type ("token" or "endpoint") read from the profile's meta.json — mirroring
// what `caam status` reports — or "" for file-swap profiles.
func lsProfileType(tool, name string, isToken bool) string {
	if !isToken {
		return ""
	}
	if _, meta, err := vault.ReadTokenProfile(tool, name); err == nil && meta.Type != "" {
		return meta.Type
	}
	return authfile.ProfileTypeToken
}

func encodeLsJSON(cmd *cobra.Command, output lsOutput) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// deleteCmd removes a profile from the vault.
var deleteCmd = &cobra.Command{
	Use:     "delete <tool> <profile-name>",
	Aliases: []string{"rm", "remove"},
	Short:   "Delete a saved profile",
	Long: `Removes a profile from the vault. This does not affect the current auth state.

Examples:
  caam delete claude old-account`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		profileName := args[1]

		if _, ok := tools[tool]; !ok {
			return fmt.Errorf("unknown tool: %s", tool)
		}

		force, _ := cmd.Flags().GetBool("force")
		if authfile.IsSystemProfile(profileName) && !force {
			return fmt.Errorf("refusing to delete system profile %s/%s without --force", tool, profileName)
		}
		if !force {
			fmt.Printf("Delete profile %s/%s? [y/N]: ", tool, profileName)
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "y" {
				fmt.Println("Cancelled")
				return nil
			}
		}

		var err error
		if authfile.IsSystemProfile(profileName) {
			err = vault.DeleteForce(tool, profileName)
		} else {
			err = vault.Delete(tool, profileName)
		}
		if err != nil {
			return fmt.Errorf("delete failed: %w", err)
		}

		fmt.Printf("Deleted %s/%s\n", tool, profileName)
		return nil
	},
}

func init() {
	deleteCmd.Flags().Bool("force", false, "skip confirmation (required to delete system profiles starting with '_')")
}

// PathsFileRecord is a single auth-file path record for `paths --json`.
type PathsFileRecord struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// PathsToolRecord groups auth-file records for one tool for `paths --json`.
type PathsToolRecord struct {
	Tool  string            `json:"tool"`
	Files []PathsFileRecord `json:"files"`
}

// pathsCmd shows auth file paths for each tool.
var pathsCmd = &cobra.Command{
	Use:   "paths [tool]",
	Short: "Show auth file paths",
	Long: `Shows where each tool stores its auth files.

Useful for understanding what caam is backing up and for manual troubleshooting.

Examples:
  caam paths           # Show all tools
  caam paths claude    # Show just Claude
  caam paths agy --json # Machine-readable records for one tool`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")

		// Default to every supported auth-swap tool (not a stale subset) so the
		// discovery surface matches the tools map (issue #27).
		toolsToShow := supportedTools()
		if len(args) > 0 {
			tool := strings.ToLower(args[0])
			if _, ok := tools[tool]; !ok {
				return fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList())
			}
			toolsToShow = []string{tool}
		}

		if jsonOutput {
			records := make([]PathsToolRecord, 0, len(toolsToShow))
			for _, tool := range toolsToShow {
				fileSet := tools[tool]()
				rec := PathsToolRecord{Tool: tool, Files: make([]PathsFileRecord, 0, len(fileSet.Files))}
				for _, spec := range fileSet.Files {
					_, statErr := os.Stat(spec.Path)
					rec.Files = append(rec.Files, PathsFileRecord{
						Path:        spec.Path,
						Exists:      statErr == nil,
						Required:    spec.Required,
						Description: spec.Description,
					})
				}
				records = append(records, rec)
			}
			data, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		}

		for _, tool := range toolsToShow {
			fileSet := tools[tool]()
			fmt.Printf("%s:\n", tool)
			for _, spec := range fileSet.Files {
				exists := "missing"
				if _, err := os.Stat(spec.Path); err == nil {
					exists = "exists"
				}
				required := ""
				if spec.Required {
					required = " (required)"
				}
				fmt.Printf("  [%s] %s%s\n", exists, spec.Path, required)
				fmt.Printf("         %s\n", spec.Description)
			}
			fmt.Println()
		}

		return nil
	},
}

// clearCmd removes auth files (logout).
var clearCmd = &cobra.Command{
	Use:   "clear <tool>",
	Short: "Clear auth files (logout)",
	Long: `Removes the auth files for a tool, effectively logging out.

This is useful if you want to start fresh or test the login flow.
Consider backing up first: caam backup <tool> <name>

Examples:
  caam clear claude`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])

		getFileSet, ok := tools[tool]
		if !ok {
			return fmt.Errorf("unknown tool: %s", tool)
		}

		fileSet := getFileSet()

		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Printf("Clear auth for %s? This will log you out. [y/N]: ", tool)
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "y" {
				fmt.Println("Cancelled")
				return nil
			}
		}

		if err := authfile.ClearAuthFiles(fileSet); err != nil {
			return fmt.Errorf("clear failed: %w", err)
		}

		fmt.Printf("Cleared auth for %s\n", tool)
		return nil
	},
}

func init() {
	clearCmd.Flags().Bool("force", false, "skip confirmation")
}

// =============================================================================
// PROFILE ISOLATION COMMANDS (ADVANCED)
// =============================================================================

// profileCmd is the parent command for profile management.
var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage isolated profiles (advanced)",
	Long: `Manage isolated profile directories for running multiple sessions simultaneously.

Unlike the backup/activate commands which swap auth files in place, profiles
create fully isolated environments with their own HOME/CODEX_HOME directories.

This is useful when you need to:
  - Run multiple sessions with different accounts at the same time
  - Keep auth state completely separate between accounts
  - Test login flows without affecting your main account`,
}

func init() {
	profileCmd.AddCommand(profileAddCmd)
	profileCmd.AddCommand(profileLsCmd)
	profileCmd.AddCommand(profileDeleteCmd)
	profileCmd.AddCommand(profileStatusCmd)
	profileCmd.AddCommand(profileUnlockCmd)
}

var profileAddCmd = &cobra.Command{
	Use:   "add <tool> <name> [--auth-mode oauth|api-key]",
	Short: "Create a new isolated profile",
	Long: `Create a new isolated profile for running multiple sessions simultaneously.

Options:
  --auth-mode        Authentication mode (oauth, api-key)
  --description, -d  Free-form notes about this profile's purpose
  --browser          Browser command (chrome, firefox, or full path)
  --browser-profile  Browser profile name or directory

Examples:
  caam profile add codex work
  caam profile add claude personal -d "Personal consulting projects"
  caam profile add claude work --browser chrome --browser-profile "Profile 2"
  caam profile add gemini team --browser firefox --browser-profile "work-firefox"`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		prov, ok := registry.Get(tool)
		if !ok {
			return fmt.Errorf("unknown provider: %s", tool)
		}

		authMode, _ := cmd.Flags().GetString("auth-mode")
		if authMode == "" {
			authMode = "oauth"
		}

		// Create profile
		prof, err := profileStore.Create(tool, name, authMode)
		if err != nil {
			return fmt.Errorf("create profile: %w", err)
		}

		// Set description if provided
		description, _ := cmd.Flags().GetString("description")
		if description != "" {
			prof.Description = description
		}

		// Set browser configuration if provided
		browserCmd, _ := cmd.Flags().GetString("browser")
		browserProfile, _ := cmd.Flags().GetString("browser-profile")
		browserName, _ := cmd.Flags().GetString("browser-name")

		if browserCmd != "" {
			prof.BrowserCommand = browserCmd
		}
		if browserProfile != "" {
			prof.BrowserProfileDir = browserProfile
		}
		if browserName != "" {
			prof.BrowserProfileName = browserName
		}

		// Save updated profile with browser config
		if err := prof.Save(); err != nil {
			profileStore.Delete(tool, name)
			return fmt.Errorf("save profile: %w", err)
		}

		// Prepare profile directory structure
		ctx := context.Background()
		if err := prov.PrepareProfile(ctx, prof); err != nil {
			// Clean up on failure
			profileStore.Delete(tool, name)
			return fmt.Errorf("prepare profile: %w", err)
		}

		fmt.Printf("Created profile %s/%s\n", tool, name)
		fmt.Printf("  Path: %s\n", prof.BasePath)
		if prof.Description != "" {
			fmt.Printf("  Description: %s\n", prof.Description)
		}
		if prof.HasBrowserConfig() {
			fmt.Printf("  Browser: %s\n", prof.BrowserDisplayName())
		}
		fmt.Printf("\nNext steps:\n")
		fmt.Printf("  caam login %s %s    # Authenticate\n", tool, name)
		fmt.Printf("  caam exec %s %s     # Run with this profile\n", tool, name)
		return nil
	},
}

func init() {
	profileAddCmd.Flags().String("auth-mode", "oauth", "authentication mode (oauth, api-key)")
	profileAddCmd.Flags().StringP("description", "d", "", "free-form notes about this profile's purpose")
	profileAddCmd.Flags().String("browser", "", "browser command (chrome, firefox, or full path)")
	profileAddCmd.Flags().String("browser-profile", "", "browser profile name or directory")
	profileAddCmd.Flags().String("browser-name", "", "human-friendly name for browser profile")
}

var profileLsCmd = &cobra.Command{
	Use:     "ls [tool]",
	Aliases: []string{"list"},
	Short:   "List isolated profiles",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			tool := strings.ToLower(args[0])
			profiles, err := profileStore.List(tool)
			if err != nil {
				return err
			}

			if len(profiles) == 0 {
				fmt.Printf("No isolated profiles for %s\n", tool)
				return nil
			}

			for _, p := range profiles {
				status := ""
				if p.IsLocked() {
					status = " [locked]"
				}
				desc := truncateDescription(p.Description, 40)
				if desc != "" {
					fmt.Printf("  %s/%s%s  %s\n", p.Provider, p.Name, status, desc)
				} else {
					fmt.Printf("  %s/%s%s\n", p.Provider, p.Name, status)
				}
			}
			return nil
		}

		allProfiles, err := profileStore.ListAll()
		if err != nil {
			return err
		}

		if len(allProfiles) == 0 {
			fmt.Println("No isolated profiles.")
			fmt.Println("Use 'caam profile add <tool> <name>' to create one.")
			return nil
		}

		for tool, profiles := range allProfiles {
			fmt.Printf("%s:\n", tool)
			for _, p := range profiles {
				status := ""
				if p.IsLocked() {
					status = " [locked]"
				}
				desc := truncateDescription(p.Description, 40)
				if desc != "" {
					fmt.Printf("  %-20s%s  %s\n", p.Name, status, desc)
				} else {
					fmt.Printf("  %s%s\n", p.Name, status)
				}
			}
		}

		return nil
	},
}

var profileDeleteCmd = &cobra.Command{
	Use:     "delete <tool> <name>",
	Aliases: []string{"rm"},
	Short:   "Delete an isolated profile",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Printf("Delete isolated profile %s/%s? [y/N]: ", tool, name)
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "y" {
				fmt.Println("Cancelled")
				return nil
			}
		}

		if err := profileStore.Delete(tool, name); err != nil {
			return fmt.Errorf("delete profile: %w", err)
		}

		fmt.Printf("Deleted %s/%s\n", tool, name)
		return nil
	},
}

func init() {
	profileDeleteCmd.Flags().Bool("force", false, "skip confirmation")
}

var profileStatusCmd = &cobra.Command{
	Use:   "status <tool> <name>",
	Short: "Show profile status",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		prov, ok := registry.Get(tool)
		if !ok {
			return fmt.Errorf("unknown provider: %s", tool)
		}

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		ctx := context.Background()
		status, err := prov.Status(ctx, prof)
		if err != nil {
			return fmt.Errorf("get status: %w", err)
		}

		fmt.Printf("Profile: %s/%s\n", tool, name)
		fmt.Printf("  Path: %s\n", prof.BasePath)
		fmt.Printf("  Auth mode: %s\n", prof.AuthMode)
		fmt.Printf("  Logged in: %v\n", status.LoggedIn)
		fmt.Printf("  Locked: %v\n", status.HasLockFile)
		if prof.AccountLabel != "" {
			fmt.Printf("  Account: %s\n", prof.AccountLabel)
		}
		if prof.Description != "" {
			fmt.Printf("  Description: %s\n", prof.Description)
		}
		if prof.HasBrowserConfig() {
			fmt.Printf("  Browser: %s\n", prof.BrowserDisplayName())
		}

		return nil
	},
}

var profileUnlockCmd = &cobra.Command{
	Use:   "unlock <tool> <name>",
	Short: "Unlock a locked profile",
	Long: `Forcibly removes a lock file from a profile.

By default, this command will only unlock profiles where the locking process
is no longer running (stale locks from crashed processes).

Use --force to unlock even if the locking process appears to still be running.
WARNING: Using --force on an active session can cause data corruption!

Examples:
  caam profile unlock codex work        # Unlock stale lock
  caam profile unlock claude home -f    # Force unlock (dangerous)`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		// Check if profile is locked
		if !prof.IsLocked() {
			fmt.Printf("Profile %s/%s is not locked\n", tool, name)
			return nil
		}

		// Get lock info for display
		lockInfo, err := prof.GetLockInfo()
		if err != nil {
			return fmt.Errorf("read lock info: %w", err)
		}

		// Check if lock is stale (process dead)
		stale, err := prof.IsLockStale()
		if err != nil {
			return fmt.Errorf("check lock status: %w", err)
		}

		force, _ := cmd.Flags().GetBool("force")

		if stale {
			// Safe to unlock - process is dead
			fmt.Printf("Lock is stale (PID %d is no longer running)\n", lockInfo.PID)
			if err := prof.Unlock(); err != nil {
				return fmt.Errorf("unlock failed: %w", err)
			}
			fmt.Printf("Unlocked %s/%s\n", tool, name)
			return nil
		}

		// Process is still running
		if !force {
			fmt.Printf("Profile %s/%s is locked by PID %d (still running)\n", tool, name, lockInfo.PID)
			fmt.Printf("Locked at: %s\n", lockInfo.LockedAt.Format("2006-01-02 15:04:05"))
			fmt.Println()
			fmt.Println("WARNING: The locking process appears to still be running.")
			fmt.Println("Force-unlocking an active session can cause data corruption!")
			fmt.Println()
			fmt.Println("Use --force to unlock anyway (not recommended)")
			return fmt.Errorf("refusing to unlock active profile (use --force to override)")
		}

		// Force unlock - user accepted the risk
		fmt.Printf("WARNING: Force-unlocking profile locked by running process (PID %d)\n", lockInfo.PID)
		fmt.Printf("Force unlock %s/%s? This may cause data corruption! [y/N]: ", tool, name)
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "y" {
			fmt.Println("Cancelled")
			return nil
		}

		if err := prof.Unlock(); err != nil {
			return fmt.Errorf("unlock failed: %w", err)
		}
		fmt.Printf("Force-unlocked %s/%s\n", tool, name)
		return nil
	},
}

func init() {
	profileUnlockCmd.Flags().BoolP("force", "f", false, "force unlock even if process is running (dangerous)")
}

var profileDescribeCmd = &cobra.Command{
	Use:   "describe <tool> <name> [description]",
	Short: "Set or show profile description",
	Long: `Set or show the description for an isolated profile.

If description is provided, sets it. Otherwise, shows the current description.
Use --clear to remove the description.

Examples:
  caam profile describe claude work                    # Show description
  caam profile describe claude work "Client projects"  # Set description
  caam profile describe claude work --clear            # Remove description`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		clearFlag, _ := cmd.Flags().GetBool("clear")

		if clearFlag {
			prof.Description = ""
			if err := prof.Save(); err != nil {
				return fmt.Errorf("save profile: %w", err)
			}
			fmt.Printf("Cleared description for %s/%s\n", tool, name)
			return nil
		}

		if len(args) == 3 {
			prof.Description = args[2]
			if err := prof.Save(); err != nil {
				return fmt.Errorf("save profile: %w", err)
			}
			fmt.Printf("Set description for %s/%s: %s\n", tool, name, prof.Description)
			return nil
		}

		// Show current description
		if prof.Description == "" {
			fmt.Printf("%s/%s has no description\n", tool, name)
		} else {
			fmt.Printf("%s/%s: %s\n", tool, name, prof.Description)
		}
		return nil
	},
}

func init() {
	profileDescribeCmd.Flags().Bool("clear", false, "remove the description")
	profileCmd.AddCommand(profileDescribeCmd)
}

var profileCloneCmd = &cobra.Command{
	Use:   "clone <tool> <source-profile> <target-profile>",
	Short: "Clone an existing profile",
	Long: `Clone an existing profile to create a new one with similar configuration.

By default, copies settings (browser config, auth mode, metadata) but NOT auth files.
Use --with-auth to also copy authentication credentials.

Examples:
  caam profile clone claude work new-client              # Clone settings only
  caam profile clone codex main backup --with-auth       # Clone with auth files
  caam profile clone claude work test -d "Testing only"  # Clone with custom description
  caam profile clone gemini old new --force              # Overwrite existing target`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		sourceName := args[1]
		targetName := args[2]

		withAuth, _ := cmd.Flags().GetBool("with-auth")
		description, _ := cmd.Flags().GetString("description")
		force, _ := cmd.Flags().GetBool("force")

		opts := profile.CloneOptions{
			WithAuth:    withAuth,
			Description: description,
			Force:       force,
		}

		cloned, err := profileStore.Clone(tool, sourceName, targetName, opts)
		if err != nil {
			return err
		}

		// Set up passthrough symlinks for the cloned profile
		// This allows dev tools (git, ssh, etc.) to work with the isolated profile
		passMgr, err := passthrough.NewManager()
		if err != nil {
			// Non-fatal: profile is cloned, just warn about passthrough
			fmt.Fprintf(os.Stderr, "Warning: could not setup passthroughs: %v\n", err)
		} else {
			if err := passMgr.SetupPassthroughs(cloned.HomePath()); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: passthrough setup failed: %v\n", err)
			}
		}

		fmt.Printf("Cloned %s/%s → %s/%s\n", tool, sourceName, tool, targetName)
		fmt.Printf("  Path: %s\n", cloned.BasePath)
		if cloned.Description != "" {
			fmt.Printf("  Description: %s\n", cloned.Description)
		}
		authCopied := 0
		if withAuth {
			authCopied = cloned.CountAuthFiles()
		}
		switch {
		case withAuth && authCopied > 0:
			fmt.Printf("  Auth files: copied (%d file(s))\n", authCopied)
		case withAuth && authCopied == 0:
			// Don't claim success we didn't achieve: the source may have no
			// credentials, or an unreadable source home. Tell the truth so the
			// user knows to log in rather than discovering it at first use.
			fmt.Println("  Auth files: none found to copy — source profile has no readable credentials")
			fmt.Fprintf(os.Stderr, "Warning: --with-auth copied 0 files; log in with 'caam login %s %s' before using this profile\n", tool, targetName)
		default:
			fmt.Println("  Auth files: not copied (use --with-auth to include)")
		}

		fmt.Printf("\nNext steps:\n")
		if !withAuth || authCopied == 0 {
			fmt.Printf("  caam login %s %s    # Authenticate\n", tool, targetName)
		}
		fmt.Printf("  caam exec %s %s      # Run with this profile\n", tool, targetName)

		return nil
	},
}

func init() {
	profileCloneCmd.Flags().Bool("with-auth", false, "also copy auth files from source")
	profileCloneCmd.Flags().StringP("description", "d", "", "set custom description (default: \"Cloned from <source>\")")
	profileCloneCmd.Flags().Bool("force", false, "overwrite existing target profile")
	profileCmd.AddCommand(profileCloneCmd)
}

// loginCmd initiates login for an isolated profile.
var loginCmd = &cobra.Command{
	Use:   "login <tool> <profile>",
	Short: "Login to an isolated profile",
	Long: `Initiates the login flow for an isolated profile.

This runs the tool's native login command with the profile's isolated environment,
so the auth credentials are stored in the profile's directory.

Examples:
  caam login codex work     # Login to work profile
  caam login claude home    # Login to home profile
  caam login codex work --device-code  # Device code flow (if supported)`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		// caam cannot drive Claude Code's auth: its OAuth endpoint is
		// undocumented and its tokens are opaque, so `caam login claude ...`
		// can only ever fail later with a misleading "profile not found" or
		// token-expired error (issue #53). Human users must authenticate with
		// Claude Code's own built-in /login flow. Return a clear, provider-
		// specific message up front instead of the generic failure path.
		if tool == "claude" {
			return fmt.Errorf("caam login is not supported for the claude provider — " +
				"use Claude Code's built-in /login flow instead (run `claude`, then type /login). " +
				"See the docs for details")
		}

		prov, ok := registry.Get(tool)
		if !ok {
			return fmt.Errorf("unknown provider: %s", tool)
		}

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		ctx := context.Background()
		deviceCode, _ := cmd.Flags().GetBool("device-code")
		if deviceCode {
			deviceCodeProv, ok := prov.(provider.DeviceCodeProvider)
			if !ok || !deviceCodeProv.SupportsDeviceCode() {
				return fmt.Errorf("%s does not support --device-code", tool)
			}
			if err := deviceCodeProv.LoginWithDeviceCode(ctx, prof); err != nil {
				return fmt.Errorf("device-code login failed: %w", err)
			}
		} else {
			if err := prov.Login(ctx, prof); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
		}

		fmt.Printf("\nLogin complete for %s/%s\n", tool, name)
		return nil
	},
}

func init() {
	loginCmd.Flags().Bool("device-code", false, "use device code flow (if supported)")
}

// execCmd runs the CLI with an isolated profile.
var execCmd = &cobra.Command{
	Use:   "exec <tool> <profile> [-- args...]",
	Short: "Run CLI with isolated profile",
	Long: `Runs the AI CLI tool with the specified isolated profile's environment.

This sets up HOME/CODEX_HOME/etc to use the profile's directory, then runs
the tool with any additional arguments.

Examples:
  caam exec codex work                        # Interactive session
  caam exec codex work -- "implement feature"  # With prompt
  caam exec claude home -- -p "fix bug"        # With flags`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		name := args[1]

		// Everything after "--" or after the profile name
		var toolArgs []string
		if len(args) > 2 {
			toolArgs = args[2:]
		}

		prov, ok := registry.Get(tool)
		if !ok {
			return fmt.Errorf("unknown provider: %s", tool)
		}

		ctx := context.Background()
		noLock, _ := cmd.Flags().GetBool("no-lock")

		// Token profiles run via env injection: no isolated profile home is
		// needed, the token and per-profile config dir are passed in the
		// environment.
		var runOpts exec.RunOptions
		if vault != nil && vault.IsTokenProfile(tool, name) {
			token, meta, err := vault.ReadTokenProfile(tool, name)
			if err != nil {
				return err
			}
			env, err := authfile.ProfileEnv(tool, name, token, meta)
			if err != nil {
				return err
			}
			prof, err := profileStore.Load(tool, name)
			if err != nil {
				// Token profiles usually exist only in the vault; use a
				// transient profile object so locking has a stable home.
				prof = &profile.Profile{
					Name:     name,
					Provider: tool,
					AuthMode: "token",
					BasePath: profileStore.ProfilePath(tool, name),
				}
			}
			runOpts = exec.RunOptions{
				Profile:      prof,
				Provider:     prov,
				Args:         toolArgs,
				NoLock:       noLock,
				Env:          env,
				UseGlobalEnv: true, // env injection needs the real HOME
			}
		} else {
			prof, err := profileStore.Load(tool, name)
			if err != nil {
				return err
			}
			runOpts = exec.RunOptions{
				Profile:  prof,
				Provider: prov,
				Args:     toolArgs,
				NoLock:   noLock,
			}
		}

		runErr := runner.Run(ctx, runOpts)

		// A non-zero exit from the wrapped tool is a runtime failure of that
		// tool, not a misuse of caam. Suppress the Cobra usage block and the
		// duplicate generic "Error: exit code N" line so the tool's own output
		// is the only error shown (mirrors the backup command), and propagate
		// the real exit code via ExitCode in main (issue #36).
		var exitErr *exec.ExitCodeError
		if errors.As(runErr, &exitErr) {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true

			// Only when the wrapped tool actually reported that it needs an
			// interactive terminal: point the user at its non-interactive form.
			// `caam exec <tool> <profile> -- ...` runs the tool in its default
			// interactive mode, which can't start when stdin is not a TTY
			// (piped, redirected, CI, or an agent harness).
			if exitErr.NeedsTTY {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"\ncaam: %q exited %d because it needs an interactive terminal, but stdin\n"+
						"is not a TTY here. To run non-interactively, use the tool's non-interactive\n"+
						"form, for example:\n\n  %s\n",
					tool, exitErr.Code, nonInteractiveExecExample(tool, name, toolArgs))
			}
		}
		return runErr
	},
}

// nonInteractiveExecExample returns an example `caam exec` invocation that runs
// the given tool non-interactively, reusing the user's profile and arguments.
// It is shown as a hint when an interactive session fails because stdin is not a
// TTY. The mapping is best-effort; unknown tools get a generic placeholder.
func nonInteractiveExecExample(tool, profileName string, toolArgs []string) string {
	args := strings.Join(quoteArgsForHint(toolArgs), " ")
	switch tool {
	case "codex":
		// codex runs non-interactively via the `exec` subcommand (alias `e`).
		if len(toolArgs) > 0 && (toolArgs[0] == "exec" || toolArgs[0] == "e") {
			return strings.TrimRight(fmt.Sprintf("caam exec codex %s -- %s", profileName, args), " ")
		}
		return strings.TrimRight(fmt.Sprintf("caam exec codex %s -- exec %s", profileName, args), " ")
	case "claude", "gemini", "agy":
		// claude/gemini/agy run a single prompt non-interactively via -p/--print.
		return strings.TrimRight(fmt.Sprintf("caam exec %s %s -- -p %s", tool, profileName, args), " ")
	default:
		return strings.TrimRight(fmt.Sprintf("caam exec %s %s -- <non-interactive flag> %s", tool, profileName, args), " ")
	}
}

// quoteArgsForHint double-quotes any argument containing whitespace so the
// rendered hint is copy-pasteable.
func quoteArgsForHint(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			out[i] = fmt.Sprintf("%q", a)
		} else {
			out[i] = a
		}
	}
	return out
}

func init() {
	execCmd.Flags().Bool("no-lock", false, "don't lock the profile during execution")
}
