package cmd

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pricing"
	"github.com/spf13/cobra"
)

// Subscription costs per month (approximate)
var subscriptionCosts = map[string]float64{
	"claude": 200.0, // Claude Max / Claude Pro - varies by plan
	"codex":  200.0, // ChatGPT Pro
	"gemini": 250.0, // Gemini Advanced
}

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "View and manage cost tracking for API usage",
	Long: `View estimated costs and manage cost rate configuration.

This command tracks costs based on wrap session durations and configurable rates.
Costs are estimated based on session time, not actual API usage.

Examples:
  caam cost                        # Show cost summary for all providers
  caam cost --provider claude      # Show costs for Claude only
  caam cost --since 7d             # Show costs from last 7 days
  caam cost --json                 # Output as JSON

Subcommands:
  caam cost sessions               # List recent wrap sessions
  caam cost rates                  # Show/set cost rate configuration`,
	Args: cobra.NoArgs,
	RunE: runCostSummary,
}

var costSessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List recent wrap sessions",
	Long: `Show recent wrap sessions with cost estimates.

Examples:
  caam cost sessions                # Show last 20 sessions
  caam cost sessions --limit 50     # Show last 50 sessions
  caam cost sessions --provider claude
  caam cost sessions --json`,
	Args: cobra.NoArgs,
	RunE: runCostSessions,
}

var costRatesCmd = &cobra.Command{
	Use:   "rates",
	Short: "View or set cost rates",
	Long: `View or configure cost rates per provider.

Rates are specified in cents. Costs are calculated as:
  estimated_cost = cents_per_session + (cents_per_minute * session_minutes)

Examples:
  caam cost rates                   # Show current rates
  caam cost rates --json            # Show rates as JSON
  caam cost rates --set claude --per-minute 5 --per-session 0
  caam cost rates --set codex --per-minute 3`,
	Args: cobra.NoArgs,
	RunE: runCostRates,
}

var costTokensCmd = &cobra.Command{
	Use:   "tokens [provider]",
	Short: "Analyze token costs from CLI logs",
	Long: `Analyze token costs by scanning CLI logs and comparing to API pricing.

This command scans local CLI logs to calculate actual token usage and estimates
what the equivalent API cost would be. This helps you understand the value
you're getting from your subscription.

Token-cost analysis is available for providers with log scanners (claude, codex, gemini).

Examples:
  caam cost tokens                    # Show costs for providers with log scanners (claude, codex, gemini)
  caam cost tokens claude             # Show Claude costs only
  caam cost tokens --last 168h        # Show costs for last 7 days (168 hours)
  caam cost tokens --last 24h         # Show costs for last 24 hours
  caam cost tokens --format json      # Output as JSON
  caam cost tokens --format csv       # Output as CSV

The cost comparison shows:
  - Total tokens used broken down by type (input/output/cache)
  - Per-model token usage and costs
  - Equivalent API pricing (what you'd pay without subscription)
  - Subscription value comparison`,
	Args: cobra.MaximumNArgs(1),
	RunE: runCostTokens,
}

func init() {
	rootCmd.AddCommand(costCmd)
	costCmd.AddCommand(costSessionsCmd)
	costCmd.AddCommand(costRatesCmd)
	costCmd.AddCommand(costTokensCmd)

	// Cost summary flags
	costCmd.Flags().String("provider", "", "filter by provider (claude, codex, gemini)")
	costCmd.Flags().String("since", "", "filter by time range (e.g., '24h', '7d', '30d')")
	costCmd.Flags().Bool("json", false, "output as JSON")

	// Sessions flags
	costSessionsCmd.Flags().IntP("limit", "n", 20, "maximum number of sessions to show")
	costSessionsCmd.Flags().String("provider", "", "filter by provider")
	costSessionsCmd.Flags().String("since", "", "filter sessions newer than duration")
	costSessionsCmd.Flags().Bool("json", false, "output as JSON")

	// Rates flags
	costRatesCmd.Flags().Bool("json", false, "output as JSON")
	costRatesCmd.Flags().String("set", "", "set rates for provider (requires --per-minute or --per-session)")
	costRatesCmd.Flags().Int("per-minute", -1, "cents per minute (use with --set)")
	costRatesCmd.Flags().Int("per-session", -1, "cents per session (use with --set)")

	// Tokens flags
	costTokensCmd.Flags().StringP("last", "l", "30d", "time period to analyze (e.g., 7d, 30d, 24h)")
	costTokensCmd.Flags().StringP("format", "f", "table", "output format: table, json, csv")
}

