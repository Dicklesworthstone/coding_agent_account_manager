package cmd

import (
	"fmt"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean up old activity logs",
	Long: `Remove old activity logs based on retention settings.

By default, uses retention settings from config.yaml:
  - retention_days: 90 (detailed activity logs)
  - aggregate_retention_days: 365 (profile stats)

Examples:
  caam cleanup              # Clean up using config settings
  caam cleanup --dry-run    # Show what would be deleted
  caam cleanup --days 30    # Override retention to 30 days
`,
	RunE: runCleanup,
}

func init() {
	rootCmd.AddCommand(cleanupCmd)
	cleanupCmd.Flags().Bool("dry-run", false, "show what would be deleted without deleting")
	cleanupCmd.Flags().Int("days", 0, "override retention_days from config")
	cleanupCmd.Flags().Bool("quiet", false, "suppress output")
}

func runCleanup(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	overrideDays, _ := cmd.Flags().GetInt("days")
	quiet, _ := cmd.Flags().GetBool("quiet")

	// Load config for retention settings
	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfg := caamdb.CleanupConfig{
		RetentionDays:          spmCfg.Analytics.RetentionDays,
		AggregateRetentionDays: spmCfg.Analytics.AggregateRetentionDays,
	}

	// Override if specified
	if overrideDays > 0 {
		cfg.RetentionDays = overrideDays
	}

	// Open database
	db, err := caamdb.Open()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if dryRun {
		result, err := db.CleanupDryRun(cfg)
		if err != nil {
			return fmt.Errorf("dry run: %w", err)
		}

		if !quiet {
			fmt.Println("Dry run - would delete:")
			fmt.Printf("  Activity logs older than %d days: %d entries\n", cfg.RetentionDays, result.ActivityLogsDeleted)
			fmt.Printf("  Stale profile stats (>%d days inactive): %d entries\n", cfg.AggregateRetentionDays, result.StatsEntriesDeleted)
			if result.VacuumRan {
				fmt.Println("  Would run VACUUM to reclaim space")
			}
		}
		return nil
	}

	// Run actual cleanup
	result, err := db.Cleanup(cfg)
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}

	if !quiet {
		fmt.Println("Cleanup complete:")
		fmt.Printf("  Activity logs deleted: %d\n", result.ActivityLogsDeleted)
		fmt.Printf("  Profile stats deleted: %d\n", result.StatsEntriesDeleted)
		if result.VacuumRan {
			fmt.Println("  VACUUM ran to reclaim space")
		}
	}

	return nil
}

var dbStatsCmd = &cobra.Command{
	Use:   "db",
	Short: "Database management commands",
}

var dbStatsShowCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show database statistics",
	Long: `Display statistics about the caam activity database.

Shows:
  - Database file location and size
  - Number of activity log entries
  - Number of profile stats entries
  - Date range of entries
`,
	RunE: runDBStats,
}

func init() {
	rootCmd.AddCommand(dbStatsCmd)
	dbStatsCmd.AddCommand(dbStatsShowCmd)
}

func runDBStats(cmd *cobra.Command, args []string) error {
	db, err := caamdb.Open()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	stats, err := db.Stats()
	if err != nil {
		return fmt.Errorf("get stats: %w", err)
	}

	fmt.Printf("Database: %s\n", stats.Path)
	fmt.Printf("Size: %s\n", formatBytes(stats.SizeBytes))
	fmt.Printf("Activity logs: %d entries\n", stats.ActivityLogCount)
	fmt.Printf("Profile stats: %d entries\n", stats.ProfileStatsCount)

	if !stats.OldestEntry.IsZero() {
		fmt.Printf("Oldest entry: %s\n", stats.OldestEntry.Format("2006-01-02"))
	}
	if !stats.NewestEntry.IsZero() {
		fmt.Printf("Newest entry: %s\n", stats.NewestEntry.Format("2006-01-02"))
	}

	return nil
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
