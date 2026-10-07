package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/deploy"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/setup"
	"github.com/spf13/cobra"
)

// setupCmd is the parent command for setup operations.
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Setup distributed auth recovery system",
	Long: `Commands for setting up the distributed auth recovery system.

The distributed system allows automatic authentication recovery across
multiple machines. A local agent handles OAuth flows while coordinators
on remote machines monitor for rate limits and request authentication.

Use 'caam setup distributed' to automatically configure everything.`,
}

func init() {
	rootCmd.AddCommand(setupCmd)
	setupCmd.AddCommand(setupDistributedCmd)
}

// setupDistributedCmd configures the distributed auth recovery system.
var setupDistributedCmd = &cobra.Command{
	Use:   "distributed",
	Short: "Auto-configure distributed auth recovery",
	Long: `Automatically configures the distributed auth recovery system across machines.

This command:
1. Parses your WezTerm config to discover SSH domains
2. Uses Tailscale (if available) for optimal connectivity
3. Deploys coordinators to remote machines
4. Generates local agent configuration

The agent runs locally (where you have a browser) and polls all coordinators
for pending authentication requests.

Examples:
  caam setup distributed                     # Auto-detect everything
  caam setup distributed --dry-run           # Preview what would be done
  caam setup distributed --remotes css,csd   # Only setup specific domains
  caam setup distributed --no-tailscale      # Use public IPs only`,
	RunE: runSetupDistributed,
}

func init() {
	setupDistributedCmd.Flags().String("wezterm-config", "", "path to wezterm.lua (default: auto-detect)")
	setupDistributedCmd.Flags().Bool("use-tailscale", true, "prefer Tailscale IPs when available")
	setupDistributedCmd.Flags().Bool("dry-run", false, "show what would be done without making changes")
	setupDistributedCmd.Flags().Bool("force", false, "skip confirmation prompt")
	setupDistributedCmd.Flags().Bool("yes", false, "skip confirmation prompt (alias for --force)")
	setupDistributedCmd.Flags().Bool("print-script", false, "print a pasteable setup script and exit")
	setupDistributedCmd.Flags().Int("local-port", 7891, "port for local auth-agent")
	setupDistributedCmd.Flags().Int("remote-port", 7890, "port for remote coordinators")
	setupDistributedCmd.Flags().StringSlice("remotes", nil, "limit setup to these domain names")
	setupDistributedCmd.Flags().Bool("no-tailscale", false, "disable Tailscale (use public IPs)")
	setupDistributedCmd.Flags().Bool("rotate-tokens", false, "issue new coordinator API tokens instead of keeping existing ones")
}

