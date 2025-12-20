// Package cmd implements the CLI commands for caam.
package cmd

import (
	"bufio"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/discovery"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize caam with interactive setup wizard",
	Long: `Interactive setup wizard that discovers existing AI tool sessions and guides
you through first-time setup.

The wizard will:
  1. Discover existing auth sessions (Claude, Codex, Gemini)
  2. Save discovered sessions as profiles for instant switching
  3. Optionally set up shell integration for seamless usage

This transforms a 10-minute manual setup into a 2-minute guided experience.

Examples:
  caam init           # Interactive wizard (recommended)
  caam init --quick   # Auto-save all discovered sessions, skip prompts
  caam init --no-shell  # Skip shell integration step`,
	RunE: runInitWizard,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().Bool("quiet", false, "non-interactive mode, just create directories")
	initCmd.Flags().Bool("quick", false, "auto-save all discovered sessions without prompts")
	initCmd.Flags().Bool("no-shell", false, "skip shell integration step")
}

func runInitWizard(cmd *cobra.Command, args []string) error {
	quiet, _ := cmd.Flags().GetBool("quiet")
	quick, _ := cmd.Flags().GetBool("quick")
	noShell, _ := cmd.Flags().GetBool("no-shell")

	// Quiet mode: just create directories
	if quiet {
		if err := createDirectories(true); err != nil {
			return err
		}
		return nil
	}

	// Print welcome banner
	printWelcomeBanner()

	// Phase 1: Create directories
	if err := createDirectories(false); err != nil {
		return err
	}

	// Phase 2: Detect tools
	detectTools(false)

	// Phase 3: Discover existing auth sessions
	scanResult := discovery.Scan()
	printDiscoveryResults(scanResult)

	// Phase 4: Save discovered sessions as profiles
	savedCount := 0
	if len(scanResult.Found) > 0 {
		savedCount = saveDiscoveredSessions(scanResult.Found, quick)
	}

	// Phase 5: Shell integration (optional)
	if !noShell && (quick || promptYesNo("Set up shell integration for seamless usage?", true)) {
		setupShellIntegration()
	}

	// Phase 6: Print summary
	printSetupSummary(scanResult, savedCount)

	return nil
}

func printWelcomeBanner() {
	fmt.Println()
	fmt.Println("  ============================================================")
	fmt.Println("          CAAM - Coding Agent Account Manager")
	fmt.Println("        Instant switching for AI coding tools")
	fmt.Println("  ============================================================")
	fmt.Println()
}

func printDiscoveryResults(result *discovery.ScanResult) {
	fmt.Println()
	fmt.Println("Scanning for existing AI tool sessions...")
	fmt.Println()

	if len(result.Found) == 0 {
		fmt.Println("  No existing sessions found.")
		fmt.Println()
		fmt.Println("  To get started:")
		fmt.Println("    1. Log in to your AI tool (claude, codex, or gemini)")
		fmt.Println("    2. Run: caam backup <tool> <profile-name>")
		fmt.Println()
		return
	}

	fmt.Println("  Found:")
	for _, auth := range result.Found {
		status := "logged in"
		if auth.Identity != "" {
			status = fmt.Sprintf("logged in as %s", auth.Identity)
		}
		fmt.Printf("    [OK] %-8s %s (%s)\n", auth.Tool, auth.Path, status)
	}

	for _, tool := range result.NotFound {
		fmt.Printf("    [--] %-8s not found or not logged in\n", tool)
	}
	fmt.Println()
}

