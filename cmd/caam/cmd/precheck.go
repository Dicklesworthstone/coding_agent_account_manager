package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/prediction"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// precheckCmd shows session planning with recommended profiles, burn rates, and rotation forecast.
var precheckCmd = &cobra.Command{
	Use:   "precheck [provider]",
	Short: "Plan your session - see which profile is best",
	Long: `Shows session planning information to help you choose the best profile.

The precheck command fetches real-time rate limit data for all profiles of a
provider and shows:
- Recommended profile based on rotation algorithm
- Backup profiles in priority order
- Profiles currently in cooldown
- Usage percentages with visual bars
- Predictions for when limits will be hit
- Alerts for profiles approaching limits

Examples:
  caam precheck claude              # Full session planner for Claude
  caam precheck codex               # Full session planner for Codex
  caam precheck claude --format json    # JSON output for scripting
  caam precheck claude --format brief   # One-line recommendation
  caam precheck claude --no-fetch       # Skip API calls (use cached data)`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPrecheckCmd,
}

func init() {
	precheckCmd.Flags().String("format", "table", "output format: table, json, brief")
	precheckCmd.Flags().Bool("no-fetch", false, "skip real-time API fetch (use cached/health data)")
	precheckCmd.Flags().Duration("timeout", 30*time.Second, "timeout for API fetches")
	precheckCmd.Flags().String("algorithm", "", "override rotation algorithm (smart, round_robin, random)")
	rootCmd.AddCommand(precheckCmd)
}

// PrecheckResult contains the structured output for precheck.
type PrecheckResult struct {
	Provider    string                `json:"provider"`
	Recommended *ProfileRecommendation `json:"recommended,omitempty"`
	Backups     []ProfileRecommendation `json:"backups"`
	InCooldown  []CooldownProfile      `json:"in_cooldown"`
	Alerts      []PrecheckAlert        `json:"alerts"`
	Summary     *UsageSummary          `json:"summary"`
	Forecast    *RotationForecast      `json:"forecast,omitempty"`
	Algorithm   string                 `json:"algorithm"`
	FetchedAt   time.Time              `json:"fetched_at"`
}

// ProfileRecommendation represents a profile with its recommendation data.
type ProfileRecommendation struct {
	Name           string   `json:"name"`
	Score          float64  `json:"score"`
	UsagePercent   int      `json:"usage_percent"`
	AvailScore     int      `json:"availability_score"`
	HealthStatus   string   `json:"health_status"`
	TokenExpiry    string   `json:"token_expiry,omitempty"`
	TimeToDepletion string  `json:"time_to_depletion,omitempty"`
	Reasons        []string `json:"reasons"`
	PoolStatus     string   `json:"pool_status"`
}

// CooldownProfile represents a profile in cooldown.
type CooldownProfile struct {
	Name          string    `json:"name"`
	CooldownUntil time.Time `json:"cooldown_until"`
	Remaining     string    `json:"remaining"`
}

// PrecheckAlert represents an alert for the precheck.
type PrecheckAlert struct {
	Type      string `json:"type"`
	Profile   string `json:"profile,omitempty"`
	Message   string `json:"message"`
	Urgency   string `json:"urgency"`
	Action    string `json:"action,omitempty"`
}

// UsageSummary contains aggregate usage information.
type UsageSummary struct {
	TotalProfiles   int    `json:"total_profiles"`
	ReadyProfiles   int    `json:"ready_profiles"`
	CooldownCount   int    `json:"cooldown_count"`
	AvgUsagePercent int    `json:"avg_usage_percent"`
	HealthyCount    int    `json:"healthy_count"`
	WarningCount    int    `json:"warning_count"`
	CriticalCount   int    `json:"critical_count"`
}

// RotationForecast contains rotation prediction data.
type RotationForecast struct {
	NextRotation     string `json:"next_rotation,omitempty"`
	RecommendedWait  string `json:"recommended_wait,omitempty"`
	ProfilesUntilReset int  `json:"profiles_until_reset"`
}

