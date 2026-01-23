package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/spf13/cobra"
)

// syncCmd is the parent command for sync operations.
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync vault across machines",
	Long: `Synchronize auth profiles with other machines in your sync pool.

Multi-machine sync allows you to keep your auth tokens synchronized across
multiple machines (work laptop, home desktop, cloud VM, etc.). When a token
is refreshed on one machine, it can automatically push to all other machines.

Quick start:
  caam sync init        # First-time setup wizard
  caam sync status      # Show pool status
  caam sync             # Sync now with all machines

Machine management:
  caam sync add <name> <address>   # Add machine to pool
  caam sync remove <name>          # Remove machine from pool
  caam sync test [name]            # Test connectivity

Auto-sync:
  caam sync enable      # Enable auto-sync after backup/refresh
  caam sync disable     # Disable auto-sync

Troubleshooting:
  caam sync log         # View sync history
  caam sync queue       # View/manage retry queue`,
	RunE: runSync,
}

// syncStatusCmd shows the sync pool status.
var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sync pool status",
	Long: `Show the current status of the sync pool, including:
  - Local machine identity
  - Auto-sync status
  - Machines in pool with their status
  - Profile counts by provider
  - Queue and history statistics`,
	RunE: runSyncStatus,
}

// syncAddCmd adds a machine to the pool.
var syncAddCmd = &cobra.Command{
	Use:   "add <name> <address>",
	Short: "Add machine to sync pool",
	Long: `Add a new machine to the sync pool.

Arguments:
  name      Friendly name for the machine (e.g., "work-laptop")
  address   IP address or hostname, optionally with user/port (e.g., "jeff@192.168.1.100:22")

Examples:
  caam sync add work-laptop 192.168.1.100
  caam sync add home-desktop jeff@10.0.0.50
  caam sync add dev-server admin@dev.example.com:2222
  caam sync add cloud-vm 34.123.45.67 --key ~/.ssh/cloud_key`,
	Args: cobra.ExactArgs(2),
	RunE: runSyncAdd,
}

// syncRemoveCmd removes a machine from the pool.
var syncRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove machine from sync pool",
	Long:  `Remove a machine from the sync pool by its name.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runSyncRemove,
}

// syncTestCmd tests connectivity to machines.
var syncTestCmd = &cobra.Command{
	Use:   "test [name]",
	Short: "Test connectivity to machines",
	Long: `Test SSH connectivity to one or all machines in the sync pool.

Without arguments, tests all machines. With a machine name, tests only that machine.

Examples:
  caam sync test              # Test all machines
  caam sync test work-laptop  # Test specific machine`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSyncTest,
}

// syncEnableCmd enables auto-sync.
var syncEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable auto-sync after backup/refresh",
	Long: `Enable automatic sync after backup or refresh operations.

When enabled, after you backup or refresh a token, it will automatically
be pushed to all machines in your sync pool.`,
	RunE: runSyncEnable,
}

// syncDisableCmd disables auto-sync.
var syncDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable auto-sync",
	Long:  `Disable automatic sync. You can still sync manually with 'caam sync'.`,
	RunE:  runSyncDisable,
}

// syncLogCmd shows sync history.
var syncLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Show sync history",
	Long: `Show recent sync operations, including pushes, pulls, and errors.

Examples:
  caam sync log               # Show last 20 entries
  caam sync log --limit 50    # Show last 50 entries
  caam sync log --errors      # Show only errors`,
	RunE: runSyncLog,
}

// syncDiscoverCmd discovers machines from SSH config.
var syncDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Discover machines from SSH config",
	Long: `Parse ~/.ssh/config to discover potential sync machines.

This filters out:
  - Known code hosting services (github.com, gitlab.com, etc.)
  - Wildcard hosts (Host *)
  - Hosts with ProxyJump (complex setups)`,
	RunE: runSyncDiscover,
}

// syncQueueCmd manages the retry queue.
var syncQueueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Manage sync retry queue",
	Long: `View and manage pending sync operations that failed and are waiting to retry.

Examples:
  caam sync queue             # View pending operations
  caam sync queue --clear     # Clear all pending
  caam sync queue --process   # Retry pending now`,
	RunE: runSyncQueue,
}

// syncEditCmd opens the sync config in an editor.
var syncEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Edit sync configuration",
	Long:  `Open the sync machines CSV file in your default editor.`,
	RunE:  runSyncEdit,
}

