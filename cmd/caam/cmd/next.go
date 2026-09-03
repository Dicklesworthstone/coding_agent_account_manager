package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

// nextCmd rotates to the next available profile for a tool.
var nextCmd = &cobra.Command{
	Use:     "next <tool>",
	Aliases: []string{"rotate"},
	Short:   "Rotate to next available profile",
	Long: `Instantly rotate to the next best profile for a tool.

Uses the configured rotation algorithm to select the next profile:
  smart       - Multi-factor scoring (health, cooldown, recency) [default]
  round_robin - Sequential rotation through profiles
  random      - Random selection

Examples:
  caam next claude      # Switch to next healthy Claude profile
  caam next codex       # Switch to next healthy Codex profile
  caam next gemini      # Switch to next healthy Gemini profile
  caam next claude --dry-run   # Show what would be selected
  caam next claude -q   # Quiet mode, minimal output`,
	Args: cobra.ExactArgs(1),
	RunE: runNext,
}

func init() {
	nextCmd.Flags().BoolP("dry-run", "n", false, "show next profile without switching")
	nextCmd.Flags().BoolP("quiet", "q", false, "minimal output")
	nextCmd.Flags().Bool("force", false, "activate even if profile is in cooldown")
	nextCmd.Flags().String("algorithm", "", "override rotation algorithm (smart, round_robin, random)")
	nextCmd.Flags().String("policy", "", "override rotation policy: availability (default), drain (prefer soonest-resetting usable quota; pair with --usage-aware)")
	nextCmd.Flags().Bool("usage-aware", false, "fetch real-time rate limits to inform selection")
	nextCmd.Flags().Bool("reload-daemon", false, "for codex: SIGTERM a running codex app-server/mcp-server daemon so the switched auth takes effect (it respawns on next use)")
	rootCmd.AddCommand(nextCmd)
}

func runNext(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	quiet, _ := cmd.Flags().GetBool("quiet")
	force, _ := cmd.Flags().GetBool("force")
	algoOverride, _ := cmd.Flags().GetString("algorithm")
	policyOverride, _ := cmd.Flags().GetString("policy")
	policyOverride = strings.ToLower(policyOverride)
	switch policyOverride {
	case "", "availability", "drain":
	default:
		return fmt.Errorf("unknown policy: %s (supported: availability, drain)", policyOverride)
	}
	usageAware, _ := cmd.Flags().GetBool("usage-aware")
	reloadDaemon, _ := cmd.Flags().GetBool("reload-daemon")

	// Validate tool
	getFileSet, ok := tools[tool]
	if !ok {
		return fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList())
	}

	// Ensure vault is initialized
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	fileSet := getFileSet()
	currentProfile, _ := vault.ActiveProfile(fileSet)

	// List available profiles
	profiles, err := vault.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	if len(profiles) == 0 {
		return fmt.Errorf("no profiles found for %s; create one with 'caam backup %s <name>'", tool, tool)
	}

	if len(profiles) == 1 {
		if currentProfile == profiles[0] {
			fmt.Printf("Only one profile available for %s (%s), already active\n", tool, profiles[0])
			return nil
		}
		// Single profile case: just activate it
		if !dryRun {
			// Re-snapshot outgoing profile's rotated tokens first (see activate.go).
			if currentProfile != "" && currentProfile != profiles[0] {
				if err := vault.ResnapshotOutgoing(fileSet, currentProfile, profiles[0]); err != nil && !quiet {
					fmt.Printf("Warning: could not re-snapshot outgoing profile %s: %v\n", currentProfile, err)
				}
			}
			if err := vault.Restore(fileSet, profiles[0]); err != nil {
				return fmt.Errorf("activate failed: %w", err)
			}
		}
		if !quiet {
			if dryRun {
				fmt.Printf("Would switch to: %s/%s\n", tool, profiles[0])
			} else {
				fmt.Printf("Activated %s profile '%s'\n", tool, profiles[0])
			}
		}
		// Codex daemon check (see issue #21): a running codex app-server caches
		// auth in-process, so the on-disk swap won't apply to it. Skip on
		// dry-run (nothing was actually switched).
		if !dryRun {
			daemonWarn := checkCodexDaemon(tool, reloadDaemon)
			if !quiet {
				printCodexDaemonWarning(cmd.ErrOrStderr(), daemonWarn)
			}
		}
		return nil
	}

	// Load config for rotation algorithm
	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		spmCfg = config.DefaultSPMConfig()
	}

	// Override algorithm/policy if specified
	if algoOverride != "" {
		spmCfg.Stealth.Rotation.Algorithm = algoOverride
	}
	if policyOverride != "" {
		spmCfg.Stealth.Rotation.Policy = policyOverride
	}

	// Open database for health/cooldown checks
	var db *caamdb.DB
	db, err = caamdb.Open()
	if err != nil {
		if !quiet {
			fmt.Printf("Warning: could not open database: %v\n", err)
		}
	} else {
		defer db.Close()
	}

	// Fetch usage data if --usage-aware is set
	var usageData map[string]*rotation.UsageInfo
	if usageAware && (tool == "claude" || tool == "codex") {
		if !quiet {
			fmt.Printf("Fetching real-time usage data for %d profiles...\n", len(profiles))
		}
		usageData = fetchUsageDataForProfiles(tool, profiles)
	} else if usageAware {
		// Loud fallback (issue #79): real-time limit fetching is implemented for
		// claude and codex only. Say so — on stderr, even in quiet mode — instead
		// of silently ignoring the flag the user asked for.
		fmt.Fprintf(os.Stderr, "caam: --usage-aware is not supported for %q (real-time limits are implemented for claude and codex only); selecting without usage data\n", tool)
	}

	// Select next profile using rotation
	selection, err := selectProfileWithRotationAndUsage(tool, profiles, currentProfile, spmCfg, db, usageData)
	if err != nil {
		return err
	}

	// If rotation selected the same profile (can happen with smart algorithm),
	// use round_robin to force rotation to a different profile.
	// Exception: under the drain policy (issue #81), staying on the profile
	// whose quota resets soonest is the whole point — don't force a rotation
	// away from it.
	if selection.Selected == currentProfile && len(profiles) > 1 &&
		spmCfg.Stealth.Rotation.Policy != "drain" {
		spmCfg.Stealth.Rotation.Algorithm = "round_robin"
		selection, err = selectProfileWithRotationAndUsage(tool, profiles, currentProfile, spmCfg, db, usageData)
		if err != nil {
			return err
		}
	}

	// Show selection info
	if !quiet {
		if currentProfile != "" {
			fmt.Printf("Current: %s/%s\n", tool, currentProfile)
		} else {
			fmt.Printf("Current: %s (no active profile)\n", tool)
		}
		fmt.Printf("Next:    %s/%s\n", tool, selection.Selected)
		fmt.Println(rotation.FormatResult(selection))
	}

	// Dry-run: stop here
	if dryRun {
		return nil
	}

	// Check cooldown (unless force specified)
	if !force && spmCfg.Stealth.Cooldown.Enabled && db != nil {
		now := time.Now().UTC()
		if ev, err := db.ActiveCooldown(tool, selection.Selected, now); err == nil && ev != nil {
			remaining := time.Until(ev.CooldownUntil)
			if remaining < 0 {
				remaining = 0
			}
			if !quiet {
				fmt.Printf("Warning: %s/%s is in cooldown (%s remaining)\n",
					tool, selection.Selected, formatDurationShort(remaining))
			}
			return fmt.Errorf("selected profile is in cooldown; use --force to override")
		}
	}

	// Re-snapshot outgoing profile's rotated tokens before clobbering the live
	// file (refresh-token rotation safety; see activate.go / ResnapshotOutgoing).
	if currentProfile != "" && currentProfile != selection.Selected {
		if err := vault.ResnapshotOutgoing(fileSet, currentProfile, selection.Selected); err != nil && !quiet {
			fmt.Printf("Warning: could not re-snapshot outgoing profile %s: %v\n", currentProfile, err)
		}
	}

	// Activate selected profile
	if err := vault.Restore(fileSet, selection.Selected); err != nil {
		return fmt.Errorf("activate failed: %w", err)
	}

	// Log event. logProfileSwitch also emits a duration-bearing deactivate event
	// for the outgoing profile so usage analytics accrue active time (issue #31).
	if spmCfg.Analytics.Enabled && db != nil {
		logProfileSwitch(db, tool, currentProfile, selection.Selected, map[string]any{
			"previous_profile": currentProfile,
			"selection_source": "next",
			"algorithm":        selection.Algorithm,
		})
	}

	if !quiet {
		remaining := len(profiles) - 1 // Other profiles available
		fmt.Printf("Switched %s to '%s' (%d other profile%s available)\n",
			tool, selection.Selected, remaining, pluralize(remaining))
		fmt.Printf("  Run '%s' to start using this account\n", tool)
	}

	// Codex daemon check (see issue #21): warn (or --reload-daemon restart) a
	// running codex app-server that has cached the previous account's auth.
	daemonWarn := checkCodexDaemon(tool, reloadDaemon)
	if !quiet {
		printCodexDaemonWarning(cmd.ErrOrStderr(), daemonWarn)
	}

	return nil
}