func runSetupDistributed(cmd *cobra.Command, args []string) error {
	// Parse flags
	weztermConfig, _ := cmd.Flags().GetString("wezterm-config")
	useTailscale, _ := cmd.Flags().GetBool("use-tailscale")
	noTailscale, _ := cmd.Flags().GetBool("no-tailscale")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	assumeYes, _ := cmd.Flags().GetBool("yes")
	force, _ := cmd.Flags().GetBool("force")
	assumeYes = assumeYes || force
	printScript, _ := cmd.Flags().GetBool("print-script")
	localPort, _ := cmd.Flags().GetInt("local-port")
	remotePort, _ := cmd.Flags().GetInt("remote-port")
	remotes, _ := cmd.Flags().GetStringSlice("remotes")
	rotateTokens, _ := cmd.Flags().GetBool("rotate-tokens")

	if noTailscale {
		useTailscale = false
	}

	opts := setup.Options{
		WezTermConfig: weztermConfig,
		UseTailscale:  useTailscale,
		LocalPort:     localPort,
		RemotePort:    remotePort,
		Remotes:       remotes,
		DryRun:        dryRun,
		RotateTokens:  rotateTokens,
	}

	orch := setup.NewOrchestrator(opts)

	// Discovery phase
	fmt.Println("Discovering machines...")
	discoverCtx, cancelDiscover := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelDiscover()

	if err := orch.Discover(discoverCtx); err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	// Deployment uploads or installs binaries, starts services, and verifies
	// each host in turn; it gets its own, longer budget.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Print discovery results
	orch.PrintDiscoveryResults()

	remoteMachines := orch.GetRemoteMachines()
	if len(remoteMachines) == 0 {
		return fmt.Errorf("no remote machines to setup; add SSH domains to WezTerm config or specify hosts with --remote")
	}

	if printScript {
		script, err := orch.BuildSetupScript(setup.ScriptOptions{
			WezTermConfig: weztermConfig,
			UseTailscale:  useTailscale,
			LocalPort:     localPort,
			RemotePort:    remotePort,
			Remotes:       remotes,
		})
		if err != nil {
			return err
		}
		fmt.Println("=== Pasteable Setup Script ===")
		fmt.Println(script)
		return nil
	}

	// Confirm before proceeding
	if !dryRun && !assumeYes {
		fmt.Printf("\nReady to deploy coordinators to %d remote machine(s).\n", len(remoteMachines))
		fmt.Print("Continue? [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("Cancelled")
			return nil
		}
	}

	fmt.Println()

	// Setup phase
	result, err := orch.Setup(ctx, func(p *setup.SetupProgress) {
		var status string
		switch p.Status {
		case "running":
			status = "⏳"
		case "success":
			status = "✓"
		case "failed":
			status = "✗"
		default:
			status = " "
		}
		fmt.Printf("  %s %s: %s %s\n", status, p.Machine, p.Step, p.Message)
	})

	if err != nil && result == nil {
		return err
	}

	// Print summary
	fmt.Println()
	fmt.Println("=== Setup Summary ===")

	successCount := 0
	for _, dr := range result.DeployResults {
		if dr.Success {
			successCount++
			fmt.Printf("  ✓ %s: deployed successfully\n", dr.Machine)
			if dr.BinaryUpdated {
				fmt.Printf("      Binary: %s -> %s\n", dr.RemoteVersion, dr.LocalVersion)
			}
		} else {
			fmt.Printf("  ✗ %s: %v\n", dr.Machine, dr.Error)
		}
	}

	if result.LocalConfigPath != "" {
		fmt.Printf("\n  Local agent config: %s\n", result.LocalConfigPath)
	}

	if len(result.Errors) > 0 {
		fmt.Printf("\n  Errors: %d\n", len(result.Errors))
		for _, err := range result.Errors {
			fmt.Printf("    - %v\n", err)
		}
	}

	// Instructions
	fmt.Println()
	fmt.Println("=== Next Steps ===")
	fmt.Println()
	if dryRun {
		fmt.Println("Run without --dry-run to apply these changes.")
	} else if successCount > 0 && restartInstalledAgentService(ctx, result.LocalConfigPath) {
		fmt.Println("The auth-agent service was restarted with the updated configuration.")
		fmt.Println("Check it with: caam auth-agent service status")
	} else if successCount > 0 {
		fmt.Println("1. Sign in to the Google accounts the agent should use (once per machine):")
		fmt.Println("     caam auth-agent signin")
		fmt.Println("   and list them under \"accounts\" in:")
		fmt.Printf("     %s\n", result.LocalConfigPath)
		fmt.Println("2. Run the local agent as a login service:")
		fmt.Println("     caam auth-agent service install")
		fmt.Println("   or in the foreground:")
		fmt.Printf("     caam auth-agent --config %s\n", result.LocalConfigPath)
	}

	return nil
}

// restartInstalledAgentService restarts an installed auth-agent service so it
// loads the coordinators and tokens setup just wrote. It reports whether a
// service was restarted.
func restartInstalledAgentService(ctx context.Context, configPath string) bool {
	if configPath == "" {
		return false
	}
	svc, err := deploy.NewAgentService(configPath)
	if err != nil {
		return false
	}
	st, err := svc.Status(ctx)
	if err != nil || !st.Installed {
		return false
	}
	if _, err := svc.Install(ctx); err != nil {
		fmt.Printf("Warning: could not restart the auth-agent service: %v\n", err)
		fmt.Println("Restart it with: caam auth-agent service install")
		return false
	}
	return true
}
