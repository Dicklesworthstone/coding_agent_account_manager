// Package setup provides orchestration for setting up the distributed auth recovery system.
package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/deploy"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/tailscale"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/wezterm"
)

// Role indicates whether a machine runs coordinator or agent.
type Role string

const (
	RoleCoordinator Role = "coordinator"
	RoleAgent       Role = "agent"
)

// DiscoveredMachine represents a machine discovered from WezTerm and Tailscale.
type DiscoveredMachine struct {
	Name          string // Display name
	WezTermDomain string // WezTerm SSH domain name (e.g., "csd")
	PublicIP      string // Public/original IP from wezterm config
	TailscaleIP   string // Tailscale IP if on tailnet
	Username      string // SSH username
	Port          int    // SSH port
	IdentityFile  string // Path to SSH key
	Role          Role   // coordinator or agent
	IsReachable   bool   // Whether we can connect
	IsLocal       bool   // Whether this is the local machine
}

// MachineOverride provides manual address/config overrides for a machine.
type MachineOverride struct {
	// PreferredIP overrides both public and tailscale IPs.
	PreferredIP string `json:"preferred_ip,omitempty"`
	// Username overrides the SSH username from WezTerm.
	Username string `json:"username,omitempty"`
	// Port overrides the SSH port.
	Port int `json:"port,omitempty"`
	// IdentityFile overrides the SSH key path.
	IdentityFile string `json:"identity_file,omitempty"`
	// Disabled skips this machine during discovery.
	Disabled bool `json:"disabled,omitempty"`
}

// DiscoveryWarning represents a non-fatal issue during discovery.
type DiscoveryWarning struct {
	Machine string // Machine name or empty for global
	Code    string // Warning code (e.g., "NO_TAILSCALE_MATCH")
	Message string // Human-readable description
}

// Options configures the setup process.
type Options struct {
	// WezTermConfig is the path to wezterm.lua. Auto-detected if empty.
	WezTermConfig string

	// UseTailscale enables Tailscale IP preference when available.
	UseTailscale bool

	// LocalPort is the port for the local auth-agent.
	LocalPort int

	// RemotePort is the port for remote coordinators.
	RemotePort int

	// Remotes limits setup to these domain names. Empty means all.
	Remotes []string

	// ManualOverrides maps WezTerm domain names to manual address/config overrides.
	// Use this when discovery produces wrong results or for machines not on tailnet.
	ManualOverrides map[string]MachineOverride

	// DryRun shows what would be done without making changes.
	DryRun bool

	// Logger for structured logging.
	Logger *slog.Logger
}

// DefaultOptions returns the default setup options.
func DefaultOptions() Options {
	return Options{
		UseTailscale: true,
		LocalPort:    7891,
		RemotePort:   7890,
		Logger:       slog.Default(),
	}
}

// Orchestrator handles the setup process.
type Orchestrator struct {
	opts              Options
	logger            *slog.Logger
	weztermConfig     *wezterm.Config
	tailscale         *tailscale.Client
	tailscaleVersion  string // CLI version for debugging schema drift
	localMachine      *DiscoveredMachine
	remoteMachines    []*DiscoveredMachine
	discoveryWarnings []DiscoveryWarning
}

// ScriptOptions controls the generated setup script.
type ScriptOptions struct {
	WezTermConfig string
	UseTailscale  bool
	LocalPort     int
	RemotePort    int
	Remotes       []string
}

// NewOrchestrator creates a new setup orchestrator.
func NewOrchestrator(opts Options) *Orchestrator {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Orchestrator{
		opts:   opts,
		logger: opts.Logger,
	}
}

