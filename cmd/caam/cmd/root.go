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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/gemini"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/tui"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
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
)

// Tools supported for auth file swapping
var tools = map[string]func() authfile.AuthFileSet{
	"codex":  authfile.CodexAuthFiles,
	"claude": authfile.ClaudeAuthFiles,
	"gemini": authfile.GeminiAuthFiles,
}

// rootCmd represents the base command.
var rootCmd = &cobra.Command{
	Use:   "caam",
	Short: "Coding Agent Account Manager - instant auth switching",
	Long: `caam (Coding Agent Account Manager) manages auth files for AI coding CLIs
to enable instant account switching for "all you can eat" subscription plans
(GPT Pro, Claude Max, Gemini Ultra).

When you hit usage limits on one account, switch to another in under a second:

  1. Login to each account once (using the tool's normal login flow)
  2. Backup the auth: caam backup claude my-account-1
  3. Later, switch instantly: caam activate claude my-account-2

No browser flows, no waiting. Just instant auth file swapping.

Supported tools:
  - codex   (OpenAI Codex CLI / GPT Pro)
  - claude  (Anthropic Claude Code / Claude Max)
  - gemini  (Google Gemini CLI / Gemini Ultra)

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

		// Initialize runner
		runner = exec.NewRunner(registry)

		// Load config
		var err error
		cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		return nil
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// isTerminal returns true if stdout is a terminal.
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// getProfileHealth returns health info for a profile by parsing auth files and checking metadata.
func getProfileHealth(tool, profileName string) *health.ProfileHealth {
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
		authPath := vaultPath + "/auth.json"
		expInfo, err = health.ParseCodexExpiry(authPath)
	case "gemini":
		expInfo, err = health.ParseGeminiExpiry(vaultPath)
	}

	// If file parsing succeeds and provides an expiry, treat it as authoritative
	if err == nil && expInfo != nil && !expInfo.ExpiresAt.IsZero() {
		ph.TokenExpiresAt = expInfo.ExpiresAt
	}

	return ph
}

// getCooldownString returns a formatted string showing cooldown remaining time.
// Returns empty string if no active cooldown or if db is unavailable.
func getCooldownString(provider, profile string, opts health.FormatOptions) string {
	db, err := caamdb.Open()
	if err != nil {
		return ""
	}
	defer db.Close()

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

	db, err := caamdb.Open()
	if err != nil {
		return false, 0, ""
	}
	defer db.Close()

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

The auth files are copied to ~/.local/share/caam/vault/<tool>/<profile>/

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
			return nil
		}
		return err
	}

	getFileSet, ok := tools[tool]
	if !ok {
		return emitJSONError(fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool))
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
	Tool          string        `json:"tool"`
	LoggedIn      bool          `json:"logged_in"`
	ActiveProfile string        `json:"active_profile,omitempty"`
	Error         string        `json:"error,omitempty"`
	Health        *statusHealth `json:"health,omitempty"`
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

	toolsToCheck := []string{"codex", "claude", "gemini"}
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
	}

	for _, tool := range toolsToCheck {
		fileSet := tools[tool]()
		hasAuth := authfile.HasAuthFiles(fileSet)

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
			if jsonOutput {
				output.Tools = append(output.Tools, statusTool{
					Tool:     tool,
					LoggedIn: true,
				})
			} else {
				fmt.Printf("%-10s  (logged in, no matching profile)\n", tool)
			}
			continue
		}

		// Get health info
		ph := getProfileHealth(tool, activeProfile)
		status := health.CalculateStatus(ph)

		if jsonOutput {
			st := statusTool{
				Tool:          tool,
				LoggedIn:      true,
				ActiveProfile: activeProfile,
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

			// Check for active cooldown and append remaining time
			cooldownStr := getCooldownString(tool, activeProfile, formatOpts)
			if cooldownStr != "" {
				healthStr = healthStr + " " + cooldownStr
			}

			fmt.Printf("%-10s  %-20s  %s\n", tool, activeProfile, healthStr)
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
	Tool   string   `json:"tool"`
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	System bool     `json:"system"`
	Health lsHealth `json:"health"`
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
  caam ls           # List all profiles
  caam ls claude    # List just Claude profiles
  caam ls --no-color  # Without colors (for piping)
  caam ls --json      # Output as JSON`,
	Args: cobra.MaximumNArgs(1),
	RunE: runLs,
}

func init() {
	lsCmd.Flags().Bool("no-color", false, "disable colored output")
	lsCmd.Flags().Bool("json", false, "output as JSON")
}