// syncInitCmd provides first-time setup wizard.
var syncInitCmd = &cobra.Command{
	Use:   "init",
	Short: "First-time sync setup wizard",
	Long: `Interactive wizard for setting up multi-machine sync.

This wizard helps you:
  1. Discover machines from ~/.ssh/config
  2. Add machines to your sync pool
  3. Test connectivity to machines
  4. Enable or disable auto-sync

Examples:
  caam sync init              # Interactive setup
  caam sync init --discover   # Auto-discover from SSH config
  caam sync init --csv        # Create CSV template only`,
	RunE: runSyncInit,
}

func init() {
	rootCmd.AddCommand(syncCmd)

	// Add subcommands
	syncCmd.AddCommand(syncInitCmd)
	syncCmd.AddCommand(syncStatusCmd)
	syncCmd.AddCommand(syncAddCmd)
	syncCmd.AddCommand(syncRemoveCmd)
	syncCmd.AddCommand(syncTestCmd)
	syncCmd.AddCommand(syncEnableCmd)
	syncCmd.AddCommand(syncDisableCmd)
	syncCmd.AddCommand(syncLogCmd)
	syncCmd.AddCommand(syncDiscoverCmd)
	syncCmd.AddCommand(syncQueueCmd)
	syncCmd.AddCommand(syncEditCmd)

	// Sync command flags
	syncCmd.Flags().String("machine", "", "sync only with specific machine")
	syncCmd.Flags().String("provider", "", "sync only specific provider")
	syncCmd.Flags().String("profile", "", "sync only specific profile")
	syncCmd.Flags().Bool("dry-run", false, "show what would sync without doing it")
	syncCmd.Flags().Bool("force", false, "force sync even if recently synced")
	syncCmd.Flags().Bool("json", false, "output results as JSON")

	// Add command flags
	syncAddCmd.Flags().String("key", "", "path to SSH private key")
	syncAddCmd.Flags().String("user", "", "SSH username")
	syncAddCmd.Flags().String("remote-path", "", "path to caam data on remote")
	syncAddCmd.Flags().Bool("test", true, "test connectivity after adding")

	// Remove command flags
	syncRemoveCmd.Flags().Bool("force", false, "skip confirmation")

	// Test command flags
	syncTestCmd.Flags().Bool("all", false, "test all machines")

	// Log command flags
	syncLogCmd.Flags().Int("limit", 20, "number of entries to show")
	syncLogCmd.Flags().String("machine", "", "filter by machine")
	syncLogCmd.Flags().String("provider", "", "filter by provider")
	syncLogCmd.Flags().Bool("errors", false, "show only errors")
	syncLogCmd.Flags().Bool("json", false, "output as JSON")

	// Init command flags
	syncInitCmd.Flags().Bool("discover", false, "auto-discover from SSH config")
	syncInitCmd.Flags().Bool("csv", false, "create CSV template only")

	// Discover command flags
	syncDiscoverCmd.Flags().Bool("add", false, "add discovered machines to pool")
	syncDiscoverCmd.Flags().Bool("test", false, "test connectivity to discovered")

	// Status command flags
	syncStatusCmd.Flags().Bool("json", false, "output as JSON")

	// Queue command flags
	syncQueueCmd.Flags().Bool("clear", false, "clear all pending retries")
	syncQueueCmd.Flags().Bool("process", false, "process pending retries now")
	syncQueueCmd.Flags().Bool("json", false, "output as JSON")
}

// loadSyncState loads the sync state, handling the case where sync isn't configured yet.
func loadSyncState() (*sync.SyncState, error) {
	state := sync.NewSyncState("")
	if err := state.Load(); err != nil {
		return nil, fmt.Errorf("load sync state: %w", err)
	}
	return state, nil
}