// Discover discovers all machines from WezTerm config and Tailscale.
func (o *Orchestrator) Discover(ctx context.Context) error {
	o.logger.Info("discovering machines...")

	// Find WezTerm config
	configPath := o.opts.WezTermConfig
	if configPath == "" {
		configPath = wezterm.FindConfigPath()
		if configPath == "" {
			return fmt.Errorf("WezTerm config not found. Try --wezterm-config flag")
		}
	}

	o.logger.Info("parsing WezTerm config", "path", configPath)

	// Parse WezTerm config
	cfg, err := wezterm.ParseConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to parse WezTerm config: %w", err)
	}
	o.weztermConfig = cfg

	if len(cfg.SSHDomains) == 0 {
		return fmt.Errorf("no SSH domains found in WezTerm config")
	}

	o.logger.Info("found SSH domains", "count", len(cfg.SSHDomains))

	// Check Tailscale availability
	if o.opts.UseTailscale {
		o.tailscale = tailscale.NewClient()
		if !o.tailscale.IsAvailable(ctx) {
			o.logger.Info("Tailscale not available, using public IPs only")
			o.addWarning("", "TAILSCALE_UNAVAILABLE", "Tailscale not running or not accessible; using public IPs only")
			o.tailscale = nil
		} else {
			// Log CLI version for debugging schema drift issues
			o.tailscaleVersion = o.tailscale.GetVersionString(ctx)
			o.logger.Info("Tailscale available", "cli_version", o.tailscaleVersion)
		}
	}

	// Discover local machine
	if err := o.discoverLocal(ctx); err != nil {
		return err
	}

	// Discover remote machines
	if err := o.discoverRemotes(ctx); err != nil {
		return err
	}

	return nil
}

// discoverLocal identifies the local machine.
func (o *Orchestrator) discoverLocal(ctx context.Context) error {
	hostname, _ := os.Hostname()
	o.logger.Info("local hostname", "hostname", hostname)

	local := &DiscoveredMachine{
		Name:    hostname,
		Role:    RoleAgent,
		IsLocal: true,
	}

	// Check if we're on a Tailscale network
	if o.tailscale != nil {
		self, err := o.tailscale.GetSelf(ctx)
		if err == nil && self != nil {
			local.Name = self.HostName
			local.TailscaleIP = self.GetIPv4()
			o.logger.Info("Tailscale identity",
				"hostname", self.HostName,
				"ip", local.TailscaleIP)
		}
	}

	o.localMachine = local
	return nil
}

