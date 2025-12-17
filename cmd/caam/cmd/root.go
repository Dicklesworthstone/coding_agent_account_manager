// Package cmd implements the CLI commands for caam (Coding Agent Account Manager).
//
// caam manages auth files for AI coding CLIs to enable instant account switching
// for "all you can eat" subscription plans (GPT Pro, Claude Max, Gemini Ultra).
//
// Two modes of operation:
//   1. Auth file swapping (PRIMARY): backup/activate to instantly switch accounts
//   2. Profile isolation: run tools with isolated HOME/CODEX_HOME for simultaneous sessions
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/user/caam/internal/authfile"
	"github.com/user/caam/internal/config"
	"github.com/user/caam/internal/exec"
	"github.com/user/caam/internal/profile"
	"github.com/user/caam/internal/provider"
	"github.com/user/caam/internal/provider/claude"
	"github.com/user/caam/internal/provider/codex"
	"github.com/user/caam/internal/provider/gemini"
	"github.com/user/caam/internal/version"
)

var (
	vault        *authfile.Vault
	profileStore *profile.Store
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
  caam exec codex work -- "implement feature X"`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Initialize vault
		vault = authfile.NewVault(authfile.DefaultVaultPath())

		// Initialize profile store
		profileStore = profile.NewStore(profile.DefaultStorePath())

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

func init() {
	// Core commands (auth file swapping - PRIMARY)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(activateCmd)
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
  caam backup gemini team-ultra`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		profileName := args[1]

		getFileSet, ok := tools[tool]
		if !ok {
			return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
		}

		fileSet := getFileSet()

		// Check if auth files exist
		if !authfile.HasAuthFiles(fileSet) {
			return fmt.Errorf("no auth files found for %s - login first using the tool's login command", tool)
		}

		// Backup to vault
		if err := vault.Backup(fileSet, profileName); err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}

		fmt.Printf("Backed up %s auth to profile '%s'\n", tool, profileName)
		fmt.Printf("  Vault: %s\n", vault.ProfilePath(tool, profileName))
		return nil
	},
}

// activateCmd restores auth files from the vault.
var activateCmd = &cobra.Command{
	Use:     "activate <tool> <profile-name>",
	Aliases: []string{"switch", "use"},
	Short:   "Activate a profile (instant switch)",
	Long: `Restores auth files from the vault, instantly switching to that account.

This is the magic command - sub-second account switching without any login flows!

Examples:
  caam activate codex work-account
  caam activate claude personal-max
  caam activate gemini team-ultra

After activating, just run the tool normally - it will use the new account.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		profileName := args[1]

		getFileSet, ok := tools[tool]
		if !ok {
			return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
		}

		fileSet := getFileSet()

		// Optionally backup current state first
		backupFirst, _ := cmd.Flags().GetBool("backup-current")
		if backupFirst {
			currentProfile, _ := vault.ActiveProfile(fileSet)
			if currentProfile != "" && currentProfile != profileName {
				if err := vault.Backup(fileSet, currentProfile); err != nil {
					fmt.Printf("Warning: could not backup current profile: %v\n", err)
				}
			}
		}

		// Restore from vault
		if err := vault.Restore(fileSet, profileName); err != nil {
			return fmt.Errorf("activate failed: %w", err)
		}

		fmt.Printf("Activated %s profile '%s'\n", tool, profileName)
		fmt.Printf("  Run '%s' to start using this account\n", tool)
		return nil
	},
}

func init() {
	activateCmd.Flags().Bool("backup-current", false, "backup current auth before switching")
}

// statusCmd shows which profile is currently active.
var statusCmd = &cobra.Command{
	Use:   "status [tool]",
	Short: "Show active profiles",
	Long: `Shows which vault profile (if any) matches the current auth state for each tool.

Examples:
  caam status           # Show all tools
  caam status claude    # Show just Claude`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		toolsToCheck := []string{"codex", "claude", "gemini"}
		if len(args) > 0 {
			tool := strings.ToLower(args[0])
			if _, ok := tools[tool]; !ok {
				return fmt.Errorf("unknown tool: %s", tool)
			}
			toolsToCheck = []string{tool}
		}

		for _, tool := range toolsToCheck {
			fileSet := tools[tool]()
			hasAuth := authfile.HasAuthFiles(fileSet)

			if !hasAuth {
				fmt.Printf("%s: not logged in\n", tool)
				continue
			}

			activeProfile, err := vault.ActiveProfile(fileSet)
			if err != nil {
				fmt.Printf("%s: logged in (error checking profile: %v)\n", tool, err)
				continue
			}

			if activeProfile != "" {
				fmt.Printf("%s: active profile '%s'\n", tool, activeProfile)
			} else {
				fmt.Printf("%s: logged in (not matching any saved profile)\n", tool)
			}
		}

		return nil
	},
}

