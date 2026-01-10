package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/daemon"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

var poolCmd = &cobra.Command{
	Use:   "pool <command>",
	Short: "Manage the authentication pool",
	Long: `View and manage the authentication pool used for proactive token monitoring.

The auth pool tracks token expiry across all profiles and can be used with the
daemon (--pool flag) for automatic background refresh.

Examples:
  caam pool status             # Show pool status
  caam pool status --json      # Show pool status as JSON
  caam pool refresh claude/work # Force refresh a profile
  caam pool refresh --all      # Force refresh all profiles`,
}

var poolStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show pool status",
	RunE:  runPoolStatus,
}

var poolRefreshCmd = &cobra.Command{
	Use:   "refresh [provider/profile]",
	Short: "Force refresh a profile or all profiles",
	RunE:  runPoolRefresh,
}

var poolListCmd = &cobra.Command{
	Use:   "list",
	Short: "List profiles in the pool with their states",
	RunE:  runPoolList,
}

func init() {
	rootCmd.AddCommand(poolCmd)
	poolCmd.AddCommand(poolStatusCmd)
	poolCmd.AddCommand(poolRefreshCmd)
	poolCmd.AddCommand(poolListCmd)

	// Status flags
	poolStatusCmd.Flags().Bool("json", false, "output as JSON")

	// Refresh flags
	poolRefreshCmd.Flags().Bool("all", false, "refresh all profiles")
	poolRefreshCmd.Flags().Duration("timeout", 30*time.Second, "timeout per refresh")

	// List flags
	poolListCmd.Flags().Bool("json", false, "output as JSON")
	poolListCmd.Flags().String("status", "", "filter by status (ready, refreshing, expired, cooldown, error)")
	poolListCmd.Flags().String("provider", "", "filter by provider")
}

func getPool() (*authpool.AuthPool, error) {
	vault := authfile.NewVault(authfile.DefaultVaultPath())
	pool := authpool.NewAuthPool(authpool.WithVault(vault))

	// Load persisted state (errors logged but not fatal - state file may not exist)
	opts := authpool.PersistOptions{}
	if err := pool.Load(opts); err != nil {
		// Load returns nil for file-not-exist, so any error here is a real problem
		fmt.Fprintf(os.Stderr, "Warning: failed to load pool state: %v\n", err)
	}

	// Load profiles from vault
	ctx := context.Background()
	if err := pool.LoadFromVault(ctx); err != nil {
		return nil, fmt.Errorf("load profiles from vault: %w", err)
	}

	return pool, nil
}

func runPoolStatus(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	pool, err := getPool()
	if err != nil {
		return err
	}

	summary := pool.Summary()

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}

	fmt.Printf("Pool Summary:\n")
	fmt.Printf("  Total profiles: %d\n", summary.TotalProfiles)
	fmt.Printf("  Ready: %d\n", summary.ReadyCount)
	fmt.Printf("  Cooldown: %d\n", summary.CooldownCount)
	fmt.Printf("  Error: %d\n", summary.ErrorCount)

	if len(summary.ByProvider) > 0 {
		fmt.Printf("\nBy Provider:\n")
		for provider, count := range summary.ByProvider {
			fmt.Printf("  %s: %d\n", provider, count)
		}
	}

	if len(summary.ByStatus) > 0 {
		fmt.Printf("\nBy Status:\n")
		for status, count := range summary.ByStatus {
			fmt.Printf("  %s: %d\n", status, count)
		}
	}

	return nil
}

func runPoolRefresh(cmd *cobra.Command, args []string) error {
	refreshAll, _ := cmd.Flags().GetBool("all")
	timeout, _ := cmd.Flags().GetDuration("timeout")

	if !refreshAll && len(args) == 0 {
		return fmt.Errorf("specify a profile (provider/name) or use --all")
	}

	vault := authfile.NewVault(authfile.DefaultVaultPath())
	healthStore := health.NewStorage(health.DefaultHealthPath())
	pool := authpool.NewAuthPool(authpool.WithVault(vault))

	// Load profiles
	ctx := context.Background()
	if err := pool.LoadFromVault(ctx); err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}

	refresher := daemon.NewPoolRefresher(vault, healthStore)
	monitor := authpool.NewMonitor(pool, refresher, authpool.DefaultMonitorConfig())

	if refreshAll {
		fmt.Println("Refreshing all profiles...")
		ctx, cancel := context.WithTimeout(context.Background(), timeout*time.Duration(pool.Count()))
		defer cancel()
		monitor.RefreshAll(ctx)
		fmt.Println("Refresh triggered for all profiles")
		return nil
	}

	// Parse provider/profile from args
	for _, arg := range args {
		provider, profile, err := parseProfileArg(arg)
		if err != nil {
			return err
		}

		fmt.Printf("Refreshing %s/%s...\n", provider, profile)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err = monitor.ForceRefresh(ctx, provider, profile)
		cancel()

		if err != nil {
			fmt.Printf("  Error: %v\n", err)
		} else {
			fmt.Printf("  Success\n")
		}
	}

	return nil
}

func runPoolList(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	statusFilter, _ := cmd.Flags().GetString("status")
	providerFilter, _ := cmd.Flags().GetString("provider")

	pool, err := getPool()
	if err != nil {
		return err
	}

	profiles := pool.GetAllProfiles(providerFilter)

	// Filter by status if specified
	if statusFilter != "" {
		var filtered []*authpool.PooledProfile
		for _, p := range profiles {
			if p.Status.String() == statusFilter {
				filtered = append(filtered, p)
			}
		}
		profiles = filtered
	}

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(profiles)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles in pool")
		return nil
	}

	fmt.Printf("%-10s %-20s %-12s %-20s\n", "PROVIDER", "PROFILE", "STATUS", "EXPIRY")
	fmt.Printf("%-10s %-20s %-12s %-20s\n", "--------", "-------", "------", "------")

	for _, p := range profiles {
		expiry := "-"
		if !p.TokenExpiry.IsZero() {
			ttl := time.Until(p.TokenExpiry)
			if ttl < 0 {
				expiry = "expired"
			} else {
				expiry = ttl.Round(time.Minute).String()
			}
		}
		fmt.Printf("%-10s %-20s %-12s %-20s\n",
			p.Provider, p.ProfileName, p.Status.String(), expiry)
	}

	return nil
}

// parseProfileArg parses "provider/profile" format.
func parseProfileArg(arg string) (provider, profile string, err error) {
	for i := 0; i < len(arg); i++ {
		if arg[i] == '/' {
			if i == 0 || i == len(arg)-1 {
				return "", "", fmt.Errorf("invalid format: %q (expected provider/profile)", arg)
			}
			return arg[:i], arg[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid format: %q (expected provider/profile)", arg)
}