// discoverRemotes discovers remote machines from WezTerm domains.
func (o *Orchestrator) discoverRemotes(ctx context.Context) error {
	var machines []*DiscoveredMachine

	// Get Tailscale peers for cross-referencing
	var peers []*tailscale.Peer
	var tailscaleStatus *tailscale.Status
	if o.tailscale != nil {
		var err error
		tailscaleStatus, err = o.tailscale.GetStatus(ctx)
		if err == nil {
			// Check for parsing warnings (schema drift)
			if tailscaleStatus.HasWarnings() {
				for _, w := range tailscaleStatus.Warnings {
					o.addWarning("", "TAILSCALE_PARSE_WARNING", w.String())
				}
			}
			for _, peer := range tailscaleStatus.Peer {
				peers = append(peers, peer)
			}
			o.logger.Info("Tailscale peers found", "count", len(peers))
		} else {
			o.addWarning("", "TAILSCALE_STATUS_ERROR", fmt.Sprintf("failed to get status: %v", err))
		}
	}

	for _, domain := range o.weztermConfig.SSHDomains {
		// Skip if not in remotes filter
		if len(o.opts.Remotes) > 0 && !contains(o.opts.Remotes, domain.Name) {
			continue
		}

		// Check for manual override
		if override, ok := o.opts.ManualOverrides[domain.Name]; ok {
			if override.Disabled {
				o.logger.Info("skipping disabled domain", "domain", domain.Name)
				continue
			}
		}

		machine := &DiscoveredMachine{
			Name:          domain.Name,
			WezTermDomain: domain.Name,
			PublicIP:      domain.RemoteAddress,
			Username:      domain.Username,
			Port:          domain.Port,
			IdentityFile:  domain.IdentityFile,
			Role:          RoleCoordinator,
		}

		if machine.Port == 0 {
			machine.Port = 22
		}

		// Apply manual overrides if present
		if override, ok := o.opts.ManualOverrides[domain.Name]; ok {
			if override.PreferredIP != "" {
				machine.PublicIP = override.PreferredIP
				machine.TailscaleIP = "" // Clear tailscale IP when manually overridden
				o.logger.Info("using manual override for IP",
					"domain", domain.Name,
					"ip", override.PreferredIP)
			}
			if override.Username != "" {
				machine.Username = override.Username
			}
			if override.Port != 0 {
				machine.Port = override.Port
			}
			if override.IdentityFile != "" {
				machine.IdentityFile = override.IdentityFile
			}
		} else {
			// Try to find Tailscale IP (only if no manual override)
			if o.tailscale != nil && len(peers) > 0 {
				matchedByIP := false
				var ambiguousMatches []string

				// Try matching by IP first
				for _, peer := range peers {
					if peer.GetIPv4() == domain.RemoteAddress {
						machine.TailscaleIP = peer.GetIPv4()
						machine.Name = peer.HostName
						matchedByIP = true
						break
					}
				}

				// If no match by IP, try fuzzy hostname match
				if !matchedByIP {
					peer, _ := o.tailscale.FindPeerByHostname(ctx, domain.Name)
					if peer != nil {
						machine.TailscaleIP = peer.GetIPv4()
						machine.Name = peer.HostName
					} else {
						// Check for possible ambiguous matches
						domainLower := strings.ToLower(domain.Name)
						for _, p := range peers {
							hostLower := strings.ToLower(p.HostName)
							if strings.Contains(hostLower, domainLower) || strings.Contains(domainLower, hostLower) {
								ambiguousMatches = append(ambiguousMatches, p.HostName)
							}
						}

						if len(ambiguousMatches) > 1 {
							o.addWarning(domain.Name, "AMBIGUOUS_MATCH",
								fmt.Sprintf("multiple potential Tailscale peers: %s", strings.Join(ambiguousMatches, ", ")))
						} else if machine.TailscaleIP == "" {
							o.addWarning(domain.Name, "NO_TAILSCALE_MATCH",
								fmt.Sprintf("no Tailscale peer found matching '%s'; using public IP", domain.Name))
						}
					}
				}
			}
		}

		machines = append(machines, machine)

		o.logger.Info("discovered remote",
			"domain", domain.Name,
			"public_ip", machine.PublicIP,
			"tailscale_ip", machine.TailscaleIP,
			"user", machine.Username)
	}

	o.remoteMachines = machines
	return nil
}

// addWarning adds a discovery warning.
func (o *Orchestrator) addWarning(machine, code, message string) {
	w := DiscoveryWarning{
		Machine: machine,
		Code:    code,
		Message: message,
	}
	o.discoveryWarnings = append(o.discoveryWarnings, w)
	o.logger.Warn("discovery warning", "machine", machine, "code", code, "message", message)
}

// GetDiscoveryWarnings returns all warnings from the discovery process.
func (o *Orchestrator) GetDiscoveryWarnings() []DiscoveryWarning {
	return o.discoveryWarnings
}

// HasDiscoveryWarnings returns true if there were any warnings during discovery.
func (o *Orchestrator) HasDiscoveryWarnings() bool {
	return len(o.discoveryWarnings) > 0
}

// GetTailscaleVersion returns the detected Tailscale CLI version.
func (o *Orchestrator) GetTailscaleVersion() string {
	return o.tailscaleVersion
}

// GetDiscoveredMachines returns all discovered machines.
func (o *Orchestrator) GetDiscoveredMachines() []*DiscoveredMachine {
	var all []*DiscoveredMachine
	if o.localMachine != nil {
		all = append(all, o.localMachine)
	}
	all = append(all, o.remoteMachines...)
	return all
}

