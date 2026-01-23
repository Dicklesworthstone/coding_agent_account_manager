package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	syncstate "github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// bundleImportCmd imports vault from a bundle.
var bundleImportCmd = &cobra.Command{
	Use:   "import <bundle.zip>",
	Short: "Import vault from bundle",
	Long: `Restore saved auth profiles from a previously exported bundle.

Import Modes:
  smart (default): Uses freshness comparison for conflicts.
    - New profiles: added to vault
    - Existing profiles: keeps the token with later expiry time

  merge: Conservative approach.
    - New profiles: added to vault
    - Existing profiles: skipped (local preserved)

  replace: Aggressive approach.
    - Overwrites all matching profiles from bundle
    - Does NOT delete local profiles not in bundle

Encrypted Bundles:
  Bundles with .enc.zip extension require a password.
  Provide via --password or you will be prompted.

Examples:
  caam bundle import ~/backup.zip                    # Smart import
  caam bundle import ~/backup.zip --dry-run          # Preview changes
  caam bundle import ~/backup.enc.zip                # Encrypted (prompts)
  caam bundle import ~/backup.zip --mode merge       # Add new only
  caam bundle import ~/backup.zip --mode replace     # Overwrite all
  caam bundle import ~/backup.zip --provider claude  # Only Claude`,
	Args: cobra.ExactArgs(1),
	RunE: runBundleImport,
}

func init() {
	bundleCmd.AddCommand(bundleImportCmd)

	// Mode
	bundleImportCmd.Flags().String("mode", "smart", "Import mode: smart, merge, or replace")

	// Encryption
	bundleImportCmd.Flags().StringP("password", "p", "", "Password for encrypted bundles")

	// Preview/control
	bundleImportCmd.Flags().Bool("dry-run", false, "Preview import without making changes")
	bundleImportCmd.Flags().Bool("force", false, "Skip confirmation prompts")

	// Optional content exclusion
	bundleImportCmd.Flags().Bool("skip-config", false, "Don't import configuration")
	bundleImportCmd.Flags().Bool("skip-projects", false, "Don't import project associations")
	bundleImportCmd.Flags().Bool("skip-health", false, "Don't import health metadata")
	bundleImportCmd.Flags().Bool("skip-database", false, "Don't import activity database")
	bundleImportCmd.Flags().Bool("skip-sync", false, "Don't import sync configuration")

	// Filtering
	bundleImportCmd.Flags().StringSlice("provider", nil, "Only import specific providers (claude,codex,gemini)")
	bundleImportCmd.Flags().StringSlice("profiles", nil, "Only import profiles matching patterns")

	// Output
	bundleImportCmd.Flags().Bool("json", false, "Output result as JSON")
}

func runBundleImport(cmd *cobra.Command, args []string) error {
	bundlePath := args[0]

	// Build import options from flags
	opts := bundle.DefaultImportOptions()

	// Mode
	modeStr, _ := cmd.Flags().GetString("mode")
	switch strings.ToLower(modeStr) {
	case "smart":
		opts.Mode = bundle.ImportModeSmart
	case "merge":
		opts.Mode = bundle.ImportModeMerge
	case "replace":
		opts.Mode = bundle.ImportModeReplace
	default:
		return fmt.Errorf("invalid mode %q; use smart, merge, or replace", modeStr)
	}

	// Password
	password, _ := cmd.Flags().GetString("password")

	// Check if encrypted and prompt for password if needed
	encrypted, err := bundle.IsEncrypted(bundlePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("check encryption: %w", err)
	}

	if encrypted && password == "" {
		var err error
		password, err = promptPassword("Enter decryption password: ")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if password == "" {
			return fmt.Errorf("password required for encrypted bundle")
		}
	}
	opts.Password = password

	// Preview/control
	opts.DryRun, _ = cmd.Flags().GetBool("dry-run")
	opts.Force, _ = cmd.Flags().GetBool("force")

	// Optional content exclusion
	opts.SkipConfig, _ = cmd.Flags().GetBool("skip-config")
	opts.SkipProjects, _ = cmd.Flags().GetBool("skip-projects")
	opts.SkipHealth, _ = cmd.Flags().GetBool("skip-health")
	opts.SkipDatabase, _ = cmd.Flags().GetBool("skip-database")
	opts.SkipSync, _ = cmd.Flags().GetBool("skip-sync")

	// Filtering
	opts.ProviderFilter, _ = cmd.Flags().GetStringSlice("provider")
	opts.ProfileFilter, _ = cmd.Flags().GetStringSlice("profiles")

	// Set paths
	vaultPath := authfile.DefaultVaultPath()
	opts.VaultPath = vaultPath
	opts.ConfigPath = config.ConfigPath()
	opts.ProjectsPath = project.DefaultPath()
	opts.HealthPath = health.DefaultHealthPath()
	opts.DatabasePath = caamdb.DefaultPath()
	opts.SyncPath = syncstate.SyncDataDir()

	// Create importer
	importer := &bundle.VaultImporter{
		BundlePath: bundlePath,
	}

	// Perform import
	result, err := importer.Import(opts)
	if err != nil {
		// If we have partial results, show them before the error
		if result != nil && opts.DryRun {
			printImportPreview(cmd, result)
		}
		return fmt.Errorf("import failed: %w", err)
	}

	// Print results
	if opts.DryRun {
		printImportPreview(cmd, result)
	} else {
		printImportResult(cmd, result)
	}

	return nil
}