// pluralize returns "s" if n != 1.
func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// fetchUsageDataForProfiles fetches real-time usage data for all profiles.
func fetchUsageDataForProfiles(tool string, profiles []string) map[string]*rotation.UsageInfo {
	vaultDir := authfile.DefaultVaultPath()
	credentials, err := usage.LoadProfileCredentials(vaultDir, tool)
	if err != nil || len(credentials) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fetcher := usage.NewMultiProfileFetcher()
	results := fetcher.FetchAllProfiles(ctx, tool, credentials)

	usageData := make(map[string]*rotation.UsageInfo)
	for _, r := range results {
		if r.Usage == nil {
			continue
		}

		// `caam next` picks an account without knowing what will run on it,
		// so every model-scoped quota counts against availability.
		usageData[r.ProfileName] = toRotationUsageInfo(r.ProfileName, r.Usage, "")
	}

	return usageData
}

// selectProfileWithRotationAndUsage selects a profile using rotation with optional usage data.
func selectProfileWithRotationAndUsage(tool string, profiles []string, currentProfile string, spmCfg *config.SPMConfig, db *caamdb.DB, usageData map[string]*rotation.UsageInfo) (*rotation.Result, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no profiles found for %s; create one with 'caam backup %s <name>'", tool, tool)
	}

	primePlanTypes(tool, profiles)

	algorithm := rotation.AlgorithmSmart
	if spmCfg != nil {
		if a := strings.TrimSpace(spmCfg.Stealth.Rotation.Algorithm); a != "" {
			algorithm = rotation.Algorithm(a)
		}
	}

	selector := rotation.NewSelector(algorithm, healthStore, db)
	applyRotationPolicy(selector, spmCfg, "")

	// Set usage data if available
	if usageData != nil {
		selector.SetUsageData(usageData)
	}

	result, err := selector.Select(tool, profiles, currentProfile)
	if err != nil {
		return nil, fmt.Errorf("rotation select: %w", err)
	}

	return result, nil
}
