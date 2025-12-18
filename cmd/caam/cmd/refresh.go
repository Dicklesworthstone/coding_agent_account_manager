package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/spf13/cobra"
)

var refreshCmd = &cobra.Command{
	Use:   "refresh [tool] [profile]",
	Short: "Refresh OAuth tokens for profiles",
	Long: `Refresh OAuth tokens before they expire.

Examples:
  caam refresh claude work
  caam refresh codex main --force
  caam refresh --all
  caam refresh --all --dry-run
`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runRefresh,
}

func init() {
	refreshCmd.Flags().Bool("all", false, "refresh all profiles")
	refreshCmd.Flags().Bool("dry-run", false, "show what would be refreshed")
	refreshCmd.Flags().Bool("force", false, "force refresh even if not expiring")
	refreshCmd.Flags().Bool("quiet", false, "suppress output")
	rootCmd.AddCommand(refreshCmd)
}

func runRefresh(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	quiet, _ := cmd.Flags().GetBool("quiet")

	threshold := refresh.DefaultRefreshThreshold
	if spmCfg, err := config.LoadSPMConfig(); err == nil {
		if v := spmCfg.Health.RefreshThreshold.Duration(); v > 0 {
			threshold = v
		}
	}

	ctx := cmd.Context()

	if all {
		return refreshAll(ctx, threshold, dryRun, force, quiet)
	}

	if len(args) == 0 {
		// Default: show what would be refreshed (status view).
		return refreshAll(ctx, threshold, true, force, quiet)
	}

	tool := strings.ToLower(args[0])
	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	if len(args) == 1 {
		// Tool-filtered status view.
		return refreshAllForTool(ctx, tool, threshold, true, force, quiet)
	}

	profile := args[1]
	return refreshSingle(ctx, tool, profile, threshold, dryRun, force, quiet)
}

func refreshAll(ctx context.Context, threshold time.Duration, dryRun, force, quiet bool) error {
	toolsToCheck := []string{"codex", "claude", "gemini"}

	var hadFailure bool
	var refreshed, skipped, failed int

	if !quiet {
		if dryRun {
			fmt.Println("Would refresh:")
		} else {
			fmt.Println("Refreshing all profiles...")
		}
	}

	for _, tool := range toolsToCheck {
		r, s, f, err := refreshTool(ctx, tool, threshold, dryRun, force, quiet)
		refreshed += r
		skipped += s
		failed += f
		if err != nil {
			hadFailure = true
		}
	}

	if !quiet {
		if dryRun {
			fmt.Println()
			fmt.Printf("Would refresh %d, would skip %d\n", refreshed, skipped)
		} else {
			fmt.Println()
			fmt.Printf("%d refreshed, %d skipped, %d failed\n", refreshed, skipped, failed)
		}
	}

	if hadFailure {
		return fmt.Errorf("%d refresh operation(s) failed", failed)
	}
	return nil
}

func refreshAllForTool(ctx context.Context, tool string, threshold time.Duration, dryRun, force, quiet bool) error {
	if !quiet {
		if dryRun {
			fmt.Printf("Would refresh %s profiles:\n", tool)
		} else {
			fmt.Printf("Refreshing %s profiles...\n", tool)
		}
	}

	_, _, failed, err := refreshTool(ctx, tool, threshold, dryRun, force, quiet)
	if err != nil {
		return fmt.Errorf("%s refresh failed (%d error(s))", tool, failed)
	}
	return nil
}

func refreshTool(ctx context.Context, tool string, threshold time.Duration, dryRun, force, quiet bool) (refreshed, skipped, failed int, err error) {
	profiles, err := vault.List(tool)
	if err != nil {
		return 0, 0, 0, err
	}
	sort.Strings(profiles)

	for _, profile := range profiles {
		should, reason, infoErr := shouldRefreshProfile(tool, profile, threshold, force)
		if infoErr != nil {
			if !quiet {
				fmt.Printf("  %-18s failed (%v)\n", tool+"/"+profile, infoErr)
			}
			failed++
			continue
		}

		if !should {
			skipped++
			if !quiet {
				if dryRun {
					fmt.Printf("  %-18s skip (%s)\n", tool+"/"+profile, reason)
				} else {
					fmt.Printf("  %-18s skipped (%s)\n", tool+"/"+profile, reason)
				}
			}
			continue
		}

		if dryRun {
			refreshed++
			if !quiet {
				fmt.Printf("  %-18s would refresh (%s)\n", tool+"/"+profile, reason)
			}
			continue
		}

		if !quiet {
			fmt.Printf("  Refreshing %-18s... ", tool+"/"+profile)
		}

		if err := refresh.RefreshProfile(ctx, tool, profile, vault, healthStore); err != nil {
			failed++
			if !quiet {
				fmt.Printf("failed (%v)\n", err)
			}
			continue
		}

		refreshed++
		if !quiet {
			ttl := refreshedTTL(tool, profile)
			if ttl != "" {
				fmt.Printf("done (%s)\n", ttl)
			} else {
				fmt.Println("done")
			}
		}
	}

	if failed > 0 {
		return refreshed, skipped, failed, fmt.Errorf("%d profiles failed", failed)
	}
	return refreshed, skipped, failed, nil
}