func printImportPreview(cmd *cobra.Command, result *bundle.ImportResult) {
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Import Preview")
	fmt.Fprintln(out, "──────────────────────────────────────────")

	// Bundle info
	if result.Manifest != nil {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Bundle Info:")
		fmt.Fprintf(out, "  Created: %s\n", result.Manifest.ExportTimestampHuman)
		fmt.Fprintf(out, "  Source: %s (%s/%s)\n",
			result.Manifest.Source.Hostname,
			result.Manifest.Source.Platform,
			result.Manifest.Source.Arch)
		fmt.Fprintf(out, "  CAAM Version: %s\n", result.Manifest.CAAMVersion)
		if result.Encrypted {
			fmt.Fprintln(out, "  Encrypted: yes")
		}
	}

	// Verification
	if result.VerificationResult != nil {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "Checksum Verification: %s\n", result.VerificationResult.Summary())
	}

	// Profile actions
	if len(result.ProfileActions) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Profiles:")

		// Group by provider
		byProvider := make(map[string][]bundle.ProfileAction)
		for _, action := range result.ProfileActions {
			byProvider[action.Provider] = append(byProvider[action.Provider], action)
		}

		for provider, actions := range byProvider {
			fmt.Fprintf(out, "  %s:\n", provider)
			for _, action := range actions {
				symbol := "?"
				switch action.Action {
				case "add":
					symbol = "+"
				case "update":
					symbol = "↑"
				case "skip":
					symbol = "-"
				}
				fmt.Fprintf(out, "    %s %s: %s\n", symbol, action.Profile, action.Reason)
			}
		}
	}

	// Optional files
	if len(result.OptionalActions) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Optional Files:")
		for _, action := range result.OptionalActions {
			symbol := "?"
			switch action.Action {
			case "import", "merge":
				symbol = "✓"
			case "skip":
				symbol = "✗"
			case "error":
				symbol = "!"
			}
			details := ""
			if action.Details != "" {
				details = fmt.Sprintf(" - %s", action.Details)
			}
			fmt.Fprintf(out, "  %s %s: %s%s\n", symbol, action.Name, action.Reason, details)
		}
	}

	// Summary
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Summary (would):")
	fmt.Fprintf(out, "  Add: %d new profiles\n", result.NewProfiles)
	fmt.Fprintf(out, "  Update: %d profiles (fresher in bundle)\n", result.UpdatedProfiles)
	fmt.Fprintf(out, "  Skip: %d profiles (local fresher or equal)\n", result.SkippedProfiles)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "This is a preview. Run without --dry-run to apply changes.")
}

func printImportResult(cmd *cobra.Command, result *bundle.ImportResult) {
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "Import Complete")
	fmt.Fprintln(out, "──────────────────────────────────────────")

	// Profile summary
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Profiles:")
	fmt.Fprintf(out, "  Added: %d\n", result.NewProfiles)
	fmt.Fprintf(out, "  Updated: %d\n", result.UpdatedProfiles)
	fmt.Fprintf(out, "  Skipped: %d\n", result.SkippedProfiles)

	// Show profile details
	if len(result.ProfileActions) > 0 {
		fmt.Fprintln(out)
		for _, action := range result.ProfileActions {
			if action.Action == "skip" {
				continue // Don't show skipped in summary
			}
			symbol := "+"
			if action.Action == "update" {
				symbol = "↑"
			}
			fmt.Fprintf(out, "  %s %s/%s: %s\n", symbol, action.Provider, action.Profile, action.Reason)
		}
	}

	// Optional files
	if len(result.OptionalActions) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Optional Files:")
		for _, action := range result.OptionalActions {
			symbol := "✓"
			if action.Action == "skip" {
				symbol = "✗"
			} else if action.Action == "error" {
				symbol = "!"
			}
			fmt.Fprintf(out, "  %s %s: %s\n", symbol, action.Name, action.Reason)
		}
	}

	// Errors
	if len(result.Errors) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Errors:")
		for _, err := range result.Errors {
			fmt.Fprintf(out, "  ⚠ %s\n", err)
		}
	}

	// Verification
	if result.VerificationResult != nil && !result.VerificationResult.Valid {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "⚠ Warning: %s\n", result.VerificationResult.Summary())
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Import complete. Use 'caam status' to verify.")
}

// promptPasswordImport reads a password from the terminal for import.
// This is separate to avoid confusion with the export password prompt.
func promptPasswordImport(prompt string) (string, error) {
	fmt.Print(prompt)

	// Check if stdin is a terminal
	if term.IsTerminal(int(os.Stdin.Fd())) {
		// Read without echo
		password, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println() // Add newline after password input
		if err != nil {
			return "", err
		}
		return string(password), nil
	}

	// Non-terminal input (piped)
	reader := bufio.NewReader(os.Stdin)
	password, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(password, "\n"), nil
}
