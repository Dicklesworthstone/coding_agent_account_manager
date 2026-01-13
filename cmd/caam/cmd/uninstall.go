package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Restore originals and remove caam data",
	Long: `Restore auth files from protected "_original" backups (if present) and then remove caam's data/config.

Operation order is critical:
  1) RESTORE original auth files from vault/<tool>/_original/
  2) THEN remove caam data (vault/profiles/health/config/db/etc)

Examples:
  caam uninstall
  caam uninstall --dry-run
  caam uninstall --keep-backups
  caam uninstall --force`,
	RunE: runUninstall,
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
	uninstallCmd.Flags().Bool("dry-run", false, "show what would be done without making changes")
	uninstallCmd.Flags().Bool("keep-backups", false, "restore originals but keep the vault backups directory")
	uninstallCmd.Flags().Bool("force", false, "skip confirmation prompt")
}

func runUninstall(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	keepBackups, _ := cmd.Flags().GetBool("keep-backups")
	force, _ := cmd.Flags().GetBool("force")

	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	toolsToRestore, toolsMissing, err := resolveOriginalRestorePlan()
	if err != nil {
		return err
	}

	xdgConfigDir := filepath.Dir(config.ConfigPath())
	xdgDataRoot := filepath.Dir(authfile.DefaultVaultPath())
	caamHomeRoot := filepath.Dir(config.SPMConfigPath())

	fmt.Println("caam uninstall will:")
	if len(toolsToRestore) > 0 {
		fmt.Printf("  ✓ Restore original auth from _original for: %s\n", strings.Join(toolsToRestore, ", "))
	} else {
		fmt.Println("  ✓ Restore original auth: (none)")
	}
	if len(toolsMissing) > 0 {
		fmt.Printf("  ⚠ No _original backup for: %s (skipping restore)\n", strings.Join(toolsMissing, ", "))
	}
	if keepBackups {
		fmt.Printf("  ✓ Remove caam data: %s (keeping vault backups)\n", xdgDataRoot)
	} else {
		fmt.Printf("  ✓ Remove caam data: %s\n", xdgDataRoot)
	}
	fmt.Printf("  ✓ Remove caam config: %s\n", xdgConfigDir)
	fmt.Printf("  ✓ Remove caam home files: %s\n", caamHomeRoot)

	if dryRun {
		fmt.Println("\nDry run: no changes will be made.")
		return nil
	}

	if !force {
		fmt.Printf("\nProceed? [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	// 1) Restore originals before any deletion.
	for _, tool := range toolsToRestore {
		getFileSet, ok := tools[tool]
		if !ok {
			continue
		}
		fileSet := getFileSet()

		fmt.Printf("Restoring %s auth from _original... ", tool)
		if err := vault.Restore(fileSet, "_original"); err != nil {
			return fmt.Errorf("restore %s/_original: %w", tool, err)
		}
		fmt.Println("done")
	}

	// 2) Remove caam data/config/db/etc (optionally keeping vault backups).
	if keepBackups {
		if err := removePath(profile.DefaultStorePath()); err != nil {
			return fmt.Errorf("remove profiles: %w", err)
		}
		if err := removePath(health.DefaultHealthPath()); err != nil {
			return fmt.Errorf("remove health file: %w", err)
		}
		// Keep vault backups directory.
	} else {
		if err := removePath(xdgDataRoot); err != nil {
			return fmt.Errorf("remove data directory: %w", err)
		}
	}

	if err := removePath(xdgConfigDir); err != nil {
		return fmt.Errorf("remove config directory: %w", err)
	}

	if err := removePath(config.SPMConfigPath()); err != nil {
		return fmt.Errorf("remove SPM config: %w", err)
	}
	if err := removePath(project.DefaultPath()); err != nil {
		return fmt.Errorf("remove projects file: %w", err)
	}
	if err := removePath(caamdb.DefaultPath()); err != nil {
		return fmt.Errorf("remove database: %w", err)
	}
	_ = removePath(caamdb.DefaultPath() + "-wal")
	_ = removePath(caamdb.DefaultPath() + "-shm")
	if err := removePath(signals.DefaultPIDFilePath()); err != nil {
		return fmt.Errorf("remove pid file: %w", err)
	}
	if err := removePath(signals.DefaultLogFilePath()); err != nil {
		return fmt.Errorf("remove log file: %w", err)
	}
	// Only attempt to remove the db parent directory if we're not keeping backups.
	// When keepBackups is true, this directory (CAAM_HOME/data) contains the vault.
	if !keepBackups {
		_ = removePath(filepath.Dir(caamdb.DefaultPath()))
	}

	// Best-effort cleanup of empty caam home root.
	_ = os.Remove(caamHomeRoot)

	fmt.Println("\ncaam has been uninstalled. Your original auth state has been restored (if an _original backup was available).")
	return nil
}

func resolveOriginalRestorePlan() (restore []string, missing []string, err error) {
	for _, tool := range []string{"codex", "claude", "gemini"} {
		hasOriginal, checkErr := vault.HasOriginalBackup(tool)
		if checkErr != nil {
			return nil, nil, fmt.Errorf("check %s/_original: %w", tool, checkErr)
		}
		if hasOriginal {
			restore = append(restore, tool)
		} else {
			missing = append(missing, tool)
		}
	}
	return restore, missing, nil
}

func removePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove root path: %s", clean)
	}
	return os.RemoveAll(clean)
}