// runSync performs a sync with all or specific machines.
func runSync(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	if len(state.Pool.ListMachines()) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No machines in sync pool.")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Get started:")
		fmt.Fprintln(cmd.OutOrStdout(), "  caam sync add <name> <address>   # Add a machine")
		fmt.Fprintln(cmd.OutOrStdout(), "  caam sync discover               # Find from SSH config")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Example:")
		fmt.Fprintln(cmd.OutOrStdout(), "  caam sync add work-laptop 192.168.1.100")
		return nil
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	machineName, _ := cmd.Flags().GetString("machine")

	machines := state.Pool.ListMachines()
	if machineName != "" {
		// Filter to specific machine
		m := state.Pool.GetMachineByName(machineName)
		if m == nil {
			return fmt.Errorf("machine %q not found in pool; run 'caam sync ls' to see available machines", machineName)
		}
		machines = []*sync.Machine{m}
	}

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Dry run - would sync with:")
		for _, m := range machines {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", m.Name, m.Address)
		}
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Syncing with %d machine(s)...\n\n", len(machines))

	// Create syncer with configuration
	syncer, err := sync.NewSyncer(sync.DefaultSyncerConfig())
	if err != nil {
		return fmt.Errorf("create syncer: %w", err)
	}
	defer syncer.Close()

	// Build context
	ctx := cmd.Context()

	var allResults []*sync.SyncResult
	for _, m := range machines {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s):\n", m.Name, m.Address)

		results, err := syncer.SyncWithMachine(ctx, m)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "    ✗ Error: %v\n\n", err)
			continue
		}

		if len(results) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "    ✓ All profiles up to date")
			fmt.Fprintln(cmd.OutOrStdout())
			continue
		}

		for _, r := range results {
			profile := fmt.Sprintf("%s/%s", r.Operation.Provider, r.Operation.Profile)
			if r.Success {
				switch r.Operation.Direction {
				case sync.SyncPush:
					fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s: pushed (local fresher)\n", profile)
				case sync.SyncPull:
					fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s: pulled (remote fresher)\n", profile)
				case sync.SyncSkip:
					fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s: up to date\n", profile)
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "    ✗ %s: %v\n", profile, r.Error)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())

		allResults = append(allResults, results...)
	}

	// Print summary
	stats := sync.AggregateResults(allResults)
	fmt.Fprintf(cmd.OutOrStdout(), "Sync complete: %d pushed, %d pulled, %d up to date, %d errors\n",
		stats.Pushed, stats.Pulled, stats.Skipped, stats.Failed)

	return nil
}

// runSyncStatus shows the sync pool status.
func runSyncStatus(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// Handle JSON output
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return runSyncStatusJSON(state, out)
	}

	fmt.Fprintln(out, "Sync Status")
	fmt.Fprintln(out, strings.Repeat("─", 50))
	fmt.Fprintln(out)

	// Local machine
	if state.Identity != nil {
		fmt.Fprintf(out, "Local Machine: %s\n", state.Identity.Hostname)
	}

	// Auto-sync status
	autoSyncStatus := "disabled"
	if state.Pool.AutoSync {
		autoSyncStatus = "enabled"
	}
	fmt.Fprintf(out, "Auto-sync: %s\n", autoSyncStatus)

	// Last full sync
	if !state.Pool.LastFullSync.IsZero() {
		fmt.Fprintf(out, "Last full sync: %s\n", formatTimeAgo(state.Pool.LastFullSync))
	}

	fmt.Fprintln(out)

	// Machines
	machines := state.Pool.ListMachines()
	if len(machines) == 0 {
		fmt.Fprintln(out, "Machines in pool: none")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Add machines with:")
		fmt.Fprintln(out, "  caam sync add <name> <address>")
		fmt.Fprintln(out, "  caam sync discover")
	} else {
		fmt.Fprintf(out, "Machines in pool: %d\n", len(machines))
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  %-15s %-20s %-10s %s\n", "NAME", "ADDRESS", "STATUS", "LAST SYNC")

		for _, m := range machines {
			status := getStatusIcon(m.Status) + " " + m.Status
			lastSync := "never"
			if !m.LastSync.IsZero() {
				lastSync = formatTimeAgo(m.LastSync)
			}
			fmt.Fprintf(out, "  %-15s %-20s %-10s %s\n", m.Name, m.Address, status, lastSync)
		}
	}

	fmt.Fprintln(out)

	// Queue and history stats
	queueCount := 0
	historyCount := 0
	if state.Queue != nil {
		queueCount = len(state.Queue.Entries)
	}
	if state.History != nil {
		historyCount = len(state.History.Entries)
	}
	fmt.Fprintf(out, "Queue: %d pending | History: %d entries\n", queueCount, historyCount)

	return nil
}

