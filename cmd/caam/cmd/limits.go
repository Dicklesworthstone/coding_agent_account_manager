package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

var limitsCmd = &cobra.Command{
	Use:   "limits [provider]",
	Short: "Fetch real-time rate limit usage from provider APIs",
	Long: `Fetch real-time rate limit and usage data from provider APIs.

This command queries the provider's API to get current rate limit utilization,
which is useful for deciding when to switch accounts. It also parses local logs
to estimate token burn rate and predict when limits will be hit.

Live limit fetching is available for providers with usage APIs (claude, codex).

Examples:
  caam limits                     # Show limits for all supported providers (claude, codex)
  caam limits claude              # Show Claude limits only
  caam limits codex               # Show Codex limits only
  caam limits --profile work      # Show limits for a specific profile
  caam limits --format json       # Output as JSON
  caam limits --best              # Show the best profile for rotation
  caam limits claude --model fable --best   # Best profile for Fable work specifically

Claude reports a separate weekly allowance per model on top of the 5-hour and
weekly windows. The SCOPED column shows the per-model allowance closest to its
cap; --model narrows scores and eligibility to the allowance for one model, so
an account out of Fable capacity is still offered for Sonnet work.`,
	RunE: runLimits,
}

func init() {
	rootCmd.AddCommand(limitsCmd)
	limitsCmd.Flags().StringP("profile", "p", "", "specific profile to check")
	limitsCmd.Flags().String("format", "table", "output format: table, json")
	limitsCmd.Flags().Bool("best", false, "show only the best profile for rotation")
	limitsCmd.Flags().Float64("threshold", 0.8, "utilization threshold for rotation (0-1)")
	limitsCmd.Flags().Bool("recommend", false, "show smart rotation recommendations")
	limitsCmd.Flags().Bool("forecast", false, "show usage forecasts and optimal switch times")
	limitsCmd.Flags().String("model", "", "model the work will run on (e.g. opus, fable); scores and eligibility then honor that model's own quota")
}