// GetRemoteMachines returns just the remote machines.
func (o *Orchestrator) GetRemoteMachines() []*DiscoveredMachine {
	return o.remoteMachines
}

// GetLocalMachine returns the local machine.
func (o *Orchestrator) GetLocalMachine() *DiscoveredMachine {
	return o.localMachine
}

// BuildSetupScript returns a pasteable bash script for running setup and follow-up checks.
func (o *Orchestrator) BuildSetupScript(opts ScriptOptions) (string, error) {
	if o.localMachine == nil {
		return "", fmt.Errorf("discovery not run")
	}
	if len(o.remoteMachines) == 0 {
		return "", fmt.Errorf("no remote machines discovered")
	}

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("# 1) Setup coordinators and local agent config\n")
	b.WriteString("caam setup distributed --yes")
	if opts.WezTermConfig != "" {
		b.WriteString(" --wezterm-config ")
		b.WriteString(shellQuote(opts.WezTermConfig))
	}
	if !opts.UseTailscale {
		b.WriteString(" --no-tailscale")
	}
	if opts.LocalPort != 0 && opts.LocalPort != 7891 {
		b.WriteString(fmt.Sprintf(" --local-port %d", opts.LocalPort))
	}
	if opts.RemotePort != 0 && opts.RemotePort != 7890 {
		b.WriteString(fmt.Sprintf(" --remote-port %d", opts.RemotePort))
	}
	if len(opts.Remotes) > 0 {
		b.WriteString(" --remotes ")
		b.WriteString(shellQuote(strings.Join(opts.Remotes, ",")))
	}
	b.WriteString("\n\n")

	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	configPath := filepath.Join(configDir, "caam", "distributed-agent.json")

	b.WriteString("# 2) Inspect and edit the local agent config\n")
	b.WriteString(fmt.Sprintf("CONFIG_PATH=%s\n", shellQuote(configPath)))
	b.WriteString("echo \"Edit $CONFIG_PATH to set chrome_profile and accounts\"\n\n")

	b.WriteString("# 3) Check coordinator service status on remotes\n")
	for _, m := range o.remoteMachines {
		sshCmd := buildSSHCommand(m, opts)
		b.WriteString(fmt.Sprintf("%s -- %s\n", sshCmd, shellQuote("systemctl --user status caam-coordinator --no-pager")))
	}
	b.WriteString("\n")

	b.WriteString("# 4) Smoke test coordinator status endpoints\n")
	for _, m := range o.remoteMachines {
		addr := m.PublicIP
		if opts.UseTailscale && m.TailscaleIP != "" {
			addr = m.TailscaleIP
		}
		b.WriteString(fmt.Sprintf("curl -fsS http://%s:%d/status\n", addr, opts.RemotePort))
	}
	b.WriteString("\n")
	b.WriteString("# 5) Start the local auth agent\n")
	b.WriteString("caam auth-agent --config \"$CONFIG_PATH\"\n")
	return b.String(), nil
}

// TestConnectivity tests SSH connectivity to a machine.
func (o *Orchestrator) TestConnectivity(ctx context.Context, m *DiscoveredMachine) error {
	machine := o.toSyncMachine(m)

	opts := sync.ConnectOptions{
		Timeout:  10 * time.Second,
		UseAgent: true,
	}

	result := sync.TestMachineConnectivity(machine, opts)
	m.IsReachable = result.Success

	if !result.Success {
		return result.Error
	}
	return nil
}

// toSyncMachine converts a DiscoveredMachine to a sync.Machine.
func (o *Orchestrator) toSyncMachine(m *DiscoveredMachine) *sync.Machine {
	// Prefer Tailscale IP if available and enabled
	address := m.PublicIP
	if o.opts.UseTailscale && m.TailscaleIP != "" {
		address = m.TailscaleIP
	}

	return &sync.Machine{
		Name:       m.Name,
		Address:    address,
		Port:       m.Port,
		SSHUser:    m.Username,
		SSHKeyPath: m.IdentityFile,
	}
}

