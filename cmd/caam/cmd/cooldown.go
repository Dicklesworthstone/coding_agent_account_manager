package cmd

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

var cooldownCmd = &cobra.Command{
	Use:   "cooldown",
	Short: "Manage profile cooldowns after limit hits",
	Long: `Track when accounts hit rate limits and enforce cooldown periods before reuse.

Examples:
  caam cooldown set claude/work --minutes 60
  caam cooldown clear claude/work
  caam cooldown clear --all
  caam cooldown list`,
}

func init() {
	rootCmd.AddCommand(cooldownCmd)
	cooldownCmd.AddCommand(cooldownSetCmd)
	cooldownCmd.AddCommand(cooldownClearCmd)
	cooldownCmd.AddCommand(cooldownListCmd)
}

var cooldownSetCmd = &cobra.Command{
	Use:   "set <provider/profile|provider> [--minutes N] [--notes TEXT]",
	Short: "Set a cooldown for a profile",
	Args:  cobra.ExactArgs(1),
	RunE:  runCooldownSet,
}

func init() {
	cooldownSetCmd.Flags().Int("minutes", 0, "cooldown duration in minutes (default: stealth.cooldown.default_minutes)")
	cooldownSetCmd.Flags().String("notes", "", "optional notes to store with the cooldown event")
}

func runCooldownSet(cmd *cobra.Command, args []string) error {
	target := strings.TrimSpace(args[0])
	provider, profile, err := resolveProviderProfile(target)
	if err != nil {
		return err
	}

	minutes, _ := cmd.Flags().GetInt("minutes")
	if minutes <= 0 {
		spmCfg, err := config.LoadSPMConfig()
		if err != nil {
			spmCfg = config.DefaultSPMConfig()
		}
		minutes = spmCfg.Stealth.Cooldown.DefaultMinutes
	}
	if minutes <= 0 {
		minutes = 60
	}

	notes, _ := cmd.Flags().GetString("notes")

	db, err := caamdb.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	ev, err := db.SetCooldown(provider, profile, time.Now().UTC(), time.Duration(minutes)*time.Minute, notes)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Recorded cooldown for %s/%s until %s (%s remaining)\n",
		ev.Provider,
		ev.ProfileName,
		ev.CooldownUntil.Local().Format("2006-01-02 15:04"),
		formatDurationShort(time.Until(ev.CooldownUntil)),
	)
	return nil
}

var cooldownClearCmd = &cobra.Command{
	Use:   "clear [provider/profile|provider] [--all]",
	Short: "Clear a cooldown (or all cooldowns)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runCooldownClear,
}

func init() {
	cooldownClearCmd.Flags().Bool("all", false, "clear all cooldowns")
}

func runCooldownClear(cmd *cobra.Command, args []string) error {
	clearAll, _ := cmd.Flags().GetBool("all")

	db, err := caamdb.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	out := cmd.OutOrStdout()

	if clearAll {
		if len(args) != 0 {
			return fmt.Errorf("--all cannot be used with a specific profile")
		}
		deleted, err := db.ClearAllCooldowns()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Cleared %d cooldown(s)\n", deleted)
		return nil
	}

	if len(args) != 1 {
		return fmt.Errorf("provide a provider/profile (or use --all)")
	}

	provider, profile, err := resolveProviderProfile(strings.TrimSpace(args[0]))
	if err != nil {
		return err
	}

	deleted, err := db.ClearCooldown(provider, profile)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Cleared %d cooldown(s) for %s/%s\n", deleted, provider, profile)
	return nil
}

var cooldownListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active cooldowns",
	Args:  cobra.NoArgs,
	RunE:  runCooldownList,
}

func runCooldownList(cmd *cobra.Command, args []string) error {
	db, err := caamdb.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	now := time.Now().UTC()
	events, err := db.ListActiveCooldowns(now)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active cooldowns.")
		return nil
	}

	return renderCooldownList(cmd.OutOrStdout(), now, events)
}

func renderCooldownList(w io.Writer, now time.Time, events []caamdb.CooldownEvent) error {
	_, _ = fmt.Fprintln(w, "Active Cooldowns")
	_, _ = fmt.Fprintln(w, "───────────────────────────────────────")

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PROFILE\tHIT\tUNTIL\tREMAINING\tNOTES")
	for _, ev := range events {
		remaining := formatDurationShort(ev.CooldownUntil.Sub(now))
		_, _ = fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%s\n",
			ev.Provider,
			ev.ProfileName,
			ev.HitAt.Local().Format("2006-01-02 15:04"),
			ev.CooldownUntil.Local().Format("2006-01-02 15:04"),
			remaining,
			ev.Notes,
		)
	}
	return tw.Flush()
}

func resolveProviderProfile(input string) (provider string, profile string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("profile is required")
	}

	// Prefer explicit provider/profile.
	if strings.Contains(input, "/") {
		parts := strings.SplitN(input, "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("profile must be in provider/name format")
		}
		provider = strings.TrimSpace(parts[0])
		profile = strings.TrimSpace(parts[1])
		if provider == "" || profile == "" {
			return "", "", fmt.Errorf("profile must be in provider/name format")
		}
		return provider, profile, nil
	}

	// Convenience: provider-only means "current active profile for this provider".
	tool := strings.ToLower(input)
	getFileSet, ok := tools[tool]
	if !ok {
		return "", "", fmt.Errorf("unknown provider: %s (expected provider/name or supported provider)", input)
	}

	fileSet := getFileSet()
	active, err := vault.ActiveProfile(fileSet)
	if err != nil {
		return "", "", fmt.Errorf("detect active profile for %s: %w", tool, err)
	}
	if strings.TrimSpace(active) == "" {
		return "", "", fmt.Errorf("no active profile detected for %s (provide provider/name)", tool)
	}
	return tool, active, nil
}

func formatDurationShort(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}

	d = d.Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}

	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	switch {
	case hours <= 0:
		return fmt.Sprintf("%dm", mins)
	case mins == 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
}

func confirmProceed(r io.Reader, w io.Writer) (bool, error) {
	_, _ = fmt.Fprint(w, "Proceed anyway? [y/N]: ")
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
