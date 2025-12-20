// Package cmd implements the CLI commands for caam.
package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/discovery"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
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

	// Phase 3: NEW - Provider-based auth detection
	providerDetections := detectProviderAuth()
	printProviderDetectionResults(providerDetections)

	// Phase 4: NEW - Import detected auth as profiles
	importedCount := 0
	hasDetectedAuth := false
	for _, d := range providerDetections {
		if d.Error == nil && d.Detection != nil && d.Detection.Found {
			hasDetectedAuth = true
			break
		}
	}
	if hasDetectedAuth {
		importedCount = importDetectedAuth(providerDetections, quick)
	}

	// Phase 5: Fallback - Also run legacy discovery for any sessions not covered
	scanResult := discovery.Scan()
	legacySavedCount := 0
	if len(scanResult.Found) > 0 && importedCount == 0 {
		// Only show legacy discovery if new import didn't find anything
		printDiscoveryResults(scanResult)
		legacySavedCount = saveDiscoveredSessions(scanResult.Found, quick)
	}

	totalSaved := importedCount + legacySavedCount

	// Phase 6: Browser configuration (optional)
	browserConfigured := false
	if !quick {
		browserConfigured = setupBrowserConfiguration()
	}

	// Phase 7: Shell integration (optional)
	if !noShell && (quick || promptYesNo("Set up shell integration for seamless usage?", true)) {
		setupShellIntegration()
	}

	// Phase 8: Print summary
	printSetupSummaryV2(providerDetections, totalSaved, browserConfigured)

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

func setupBrowserConfiguration() bool {
	browsers := browser.DetectBrowsers()
	if len(browsers) == 0 {
		return false
	}

	fmt.Println()
	fmt.Println("------------------------------------------------------------")
	fmt.Println("  STEP 2: Browser Profile Configuration (Optional)")
	fmt.Println("------------------------------------------------------------")
	fmt.Println()
	fmt.Println("  Configure browser profiles to automatically use the right")
	fmt.Println("  account during OAuth login (Google, GitHub, etc.).")
	fmt.Println()

	// Show detected browsers
	fmt.Println("  Detected browsers:")
	for i, b := range browsers {
		profileCount := len(b.Profiles)
		fmt.Printf("    [%d] %s (%d profile(s))\n", i+1, b.Name, profileCount)
	}
	fmt.Println("    [0] Skip browser configuration")
	fmt.Println()

	if !promptYesNo("Would you like to configure browser profiles?", false) {
		fmt.Println("  Skipping browser configuration.")
		fmt.Println()
		return false
	}

	fmt.Println()

	// Let user select a browser
	browserIdx := promptNumber("  Select browser number:", 0, len(browsers))
	if browserIdx == 0 {
		fmt.Println("  Skipping browser configuration.")
		fmt.Println()
		return false
	}

	selectedBrowser := browsers[browserIdx-1]

	// Show profiles for selected browser
	if len(selectedBrowser.Profiles) == 0 {
		fmt.Printf("  No profiles found in %s.\n", selectedBrowser.Name)
		fmt.Println()
		return false
	}

	fmt.Println()
	fmt.Printf("  Profiles in %s:\n", selectedBrowser.Name)
	for i, p := range selectedBrowser.Profiles {
		displayName := p.Name
		if p.Email != "" {
			displayName = fmt.Sprintf("%s (%s)", p.Name, p.Email)
		}
		if p.IsDefault {
			displayName += " [default]"
		}
		fmt.Printf("    [%d] %s\n", i+1, displayName)
	}
	fmt.Println("    [0] Cancel")
	fmt.Println()

	profileIdx := promptNumber("  Select profile number:", 0, len(selectedBrowser.Profiles))
	if profileIdx == 0 {
		fmt.Println("  Cancelled.")
		fmt.Println()
		return false
	}

	selectedProfile := selectedBrowser.Profiles[profileIdx-1]

	// Save to global config
	fmt.Println()
	fmt.Printf("  Browser: %s\n", selectedBrowser.Name)
	fmt.Printf("  Profile: %s\n", selectedProfile.Name)
	if selectedProfile.Email != "" {
		fmt.Printf("  Account: %s\n", selectedProfile.Email)
	}
	fmt.Println()

	fmt.Printf("  [OK] Configured %s with profile '%s'\n", selectedBrowser.Name, selectedProfile.Name)
	fmt.Println()
	fmt.Println("  This will be used during OAuth logins to automatically")
	fmt.Println("  open the correct browser profile.")
	fmt.Println()

	// Store in global config
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("  Warning: could not load config: %v\n", err)
		return false
	}

	cfg.BrowserCommand = selectedBrowser.Command
	cfg.BrowserProfileDir = selectedProfile.ID
	cfg.BrowserProfileName = selectedProfile.Name

	if err := cfg.Save(); err != nil {
		fmt.Printf("  Warning: could not save browser config: %v\n", err)
		return false
	}

	return true
}

