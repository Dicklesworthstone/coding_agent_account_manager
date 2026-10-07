package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/daemon"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/update"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon <command>",
	Short: "Manage the background token refresh daemon",
	Long: `Start, stop, and monitor the background daemon for proactive token management.

The daemon runs in the background and automatically refreshes tokens before they expire,
ensuring your AI tools always have valid authentication.

Examples:
  caam daemon start          # Start the daemon in the background
  caam daemon start --fg     # Start the daemon in the foreground
  caam daemon stop           # Stop the running daemon
  caam daemon status         # Check if daemon is running
  caam daemon logs           # View daemon logs`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the background daemon",
	Long: `Start the background token refresh daemon.

The daemon will periodically check all profiles and refresh tokens before they expire.
By default, it runs in the background. Use --fg to run in the foreground (useful for debugging).`,
	RunE: runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon",
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check daemon status",
	RunE:  runDaemonStatus,
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View daemon logs",
	RunE:  runDaemonLogs,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonCmd.AddCommand(daemonAutoBackupCmd)

	daemonStatusCmd.Flags().Bool("json", false, "output status as JSON")

	daemonAutoBackupCmd.Flags().Bool("enable", false, "enable scheduled backups")
	daemonAutoBackupCmd.Flags().Bool("disable", false, "disable scheduled backups")
	daemonAutoBackupCmd.Flags().Duration("interval", 7*24*time.Hour, "time between backups (minimum 1h)")
	daemonAutoBackupCmd.Flags().Int("keep", 5, "number of backups to keep")
	daemonAutoBackupCmd.Flags().String("location", "", "directory for backup archives (default ~/.caam-backups)")

	// Start flags
	daemonStartCmd.Flags().Bool("fg", false, "run in foreground (don't daemonize)")
	daemonStartCmd.Flags().Duration("interval", daemon.DefaultCheckInterval, "check interval")
	daemonStartCmd.Flags().Duration("threshold", daemon.DefaultRefreshThreshold, "refresh threshold (how long before expiry to refresh)")
	daemonStartCmd.Flags().BoolP("verbose", "v", false, "verbose logging")
	daemonStartCmd.Flags().Bool("pool", false, "enable auth pool for proactive token monitoring")

	// Logs flags
	daemonLogsCmd.Flags().IntP("lines", "n", 50, "number of lines to show")
	daemonLogsCmd.Flags().BoolP("follow", "f", false, "follow log output")
}

func runDaemonStart(cmd *cobra.Command, args []string) error {
	foreground, _ := cmd.Flags().GetBool("fg")
	interval, _ := cmd.Flags().GetDuration("interval")
	threshold, _ := cmd.Flags().GetDuration("threshold")
	verbose, _ := cmd.Flags().GetBool("verbose")
	usePool, _ := cmd.Flags().GetBool("pool")

	// Load global config to check for PID file setting
	if spmCfg, err := config.LoadSPMConfig(); err == nil {
		if spmCfg.Runtime.PIDFilePath != "" {
			daemon.SetPIDFilePath(spmCfg.Runtime.PIDFilePath)
		}
	}

	// Check if daemon is already running
	running, pid, err := daemon.GetDaemonStatus()
	if err != nil {
		return fmt.Errorf("check daemon status: %w", err)
	}
	if running {
		return fmt.Errorf("daemon already running (pid %d)", pid)
	}

	if foreground {
		return runDaemonForeground(interval, threshold, verbose, usePool)
	}

	return runDaemonBackground(interval, threshold, verbose, usePool)
}

func runDaemonForeground(interval, threshold time.Duration, verbose, usePool bool) error {
	fmt.Println("Starting daemon in foreground mode...")
	if usePool {
		fmt.Println("Auth pool enabled")
	}
	fmt.Println("Press Ctrl+C to stop")

	// Initialize vault and health store
	v := authfile.NewVault(authfile.DefaultVaultPath())
	hs := health.NewStorage(health.DefaultHealthPath())

	cfg := &daemon.Config{
		CheckInterval:    interval,
		RefreshThreshold: threshold,
		Verbose:          verbose,
		UseAuthPool:      usePool,
	}
	// The daemon's own output is its log; alerts go to the configured
	// desktop and webhook channels.
	if spmCfg, err := config.LoadSPMConfig(); err == nil {
		cfg.Notifier = configuredNotifier(spmCfg, notifierChannels{External: true})
		cfg.UpdateChecker, cfg.UpdateCheckInterval = daemonUpdateChecker(spmCfg.Daemon.UpdateCheck)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: alerts disabled (load config: %v)\n", err)
	}

	d := daemon.New(v, hs, cfg)

	return d.Start()
}

