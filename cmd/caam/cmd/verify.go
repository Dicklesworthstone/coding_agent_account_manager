package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/spf13/cobra"
)

// VerifyProfileResult represents the verification result for a single profile.
type VerifyProfileResult struct {
	Provider    string     `json:"provider"`
	Profile     string     `json:"profile"`
	Status      string     `json:"status"` // "healthy", "warning", "critical", "unknown"
	TokenExpiry *time.Time `json:"token_expiry,omitempty"`
	ExpiresIn   string     `json:"expires_in,omitempty"`
	ErrorCount  int        `json:"error_count,omitempty"`
	Penalty     float64    `json:"penalty,omitempty"`
	Issues      []string   `json:"issues,omitempty"`
	Score       float64    `json:"score"`
}

// VerifyOutput represents the complete verification output.
type VerifyOutput struct {
	Profiles        []VerifyProfileResult `json:"profiles"`
	Summary         VerifySummary         `json:"summary"`
	Recommendations []string              `json:"recommendations,omitempty"`
	Fixes           []VerifyFixResult     `json:"fixes,omitempty"`
}

// VerifyFixResult records the outcome of a --fix refresh attempt for one profile.
type VerifyFixResult struct {
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
	Action   string `json:"action"` // "refreshed", "skipped", "failed"
	Detail   string `json:"detail,omitempty"`
}

// VerifySummary provides a summary of verification results.
type VerifySummary struct {
	TotalProfiles int `json:"total_profiles"`
	HealthyCount  int `json:"healthy_count"`
	WarningCount  int `json:"warning_count"`
	CriticalCount int `json:"critical_count"`
	UnknownCount  int `json:"unknown_count"`
}

var verifyCmd = &cobra.Command{
	Use:   "verify [tool]",
	Short: "Validate all profile tokens",
	Long: `Check the health and validity of all saved profile tokens.

Verifies:
  - Token expiration times
  - Recent error counts
  - Penalty scores

Examples:
  caam verify              # Verify all profiles
  caam verify claude       # Verify only Claude profiles
  caam verify --json       # Machine-readable output
  caam verify --fix        # Auto-refresh expiring tokens`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerify,
}

func init() {
	rootCmd.AddCommand(verifyCmd)
	verifyCmd.Flags().Bool("json", false, "output as JSON")
	verifyCmd.Flags().Bool("fix", false, "auto-refresh expiring/expired tokens for refreshable profiles")
}

