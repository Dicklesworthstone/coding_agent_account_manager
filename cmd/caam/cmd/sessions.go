// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// SessionInfo represents information about a running session.
type SessionInfo struct {
	Provider  string    `json:"provider"`
	Profile   string    `json:"profile"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"` // "active", "stale"
	Duration  string    `json:"duration,omitempty"`
}

// SessionsReport contains all session information.
type SessionsReport struct {
	Sessions    []SessionInfo `json:"sessions"`
	TotalActive int           `json:"total_active"`
	TotalStale  int           `json:"total_stale"`
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List running caam sessions",
	Long: `Shows all isolated profiles that are currently locked (in use).

Displays which profiles are actively running with caam exec, including:
  - Provider and profile name
  - Process ID (PID)
  - When the session started
  - Whether the session is active or stale (process no longer running)

Use --json for machine-readable output.
Use --provider to filter by a specific provider.

Examples:
  caam sessions              # Show all sessions
  caam sessions --provider codex  # Show only codex sessions
  caam sessions --json       # JSON output for scripting`,
	RunE: func(cmd *cobra.Command, args []string) error {
		providerFilter, _ := cmd.Flags().GetString("provider")
		jsonOutput, _ := cmd.Flags().GetBool("json")

		report, err := collectSessions(providerFilter)
		if err != nil {
			return err
		}

		if jsonOutput {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		printSessionsReport(report)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(sessionsCmd)
	sessionsCmd.Flags().String("provider", "", "filter by provider (codex, claude, gemini, cursor)")
	sessionsCmd.Flags().Bool("json", false, "output in JSON format")
}

// collectSessions gathers information about all locked profiles.
func collectSessions(providerFilter string) (*SessionsReport, error) {
	report := &SessionsReport{
		Sessions: []SessionInfo{},
	}

	allProfiles, err := profileStore.ListAll()
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}

	for provider, profiles := range allProfiles {
		// Apply provider filter if specified
		if providerFilter != "" && !strings.EqualFold(provider, providerFilter) {
			continue
		}

		for _, prof := range profiles {
			if !prof.IsLocked() {
				continue
			}

			info := SessionInfo{
				Provider: provider,
				Profile:  prof.Name,
			}

			lockInfo, err := prof.GetLockInfo()
			if err != nil {
				info.Status = "error"
				report.Sessions = append(report.Sessions, info)
				continue
			}

			if lockInfo == nil {
				continue // No lock info available
			}

			info.PID = lockInfo.PID
			info.StartedAt = lockInfo.LockedAt
			info.Duration = formatDuration(time.Since(lockInfo.LockedAt))

			// Check if process is still alive
			if profile.IsProcessAlive(lockInfo.PID) {
				info.Status = "active"
				report.TotalActive++
			} else {
				info.Status = "stale"
				report.TotalStale++
			}

			report.Sessions = append(report.Sessions, info)
		}
	}

	return report, nil
}

// formatDuration formats a duration in a human-friendly way.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// printSessionsReport prints the sessions report in a human-readable table format.
func printSessionsReport(report *SessionsReport) {
	if len(report.Sessions) == 0 {
		fmt.Println("No active sessions found.")
		fmt.Println("\nUse 'caam exec <tool> <profile>' to start a session.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "PROVIDER\tPROFILE\tPID\tSTARTED\tSTATUS")
	fmt.Fprintln(w, "--------\t-------\t---\t-------\t------")

	for _, session := range report.Sessions {
		status := session.Status
		if status == "stale" {
			status = "stale (process not running)"
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n",
			session.Provider,
			session.Profile,
			session.PID,
			session.Duration,
			status,
		)
	}

	fmt.Fprintln(w)

	// Summary
	if report.TotalStale > 0 {
		fmt.Fprintf(w, "\nSummary: %d active, %d stale\n", report.TotalActive, report.TotalStale)
		fmt.Fprintln(w, "Tip: Run 'caam doctor --fix' to clean stale locks")
	} else if report.TotalActive > 0 {
		fmt.Fprintf(w, "\nTotal active sessions: %d\n", report.TotalActive)
	}
}