func saveDiscoveredSessions(found []discovery.DiscoveredAuth, autoSave bool) int {
	fmt.Println("------------------------------------------------------------")
	fmt.Println("  STEP 1: Save Current Sessions")
	fmt.Println("------------------------------------------------------------")
	fmt.Println()
	fmt.Println("  Saving your sessions as profiles lets you switch back to them later.")
	fmt.Println()

	vault := authfile.NewVault(authfile.DefaultVaultPath())
	savedCount := 0

	for _, auth := range found {
		// Suggest profile name based on identity
		suggested := suggestProfileName(auth)

		var profileName string
		if autoSave {
			profileName = suggested
			fmt.Printf("  Saving %s as '%s'...\n", auth.Tool, profileName)
		} else {
			fmt.Printf("  Save your %s session as a profile?\n", auth.Tool)
			if auth.Identity != "" {
				fmt.Printf("  Currently logged in as: %s\n", auth.Identity)
			}
			profileName = promptWithDefault(fmt.Sprintf("  Profile name [%s]:", suggested), suggested)
			if profileName == "" {
				fmt.Println("  Skipped.")
				continue
			}
		}

		// Get the auth file set for this tool
		fileSet := getAuthFileSetForTool(string(auth.Tool))
		if fileSet == nil {
			fmt.Printf("  Error: unknown tool %s\n", auth.Tool)
			continue
		}

		// Backup to vault
		if err := vault.Backup(*fileSet, profileName); err != nil {
			fmt.Printf("  Error saving profile: %v\n", err)
			continue
		}

		fmt.Printf("  [OK] Saved %s/%s\n", auth.Tool, profileName)
		savedCount++
	}

	fmt.Println()
	return savedCount
}

func suggestProfileName(auth discovery.DiscoveredAuth) string {
	if auth.Identity != "" {
		// Use email prefix or full identity
		if idx := strings.Index(auth.Identity, "@"); idx > 0 {
			return auth.Identity[:idx]
		}
		// Clean up identity for use as profile name
		name := strings.ReplaceAll(auth.Identity, " ", "_")
		name = strings.ReplaceAll(name, "/", "_")
		if len(name) > 20 {
			name = name[:20]
		}
		return name
	}
	return "main"
}

func getAuthFileSetForTool(tool string) *authfile.AuthFileSet {
	switch tool {
	case "claude":
		set := authfile.ClaudeAuthFiles()
		return &set
	case "codex":
		set := authfile.CodexAuthFiles()
		return &set
	case "gemini":
		set := authfile.GeminiAuthFiles()
		return &set
	default:
		return nil
	}
}

func setupShellIntegration() {
	fmt.Println()
	fmt.Println("------------------------------------------------------------")
	fmt.Println("  STEP 2: Shell Integration")
	fmt.Println("------------------------------------------------------------")
	fmt.Println()
	fmt.Println("  Shell integration creates wrapper functions so that running")
	fmt.Println("  'claude', 'codex', or 'gemini' automatically uses caam's")
	fmt.Println("  rate limit handling and profile switching.")
	fmt.Println()

	// Detect shell
	shell := detectCurrentShell()
	fmt.Printf("  Detected shell: %s\n", shell)

	// Get the init command
	initLine := getShellInitLine(shell)
	rcFile := getShellRCFile(shell)

	fmt.Println()
	fmt.Println("  Add this line to your shell config:")
	fmt.Printf("    %s\n", initLine)
	fmt.Println()

	if rcFile != "" {
		if promptYesNo(fmt.Sprintf("  Add to %s now?", rcFile), true) {
			if err := appendToShellRC(rcFile, initLine); err != nil {
				fmt.Printf("  Error: %v\n", err)
				fmt.Println("  Please add the line manually.")
			} else {
				fmt.Printf("  [OK] Added to %s\n", rcFile)
				fmt.Println()
				fmt.Println("  Run this to activate now:")
				fmt.Printf("    source %s\n", rcFile)
			}
		}
	}
	fmt.Println()
}

func detectCurrentShell() string {
	shell := os.Getenv("SHELL")
	if shell != "" {
		base := filepath.Base(shell)
		switch base {
		case "fish":
			return "fish"
		case "zsh":
			return "zsh"
		case "bash":
			return "bash"
		}
	}
	return "bash"
}

func getShellInitLine(shell string) string {
	switch shell {
	case "fish":
		return "caam shell init --fish | source"
	default:
		return `eval "$(caam shell init)"`
	}
}

func getShellRCFile(shell string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	switch shell {
	case "fish":
		return filepath.Join(homeDir, ".config", "fish", "config.fish")
	case "zsh":
		return filepath.Join(homeDir, ".zshrc")
	case "bash":
		// Check if .bashrc exists, otherwise .bash_profile
		bashrc := filepath.Join(homeDir, ".bashrc")
		if _, err := os.Stat(bashrc); err == nil {
			return bashrc
		}
		return filepath.Join(homeDir, ".bash_profile")
	default:
		return filepath.Join(homeDir, ".bashrc")
	}
}

