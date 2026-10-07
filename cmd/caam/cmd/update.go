// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/deploy"
	caamsync "github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/update"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
)

// UpdateOutput represents the JSON output for update commands.
type UpdateOutput struct {
	Action          string `json:"action"` // "check", "update"
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available,omitempty"`
	Updated         bool   `json:"updated,omitempty"`
	BackupPath      string `json:"backup_path,omitempty"`
	ReleaseURL      string `json:"release_url,omitempty"`
	DownloadSize    int64  `json:"download_size,omitempty"`
	Channel         string `json:"channel"`
	Error           string `json:"error,omitempty"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Self-update caam to the latest version",
	Long: `Updates caam to the latest version from GitHub releases.

The update process:
  1. Fetches release metadata from GitHub
  2. Verifies the signature on SHA256SUMS (minisign with the embedded
     release key for releases >= 0.1.18; legacy cosign for older releases)
  3. Verifies SHA256 checksum of the binary archive
  4. Creates a backup of the current binary
  5. Atomically replaces the binary

With --remotes, brings the distributed auth coordinators listed in the
auth-agent config (written by 'caam setup distributed') to this caam's
version instead. Each host keeps its deployed coordinator config; the new
binary is checksum-verified (or, for another platform, the matching release
is installed), the service restarted, and its API verified through SSH. A
coordinator that fails verification is rolled back to the previous binary
and unit, which stay on the host as <binary>.caam-prev otherwise;
--remotes --rollback restores them on demand. --force redeploys and restarts
even at the same version (for example to refresh the service unit).

Flags:
  --check     Check for updates without installing
  --channel   Update channel: "stable" (default) or "beta"
  --version   Update to a specific version (e.g., "1.2.3")
  --json      Output results in JSON format
  --force     Force update even if already at latest version
  --remotes   Update the remote coordinators instead of this binary
  --dry-run   With --remotes, show what would change
  --rollback  With --remotes, restore the coordinators the last upgrade replaced

Examples:
  caam update              # Update to latest stable version
  caam update --check      # Check if updates are available
  caam update --channel=beta  # Update to latest beta version
  caam update --version=1.2.0 # Update to specific version
  caam update --remotes --dry-run  # Plan coordinator upgrades
  caam update --remotes            # Upgrade coordinators to this version
  caam update --remotes --rollback # Restore the previous coordinators`,
	RunE: runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.Flags().Bool("check", false, "check for updates without installing")
	updateCmd.Flags().String("channel", "stable", "update channel (stable or beta)")
	updateCmd.Flags().String("version", "", "update to a specific version")
	updateCmd.Flags().Bool("json", false, "output in JSON format")
	updateCmd.Flags().Bool("force", false, "force update even if at latest version")
	updateCmd.Flags().Bool("remotes", false, "update the distributed auth coordinators to this caam's version")
	updateCmd.Flags().Bool("dry-run", false, "with --remotes, show what would change without changing it")
	updateCmd.Flags().String("config", "", "with --remotes, the auth-agent config listing the coordinators (default: the 'caam setup distributed' config)")
	updateCmd.Flags().Bool("rollback", false, "with --remotes, restore the coordinators replaced by the last upgrade")
}

