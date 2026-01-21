// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

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
  2. Verifies cosign signature on SHA256SUMS
  3. Verifies SHA256 checksum of the binary archive
  4. Creates a backup of the current binary
  5. Atomically replaces the binary

Flags:
  --check     Check for updates without installing
  --channel   Update channel: "stable" (default) or "beta"
  --version   Update to a specific version (e.g., "1.2.3")
  --json      Output results in JSON format
  --force     Force update even if already at latest version

Examples:
  caam update              # Update to latest stable version
  caam update --check      # Check if updates are available
  caam update --channel=beta  # Update to latest beta version
  caam update --version=1.2.0 # Update to specific version`,
	RunE: runUpdate,
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.Flags().Bool("check", false, "check for updates without installing")
	updateCmd.Flags().String("channel", "stable", "update channel (stable or beta)")
	updateCmd.Flags().String("version", "", "update to a specific version")
	updateCmd.Flags().Bool("json", false, "output in JSON format")
	updateCmd.Flags().Bool("force", false, "force update even if at latest version")
}

func runUpdate(cmd *cobra.Command, args []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check")
	channel, _ := cmd.Flags().GetString("channel")
	targetVersion, _ := cmd.Flags().GetString("version")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	force, _ := cmd.Flags().GetBool("force")

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