func appendToShellRC(rcFile, line string) error {
	// Check if line already exists
	content, err := os.ReadFile(rcFile)
	if err == nil && strings.Contains(string(content), "caam shell init") {
		// Already configured
		return nil
	}

	// Append to file
	f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add a newline and comment before the init line
	_, err = f.WriteString(fmt.Sprintf("\n# caam shell integration\n%s\n", line))
	return err
}

func printSetupSummary(result *discovery.ScanResult, savedCount int) {
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  Setup Complete!")
	fmt.Println("============================================================")
	fmt.Println()

	if savedCount > 0 {
		fmt.Printf("  Saved %d profile(s) to vault.\n", savedCount)
		fmt.Println()
	}

	fmt.Println("  Quick commands:")
	fmt.Println("    caam status      - Show current profiles and status")
	fmt.Println("    caam ls          - List all saved profiles")
	fmt.Println("    caam activate <tool> <profile> - Switch to a profile")
	fmt.Println("    caam run <tool>  - Run with automatic rate limit handling")
	fmt.Println()

	if len(result.NotFound) > 0 {
		fmt.Println("  To add more accounts:")
		for _, tool := range result.NotFound {
			fmt.Printf("    1. Log in to %s\n", tool)
			fmt.Printf("    2. Run: caam backup %s <profile-name>\n", tool)
		}
		fmt.Println()
	}

	fmt.Println("  Happy coding!")
	fmt.Println()
}

// createDirectories creates the necessary data directories.
func createDirectories(quiet bool) error {
	if !quiet {
		fmt.Println("Creating directories...")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	// Determine paths using XDG conventions
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(homeDir, ".local", "share")
	}

	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(homeDir, ".config")
	}

	dirs := []struct {
		path string
		name string
	}{
		{filepath.Join(xdgData, "caam"), "caam data directory"},
		{authfile.DefaultVaultPath(), "vault directory"},
		{profile.DefaultStorePath(), "profiles directory"},
		{filepath.Join(xdgConfig, "caam"), "config directory"},
	}

	for _, dir := range dirs {
		// Check if exists
		if info, err := os.Stat(dir.path); err == nil && info.IsDir() {
			if !quiet {
				fmt.Printf("  [OK] %s already exists\n", dir.name)
			}
			continue
		}

		// Create directory
		if err := os.MkdirAll(dir.path, 0700); err != nil {
			return fmt.Errorf("create %s: %w", dir.name, err)
		}

		if !quiet {
			fmt.Printf("  [OK] Created %s\n", dir.name)
		}
	}

	if !quiet {
		fmt.Println()
	}

	return nil
}

// detectTools checks for installed CLI tools.
func detectTools(quiet bool) {
	if !quiet {
		fmt.Println("Detecting CLI tools...")
	}

	toolBinaries := map[string]string{
		"codex":  "codex",
		"claude": "claude",
		"gemini": "gemini",
	}

	foundCount := 0
	for tool, binary := range toolBinaries {
		path, err := osexec.LookPath(binary)
		if err == nil {
			if !quiet {
				fmt.Printf("  [OK] %s found at %s\n", tool, path)
			}
			foundCount++
		} else {
			if !quiet {
				fmt.Printf("  [--] %s not found\n", tool)
			}
		}
	}

	if !quiet {
		fmt.Println()
		if foundCount == 0 {
			fmt.Println("  No CLI tools found. Install at least one:")
			fmt.Println("    - Codex CLI: https://github.com/openai/codex-cli")
			fmt.Println("    - Claude Code: https://github.com/anthropics/claude-code")
			fmt.Println("    - Gemini CLI: https://github.com/google/gemini-cli")
			fmt.Println()
		}
	}
}

// promptWithDefault prompts for input with a default value.
func promptWithDefault(prompt, defaultVal string) string {
	fmt.Print(prompt + " ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal
	}
	return input
}

// promptYesNo prompts for a yes/no answer.
func promptYesNo(prompt string, defaultYes bool) bool {
	suffix := " [Y/n]: "
	if !defaultYes {
		suffix = " [y/N]: "
	}

	fmt.Print(prompt + suffix)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes"
}