func runVerify(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	fixMode, _ := cmd.Flags().GetBool("fix")

	var toolFilter string
	if len(args) > 0 {
		toolFilter = strings.ToLower(args[0])
		if _, ok := tools[toolFilter]; !ok {
			return fmt.Errorf("unknown tool: %s (supported: %s)", toolFilter, supportedToolsList())
		}
	}

	output := &VerifyOutput{
		Profiles:        []VerifyProfileResult{},
		Recommendations: []string{},
	}

	// --fix: before reporting, attempt to refresh refreshable profiles whose
	// tokens are expiring or expired. This delegates to the same provider-aware
	// refresh path as `caam refresh` (Claude is intentionally unsupported and is
	// reported as skipped; Codex/Gemini refresh where possible). Verification
	// below then reflects the post-refresh state.
	if fixMode {
		output.Fixes = runVerifyFix(cmd, toolFilter)
	}

	// Get all profiles
	providers := supportedTools()
	for _, provider := range providers {
		if toolFilter != "" && provider != toolFilter {
			continue
		}

		profiles, err := vault.List(provider)
		if err != nil {
			continue
		}

		for _, profileName := range profiles {
			result := verifyProfile(provider, profileName)
			output.Profiles = append(output.Profiles, result)

			// Update summary
			switch result.Status {
			case "healthy":
				output.Summary.HealthyCount++
			case "warning":
				output.Summary.WarningCount++
			case "critical":
				output.Summary.CriticalCount++
			default:
				output.Summary.UnknownCount++
			}
		}
	}

	output.Summary.TotalProfiles = len(output.Profiles)

	// Generate recommendations
	output.Recommendations = generateRecommendations(output)

	if jsonOutput {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	printVerifyOutput(cmd.OutOrStdout(), output)
	return nil
}

// runVerifyFix attempts to refresh refreshable profiles whose tokens are
// expiring or expired, returning a per-profile record of what happened. It is
// best-effort: individual refresh failures are recorded but do not abort the
// overall verify run (verification still reports the resulting health).
func runVerifyFix(cmd *cobra.Command, toolFilter string) []VerifyFixResult {
	ctx := cmd.Context()

	threshold := refresh.DefaultRefreshThreshold
	if spmCfg, err := config.LoadSPMConfig(); err == nil {
		if v := spmCfg.Health.RefreshThreshold.Duration(); v > 0 {
			threshold = v
		}
	}

	var results []VerifyFixResult

	for _, provider := range supportedTools() {
		if toolFilter != "" && provider != toolFilter {
			continue
		}

		profiles, err := vault.List(provider)
		if err != nil {
			continue
		}
		sort.Strings(profiles)

		for _, profileName := range profiles {
			should, reason, err := shouldRefreshProfile(provider, profileName, threshold, false)
			if err != nil {
				results = append(results, VerifyFixResult{
					Provider: provider, Profile: profileName,
					Action: "failed", Detail: err.Error(),
				})
				continue
			}
			if !should {
				// Not expiring/expired (or no refresh token) — nothing to fix.
				continue
			}

			if err := refresh.RefreshProfile(ctx, provider, profileName, vault, healthStore); err != nil {
				if errors.Is(err, refresh.ErrUnsupported) {
					results = append(results, VerifyFixResult{
						Provider: provider, Profile: profileName,
						Action: "skipped", Detail: err.Error(),
					})
					continue
				}
				results = append(results, VerifyFixResult{
					Provider: provider, Profile: profileName,
					Action: "failed", Detail: err.Error(),
				})
				continue
			}

			// Keep the isolated profile in sync to avoid token drift.
			syncVaultToIsolated(provider, profileName)
			results = append(results, VerifyFixResult{
				Provider: provider, Profile: profileName,
				Action: "refreshed", Detail: reason,
			})
		}
	}

	return results
}

func verifyProfile(provider, profileName string) VerifyProfileResult {
	result := VerifyProfileResult{
		Provider: provider,
		Profile:  profileName,
		Status:   "unknown",
		Issues:   []string{},
	}

	// Get profile health using the existing getProfileHealth function
	ph := getProfileHealth(provider, profileName)
	if ph == nil {
		result.Issues = append(result.Issues, "Could not retrieve health data")
		return result
	}

	// Calculate health status
	status, score := health.CalculateHealth(ph, health.DefaultHealthConfig())
	result.Score = score

	// Convert status to string
	switch status {
	case health.StatusHealthy:
		result.Status = "healthy"
	case health.StatusWarning:
		result.Status = "warning"
	case health.StatusCritical:
		result.Status = "critical"
	default:
		result.Status = "unknown"
	}

	// Token expiry information
	if !ph.TokenExpiresAt.IsZero() {
		result.TokenExpiry = &ph.TokenExpiresAt
		remaining := time.Until(ph.TokenExpiresAt)
		if remaining > 0 {
			result.ExpiresIn = formatTimeRemaining(remaining)
		} else {
			result.ExpiresIn = "expired"
			result.Issues = append(result.Issues, fmt.Sprintf("Token expired %s ago", formatTimeRemaining(-remaining)))
		}
	} else {
		result.Issues = append(result.Issues, "No token expiry information found")
	}

	// Error information
	result.ErrorCount = ph.ErrorCount1h
	if ph.ErrorCount1h > 0 {
		result.Issues = append(result.Issues, fmt.Sprintf("%d error(s) in the last hour", ph.ErrorCount1h))
	}

	// Penalty information
	result.Penalty = ph.Penalty
	if ph.Penalty > 0.5 {
		result.Issues = append(result.Issues, fmt.Sprintf("High penalty score: %.2f", ph.Penalty))
	}

	// Warning for expiring soon
	if !ph.TokenExpiresAt.IsZero() {
		remaining := time.Until(ph.TokenExpiresAt)
		if remaining > 0 && remaining < time.Hour {
			result.Issues = append(result.Issues, "Token expiring soon (within 1 hour)")
		}
	}

	return result
}

func formatTimeRemaining(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return "< 1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		if mins > 0 {
			return fmt.Sprintf("%dh%dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := int(d.Hours() / 24)
	return fmt.Sprintf("%dd", days)
}

func generateRecommendations(output *VerifyOutput) []string {
	var recs []string

	// Group by provider for recommendations
	expiredByProvider := make(map[string][]string)
	expiringSoonByProvider := make(map[string][]string)

	for _, p := range output.Profiles {
		if p.ExpiresIn == "expired" {
			expiredByProvider[p.Provider] = append(expiredByProvider[p.Provider], p.Profile)
		} else if p.TokenExpiry != nil && time.Until(*p.TokenExpiry) < time.Hour {
			expiringSoonByProvider[p.Provider] = append(expiringSoonByProvider[p.Provider], p.Profile)
		}
	}

	// Expiring soon recommendations
	for provider, profiles := range expiringSoonByProvider {
		for _, profile := range profiles {
			recs = append(recs, fmt.Sprintf("Run 'caam refresh %s %s' to refresh expiring token", provider, profile))
		}
	}

	// Expired recommendations
	for provider, profiles := range expiredByProvider {
		for _, profile := range profiles {
			recs = append(recs, fmt.Sprintf("Re-login to '%s/%s' - token has expired", provider, profile))
		}
	}

	// General recommendations
	if output.Summary.CriticalCount > 0 {
		recs = append(recs, "Consider removing or re-authenticating critical profiles")
	}

	return recs
}

func printVerifyOutput(w io.Writer, output *VerifyOutput) {
	if len(output.Fixes) > 0 {
		fmt.Fprintln(w, "Fix attempts (--fix):")
		for _, fix := range output.Fixes {
			line := fmt.Sprintf("  %s %s/%s", verifyFixIcon(fix.Action), fix.Provider, fix.Profile)
			if fix.Detail != "" {
				line += fmt.Sprintf(" - %s (%s)", fix.Action, fix.Detail)
			} else {
				line += " - " + fix.Action
			}
			fmt.Fprintln(w, line)
		}
		fmt.Fprintln(w)
	}

	if len(output.Profiles) == 0 {
		fmt.Fprintln(w, "No profiles found.")
		return
	}

	fmt.Fprintln(w, "Profile Health Verification")
	fmt.Fprintln(w, "═══════════════════════════════════════════════════════════")
	fmt.Fprintln(w)

	// Group by provider
	byProvider := make(map[string][]VerifyProfileResult)
	for _, p := range output.Profiles {
		byProvider[p.Provider] = append(byProvider[p.Provider], p)
	}

	providers := make([]string, 0, len(byProvider))
	for p := range byProvider {
		providers = append(providers, p)
	}
	sort.Strings(providers)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	for _, provider := range providers {
		profiles := byProvider[provider]
		fmt.Fprintf(w, "%s:\n", strings.ToUpper(provider[:1])+provider[1:])

		for _, p := range profiles {
			icon := getVerifyStatusIcon(p.Status)
			expiryInfo := ""
			if p.ExpiresIn != "" {
				if p.ExpiresIn == "expired" {
					expiryInfo = "(expired)"
				} else {
					expiryInfo = fmt.Sprintf("(expires in %s)", p.ExpiresIn)
				}
			}

			issuesInfo := ""
			if len(p.Issues) > 0 && p.Status != "healthy" {
				issuesInfo = " - " + p.Issues[0]
			}

			fmt.Fprintf(tw, "  %s %s\t%s%s\n", icon, p.Profile, expiryInfo, issuesInfo)
		}
		tw.Flush()
		fmt.Fprintln(w)
	}

	// Summary
	fmt.Fprintln(w, "───────────────────────────────────────────────────────────")
	fmt.Fprintf(w, "Summary: %d healthy, %d warning, %d critical",
		output.Summary.HealthyCount,
		output.Summary.WarningCount,
		output.Summary.CriticalCount)
	if output.Summary.UnknownCount > 0 {
		fmt.Fprintf(w, ", %d unknown", output.Summary.UnknownCount)
	}
	fmt.Fprintln(w)

	// Recommendations
	if len(output.Recommendations) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Recommendations:")
		for _, rec := range output.Recommendations {
			fmt.Fprintf(w, "  • %s\n", rec)
		}
	}
}

func verifyFixIcon(action string) string {
	switch action {
	case "refreshed":
		return "✓"
	case "skipped":
		return "•"
	case "failed":
		return "✗"
	default:
		return "?"
	}
}

func getVerifyStatusIcon(status string) string {
	switch status {
	case "healthy":
		return "✓"
	case "warning":
		return "⚠"
	case "critical":
		return "✗"
	default:
		return "?"
	}
}