func runLs(cmd *cobra.Command, args []string) error {
	noColor, _ := cmd.Flags().GetBool("no-color")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	formatOpts := health.FormatOptions{NoColor: noColor || !isTerminal()}

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

		if len(profiles) == 0 {
			if jsonOutput {
				output.Profiles = []lsProfile{}
				output.Count = 0
				return encodeLsJSON(cmd, output)
			}
			fmt.Printf("No profiles saved for %s\n", tool)
			return nil
		}

		// Check which is active
		fileSet := tools[tool]()
		activeProfile, _ := vault.ActiveProfile(fileSet)

		for _, p := range profiles {
			ph := getProfileHealth(tool, p)
			status := health.CalculateStatus(ph)

			if jsonOutput {
				lp := lsProfile{
					Tool:   tool,
					Name:   p,
					Active: p == activeProfile,
					System: authfile.IsSystemProfile(p),
					Health: lsHealth{
						Status:     status.String(),
						ErrorCount: ph.ErrorCount1h,
					},
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

				tag := ""
				if authfile.IsSystemProfile(p) {
					tag = "[system]"
				}

				healthStr := health.FormatHealthStatus(status, ph, formatOpts)
				fmt.Printf("%s%-20s  %-8s  %s\n", marker, p, tag, healthStr)
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

	if len(allProfiles) == 0 {
		if jsonOutput {
			output.Profiles = []lsProfile{}
			output.Count = 0
			return encodeLsJSON(cmd, output)
		}
		fmt.Println("No profiles saved yet.")
		fmt.Println("\nTo save your first profile:")
		fmt.Println("  1. Login using the tool's command (codex login, /login in claude)")
		fmt.Println("  2. Run: caam backup <tool> <profile-name>")
		return nil
	}

	for tool, profiles := range allProfiles {
		fileSet := tools[tool]()
		activeProfile, _ := vault.ActiveProfile(fileSet)

		if !jsonOutput {
			fmt.Printf("%s:\n", tool)
		}

		for _, p := range profiles {
			ph := getProfileHealth(tool, p)
			status := health.CalculateStatus(ph)

			if jsonOutput {
				lp := lsProfile{
					Tool:   tool,
					Name:   p,
					Active: p == activeProfile,
					System: authfile.IsSystemProfile(p),
					Health: lsHealth{
						Status:     status.String(),
						ErrorCount: ph.ErrorCount1h,
					},
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

				tag := ""
				if authfile.IsSystemProfile(p) {
					tag = "[system]"
				}

				healthStr := health.FormatHealthStatus(status, ph, formatOpts)
				fmt.Printf("  %s%-20s  %-8s  %s\n", marker, p, tag, healthStr)
			}
		}
	}

	if jsonOutput {
		output.Count = len(output.Profiles)
		return encodeLsJSON(cmd, output)
	}

	return nil
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

// pathsCmd shows auth file paths for each tool.
var pathsCmd = &cobra.Command{
	Use:   "paths [tool]",
	Short: "Show auth file paths",
	Long: `Shows where each tool stores its auth files.

Useful for understanding what caam is backing up and for manual troubleshooting.

Examples:
  caam paths           # Show all tools
  caam paths claude    # Show just Claude`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		toolsToShow := []string{"codex", "claude", "gemini"}
		if len(args) > 0 {
			tool := strings.ToLower(args[0])
			if _, ok := tools[tool]; !ok {
				return fmt.Errorf("unknown tool: %s", tool)
			}
			toolsToShow = []string{tool}
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
			fmt.Fprintf(os.Stderr, "warning: could not setup passthroughs: %v\n", err)
		} else {
			if err := passMgr.SetupPassthroughs(cloned.HomePath()); err != nil {
				fmt.Fprintf(os.Stderr, "warning: passthrough setup failed: %v\n", err)
			}
		}

		fmt.Printf("Cloned %s/%s → %s/%s\n", tool, sourceName, tool, targetName)
		fmt.Printf("  Path: %s\n", cloned.BasePath)
		if cloned.Description != "" {
			fmt.Printf("  Description: %s\n", cloned.Description)
		}
		if withAuth {
			fmt.Println("  Auth files: copied")
		} else {
			fmt.Println("  Auth files: not copied (use --with-auth to include)")
		}

		fmt.Printf("\nNext steps:\n")
		if !withAuth {
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

		prof, err := profileStore.Load(tool, name)
		if err != nil {
			return err
		}

		ctx := context.Background()
		noLock, _ := cmd.Flags().GetBool("no-lock")

		return runner.Run(ctx, exec.RunOptions{
			Profile:  prof,
			Provider: prov,
			Args:     toolArgs,
			NoLock:   noLock,
		})
	},
}

func init() {
	execCmd.Flags().Bool("no-lock", false, "don't lock the profile during execution")
}