func runCostSummary(cmd *cobra.Command, args []string) error {
	provider, _ := cmd.Flags().GetString("provider")
	sinceStr, _ := cmd.Flags().GetString("since")
	jsonOutput, _ := cmd.Flags().GetBool("json")

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

	summaries, err := db.GetCostSummary(provider, sinceTime)
	if err != nil {
		return fmt.Errorf("get cost summary: %w", err)
	}

	if jsonOutput {
		return renderCostSummaryJSON(cmd.OutOrStdout(), summaries, sinceTime)
	}
	return renderCostSummary(cmd.OutOrStdout(), summaries, sinceTime)
}

func runCostSessions(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	provider, _ := cmd.Flags().GetString("provider")
	sinceStr, _ := cmd.Flags().GetString("since")
	jsonOutput, _ := cmd.Flags().GetBool("json")

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

	sessions, err := db.GetWrapSessions(provider, sinceTime, limit)
	if err != nil {
		return fmt.Errorf("get sessions: %w", err)
	}

	if len(sessions) == 0 {
		if jsonOutput {
			return renderSessionsJSON(cmd.OutOrStdout(), nil)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No wrap sessions found.")
		fmt.Fprintln(cmd.OutOrStdout(), "\nSessions are recorded when using: caam wrap <provider> <command>")
		return nil
	}

	if jsonOutput {
		return renderSessionsJSON(cmd.OutOrStdout(), sessions)
	}
	return renderSessions(cmd.OutOrStdout(), sessions)
}

func runCostRates(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	setProvider, _ := cmd.Flags().GetString("set")
	perMinute, _ := cmd.Flags().GetInt("per-minute")
	perSession, _ := cmd.Flags().GetInt("per-session")

	db, err := caamdb.Open()
	if err != nil {
		return err
	}
	defer db.Close()

	// Handle setting rates
	if setProvider != "" {
		// Get current rates to preserve values not being set
		currentRate, _ := db.GetCostRate(setProvider)
		newPerMinute := 0
		newPerSession := 0
		if currentRate != nil {
			newPerMinute = currentRate.CentsPerMinute
			newPerSession = currentRate.CentsPerSession
		}

		if perMinute >= 0 {
			newPerMinute = perMinute
		}
		if perSession >= 0 {
			newPerSession = perSession
		}

		if err := db.SetCostRate(setProvider, newPerMinute, newPerSession); err != nil {
			return fmt.Errorf("set rate: %w", err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Updated %s: %d¢/min, %d¢/session\n", setProvider, newPerMinute, newPerSession)
		return nil
	}

	// Show current rates
	rates, err := db.GetAllCostRates()
	if err != nil {
		return fmt.Errorf("get rates: %w", err)
	}

	if jsonOutput {
		return renderRatesJSON(cmd.OutOrStdout(), rates)
	}
	return renderRates(cmd.OutOrStdout(), rates)
}

// JSON output types
type costSummaryOutput struct {
	Summaries   []costSummaryItem `json:"summaries"`
	TotalCents  int               `json:"total_cents"`
	TotalDollar string            `json:"total_dollars"`
	Since       string            `json:"since,omitempty"`
}

type costSummaryItem struct {
	Provider        string  `json:"provider"`
	TotalSessions   int     `json:"total_sessions"`
	TotalMinutes    float64 `json:"total_minutes"`
	TotalCostCents  int     `json:"total_cost_cents"`
	TotalCostDollar string  `json:"total_cost_dollars"`
	RateLimitHits   int     `json:"rate_limit_hits"`
	AvgMinutes      float64 `json:"avg_session_minutes"`
}

type sessionsOutput struct {
	Sessions []sessionItem `json:"sessions"`
	Count    int           `json:"count"`
}

type sessionItem struct {
	ID             int     `json:"id"`
	Provider       string  `json:"provider"`
	Profile        string  `json:"profile"`
	StartedAt      string  `json:"started_at"`
	DurationSecs   int     `json:"duration_seconds"`
	DurationMins   float64 `json:"duration_minutes"`
	ExitCode       int     `json:"exit_code"`
	RateLimitHit   bool    `json:"rate_limit_hit"`
	EstimatedCents int     `json:"estimated_cost_cents"`
}

type ratesOutput struct {
	Rates []rateItem `json:"rates"`
}

type rateItem struct {
	Provider        string `json:"provider"`
	CentsPerMinute  int    `json:"cents_per_minute"`
	CentsPerSession int    `json:"cents_per_session"`
	UpdatedAt       string `json:"updated_at"`
}

func renderCostSummaryJSON(w io.Writer, summaries []caamdb.CostSummary, since time.Time) error {
	var totalCents int
	items := make([]costSummaryItem, len(summaries))
	for i, s := range summaries {
		totalCents += s.TotalCostCents
		items[i] = costSummaryItem{
			Provider:        s.Provider,
			TotalSessions:   s.TotalSessions,
			TotalMinutes:    float64(s.TotalDurationSecs) / 60.0,
			TotalCostCents:  s.TotalCostCents,
			TotalCostDollar: formatDollars(s.TotalCostCents),
			RateLimitHits:   s.RateLimitHits,
			AvgMinutes:      s.AverageDurationSec / 60.0,
		}
	}

	output := costSummaryOutput{
		Summaries:   items,
		TotalCents:  totalCents,
		TotalDollar: formatDollars(totalCents),
	}
	if !since.IsZero() {
		output.Since = since.UTC().Format(time.RFC3339)
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func renderCostSummary(w io.Writer, summaries []caamdb.CostSummary, since time.Time) error {
	if len(summaries) == 0 {
		fmt.Fprintln(w, "No cost data available.")
		fmt.Fprintln(w, "\nCosts are tracked when using: caam wrap <provider> <command>")
		return nil
	}

	if !since.IsZero() {
		fmt.Fprintf(w, "Cost Summary (since %s)\n", since.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintln(w, "Cost Summary (all time)")
	}
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PROVIDER\tSESSIONS\tTIME\tEST. COST\tRATE LIMITS")

	var totalCents int
	for _, s := range summaries {
		totalCents += s.TotalCostCents
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\n",
			s.Provider,
			s.TotalSessions,
			formatDurationShort(time.Duration(s.TotalDurationSecs)*time.Second),
			formatDollars(s.TotalCostCents),
			s.RateLimitHits,
		)
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Total Estimated Cost: %s\n", formatDollars(totalCents))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Note: Costs are estimates based on session duration, not actual API usage.")

	return nil
}

func renderSessionsJSON(w io.Writer, sessions []caamdb.WrapSession) error {
	items := make([]sessionItem, len(sessions))
	for i, s := range sessions {
		items[i] = sessionItem{
			ID:             s.ID,
			Provider:       s.Provider,
			Profile:        s.ProfileName,
			StartedAt:      s.StartedAt.UTC().Format(time.RFC3339),
			DurationSecs:   s.DurationSeconds,
			DurationMins:   float64(s.DurationSeconds) / 60.0,
			ExitCode:       s.ExitCode,
			RateLimitHit:   s.RateLimitHit,
			EstimatedCents: s.EstimatedCostCents,
		}
	}

	output := sessionsOutput{
		Sessions: items,
		Count:    len(items),
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func renderSessions(w io.Writer, sessions []caamdb.WrapSession) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tPROVIDER\tPROFILE\tDURATION\tCOST\tSTATUS")

	for _, s := range sessions {
		status := fmt.Sprintf("exit %d", s.ExitCode)
		if s.RateLimitHit {
			status = "rate-limited"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.StartedAt.Local().Format("01-02 15:04"),
			s.Provider,
			s.ProfileName,
			formatDurationShort(time.Duration(s.DurationSeconds)*time.Second),
			formatDollars(s.EstimatedCostCents),
			status,
		)
	}
	return tw.Flush()
}

func renderRatesJSON(w io.Writer, rates []caamdb.CostRate) error {
	items := make([]rateItem, len(rates))
	for i, r := range rates {
		items[i] = rateItem{
			Provider:        r.Provider,
			CentsPerMinute:  r.CentsPerMinute,
			CentsPerSession: r.CentsPerSession,
			UpdatedAt:       r.UpdatedAt.UTC().Format(time.RFC3339),
		}
	}

	output := ratesOutput{Rates: items}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func renderRates(w io.Writer, rates []caamdb.CostRate) error {
	if len(rates) == 0 {
		fmt.Fprintln(w, "No cost rates configured.")
		return nil
	}

	fmt.Fprintln(w, "Cost Rates:")
	fmt.Fprintln(w)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PROVIDER\tPER MINUTE\tPER SESSION\tLAST UPDATED")

	for _, r := range rates {
		_, _ = fmt.Fprintf(tw, "%s\t%d¢\t%d¢\t%s\n",
			r.Provider,
			r.CentsPerMinute,
			r.CentsPerSession,
			r.UpdatedAt.Local().Format("2006-01-02"),
		)
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	fmt.Fprintln(w, "To update rates: caam cost rates --set <provider> --per-minute <cents>")

	return nil
}

// formatDollars converts cents to a dollar string
func formatDollars(cents int) string {
	if cents == 0 {
		return "$0.00"
	}
	dollars := float64(cents) / 100.0
	return fmt.Sprintf("$%.2f", dollars)
}

// =============================================================================
// Token Cost Analysis (caam cost tokens)
// =============================================================================

// TokenCostAnalysis holds the token cost analysis results
type TokenCostAnalysis struct {
	Provider          string           `json:"provider"`
	Period            string           `json:"period"`
	Since             time.Time        `json:"since"`
	Until             time.Time        `json:"until"`
	TotalTokens       int64            `json:"total_tokens"`
	InputTokens       int64            `json:"input_tokens"`
	OutputTokens      int64            `json:"output_tokens"`
	CacheReadTokens   int64            `json:"cache_read_tokens"`
	CacheCreateTokens int64            `json:"cache_create_tokens"`
	ByModel           []TokenModelCost `json:"by_model"`
	TotalAPICost      float64          `json:"total_api_cost"`
	SubscriptionCost  float64          `json:"subscription_cost,omitempty"`
	Savings           float64          `json:"savings,omitempty"`
	SavingsPercent    float64          `json:"savings_percent,omitempty"`
}

// TokenModelCost holds per-model cost breakdown
type TokenModelCost struct {
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	Percent      float64 `json:"percent"`
	APICost      float64 `json:"api_cost"`
}

// tokenCostProviders are the providers with token-cost log scanners.
var tokenCostProviders = []string{"claude", "codex", "gemini"}

func isTokenCostProvider(p string) bool {
	for _, tp := range tokenCostProviders {
		if tp == p {
			return true
		}
	}
	return false
}

func runCostTokens(cmd *cobra.Command, args []string) error {
	lastStr, _ := cmd.Flags().GetString("last")
	format, _ := cmd.Flags().GetString("format")

	// Parse the duration using the same parser as other commands
	period, err := parseDuration(lastStr)
	if err != nil {
		return fmt.Errorf("invalid --last duration: %w", err)
	}

	// Token-cost analysis relies on per-provider log scanners, which only exist
	// for a subset of providers. Be explicit about scope: default to the
	// providers that have scanners, and reject an explicit unsupported provider
	// with a clear message instead of silently skipping it (issue #32).
	var providers []string
	if len(args) > 0 {
		p := strings.ToLower(args[0])
		if !isTokenCostProvider(p) {
			return fmt.Errorf("token cost analysis not supported for provider: %s (supported: %s)", p, strings.Join(tokenCostProviders, ", "))
		}
		providers = []string{p}
	} else {
		providers = append([]string(nil), tokenCostProviders...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := cmd.OutOrStdout()
	since := time.Now().Add(-period)

	// Initialize log scanners
	scanner := logs.NewMultiScanner()
	scanner.Register("claude", logs.NewClaudeScanner())
	scanner.Register("codex", logs.NewCodexScanner())
	scanner.Register("gemini", logs.NewGeminiScanner())

	var analyses []TokenCostAnalysis

	for _, provider := range providers {
		providerScanner := scanner.Scanner(provider)
		if providerScanner == nil {
			continue
		}

		result, err := providerScanner.Scan(ctx, "", since)
		if err != nil {
			if format != "json" {
				fmt.Fprintf(out, "%s: error scanning logs: %v\n", provider, err)
			}
			continue
		}

		entries := result.Entries
		if len(entries) == 0 {
			continue
		}

		analysis := analyzeTokenCosts(provider, entries, since, time.Now(), period)
		analyses = append(analyses, analysis)
	}

	if len(analyses) == 0 {
		if format == "json" {
			fmt.Fprintln(out, "[]")
		} else {
			fmt.Fprintln(out, "No log data found for the specified period.")
		}
		return nil
	}

	return renderTokenCostAnalysis(out, format, analyses)
}

func analyzeTokenCosts(provider string, entries []*logs.LogEntry, since, until time.Time, period time.Duration) TokenCostAnalysis {
	usage := logs.Aggregate(entries)

	analysis := TokenCostAnalysis{
		Provider:          provider,
		Period:            formatTokenPeriod(period),
		Since:             since,
		Until:             until,
		TotalTokens:       usage.TotalTokens,
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		CacheReadTokens:   usage.CacheReadTokens,
		CacheCreateTokens: usage.CacheCreateTokens,
	}

	// Calculate per-model costs
	var totalAPICost float64
	modelCosts := make([]TokenModelCost, 0, len(usage.ByModel))

	for modelName, mu := range usage.ByModel {
		mc := TokenModelCost{
			Model:        modelName,
			InputTokens:  mu.InputTokens,
			OutputTokens: mu.OutputTokens,
			TotalTokens:  mu.TotalTokens,
		}

		if usage.TotalTokens > 0 {
			mc.Percent = float64(mu.TotalTokens) / float64(usage.TotalTokens) * 100
		}

		// Calculate API cost for this model
		modelUsage := &logs.TokenUsage{
			InputTokens:  mu.InputTokens,
			OutputTokens: mu.OutputTokens,
			ByModel:      map[string]*logs.ModelTokenUsage{modelName: mu},
		}
		// Distribute cache tokens proportionally if single model
		if len(usage.ByModel) == 1 {
			modelUsage.CacheReadTokens = usage.CacheReadTokens
			modelUsage.CacheCreateTokens = usage.CacheCreateTokens
		}

		mc.APICost = pricing.CalculateCost(modelUsage, modelName, provider)
		totalAPICost += mc.APICost

		modelCosts = append(modelCosts, mc)
	}

	// Sort by usage descending
	sort.Slice(modelCosts, func(i, j int) bool {
		return modelCosts[i].TotalTokens > modelCosts[j].TotalTokens
	})

	analysis.ByModel = modelCosts
	analysis.TotalAPICost = totalAPICost

	// Calculate subscription comparison (prorated for period)
	if subCost, ok := subscriptionCosts[provider]; ok {
		// Prorate subscription cost for the analysis period
		daysInPeriod := period.Hours() / 24
		monthlyFraction := daysInPeriod / 30
		proratedSub := subCost * monthlyFraction

		analysis.SubscriptionCost = proratedSub
		analysis.Savings = totalAPICost - proratedSub
		if totalAPICost > 0 {
			analysis.SavingsPercent = (analysis.Savings / totalAPICost) * 100
		}
	}

	return analysis
}

func formatTokenPeriod(d time.Duration) string {
	hours := int(d.Hours())
	if hours < 24 {
		return fmt.Sprintf("%dh", hours)
	}
	days := hours / 24
	return fmt.Sprintf("%dd", days)
}

func renderTokenCostAnalysis(w io.Writer, format string, analyses []TokenCostAnalysis) error {
	format = strings.ToLower(strings.TrimSpace(format))

	switch format {
	case "json":
		data, err := json.MarshalIndent(analyses, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(data))
		return nil

	case "csv":
		return renderTokenCostCSV(w, analyses)

	case "table", "":
		return renderTokenCostTable(w, analyses)

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func renderTokenCostTable(w io.Writer, analyses []TokenCostAnalysis) error {
	for i, a := range analyses {
		if i > 0 {
			fmt.Fprintln(w)
		}

		// Header
		fmt.Fprintf(w, "TOKEN COST ANALYSIS - %s (Last %s)\n", strings.ToUpper(a.Provider), a.Period)
		fmt.Fprintln(w, strings.Repeat("=", 70))

		// Summary
		fmt.Fprintln(w, "SUMMARY")
		fmt.Fprintln(w, strings.Repeat("-", 70))
		fmt.Fprintf(w, "  Total tokens:     %s\n", formatTokenCount(a.TotalTokens))
		fmt.Fprintf(w, "    Input:          %s (%d%%)\n",
			formatTokenCount(a.InputTokens),
			tokenPercentage(a.InputTokens, a.TotalTokens))
		fmt.Fprintf(w, "    Output:         %s (%d%%)\n",
			formatTokenCount(a.OutputTokens),
			tokenPercentage(a.OutputTokens, a.TotalTokens))
		if a.CacheReadTokens > 0 {
			fmt.Fprintf(w, "    Cache read:     %s (%d%%)\n",
				formatTokenCount(a.CacheReadTokens),
				tokenPercentage(a.CacheReadTokens, a.TotalTokens))
		}
		if a.CacheCreateTokens > 0 {
			fmt.Fprintf(w, "    Cache create:   %s (%d%%)\n",
				formatTokenCount(a.CacheCreateTokens),
				tokenPercentage(a.CacheCreateTokens, a.TotalTokens))
		}

		// Model breakdown
		if len(a.ByModel) > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "MODEL BREAKDOWN")
			fmt.Fprintln(w, strings.Repeat("-", 70))

			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "  MODEL\tTOKENS\tUSAGE\tAPI COST")

			for _, mc := range a.ByModel {
				bar := tokenProgressBar(mc.Percent, 20)
				fmt.Fprintf(tw, "  %s\t%s\t%s %.0f%%\t$%.2f\n",
					truncateTokenModel(mc.Model, 20),
					formatTokenCount(mc.TotalTokens),
					bar,
					mc.Percent,
					mc.APICost)
			}
			tw.Flush()
		}

		// Cost comparison
		fmt.Fprintln(w)
		fmt.Fprintln(w, "COST COMPARISON")
		fmt.Fprintln(w, strings.Repeat("-", 70))
		fmt.Fprintf(w, "  If pay-per-token:   $%.2f\n", a.TotalAPICost)

		if a.SubscriptionCost > 0 {
			fmt.Fprintf(w, "  Subscription cost:  $%.2f (prorated for %s)\n", a.SubscriptionCost, a.Period)
			if a.Savings > 0 {
				fmt.Fprintf(w, "  Your savings:       $%.2f (%.0f%%)\n", a.Savings, a.SavingsPercent)
			} else if a.Savings < 0 {
				fmt.Fprintf(w, "  You paid extra:     $%.2f\n", -a.Savings)
			} else {
				fmt.Fprintf(w, "  Break-even\n")
			}
		}
	}

	return nil
}

func renderTokenCostCSV(w io.Writer, analyses []TokenCostAnalysis) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Write header
	if err := cw.Write([]string{
		"provider", "period", "model", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_create_tokens", "total_tokens",
		"usage_percent", "api_cost", "subscription_cost", "savings",
	}); err != nil {
		return err
	}

	for _, a := range analyses {
		for _, mc := range a.ByModel {
			record := []string{
				a.Provider,
				a.Period,
				mc.Model,
				fmt.Sprintf("%d", mc.InputTokens),
				fmt.Sprintf("%d", mc.OutputTokens),
				fmt.Sprintf("%d", a.CacheReadTokens),
				fmt.Sprintf("%d", a.CacheCreateTokens),
				fmt.Sprintf("%d", mc.TotalTokens),
				fmt.Sprintf("%.2f", mc.Percent),
				fmt.Sprintf("%.2f", mc.APICost),
				fmt.Sprintf("%.2f", a.SubscriptionCost),
				fmt.Sprintf("%.2f", a.Savings),
			}
			if err := cw.Write(record); err != nil {
				return err
			}
		}

		// If no models, write a summary row
		if len(a.ByModel) == 0 {
			record := []string{
				a.Provider,
				a.Period,
				"",
				fmt.Sprintf("%d", a.InputTokens),
				fmt.Sprintf("%d", a.OutputTokens),
				fmt.Sprintf("%d", a.CacheReadTokens),
				fmt.Sprintf("%d", a.CacheCreateTokens),
				fmt.Sprintf("%d", a.TotalTokens),
				"100.00",
				fmt.Sprintf("%.2f", a.TotalAPICost),
				fmt.Sprintf("%.2f", a.SubscriptionCost),
				fmt.Sprintf("%.2f", a.Savings),
			}
			if err := cw.Write(record); err != nil {
				return err
			}
		}
	}

	return nil
}

func formatTokenCount(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	} else if tokens >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

func tokenPercentage(part, total int64) int {
	if total == 0 {
		return 0
	}
	return int(float64(part) / float64(total) * 100)
}

func tokenProgressBar(percent float64, width int) string {
	filled := int(percent / 100 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
}

func truncateTokenModel(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