func runPrecheckCmd(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	noFetch, _ := cmd.Flags().GetBool("no-fetch")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	algoOverride, _ := cmd.Flags().GetString("algorithm")

	// Default to claude if no provider specified
	provider := "claude"
	if len(args) > 0 {
		provider = strings.ToLower(args[0])
	}

	// Validate provider
	if _, ok := tools[provider]; !ok {
		return fmt.Errorf("unknown provider: %s (supported: claude, codex, gemini)", provider)
	}

	// Initialize dependencies
	vaultInst := authfile.NewVault(authfile.DefaultVaultPath())
	healthStoreInst := health.NewStorage(health.DefaultHealthPath())

	// List profiles
	profiles, err := vaultInst.List(provider)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	// Filter out system profiles
	var userProfiles []string
	for _, p := range profiles {
		if !strings.HasPrefix(p, "_") {
			userProfiles = append(userProfiles, p)
		}
	}

	if len(userProfiles) == 0 {
		return fmt.Errorf("no profiles found for %s; create one with 'caam backup %s <name>'", provider, provider)
	}

	// Load SPM config
	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		spmCfg = config.DefaultSPMConfig()
	}

	if algoOverride != "" {
		spmCfg.Stealth.Rotation.Algorithm = algoOverride
	}

	// Open database
	var db *caamdb.DB
	db, err = caamdb.Open()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStderr(), "Warning: could not open database: %v\n", err)
	} else {
		defer db.Close()
	}

	// Initialize auth pool
	pool := authpool.NewAuthPool(authpool.WithVault(vaultInst))
	ctx := context.Background()
	if loadErr := pool.LoadFromVault(ctx); loadErr != nil {
		// Non-fatal: pool can still work without pre-loaded data
		fmt.Fprintf(cmd.OutOrStderr(), "Warning: could not load auth pool: %v\n", loadErr)
	}

	// Fetch real-time usage data (unless --no-fetch)
	var usageResults []usage.ProfileUsage
	var usageMap map[string]*usage.UsageInfo

	if !noFetch && (provider == "claude" || provider == "codex") {
		credentials, err := usage.LoadProfileCredentials(authfile.DefaultVaultPath(), provider)
		if err == nil && len(credentials) > 0 {
			fetchCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			fetcher := usage.NewMultiProfileFetcher()
			usageResults = fetcher.FetchAllProfiles(fetchCtx, provider, credentials)

			usageMap = make(map[string]*usage.UsageInfo)
			for _, r := range usageResults {
				if r.Usage != nil {
					usageMap[r.ProfileName] = r.Usage
				}
			}
		}
	}

	// Build rotation selector
	algorithm := rotation.AlgorithmSmart
	if spmCfg.Stealth.Rotation.Algorithm != "" {
		algorithm = rotation.Algorithm(spmCfg.Stealth.Rotation.Algorithm)
	}

	selector := rotation.NewSelector(algorithm, healthStoreInst, db)

	// Set usage data for smart selection
	if len(usageMap) > 0 {
		rotationUsage := make(map[string]*rotation.UsageInfo)
		for name, info := range usageMap {
			ru := &rotation.UsageInfo{
				ProfileName: name,
				AvailScore:  info.AvailabilityScore(),
				Error:       info.Error,
			}
			if info.PrimaryWindow != nil {
				ru.PrimaryPercent = info.PrimaryWindow.UsedPercent
			}
			if info.SecondaryWindow != nil {
				ru.SecondaryPercent = info.SecondaryWindow.UsedPercent
			}
			rotationUsage[name] = ru
		}
		selector.SetUsageData(rotationUsage)
	}

	// Get current active profile
	fileSet := tools[provider]()
	currentProfile, _ := vaultInst.ActiveProfile(fileSet)

	// Run rotation selection
	selectionResult, err := selector.Select(provider, userProfiles, currentProfile)
	if err != nil && !strings.Contains(err.Error(), "cooldown") {
		return fmt.Errorf("rotation selection failed: %w", err)
	}

	// Build precheck result
	result := buildPrecheckResult(provider, userProfiles, selectionResult, usageMap, pool, healthStoreInst, db, string(algorithm))

	// Output based on format
	switch format {
	case "json":
		return precheckOutputJSON(cmd.OutOrStdout(), result)
	case "brief":
		return precheckOutputBrief(cmd.OutOrStdout(), result)
	default:
		return precheckOutputTable(cmd.OutOrStdout(), result)
	}
}