// runSyncAdd adds a machine to the pool.
func runSyncAdd(cmd *cobra.Command, args []string) error {
	name := args[0]
	address := args[1]

	state, err := loadSyncState()
	if err != nil {
		return err
	}

	// Parse address for user@host:port format
	sshUser, _ := cmd.Flags().GetString("user")
	sshKeyPath, _ := cmd.Flags().GetString("key")
	remotePath, _ := cmd.Flags().GetString("remote-path")
	testAfter, _ := cmd.Flags().GetBool("test")

	// Parse user from address if present
	if strings.Contains(address, "@") {
		parts := strings.SplitN(address, "@", 2)
		if sshUser == "" {
			sshUser = parts[0]
		}
		address = parts[1]
	}

	// Parse port from address if present
	port := sync.DefaultSSHPort
	if strings.Contains(address, ":") {
		parts := strings.Split(address, ":")
		address = parts[0]
		if len(parts) > 1 {
			var parsedPort int
			if _, err := fmt.Sscanf(parts[1], "%d", &parsedPort); err != nil {
				return fmt.Errorf("invalid port %q: %w", parts[1], err)
			}
			if parsedPort < 1 || parsedPort > 65535 {
				return fmt.Errorf("port %d out of valid range (1-65535)", parsedPort)
			}
			port = parsedPort
		}
	}

	machine := sync.NewMachine(name, address)
	machine.Port = port
	machine.SSHUser = sshUser
	machine.SSHKeyPath = sshKeyPath
	machine.RemotePath = remotePath
	machine.Source = sync.SourceManual

	if err := state.Pool.AddMachine(machine); err != nil {
		return fmt.Errorf("add machine: %w", err)
	}

	if err := state.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Added machine %q (%s) to sync pool\n", name, address)

	if testAfter {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintf(cmd.OutOrStdout(), "Testing connectivity to %s...\n", name)
		pool := sync.NewConnectionPool(sync.DefaultConnectOptions())
		defer pool.CloseAll()
		testSyncMachine(cmd.OutOrStdout(), pool, machine)
	}

	return nil
}

// runSyncRemove removes a machine from the pool.
func runSyncRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	state, err := loadSyncState()
	if err != nil {
		return err
	}

	machine := state.Pool.GetMachineByName(name)
	if machine == nil {
		return fmt.Errorf("machine %q not found in pool; run 'caam sync ls' to see available machines", name)
	}

	force, _ := cmd.Flags().GetBool("force")
	if !force {
		fmt.Fprintf(cmd.OutOrStdout(), "Remove machine %q from sync pool? (y/N): ", name)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
	}

	if err := state.Pool.RemoveMachine(machine.ID); err != nil {
		return fmt.Errorf("remove machine: %w", err)
	}

	if err := state.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Removed machine %q from sync pool\n", name)
	return nil
}

// runSyncTest tests connectivity to machines.
func runSyncTest(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	machines := state.Pool.ListMachines()
	if len(machines) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No machines in sync pool.")
		return nil
	}

	// Filter to specific machine if provided
	if len(args) > 0 {
		name := args[0]
		m := state.Pool.GetMachineByName(name)
		if m == nil {
			return fmt.Errorf("machine %q not found in pool; run 'caam sync ls' to see available machines", name)
		}
		machines = []*sync.Machine{m}
	}

	// Create connection pool for testing
	pool := sync.NewConnectionPool(sync.DefaultConnectOptions())
	defer pool.CloseAll()

	if len(machines) == 1 {
		m := machines[0]
		fmt.Fprintf(cmd.OutOrStdout(), "Testing connection to %s (%s)...\n", m.Name, m.Address)
		testSyncMachine(cmd.OutOrStdout(), pool, m)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Testing all machines...")
		fmt.Fprintln(cmd.OutOrStdout())
		passed := 0
		for _, m := range machines {
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s):\n", m.Name, m.Address)
			if testSyncMachine(cmd.OutOrStdout(), pool, m) {
				passed++
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Result: %d/%d machines reachable\n", passed, len(machines))
	}

	return nil
}

func testSyncMachine(out io.Writer, pool *sync.ConnectionPool, m *sync.Machine) bool {
	client, err := pool.Get(m)
	if err != nil {
		fmt.Fprintf(out, "  SSH connection: ✗ %v\n", err)
		return false
	}

	// Try to verify caam vault exists on remote
	vaultPath := remoteVaultPath(m)
	exists, err := client.FileExists(vaultPath)
	if err != nil {
		fmt.Fprintf(out, "  SSH connection: ✓ connected\n")
		fmt.Fprintf(out, "  CAAM vault: ⚠️  could not check (%v)\n", err)
		return true
	}
	if exists {
		fmt.Fprintln(out, "  SSH connection: ✓ connected")
		fmt.Fprintln(out, "  CAAM vault: ✓ found")
	} else {
		fmt.Fprintln(out, "  SSH connection: ✓ connected")
		fmt.Fprintln(out, "  CAAM vault: ⚠️  not found (will be created on first sync)")
	}
	return true
}

func remoteVaultPath(m *sync.Machine) string {
	if m == nil {
		return sync.DefaultSyncerConfig().RemoteVaultPath
	}
	if strings.TrimSpace(m.RemotePath) == "" {
		return sync.DefaultSyncerConfig().RemoteVaultPath
	}
	return path.Join(m.RemotePath, "vault")
}