func runDaemonBackground(interval, threshold time.Duration, verbose, usePool bool) error {
	// Build the command to run in background
	args := []string{"daemon", "start", "--fg",
		"--interval", interval.String(),
		"--threshold", threshold.String(),
	}
	if verbose {
		args = append(args, "--verbose")
	}
	if usePool {
		args = append(args, "--pool")
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	cmd := exec.Command(executable, args...)

	// Redirect output to log file
	logPath := daemon.LogFilePath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	// Detach from current process group
	cmd.SysProcAttr = getSysProcAttr()

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	logFile.Close()

	// Give it a moment to start and check if it's still running
	time.Sleep(100 * time.Millisecond)

	if cmd.Process != nil {
		fmt.Printf("Daemon started (pid %d)\n", cmd.Process.Pid)
		fmt.Printf("Logs: %s\n", logPath)
		fmt.Printf("Check interval: %v\n", interval)
		fmt.Printf("Refresh threshold: %v before expiry\n", threshold)
	}

	return nil
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	// Load global config to check for PID file setting
	if spmCfg, err := config.LoadSPMConfig(); err == nil {
		if spmCfg.Runtime.PIDFilePath != "" {
			daemon.SetPIDFilePath(spmCfg.Runtime.PIDFilePath)
		}
	}

	running, pid, err := daemon.GetDaemonStatus()
	if err != nil {
		return fmt.Errorf("check daemon status: %w", err)
	}

	if !running {
		fmt.Println("Daemon is not running")
		return nil
	}

	fmt.Printf("Stopping daemon (pid %d)...\n", pid)

	if err := daemon.StopDaemonByPID(pid); err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}

	// Wait for process to exit
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if !daemon.IsProcessRunning(pid) {
			break
		}
	}

	if daemon.IsProcessRunning(pid) {
		fmt.Println("Warning: daemon did not stop within 3 seconds")
		return nil
	}

	daemon.RemovePIDFile()
	fmt.Println("Daemon stopped")
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	// Load global config to check for PID file setting
	if spmCfg, err := config.LoadSPMConfig(); err == nil {
		if spmCfg.Runtime.PIDFilePath != "" {
			daemon.SetPIDFilePath(spmCfg.Runtime.PIDFilePath)
		}
	}

	running, pid, err := daemon.GetDaemonStatus()
	if err != nil {
		return fmt.Errorf("check daemon status: %w", err)
	}

	backup, backupErr := loadAutoBackupStatus()
	jsonOutput, _ := cmd.Flags().GetBool("json")
	out := cmd.OutOrStdout()

	if jsonOutput {
		status := struct {
			Running    bool                 `json:"running"`
			PID        int                  `json:"pid,omitempty"`
			LogFile    string               `json:"log_file"`
			AutoBackup *daemon.BackupStatus `json:"auto_backup,omitempty"`
			Error      string               `json:"auto_backup_error,omitempty"`
		}{Running: running, LogFile: daemon.LogFilePath()}
		if running {
			status.PID = pid
		}
		if backupErr != nil {
			status.Error = backupErr.Error()
		} else {
			status.AutoBackup = &backup
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	if running {
		fmt.Fprintf(out, "Daemon is running (pid %d)\n", pid)
		fmt.Fprintf(out, "Log file: %s\n", daemon.LogFilePath())
	} else {
		fmt.Fprintln(out, "Daemon is not running")
	}
	fmt.Fprintln(out)
	if backupErr != nil {
		fmt.Fprintf(out, "Auto-backup: unknown (%v)\n", backupErr)
		return nil
	}
	printAutoBackupStatus(out, backup, running)
	return nil
}

// loadAutoBackupStatus reads the backup schedule and the daemon's persisted
// backup state.
func loadAutoBackupStatus() (daemon.BackupStatus, error) {
	cfg, err := config.Load()
	if err != nil {
		return daemon.BackupStatus{}, fmt.Errorf("load config: %w", err)
	}
	return daemon.ReadBackupStatus(cfg.Backup)
}

func printAutoBackupStatus(out io.Writer, st daemon.BackupStatus, daemonRunning bool) {
	if !st.Enabled {
		fmt.Fprintln(out, "Auto-backup: disabled")
		fmt.Fprintln(out, "  Enable with: caam daemon auto-backup --enable [--interval 24h] [--keep 5]")
		return
	}
	fmt.Fprintf(out, "Auto-backup: every %s, keeping %d, in %s\n", st.IntervalText, st.KeepLast, st.Location)
	if st.LastBackup.IsZero() {
		fmt.Fprintln(out, "  Last backup: never")
	} else {
		fmt.Fprintf(out, "  Last backup: %s (%s ago)\n", st.LastBackup.Format(time.RFC3339), time.Since(st.LastBackup).Round(time.Minute))
		if st.LastBackupPath != "" {
			fmt.Fprintf(out, "  Last file:   %s\n", st.LastBackupPath)
		}
	}
	switch {
	case !daemonRunning:
		fmt.Fprintln(out, "  Next backup: when the daemon runs (caam daemon start)")
	case !st.NextBackup.After(time.Now()):
		fmt.Fprintln(out, "  Next backup: due at the daemon's next check")
	default:
		fmt.Fprintf(out, "  Next backup: %s (in %s)\n", st.NextBackup.Format(time.RFC3339), time.Until(st.NextBackup).Round(time.Minute))
	}
	fmt.Fprintf(out, "  Backups created: %d\n", st.BackupCount)
	if st.LastError != "" {
		fmt.Fprintf(out, "  Last error: %s (%s)\n", st.LastError, st.LastErrorTime.Format(time.RFC3339))
	}
}

var daemonAutoBackupCmd = &cobra.Command{
	Use:   "auto-backup",
	Short: "Show or configure the daemon's scheduled vault backups",
	Long: `Show or change the daemon's scheduled vault backups.

While the daemon runs, it archives the vault on a schedule and keeps the most
recent archives. With no flags, prints the current schedule and history.

Examples:
  caam daemon auto-backup                         # Show status
  caam daemon auto-backup --enable                # Weekly, keep 5
  caam daemon auto-backup --enable --interval 24h --keep 7
  caam daemon auto-backup --location ~/Backups/caam
  caam daemon auto-backup --disable

A running daemon picks up changes when restarted:
  caam daemon stop && caam daemon start`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runDaemonAutoBackup,
}

func runDaemonAutoBackup(cmd *cobra.Command, args []string) error {
	enable, _ := cmd.Flags().GetBool("enable")
	disable, _ := cmd.Flags().GetBool("disable")
	if enable && disable {
		return fmt.Errorf("--enable and --disable are mutually exclusive")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	changed := false
	if enable || disable {
		cfg.Backup.Enabled = enable
		changed = true
	}
	if cmd.Flags().Changed("interval") {
		interval, _ := cmd.Flags().GetDuration("interval")
		if interval < time.Hour {
			return fmt.Errorf("--interval must be at least 1h, got %s", interval)
		}
		cfg.Backup.Interval = config.Duration(interval)
		changed = true
	}
	if cmd.Flags().Changed("keep") {
		keep, _ := cmd.Flags().GetInt("keep")
		if keep < 1 {
			return fmt.Errorf("--keep must be at least 1, got %d", keep)
		}
		cfg.Backup.KeepLast = keep
		changed = true
	}
	if cmd.Flags().Changed("location") {
		location, _ := cmd.Flags().GetString("location")
		location = strings.TrimSpace(location)
		if strings.HasPrefix(location, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				location = filepath.Join(home, location[2:])
			}
		}
		if location != "" && !filepath.IsAbs(location) {
			abs, err := filepath.Abs(location)
			if err != nil {
				return fmt.Errorf("resolve --location: %w", err)
			}
			location = abs
		}
		cfg.Backup.Location = location
		changed = true
	}

	if changed {
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
	}

	running, _, _ := daemon.GetDaemonStatus()
	st, err := daemon.ReadBackupStatus(cfg.Backup)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	printAutoBackupStatus(out, st, running)
	if changed && running {
		fmt.Fprintln(out, "\nRestart the daemon to apply: caam daemon stop && caam daemon start")
	}
	return nil
}

func runDaemonLogs(cmd *cobra.Command, _ []string) error {
	lines, _ := cmd.Flags().GetInt("lines")
	follow, _ := cmd.Flags().GetBool("follow")

	logPath := daemon.LogFilePath()

	// Check if log file exists
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		fmt.Println("No daemon logs found")
		fmt.Printf("Expected at: %s\n", logPath)
		return nil
	}

	// Use tail to show logs
	tailArgs := []string{"-n", fmt.Sprintf("%d", lines)}
	if follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, logPath)

	tailCmd := exec.Command("tail", tailArgs...)
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr

	return tailCmd.Run()
}

// daemonUpdateChecker returns the release checker for an enabled
// daemon.update_check setting, or nil when checks are off.
func daemonUpdateChecker(uc config.UpdateCheckConfig) (daemon.UpdateChecker, time.Duration) {
	if !uc.Enabled {
		return nil, 0
	}
	cfg := update.DefaultConfig()
	if uc.Channel == "beta" {
		cfg.Channel = update.ChannelBeta
	} else {
		cfg.Channel = update.ChannelStable
	}
	return update.New(cfg), uc.Interval.Duration()
}
