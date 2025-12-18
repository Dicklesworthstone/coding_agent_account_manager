package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/spf13/cobra"
)

// activateCmd restores auth files from the vault.
var activateCmd = &cobra.Command{
	Use:     "activate <tool> [profile-name]",
	Aliases: []string{"switch", "use"},
	Short:   "Activate a profile (instant switch)",
	Long: `Restores auth files from the vault, instantly switching to that account.

This is the magic command - sub-second account switching without any login flows!

Examples:
  caam activate codex work-account
  caam activate codex
  caam activate claude personal-max
  caam activate gemini team-ultra

After activating, just run the tool normally - it will use the new account.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runActivate,
}

func init() {
	activateCmd.Flags().Bool("backup-current", false, "backup current auth before switching")
}

func runActivate(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	var profileName string
	if len(args) == 2 {
		profileName = args[1]
	} else {
		var source string
		var err error
		profileName, source, err = resolveActivateProfile(tool)
		if err != nil {
			return err
		}
		if source != "" {
			fmt.Printf("Using %s: %s/%s\n", source, tool, profileName)
		}
	}

	getFileSet, ok := tools[tool]
	if !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	fileSet := getFileSet()

	// Safety: on first activate, preserve the user's pre-caam auth state.
	if did, err := vault.BackupOriginal(fileSet); err != nil {
		return fmt.Errorf("backup original auth: %w", err)
	} else if did {
		fmt.Printf("Backed up original %s auth to %s\n", tool, "_original")
	}

	// Step 1: Refresh if needed
	_ = refreshIfNeeded(cmd.Context(), tool, profileName)

	// Smart auto-backup before switch (based on safety config)
	spmCfg, _ := config.LoadSPMConfig()
	backupMode := spmCfg.Safety.AutoBackupBeforeSwitch
	if backupMode == "" {
		backupMode = "smart" // Default
	}

	// Check if --backup-current flag overrides config
	backupFirst, _ := cmd.Flags().GetBool("backup-current")
	if backupFirst {
		backupMode = "always"
	}

	if backupMode != "never" {
		shouldBackup := false
		currentProfile, _ := vault.ActiveProfile(fileSet)

		switch backupMode {
		case "always":
			// Always backup if there are auth files and we're switching to a different profile
			shouldBackup = currentProfile != profileName
		case "smart":
			// Backup only if current state doesn't match any vault profile (would be lost)
			shouldBackup = currentProfile == "" && authfile.HasAuthFiles(fileSet)
		}

		if shouldBackup {
			backupName, err := vault.BackupCurrent(fileSet)
			if err != nil {
				fmt.Printf("Warning: could not auto-backup current state: %v\n", err)
			} else if backupName != "" {
				fmt.Printf("Auto-backed up current state to %s\n", backupName)

				// Rotate old backups if limit is set
				if spmCfg.Safety.MaxAutoBackups > 0 {
					if err := vault.RotateAutoBackups(tool, spmCfg.Safety.MaxAutoBackups); err != nil {
						fmt.Printf("Warning: could not rotate old backups: %v\n", err)
					}
				}
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
}

func resolveActivateProfile(tool string) (profileName string, source string, err error) {
	// Prefer project association (if enabled).
	spmCfg, err := config.LoadSPMConfig()
	if err == nil && spmCfg.Project.Enabled && projectStore != nil {
		cwd, wdErr := os.Getwd()
		if wdErr != nil {
			return "", "", fmt.Errorf("get current directory: %w", wdErr)
		}
		resolved, resErr := projectStore.Resolve(cwd)
		if resErr == nil {
			if p := strings.TrimSpace(resolved.Profiles[tool]); p != "" {
				src := resolved.Sources[tool]
				if src == "" || src == cwd {
					return p, "project association", nil
				}
				if src == "<default>" {
					return p, "project default", nil
				}
				return p, "project association", nil
			}
		}
	}

	// Fall back to configured default profile (caam config.json).
	if cfg != nil {
		if p := strings.TrimSpace(cfg.GetDefault(tool)); p != "" {
			return p, "default profile", nil
		}
	}

	return "", "", fmt.Errorf("no profile specified for %s and no project association/default found\nHint: run 'caam activate %s <profile-name>', 'caam use %s <profile-name>', or 'caam project set %s <profile-name>'", tool, tool, tool, tool)
}

func refreshIfNeeded(ctx context.Context, provider, profile string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// Try to get health data. If missing, we might want to populate it?
	// But RefreshProfile uses vault path.
	// If we don't have health data, we don't know expiry, so we can't decide to refresh.
	// `getProfileHealth` in root.go parses files.
	// We should use that logic? `getProfileHealth` is in `root.go` (same package).
	h := getProfileHealth(provider, profile)

	if !refresh.ShouldRefresh(h, 0) {
		return nil
	}

	fmt.Printf("Refreshing token (%s)... ", health.FormatTimeRemaining(h.TokenExpiresAt))

	err := refresh.RefreshProfile(ctx, provider, profile, vault, healthStore)
	if err != nil {
		if errors.Is(err, refresh.ErrUnsupported) {
			fmt.Printf("skipped (%v)\n", err)
			return nil
		}
		fmt.Printf("failed (%v)\n", err)
		return nil // Continue activation even if refresh fails
	}

	fmt.Println("done")
	return nil
}
