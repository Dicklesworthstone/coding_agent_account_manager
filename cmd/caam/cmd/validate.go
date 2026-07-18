// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

var validateCmd = &cobra.Command{
	Use:   "validate [tool] [profile]",
	Short: "Validate authentication tokens",
	Long: `Validate that authentication tokens actually work.

By default, performs passive validation (no network calls):
  - Check auth file existence
  - Check token format/structure
  - Check expiry timestamps

Use --active for active validation (makes minimal API calls):
  - Verifies token is actually valid with the provider
  - May incur minimal API costs

Examples:
  caam validate                    # Validate all profiles (passive)
  caam validate claude             # Validate all Claude profiles
  caam validate claude work        # Validate specific profile
  caam validate --active           # Active validation for all profiles
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
	validateCmd.Flags().BoolVar(&validateActive, "active", false, "Perform active validation (API calls)")
	validateCmd.Flags().BoolVar(&validateJSON, "json", false, "Output in JSON format")
	validateCmd.Flags().BoolVar(&validateAll, "all", false, "Validate all profiles (default behavior)")
	rootCmd.AddCommand(validateCmd)
}

// ValidationOutput represents the JSON output for validation results.
type ValidationOutput struct {
	Provider  string    `json:"provider"`
	Profile   string    `json:"profile"`
	Valid     bool      `json:"valid"`
	Method    string    `json:"method"`
	ExpiresAt string    `json:"expires_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
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

	// Active validation against saved vault profiles is not implemented; vault
	// profiles are raw auth files, not isolated profile homes. Surface this on
	// stderr (diagnostics) and proceed with passive validation rather than
	// silently pretending to make API calls.
	if validateActive {
		fmt.Fprintln(os.Stderr, "note: --active is not supported for saved vault profiles; performing passive validation")
	}

	results := []ValidationOutput{}

	for _, tool := range supportedTools() {
		if toolFilter != "" && tool != toolFilter {
			continue
		}

		profiles, err := vault.List(tool)
		if err != nil {
			continue // No profiles for this tool
		}
		sort.Strings(profiles)

		for _, profileName := range profiles {
			if authfile.IsSystemProfile(profileName) {
				continue // Skip _original / _backup_* system profiles
			}
			if profileFilter != "" && profileName != profileFilter {
				continue
			}
			results = append(results, validateVaultProfile(tool, profileName))
		}
	}

	// Output results. Encode empty results as [] (not null) for agent parsing.
	if validateJSON {
		return outputJSON(results)
	}
	return outputHuman(results)
}

// validateVaultProfile passively validates a single saved vault profile. A
// profile is valid when its auth files are present and parseable. An expired
// access token does NOT make the profile invalid if a refresh token is present:
// such profiles are refreshable, and reporting them as hard-expired is
// misleading (issue #22). Only credentials with no refresh capability are
// reported as expired/invalid.
func validateVaultProfile(tool, profileName string) ValidationOutput {
	out := ValidationOutput{
		Provider:  tool,
		Profile:   profileName,
		Method:    "passive",
		CheckedAt: time.Now(),
	}

	info, err := loadExpiryInfo(tool, profileName)
	if err != nil {
		switch {
		case errors.Is(err, health.ErrNoAuthFile):
			out.Valid = false
			out.Error = "no auth files found"
		case errors.Is(err, health.ErrNoExpiry):
			// Auth files exist but carry no parseable expiry/refresh metadata.
			// Treat as valid-but-unknown: the credentials are present.
			out.Valid = true
		default:
			out.Valid = false
			out.Error = err.Error()
		}
		return out
	}

	// Tools without expiry parsing (opencode/cursor/agy/grok) return nil info; the
	// presence of the vault profile dir is the validation signal for them.
	if info == nil {
		out.Valid = true
		return out
	}

	expired := !info.ExpiresAt.IsZero() && time.Until(info.ExpiresAt) <= 0
	switch {
	case expired && info.HasRefreshToken:
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
		if !info.ExpiresAt.IsZero() {
			out.ExpiresAt = formatExpiryTime(info.ExpiresAt)
		}
	}

	return out
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