func runUpdate(cmd *cobra.Command, args []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check")
	channel, _ := cmd.Flags().GetString("channel")
	targetVersion, _ := cmd.Flags().GetString("version")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	force, _ := cmd.Flags().GetBool("force")
	remotes, _ := cmd.Flags().GetBool("remotes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	rollback, _ := cmd.Flags().GetBool("rollback")

	if remotes {
		if checkOnly || targetVersion != "" {
			return fmt.Errorf("--remotes upgrades coordinators to this caam's version; it cannot be combined with --check or --version")
		}
		if rollback && (dryRun || force) {
			return fmt.Errorf("--rollback cannot be combined with --dry-run or --force")
		}
		configPath, _ := cmd.Flags().GetString("config")
		return runRemoteUpdate(cmd, configPath, remoteUpdateRequest{
			Options:  deploy.UpgradeOptions{DryRun: dryRun, Force: force},
			Rollback: rollback,
		}, jsonOutput)
	}
	if rollback {
		return fmt.Errorf("--rollback applies to --remotes; 'caam update' keeps a backup of this binary next to it")
	}
	if dryRun {
		return fmt.Errorf("--dry-run applies to --remotes; use --check to see whether this caam has an update")
	}

	// Build update config
	config := update.DefaultConfig()

	switch channel {
	case "stable":
		config.Channel = update.ChannelStable
	case "beta":
		config.Channel = update.ChannelBeta
	default:
		return fmt.Errorf("invalid channel: %s (use 'stable' or 'beta')", channel)
	}

	if targetVersion != "" {
		config.TargetVersion = targetVersion
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	updater := update.New(config)

	if checkOnly {
		return runUpdateCheck(ctx, updater, channel, jsonOutput)
	}

	return runUpdateInstall(ctx, updater, channel, jsonOutput, force)
}

func runUpdateCheck(ctx context.Context, updater *update.Updater, channel string, jsonOutput bool) error {
	result, err := updater.Check(ctx)

	output := UpdateOutput{
		Action:         "check",
		CurrentVersion: version.Short(),
		Channel:        channel,
	}

	if err != nil {
		output.Error = err.Error()
		if jsonOutput {
			return printJSON(output)
		}
		return fmt.Errorf("check for updates: %w", err)
	}

	output.LatestVersion = result.LatestVersion
	output.UpdateAvailable = result.UpdateAvailable
	if result.Release != nil {
		output.ReleaseURL = result.Release.HTMLURL
	}

	if jsonOutput {
		return printJSON(output)
	}

	fmt.Printf("Current version: %s\n", output.CurrentVersion)
	fmt.Printf("Latest version:  %s\n", output.LatestVersion)
	fmt.Printf("Channel:         %s\n", channel)

	if result.UpdateAvailable {
		fmt.Println("\nUpdate available! Run 'caam update' to install.")
		if result.Release != nil {
			fmt.Printf("Release notes: %s\n", result.Release.HTMLURL)
		}
	} else {
		fmt.Println("\nYou're running the latest version.")
	}

	return nil
}

func runUpdateInstall(ctx context.Context, updater *update.Updater, channel string, jsonOutput bool, force bool) error {
	// First check if update is available
	check, err := updater.Check(ctx)
	if err != nil {
		output := UpdateOutput{
			Action:         "update",
			CurrentVersion: version.Short(),
			Channel:        channel,
			Error:          err.Error(),
		}
		if jsonOutput {
			return printJSON(output)
		}
		return fmt.Errorf("check for updates: %w", err)
	}

	if !check.UpdateAvailable && !force {
		output := UpdateOutput{
			Action:          "update",
			CurrentVersion:  version.Short(),
			LatestVersion:   check.LatestVersion,
			UpdateAvailable: false,
			Updated:         false,
			Channel:         channel,
		}
		if jsonOutput {
			return printJSON(output)
		}
		fmt.Printf("Already at latest version (%s). Use --force to reinstall.\n", check.LatestVersion)
		return nil
	}

	if !jsonOutput {
		fmt.Printf("Updating caam from %s to %s...\n", check.CurrentVersion, check.LatestVersion)
	}

	result, err := updater.Update(ctx)

	output := UpdateOutput{
		Action:         "update",
		CurrentVersion: check.CurrentVersion,
		LatestVersion:  check.LatestVersion,
		Channel:        channel,
	}

	if err != nil {
		output.Error = err.Error()
		if jsonOutput {
			return printJSON(output)
		}
		return fmt.Errorf("update: %w", err)
	}

	output.Updated = result.Updated
	output.BackupPath = result.BackupPath
	output.ReleaseURL = result.ReleaseURL
	output.DownloadSize = result.DownloadSize

	if jsonOutput {
		return printJSON(output)
	}

	if result.Updated {
		fmt.Println("\n✓ Update successful!")
		fmt.Printf("  From:   %s\n", result.FromVersion)
		fmt.Printf("  To:     %s\n", result.ToVersion)
		fmt.Printf("  Backup: %s\n", result.BackupPath)
		fmt.Println("\nRun 'caam version' to verify the update.")
	} else {
		fmt.Println("No update performed.")
	}

	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// RemoteUpdateOutput is the JSON output of 'caam update --remotes'.
type RemoteUpdateOutput struct {
	Action  string                 `json:"action"` // "remote_update" or "remote_rollback"
	DryRun  bool                   `json:"dry_run,omitempty"`
	Version string                 `json:"version"`
	Results []deploy.UpgradeResult `json:"results"`
}

// remoteUpdateRequest is what 'caam update --remotes' does on each host.
type remoteUpdateRequest struct {
	Options  deploy.UpgradeOptions
	Rollback bool // restore the coordinator the last upgrade replaced
}

// upgradeSkipped marks a coordinator that cannot be reached over SSH.
const upgradeSkipped = "skipped"

// upgradeCoordinatorHost connects to one coordinator host and upgrades (or
// rolls back) its coordinator.
var upgradeCoordinatorHost = func(ctx context.Context, ep *agent.CoordinatorEndpoint, req remoteUpdateRequest, logger *slog.Logger) deploy.UpgradeResult {
	d := deploy.NewDeployer(&caamsync.Machine{
		Name:       ep.Name,
		Address:    ep.SSH.Host,
		Port:       ep.SSH.Port,
		SSHUser:    ep.SSH.User,
		SSHKeyPath: ep.SSH.IdentityFile,
	}, logger)
	if err := d.Connect(); err != nil {
		return deploy.UpgradeResult{Machine: ep.Name, Action: deploy.UpgradeFailed, Error: err.Error()}
	}
	defer d.Disconnect()
	if req.Rollback {
		return *d.RollbackCoordinator(ctx)
	}
	return *d.UpgradeCoordinator(ctx, req.Options)
}

func runRemoteUpdate(cmd *cobra.Command, configPath string, req remoteUpdateRequest, jsonOutput bool) error {
	if configPath == "" {
		configPath = agent.DefaultConfigPath()
	}
	fc, err := agent.LoadFileConfig(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no auth-agent config at %s: run 'caam setup distributed' first", configPath)
	}
	if err != nil {
		return fmt.Errorf("agent config %s: %w", configPath, err)
	}
	if len(fc.Coordinators) == 0 {
		return fmt.Errorf("agent config %s lists no coordinators", configPath)
	}

	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))

	out := cmd.OutOrStdout()
	output := RemoteUpdateOutput{Action: "remote_update", DryRun: req.Options.DryRun, Version: version.Short()}
	if req.Rollback {
		output.Action = "remote_rollback"
	}
	if !jsonOutput {
		if req.Rollback {
			fmt.Fprintln(out, "Rolling back coordinators to their previous version")
		} else {
			fmt.Fprintf(out, "Coordinators -> caam %s\n", version.Short())
		}
	}
	failures := 0
	for _, ep := range fc.Coordinators {
		var res deploy.UpgradeResult
		if ep.SSH == nil || strings.TrimSpace(ep.SSH.Host) == "" {
			res = deploy.UpgradeResult{Machine: ep.Name, Action: upgradeSkipped,
				Error: "no ssh block in the agent config; update caam on that host directly"}
		} else {
			res = upgradeCoordinatorHost(ctx, ep, req, logger)
		}
		if res.Machine == "" {
			res.Machine = ep.URL
		}
		// A rollback is the goal of --rollback and a failure of an upgrade.
		if res.Action == deploy.UpgradeFailed || (res.Action == deploy.UpgradeRolledBack && !req.Rollback) {
			failures++
		}
		output.Results = append(output.Results, res)
		if !jsonOutput {
			printUpgradeResult(out, res)
		}
	}

	if jsonOutput {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output); err != nil {
			return err
		}
	}
	if failures > 0 {
		cmd.SilenceUsage = true
		verb := "upgraded"
		if req.Rollback {
			verb = "rolled back"
		}
		return fmt.Errorf("%d of %d coordinators were not %s", failures, len(fc.Coordinators), verb)
	}
	return nil
}