func runLimits(cmd *cobra.Command, args []string) error {
	profileArg, _ := cmd.Flags().GetString("profile")
	format, _ := cmd.Flags().GetString("format")
	showBest, _ := cmd.Flags().GetBool("best")
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	showRecommend, _ := cmd.Flags().GetBool("recommend")
	showForecast, _ := cmd.Flags().GetBool("forecast")
	model, _ := cmd.Flags().GetString("model")

	// Live limit fetching only has API support for a subset of providers (those
	// with credential readers + API fetchers). Be explicit about scope: default
	// to the supported set, and reject an explicit unsupported provider with a
	// clear message rather than silently returning empty data (issue #32).
	var providers []string
	if len(args) > 0 {
		p := strings.ToLower(args[0])
		if !isLimitsProvider(p) {
			return fmt.Errorf("limits not supported for provider: %s (supported: %s)", p, strings.Join(limitsProviders, ", "))
		}
		providers = []string{p}
	} else {
		providers = append([]string(nil), limitsProviders...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vaultDir := getVaultDir()
	out := cmd.OutOrStdout()

	// Initialize log scanners for burn rate calculation
	scanner := logs.NewMultiScanner()
	scanner.Register("claude", logs.NewClaudeScanner())
	scanner.Register("codex", logs.NewCodexScanner())
	scanner.Register("gemini", logs.NewGeminiScanner())

	fetcher := usage.NewMultiProfileFetcher(usage.WithLogScanner(scanner))

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

	// A model-scoped quota only binds the model it is scoped to, so ranking
	// and eligibility re-sort around the model the work will run on when the
	// caller names one (issue #97).
	if model != "" {
		sortResultsForModel(allResults, model)
	}

	if showBest {
		return renderBestProfile(out, format, allResults, threshold, model)
	}

	if showRecommend {
		return renderRecommendations(out, format, allResults, threshold, model)
	}

	if showForecast {
		return renderForecast(out, format, allResults)
	}

	return renderLimits(out, format, allResults, model)
}

// sortResultsForModel re-ranks profiles by their availability for one model.
// MultiProfileFetcher sorts by the model-agnostic score, which counts every
// scoped quota; with a model in hand only that model's own quota should.
func sortResultsForModel(results []usage.ProfileUsage, model string) {
	sort.SliceStable(results, func(i, j int) bool {
		scoreI, scoreJ := 0, 0
		if results[i].Usage != nil {
			scoreI = results[i].Usage.AvailabilityScoreForModel(model)
		}
		if results[j].Usage != nil {
			scoreJ = results[j].Usage.AvailabilityScoreForModel(model)
		}
		if scoreI == scoreJ {
			return results[i].ProfileName < results[j].ProfileName
		}
		return scoreI > scoreJ
	})
}

// limitsProviders are the providers with live limit/usage API support.
var limitsProviders = []string{"claude", "codex"}

func isLimitsProvider(p string) bool {
	for _, lp := range limitsProviders {
		if lp == p {
			return true
		}
	}
	return false
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

func renderLimits(w io.Writer, format string, results []usage.ProfileUsage, model string) error {
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
		fmt.Fprintln(w, "──────────────────────────────────────────────────────────────────────────────────────────")

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PROFILE\tSCORE\tPRIMARY\tSECONDARY\tSCOPED\tRESETS IN\tBURN/HR\tDEPLETES\tSTATUS")

		for _, r := range results {
			profileName := fmt.Sprintf("%s/%s", r.Provider, r.ProfileName)
			score := 0
			primary := "-"
			secondary := "-"
			scoped := "-"
			resetsIn := "-"
			status := "unknown"
			burnRate := "-"
			depletesIn := "-"

			if r.Usage != nil {
				score = r.Usage.AvailabilityScoreForModel(model)
				scoped = formatScopedLimit(r.Usage.ScopedLimit(model))

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

				if r.Usage.BurnRate != nil && r.Usage.BurnRate.TokensPerHour > 0 {
					burnRate = formatBurnRate(r.Usage.BurnRate.TokensPerHour)
				}

				if ttl := r.Usage.TimeToDepletion(); ttl > 0 {
					depletesIn = formatLimitsDuration(ttl)
					// Add warning indicator for imminent depletion
					if ttl < 30*time.Minute {
						depletesIn += " ⚠️"
					}
				}
			}

			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				profileName, score, primary, secondary, scoped, resetsIn, burnRate, depletesIn, status)
		}

		tw.Flush()
		return nil

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

// formatScopedLimit renders the model-scoped quota closest to its cap, e.g.
// "Fable 100%". A profile with no per-model quota reports "-".
func formatScopedLimit(w *usage.UsageWindow) string {
	if w == nil {
		return "-"
	}
	return fmt.Sprintf("%s %d%%", scopedLabel(w), w.UsedPercent)
}

// scopedLabel names the model a window is scoped to, for windows the provider
// reported without a display name.
func scopedLabel(w *usage.UsageWindow) string {
	if w == nil || w.Label == "" {
		return "model quota"
	}
	return w.Label
}

// formatBurnRate formats tokens per hour in a compact way.
func formatBurnRate(tokensPerHour float64) string {
	if tokensPerHour >= 1_000_000 {
		return fmt.Sprintf("%.1fM", tokensPerHour/1_000_000)
	} else if tokensPerHour >= 1_000 {
		return fmt.Sprintf("%.1fK", tokensPerHour/1_000)
	}
	return fmt.Sprintf("%.0f", tokensPerHour)
}

func renderBestProfile(w io.Writer, format string, results []usage.ProfileUsage, threshold float64, model string) error {
	// Filter to profiles that are available
	var available []usage.ProfileUsage
	for _, r := range results {
		if r.Usage != nil && r.Usage.Error == "" && !r.Usage.IsNearLimitForModel(threshold, model) {
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
			best.Provider, best.ProfileName, best.Usage.AvailabilityScoreForModel(model))

		if scoped := best.Usage.ScopedLimit(model); scoped != nil {
			fmt.Fprintf(w, "  Model-scoped window: %s used\n", formatScopedLimit(scoped))
		}

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

// Recommendation represents a smart rotation recommendation.
type Recommendation struct {
	Action      string `json:"action"`
	Profile     string `json:"profile"`
	Reason      string `json:"reason"`
	Urgency     string `json:"urgency"` // "now", "soon", "later", "none"
	SwitchIn    string `json:"switch_in,omitempty"`
	CurrentLoad int    `json:"current_load_percent"`
}

// Forecast represents a usage forecast for a profile.
type Forecast struct {
	Profile           string `json:"profile"`
	CurrentPrimary    int    `json:"current_primary_percent"`
	CurrentSecondary  int    `json:"current_secondary_percent"`
	PrimaryResetsIn   string `json:"primary_resets_in"`
	SecondaryResetsIn string `json:"secondary_resets_in"`
	SafeToUseIn       string `json:"safe_to_use_in,omitempty"`
	Recommendation    string `json:"recommendation"`
}

func renderRecommendations(w io.Writer, format string, results []usage.ProfileUsage, threshold float64, model string) error {
	format = strings.ToLower(strings.TrimSpace(format))

	recs := generateLimitsRecommendations(results, threshold, model)

	switch format {
	case "json":
		data, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(data))
		return nil

	case "table", "":
		if len(recs) == 0 {
			fmt.Fprintln(w, "No recommendations - all profiles are healthy.")
			return nil
		}

		fmt.Fprintln(w, "Smart Rotation Recommendations")
		fmt.Fprintln(w, "────────────────────────────────────────────────────────────────")

		for _, rec := range recs {
			urgencyIcon := "ℹ️ "
			switch rec.Urgency {
			case "now":
				urgencyIcon = "🔴"
			case "soon":
				urgencyIcon = "🟡"
			case "later":
				urgencyIcon = "🟢"
			}

			fmt.Fprintf(w, "%s %s: %s\n", urgencyIcon, rec.Action, rec.Profile)
			fmt.Fprintf(w, "   Reason: %s\n", rec.Reason)
			if rec.SwitchIn != "" {
				fmt.Fprintf(w, "   Switch in: %s\n", rec.SwitchIn)
			}
			fmt.Fprintln(w)
		}

		return nil

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func generateLimitsRecommendations(results []usage.ProfileUsage, threshold float64, model string) []Recommendation {
	var recs []Recommendation

	// Group by provider
	byProvider := make(map[string][]usage.ProfileUsage)
	for _, r := range results {
		byProvider[r.Provider] = append(byProvider[r.Provider], r)
	}

	thresholdPct := int(threshold * 100)

	for provider, profiles := range byProvider {
		// Find profiles that need attention
		var nearLimit, healthy []usage.ProfileUsage
		for _, p := range profiles {
			if p.Usage == nil || p.Usage.Error != "" {
				continue
			}
			if p.Usage.IsNearLimitForModel(threshold, model) {
				nearLimit = append(nearLimit, p)
			} else {
				healthy = append(healthy, p)
			}
		}

		// Generate recommendations
		for _, p := range nearLimit {
			primary := 0
			if p.Usage.PrimaryWindow != nil {
				primary = p.Usage.PrimaryWindow.UsedPercent
			}

			urgency := "soon"
			if primary >= 90 {
				urgency = "now"
			} else if primary >= thresholdPct {
				urgency = "soon"
			}

			switchIn := ""
			if ttl := p.Usage.TimeUntilReset(); ttl > 0 {
				switchIn = formatLimitsDuration(ttl)
			}

			reason := fmt.Sprintf("Primary usage at %d%% (threshold: %d%%)", primary, thresholdPct)
			if p.Usage.SecondaryWindow != nil && p.Usage.SecondaryWindow.UsedPercent >= thresholdPct {
				reason += fmt.Sprintf(", secondary at %d%%", p.Usage.SecondaryWindow.UsedPercent)
			}
			// Say so when a per-model quota is what makes the profile
			// unusable: at 0% general usage the primary figure alone reads as
			// "plenty left" (issue #97).
			if scoped := p.Usage.ScopedLimit(model); scoped != nil && scoped.UsedPercent >= thresholdPct {
				reason += fmt.Sprintf(", %s exhausted at %d%%", scopedLabel(scoped), scoped.UsedPercent)
				if primary < thresholdPct {
					urgency = "now"
				}
			}

			rec := Recommendation{
				Action:      "Switch from",
				Profile:     fmt.Sprintf("%s/%s", provider, p.ProfileName),
				Reason:      reason,
				Urgency:     urgency,
				SwitchIn:    switchIn,
				CurrentLoad: primary,
			}
			recs = append(recs, rec)
		}

		// Suggest best alternative
		if len(nearLimit) > 0 && len(healthy) > 0 {
			// Find the one with lowest usage
			best := healthy[0]
			for _, h := range healthy[1:] {
				if h.Usage.AvailabilityScoreForModel(model) > best.Usage.AvailabilityScoreForModel(model) {
					best = h
				}
			}

			bestPrimary := 0
			if best.Usage.PrimaryWindow != nil {
				bestPrimary = best.Usage.PrimaryWindow.UsedPercent
			}

			rec := Recommendation{
				Action:      "Switch to",
				Profile:     fmt.Sprintf("%s/%s", provider, best.ProfileName),
				Reason:      fmt.Sprintf("Has %d%% availability (primary at %d%%)", best.Usage.AvailabilityScoreForModel(model), bestPrimary),
				Urgency:     "none",
				CurrentLoad: bestPrimary,
			}
			recs = append(recs, rec)
		}
	}

	return recs
}

func renderForecast(w io.Writer, format string, results []usage.ProfileUsage) error {
	format = strings.ToLower(strings.TrimSpace(format))

	forecasts := generateForecasts(results)

	switch format {
	case "json":
		data, err := json.MarshalIndent(forecasts, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(data))
		return nil

	case "table", "":
		if len(forecasts) == 0 {
			fmt.Fprintln(w, "No usage data available for forecasting.")
			return nil
		}

		fmt.Fprintln(w, "Usage Forecasts")
		fmt.Fprintln(w, "────────────────────────────────────────────────────────────────")

		for _, f := range forecasts {
			fmt.Fprintf(w, "%s\n", f.Profile)
			fmt.Fprintf(w, "  Current: Primary %d%%, Secondary %d%%\n", f.CurrentPrimary, f.CurrentSecondary)
			fmt.Fprintf(w, "  Resets:  Primary in %s, Secondary in %s\n", f.PrimaryResetsIn, f.SecondaryResetsIn)
			if f.SafeToUseIn != "" {
				fmt.Fprintf(w, "  Safe to use in: %s\n", f.SafeToUseIn)
			}
			fmt.Fprintf(w, "  Recommendation: %s\n", f.Recommendation)
			fmt.Fprintln(w)
		}

		return nil

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func generateForecasts(results []usage.ProfileUsage) []Forecast {
	var forecasts []Forecast

	for _, r := range results {
		if r.Usage == nil || r.Usage.Error != "" {
			continue
		}

		f := Forecast{
			Profile: fmt.Sprintf("%s/%s", r.Provider, r.ProfileName),
		}

		if r.Usage.PrimaryWindow != nil {
			f.CurrentPrimary = r.Usage.PrimaryWindow.UsedPercent
			f.PrimaryResetsIn = formatLimitsDuration(time.Until(r.Usage.PrimaryWindow.ResetsAt))
		}

		if r.Usage.SecondaryWindow != nil {
			f.CurrentSecondary = r.Usage.SecondaryWindow.UsedPercent
			f.SecondaryResetsIn = formatLimitsDuration(time.Until(r.Usage.SecondaryWindow.ResetsAt))
		}

		// Determine when safe to use
		if f.CurrentPrimary >= 80 {
			// Need to wait for reset
			f.SafeToUseIn = f.PrimaryResetsIn
			f.Recommendation = fmt.Sprintf("Wait for primary window reset (%s)", f.PrimaryResetsIn)
		} else if f.CurrentPrimary >= 50 {
			f.Recommendation = "Use sparingly - approaching limit"
		} else if f.CurrentPrimary >= 30 {
			f.Recommendation = "Good availability - moderate usage"
		} else {
			f.Recommendation = "Excellent availability - safe for heavy usage"
		}

		// Adjust for secondary window
		if f.CurrentSecondary >= 80 {
			f.Recommendation += " (watch secondary limit)"
		}

		forecasts = append(forecasts, f)
	}

	return forecasts
}