// runSyncEnable enables auto-sync.
func runSyncEnable(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	state.Pool.AutoSync = true
	state.Pool.Enabled = true

	if err := state.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	machineCount := len(state.Pool.ListMachines())

	fmt.Fprintln(cmd.OutOrStdout(), "Auto-sync is now enabled.")

	if machineCount == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Note: No machines in sync pool yet.")
		fmt.Fprintln(cmd.OutOrStdout(), "Add machines with: caam sync add <name> <address>")
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Will sync with %d machine(s) after backup/refresh.\n", machineCount)
	}

	return nil
}

// runSyncDisable disables auto-sync.
func runSyncDisable(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	state.Pool.AutoSync = false

	if err := state.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Auto-sync is now disabled.")
	fmt.Fprintln(cmd.OutOrStdout(), "Use 'caam sync' to manually sync when needed.")

	return nil
}

// runSyncLog shows sync history.
func runSyncLog(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	limit, _ := cmd.Flags().GetInt("limit")
	machineFilter, _ := cmd.Flags().GetString("machine")
	providerFilter, _ := cmd.Flags().GetString("provider")
	errorsOnly, _ := cmd.Flags().GetBool("errors")

	if state.History == nil || len(state.History.Entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No sync history yet.")
		return nil
	}

	entries := state.History.Entries

	// Apply filters
	var filtered []sync.HistoryEntry
	for _, e := range entries {
		if machineFilter != "" && e.Machine != machineFilter {
			continue
		}
		if providerFilter != "" && e.Provider != providerFilter {
			continue
		}
		if errorsOnly && e.Success {
			continue
		}
		filtered = append(filtered, e)
	}

	// Apply limit
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}

	if len(filtered) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No matching entries.")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Sync History (last %d entries)\n\n", len(filtered))
	fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-15s %-25s %-8s %s\n", "TIME", "MACHINE", "PROFILE", "ACTION", "STATUS")

	for _, e := range filtered {
		status := "✓"
		if !e.Success {
			status = "✗ " + e.Error
		}
		profile := fmt.Sprintf("%s/%s", e.Provider, e.Profile)
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-15s %-25s %-8s %s\n",
			e.Timestamp.Format("2006-01-02 15:04:05"),
			e.Machine,
			profile,
			e.Action,
			status,
		)
	}

	return nil
}

// runSyncDiscover discovers machines from SSH config.
func runSyncDiscover(cmd *cobra.Command, args []string) error {
	machines, err := sync.DiscoverFromSSHConfig()
	if err != nil {
		return fmt.Errorf("discover from SSH config: %w", err)
	}

	if len(machines) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No machines discovered from ~/.ssh/config")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "You can add machines manually:")
		fmt.Fprintln(cmd.OutOrStdout(), "  caam sync add <name> <address>")
		return nil
	}

	state, err := loadSyncState()
	if err != nil {
		return err
	}

	addToPool, _ := cmd.Flags().GetBool("add")

	fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d machine(s) from ~/.ssh/config:\n\n", len(machines))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-15s %-20s %-25s %s\n", "NAME", "ADDRESS", "KEY", "STATUS")

	addedCount := 0
	for _, m := range machines {
		// Check if already in pool
		existing := state.Pool.GetMachineByName(m.Name)
		status := "not in pool"
		if existing != nil {
			status = "already in pool"
		}

		keyPath := m.SSHKeyPath
		if keyPath == "" {
			keyPath = "(default)"
		}

		fmt.Fprintf(cmd.OutOrStdout(), "  %-15s %-20s %-25s %s\n", m.Name, m.Address, keyPath, status)

		if addToPool && existing == nil {
			if err := state.Pool.AddMachine(m); err == nil {
				addedCount++
			}
		}
	}

	if addToPool && addedCount > 0 {
		if err := state.Save(); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\nAdded %d machine(s) to sync pool.\n", addedCount)
	} else if !addToPool && len(machines) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Add discovered machines to pool with:")
		fmt.Fprintln(cmd.OutOrStdout(), "  caam sync discover --add")
	}

	return nil
}

