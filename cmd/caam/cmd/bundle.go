package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"io"
	"path/filepath"

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

// bundleCmd is the parent command for bundle operations.
var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Export/import full vault bundles",
	Long: `Manage full vault bundles for backup and migration.

Unlike the 'export' command which exports individual profiles in tar.gz format
for quick transfers, the 'bundle' command creates comprehensive backup archives
that include:

  - All vault profiles (or filtered subset)
  - Configuration files
  - Project associations
  - Health metadata
  - Optional: activity database
  - Optional: sync pool configuration

Bundles use zip format with SHA-256 checksums and optional AES-256-GCM encryption.

Examples:
  caam bundle export                      # Export all profiles
  caam bundle export -e                   # Export with encryption
  caam bundle export --provider claude    # Export only Claude profiles
  caam bundle export --dry-run            # Preview what would be exported`,
}

// bundleExportCmd exports the vault as a bundle.
var bundleExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export vault as encrypted bundle",
	Long: `Export the vault and configuration as a portable bundle.

Creates a zip archive containing vault profiles and optional metadata.
The bundle includes a manifest with checksums for integrity verification.

Encryption:
  Use -e/--encrypt to protect the bundle with AES-256-GCM encryption.
  The password can be provided via --password or will be prompted interactively.
  Encrypted bundles have .enc.zip extension and require the password to import.

Filtering:
  --provider: Only include specific providers (claude, codex, gemini)
  --profiles: Only include profiles matching patterns (e.g., "work", "alice")

Optional content (included by default, can be excluded):
  --no-config: Exclude configuration file
  --no-projects: Exclude project associations
  --no-health: Exclude health metadata
  --include-database: Include activity database (excluded by default)
  --include-sync: Include sync pool configuration

Examples:
  caam bundle export                          # Export all to current directory
  caam bundle export -o /backup               # Export to specific directory
  caam bundle export -e                       # Export with encryption (prompted)
  caam bundle export -e -p "secret123"        # Export with password
  caam bundle export --provider claude,codex  # Only Claude and Codex
  caam bundle export --profiles "work,team"   # Only matching profiles
  caam bundle export --dry-run                # Preview without creating`,
	RunE: runBundleExport,
}

func init() {
	bundleCmd.AddCommand(bundleExportCmd)
	rootCmd.AddCommand(bundleCmd)

	// Output options
	bundleExportCmd.Flags().StringP("output", "o", "", "output directory (default: current directory)")
	bundleExportCmd.Flags().Bool("verbose-filename", false, "use descriptive filename with timestamp")
	bundleExportCmd.Flags().Bool("dry-run", false, "preview export without creating files")

	// Encryption options
	bundleExportCmd.Flags().BoolP("encrypt", "e", false, "encrypt the bundle with AES-256-GCM")
	bundleExportCmd.Flags().StringP("password", "p", "", "encryption password (prompted if not provided)")

	// Filtering options
	bundleExportCmd.Flags().StringSlice("provider", nil, "only include specific providers (claude,codex,gemini)")
	bundleExportCmd.Flags().StringSlice("profiles", nil, "only include profiles matching patterns")

	// Content inclusion options (defaults match bundle.DefaultExportOptions)
	bundleExportCmd.Flags().Bool("no-config", false, "exclude configuration file")
	bundleExportCmd.Flags().Bool("no-projects", false, "exclude project associations")
	bundleExportCmd.Flags().Bool("no-health", false, "exclude health metadata")
	bundleExportCmd.Flags().Bool("include-database", false, "include activity database (large)")
	bundleExportCmd.Flags().Bool("include-sync", true, "include sync pool configuration")
}

