package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

var addCmd = &cobra.Command{
	Use:   "add <tool> [profile-name]",
	Short: "Add a new account with one command",
	Long: `Add a new account by automating the login flow.

This command streamlines adding a new account:
  1. Backs up current auth (if exists) to a timestamped backup
  2. Clears existing auth files
  3. Launches the tool's login flow
  4. Waits for you to complete authentication
  5. Saves the new auth as a profile
  6. Optionally activates the new profile

Examples:
  caam add claude              # Interactive - prompts for profile name
  caam add claude work-2       # Pre-specify profile name
  caam add codex --no-activate # Don't activate after adding
  caam add gemini --timeout 5m # Custom timeout for login flow`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAdd,
}

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().Bool("no-activate", false, "don't activate the new profile after adding")
	addCmd.Flags().Duration("timeout", 5*time.Minute, "timeout for login flow completion")
	addCmd.Flags().Bool("force", false, "skip confirmation prompts")
}

func runAdd(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	noActivate, _ := cmd.Flags().GetBool("no-activate")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	force, _ := cmd.Flags().GetBool("force")

	getFileSet, ok := tools[tool]
	if !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	// Initialize vault
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	fileSet := getFileSet()

	// Determine profile name
	var profileName string
	if len(args) == 2 {
		profileName = args[1]
	}

	// Check if profile name already exists
	if profileName != "" {
		profiles, err := vault.List(tool)
		if err == nil {
			for _, p := range profiles {
				if p == profileName {
					return fmt.Errorf("profile %s/%s already exists (use a different name or delete it first)", tool, profileName)
				}
			}
		}
	}

	// Check if auth files currently exist
	hasExistingAuth := authfile.HasAuthFiles(fileSet)

	if hasExistingAuth && !force {
		fmt.Printf("Current %s auth will be backed up and cleared.\n", tool)
		fmt.Print("Continue? [Y/n]: ")
		if !promptConfirm(true) {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Step 1: Backup current auth if exists
	if hasExistingAuth {
		backupName := fmt.Sprintf("_auto_backup_%s", time.Now().Format("20060102_150405"))
		fmt.Printf("Backing up current auth to %s/%s...\n", tool, backupName)
		if err := vault.Backup(fileSet, backupName); err != nil {
			return fmt.Errorf("backup current auth: %w", err)
		}
		fmt.Printf("  Backed up to %s/%s\n", tool, backupName)
	}

	// Step 2: Clear auth files
	fmt.Printf("Clearing %s auth files...\n", tool)
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); err == nil {
			if err := os.Remove(spec.Path); err != nil {
				return fmt.Errorf("remove %s: %w", spec.Path, err)
			}
		}
	}

	// Step 3: Launch login flow
	fmt.Printf("\nLaunching %s login...\n", tool)
	fmt.Println("Complete the authentication in the terminal/browser.")
	fmt.Println("Press Ctrl+C when done or if you want to cancel.")
	fmt.Println()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan error, 1)

	go func() {
		done <- runToolLogin(ctx, tool)
	}()

	select {
	case err := <-done:
		signal.Stop(sigChan)
		if err != nil && ctx.Err() != context.Canceled {
			// Login process exited - check if auth files appeared
			if !authfile.HasAuthFiles(fileSet) {
				fmt.Println("\nLogin process exited but no auth files were created.")
				fmt.Println("The login may have failed. Try again with: caam add " + tool)
				return fmt.Errorf("login did not create auth files")
			}
		}
	case <-sigChan:
		signal.Stop(sigChan)
		cancel()
		fmt.Println("\n\nLogin interrupted.")
	case <-ctx.Done():
		signal.Stop(sigChan)
		fmt.Printf("\nTimeout after %v waiting for login to complete.\n", timeout)
		return fmt.Errorf("login timed out")
	}

	// Step 4: Check if auth files appeared
	if !authfile.HasAuthFiles(fileSet) {
		fmt.Println("\nNo auth files detected after login.")
		return fmt.Errorf("login did not create auth files")
	}

	fmt.Println("\nLogin successful!")

	// Step 5: Prompt for profile name if not provided
	if profileName == "" {
		fmt.Print("Profile name: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		profileName = strings.TrimSpace(input)
		if profileName == "" {
			profileName = "new-account"
		}
	}

	// Validate profile name
	if strings.HasPrefix(profileName, "_") {
		return fmt.Errorf("profile names starting with '_' are reserved for system use")
	}

	// Check again if profile exists (in case user entered same name interactively)
	profiles, _ := vault.List(tool)
	for _, p := range profiles {
		if p == profileName {
			// Generate unique name
			profileName = fmt.Sprintf("%s_%s", profileName, time.Now().Format("150405"))
			fmt.Printf("Profile name already exists, using: %s\n", profileName)
			break
		}
	}

	// Step 6: Save as new profile
	fmt.Printf("Saving as %s/%s...\n", tool, profileName)
	if err := vault.Backup(fileSet, profileName); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}
	fmt.Printf("  Saved %s/%s\n", tool, profileName)

	// Step 7: Optionally activate
	if !noActivate {
		fmt.Printf("Activating %s/%s...\n", tool, profileName)
		if err := vault.Restore(fileSet, profileName); err != nil {
			return fmt.Errorf("activate profile: %w", err)
		}
		fmt.Printf("  Activated %s/%s\n", tool, profileName)
	}

	fmt.Println()
	fmt.Println("Done! Your new account has been added.")
	fmt.Printf("\nQuick commands:\n")
	fmt.Printf("  caam activate %s %s  # Switch to this profile\n", tool, profileName)
	fmt.Printf("  caam ls %s            # List all %s profiles\n", tool, tool)

	return nil
}

// runToolLogin launches the tool's login command.
func runToolLogin(ctx context.Context, tool string) error {
	var cmd *exec.Cmd

	switch tool {
	case "claude":
		// Claude uses interactive login
		cmd = exec.CommandContext(ctx, "claude")
	case "codex":
		// Codex uses `codex auth login`
		cmd = exec.CommandContext(ctx, "codex", "auth", "login")
	case "gemini":
		// Gemini uses `gemini auth login`
		cmd = exec.CommandContext(ctx, "gemini", "auth", "login")
	default:
		return fmt.Errorf("unsupported tool: %s", tool)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// promptConfirm prompts for yes/no confirmation.
func promptConfirm(defaultYes bool) bool {
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}
