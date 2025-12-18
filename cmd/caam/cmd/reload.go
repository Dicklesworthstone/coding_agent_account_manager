package cmd

import (
	"fmt"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Send SIGHUP to a running caam TUI",
	Long: `Sends a reload signal (SIGHUP) to the running caam TUI process.

This is useful if you want to force a refresh without restarting the TUI.
The TUI writes its PID to a pid file when running (if enabled in config).

Examples:
  caam reload
  caam reload --pid-file /custom/path/caam.pid`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		pidFile, _ := cmd.Flags().GetString("pid-file")
		if pidFile == "" {
			pidFile = signals.DefaultPIDFilePath()
		}

		pid, err := signals.ReadPIDFile(pidFile)
		if err != nil {
			return fmt.Errorf("read pid file: %w", err)
		}

		if err := signals.SendHUP(pid); err != nil {
			return fmt.Errorf("send reload signal: %w", err)
		}

		fmt.Printf("Sent reload signal to PID %d\n", pid)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(reloadCmd)
	reloadCmd.Flags().String("pid-file", "", "path to caam pid file (defaults to ~/.caam/caam.pid)")
}