// SetupProgress tracks the progress of a setup operation.
type SetupProgress struct {
	Machine  string
	Step     string
	Status   string // pending, running, success, failed
	Message  string
	Started  time.Time
	Finished time.Time
}

// SetupResult contains the results of the setup process.
type SetupResult struct {
	LocalConfigPath   string
	CoordinatorConfig string
	DeployResults     []*deploy.DeployResult
	Errors            []error
}

// Setup performs the full setup process.
func (o *Orchestrator) Setup(ctx context.Context, progress func(*SetupProgress)) (*SetupResult, error) {
	result := &SetupResult{}

	if len(o.remoteMachines) == 0 {
		return nil, fmt.Errorf("no remote machines to setup")
	}

	// Deploy coordinators to remote machines
	for _, machine := range o.remoteMachines {
		p := &SetupProgress{
			Machine: machine.Name,
			Step:    "deploy",
			Status:  "running",
			Started: time.Now(),
		}
		if progress != nil {
			progress(p)
		}

		if o.opts.DryRun {
			o.logger.Info("[dry-run] would deploy coordinator",
				"machine", machine.Name,
				"address", o.getAddress(machine))
			p.Status = "success"
			p.Message = "dry-run: skipped"
			p.Finished = time.Now()
			if progress != nil {
				progress(p)
			}
			continue
		}

		deployResult, err := o.deployCoordinator(ctx, machine)
		if err != nil {
			p.Status = "failed"
			p.Message = err.Error()
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", machine.Name, err))
			o.logger.Error("deployment failed",
				"machine", machine.Name,
				"error", err)
		} else {
			p.Status = "success"
			p.Message = "deployed successfully"
			o.logger.Info("deployment succeeded", "machine", machine.Name)
		}
		p.Finished = time.Now()

		if deployResult != nil {
			result.DeployResults = append(result.DeployResults, deployResult)
		}

		if progress != nil {
			progress(p)
		}
	}

	// Generate local agent config
	localConfigPath, err := o.generateLocalConfig()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("local config: %w", err))
	} else {
		result.LocalConfigPath = localConfigPath
	}

	return result, nil
}

// deployCoordinator deploys a coordinator to a remote machine.
func (o *Orchestrator) deployCoordinator(ctx context.Context, m *DiscoveredMachine) (*deploy.DeployResult, error) {
	syncMachine := o.toSyncMachine(m)

	deployer := deploy.NewDeployer(syncMachine, o.logger)
	if err := deployer.Connect(); err != nil {
		return nil, fmt.Errorf("connect failed: %w", err)
	}
	defer deployer.Disconnect()

	config := deploy.DefaultCoordinatorConfig()
	config.Port = o.opts.RemotePort

	return deployer.DeployCoordinator(ctx, config)
}

// generateLocalConfig generates the local agent configuration.
func (o *Orchestrator) generateLocalConfig() (string, error) {
	// Build coordinator endpoints
	var coordinators []*agent.CoordinatorEndpoint
	for _, m := range o.remoteMachines {
		addr := o.getAddress(m)
		coordinators = append(coordinators, &agent.CoordinatorEndpoint{
			Name:        m.WezTermDomain,
			URL:         fmt.Sprintf("http://%s:%d", addr, o.opts.RemotePort),
			DisplayName: m.Name,
		})
	}

	config := struct {
		Port          int                          `json:"port"`
		Coordinators  []*agent.CoordinatorEndpoint `json:"coordinators"`
		PollInterval  string                       `json:"poll_interval"`
		Accounts      []string                     `json:"accounts"`
		Strategy      string                       `json:"strategy"`
		ChromeProfile string                       `json:"chrome_profile"`
	}{
		Port:          o.opts.LocalPort,
		Coordinators:  coordinators,
		PollInterval:  "2s",
		Accounts:      []string{},
		Strategy:      "lru",
		ChromeProfile: "",
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}

	// Determine config path
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}

	configPath := filepath.Join(configDir, "caam", "distributed-agent.json")

	if o.opts.DryRun {
		o.logger.Info("[dry-run] would write local agent config",
			"path", configPath)
		fmt.Println("--- distributed-agent.json ---")
		fmt.Println(string(data))
		fmt.Println("---")
		return configPath, nil
	}

	// Create directory
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return "", err
	}

	// Atomic write: write to temp file, sync, then rename
	tmpPath := configPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create temp config file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp config file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("sync temp config file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close temp config file: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename temp config file: %w", err)
	}

	o.logger.Info("wrote local agent config", "path", configPath)
	return configPath, nil
}