// runSyncQueue manages the retry queue.
func runSyncQueue(cmd *cobra.Command, args []string) error {
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	clear, _ := cmd.Flags().GetBool("clear")
	process, _ := cmd.Flags().GetBool("process")

	if clear {
		if state.Queue != nil {
			state.Queue.Entries = nil
		}
		if err := state.Save(); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Cleared sync queue.")
		return nil
	}

	if process {
		return runSyncQueueProcess(state, cmd.OutOrStdout())
	}

	// Show queue
	if state.Queue == nil || len(state.Queue.Entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Sync queue is empty.")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Sync Queue: %d pending\n\n", len(state.Queue.Entries))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-25s %-15s %-10s %s\n", "PROFILE", "MACHINE", "ATTEMPTS", "LAST ERROR")

	for _, e := range state.Queue.Entries {
		profile := fmt.Sprintf("%s/%s", e.Provider, e.Profile)
		lastError := e.LastError
		if len(lastError) > 30 {
			lastError = lastError[:27] + "..."
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %-25s %-15s %-10d %s\n", profile, e.Machine, e.Attempts, lastError)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Use 'caam sync queue --process' to retry now.")
	fmt.Fprintln(cmd.OutOrStdout(), "Use 'caam sync queue --clear' to clear the queue.")

	return nil
}

// runSyncEdit opens the sync config in an editor.
func runSyncEdit(cmd *cobra.Command, args []string) error {
	csvPath := sync.CSVPath()

	// Ensure the file exists
	created, err := sync.EnsureCSVFile()
	if err != nil {
		return fmt.Errorf("create CSV: %w", err)
	}
	if created {
		fmt.Fprintf(cmd.OutOrStdout(), "Created new sync machines file: %s\n\n", csvPath)
	}

	// Get editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		// Try common editors
		for _, e := range []string{"nano", "vim", "vi"} {
			if _, err := exec.LookPath(e); err == nil {
				editor = e
				break
			}
		}
	}
	if editor == "" {
		return fmt.Errorf("no editor found; set $EDITOR environment variable (e.g., 'export EDITOR=vim' or 'export EDITOR=nano')")
	}

	// Open editor
	c := exec.Command(editor, csvPath)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return c.Run()
}

// runSyncInit implements the first-time setup wizard.
func runSyncInit(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	reader := bufio.NewReader(os.Stdin)

	csvOnly, _ := cmd.Flags().GetBool("csv")
	autoDiscover, _ := cmd.Flags().GetBool("discover")

	// CSV-only mode
	if csvOnly {
		csvPath := sync.CSVPath()
		created, err := sync.EnsureCSVFile()
		if err != nil {
			return fmt.Errorf("create CSV: %w", err)
		}
		if created {
			fmt.Fprintf(out, "Created %s\n", csvPath)
			fmt.Fprintln(out, "Edit this file to add your machines, then run 'caam sync init' again.")
		} else {
			fmt.Fprintf(out, "CSV file already exists: %s\n", csvPath)
		}
		return nil
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Welcome to CAAM Sync Setup!")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "This will help you set up syncing between your machines.")
	fmt.Fprintln(out, "")

	// Load state
	state, err := loadSyncState()
	if err != nil {
		return err
	}

	// Step 1: Create sync pool
	fmt.Fprintln(out, "Step 1: Create your sync pool")
	fmt.Fprintln(out, "")

	// Discover machines from SSH config
	discovered, err := sync.DiscoverFromSSHConfig()
	if err != nil {
		discovered = nil // Non-fatal
	}

	var selectedMachines []*sync.Machine

	if len(discovered) > 0 {
		fmt.Fprintln(out, "  Discovered hosts from ~/.ssh/config:")
		for i, m := range discovered {
			fmt.Fprintf(out, "    [%d] %s (%s)\n", i+1, m.Name, m.Address)
		}
		fmt.Fprintln(out, "    [a] Add all discovered hosts")
		fmt.Fprintln(out, "    [m] Manually add a machine")
		fmt.Fprintln(out, "    [s] Skip for now")
		fmt.Fprintln(out, "")

		if autoDiscover {
			selectedMachines = discovered
			fmt.Fprintln(out, "  Auto-selecting all discovered hosts...")
		} else {
			fmt.Fprint(out, "  Choice: ")
			choice, _ := reader.ReadString('\n')
			choice = strings.TrimSpace(strings.ToLower(choice))

			switch choice {
			case "a":
				selectedMachines = discovered
			case "m":
				m, err := promptForMachine(reader, out)
				if err != nil {
					fmt.Fprintf(out, "  Error: %v\n", err)
				} else if m != nil {
					selectedMachines = append(selectedMachines, m)
				}
			case "s":
				// Skip
			default:
				// Try to parse as number
				var idx int
				if _, err := fmt.Sscanf(choice, "%d", &idx); err == nil && idx > 0 && idx <= len(discovered) {
					selectedMachines = append(selectedMachines, discovered[idx-1])
				}
			}
		}
	} else {
		fmt.Fprintln(out, "  No hosts found in ~/.ssh/config")
		fmt.Fprintln(out, "")
		fmt.Fprint(out, "  Would you like to add a machine manually? (y/N): ")
		choice, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(choice)) == "y" {
			m, err := promptForMachine(reader, out)
			if err != nil {
				fmt.Fprintf(out, "  Error: %v\n", err)
			} else if m != nil {
				selectedMachines = append(selectedMachines, m)
			}
		}
	}

	// Add selected machines to pool
	for _, m := range selectedMachines {
		if err := state.Pool.AddMachine(m); err != nil {
			// Ignore duplicate errors
			if !strings.Contains(err.Error(), "already exists") {
				fmt.Fprintf(out, "  Warning: could not add %s: %v\n", m.Name, err)
			}
		}
	}

	fmt.Fprintln(out, "")

	// Step 2: Test connectivity
	if len(selectedMachines) > 0 {
		fmt.Fprintln(out, "Step 2: Test connectivity")
		fmt.Fprintln(out, "")

		pool := sync.NewConnectionPool(sync.DefaultConnectOptions())
		defer pool.CloseAll()

		var online, offline int
		for _, m := range selectedMachines {
			fmt.Fprintf(out, "  Testing %s...", m.Name)
			start := time.Now()

			client, err := pool.Get(m)
			latency := time.Since(start)

			if err != nil {
				fmt.Fprintf(out, " ✗ Failed: %v\n", err)
				m.SetError(err.Error())
				offline++
			} else {
				fmt.Fprintf(out, " ✓ OK (%dms)\n", latency.Milliseconds())
				m.SetOnline()
				_ = client // Keep connection for potential later use
				online++
			}
		}

		fmt.Fprintln(out, "")
		if offline > 0 {
			fmt.Fprintf(out, "  %d machine(s) failed. Continue anyway? (Y/n): ", offline)
			choice, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(choice)) == "n" {
				fmt.Fprintln(out, "  Aborted.")
				return nil
			}
		}
	}

	// Step 3: Auto-sync
	fmt.Fprintln(out, "Step 3: Enable auto-sync?")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  Auto-sync automatically pushes fresh tokens when you backup or refresh.")
	fmt.Fprintln(out, "")
	fmt.Fprint(out, "  Enable auto-sync? (y/N): ")
	choice, _ := reader.ReadString('\n')
	if strings.TrimSpace(strings.ToLower(choice)) == "y" {
		state.Pool.AutoSync = true
		state.Pool.Enabled = true
	}

	// Save state
	if err := state.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Also save to CSV
	machines := state.Pool.ListMachines()
	if len(machines) > 0 {
		if err := sync.SaveToCSV(machines); err != nil {
			fmt.Fprintf(out, "Warning: could not save to CSV: %v\n", err)
		}
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Setup complete!")

	online := len(state.Pool.OnlineMachines())
	offline := len(state.Pool.OfflineMachines())
	autoSyncStr := "disabled"
	if state.Pool.AutoSync {
		autoSyncStr = "enabled"
	}
	fmt.Fprintf(out, "  Machines in pool: %d (%d online, %d offline)\n", state.Pool.MachineCount(), online, offline)
	fmt.Fprintf(out, "  Auto-sync: %s\n", autoSyncStr)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Run 'caam sync' to sync now, or it will happen automatically.")

	return nil
}

