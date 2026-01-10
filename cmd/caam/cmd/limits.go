package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

var limitsCmd = &cobra.Command{
	Use:   "limits [provider]",
	Short: "Fetch real-time rate limit usage from provider APIs",
	Long: `Fetch real-time rate limit and usage data from provider APIs.

This command queries the provider's API to get current rate limit utilization,
which is useful for deciding when to switch accounts.

Examples:
  caam limits                     # Show limits for all providers
  caam limits claude              # Show Claude limits only
  caam limits codex               # Show Codex limits only
  caam limits --profile work      # Show limits for a specific profile
  caam limits --format json       # Output as JSON
  caam limits --best              # Show the best profile for rotation`,
	RunE: runLimits,
}

func init() {
	rootCmd.AddCommand(limitsCmd)
	limitsCmd.Flags().StringP("profile", "p", "", "specific profile to check")
	limitsCmd.Flags().String("format", "table", "output format: table, json")
	limitsCmd.Flags().Bool("best", false, "show only the best profile for rotation")
	limitsCmd.Flags().Float64("threshold", 0.8, "utilization threshold for rotation (0-1)")
}

func runLimits(cmd *cobra.Command, args []string) error {
	profileArg, _ := cmd.Flags().GetString("profile")
	format, _ := cmd.Flags().GetString("format")
	showBest, _ := cmd.Flags().GetBool("best")
	threshold, _ := cmd.Flags().GetFloat64("threshold")

	var providers []string
	if len(args) > 0 {
		providers = []string{strings.ToLower(args[0])}
	} else {
		providers = []string{"claude", "codex"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vaultDir := getVaultDir()
	fetcher := usage.NewMultiProfileFetcher()
	out := cmd.OutOrStdout()

	allResults := make([]usage.ProfileUsage, 0)

	for _, provider := range providers {
		if profileArg != "" {
			// Fetch for specific profile
			token, err := getProfileToken(vaultDir, provider, profileArg)
			if err != nil {
				if format != "json" {
					fmt.Fprintf(out, "%s/%s: %v\n", provider, profileArg, err)
				}
				continue
			}

			profiles := map[string]string{profileArg: token}
			results := fetcher.FetchAllProfiles(ctx, provider, profiles)
			allResults = append(allResults, results...)
		} else {
			// Fetch for all profiles
			credentials, err := usage.LoadProfileCredentials(vaultDir, provider)
			if err != nil {
				if format != "json" {
					fmt.Fprintf(out, "%s: error loading credentials: %v\n", provider, err)
				}
				continue
			}
			if len(credentials) == 0 {
				continue
			}

			results := fetcher.FetchAllProfiles(ctx, provider, credentials)
			allResults = append(allResults, results...)
		}
	}

	if showBest {
		return renderBestProfile(out, format, allResults, threshold)
	}

	return renderLimits(out, format, allResults)
}

func getProfileToken(vaultDir, provider, profileName string) (string, error) {
	profileDir := filepath.Join(vaultDir, provider, profileName)

	switch provider {
	case "claude":
		// Try new location first
		credPath := filepath.Join(profileDir, ".credentials.json")
		token, _, err := usage.ReadClaudeCredentials(credPath)
		if err == nil {
			return token, nil
		}
		// Fall back to old location
		oldPath := filepath.Join(profileDir, ".claude.json")
		token, _, err = usage.ReadClaudeCredentials(oldPath)
		return token, err
	case "codex":
		authPath := filepath.Join(profileDir, "auth.json")
		token, _, err := usage.ReadCodexCredentials(authPath)
		return token, err
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func getVaultDir() string {
	return authfile.DefaultVaultPath()
}

func renderLimits(w io.Writer, format string, results []usage.ProfileUsage) error {
	format = strings.ToLower(strings.TrimSpace(format))

	switch format {
	case "json":
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(data))
		return nil

	case "table", "":
		if len(results) == 0 {
			fmt.Fprintln(w, "No profiles found.")
			return nil
		}

		fmt.Fprintln(w, "Rate Limit Usage")
		fmt.Fprintln(w, "────────────────────────────────────────────────────────────────")

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PROFILE\tSCORE\tPRIMARY\tSECONDARY\tRESETS IN\tSTATUS")

		for _, r := range results {
			profileName := fmt.Sprintf("%s/%s", r.Provider, r.ProfileName)
			score := 0
			primary := "-"
			secondary := "-"
			resetsIn := "-"
			status := "unknown"

			if r.Usage != nil {
				score = r.Usage.AvailabilityScore()

				if r.Usage.Error != "" {
					status = "error: " + truncate(r.Usage.Error, 20)
				} else {
					status = "ok"
				}

				if r.Usage.PrimaryWindow != nil {
					primary = fmt.Sprintf("%d%%", r.Usage.PrimaryWindow.UsedPercent)
				}

				if r.Usage.SecondaryWindow != nil {
					secondary = fmt.Sprintf("%d%%", r.Usage.SecondaryWindow.UsedPercent)
				}

				if ttl := r.Usage.TimeUntilReset(); ttl > 0 {
					resetsIn = formatLimitsDuration(ttl)
				}
			}

			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
				profileName, score, primary, secondary, resetsIn, status)
		}

		tw.Flush()
		return nil

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func renderBestProfile(w io.Writer, format string, results []usage.ProfileUsage, threshold float64) error {
	// Filter to profiles that are available
	var available []usage.ProfileUsage
	for _, r := range results {
		if r.Usage != nil && r.Usage.Error == "" && !r.Usage.IsNearLimit(threshold) {
			available = append(available, r)
		}
	}

	if len(available) == 0 {
		// Fall back to best score even if above threshold
		if len(results) > 0 && results[0].Usage != nil && results[0].Usage.Error == "" {
			available = results[:1]
		}
	}

	format = strings.ToLower(strings.TrimSpace(format))

	switch format {
	case "json":
		if len(available) == 0 {
			fmt.Fprintln(w, "null")
			return nil
		}
		data, err := json.MarshalIndent(available[0], "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(data))
		return nil

	case "table", "":
		if len(available) == 0 {
			fmt.Fprintln(w, "No available profiles found.")
			return nil
		}

		best := available[0]
		fmt.Fprintf(w, "Best profile: %s/%s (score: %d)\n",
			best.Provider, best.ProfileName, best.Usage.AvailabilityScore())

		if best.Usage.PrimaryWindow != nil {
			fmt.Fprintf(w, "  Primary window: %d%% used, resets in %s\n",
				best.Usage.PrimaryWindow.UsedPercent,
				formatLimitsDuration(time.Until(best.Usage.PrimaryWindow.ResetsAt)))
		}

		if best.Usage.SecondaryWindow != nil {
			fmt.Fprintf(w, "  Secondary window: %d%% used, resets in %s\n",
				best.Usage.SecondaryWindow.UsedPercent,
				formatLimitsDuration(time.Until(best.Usage.SecondaryWindow.ResetsAt)))
		}

		return nil

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func formatLimitsDuration(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours >= 24 {
		days := hours / 24
		hours = hours % 24
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