// getAddress returns the best address to use for a machine.
func (o *Orchestrator) getAddress(m *DiscoveredMachine) string {
	if o.opts.UseTailscale && m.TailscaleIP != "" {
		return m.TailscaleIP
	}
	return m.PublicIP
}

// PrintDiscoveryResults prints a summary of discovered machines.
func (o *Orchestrator) PrintDiscoveryResults() {
	fmt.Println()
	fmt.Println("=== Discovery Results ===")

	if o.tailscaleVersion != "" {
		fmt.Printf("Tailscale CLI version: %s\n", o.tailscaleVersion)
	}

	if o.localMachine != nil {
		fmt.Printf("\nLocal Machine:\n")
		fmt.Printf("  Name: %s\n", o.localMachine.Name)
		if o.localMachine.TailscaleIP != "" {
			fmt.Printf("  Tailscale IP: %s\n", o.localMachine.TailscaleIP)
		}
		fmt.Printf("  Role: %s\n", o.localMachine.Role)
	}

	fmt.Printf("\nRemote Machines (%d):\n", len(o.remoteMachines))
	for _, m := range o.remoteMachines {
		fmt.Printf("\n  %s (%s):\n", m.Name, m.WezTermDomain)
		fmt.Printf("    Public IP: %s\n", m.PublicIP)
		if m.TailscaleIP != "" {
			fmt.Printf("    Tailscale IP: %s (preferred)\n", m.TailscaleIP)
		}
		fmt.Printf("    User: %s\n", m.Username)
		fmt.Printf("    Role: %s\n", m.Role)
	}

	// Print warnings if any
	if len(o.discoveryWarnings) > 0 {
		fmt.Printf("\n=== Discovery Warnings (%d) ===\n", len(o.discoveryWarnings))
		for _, w := range o.discoveryWarnings {
			if w.Machine != "" {
				fmt.Printf("  [%s] %s: %s\n", w.Code, w.Machine, w.Message)
			} else {
				fmt.Printf("  [%s] %s\n", w.Code, w.Message)
			}
		}
	}
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\'' || r == '"' || r == '\\' || r == '$' || r == '`'
	}) == -1 {
		return s
	}
	// Single-quote and escape existing single quotes.
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func buildSSHCommand(m *DiscoveredMachine, opts ScriptOptions) string {
	addr := m.PublicIP
	if opts.UseTailscale && m.TailscaleIP != "" {
		addr = m.TailscaleIP
	}

	var b strings.Builder
	b.WriteString("ssh")
	if m.IdentityFile != "" {
		b.WriteString(" -i ")
		b.WriteString(shellQuote(m.IdentityFile))
	}
	if m.Port != 0 && m.Port != 22 {
		b.WriteString(fmt.Sprintf(" -p %d", m.Port))
	}
	b.WriteString(" ")
	if m.Username != "" {
		b.WriteString(m.Username)
		b.WriteString("@")
	}
	b.WriteString(addr)
	return b.String()
}

// Helper functions

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, item) {
			return true
		}
	}
	return false
}
