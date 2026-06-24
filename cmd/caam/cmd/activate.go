package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/stealth"
	"github.com/spf13/cobra"
)

// activateOutput is the JSON output structure for activate command.
type activateOutput struct {
	Success         bool                    `json:"success"`
	Tool            string                  `json:"tool"`
	Profile         string                  `json:"profile"`
	PreviousProfile string                  `json:"previous_profile,omitempty"`
	Source          string                  `json:"source,omitempty"`
	AutoBackup      string                  `json:"auto_backup,omitempty"`
	Refreshed       bool                    `json:"refreshed,omitempty"`
	Rotation        *activateRotationResult `json:"rotation,omitempty"`
	CodexDaemon     *codexDaemonWarning     `json:"codex_daemon,omitempty"`
	Error           string                  `json:"error,omitempty"`
}

type activateRotationResult struct {
	Algorithm    string                        `json:"algorithm"`
	Selected     string                        `json:"selected"`
	Alternatives []activateRotationAlternative `json:"alternatives,omitempty"`
}

type activateRotationAlternative struct {
	Profile string  `json:"profile"`
	Score   float64 `json:"score"`
}

// activateCmd restores auth files from the vault.
var activateCmd = &cobra.Command{
	Use: "activate <tool> [profile-name]",
	// "switch" is the unambiguous activation alias. "use" is intentionally NOT an
	// alias here: there is a separate top-level `caam use <provider> <profile>`
	// command that sets the default profile in config (different semantics), so
	// advertising it as an activate alias would be misleading.
	Aliases: []string{"switch"},
	Short:   "Activate a profile (instant switch)",
	Long: `Restores auth files from the vault, instantly switching to that account.

This is the magic command - sub-second account switching without any login flows!

Examples:
  caam activate codex work-account
  caam activate codex
  caam activate claude personal-max
  caam activate gemini team-ultra
  caam activate claude --auto

The --auto flag enables smart profile rotation, which selects the best profile
based on health status, cooldown state, and usage patterns. Three algorithms
are available (configured in config.yaml):

  smart       - Multi-factor scoring (health, cooldown, recency)
  round_robin - Sequential rotation through profiles
  random      - Random selection

After activating, just run the tool normally - it will use the new account.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runActivate,
}

func init() {
	activateCmd.Flags().Bool("backup-current", false, "backup current auth before switching")
	activateCmd.Flags().Bool("force", false, "activate even if the profile is in cooldown")
	activateCmd.Flags().Bool("auto", false, "auto-select profile using rotation algorithm")
	activateCmd.Flags().Bool("json", false, "output as JSON")
	activateCmd.Flags().Bool("reload-daemon", false, "for codex: SIGTERM a running codex app-server/mcp-server daemon so the switched auth takes effect (it respawns on next use)")
}

func runActivate(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	autoSelect, _ := cmd.Flags().GetBool("auto")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Track output for JSON mode
	output := activateOutput{
		Tool: tool,
	}

	// Helper to emit JSON error
	emitJSONError := func(err error) error {
		if jsonOutput {
			output.Success = false
			output.Error = err.Error()
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			_ = enc.Encode(output)
			// The machine-readable error payload is already on stdout. Return the
			// underlying error so the process exits non-zero (the README agent
			// contract is "exit 0 = success"), but silence Cobra's usage dump and
			// duplicate stderr error print since this is a runtime failure, not a
			// flag/usage error.
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			return err
		}
		return err
	}

	if len(args) == 2 && autoSelect {
		return emitJSONError(fmt.Errorf("--auto cannot be used when a profile name is provided"))
	}

	getFileSet, ok := tools[tool]
	if !ok {
		return emitJSONError(fmt.Errorf("unknown tool: %s (supported: %s)", tool, supportedToolsList()))
	}

	// Ensure vault is initialized before using it
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	fileSet := getFileSet()
	previousProfile, _ := vault.ActiveProfile(fileSet)
	output.PreviousProfile = previousProfile

	// Safety: on first activate, preserve the user's pre-caam auth state.
	if did, err := vault.BackupOriginal(fileSet); err != nil {
		return emitJSONError(fmt.Errorf("backup original auth: %w", err))
	} else if did && !jsonOutput {
		fmt.Printf("Backed up original %s auth to %s\n", tool, "_original")
	}

	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		// Invalid config should not crash activation; fall back to defaults.
		spmCfg = config.DefaultSPMConfig()
	}

	needDB := spmCfg.Analytics.Enabled || spmCfg.Stealth.Cooldown.Enabled || spmCfg.Stealth.Rotation.Enabled || autoSelect
	var db *caamdb.DB
	if needDB {
		db, err = getDB()
		if err != nil {
			if !jsonOutput {
				fmt.Printf("Warning: could not open database: %v\n", err)
			}
		}
	}

	var profileName string
	var source string
	var selection *rotation.Result

	if len(args) == 2 {
		profileName = args[1]

		// Try to resolve as alias or fuzzy match
		profiles, err := vault.List(tool)
		if err == nil {
			profileName = resolveProfileName(tool, profileName, profiles, jsonOutput)
		}
	} else {
		// Resolve from project/default first unless user explicitly requested rotation.
		if !autoSelect {
			var err error
			profileName, source, err = resolveActivateProfile(tool, spmCfg)
			if err != nil {
				// If rotation is enabled, fall back to selecting automatically.
				if !spmCfg.Stealth.Rotation.Enabled {
					return emitJSONError(err)
				}
				autoSelect = true
			} else if spmCfg.Stealth.Rotation.Enabled && db != nil {
				// If the resolved default is in cooldown, automatically pick another profile.
				now := time.Now().UTC()
				if ev, err := db.ActiveCooldown(tool, profileName, now); err == nil && ev != nil {
					autoSelect = true
					source = "rotation (default in cooldown)"
				}
			}
		}

		if autoSelect {
			profiles, err := vault.List(tool)
			if err != nil {
				return emitJSONError(fmt.Errorf("list profiles: %w", err))
			}

			selection, err = selectProfileWithRotation(tool, profiles, previousProfile, spmCfg, db)
			if err != nil {
				return emitJSONError(err)
			}

			profileName = selection.Selected
			if source == "" {
				source = "rotation"
			}
		}

		if source != "" && !jsonOutput {
			fmt.Printf("Using %s: %s/%s\n", source, tool, profileName)
			if selection != nil {
				fmt.Println(rotation.FormatResult(selection))
			}
		}
	}

	// Track source and rotation in output
	output.Source = source
	if selection != nil {
		rot := &activateRotationResult{
			Algorithm: string(selection.Algorithm),
			Selected:  selection.Selected,
		}
		for _, alt := range selection.Alternatives {
			rot.Alternatives = append(rot.Alternatives, activateRotationAlternative{
				Profile: alt.Name,
				Score:   alt.Score,
			})
		}
		output.Rotation = rot
	}

	// Stealth: enforce per-profile cooldowns (opt-in).
	if spmCfg.Stealth.Cooldown.Enabled {
		force, _ := cmd.Flags().GetBool("force")

		if db == nil {
			if !jsonOutput {
				fmt.Printf("Warning: cooldown enforcement enabled but database is unavailable\n")
			}
		} else {
			now := time.Now().UTC()
			ev, err := db.ActiveCooldown(tool, profileName, now)
			if err != nil {
				if !jsonOutput {
					fmt.Printf("Warning: could not check cooldowns: %v\n", err)
				}
			} else if ev != nil {
				remaining := time.Until(ev.CooldownUntil)
				if remaining < 0 {
					remaining = 0
				}
				hitAgo := now.Sub(ev.HitAt)
				if hitAgo < 0 {
					hitAgo = 0
				}

				if !jsonOutput {
					fmt.Printf("Warning: %s/%s is in cooldown\n", tool, profileName)
					fmt.Printf("  Limit hit: %s ago\n", formatDurationShort(hitAgo))
					fmt.Printf("  Cooldown remaining: %s\n", formatDurationShort(remaining))
				}

				if !force {
					if !isTerminal() || jsonOutput {
						return emitJSONError(fmt.Errorf("%s/%s is in cooldown (%s remaining); re-run with --force to activate anyway", tool, profileName, formatDurationShort(remaining)))
					}

					ok, err := confirmProceed(cmd.InOrStdin(), cmd.OutOrStdout())
					if err != nil {
						return emitJSONError(fmt.Errorf("confirm proceed: %w", err))
					}
					if !ok {
						fmt.Println("Cancelled")
						return nil
					}
				} else if !jsonOutput {
					fmt.Println("Proceeding due to --force...")
				}
			}
		}
	}

	// Step 1: Refresh if needed
	refreshed := refreshIfNeeded(cmd.Context(), tool, profileName, jsonOutput)
	output.Refreshed = refreshed

	// Smart auto-backup before switch (based on safety config)
	backupMode := strings.TrimSpace(spmCfg.Safety.AutoBackupBeforeSwitch)
	if backupMode == "" {
		backupMode = "smart" // Default
	}

	// Check if --backup-current flag overrides config
	backupFirst, _ := cmd.Flags().GetBool("backup-current")
	if backupFirst {
		backupMode = "always"
	}

	if backupMode != "never" {
		shouldBackup := false
		currentProfile, _ := vault.ActiveProfile(fileSet)

		switch backupMode {
		case "always":
			// Always backup if there are auth files and we're switching to a different profile
			shouldBackup = currentProfile != profileName
		case "smart":
			// Backup only if current state doesn't match any vault profile (would be lost)
			shouldBackup = currentProfile == "" && authfile.HasAuthFiles(fileSet)
		}

		if shouldBackup {
			backupName, err := vault.BackupCurrent(fileSet)
			if err != nil {
				if !jsonOutput {
					fmt.Printf("Warning: could not auto-backup current state: %v\n", err)
				}
			} else if backupName != "" {
				output.AutoBackup = backupName
				if !jsonOutput {
					fmt.Printf("Auto-backed up current state to %s\n", backupName)
				}

				// Rotate old backups if limit is set
				if spmCfg.Safety.MaxAutoBackups > 0 {
					if err := vault.RotateAutoBackups(tool, spmCfg.Safety.MaxAutoBackups); err != nil {
						if !jsonOutput {
							fmt.Printf("Warning: could not rotate old backups: %v\n", err)
						}
					}
				}
			}
		}
	}

	// Re-snapshot the OUTGOING profile's (possibly rotated) tokens back into its
	// own vault dir before we clobber the live file. Codex/ChatGPT rotate OAuth
	// refresh tokens in place while a profile is active; without this, the
	// vault copy of the outgoing profile goes stale and a later restore replays
	// an already-consumed refresh_token, tripping reuse detection and bricking
	// the account. Non-fatal: a failure here must never block the switch.
	if outgoing, _ := vault.ActiveProfile(fileSet); outgoing != "" && outgoing != profileName {
		if err := vault.ResnapshotOutgoing(fileSet, outgoing, profileName); err != nil {
			if !jsonOutput {
				fmt.Printf("Warning: could not re-snapshot outgoing profile %s: %v\n", outgoing, err)
			}
		} else if !jsonOutput {
			fmt.Printf("Re-snapshotted outgoing profile %s (token rotation safety)\n", outgoing)
		}
	}

	// Stealth: optional delay before the actual switch happens.
	// Skip stealth delay in JSON mode as it's for interactive use
	if spmCfg.Stealth.SwitchDelay.Enabled && !jsonOutput {
		delay, err := stealth.ComputeDelay(spmCfg.Stealth.SwitchDelay.MinSeconds, spmCfg.Stealth.SwitchDelay.MaxSeconds, nil)
		if err != nil {
			fmt.Printf("Warning: invalid stealth.switch_delay config: %v\n", err)
		} else if delay > 0 {
			fmt.Printf("Stealth mode: waiting %d seconds before switch...\n", int(delay.Round(time.Second).Seconds()))

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt)

			skip := make(chan struct{})
			stop := make(chan struct{})
			go func() {
				select {
				case <-sigCh:
					close(skip)
				case <-stop:
				case <-cmd.Context().Done():
				}
			}()

			skipped, waitErr := stealth.Wait(cmd.Context(), delay, stealth.WaitOptions{
				Output:        os.Stdout,
				Skip:          skip,
				ShowCountdown: spmCfg.Stealth.SwitchDelay.ShowCountdown,
			})

			close(stop)
			signal.Stop(sigCh)

			if waitErr != nil {
				return fmt.Errorf("stealth delay: %w", waitErr)
			}
			if skipped {
				fmt.Println("Skipping delay...")
			}
		}
	}

	// Restore from vault
	if err := vault.Restore(fileSet, profileName); err != nil {
		return emitJSONError(fmt.Errorf("activate failed: %w", err))
	}

	// Codex daemon check: swapping auth.json on disk does not affect a running
	// `codex app-server`/`mcp-server`, which caches auth in-process. Detect it
	// and warn (or, with --reload-daemon, restart it) so the switch isn't a
	// silent no-op for daemon-backed sessions. See issue #21.
	reloadDaemon, _ := cmd.Flags().GetBool("reload-daemon")
	daemonWarn := checkCodexDaemon(tool, reloadDaemon)
	if daemonWarn.Detected {
		dw := daemonWarn
		output.CodexDaemon = &dw
	}

	if spmCfg.Analytics.Enabled && db != nil {
		logProfileSwitch(db, tool, previousProfile, profileName, map[string]any{
			"previous_profile": previousProfile,
			"selection_source": source,
		})
	}

	output.Profile = profileName
	output.Success = true

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	fmt.Printf("Activated %s profile '%s'\n", tool, profileName)
	fmt.Printf("  Run '%s' to start using this account\n", tool)
	printCodexDaemonWarning(cmd.ErrOrStderr(), daemonWarn)
	return nil
}

// logProfileSwitch records analytics events for a profile switch. When moving
// away from a different, non-system outgoing profile it emits a duration-bearing
// `deactivate` event for that outgoing profile (duration = now - its last
// activation) so usage analytics accrue active time, then emits the `activate`
// event for the incoming profile at the same instant. Without the deactivate
// event, `caam usage` shows zero active hours for ordinary switches (issue #31).
//
// System profiles (_original, _backup_*, _auto_backup_*) are skipped as the
// outgoing profile so they don't pollute user-facing usage stats.
func logProfileSwitch(db *caamdb.DB, tool, outgoing, incoming string, details map[string]any) {
	if db == nil {
		return
	}

	now := time.Now()

	if outgoing != "" && outgoing != incoming && !authfile.IsSystemProfile(outgoing) {
		if last, err := db.LastActivation(tool, outgoing); err == nil && !last.IsZero() {
			if d := now.Sub(last); d > 0 {
				_ = db.LogEvent(caamdb.Event{
					Type:        caamdb.EventDeactivate,
					Provider:    tool,
					ProfileName: outgoing,
					Timestamp:   now,
					Duration:    d,
					Details: map[string]any{
						"switched_to": incoming,
					},
				})
			}
		}
	}

	_ = db.LogEvent(caamdb.Event{
		Type:        caamdb.EventActivate,
		Provider:    tool,
		ProfileName: incoming,
		Timestamp:   now,
		Details:     details,
	})
}

func resolveActivateProfile(tool string, spmCfg *config.SPMConfig) (profileName string, source string, err error) {
	// Prefer project association (if enabled).
	if spmCfg == nil {
		spmCfg, _ = config.LoadSPMConfig()
	}

	if spmCfg != nil && spmCfg.Project.Enabled && projectStore != nil {
		cwd, wdErr := os.Getwd()
		if wdErr != nil {
			return "", "", fmt.Errorf("get current directory: %w", wdErr)
		}
		resolved, resErr := projectStore.Resolve(cwd)
		if resErr == nil {
			if p := strings.TrimSpace(resolved.Profiles[tool]); p != "" {
				src := resolved.Sources[tool]
				if src == "" || src == cwd {
					return p, "project association", nil
				}
				if src == "<default>" {
					return p, "project default", nil
				}
				return p, "project association", nil
			}
		}
	}

	// Fall back to configured default profile (caam config.json).
	if cfg != nil {
		if p := strings.TrimSpace(cfg.GetDefault(tool)); p != "" {
			return p, "default profile", nil
		}
	}

	return "", "", fmt.Errorf("no profile specified for %s and no project association/default found\nHint: run 'caam activate %s <profile-name>', 'caam use %s <profile-name>', or 'caam project set %s <profile-name>'", tool, tool, tool, tool)
}

// refreshIfNeeded refreshes a token if it's close to expiry.
// Returns true if a refresh was actually performed successfully.
// The quiet parameter suppresses all output (for JSON mode).
func refreshIfNeeded(ctx context.Context, provider, profile string, quiet bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}

	// Try to get health data. If missing, we might want to populate it?
	// But RefreshProfile uses vault path.
	// If we don't have health data, we don't know expiry, so we can't decide to refresh.
	// `getProfileHealth` in root.go parses files.
	// We should use that logic? `getProfileHealth` is in `root.go` (same package).
	h := getProfileHealth(provider, profile)

	if !refresh.ShouldRefresh(h, 0) {
		return false
	}

	if !quiet {
		fmt.Printf("Refreshing token (%s)... ", health.FormatTimeRemaining(h.TokenExpiresAt))
	}

	err := refresh.RefreshProfile(ctx, provider, profile, vault, healthStore)
	if err != nil {
		if errors.Is(err, refresh.ErrUnsupported) {
			if !quiet {
				fmt.Printf("skipped (%v)\n", err)
			}
			return false
		}
		if !quiet {
			fmt.Printf("failed (%v)\n", err)
		}
		return false // Continue activation even if refresh fails
	}

	if !quiet {
		fmt.Println("done")
	}
	return true
}

func selectProfileWithRotation(tool string, profiles []string, currentProfile string, spmCfg *config.SPMConfig, db *caamdb.DB) (*rotation.Result, error) {
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
	result, err := selector.Select(tool, profiles, currentProfile)
	if err != nil {
		return nil, fmt.Errorf("rotation select: %w", err)
	}

	return result, nil
}

// resolveProfileName resolves a profile name from user input.
// It tries: exact match -> alias resolution -> fuzzy match.
// The quiet parameter suppresses all output (for JSON mode).
func resolveProfileName(tool, input string, profiles []string, quiet bool) string {
	// Check for exact match first
	for _, p := range profiles {
		if p == input {
			return input
		}
	}

	// Try alias resolution
	globalCfg, err := config.Load()
	if err == nil {
		if resolved := globalCfg.ResolveAliasForProvider(tool, input); resolved != "" {
			if !quiet {
				fmt.Printf("Using alias: %s -> %s\n", input, resolved)
			}
			return resolved
		}

		// Try fuzzy matching
		matches := globalCfg.FuzzyMatch(tool, input, profiles)
		if len(matches) > 0 {
			if !quiet && matches[0] != input {
				fmt.Printf("Matched: %s -> %s\n", input, matches[0])
			}
			return matches[0]
		}
	}

	// No match found, return original (will fail later with proper error)
	return input
}
