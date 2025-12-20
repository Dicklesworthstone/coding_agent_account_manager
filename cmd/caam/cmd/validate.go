// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/gemini"
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
	ctx := context.Background()

	// Get profile store
	store := profile.NewStore(profile.DefaultStorePath())

	// Build provider registry
	registry := provider.NewRegistry()
	registry.Register(claude.New())
	registry.Register(codex.New())
	registry.Register(gemini.New())

	var results []ValidationOutput
	var err error

	// Determine which profiles to validate
	switch len(args) {
	case 0:
		// Validate all profiles
		results, err = validateAllProfiles(ctx, store, registry, !validateActive)
	case 1:
		// Validate all profiles for a specific provider
		results, err = validateProviderProfiles(ctx, store, registry, args[0], !validateActive)
	case 2:
		// Validate specific profile
		results, err = validateSingleProfile(ctx, store, registry, args[0], args[1], !validateActive)
	}

	if err != nil {
		return err
	}

	// Output results
	if validateJSON {
		return outputJSON(results)
	}
	return outputHuman(results)
}

func validateAllProfiles(ctx context.Context, store *profile.Store, registry *provider.Registry, passive bool) ([]ValidationOutput, error) {
	var results []ValidationOutput

	for _, prov := range registry.All() {
		profiles, err := store.List(prov.ID())
		if err != nil {
			continue // Skip providers with no profiles
		}

		for _, prof := range profiles {
			result, err := validateProfile(ctx, prov, prof, passive)
			if err != nil {
				result = &ValidationOutput{
					Provider:  prov.ID(),
					Profile:   prof.Name,
					Valid:     false,
					Method:    methodString(passive),
					Error:     err.Error(),
					CheckedAt: time.Now(),
				}
			}
			results = append(results, *result)
		}
	}

	return results, nil
}

func validateProviderProfiles(ctx context.Context, store *profile.Store, registry *provider.Registry, providerID string, passive bool) ([]ValidationOutput, error) {
	prov, ok := registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	profiles, err := store.List(providerID)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}

	var results []ValidationOutput
	for _, prof := range profiles {
		result, err := validateProfile(ctx, prov, prof, passive)
		if err != nil {
			result = &ValidationOutput{
				Provider:  prov.ID(),
				Profile:   prof.Name,
				Valid:     false,
				Method:    methodString(passive),
				Error:     err.Error(),
				CheckedAt: time.Now(),
			}
		}
		results = append(results, *result)
	}

	return results, nil
}

func validateSingleProfile(ctx context.Context, store *profile.Store, registry *provider.Registry, providerID, profileName string, passive bool) ([]ValidationOutput, error) {
	prov, ok := registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	prof, err := store.Load(providerID, profileName)
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}

	result, err := validateProfile(ctx, prov, prof, passive)
	if err != nil {
		return nil, err
	}

	return []ValidationOutput{*result}, nil
}

func validateProfile(ctx context.Context, prov provider.Provider, prof *profile.Profile, passive bool) (*ValidationOutput, error) {
	result, err := prov.ValidateToken(ctx, prof, passive)
	if err != nil {
		return nil, err
	}

	output := &ValidationOutput{
		Provider:  result.Provider,
		Profile:   result.Profile,
		Valid:     result.Valid,
		Method:    result.Method,
		Error:     result.Error,
		CheckedAt: result.CheckedAt,
	}

	if !result.ExpiresAt.IsZero() {
		output.ExpiresAt = formatExpiryTime(result.ExpiresAt)
	}

	return output, nil
}

func methodString(passive bool) string {
	if passive {
		return "passive"
	}
	return "active"
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