func buildPrecheckResult(
	provider string,
	profiles []string,
	selection *rotation.Result,
	usageMap map[string]*usage.UsageInfo,
	pool *authpool.AuthPool,
	healthStore *health.Storage,
	db *caamdb.DB,
	algorithm string,
) *PrecheckResult {
	result := &PrecheckResult{
		Provider:   provider,
		Backups:    make([]ProfileRecommendation, 0),
		InCooldown: make([]CooldownProfile, 0),
		Alerts:     make([]PrecheckAlert, 0),
		Algorithm:  algorithm,
		FetchedAt:  time.Now(),
	}

	// Summary counters
	summary := &UsageSummary{
		TotalProfiles: len(profiles),
	}

	now := time.Now()
	var totalUsage int
	var usageCount int

	// Process each profile
	for _, profileName := range profiles {
		rec := ProfileRecommendation{
			Name:       profileName,
			PoolStatus: "unknown",
		}

		// Get usage data
		if usageMap != nil {
			if info, ok := usageMap[profileName]; ok && info != nil {
				rec.AvailScore = info.AvailabilityScore()
				if info.PrimaryWindow != nil {
					rec.UsagePercent = info.PrimaryWindow.UsedPercent
					totalUsage += rec.UsagePercent
					usageCount++
				}
				if info.TimeToDepletion() > 0 {
					rec.TimeToDepletion = formatDurationShort(info.TimeToDepletion())
				}
			}
		}

		// Get health status
		if healthStore != nil {
			if h, err := healthStore.GetProfile(provider, profileName); err == nil && h != nil {
				status := health.CalculateStatus(h)
				rec.HealthStatus = status.String()
				switch status {
				case health.StatusHealthy:
					summary.HealthyCount++
				case health.StatusWarning:
					summary.WarningCount++
				case health.StatusCritical:
					summary.CriticalCount++
				}
				if !h.TokenExpiresAt.IsZero() {
					ttl := time.Until(h.TokenExpiresAt)
					if ttl > 0 {
						rec.TokenExpiry = formatDurationShort(ttl)
					} else {
						rec.TokenExpiry = "expired"
					}
				}
			}
		}

		// Get pool status
		if pool != nil {
			poolStatus := pool.GetStatus(provider, profileName)
			rec.PoolStatus = poolStatus.String()
		}

		// Check cooldown
		if db != nil {
			if ev, err := db.ActiveCooldown(provider, profileName, now); err == nil && ev != nil {
				remaining := time.Until(ev.CooldownUntil)
				if remaining > 0 {
					result.InCooldown = append(result.InCooldown, CooldownProfile{
						Name:          profileName,
						CooldownUntil: ev.CooldownUntil,
						Remaining:     formatDurationShort(remaining),
					})
					summary.CooldownCount++
					continue // Don't add to backups
				}
			}
		}

		// Get score and reasons from selection result
		if selection != nil {
			for _, ps := range selection.Alternatives {
				if ps.Name == profileName {
					rec.Score = ps.Score
					for _, r := range ps.Reasons {
						prefix := "+"
						if !r.Positive {
							prefix = "-"
						}
						rec.Reasons = append(rec.Reasons, prefix+" "+r.Text)
					}
					break
				}
			}
		}

		// Determine if recommended or backup
		if selection != nil && selection.Selected == profileName {
			result.Recommended = &rec
			summary.ReadyProfiles++
		} else if rec.Score > -9000 { // Not in cooldown
			result.Backups = append(result.Backups, rec)
			summary.ReadyProfiles++
		}
	}

	// Sort backups by score
	sort.Slice(result.Backups, func(i, j int) bool {
		return result.Backups[i].Score > result.Backups[j].Score
	})

	// Calculate average usage
	if usageCount > 0 {
		summary.AvgUsagePercent = totalUsage / usageCount
	}
	result.Summary = summary

	// Generate alerts using prediction package
	if len(usageMap) > 0 {
		// Convert usage map to predictions
		var usageInfos []*usage.UsageInfo
		for _, info := range usageMap {
			usageInfos = append(usageInfos, info)
		}

		engine := prediction.NewPredictionEngine()
		var predictions []*prediction.Prediction
		for _, info := range usageInfos {
			pred := engine.Predict(context.Background(), info)
			if pred != nil && pred.Error == "" {
				predictions = append(predictions, pred)
			}
		}

		alertOpts := prediction.DefaultAlertOptions()
		alerts := prediction.GenerateAlerts(predictions, alertOpts)
		for _, a := range alerts {
			urgency := "low"
			switch a.Urgency {
			case prediction.UrgencyMedium:
				urgency = "medium"
			case prediction.UrgencyHigh:
				urgency = "high"
			}
			alertType := "info"
			switch a.Type {
			case prediction.AlertImminentLimit:
				alertType = "imminent"
			case prediction.AlertApproachingLimit:
				alertType = "approaching"
			case prediction.AlertSwitchRecommended:
				alertType = "rotation_recommended"
			case prediction.AlertAllProfilesLow:
				alertType = "all_low"
			}
			result.Alerts = append(result.Alerts, PrecheckAlert{
				Type:    alertType,
				Profile: a.Profile,
				Message: a.Message,
				Urgency: urgency,
				Action:  a.SuggestedAction,
			})
		}
	}

	// Build rotation forecast
	if result.Recommended != nil && result.Recommended.TimeToDepletion != "" {
		result.Forecast = &RotationForecast{
			NextRotation:       result.Recommended.TimeToDepletion,
			ProfilesUntilReset: summary.ReadyProfiles,
		}
	}

	return result
}

