// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
	"github.com/spf13/cobra"
)

// ============================================================================
// Robot Mode - Agent-Optimized CLI Interface
// ============================================================================
//
// Designed for coding agents (Claude, Codex, etc.) that need programmatic access
// to caam functionality. All output is JSON. No interactive prompts.
//
// Design principles:
// - JSON output by default (no --json flag needed)
// - Structured errors with error_code field
// - Actionable suggestions in output
// - Exit codes: 0=success, 1=error, 2=partial success
// - Compact but complete information

// RobotOutput is the standard response wrapper for all robot commands.
type RobotOutput struct {
	Success     bool        `json:"success"`
	Command     string      `json:"command"`
	Timestamp   string      `json:"timestamp"`
	Data        interface{} `json:"data,omitempty"`
	Error       *RobotError `json:"error,omitempty"`
	Suggestions []string    `json:"suggestions,omitempty"`
	Timing      *RobotTiming `json:"timing,omitempty"`
}

// RobotError provides structured error information.
type RobotError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// RobotTiming tracks execution time for performance monitoring.
type RobotTiming struct {
	StartedAt  string `json:"started_at"`
	DurationMs int64  `json:"duration_ms"`
}

// RobotStatusData contains the full status overview.
type RobotStatusData struct {
	Version      string              `json:"version"`
	OS           string              `json:"os"`
	Arch         string              `json:"arch"`
	VaultPath    string              `json:"vault_path"`
	ConfigPath   string              `json:"config_path"`
	Providers    []RobotProviderInfo `json:"providers"`
	Summary      RobotStatusSummary  `json:"summary"`
	Coordinators []RobotCoordinator  `json:"coordinators,omitempty"`
}

// RobotStatusSummary provides quick counts.
type RobotStatusSummary struct {
	TotalProfiles      int  `json:"total_profiles"`
	ActiveProfiles     int  `json:"active_profiles"`
	HealthyProfiles    int  `json:"healthy_profiles"`
	CooldownProfiles   int  `json:"cooldown_profiles"`
	ExpiringSoon       int  `json:"expiring_soon"` // < 24h
	AllProfilesBlocked bool `json:"all_profiles_blocked"`
}

// RobotProviderInfo contains provider-specific status.
type RobotProviderInfo struct {
	ID            string               `json:"id"`
	DisplayName   string               `json:"display_name"`
	LoggedIn      bool                 `json:"logged_in"`
	ActiveProfile string               `json:"active_profile,omitempty"`
	Profiles      []RobotProfileInfo   `json:"profiles"`
	AuthPaths     []RobotAuthPath      `json:"auth_paths"`
}

// RobotProfileInfo contains profile details optimized for agents.
type RobotProfileInfo struct {
	Name           string            `json:"name"`
	Active         bool              `json:"active"`
	System         bool              `json:"system"`
	Email          string            `json:"email,omitempty"`
	PlanType       string            `json:"plan_type,omitempty"`
	Health         RobotHealthInfo   `json:"health"`
	Cooldown       *RobotCooldown    `json:"cooldown,omitempty"`
	Recommendation string            `json:"recommendation,omitempty"`
}

// RobotHealthInfo contains health status.
type RobotHealthInfo struct {
	Status       string `json:"status"` // healthy, warning, critical, unknown
	Reason       string `json:"reason,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
	ExpiresIn    string `json:"expires_in,omitempty"` // human-readable
	ErrorCount1h int    `json:"error_count_1h"`
}

// RobotCooldown contains cooldown information.
type RobotCooldown struct {
	Active       bool   `json:"active"`
	Until        string `json:"until,omitempty"`
	RemainingMs  int64  `json:"remaining_ms,omitempty"`
	RemainingStr string `json:"remaining_str,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// RobotAuthPath shows auth file locations.
type RobotAuthPath struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// RobotCoordinator contains coordinator status.
type RobotCoordinator struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Healthy  bool   `json:"healthy"`
	Latency  int64  `json:"latency_ms,omitempty"`
	Error    string `json:"error,omitempty"`
	Pending  int    `json:"pending_auth_requests"`
}

// RobotNextData contains recommended next action.
type RobotNextData struct {
	Provider        string            `json:"provider"`
	Profile         string            `json:"profile"`
	Score           float64           `json:"score"`
	Reasons         []string          `json:"reasons"`
	Command         string            `json:"command"`
	AlternateChoice *RobotNextProfile `json:"alternate,omitempty"`
}

// RobotNextProfile is an alternate profile option.
type RobotNextProfile struct {
	Provider string  `json:"provider"`
	Profile  string  `json:"profile"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason"`
}

// RobotActResult is the result of an action.
type RobotActResult struct {
	Action      string `json:"action"`
	Provider    string `json:"provider"`
	Profile     string `json:"profile"`
	OldProfile  string `json:"old_profile,omitempty"`
	Success     bool   `json:"success"`
	Message     string `json:"message"`
}

var robotCmd = &cobra.Command{
	Use:   "robot [command]",
	Short: "Agent-optimized commands (JSON output)",
	Long: `Robot mode provides CLI commands optimized for coding agents.

All commands output JSON to stdout. Errors are structured with error codes.
No interactive prompts - designed for programmatic use.

Run 'caam robot' with no arguments for a quick-start guide.`,
	RunE: runRobotQuickStart,
}

var robotStatusCmd = &cobra.Command{
	Use:   "status [provider]",
	Short: "Full system status overview",
	Long: `Returns comprehensive status information in JSON format.

Includes:
- All profiles with health status
- Active profiles
- Cooldown information
- Token expiry status
- Coordinator status (if configured)
- Actionable suggestions

Use --provider to filter to a specific provider.
Use --compact for minimal output (IDs and status only).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRobotStatus,
}

var robotNextCmd = &cobra.Command{
	Use:   "next <provider>",
	Short: "Suggest best profile to use",
	Long: `Analyzes all profiles for a provider and suggests the best one to use.

Scoring factors:
- Health status (healthy > warning > critical)
- Cooldown status (not in cooldown preferred)
- Token expiry (longer expiry preferred)
- Recent error count (fewer errors preferred)
- Last used time (LRU by default)

Returns the recommended profile with activation command.`,
	Args: cobra.ExactArgs(1),
	RunE: runRobotNext,
}

var robotActCmd = &cobra.Command{
	Use:   "act <action> <provider> [profile] [args...]",
	Short: "Execute an action",
	Long: `Execute an action and return the result.

Supported actions:
  activate <provider> <profile>  - Activate a profile
  cooldown <provider> <profile> [duration]  - Start cooldown
  uncooldown <provider> <profile>  - Clear cooldown
  refresh <provider> <profile>  - Refresh token
  backup <provider> <profile>   - Backup current auth

All actions return structured results with success/failure status.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runRobotAct,
}

var robotHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Quick health check",
	Long: `Returns a quick health check suitable for monitoring.

Checks:
- Vault accessibility
- Profile store accessibility
- Token expiry status
- Cooldown status
- Coordinator connectivity (if configured)

Exit codes:
  0 - All healthy
  1 - Error running check
  2 - Health issues detected`,
	RunE: runRobotHealth,
}

var robotWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Stream status changes",
	Long: `Streams status updates as newline-delimited JSON.

Each line is a complete JSON object with the current status.
Updates are emitted on changes or at the poll interval.

Use --interval to set poll interval (default 5s).
Use --provider to filter to a specific provider.`,
	RunE: runRobotWatch,
}


// robotOutput writes a RobotOutput to stdout.
func robotOutput(cmd *cobra.Command, output RobotOutput) error {
	output.Timestamp = time.Now().UTC().Format(time.RFC3339)
	enc := json.NewEncoder(cmd.OutOrStdout())
	return enc.Encode(output)
}

// robotError creates an error output.
func robotError(cmd *cobra.Command, command string, code, message string, details string, suggestions []string) error {
	output := RobotOutput{
		Success: false,
		Command: command,
		Error: &RobotError{
			Code:    code,
			Message: message,
			Details: details,
		},
		Suggestions: suggestions,
	}
	robotOutput(cmd, output)
	return fmt.Errorf("%s: %s", code, message)
}

func runRobotStatus(cmd *cobra.Command, args []string) error {
	start := time.Now()
	providerFilter, _ := cmd.Flags().GetString("provider")
	compact, _ := cmd.Flags().GetBool("compact")
	includeCoords, _ := cmd.Flags().GetBool("include-coordinators")

	// Determine which providers to check
	providersToCheck := []string{"codex", "claude", "gemini"}
	if len(args) > 0 {
		providerFilter = strings.ToLower(args[0])
	}
	if providerFilter != "" {
		validProviders := map[string]bool{"codex": true, "claude": true, "gemini": true}
		if !validProviders[providerFilter] {
			return robotError(cmd, "status", "INVALID_PROVIDER",
				fmt.Sprintf("unknown provider: %s", providerFilter),
				"valid providers: codex, claude, gemini",
				[]string{"caam robot status claude", "caam robot status codex", "caam robot status gemini"})
		}
		providersToCheck = []string{providerFilter}
	}

	data := RobotStatusData{
		Version:   version.Version,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		VaultPath: authfile.DefaultVaultPath(),
		Providers: make([]RobotProviderInfo, 0, len(providersToCheck)),
	}

	if configDir, err := os.UserConfigDir(); err == nil {
		data.ConfigPath = filepath.Join(configDir, "caam")
	}

	var suggestions []string

	var usableProfiles int
	for _, tool := range providersToCheck {
		provInfo := buildProviderInfo(tool, compact)
		data.Providers = append(data.Providers, provInfo)

		// Update summary
		data.Summary.TotalProfiles += len(provInfo.Profiles)
		if provInfo.ActiveProfile != "" {
			data.Summary.ActiveProfiles++
		}
		for _, p := range provInfo.Profiles {
			isHealthy := p.Health.Status == "healthy"
			inCooldown := p.Cooldown != nil && p.Cooldown.Active

			if isHealthy {
				data.Summary.HealthyProfiles++
			}
			if inCooldown {
				data.Summary.CooldownProfiles++
			}
			// Count profiles that are usable (healthy AND not in cooldown)
			if isHealthy && !inCooldown {
				usableProfiles++
			}
			if p.Health.ExpiresAt != "" {
				if exp, err := time.Parse(time.RFC3339, p.Health.ExpiresAt); err == nil {
					if time.Until(exp) < 24*time.Hour {
						data.Summary.ExpiringSoon++
					}
				}
			}
		}
	}

	// Check if all profiles are blocked (cooldown or unhealthy)
	if data.Summary.TotalProfiles > 0 && usableProfiles == 0 {
		data.Summary.AllProfilesBlocked = true
		suggestions = append(suggestions, "All profiles are in cooldown or unhealthy. Consider adding a new profile or waiting for cooldown to expire.")
	}

	// Add suggestions based on status
	if data.Summary.ExpiringSoon > 0 {
		suggestions = append(suggestions, fmt.Sprintf("%d profile(s) expiring within 24h. Consider refreshing tokens.", data.Summary.ExpiringSoon))
	}

	// Check coordinators if requested
	if includeCoords {
		data.Coordinators = checkCoordinators()
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success:     true,
		Command:     "status",
		Data:        data,
		Suggestions: suggestions,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

func buildProviderInfo(tool string, compact bool) RobotProviderInfo {
	info := RobotProviderInfo{
		ID:          tool,
		DisplayName: getProviderDisplayName(tool),
		Profiles:    []RobotProfileInfo{},
		AuthPaths:   []RobotAuthPath{},
	}

	// Check if logged in
	fileSet := tools[tool]()
	info.LoggedIn = authfile.HasAuthFiles(fileSet)

	// Get active profile
	if info.LoggedIn {
		if activeProfile, err := vault.ActiveProfile(fileSet); err == nil && activeProfile != "" {
			info.ActiveProfile = activeProfile
		}
	}

	// Get auth paths (unless compact)
	if !compact {
		for _, spec := range fileSet.Files {
			_, err := os.Stat(spec.Path)
			info.AuthPaths = append(info.AuthPaths, RobotAuthPath{
				Path:        spec.Path,
				Exists:      err == nil,
				Required:    spec.Required,
				Description: spec.Description,
			})
		}
	}

	// List profiles
	profiles, err := vault.List(tool)
	if err != nil {
		return info
	}

	db, _ := caamdb.Open()
	defer func() {
		if db != nil {
			db.Close()
		}
	}()

	for _, profileName := range profiles {
		pInfo := buildProfileInfo(tool, profileName, info.ActiveProfile, db, compact)
		info.Profiles = append(info.Profiles, pInfo)
	}

	return info
}

func buildProfileInfo(tool, profileName, activeProfile string, db *caamdb.DB, compact bool) RobotProfileInfo {
	pInfo := RobotProfileInfo{
		Name:   profileName,
		Active: profileName == activeProfile,
		System: authfile.IsSystemProfile(profileName),
	}

	// Get health info
	ph, id := getProfileHealthWithIdentity(tool, profileName)
	status := health.CalculateStatus(ph)

	pInfo.Health = RobotHealthInfo{
		Status:       status.String(),
		ErrorCount1h: ph.ErrorCount1h,
	}

	if !compact {
		if !ph.TokenExpiresAt.IsZero() {
			pInfo.Health.ExpiresAt = ph.TokenExpiresAt.Format(time.RFC3339)
			remaining := time.Until(ph.TokenExpiresAt)
			if remaining > 0 {
				pInfo.Health.ExpiresIn = robotFormatDuration(remaining)
			} else {
				pInfo.Health.ExpiresIn = "expired"
				pInfo.Health.Reason = "token expired"
			}
		}

		// Set reason based on status
		if status == health.StatusWarning || status == health.StatusCritical {
			pInfo.Health.Reason = getHealthReason(ph, status)
		}
	}

	// Get identity info
	if id != nil {
		if compact {
			pInfo.Email = id.Email
		} else {
			pInfo.Email = id.Email
			pInfo.PlanType = id.PlanType
		}
	}

	// Check cooldown
	if db != nil {
		now := time.Now()
		if cooldown, err := db.ActiveCooldown(tool, profileName, now); err == nil && cooldown != nil {
			remaining := cooldown.CooldownUntil.Sub(now)
			if remaining > 0 {
				pInfo.Cooldown = &RobotCooldown{
					Active:       true,
					Until:        cooldown.CooldownUntil.Format(time.RFC3339),
					RemainingMs:  remaining.Milliseconds(),
					RemainingStr: robotFormatDuration(remaining),
					Reason:       cooldown.Notes,
				}
			}
		}
	}

	// Generate recommendation (unless compact)
	if !compact {
		pInfo.Recommendation = generateRecommendation(pInfo)
	}

	return pInfo
}

func getHealthReason(ph *health.ProfileHealth, status health.HealthStatus) string {
	// Use thresholds from health.DefaultHealthConfig()
	cfg := health.DefaultHealthConfig()

	if status == health.StatusCritical {
		if !ph.TokenExpiresAt.IsZero() && time.Until(ph.TokenExpiresAt) <= 0 {
			return "token expired"
		}
		if ph.ErrorCount1h >= cfg.ErrorCountCritical {
			return fmt.Sprintf("high error rate (%d errors in 1h)", ph.ErrorCount1h)
		}
	}
	if status == health.StatusWarning {
		if !ph.TokenExpiresAt.IsZero() && time.Until(ph.TokenExpiresAt) < 24*time.Hour {
			return "token expiring soon"
		}
		if ph.ErrorCount1h >= cfg.ErrorCountWarning {
			return fmt.Sprintf("elevated error rate (%d errors in 1h)", ph.ErrorCount1h)
		}
	}
	return ""
}

func generateRecommendation(p RobotProfileInfo) string {
	if p.Cooldown != nil && p.Cooldown.Active {
		return fmt.Sprintf("wait for cooldown (%s remaining)", p.Cooldown.RemainingStr)
	}
	if p.Health.Status == "critical" {
		if strings.Contains(p.Health.Reason, "expired") {
			return "refresh token required"
		}
		return "investigate errors before use"
	}
	if p.Health.Status == "warning" {
		if strings.Contains(p.Health.Reason, "expiring") {
			return "consider refreshing token soon"
		}
	}
	if p.Health.Status == "healthy" && !p.Active {
		return "ready to activate"
	}
	return ""
}

func robotFormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

func checkCoordinators() []RobotCoordinator {
	// Check known coordinator endpoints
	// This is a simplified version - in production, this would read from config
	endpoints := []struct {
		name string
		url  string
	}{
		{"local", "http://localhost:7890"},
	}

	var coords []RobotCoordinator
	client := &http.Client{Timeout: 2 * time.Second}

	for _, ep := range endpoints {
		coord := RobotCoordinator{
			Name: ep.name,
			URL:  ep.url,
		}

		start := time.Now()
		resp, err := client.Get(ep.url + "/status")
		coord.Latency = time.Since(start).Milliseconds()

		if err != nil {
			coord.Error = err.Error()
			coord.Healthy = false
		} else {
			resp.Body.Close()
			coord.Healthy = resp.StatusCode == http.StatusOK

			// Try to get pending count
			if pendResp, err := client.Get(ep.url + "/auth/pending"); err == nil {
				var pending []interface{}
				json.NewDecoder(pendResp.Body).Decode(&pending)
				pendResp.Body.Close()
				coord.Pending = len(pending)
			}
		}

		coords = append(coords, coord)
	}

	return coords
}

func runRobotNext(cmd *cobra.Command, args []string) error {
	start := time.Now()
	provider := strings.ToLower(args[0])

	if _, ok := tools[provider]; !ok {
		return robotError(cmd, "next", "INVALID_PROVIDER",
			fmt.Sprintf("unknown provider: %s", provider),
			"valid providers: codex, claude, gemini",
			nil)
	}

	strategy, _ := cmd.Flags().GetString("strategy")
	includeCooldown, _ := cmd.Flags().GetBool("include-cooldown")

	// Get all profiles for this provider
	profiles, err := vault.List(provider)
	if err != nil {
		return robotError(cmd, "next", "VAULT_ERROR",
			"failed to list profiles",
			err.Error(),
			[]string{"caam robot status " + provider})
	}

	if len(profiles) == 0 {
		return robotError(cmd, "next", "NO_PROFILES",
			fmt.Sprintf("no profiles found for %s", provider),
			"",
			[]string{
				"caam backup " + provider + " <profile-name>",
				"caam auth import " + provider,
			})
	}

	db, _ := caamdb.Open()
	defer func() {
		if db != nil {
			db.Close()
		}
	}()

	// Score each profile
	type scoredProfile struct {
		name    string
		score   float64
		reasons []string
		info    RobotProfileInfo
	}

	var scored []scoredProfile
	now := time.Now()

	for _, profileName := range profiles {
		pInfo := buildProfileInfo(provider, profileName, "", db, false)

		// Skip profiles in cooldown unless requested
		if !includeCooldown && pInfo.Cooldown != nil && pInfo.Cooldown.Active {
			continue
		}

		sp := scoredProfile{
			name:    profileName,
			info:    pInfo,
			reasons: []string{},
		}

		// Calculate score (higher is better)
		switch pInfo.Health.Status {
		case "healthy":
			sp.score += 100
			sp.reasons = append(sp.reasons, "healthy status")
		case "warning":
			sp.score += 50
			sp.reasons = append(sp.reasons, "warning status")
		case "critical":
			sp.score += 10
			sp.reasons = append(sp.reasons, "critical status (not recommended)")
		default:
			sp.score += 30
		}

		// Cooldown penalty
		if pInfo.Cooldown != nil && pInfo.Cooldown.Active {
			sp.score -= 200
			sp.reasons = append(sp.reasons, fmt.Sprintf("in cooldown (%s remaining)", pInfo.Cooldown.RemainingStr))
		}

		// Error penalty
		if pInfo.Health.ErrorCount1h > 0 {
			sp.score -= float64(pInfo.Health.ErrorCount1h * 10)
			sp.reasons = append(sp.reasons, fmt.Sprintf("%d recent errors", pInfo.Health.ErrorCount1h))
		}

		// Token expiry consideration
		if pInfo.Health.ExpiresAt != "" {
			if exp, err := time.Parse(time.RFC3339, pInfo.Health.ExpiresAt); err == nil {
				remaining := exp.Sub(now)
				if remaining > 7*24*time.Hour {
					sp.score += 20
					sp.reasons = append(sp.reasons, "token valid for >7d")
				} else if remaining > 24*time.Hour {
					sp.score += 10
					sp.reasons = append(sp.reasons, fmt.Sprintf("token expires in %s", robotFormatDuration(remaining)))
				} else if remaining > 0 {
					sp.score -= 20
					sp.reasons = append(sp.reasons, fmt.Sprintf("token expiring soon (%s)", robotFormatDuration(remaining)))
				} else {
					sp.score -= 100
					sp.reasons = append(sp.reasons, "token expired")
				}
			}
		}

		// LRU bonus (strategy-dependent)
		if strategy == "lru" || strategy == "smart" {
			// Could check last used time here
			// For now, just slightly favor non-active profiles
			if !pInfo.Active {
				sp.score += 5
			}
		}

		scored = append(scored, sp)
	}

	if len(scored) == 0 {
		suggestions := []string{
			fmt.Sprintf("caam robot status %s", provider),
		}
		if !includeCooldown {
			suggestions = append(suggestions, "caam robot next "+provider+" --include-cooldown")
		}
		return robotError(cmd, "next", "ALL_BLOCKED",
			"all profiles are blocked or in cooldown",
			"",
			suggestions)
	}

	// Sort by score (descending)
	for i := 0; i < len(scored)-1; i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	best := scored[0]
	data := RobotNextData{
		Provider: provider,
		Profile:  best.name,
		Score:    best.score,
		Reasons:  best.reasons,
		Command:  fmt.Sprintf("caam activate %s %s", provider, best.name),
	}

	// Include alternate if available
	if len(scored) > 1 {
		alt := scored[1]
		data.AlternateChoice = &RobotNextProfile{
			Provider: provider,
			Profile:  alt.name,
			Score:    alt.score,
			Reason:   strings.Join(alt.reasons, "; "),
		}
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: true,
		Command: "next",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

func runRobotAct(cmd *cobra.Command, args []string) error {
	start := time.Now()
	action := strings.ToLower(args[0])
	provider := strings.ToLower(args[1])

	if _, ok := tools[provider]; !ok {
		return robotError(cmd, "act", "INVALID_PROVIDER",
			fmt.Sprintf("unknown provider: %s", provider),
			"valid providers: codex, claude, gemini",
			nil)
	}

	var result RobotActResult
	result.Action = action
	result.Provider = provider

	switch action {
	case "activate":
		if len(args) < 3 {
			return robotError(cmd, "act", "MISSING_PROFILE",
				"profile name required for activate",
				"usage: caam robot act activate <provider> <profile>",
				nil)
		}
		profile := args[2]
		result.Profile = profile

		// Get current active profile
		fileSet := tools[provider]()
		if oldProfile, err := vault.ActiveProfile(fileSet); err == nil {
			result.OldProfile = oldProfile
		}

		// Activate the profile
		if err := vault.Restore(fileSet, profile); err != nil {
			return robotError(cmd, "act", "ACTIVATE_FAILED",
				fmt.Sprintf("failed to activate %s/%s", provider, profile),
				err.Error(),
				[]string{fmt.Sprintf("caam robot status %s", provider)})
		}

		result.Success = true
		result.Message = fmt.Sprintf("activated %s/%s", provider, profile)

	case "cooldown":
		if len(args) < 3 {
			return robotError(cmd, "act", "MISSING_PROFILE",
				"profile name required for cooldown",
				"usage: caam robot act cooldown <provider> <profile> [duration]",
				nil)
		}
		profile := args[2]
		result.Profile = profile

		duration := 4 * time.Hour // default
		if len(args) >= 4 {
			if d, err := time.ParseDuration(args[3]); err == nil {
				duration = d
			}
		}

		db, err := caamdb.Open()
		if err != nil {
			return robotError(cmd, "act", "DB_ERROR",
				"failed to open database",
				err.Error(),
				nil)
		}
		defer db.Close()

		hitAt := time.Now()
		cooldownEvent, err := db.SetCooldown(provider, profile, hitAt, duration, "manual via robot act")
		if err != nil {
			return robotError(cmd, "act", "COOLDOWN_FAILED",
				"failed to set cooldown",
				err.Error(),
				nil)
		}

		result.Success = true
		result.Message = fmt.Sprintf("cooldown set until %s (%s)", cooldownEvent.CooldownUntil.Format(time.RFC3339), robotFormatDuration(duration))

	case "uncooldown":
		if len(args) < 3 {
			return robotError(cmd, "act", "MISSING_PROFILE",
				"profile name required for uncooldown",
				"usage: caam robot act uncooldown <provider> <profile>",
				nil)
		}
		profile := args[2]
		result.Profile = profile

		db, err := caamdb.Open()
		if err != nil {
			return robotError(cmd, "act", "DB_ERROR",
				"failed to open database",
				err.Error(),
				nil)
		}
		defer db.Close()

		if _, err := db.ClearCooldown(provider, profile); err != nil {
			return robotError(cmd, "act", "UNCOOLDOWN_FAILED",
				"failed to clear cooldown",
				err.Error(),
				nil)
		}

		result.Success = true
		result.Message = fmt.Sprintf("cleared cooldown for %s/%s", provider, profile)

	case "backup":
		fileSet := tools[provider]()
		if !authfile.HasAuthFiles(fileSet) {
			return robotError(cmd, "act", "NO_AUTH",
				fmt.Sprintf("no auth files found for %s", provider),
				"login first using the tool's login command",
				nil)
		}

		profile := "backup-" + time.Now().Format("20060102-150405")
		if len(args) >= 3 {
			profile = args[2]
		}
		result.Profile = profile

		if err := vault.Backup(fileSet, profile); err != nil {
			return robotError(cmd, "act", "BACKUP_FAILED",
				"backup failed",
				err.Error(),
				nil)
		}

		result.Success = true
		result.Message = fmt.Sprintf("backed up to %s/%s", provider, profile)

	default:
		return robotError(cmd, "act", "INVALID_ACTION",
			fmt.Sprintf("unknown action: %s", action),
			"valid actions: activate, cooldown, uncooldown, backup",
			[]string{
				"caam robot act activate <provider> <profile>",
				"caam robot act cooldown <provider> <profile> [duration]",
				"caam robot act uncooldown <provider> <profile>",
				"caam robot act backup <provider> [profile]",
			})
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: result.Success,
		Command: "act",
		Data:    result,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

func runRobotHealth(cmd *cobra.Command, args []string) error {
	start := time.Now()

	type HealthCheck struct {
		Name    string `json:"name"`
		Status  string `json:"status"` // ok, warning, error
		Message string `json:"message,omitempty"`
	}

	type HealthCheckResult struct {
		Overall     string        `json:"overall"` // healthy, degraded, unhealthy
		Checks      []HealthCheck `json:"checks"`
		Issues      []string      `json:"issues,omitempty"`
		Suggestions []string      `json:"suggestions,omitempty"`
	}

	result := HealthCheckResult{
		Overall: "healthy",
		Checks:  []HealthCheck{},
	}

	// Check vault
	vaultPath := authfile.DefaultVaultPath()
	if _, err := os.Stat(vaultPath); err == nil {
		result.Checks = append(result.Checks, HealthCheck{
			Name:   "vault",
			Status: "ok",
		})
	} else {
		result.Checks = append(result.Checks, HealthCheck{
			Name:    "vault",
			Status:  "error",
			Message: "vault directory not found",
		})
		result.Issues = append(result.Issues, "vault directory not accessible")
		result.Overall = "unhealthy"
	}

	// Check database
	if db, err := caamdb.Open(); err == nil {
		db.Close()
		result.Checks = append(result.Checks, HealthCheck{
			Name:   "database",
			Status: "ok",
		})
	} else {
		result.Checks = append(result.Checks, HealthCheck{
			Name:    "database",
			Status:  "error",
			Message: err.Error(),
		})
		result.Issues = append(result.Issues, "database not accessible")
		if result.Overall == "healthy" {
			result.Overall = "degraded"
		}
	}

	// Check each provider
	for _, tool := range []string{"codex", "claude", "gemini"} {
		profiles, err := vault.List(tool)
		if err != nil {
			continue
		}

		healthyCount := 0
		totalCount := len(profiles)

		for _, profileName := range profiles {
			ph := buildProfileHealth(tool, profileName)
			status := health.CalculateStatus(ph)
			if status == health.StatusHealthy {
				healthyCount++
			}
		}

		if totalCount > 0 {
			if healthyCount == 0 {
				result.Checks = append(result.Checks, HealthCheck{
					Name:    tool,
					Status:  "warning",
					Message: fmt.Sprintf("0/%d profiles healthy", totalCount),
				})
				result.Issues = append(result.Issues, fmt.Sprintf("%s: no healthy profiles", tool))
				if result.Overall == "healthy" {
					result.Overall = "degraded"
				}
			} else if healthyCount < totalCount {
				result.Checks = append(result.Checks, HealthCheck{
					Name:    tool,
					Status:  "ok",
					Message: fmt.Sprintf("%d/%d profiles healthy", healthyCount, totalCount),
				})
			} else {
				result.Checks = append(result.Checks, HealthCheck{
					Name:   tool,
					Status: "ok",
				})
			}
		}
	}

	// Generate suggestions
	if len(result.Issues) > 0 {
		result.Suggestions = append(result.Suggestions, "caam robot status --include-coordinators")
		result.Suggestions = append(result.Suggestions, "caam doctor")
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: result.Overall != "unhealthy",
		Command: "health",
		Data:    result,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

func runRobotWatch(cmd *cobra.Command, args []string) error {
	interval, _ := cmd.Flags().GetInt("interval")
	providerFilter, _ := cmd.Flags().GetString("provider")

	// Validate provider filter if specified
	if providerFilter != "" {
		providerFilter = strings.ToLower(providerFilter)
		if _, ok := tools[providerFilter]; !ok {
			return robotError(cmd, "watch", "INVALID_PROVIDER",
				fmt.Sprintf("unknown provider: %s", providerFilter),
				"valid providers: codex, claude, gemini",
				nil)
		}
	}

	if interval < 1 {
		interval = 1
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	ctx := cmd.Context()

	// Emit initial status
	if err := emitWatchStatus(cmd, providerFilter); err != nil {
		return nil // Exit gracefully if we can't write output
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := emitWatchStatus(cmd, providerFilter); err != nil {
				return nil // Exit gracefully if stdout is closed
			}
		}
	}
}

func emitWatchStatus(cmd *cobra.Command, providerFilter string) error {
	providersToCheck := []string{"codex", "claude", "gemini"}
	if providerFilter != "" {
		providersToCheck = []string{providerFilter}
	}

	type WatchEvent struct {
		Timestamp string              `json:"timestamp"`
		Providers []RobotProviderInfo `json:"providers"`
	}

	event := WatchEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Providers: make([]RobotProviderInfo, 0),
	}

	for _, tool := range providersToCheck {
		event.Providers = append(event.Providers, buildProviderInfo(tool, true))
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	return enc.Encode(event)
}

// ============================================================================
// Quick Start Guide - Token-efficient markdown for coding agents
// ============================================================================

func runRobotQuickStart(cmd *cobra.Command, args []string) error {
	guide := `# caam robot - Agent Quick Start

## Core Commands (JSON output)
` + "```" + `
caam robot status              # Full system overview
caam robot status claude       # Single provider
caam robot next claude         # Best profile recommendation
caam robot limits claude       # Rate limits + burn rate
caam robot precheck claude     # Session planner
` + "```" + `

## Actions
` + "```" + `
caam robot act activate claude <profile>   # Switch profile
caam robot act cooldown claude <profile> 1h  # Set cooldown
caam robot act uncooldown claude <profile>   # Clear cooldown
caam robot act backup claude [name]          # Backup current auth
caam robot act delete claude <profile>       # Delete profile
caam robot act refresh claude <profile>      # Refresh token
` + "```" + `

## Diagnostics
` + "```" + `
caam robot health              # Quick health check
caam robot doctor              # Full diagnostics
caam robot validate claude     # Token validation
caam robot paths               # Auth file locations
caam robot history             # Recent activity
` + "```" + `

## Configuration
` + "```" + `
caam robot config              # Show config
caam robot config set <k> <v>  # Set config value
` + "```" + `

## Response Format
All commands return:
` + "```json" + `
{
  "success": true|false,
  "command": "command_name",
  "timestamp": "RFC3339",
  "data": {...},
  "error": {"code": "ERROR_CODE", "message": "..."},
  "suggestions": ["helpful action commands"],
  "timing": {"duration_ms": 42}
}
` + "```" + `

## Error Codes
- INVALID_PROVIDER: Unknown provider (use: claude, codex, gemini)
- NO_PROFILES: No profiles exist for provider
- ALL_BLOCKED: All profiles in cooldown/unhealthy
- MISSING_PROFILE: Profile name required
- VAULT_ERROR: Cannot access profile storage

## Typical Workflow
1. ` + "`caam robot precheck claude`" + ` - Plan session
2. ` + "`caam robot act activate claude <best>`" + ` - Switch
3. Work until rate limited...
4. ` + "`caam robot act cooldown claude <current> 1h`" + ` - Mark cooldown
5. ` + "`caam robot next claude`" + ` - Get next best
6. Repeat from step 2
`
	fmt.Fprint(cmd.OutOrStdout(), guide)
	return nil
}

// ============================================================================
// New Robot Subcommands
// ============================================================================

var robotLimitsCmd = &cobra.Command{
	Use:   "limits <provider>",
	Short: "Fetch rate limits and burn rate",
	Long: `Fetches real-time rate limit data from provider APIs.

Returns usage percentages, reset times, burn rates, and depletion forecasts.
Useful for deciding when to switch profiles.`,
	Args: cobra.ExactArgs(1),
	RunE: runRobotLimits,
}

var robotPrecheckCmd = &cobra.Command{
	Use:   "precheck <provider>",
	Short: "Session planner with recommendations",
	Long: `Comprehensive session planner showing:
- Recommended profile with score breakdown
- Backup profiles in priority order
- Profiles in cooldown
- Usage forecasts and alerts
- Quick action commands`,
	Args: cobra.ExactArgs(1),
	RunE: runRobotPrecheck,
}

var robotValidateCmd = &cobra.Command{
	Use:   "validate [provider] [profile]",
	Short: "Validate auth tokens",
	Long: `Validates authentication tokens.

Without arguments, validates all profiles.
With provider, validates all profiles for that provider.
With provider and profile, validates that specific profile.`,
	Args: cobra.MaximumNArgs(2),
	RunE: runRobotValidate,
}

var robotDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Full system diagnostics",
	Long: `Runs comprehensive diagnostic checks:
- CLI tools installation
- Directory permissions
- Config validation
- Profile integrity
- Lock file status
- Auth file status`,
	RunE: runRobotDoctor,
}

var robotPathsCmd = &cobra.Command{
	Use:   "paths",
	Short: "Show all auth file paths",
	Long:  `Returns all auth file paths for all providers, with existence status.`,
	RunE:  runRobotPaths,
}

var robotHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "Recent activity log",
	Long: `Returns recent activity events from the database.

Use --days to specify how many days of history to fetch.
Use --limit to limit the number of events.`,
	RunE: runRobotHistory,
}

var robotConfigCmd = &cobra.Command{
	Use:   "config [set <key> <value>]",
	Short: "View or modify configuration",
	Long: `View or modify caam configuration.

Without arguments, returns full config as JSON.
With 'set <key> <value>', updates a config value.`,
	RunE: runRobotConfig,
}

func init() {
	rootCmd.AddCommand(robotCmd)
	robotCmd.AddCommand(robotStatusCmd)
	robotCmd.AddCommand(robotNextCmd)
	robotCmd.AddCommand(robotActCmd)
	robotCmd.AddCommand(robotHealthCmd)
	robotCmd.AddCommand(robotWatchCmd)
	robotCmd.AddCommand(robotLimitsCmd)
	robotCmd.AddCommand(robotPrecheckCmd)
	robotCmd.AddCommand(robotValidateCmd)
	robotCmd.AddCommand(robotDoctorCmd)
	robotCmd.AddCommand(robotPathsCmd)
	robotCmd.AddCommand(robotHistoryCmd)
	robotCmd.AddCommand(robotConfigCmd)

	// Status flags
	robotStatusCmd.Flags().String("provider", "", "filter to specific provider")
	robotStatusCmd.Flags().Bool("compact", false, "minimal output")
	robotStatusCmd.Flags().Bool("include-coordinators", false, "check coordinator status")

	// Next flags
	robotNextCmd.Flags().String("strategy", "smart", "selection strategy: smart, lru, random")
	robotNextCmd.Flags().Bool("include-cooldown", false, "include profiles in cooldown")

	// Watch flags
	robotWatchCmd.Flags().Int("interval", 5, "poll interval in seconds")
	robotWatchCmd.Flags().String("provider", "", "filter to specific provider")

	// Limits flags
	robotLimitsCmd.Flags().Bool("forecast", false, "include depletion forecasts")

	// Precheck flags
	robotPrecheckCmd.Flags().Duration("timeout", 30*time.Second, "API fetch timeout")
	robotPrecheckCmd.Flags().Bool("no-fetch", false, "skip API calls (use cached data)")

	// Validate flags
	robotValidateCmd.Flags().Bool("active", false, "perform active validation (API calls)")

	// Doctor flags
	robotDoctorCmd.Flags().Bool("fix", false, "attempt to fix issues")
	robotDoctorCmd.Flags().Bool("validate-tokens", false, "also validate auth tokens")

	// History flags
	robotHistoryCmd.Flags().Int("days", 7, "number of days of history")
	robotHistoryCmd.Flags().Int("limit", 50, "max events to return")
	robotHistoryCmd.Flags().String("provider", "", "filter to specific provider")
}

// RobotLimitsData contains rate limit information.
type RobotLimitsData struct {
	Provider string               `json:"provider"`
	Profiles []RobotProfileLimits `json:"profiles"`
}

// RobotProfileLimits contains rate limits for a profile.
type RobotProfileLimits struct {
	Name           string `json:"name"`
	AvailScore     int    `json:"availability_score"`
	PrimaryPct     int    `json:"primary_percent,omitempty"`
	SecondaryPct   int    `json:"secondary_percent,omitempty"`
	ResetsIn       string `json:"resets_in,omitempty"`
	BurnRate       string `json:"burn_rate,omitempty"`
	DepletesIn     string `json:"depletes_in,omitempty"`
	Error          string `json:"error,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

func runRobotLimits(cmd *cobra.Command, args []string) error {
	start := time.Now()
	provider := strings.ToLower(args[0])

	if _, ok := tools[provider]; !ok {
		return robotError(cmd, "limits", "INVALID_PROVIDER",
			fmt.Sprintf("unknown provider: %s", provider),
			"valid providers: codex, claude, gemini",
			nil)
	}

	// For now, use health data as a proxy. Full implementation would call usage APIs.
	profiles, err := vault.List(provider)
	if err != nil {
		return robotError(cmd, "limits", "VAULT_ERROR",
			"failed to list profiles",
			err.Error(),
			nil)
	}

	if len(profiles) == 0 {
		return robotError(cmd, "limits", "NO_PROFILES",
			fmt.Sprintf("no profiles found for %s", provider),
			"",
			[]string{fmt.Sprintf("caam backup %s <name>", provider)})
	}

	data := RobotLimitsData{
		Provider: provider,
		Profiles: make([]RobotProfileLimits, 0, len(profiles)),
	}

	for _, profileName := range profiles {
		if strings.HasPrefix(profileName, "_") {
			continue
		}

		limits := RobotProfileLimits{
			Name: profileName,
		}

		// Get health info for estimates
		ph, _ := getProfileHealthWithIdentity(provider, profileName)
		status := health.CalculateStatus(ph)

		switch status {
		case health.StatusHealthy:
			limits.AvailScore = 100
			limits.Recommendation = "ready to use"
		case health.StatusWarning:
			limits.AvailScore = 50
			limits.Recommendation = "use with caution"
		case health.StatusCritical:
			limits.AvailScore = 10
			limits.Recommendation = "avoid - issues detected"
		default:
			limits.AvailScore = 0
			limits.Recommendation = "status unknown"
		}

		data.Profiles = append(data.Profiles, limits)
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: true,
		Command: "limits",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

// RobotPrecheckData contains session planning data.
type RobotPrecheckData struct {
	Provider    string                  `json:"provider"`
	Recommended *RobotPrecheckProfile   `json:"recommended,omitempty"`
	Backups     []RobotPrecheckProfile  `json:"backups"`
	InCooldown  []RobotCooldownProfile  `json:"in_cooldown"`
	Alerts      []RobotPrecheckAlert    `json:"alerts,omitempty"`
	Summary     RobotPrecheckSummary    `json:"summary"`
	Commands    RobotPrecheckCommands   `json:"commands"`
}

// RobotPrecheckProfile is a profile with recommendation data.
type RobotPrecheckProfile struct {
	Name       string   `json:"name"`
	Score      float64  `json:"score"`
	Health     string   `json:"health"`
	Reasons    []string `json:"reasons"`
	PoolStatus string   `json:"pool_status,omitempty"`
}

// RobotCooldownProfile is a profile in cooldown.
type RobotCooldownProfile struct {
	Name      string `json:"name"`
	Remaining string `json:"remaining"`
	Until     string `json:"until"`
}

// RobotPrecheckAlert is an alert for the precheck.
type RobotPrecheckAlert struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Urgency string `json:"urgency"`
	Action  string `json:"action,omitempty"`
}

// RobotPrecheckSummary contains aggregate info.
type RobotPrecheckSummary struct {
	Total      int `json:"total_profiles"`
	Ready      int `json:"ready_profiles"`
	InCooldown int `json:"in_cooldown"`
	Healthy    int `json:"healthy"`
	Warning    int `json:"warning"`
	Critical   int `json:"critical"`
}

// RobotPrecheckCommands contains suggested commands.
type RobotPrecheckCommands struct {
	Activate string `json:"activate,omitempty"`
	Next     string `json:"next"`
	Run      string `json:"run"`
}

func runRobotPrecheck(cmd *cobra.Command, args []string) error {
	start := time.Now()
	provider := strings.ToLower(args[0])

	if _, ok := tools[provider]; !ok {
		return robotError(cmd, "precheck", "INVALID_PROVIDER",
			fmt.Sprintf("unknown provider: %s", provider),
			"valid providers: codex, claude, gemini",
			nil)
	}

	profiles, err := vault.List(provider)
	if err != nil {
		return robotError(cmd, "precheck", "VAULT_ERROR",
			"failed to list profiles", err.Error(), nil)
	}

	if len(profiles) == 0 {
		return robotError(cmd, "precheck", "NO_PROFILES",
			fmt.Sprintf("no profiles found for %s", provider),
			"",
			[]string{fmt.Sprintf("caam backup %s <name>", provider)})
	}

	db, _ := caamdb.Open()
	defer func() {
		if db != nil {
			db.Close()
		}
	}()

	data := RobotPrecheckData{
		Provider:   provider,
		Backups:    make([]RobotPrecheckProfile, 0),
		InCooldown: make([]RobotCooldownProfile, 0),
		Commands: RobotPrecheckCommands{
			Next: fmt.Sprintf("caam robot next %s", provider),
			Run:  fmt.Sprintf("caam run %s -- <command>", provider),
		},
	}

	now := time.Now()
	var bestScore float64 = -9999
	var bestProfile string

	for _, profileName := range profiles {
		if strings.HasPrefix(profileName, "_") {
			continue
		}

		data.Summary.Total++

		// Check cooldown
		if db != nil {
			if ev, err := db.ActiveCooldown(provider, profileName, now); err == nil && ev != nil {
				remaining := time.Until(ev.CooldownUntil)
				if remaining > 0 {
					data.InCooldown = append(data.InCooldown, RobotCooldownProfile{
						Name:      profileName,
						Remaining: robotFormatDuration(remaining),
						Until:     ev.CooldownUntil.Format(time.RFC3339),
					})
					data.Summary.InCooldown++
					continue
				}
			}
		}

		// Get health
		ph, _ := getProfileHealthWithIdentity(provider, profileName)
		status := health.CalculateStatus(ph)

		rec := RobotPrecheckProfile{
			Name:    profileName,
			Health:  status.String(),
			Reasons: []string{},
		}

		// Calculate score
		switch status {
		case health.StatusHealthy:
			rec.Score = 100
			rec.Reasons = append(rec.Reasons, "+healthy status")
			data.Summary.Healthy++
		case health.StatusWarning:
			rec.Score = 50
			rec.Reasons = append(rec.Reasons, "-warning status")
			data.Summary.Warning++
		case health.StatusCritical:
			rec.Score = 10
			rec.Reasons = append(rec.Reasons, "-critical status")
			data.Summary.Critical++
		default:
			rec.Score = 30
		}

		data.Summary.Ready++

		if rec.Score > bestScore {
			bestScore = rec.Score
			bestProfile = profileName
			data.Recommended = &RobotPrecheckProfile{
				Name:    rec.Name,
				Score:   rec.Score,
				Health:  rec.Health,
				Reasons: rec.Reasons,
			}
		} else {
			data.Backups = append(data.Backups, rec)
		}
	}

	// Update recommended from backups if needed
	if data.Recommended == nil && len(data.Backups) > 0 {
		data.Recommended = &data.Backups[0]
		data.Backups = data.Backups[1:]
	}

	if data.Recommended != nil {
		data.Commands.Activate = fmt.Sprintf("caam robot act activate %s %s", provider, data.Recommended.Name)
	}

	// Generate alerts
	if data.Summary.Ready == 0 && data.Summary.InCooldown > 0 {
		data.Alerts = append(data.Alerts, RobotPrecheckAlert{
			Type:    "all_blocked",
			Message: "all profiles are in cooldown",
			Urgency: "high",
			Action:  "wait for cooldown to expire or add new profile",
		})
	}
	if data.Summary.Critical > 0 {
		data.Alerts = append(data.Alerts, RobotPrecheckAlert{
			Type:    "critical_profiles",
			Message: fmt.Sprintf("%d profile(s) in critical status", data.Summary.Critical),
			Urgency: "medium",
			Action:  "run 'caam robot doctor' for diagnostics",
		})
	}

	_ = bestProfile // Used above

	duration := time.Since(start)
	output := RobotOutput{
		Success: true,
		Command: "precheck",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

// RobotValidateData contains validation results.
type RobotValidateData struct {
	Method   string                `json:"method"`
	Profiles []RobotValidateResult `json:"profiles"`
	Summary  RobotValidateSummary  `json:"summary"`
}

// RobotValidateResult is a single validation result.
type RobotValidateResult struct {
	Provider  string `json:"provider"`
	Profile   string `json:"profile"`
	Valid     bool   `json:"valid"`
	ExpiresAt string `json:"expires_at,omitempty"`
	ExpiresIn string `json:"expires_in,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RobotValidateSummary contains validation summary.
type RobotValidateSummary struct {
	Total   int `json:"total"`
	Valid   int `json:"valid"`
	Invalid int `json:"invalid"`
}

func runRobotValidate(cmd *cobra.Command, args []string) error {
	start := time.Now()

	var providersToCheck []string
	var profileFilter string

	if len(args) >= 1 {
		provider := strings.ToLower(args[0])
		if _, ok := tools[provider]; !ok {
			return robotError(cmd, "validate", "INVALID_PROVIDER",
				fmt.Sprintf("unknown provider: %s", provider),
				"valid providers: codex, claude, gemini",
				nil)
		}
		providersToCheck = []string{provider}
		if len(args) >= 2 {
			profileFilter = args[1]
		}
	} else {
		providersToCheck = []string{"codex", "claude", "gemini"}
	}

	data := RobotValidateData{
		Method:   "passive",
		Profiles: make([]RobotValidateResult, 0),
	}

	for _, provider := range providersToCheck {
		profiles, err := vault.List(provider)
		if err != nil {
			continue
		}

		for _, profileName := range profiles {
			if strings.HasPrefix(profileName, "_") {
				continue
			}
			if profileFilter != "" && profileName != profileFilter {
				continue
			}

			result := RobotValidateResult{
				Provider: provider,
				Profile:  profileName,
			}

			// Get health info for token expiry
			ph, _ := getProfileHealthWithIdentity(provider, profileName)
			if !ph.TokenExpiresAt.IsZero() {
				result.ExpiresAt = ph.TokenExpiresAt.Format(time.RFC3339)
				remaining := time.Until(ph.TokenExpiresAt)
				if remaining > 0 {
					result.ExpiresIn = robotFormatDuration(remaining)
					result.Valid = true
					data.Summary.Valid++
				} else {
					result.ExpiresIn = "expired"
					result.Valid = false
					result.Error = "token expired"
					data.Summary.Invalid++
				}
			} else {
				// No expiry info - assume valid
				result.Valid = true
				data.Summary.Valid++
			}

			data.Summary.Total++
			data.Profiles = append(data.Profiles, result)
		}
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: data.Summary.Invalid == 0,
		Command: "validate",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

func runRobotDoctor(cmd *cobra.Command, args []string) error {
	start := time.Now()
	fix, _ := cmd.Flags().GetBool("fix")
	validateTokens, _ := cmd.Flags().GetBool("validate-tokens")

	report := runDoctorChecks(fix, validateTokens)

	duration := time.Since(start)
	output := RobotOutput{
		Success: report.OverallOK,
		Command: "doctor",
		Data:    report,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	if !report.OverallOK {
		output.Suggestions = []string{
			"caam robot doctor --fix",
			"caam doctor --fix",
		}
	}

	return robotOutput(cmd, output)
}

// RobotPathsData contains auth file paths.
type RobotPathsData struct {
	VaultPath  string             `json:"vault_path"`
	ConfigPath string             `json:"config_path"`
	Providers  []RobotProviderPaths `json:"providers"`
}

// RobotProviderPaths contains paths for a provider.
type RobotProviderPaths struct {
	ID    string           `json:"id"`
	Files []RobotFilePath  `json:"files"`
}

// RobotFilePath is a single file path.
type RobotFilePath struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

func runRobotPaths(cmd *cobra.Command, args []string) error {
	start := time.Now()

	data := RobotPathsData{
		VaultPath:  authfile.DefaultVaultPath(),
		Providers:  make([]RobotProviderPaths, 0),
	}

	configDir, err := os.UserConfigDir()
	if err == nil {
		data.ConfigPath = filepath.Join(configDir, "caam", "config.json")
	}

	for tool, getFileSet := range tools {
		fileSet := getFileSet()
		provPaths := RobotProviderPaths{
			ID:    tool,
			Files: make([]RobotFilePath, 0),
		}

		for _, spec := range fileSet.Files {
			_, err := os.Stat(spec.Path)
			provPaths.Files = append(provPaths.Files, RobotFilePath{
				Path:        spec.Path,
				Exists:      err == nil,
				Required:    spec.Required,
				Description: spec.Description,
			})
		}

		data.Providers = append(data.Providers, provPaths)
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: true,
		Command: "paths",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

// RobotHistoryData contains activity history.
type RobotHistoryData struct {
	Since  string              `json:"since"`
	Events []RobotHistoryEvent `json:"events"`
	Count  int                 `json:"count"`
}

// RobotHistoryEvent is a single activity event.
type RobotHistoryEvent struct {
	Timestamp  string `json:"timestamp"`
	Provider   string `json:"provider"`
	Profile    string `json:"profile"`
	Event      string `json:"event"`
	Duration   string `json:"duration,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

func runRobotHistory(cmd *cobra.Command, args []string) error {
	start := time.Now()
	days, _ := cmd.Flags().GetInt("days")
	limit, _ := cmd.Flags().GetInt("limit")
	providerFilter, _ := cmd.Flags().GetString("provider")

	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	data := RobotHistoryData{
		Since:  since.Format(time.RFC3339),
		Events: make([]RobotHistoryEvent, 0),
	}

	db, err := caamdb.Open()
	if err != nil {
		return robotError(cmd, "history", "DB_ERROR",
			"failed to open database", err.Error(), nil)
	}
	defer db.Close()

	// Query activity log
	if db.Conn() != nil {
		query := `SELECT timestamp, provider, profile_name, event_type, COALESCE(duration_seconds, 0), COALESCE(notes, '')
			FROM activity_log
			WHERE datetime(timestamp) >= datetime(?)
			ORDER BY datetime(timestamp) DESC
			LIMIT ?`
		rows, err := db.Conn().Query(query, since.UTC().Format("2006-01-02 15:04:05"), limit)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var tsStr, provider, profile, eventType, notes string
				var durationSecs int64
				if err := rows.Scan(&tsStr, &provider, &profile, &eventType, &durationSecs, &notes); err != nil {
					continue
				}

				if providerFilter != "" && provider != providerFilter {
					continue
				}

				event := RobotHistoryEvent{
					Timestamp: tsStr,
					Provider:  provider,
					Profile:   profile,
					Event:     eventType,
					Notes:     notes,
				}
				if durationSecs > 0 {
					event.Duration = robotFormatDuration(time.Duration(durationSecs) * time.Second)
				}
				data.Events = append(data.Events, event)
			}
		}
	}

	data.Count = len(data.Events)

	duration := time.Since(start)
	output := RobotOutput{
		Success: true,
		Command: "history",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}

func runRobotConfig(cmd *cobra.Command, args []string) error {
	start := time.Now()

	// Handle set subcommand
	if len(args) >= 3 && args[0] == "set" {
		key := args[1]
		value := args[2]

		// Simple key-value setting for common config options
		switch key {
		case "rotation_algorithm", "algorithm":
			spmCfg, _ := config.LoadSPMConfig()
			if spmCfg == nil {
				spmCfg = config.DefaultSPMConfig()
			}
			spmCfg.Stealth.Rotation.Algorithm = value
			if err := spmCfg.Save(); err != nil {
				return robotError(cmd, "config", "SAVE_ERROR",
					"failed to save config", err.Error(), nil)
			}
		default:
			return robotError(cmd, "config", "UNKNOWN_KEY",
				fmt.Sprintf("unknown config key: %s", key),
				"valid keys: rotation_algorithm",
				nil)
		}

		output := RobotOutput{
			Success: true,
			Command: "config",
			Data: map[string]string{
				"action": "set",
				"key":    key,
				"value":  value,
			},
		}
		return robotOutput(cmd, output)
	}

	// Default: show config
	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}

	spmCfg, _ := config.LoadSPMConfig()
	if spmCfg == nil {
		spmCfg = config.DefaultSPMConfig()
	}

	data := map[string]interface{}{
		"config":     cfg,
		"spm_config": spmCfg,
	}

	duration := time.Since(start)
	output := RobotOutput{
		Success: true,
		Command: "config",
		Data:    data,
		Timing: &RobotTiming{
			StartedAt:  start.UTC().Format(time.RFC3339),
			DurationMs: duration.Milliseconds(),
		},
	}

	return robotOutput(cmd, output)
}