// promptForMachine prompts the user to enter machine details.
func promptForMachine(reader *bufio.Reader, out io.Writer) (*sync.Machine, error) {
	fmt.Fprint(out, "    Machine name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}

	fmt.Fprint(out, "    Address (IP or hostname): ")
	address, _ := reader.ReadString('\n')
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("address required")
	}

	// Parse address for user@host:port format
	sshUser := ""
	if strings.Contains(address, "@") {
		parts := strings.SplitN(address, "@", 2)
		sshUser = parts[0]
		address = parts[1]
	}

	port := sync.DefaultSSHPort
	if strings.Contains(address, ":") {
		parts := strings.Split(address, ":")
		address = parts[0]
		if len(parts) > 1 {
			var parsedPort int
			if _, err := fmt.Sscanf(parts[1], "%d", &parsedPort); err != nil {
				return nil, fmt.Errorf("invalid port %q: %w", parts[1], err)
			}
			if parsedPort < 1 || parsedPort > 65535 {
				return nil, fmt.Errorf("port %d out of valid range (1-65535)", parsedPort)
			}
			port = parsedPort
		}
	}

	m := sync.NewMachine(name, address)
	m.Port = port
	m.SSHUser = sshUser
	m.Source = sync.SourceManual

	fmt.Fprint(out, "    SSH key path (optional, press Enter to skip): ")
	keyPath, _ := reader.ReadString('\n')
	keyPath = strings.TrimSpace(keyPath)
	if keyPath != "" {
		m.SSHKeyPath = keyPath
	}

	return m, nil
}