func precheckOutputJSON(w io.Writer, result *PrecheckResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func precheckOutputBrief(w io.Writer, result *PrecheckResult) error {
	if result.Recommended == nil {
		if len(result.InCooldown) > 0 {
			fmt.Fprintf(w, "%s: all in cooldown\n", result.Provider)
		} else {
			fmt.Fprintf(w, "%s: no profiles\n", result.Provider)
		}
		return nil
	}

	rec := result.Recommended
	extra := ""
	if rec.UsagePercent > 0 {
		extra = fmt.Sprintf(" (%d%% used)", rec.UsagePercent)
	}
	fmt.Fprintf(w, "%s: %s%s\n", result.Provider, rec.Name, extra)
	return nil
}

func precheckOutputTable(w io.Writer, result *PrecheckResult) error {
	// Header
	fmt.Fprintln(w, strings.Repeat("=", 72))
	fmt.Fprintf(w, "  SESSION PLANNER: %s  |  Algorithm: %s  |  %s\n",
		strings.ToUpper(result.Provider),
		result.Algorithm,
		result.FetchedAt.Format("15:04:05"))
	fmt.Fprintln(w, strings.Repeat("=", 72))
	fmt.Fprintln(w)

	// Recommended profile
	if result.Recommended != nil {
		rec := result.Recommended
		fmt.Fprintln(w, "  RECOMMENDED:")
		fmt.Fprintf(w, "    %s", rec.Name)
		if rec.HealthStatus != "" {
			fmt.Fprintf(w, " [%s]", rec.HealthStatus)
		}
		fmt.Fprintln(w)

		// Usage bar
		if rec.UsagePercent > 0 || rec.AvailScore > 0 {
			fmt.Fprintf(w, "    Usage:  %s %d%%\n", renderUsageBar(rec.UsagePercent, 20), rec.UsagePercent)
			fmt.Fprintf(w, "    Avail:  %d/100\n", rec.AvailScore)
		}

		// Time to depletion
		if rec.TimeToDepletion != "" {
			fmt.Fprintf(w, "    Est. time to limit: %s\n", rec.TimeToDepletion)
		}

		// Token expiry
		if rec.TokenExpiry != "" {
			fmt.Fprintf(w, "    Token expires: %s\n", rec.TokenExpiry)
		}

		// Reasons
		if len(rec.Reasons) > 0 {
			fmt.Fprintln(w, "    Factors:")
			for _, r := range rec.Reasons {
				fmt.Fprintf(w, "      %s\n", r)
			}
		}
		fmt.Fprintln(w)
	}

	// Backup profiles
	if len(result.Backups) > 0 {
		fmt.Fprintln(w, "  BACKUP PROFILES:")
		for i, backup := range result.Backups {
			if i >= 3 { // Show top 3 backups
				fmt.Fprintf(w, "    ... and %d more\n", len(result.Backups)-3)
				break
			}
			statusStr := ""
			if backup.HealthStatus != "" {
				statusStr = fmt.Sprintf(" [%s]", backup.HealthStatus)
			}
			usageStr := ""
			if backup.UsagePercent > 0 {
				usageStr = fmt.Sprintf(" %d%%", backup.UsagePercent)
			}
			fmt.Fprintf(w, "    %d. %s%s%s\n", i+1, backup.Name, statusStr, usageStr)
		}
		fmt.Fprintln(w)
	}

	// Cooldown profiles
	if len(result.InCooldown) > 0 {
		fmt.Fprintln(w, "  IN COOLDOWN:")
		for _, cd := range result.InCooldown {
			fmt.Fprintf(w, "    %s - %s remaining\n", cd.Name, cd.Remaining)
		}
		fmt.Fprintln(w)
	}

	// Alerts
	if len(result.Alerts) > 0 {
		fmt.Fprintln(w, "  ALERTS:")
		for _, alert := range result.Alerts {
			icon := "!"
			switch alert.Urgency {
			case "high":
				icon = "!!!"
			case "medium":
				icon = "!!"
			}
			fmt.Fprintf(w, "    [%s] %s\n", icon, alert.Message)
			if alert.Action != "" {
				fmt.Fprintf(w, "        Action: %s\n", alert.Action)
			}
		}
		fmt.Fprintln(w)
	}

	// Summary
	if result.Summary != nil {
		s := result.Summary
		fmt.Fprintln(w, "  SUMMARY:")
		fmt.Fprintf(w, "    Profiles: %d total, %d ready, %d cooldown\n",
			s.TotalProfiles, s.ReadyProfiles, s.CooldownCount)
		fmt.Fprintf(w, "    Health:   %d healthy, %d warning, %d critical\n",
			s.HealthyCount, s.WarningCount, s.CriticalCount)
		if s.AvgUsagePercent > 0 {
			fmt.Fprintf(w, "    Avg usage: %d%%\n", s.AvgUsagePercent)
		}
		fmt.Fprintln(w)
	}

	// Quick actions
	fmt.Fprintln(w, "  QUICK ACTIONS:")
	if result.Recommended != nil {
		fmt.Fprintf(w, "    caam activate %s %s    # Switch to recommended\n",
			result.Provider, result.Recommended.Name)
	}
	fmt.Fprintf(w, "    caam next %s              # Auto-rotate to next best\n", result.Provider)
	fmt.Fprintf(w, "    caam run %s -- ...        # Run with auto-failover\n", result.Provider)
	fmt.Fprintln(w)

	fmt.Fprintln(w, strings.Repeat("=", 72))

	return nil
}

// renderUsageBar creates an ASCII usage bar.
func renderUsageBar(percent int, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := (percent * width) / 100
	empty := width - filled

	bar := strings.Repeat("#", filled) + strings.Repeat("-", empty)
	return "[" + bar + "]"
}
