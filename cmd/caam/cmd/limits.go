package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
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
  caam limits claude --cached     # Offline: read the snapshot Claude Code cached on disk
  caam limits claude --profile work --source isolated   # Read a specific credential store

Claude reports a separate weekly allowance per model on top of the 5-hour and
weekly windows. The SCOPED column shows the per-model allowance closest to its
cap; --model narrows scores and eligibility to the allowance for one model, so
an account out of Fable capacity is still offered for Sonnet work.

Credential namespaces (--source)
--------------------------------
One profile name can exist in three unrelated stores: the "vault" (the
backup/activate store), an "isolated" profile (its own HOME and XDG config
dir, which is where an in-app /login under "caam exec" writes), and a
"shallow" HOME. --profile reads the vault by default; output now always names
the namespace and path it read, lists the other namespaces holding the same
name, and refuses to report a verdict when an unselected namespace holds a
strictly healthier credential. Pass --source vault|isolated|shallow to choose
explicitly. Credentials are never copied between namespaces.

Offline mode (--cached)
-----------------------
--cached answers the same question without the network and without presenting
any token: Claude Code caches the usage figures it last received in each
account's own .claude.json, and --cached reads those files. Only Claude keeps
such a cache.

The snapshot only moves when that account itself runs a session, so a profile
you are not currently using can be hours or days stale — or have no cache at
all. Every row therefore reports its snapshot's age in an AS OF column, and a
profile with nothing cached is reported as "no cached data" and excluded from
--best rather than being shown as 0% used.`,
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
	limitsCmd.Flags().String("source", "", "credential namespace to read: vault (default), isolated, or shallow")
	limitsCmd.Flags().Bool("cached", false, "read the usage snapshot Claude Code cached on disk instead of querying the API (offline, presents no token; claude only)")
}

func runLimits(cmd *cobra.Command, args []string) error {
	profileArg, _ := cmd.Flags().GetString("profile")
	format, _ := cmd.Flags().GetString("format")
	showBest, _ := cmd.Flags().GetBool("best")
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	showRecommend, _ := cmd.Flags().GetBool("recommend")
	showForecast, _ := cmd.Flags().GetBool("forecast")
	model, _ := cmd.Flags().GetString("model")
	source, _ := cmd.Flags().GetString("source")
	cached, _ := cmd.Flags().GetBool("cached")

	source = strings.ToLower(strings.TrimSpace(source))
	if source != "" && !ValidCredNamespace(source) {
		return fmt.Errorf("unknown --source %q (want one of: %s)", source, strings.Join(credNamespaces, ", "))
	}

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

	// Only Claude keeps a usage snapshot on disk, so --cached has nothing to
	// read for anyone else. Say so instead of silently returning empty rows.
	if cached {
		for _, p := range providers {
			if p != "claude" {
				return fmt.Errorf("--cached is claude-only: %s keeps no usage snapshot on disk (drop --cached to query its API)", p)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	vaultDir := getVaultDir()
	out := cmd.OutOrStdout()
	lookup := buildCredentialLookup(vaultDir)
	now := time.Now()
	var sourceNotes []string

	// Initialize log scanners for burn rate calculation
	scanner := logs.NewMultiScanner()
	scanner.Register("claude", logs.NewClaudeScanner())
	scanner.Register("codex", logs.NewCodexScanner())
	scanner.Register("gemini", logs.NewGeminiScanner())

	fetcher := usage.NewMultiProfileFetcher(usage.WithLogScanner(scanner))

	allResults := make([]usage.ProfileUsage, 0)

	for _, provider := range providers {
		if profileArg != "" {
			// A single named profile: resolve WHICH credential namespace the
			// name means before reading anything (issue #100).
			res, err := resolveProfileCredential(lookup, provider, profileArg, source)
			if err != nil {
				if format != "json" {
					fmt.Fprintf(out, "%s/%s: %v\n", provider, profileArg, err)
				}
				continue
			}
			// A strictly healthier copy elsewhere means the default namespace
			// would produce a verdict a controller must not act on. Refuse.
			if len(res.Healthier) > 0 {
				return res.AmbiguityError(provider, profileArg)
			}
			sourceNotes = append(sourceNotes, res.describe(provider, profileArg))

			var results []usage.ProfileUsage
			if cached {
				results = []usage.ProfileUsage{cachedProfileUsage(provider, profileArg, res.Selected.ClaudeJSON, now)}
			} else {
				results = fetcher.FetchAllProfiles(ctx, provider, map[string]string{profileArg: res.Selected.Token})
			}
			report := res.report()
			for i := range results {
				results[i].CredentialSource = report
			}
			allResults = append(allResults, results...)
			continue
		}

		if cached || source != "" {
			// Every profile in one namespace. The default namespace is the
			// vault, which is what the historical all-profiles path read.
			ns := source
			if ns == "" {
				ns = credNamespaceVault
			}
			names, err := namespaceProfileNames(lookup, ns, provider)
			if err != nil {
				if format != "json" {
					fmt.Fprintf(out, "%s: error listing %s profiles: %v\n", provider, ns, err)
				}
				continue
			}
			for _, name := range names {
				if cached {
					// An offline read needs no credential at all — that is
					// the point of it — so do not open one.
					allResults = append(allResults, cachedProfileUsage(provider, name, lookup.claudeJSONPath(ns, provider, name), now))
					continue
				}
				c := lookup.inspect(ns, provider, name)
				if !c.Found() {
					continue
				}
				allResults = append(allResults, fetcher.FetchAllProfiles(ctx, provider, map[string]string{name: c.Token})...)
			}
			continue
		}

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

	if format != "json" && len(sourceNotes) > 0 {
		for _, note := range sourceNotes {
			fmt.Fprintln(out, note)
		}
		fmt.Fprintln(out)
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

// buildCredentialLookup wires the three credential namespaces from caam's
// process globals. It is separate from the resolver itself so tests can build
// a lookup over temp directories without touching globals.
func buildCredentialLookup(vaultDir string) credentialLookup {
	l := credentialLookup{VaultDir: vaultDir, Profiles: profileStore}
	if home, err := os.UserHomeDir(); err == nil {
		l.LiveHome = home
	}
	if mgr, err := shallow.NewManager("", ""); err == nil {
		l.Shallow = mgr
	}
	l.ActiveName = func(provider string) string {
		if vault == nil {
			return ""
		}
		get, ok := tools[provider]
		if !ok {
			return ""
		}
		name, err := vault.ActiveProfile(get())
		if err != nil {
			return ""
		}
		return name
	}
	return l
}

// cachedProfileUsage builds one row from the usage snapshot Claude Code left
// in a profile's .claude.json.
//
// A profile with no snapshot is a row with an explicit "no cached data" error
// rather than an all-zero row: zero would read as "this account is idle and
// the best one to switch to", which is exactly the wrong conclusion, and the
// error also keeps it out of --best and the recommendations.
func cachedProfileUsage(provider, name, claudeJSON string, now time.Time) usage.ProfileUsage {
	row := usage.ProfileUsage{Provider: provider, ProfileName: name}
	if claudeJSON == "" {
		row.Usage = &usage.UsageInfo{
			Provider: provider, ProfileName: name, Source: usage.SourceCache,
			Error: usage.ErrNoCachedUsage.Error(),
		}
		return row
	}
	info, err := usage.ReadCachedClaudeUsage(claudeJSON, now)
	if err != nil {
		// A file that exists but does not parse is a real problem worth
		// surfacing; an absent or snapshot-less one is the ordinary state.
		msg := usage.ErrNoCachedUsage.Error()
		if !errors.Is(err, usage.ErrNoCachedUsage) {
			msg = err.Error()
		}
		row.Usage = &usage.UsageInfo{
			Provider: provider, ProfileName: name, Source: usage.SourceCache,
			Error: msg,
		}
		return row
	}
	info.ProfileName = name
	row.Usage = info
	return row
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

		// An AS OF column appears only for offline reads: for a live fetch
		// every row is seconds old and the column would be noise, while for a
		// cached read the age IS the caveat.
		offline := false
		for _, r := range results {
			if r.Usage != nil && r.Usage.Source == usage.SourceCache {
				offline = true
				break
			}
		}

		if offline {
			fmt.Fprintln(w, "Rate Limit Usage (cached on disk by Claude Code — no network, no token presented)")
		} else {
			fmt.Fprintln(w, "Rate Limit Usage")
		}
		fmt.Fprintln(w, "──────────────────────────────────────────────────────────────────────────────────────────")

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		header := "PROFILE\tSCORE\tPRIMARY\tSECONDARY\tSCOPED\tRESETS IN\tBURN/HR\tDEPLETES\tSTATUS"
		if offline {
			header += "\tAS OF"
		}
		fmt.Fprintln(tw, header)

		now := time.Now()
		for _, r := range results {
			profileName := fmt.Sprintf("%s/%s", r.Provider, r.ProfileName)
			score := "-"
			primary := "-"
			secondary := "-"
			scoped := "-"
			resetsIn := "-"
			status := "unknown"
			burnRate := "-"
			depletesIn := "-"

			if r.Usage != nil {
				noData := r.Usage.Error == usage.ErrNoCachedUsage.Error()

				switch {
				case noData:
					// Not a failure: this account simply has not refreshed a
					// snapshot yet. Never render it as 0% used.
					status = "no cached data"
				case r.Usage.Error != "":
					status = "error: " + truncate(r.Usage.Error, 20)
				default:
					status = "ok"
				}

				// A row with no data scores nothing. Running the availability
				// scorer over empty windows would return a perfect 100 and
				// present an account caam knows nothing about as the idlest
				// one on the table.
				if !noData {
					score = strconv.Itoa(r.Usage.AvailabilityScoreForModel(model))
					scoped = formatScopedLimit(r.Usage.ScopedLimit(model))
				}

				primary = formatWindowPercent(r.Usage.PrimaryWindow)
				secondary = formatWindowPercent(r.Usage.SecondaryWindow)

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

			if offline {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					profileName, score, primary, secondary, scoped, resetsIn, burnRate, depletesIn, status,
					formatCacheAge(r.Usage, now))
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					profileName, score, primary, secondary, scoped, resetsIn, burnRate, depletesIn, status)
			}
		}

		tw.Flush()
		if offline {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Figures are as cached by Claude Code and only move when that account itself runs a")
			fmt.Fprintln(w, "session; \"no cached data\" means the account has none yet, not that it is idle.")
		}
		return nil

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

// formatWindowPercent renders a window's utilization. A window that had
// already rolled over when the figures were read is marked, so an honest 0%
// from a stale snapshot is not mistaken for a measured one.
func formatWindowPercent(w *usage.UsageWindow) string {
	if w == nil {
		return "-"
	}
	if w.Rolled {
		return "0% (rolled)"
	}
	return fmt.Sprintf("%d%%", w.UsedPercent)
}

// formatCacheAge renders how stale a cached snapshot is. A snapshot with no
// timestamp of its own reports "unknown" rather than "0s ago".
func formatCacheAge(u *usage.UsageInfo, now time.Time) string {
	if u == nil {
		return "-"
	}
	if u.Error == usage.ErrNoCachedUsage.Error() {
		return "-"
	}
	age, ok := u.CacheAge(now)
	if !ok {
		return "unknown"
	}
	return formatLimitsDuration(age) + " ago"
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