func runBundleExport(cmd *cobra.Command, args []string) error {
	// Build export options from flags
	opts := bundle.DefaultExportOptions()

	// Output options
	outputDir, _ := cmd.Flags().GetString("output")
	if outputDir == "" {
		var err error
		outputDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}
	}
	opts.OutputDir = outputDir

	opts.VerboseFilename, _ = cmd.Flags().GetBool("verbose-filename")
	opts.DryRun, _ = cmd.Flags().GetBool("dry-run")

	// Encryption options
	opts.Encrypt, _ = cmd.Flags().GetBool("encrypt")
	password, _ := cmd.Flags().GetString("password")

	if opts.Encrypt {
		if password == "" {
			// Prompt for password
			var err error
			password, err = promptPassword("Enter encryption password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			if password == "" {
				return fmt.Errorf("password cannot be empty when encryption is enabled")
			}

			// Confirm password
			confirm, err := promptPassword("Confirm password: ")
			if err != nil {
				return fmt.Errorf("read password confirmation: %w", err)
			}
			if password != confirm {
				return fmt.Errorf("passwords do not match")
			}
		}
		opts.Password = password
	}

	// Filtering options
	opts.ProviderFilter, _ = cmd.Flags().GetStringSlice("provider")
	opts.ProfileFilter, _ = cmd.Flags().GetStringSlice("profiles")

	// Content inclusion options
	noConfig, _ := cmd.Flags().GetBool("no-config")
	noProjects, _ := cmd.Flags().GetBool("no-projects")
	noHealth, _ := cmd.Flags().GetBool("no-health")
	includeDatabase, _ := cmd.Flags().GetBool("include-database")
	includeSync, _ := cmd.Flags().GetBool("include-sync")

	opts.IncludeConfig = !noConfig
	opts.IncludeProjects = !noProjects
	opts.IncludeHealth = !noHealth
	opts.IncludeDatabase = includeDatabase
	opts.IncludeSyncConfig = includeSync

	// Build exporter with paths
	// Data path is the parent of vault path
	vaultPath := authfile.DefaultVaultPath()
	dataPath := filepath.Dir(vaultPath)
	exporter := &bundle.VaultExporter{
		VaultPath:    vaultPath,
		DataPath:     dataPath,
		ConfigPath:   config.ConfigPath(),
		ProjectsPath: project.DefaultPath(),
		HealthPath:   health.DefaultHealthPath(),
		DatabasePath: caamdb.DefaultPath(),
		SyncPath:     syncstate.SyncDataDir(),
	}

	// Preview mode
	if opts.DryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Dry run - previewing export:")
		fmt.Fprintln(cmd.OutOrStdout())
	}

	// Perform export
	result, err := exporter.Export(opts)
	if err != nil {
		return fmt.Errorf("export failed: %w", err)
	}

	// Print results
	printExportResult(cmd, result, opts.DryRun)

	return nil
}

func printExportResult(cmd *cobra.Command, result *bundle.ExportResult, dryRun bool) {
	out := cmd.OutOrStdout()

	if dryRun {
		fmt.Fprintln(out, "Export Preview")
		fmt.Fprintln(out, "──────────────────────────────────────────")
	} else {
		fmt.Fprintln(out, "Export Complete")
		fmt.Fprintln(out, "──────────────────────────────────────────")
	}

	// Vault summary
	manifest := result.Manifest
	fmt.Fprintf(out, "Vault profiles: %d\n", manifest.Contents.Vault.TotalProfiles)
	for provider, profiles := range manifest.Contents.Vault.Profiles {
		fmt.Fprintf(out, "  %s: %s\n", provider, strings.Join(profiles, ", "))
	}

	// Optional content
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Included content:")
	printContentStatus(out, "  Config", manifest.Contents.Config)
	printContentStatus(out, "  Projects", manifest.Contents.Projects)
	printContentStatus(out, "  Health", manifest.Contents.Health)
	printContentStatus(out, "  Database", manifest.Contents.Database)
	printContentStatus(out, "  Sync Config", manifest.Contents.SyncConfig)

	// Output info
	fmt.Fprintln(out)
	if dryRun {
		fmt.Fprintf(out, "Would create: %s\n", result.OutputPath)
	} else {
		fmt.Fprintf(out, "Output: %s\n", result.OutputPath)
		fmt.Fprintf(out, "Size: %s\n", bundle.FormatSize(result.CompressedSize))
	}

	if result.Encrypted {
		fmt.Fprintln(out, "Encryption: AES-256-GCM with Argon2id")
		if !dryRun {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "⚠️  Store your password securely - it cannot be recovered!")
		}
	} else {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "⚠️  Bundle contains OAuth tokens - treat it like a password!")
		fmt.Fprintln(out, "   Consider using --encrypt for sensitive transfers.")
	}
}

func printContentStatus(out io.Writer, name string, content bundle.OptionalContent) {
	if content.Included {
		note := ""
		if content.Count > 0 {
			note = fmt.Sprintf(" (%d items)", content.Count)
		}
		if content.Note != "" {
			note = fmt.Sprintf(" (%s)", content.Note)
		}
		fmt.Fprintf(out, "%s: ✓%s\n", name, note)
	} else {
		reason := content.Reason
		if reason == "" {
			reason = "excluded"
		}
		fmt.Fprintf(out, "%s: ✗ (%s)\n", name, reason)
	}
}

// promptPassword reads a password from the terminal without echo.
func promptPassword(prompt string) (string, error) {
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
	return strings.TrimRight(password, "\r\n"), nil
}