func refreshSingle(ctx context.Context, tool, profile string, threshold time.Duration, dryRun, force, quiet bool) error {
	should, reason, err := shouldRefreshProfile(tool, profile, threshold, force)
	if err != nil {
		return err
	}

	key := tool + "/" + profile
	if !should {
		if !quiet {
			if dryRun {
				fmt.Printf("%s would be skipped (%s)\n", key, reason)
			} else {
				fmt.Printf("%s skipped (%s)\n", key, reason)
			}
		}
		return nil
	}

	if dryRun {
		if !quiet {
			fmt.Printf("%s would be refreshed (%s)\n", key, reason)
		}
		return nil
	}

	if !quiet {
		fmt.Printf("Refreshing %s... ", key)
	}

	if err := refresh.RefreshProfile(ctx, tool, profile, vault, healthStore); err != nil {
		if !quiet {
			fmt.Printf("failed (%v)\n", err)
		}
		return err
	}

	if !quiet {
		ttl := refreshedTTL(tool, profile)
		if ttl != "" {
			fmt.Printf("done (%s)\n", ttl)
		} else {
			fmt.Println("done")
		}
	}

	return nil
}

func shouldRefreshProfile(tool, profile string, threshold time.Duration, force bool) (bool, string, error) {
	if _, ok := tools[tool]; !ok {
		return false, "", fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	// Ensure profile exists.
	if err := ensureVaultProfileDir(tool, profile); err != nil {
		return false, "", err
	}

	info, err := loadExpiryInfo(tool, profile)
	if err != nil {
		if errors.Is(err, health.ErrNoAuthFile) {
			return false, "no auth files", nil
		}
		if errors.Is(err, health.ErrNoExpiry) {
			return false, "no refresh token", nil
		}
		return false, "", err
	}
	if info == nil || !info.HasRefreshToken {
		return false, "no refresh token", nil
	}

	if force {
		return true, "forced", nil
	}

	if info.ExpiresAt.IsZero() {
		return false, "unknown expiry", nil
	}

	ttl := time.Until(info.ExpiresAt)
	if ttl <= 0 {
		return true, "expired", nil
	}
	if ttl < threshold {
		return true, "expires " + health.FormatTimeRemaining(info.ExpiresAt), nil
	}

	return false, "expires " + health.FormatTimeRemaining(info.ExpiresAt), nil
}

func loadExpiryInfo(tool, profile string) (*health.ExpiryInfo, error) {
	vaultPath := vault.ProfilePath(tool, profile)

	switch tool {
	case "claude":
		return health.ParseClaudeExpiry(vaultPath)
	case "codex":
		return health.ParseCodexExpiry(filepath.Join(vaultPath, "auth.json"))
	case "gemini":
		return health.ParseGeminiExpiry(vaultPath)
	default:
		return nil, fmt.Errorf("refresh not supported for tool: %s", tool)
	}
}

func refreshedTTL(tool, profile string) string {
	// Gemini refresh updates health metadata, not files.
	if tool == "gemini" && healthStore != nil {
		h, err := healthStore.GetProfile(tool, profile)
		if err == nil && h != nil && !h.TokenExpiresAt.IsZero() {
			return health.FormatTimeRemaining(h.TokenExpiresAt)
		}
		return ""
	}

	info, err := loadExpiryInfo(tool, profile)
	if err == nil && info != nil && !info.ExpiresAt.IsZero() {
		return health.FormatTimeRemaining(info.ExpiresAt)
	}
	return ""
}

func ensureVaultProfileDir(tool, profile string) error {
	path := vault.ProfilePath(tool, profile)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("profile %s/%s not found in vault", tool, profile)
		}
		return fmt.Errorf("stat profile: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("profile path is not a directory: %s", path)
	}
	return nil
}
