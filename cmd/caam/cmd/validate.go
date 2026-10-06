// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

var validateCmd = &cobra.Command{
	Use:   "validate [tool] [profile]",
	Short: "Validate authentication tokens",
	Long: `Check saved authentication credentials and their known expiry.

By default, performs passive validation (no network calls):
  - Check auth file existence
  - Check token format/structure
  - Check expiry timestamps

Provider probing is not supported for saved vault profiles. Requests with
--active fail explicitly; passive results do not prove provider acceptance.

Examples:
  caam validate                    # Validate all profiles (passive)
  caam validate claude             # Validate all Claude profiles
  caam validate claude work        # Validate specific profile
  caam validate claude work --json # JSON output`,
	Args: cobra.MaximumNArgs(2),
	RunE: runValidate,
}

var (
	validateActive bool
	validateJSON   bool
	validateAll    bool
)

func init() {
	validateCmd.Flags().BoolVar(&validateActive, "active", false, "Request provider verification (unsupported for saved vault profiles)")
	validateCmd.Flags().BoolVar(&validateJSON, "json", false, "Output in JSON format")
	validateCmd.Flags().BoolVar(&validateAll, "all", false, "Validate all profiles (default behavior)")
	rootCmd.AddCommand(validateCmd)
}

// ValidationOutput represents the JSON output for validation results.
type ValidationOutput struct {
	Provider       string    `json:"provider"`
	Profile        string    `json:"profile"`
	Valid          bool      `json:"valid"`
	Method         string    `json:"method"`
	ExpiresAt      string    `json:"expires_at,omitempty"`
	Error          string    `json:"error,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
	Recommendation string    `json:"recommendation,omitempty"`
	health.Signals
}

func runValidate(cmd *cobra.Command, args []string) error {
	// Ensure the vault is initialized (PersistentPreRunE normally does this, but
	// keep validate robust when invoked directly, e.g. in tests).
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	// validate operates on the SAME saved-profile source of truth as backup,
	// activate, and ls: the vault. Previously it read the isolated profile.Store,
	// so it reported missing/invalid credentials for normal vault-backed profiles
	// (issue #23).
	var toolFilter, profileFilter string
	switch len(args) {
	case 1:
		toolFilter = args[0]
	case 2:
		toolFilter = args[0]
		profileFilter = args[1]
	}

	if toolFilter != "" {
		if _, ok := tools[toolFilter]; !ok {
			return fmt.Errorf("unknown tool: %s (supported: %s)", toolFilter, supportedToolsList())
		}
	}

	// A passive result cannot satisfy a request to verify with the provider.
	if validateActive {
		return fmt.Errorf("active validation is not supported for saved vault profiles; omit --active for passive credential checks")
	}

	results := []ValidationOutput{}

	for _, tool := range supportedTools() {
		if toolFilter != "" && tool != toolFilter {
			continue
		}

		profiles, err := vault.List(tool)
		if err != nil {
			return fmt.Errorf("list %s profiles: %w", tool, err)
		}
		sort.Strings(profiles)

		for _, profileName := range profiles {
			if authfile.IsSystemProfile(profileName) {
				continue // Skip _original / _backup_* system profiles
			}
			if profileFilter != "" && profileName != profileFilter {
				continue
			}
			result, _ := validateVaultProfile(tool, profileName)
			results = append(results, result)
		}
	}
	if profileFilter != "" && len(results) == 0 {
		return fmt.Errorf("saved profile %s/%s not found", toolFilter, profileFilter)
	}

	// Output results. Encode empty results as [] (not null) for agent parsing.
	var outputErr error
	if validateJSON {
		outputErr = outputJSON(results)
	} else {
		outputErr = outputHuman(results)
	}
	if outputErr != nil {
		return outputErr
	}
	for _, result := range results {
		if !result.Valid {
			return fmt.Errorf("one or more saved profiles failed passive validation")
		}
	}
	return nil
}

// readVaultProfileHealth checks the saved credential before reading its expiry.
// An identity file, settings file, or stale health record cannot establish that
// a profile contains credentials. The saved files are also the source used by
// activation; a different live or isolated login must not validate this copy.
func readVaultProfileHealth(tool, profileName string) (*health.ProfileHealth, error) {
	ph := &health.ProfileHealth{}
	if healthStore != nil {
		stored, err := healthStore.GetProfile(tool, profileName)
		if err != nil {
			return ph, fmt.Errorf("read profile health: %w", err)
		}
		if stored != nil {
			ph = stored
		}
	}
	// Expiry and renewal capability belong to the current credential, even if
	// it has no expiry. Retain only persisted error and verification metadata.
	applyExpiryInfo(ph, &health.ExpiryInfo{})
	fileSet, ok := tools[tool]
	if !ok {
		return ph, fmt.Errorf("unknown tool: %s", tool)
	}
	if err := vault.ValidateProfileCredentials(fileSet(), profileName); err != nil {
		return ph, err
	}

	dir := vault.ProfilePath(tool, profileName)
	var info *health.ExpiryInfo
	var err error
	switch tool {
	case "claude":
		info, err = health.ParseClaudeExpiry(dir)
	case "codex":
		info, err = health.ParseCodexExpiry(filepath.Join(dir, "auth.json"))
	case "gemini":
		info, err = health.ParseGeminiExpiry(dir)
	case "grok":
		info, err = health.ParseGrokExpiry(filepath.Join(dir, "auth.json"))
	case "cursor":
		info, err = health.ParseCursorExpiry(filepath.Join(dir, "auth.json"))
	}
	// A supported credential may have no expiry parser (for example, a legacy
	// Claude API key). Presence was established above, independently of expiry.
	if err != nil && !errors.Is(err, health.ErrNoExpiry) && !errors.Is(err, health.ErrNoAuthFile) {
		return ph, err
	}
	if info != nil {
		applyExpiryInfo(ph, info)
	}
	return ph, nil
}

func invalidCredentialSignals() health.Signals {
	no, yes := false, true
	return health.Signals{RefreshDue: &no, LaunchUsable: &no, LoginRequired: &yes}
}

// validateVaultProfile is shared by the human and robot validation surfaces.
// Valid describes a passive local check, not a successful provider probe. An
// expired access token remains valid when the credential is renewable, unless
// the provider has already rejected that same credential.
func validateVaultProfile(tool, profileName string) (ValidationOutput, *health.ProfileHealth) {
	out := ValidationOutput{
		Provider:  tool,
		Profile:   profileName,
		Method:    "passive",
		CheckedAt: time.Now(),
	}

	ph, err := readVaultProfileHealth(tool, profileName)
	if err != nil {
		out.Error = err.Error()
		out.Signals = invalidCredentialSignals()
		out.Recommendation = fmt.Sprintf("log in with %s, then run 'caam backup %s %s' to save the credentials", tool, tool, profileName)
		return out, ph
	}
	out.Signals = health.CredentialSignals(ph, health.DefaultHealthConfig())
	out.Recommendation = health.FormatRecommendation(tool, profileName, ph)
	if ph.ProviderRejected() {
		out.Error = "provider rejected credential; log in again"
		return out, ph
	}

	expired := !ph.TokenExpiresAt.IsZero() && time.Until(ph.TokenExpiresAt) <= 0
	switch {
	case expired && ph.CredentialRenewable():
		// Refreshable: short-lived access token expired but a refresh token
		// remains. Considered valid/refreshable, not hard-expired. Avoid
		// presenting the access-token expiry as account expiry (issue #22).
		out.Valid = true
		out.ExpiresAt = "refreshable"
	case expired:
		out.Valid = false
		out.Error = "token expired and no refresh token available"
		out.ExpiresAt = "expired"
	default:
		out.Valid = true
		if !ph.TokenExpiresAt.IsZero() {
			out.ExpiresAt = formatExpiryTime(ph.TokenExpiresAt)
		}
	}

	return out, ph
}

func formatExpiryTime(t time.Time) string {
	now := time.Now()
	diff := t.Sub(now)

	if diff < 0 {
		return "expired"
	}

	if diff < time.Hour {
		return fmt.Sprintf("in %d minutes", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("in %d hours", int(diff.Hours()))
	}
	return fmt.Sprintf("in %d days", int(diff.Hours()/24))
}

func outputJSON(results []ValidationOutput) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func outputHuman(results []ValidationOutput) error {
	if len(results) == 0 {
		fmt.Println("No profiles to validate.")
		return nil
	}

	fmt.Println("Token Validation Results")
	fmt.Println("========================")
	fmt.Println()

	validCount := 0
	invalidCount := 0

	for _, r := range results {
		status := "✓"
		statusColor := "\033[32m" // Green
		if !r.Valid {
			status = "✗"
			statusColor = "\033[31m" // Red
			invalidCount++
		} else {
			validCount++
		}

		// Print result line
		fmt.Printf("%s%s\033[0m %s/%s", statusColor, status, r.Provider, r.Profile)

		if r.Valid {
			if r.ExpiresAt != "" {
				fmt.Printf(" (expires %s)", r.ExpiresAt)
			} else {
				fmt.Print(" (valid)")
			}
		} else {
			fmt.Printf(" - %s", r.Error)
		}
		fmt.Println()
	}

	fmt.Println()
	fmt.Printf("Summary: %d valid, %d invalid (method: %s)\n", validCount, invalidCount, results[0].Method)

	if invalidCount > 0 {
		return fmt.Errorf("%d invalid token(s) found", invalidCount)
	}
	return nil
}