// lsCmd lists all stored profiles.
var lsCmd = &cobra.Command{
	Use:     "ls [tool]",
	Aliases: []string{"list"},
	Short:   "List saved profiles",
	Long: `Lists all profiles stored in the vault.

Examples:
  caam ls           # List all profiles
  caam ls claude    # List just Claude profiles`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
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
				fmt.Printf("No profiles saved for %s\n", tool)
				return nil
			}

			// Check which is active
			fileSet := tools[tool]()
			activeProfile, _ := vault.ActiveProfile(fileSet)

			for _, p := range profiles {
				marker := "  "
				if p == activeProfile {
					marker = "* "
				}
				fmt.Printf("%s%s/%s\n", marker, tool, p)
			}
			return nil
		}

		// List all
		allProfiles, err := vault.ListAll()
		if err != nil {
			return err
		}

		if len(allProfiles) == 0 {
			fmt.Println("No profiles saved yet.")
			fmt.Println("\nTo save your first profile:")
			fmt.Println("  1. Login using the tool's command (codex login, /login in claude)")
			fmt.Println("  2. Run: caam backup <tool> <profile-name>")
			return nil
		}

		for tool, profiles := range allProfiles {
			fileSet := tools[tool]()
			activeProfile, _ := vault.ActiveProfile(fileSet)

			fmt.Printf("%s:\n", tool)
			for _, p := range profiles {
				marker := "  "
				if p == activeProfile {
					marker = "* "
				}
				fmt.Printf("  %s%s\n", marker, p)
			}
		}

		return nil
	},
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
		if !force {
			fmt.Printf("Delete profile %s/%s? [y/N]: ", tool, profileName)
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "y" {
				fmt.Println("Cancelled")
				return nil
			}
		}

		if err := vault.Delete(tool, profileName); err != nil {
			return fmt.Errorf("delete failed: %w", err)
		}

		fmt.Printf("Deleted %s/%s\n", tool, profileName)
		return nil
	},
}

func init() {
	deleteCmd.Flags().Bool("force", false, "skip confirmation")
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
}

var profileAddCmd = &cobra.Command{
	Use:   "add <tool> <name> [--auth-mode oauth|api-key]",
	Short: "Create a new isolated profile",
	Args:  cobra.ExactArgs(2),
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

		// Prepare profile directory structure
		ctx := context.Background()
		if err := prov.PrepareProfile(ctx, prof); err != nil {
			// Clean up on failure
			profileStore.Delete(tool, name)
			return fmt.Errorf("prepare profile: %w", err)
		}

		fmt.Printf("Created profile %s/%s\n", tool, name)
		fmt.Printf("  Path: %s\n", prof.BasePath)
		fmt.Printf("\nNext steps:\n")
		fmt.Printf("  caam login %s %s    # Authenticate\n", tool, name)
		fmt.Printf("  caam exec %s %s     # Run with this profile\n", tool, name)
		return nil
	},
}

func init() {
	profileAddCmd.Flags().String("auth-mode", "oauth", "authentication mode (oauth, api-key)")
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
				fmt.Printf("  %s/%s%s\n", p.Provider, p.Name, status)
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
				fmt.Printf("  %s%s\n", p.Name, status)
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

		return nil
	},
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
  caam login claude home    # Login to home profile`,
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
		if err := prov.Login(ctx, prof); err != nil {
			return fmt.Errorf("login failed: %w", err)
		}

		fmt.Printf("\nLogin complete for %s/%s\n", tool, name)
		return nil
	},
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