// promptNumber prompts for a number in range [min, max].
func promptNumber(prompt string, min, max int) int {
	for {
		fmt.Print(prompt + " ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "" {
			return min
		}

		var num int
		if _, err := fmt.Sscanf(input, "%d", &num); err != nil {
			fmt.Printf("  Please enter a number between %d and %d.\n", min, max)
			continue
		}

		if num < min || num > max {
			fmt.Printf("  Please enter a number between %d and %d.\n", min, max)
			continue
		}

		return num
	}
}

func printSetupSummary(result *discovery.ScanResult, savedCount int, browserConfigured bool) {
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  Setup Complete!")
	fmt.Println("============================================================")
	fmt.Println()

	if savedCount > 0 {
		fmt.Printf("  Saved %d profile(s) to vault.\n", savedCount)
	}

	if browserConfigured {
		fmt.Println("  Browser profiles configured for OAuth logins.")
	}
	fmt.Println()

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

func printSetupSummaryV2(detections []ProviderAuthDetection, savedCount int, browserConfigured bool) {
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  Setup Complete!")
	fmt.Println("============================================================")
	fmt.Println()

	if savedCount > 0 {
		fmt.Printf("  Created %d profile(s).\n", savedCount)
	}

	if browserConfigured {
		fmt.Println("  Browser profiles configured for OAuth logins.")
	}
	fmt.Println()

	fmt.Println("  Quick commands:")
	fmt.Println("    caam ls          - List all profiles")
	fmt.Println("    caam status      - Show current status")
	fmt.Println("    caam run <tool>/<profile>  - Run with a profile")
	fmt.Println()
	fmt.Println("  Example:")
	fmt.Println("    caam run claude/default -- --help")
	fmt.Println()

	// Show providers without auth
	missingAuth := []string{}
	for _, d := range detections {
		if d.Error != nil || !d.Detection.Found {
			missingAuth = append(missingAuth, d.DisplayName)
		}
	}

	if len(missingAuth) > 0 {
		fmt.Println("  To add more accounts:")
		fmt.Println("    1. Log in to the AI tool (claude, codex, or gemini)")
		fmt.Println("    2. Run: caam auth import <tool>")
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

// ProviderAuthDetection holds detection results for a single provider.
type ProviderAuthDetection struct {
	ProviderID  string
	DisplayName string
	Detection   *provider.AuthDetection
	Error       error
}

// detectProviderAuth uses the new provider-based detection system.
func detectProviderAuth() []ProviderAuthDetection {
	var results []ProviderAuthDetection

	for _, prov := range registry.All() {
		detection, err := prov.DetectExistingAuth()
		results = append(results, ProviderAuthDetection{
			ProviderID:  prov.ID(),
			DisplayName: prov.DisplayName(),
			Detection:   detection,
			Error:       err,
		})
	}

	return results
}

// printProviderDetectionResults shows detection results with detailed info.
func printProviderDetectionResults(detections []ProviderAuthDetection) {
	fmt.Println()
	fmt.Println("Checking for existing auth credentials...")
	fmt.Println()

	foundCount := 0
	for _, d := range detections {
		if d.Error != nil {
			fmt.Printf("  [!] %s: error checking (%v)\n", d.DisplayName, d.Error)
			continue
		}

		if !d.Detection.Found {
			fmt.Printf("  [--] %s: no existing auth detected\n", d.DisplayName)
			continue
		}

		foundCount++
		if d.Detection.Primary != nil {
			loc := d.Detection.Primary
			status := "valid"
			if !loc.IsValid {
				status = loc.ValidationError
			}
			modTime := ""
			if !loc.LastModified.IsZero() {
				modTime = fmt.Sprintf(" (modified %s)", formatTimeAgo(loc.LastModified))
			}
			fmt.Printf("  [OK] %s: %s%s\n", d.DisplayName, shortenHomePath(loc.Path), modTime)
			fmt.Printf("       Status: %s\n", status)
		}

		if d.Detection.Warning != "" {
			fmt.Printf("       Warning: %s\n", d.Detection.Warning)
		}
	}
	fmt.Println()

	if foundCount == 0 {
		fmt.Println("  No existing auth credentials found.")
		fmt.Println()
		fmt.Println("  To get started:")
		fmt.Println("    1. Log in to your AI tool (claude, codex, or gemini)")
		fmt.Println("    2. Run: caam profile create <tool> <name>")
		fmt.Println()
	}
}

// importDetectedAuth imports detected auth credentials into profiles.
func importDetectedAuth(detections []ProviderAuthDetection, autoSave bool) int {
	fmt.Println("------------------------------------------------------------")
	fmt.Println("  IMPORT: Create Profiles from Detected Auth")
	fmt.Println("------------------------------------------------------------")
	fmt.Println()
	fmt.Println("  Import detected credentials to create profiles that you")
	fmt.Println("  can switch between without re-authenticating.")
	fmt.Println()

	ctx := context.Background()
	importedCount := 0

	for _, d := range detections {
		if d.Error != nil || !d.Detection.Found || d.Detection.Primary == nil {
			continue
		}

		prov, ok := registry.Get(d.ProviderID)
		if !ok {
			continue
		}

		loc := d.Detection.Primary

		// Check if profile already exists
		defaultName := "default"
		if profileStore.Exists(d.ProviderID, defaultName) {
			fmt.Printf("  [--] %s: profile 'default' already exists, skipping\n", d.DisplayName)
			continue
		}

		var profileName string
		if autoSave {
			profileName = defaultName
			fmt.Printf("  Importing %s auth as '%s'...\n", d.DisplayName, profileName)
		} else {
			if !promptYesNo(fmt.Sprintf("  Import %s auth as a profile?", d.DisplayName), true) {
				fmt.Println("  Skipped.")
				continue
			}
			profileName = promptWithDefault(fmt.Sprintf("  Profile name [%s]:", defaultName), defaultName)
			if profileName == "" {
				fmt.Println("  Skipped.")
				continue
			}
		}

		// Create profile
		prof, err := profileStore.Create(d.ProviderID, profileName, "oauth")
		if err != nil {
			fmt.Printf("  [!] Error creating profile: %v\n", err)
			continue
		}

		// Save profile
		if err := prof.Save(); err != nil {
			profileStore.Delete(d.ProviderID, profileName)
			fmt.Printf("  [!] Error saving profile: %v\n", err)
			continue
		}

		// Prepare profile directory
		if err := prov.PrepareProfile(ctx, prof); err != nil {
			profileStore.Delete(d.ProviderID, profileName)
			fmt.Printf("  [!] Error preparing profile: %v\n", err)
			continue
		}

		// Import auth
		_, err = prov.ImportAuth(ctx, loc.Path, prof)
		if err != nil {
			profileStore.Delete(d.ProviderID, profileName)
			fmt.Printf("  [!] Error importing auth: %v\n", err)
			continue
		}

		fmt.Printf("  [OK] Created %s/%s\n", d.ProviderID, profileName)
		importedCount++
	}

	fmt.Println()
	return importedCount
}

// shortenHomePath replaces home directory with ~.
func shortenHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