// runSyncStatusJSON outputs sync status as JSON.
func runSyncStatusJSON(state *sync.SyncState, out io.Writer) error {
	type machineJSON struct {
		Name     string     `json:"name"`
		Address  string     `json:"address"`
		Status   string     `json:"status"`
		LastSync *time.Time `json:"last_sync,omitempty"`
	}

	type statusJSON struct {
		LocalMachine string        `json:"local_machine,omitempty"`
		AutoSync     bool          `json:"auto_sync"`
		LastFullSync *time.Time    `json:"last_full_sync,omitempty"`
		Machines     []machineJSON `json:"machines"`
		QueuePending int           `json:"queue_pending"`
		HistoryCount int           `json:"history_count"`
	}

	output := statusJSON{
		AutoSync: state.Pool.AutoSync,
		Machines: []machineJSON{}, // Initialize as empty array, not nil
	}

	if state.Identity != nil {
		output.LocalMachine = state.Identity.Hostname
	}

	if !state.Pool.LastFullSync.IsZero() {
		t := state.Pool.LastFullSync
		output.LastFullSync = &t
	}

	for _, m := range state.Pool.ListMachines() {
		mj := machineJSON{
			Name:    m.Name,
			Address: m.Address,
			Status:  m.Status,
		}
		if !m.LastSync.IsZero() {
			t := m.LastSync
			mj.LastSync = &t
		}
		output.Machines = append(output.Machines, mj)
	}

	if state.Queue != nil {
		output.QueuePending = len(state.Queue.Entries)
	}
	if state.History != nil {
		output.HistoryCount = len(state.History.Entries)
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// runSyncQueueProcess processes pending queue entries.
func runSyncQueueProcess(state *sync.SyncState, out io.Writer) error {
	if state.Queue == nil || len(state.Queue.Entries) == 0 {
		fmt.Fprintln(out, "Sync queue is empty.")
		return nil
	}

	ctx := context.Background()
	syncer, err := sync.NewSyncer(sync.DefaultSyncerConfig())
	if err != nil {
		return fmt.Errorf("create syncer: %w", err)
	}
	defer syncer.Close()

	fmt.Fprintln(out, "Processing pending retries...")
	fmt.Fprintln(out, "")

	// Track entries to remove after processing (avoid modifying slice during iteration)
	type entryKey struct {
		provider, profile, machine string
	}
	var toRemove []entryKey

	processed := 0
	total := len(state.Queue.Entries)
	for _, entry := range state.Queue.Entries {
		profile := fmt.Sprintf("%s/%s", entry.Provider, entry.Profile)
		fmt.Fprintf(out, "  Retrying %s on %s...", profile, entry.Machine)

		// Find the specific machine that failed
		machine := state.Pool.GetMachine(entry.Machine)
		if machine == nil {
			fmt.Fprintf(out, " ✗ machine not found in pool\n")
			// Machine was removed from pool, remove from queue
			toRemove = append(toRemove, entryKey{entry.Provider, entry.Profile, entry.Machine})
			continue
		}

		// Sync only with the specific machine that failed, not all machines
		result, err := syncer.SyncProfileWithMachine(ctx, entry.Provider, entry.Profile, machine)
		if err != nil {
			fmt.Fprintf(out, " ✗ %v\n", err)
			state.AddToQueue(entry.Provider, entry.Profile, entry.Machine, err.Error())
			continue
		}

		if result.Success {
			fmt.Fprintln(out, " ✓ OK")
			toRemove = append(toRemove, entryKey{entry.Provider, entry.Profile, entry.Machine})
			processed++
		} else {
			errMsg := "sync failed"
			if result.Error != nil {
				errMsg = result.Error.Error()
			}
			fmt.Fprintf(out, " ✗ %s\n", errMsg)
			state.AddToQueue(entry.Provider, entry.Profile, entry.Machine, errMsg)
		}
	}

	// Remove successful entries from queue
	for _, key := range toRemove {
		state.RemoveFromQueue(key.provider, key.profile, key.machine)
	}

	// Save updated state
	if err := state.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "Processed %d/%d queue entries\n", processed, total)
	return nil
}

// Helper functions

// getStatusIcon returns an icon for a machine status.
func getStatusIcon(status string) string {
	switch status {
	case sync.StatusOnline:
		return "🟢"
	case sync.StatusOffline:
		return "🔴"
	case sync.StatusSyncing:
		return "🔄"
	case sync.StatusError:
		return "⚠️"
	default:
		return "⚪"
	}
}

// formatTimeAgo formats a time as "X ago".
func formatTimeAgo(t time.Time) string {
	diff := time.Since(t)

	if diff < time.Minute {
		return "just now"
	} else if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", mins)
	} else if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}

	days := int(diff.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}
