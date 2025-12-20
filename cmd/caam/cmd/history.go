package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "List recent activity events",
	Long: `Show recent account activity from the event log.

Examples:
  caam history                     # Show last 20 events
  caam history --limit 50          # Show last 50 events
  caam history --provider claude   # Filter by provider
  caam history --type error        # Show only errors
  caam history --since 24h         # Events from last 24 hours
  caam history --json              # Output as JSON

Event types: activate, login, refresh, error, switch, deactivate`,
	Args: cobra.NoArgs,
	RunE: runHistory,
}

func init() {
	rootCmd.AddCommand(historyCmd)
	historyCmd.Flags().IntP("limit", "n", 20, "maximum number of events to show")
	historyCmd.Flags().String("provider", "", "filter by provider (claude, codex, gemini)")
	historyCmd.Flags().String("profile", "", "filter by profile name")
	historyCmd.Flags().String("type", "", "filter by event type (activate, error, refresh, etc.)")
	historyCmd.Flags().String("since", "", "filter events newer than duration (e.g., '24h', '7d')")
	historyCmd.Flags().Bool("json", false, "output as JSON")
}

func runHistory(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	providerFilter, _ := cmd.Flags().GetString("provider")
	profileFilter, _ := cmd.Flags().GetString("profile")
	typeFilter, _ := cmd.Flags().GetString("type")
	sinceStr, _ := cmd.Flags().GetString("since")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Parse since duration
	var sinceTime time.Time
	if sinceStr != "" {
		duration, err := parseDuration(sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since duration: %w", err)
		}
		sinceTime = time.Now().Add(-duration)
	}

	db, err := caamdb.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	// Fetch more events than limit to account for filtering
	fetchLimit := limit * 5
	if fetchLimit < 100 {
		fetchLimit = 100
	}
	events, err := db.ListRecentEvents(fetchLimit)
	if err != nil {
		return fmt.Errorf("get events: %w", err)
	}

	// Apply filters
	filtered := filterEvents(events, providerFilter, profileFilter, typeFilter, sinceTime)

	// Apply limit after filtering
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	if len(filtered) == 0 {
		if jsonOutput {
			return renderEventsJSON(cmd.OutOrStdout(), nil)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No events matching filters.")
		return nil
	}

	if jsonOutput {
		return renderEventsJSON(cmd.OutOrStdout(), filtered)
	}
	return renderEventList(cmd.OutOrStdout(), filtered)
}

// parseDuration parses duration strings like "24h", "7d", "30m"
func parseDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Handle day suffix specially
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSuffix(s, "d")
		var days int
		if _, err := fmt.Sscanf(numStr, "%d", &days); err != nil {
			return 0, fmt.Errorf("invalid days: %s", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	// Use standard Go duration parser for h, m, s
	return time.ParseDuration(s)
}

// filterEvents applies all filters to the event list
func filterEvents(events []caamdb.Event, provider, profile, eventType string, since time.Time) []caamdb.Event {
	result := make([]caamdb.Event, 0, len(events))
	for _, ev := range events {
		// Provider filter
		if provider != "" && !strings.EqualFold(ev.Provider, provider) {
			continue
		}
		// Profile filter
		if profile != "" && !strings.EqualFold(ev.ProfileName, profile) {
			continue
		}
		// Type filter
		if eventType != "" && !strings.EqualFold(ev.Type, eventType) {
			continue
		}
		// Since filter
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}
		result = append(result, ev)
	}
	return result
}

// historyOutput is the JSON output structure
type historyOutput struct {
	Events []historyEvent `json:"events"`
	Count  int            `json:"count"`
}

type historyEvent struct {
	Timestamp       string         `json:"timestamp"`
	Type            string         `json:"type"`
	Provider        string         `json:"provider"`
	Profile         string         `json:"profile"`
	Details         map[string]any `json:"details,omitempty"`
	DurationSeconds int64          `json:"duration_seconds,omitempty"`
}

func renderEventsJSON(w io.Writer, events []caamdb.Event) error {
	output := historyOutput{
		Events: make([]historyEvent, len(events)),
		Count:  len(events),
	}

	for i, ev := range events {
		output.Events[i] = historyEvent{
			Timestamp:       ev.Timestamp.UTC().Format(time.RFC3339),
			Type:            ev.Type,
			Provider:        ev.Provider,
			Profile:         ev.ProfileName,
			Details:         ev.Details,
			DurationSeconds: int64(ev.Duration.Seconds()),
		}
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func renderEventList(w io.Writer, events []caamdb.Event) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIMESTAMP\tTYPE\tPROVIDER\tPROFILE")
	for _, ev := range events {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			ev.Timestamp.Local().Format("2006-01-02 15:04:05"),
			ev.Type,
			ev.Provider,
			ev.ProfileName,
		)
	}
	return tw.Flush()
}