func printUpgradeResult(w io.Writer, r deploy.UpgradeResult) {
	from := r.FromVersion
	if from == "" {
		from = "not installed"
	}
	switch r.Action {
	case deploy.UpgradeUpgraded:
		fmt.Fprintf(w, "  ✓ %s: %s -> %s\n", r.Machine, from, r.ToVersion)
	case deploy.UpgradeUpToDate:
		fmt.Fprintf(w, "  = %s: up to date (%s)\n", r.Machine, from)
	case deploy.UpgradeWouldApply:
		fmt.Fprintf(w, "  ~ %s: would upgrade %s -> %s\n", r.Machine, from, r.ToVersion)
	case deploy.UpgradeRolledBack:
		if r.Error != "" { // an upgrade that failed verification
			fmt.Fprintf(w, "  ↺ %s: rolled back to %s: %s\n", r.Machine, from, r.Error)
		} else {
			fmt.Fprintf(w, "  ↺ %s: rolled back %s -> %s\n", r.Machine, from, r.ToVersion)
		}
	case upgradeSkipped:
		fmt.Fprintf(w, "  - %s: skipped: %s\n", r.Machine, r.Error)
	default:
		fmt.Fprintf(w, "  ✗ %s: %s\n", r.Machine, r.Error)
	}
	if !r.Verified && (r.Action == deploy.UpgradeUpToDate || r.Action == deploy.UpgradeUpgraded || r.Action == deploy.UpgradeRolledBack) {
		fmt.Fprintf(w, "      ⚠ the coordinator is not answering its status check\n")
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "      ⚠ %s\n", warning)
	}
}
