// Package setup provides orchestration for setting up the distributed auth recovery system.
package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
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

// endpointName is the machine's name in the agent config: its WezTerm domain,
// which Tailscale matching does not rename, or the --host name.
func (m *DiscoveredMachine) endpointName() string {
	if m.WezTermDomain != "" {
		return m.WezTermDomain
	}
	return m.Name
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

	// Hosts are additional coordinator hosts, as [user@]host[:port] or a
	// ~/.ssh/config alias. With hosts, a WezTerm config is optional (tmux
	// users need none).
	Hosts []string

	// ManualOverrides maps WezTerm domain names to manual address/config overrides.
	// Use this when discovery produces wrong results or for machines not on tailnet.
	ManualOverrides map[string]MachineOverride

	// DryRun shows what would be done without making changes.
	DryRun bool

	// AgentConfigPath is where the local agent config is written.
	// Defaults to agent.DefaultConfigPath().
	AgentConfigPath string

	// RotateTokens issues new coordinator tokens instead of keeping the
	// tokens already recorded for redeployed hosts.
	RotateTokens bool

	// Logger for structured logging.
	Logger *slog.Logger
}

func (o *Orchestrator) agentConfigPath() string {
	if o.opts.AgentConfigPath != "" {
		return o.opts.AgentConfigPath
	}
	return agent.DefaultConfigPath()
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
	Hosts         []string
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

	// Find WezTerm config. It is optional when hosts are given explicitly.
	hostsOnly := len(o.opts.Hosts) > 0
	configPath := o.opts.WezTermConfig
	if configPath == "" {
		configPath = wezterm.FindConfigPath()
	}
	o.weztermConfig = &wezterm.Config{}
	switch {
	case configPath == "" && !hostsOnly:
		return fmt.Errorf("WezTerm config not found; name coordinator hosts with --host (tmux users need no WezTerm), or point to wezterm.lua with --wezterm-config")
	case configPath != "":
		o.logger.Info("parsing WezTerm config", "path", configPath)
		cfg, err := wezterm.ParseConfig(configPath)
		switch {
		case err == nil:
			o.weztermConfig = cfg
		case hostsOnly:
			o.addWarning("", "WEZTERM_CONFIG_ERROR", fmt.Sprintf("ignoring WezTerm config %s: %v", configPath, err))
		default:
			return fmt.Errorf("failed to parse WezTerm config: %w", err)
		}
	}

	if len(o.weztermConfig.SSHDomains) == 0 && !hostsOnly {
		return fmt.Errorf("no SSH domains found in WezTerm config; add ssh_domains entries or name coordinator hosts with --host")
	}

	o.logger.Info("found SSH domains", "count", len(o.weztermConfig.SSHDomains), "hosts", len(o.opts.Hosts))

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

	hostMachines, err := hostsToMachines(o.opts.Hosts, sshConfigHosts())
	if err != nil {
		return err
	}
	for _, m := range hostMachines {
		duplicate := false
		for _, existing := range machines {
			if strings.EqualFold(existing.endpointName(), m.Name) || strings.EqualFold(existing.Name, m.Name) {
				duplicate = true
			}
		}
		if duplicate {
			o.addWarning(m.Name, "DUPLICATE_HOST", "named by --host and by a WezTerm domain; using the WezTerm domain")
			continue
		}
		machines = append(machines, m)
		o.logger.Info("remote from --host", "name", m.Name, "address", m.PublicIP, "user", m.Username, "port", m.Port)
	}

	o.remoteMachines = machines
	return nil
}

// sshConfigHosts returns the hosts in ~/.ssh/config by alias, so --host can
// name them the way ssh does.
func sshConfigHosts() map[string]*sync.Machine {
	hosts := map[string]*sync.Machine{}
	machines, err := sync.DiscoverFromSSHConfig()
	if err != nil {
		return hosts
	}
	for _, m := range machines {
		hosts[strings.ToLower(m.Name)] = m
	}
	return hosts
}

// hostsToMachines turns --host specs ([user@]host[:port], or an alias from
// ~/.ssh/config, whose HostName, User, Port and IdentityFile apply unless
// the spec sets user or port) into coordinator machines.
func hostsToMachines(specs []string, sshConfig map[string]*sync.Machine) ([]*DiscoveredMachine, error) {
	var machines []*DiscoveredMachine
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		user, hostPort, hasUser := strings.Cut(spec, "@")
		if !hasUser {
			user, hostPort = "", spec
		}
		host, port := hostPort, 0
		if h, p, err := net.SplitHostPort(hostPort); err == nil {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 || n > 65535 {
				return nil, fmt.Errorf("--host %q: invalid port %q", spec, p)
			}
			host, port = h, n
		}
		host = strings.Trim(host, "[]")
		if host == "" || strings.ContainsAny(host, " /") {
			return nil, fmt.Errorf("--host %q: expected [user@]host[:port]", spec)
		}

		m := &DiscoveredMachine{Name: host, PublicIP: host, Username: user, Port: port, Role: RoleCoordinator}
		if alias, ok := sshConfig[strings.ToLower(host)]; ok {
			m.PublicIP = alias.Address
			if m.Username == "" {
				m.Username = alias.SSHUser
			}
			if m.Port == 0 {
				m.Port = alias.Port
			}
			m.IdentityFile = alias.SSHKeyPath
		}
		if m.Port == 0 {
			m.Port = 22
		}
		machines = append(machines, m)
	}
	return machines, nil
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
	for _, host := range opts.Hosts {
		b.WriteString(" --host ")
		b.WriteString(shellQuote(host))
	}
	b.WriteString("\n\n")

	configPath := o.agentConfigPath()

	b.WriteString("# 2) Inspect and edit the local agent config\n")
	b.WriteString(fmt.Sprintf("CONFIG_PATH=%s\n", shellQuote(configPath)))
	b.WriteString("echo \"Sign in to the agent's Google accounts with: caam auth-agent signin\"\n")
	b.WriteString("echo \"Then add them to the rotation: caam auth-agent accounts add --config $CONFIG_PATH <email>...\"\n\n")

	b.WriteString("# 3) Check coordinator service status on remotes\n")
	for _, m := range o.remoteMachines {
		sshCmd := buildSSHCommand(m, opts)
		b.WriteString(fmt.Sprintf("%s -- %s\n", sshCmd, shellQuote("systemctl --user status "+deploy.CoordinatorServiceName+" --no-pager")))
	}
	b.WriteString("\n")

	// The API listens on remote loopback with a token, so query it on the host.
	b.WriteString("# 4) Smoke test the authenticated coordinator API on each remote\n")
	for _, m := range o.remoteMachines {
		sshCmd := buildSSHCommand(m, opts)
		b.WriteString(fmt.Sprintf("%s -- %s\n", sshCmd, shellQuote(`PATH="$HOME/.local/bin:$HOME/bin:$PATH" caam auth-coordinator status`)))
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

// Setup performs the full setup process: each remote gets a coordinator with
// its own API token, and every successfully verified coordinator is recorded
// in the local agent config with that token and an SSH tunnel endpoint.
func (o *Orchestrator) Setup(ctx context.Context, progress func(*SetupProgress)) (*SetupResult, error) {
	result := &SetupResult{}

	if len(o.remoteMachines) == 0 {
		return nil, fmt.Errorf("no remote machines to setup")
	}

	var endpoints []*agent.CoordinatorEndpoint

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
			endpoints = append(endpoints, o.coordinatorEndpoint(machine, dryRunTokenPlaceholder))
			p.Status = "success"
			p.Message = "dry-run: skipped"
			p.Finished = time.Now()
			if progress != nil {
				progress(p)
			}
			continue
		}

		token, err := o.coordinatorToken(machine)
		if err != nil {
			return nil, err
		}
		deployResult, err := o.deployCoordinator(ctx, machine, token)
		if err != nil {
			p.Status = "failed"
			p.Message = err.Error()
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", machine.Name, err))
			o.logger.Error("deployment failed",
				"machine", machine.Name,
				"error", err)
		} else {
			p.Status = "success"
			p.Message = "deployed and verified"
			o.logger.Info("deployment succeeded", "machine", machine.Name)
			endpoints = append(endpoints, o.coordinatorEndpoint(machine, token))
		}
		p.Finished = time.Now()

		if deployResult != nil {
			result.DeployResults = append(result.DeployResults, deployResult)
		}

		if progress != nil {
			progress(p)
		}
	}

	// Record verified coordinators in the local agent config. A failed
	// deployment keeps whatever entry that host already had.
	if len(endpoints) == 0 {
		return result, nil
	}
	localConfigPath, err := o.generateLocalConfig(endpoints)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("local config: %w", err))
	} else {
		result.LocalConfigPath = localConfigPath
	}

	return result, nil
}

// dryRunTokenPlaceholder stands in for tokens that a real run generates.
const dryRunTokenPlaceholder = "<generated during setup>"

// coordinatorToken returns the API token to deploy for a machine. Re-running
// setup keeps a host's existing token, so a running auth-agent service keeps
// working after coordinators are redeployed; RotateTokens forces new ones.
func (o *Orchestrator) coordinatorToken(m *DiscoveredMachine) (string, error) {
	if !o.opts.RotateTokens {
		if existing, err := agent.LoadFileConfig(o.agentConfigPath()); err == nil {
			for _, c := range existing.Coordinators {
				if c != nil && strings.EqualFold(c.Name, m.endpointName()) && len(c.Token) >= 32 {
					return c.Token, nil
				}
			}
		}
	}
	return generateToken()
}

// generateToken returns a random coordinator API token.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate coordinator token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// coordinatorEndpoint describes how the local agent reaches a deployed
// coordinator: over its own SSH connection to the host the deployment used,
// to the loopback-only API, authenticated with the host's token.
func (o *Orchestrator) coordinatorEndpoint(m *DiscoveredMachine, token string) *agent.CoordinatorEndpoint {
	return &agent.CoordinatorEndpoint{
		Name:        m.endpointName(),
		URL:         "http://" + coordinator.ListenAddress(coordinator.DefaultBindAddress, o.opts.RemotePort),
		DisplayName: m.Name,
		Token:       token,
		SSH: &agent.SSHTunnel{
			Host:         o.getAddress(m),
			Port:         m.Port,
			User:         m.Username,
			IdentityFile: m.IdentityFile,
		},
	}
}

// deployCoordinator deploys a coordinator to a remote machine.
func (o *Orchestrator) deployCoordinator(ctx context.Context, m *DiscoveredMachine, token string) (*deploy.DeployResult, error) {
	syncMachine := o.toSyncMachine(m)

	deployer := deploy.NewDeployer(syncMachine, o.logger)
	if err := deployer.Connect(); err != nil {
		return nil, fmt.Errorf("connect failed: %w", err)
	}
	defer deployer.Disconnect()

	config := deploy.DefaultCoordinatorConfig()
	config.Port = o.opts.RemotePort
	config.AuthToken = token

	return deployer.DeployCoordinator(ctx, config)
}

// generateLocalConfig merges coordinator endpoints into the local agent
// config. Endpoints replace existing entries with the same name; other
// coordinators and user settings (accounts, Chrome profile, strategy) are
// preserved.
func (o *Orchestrator) generateLocalConfig(endpoints []*agent.CoordinatorEndpoint) (string, error) {
	configPath := o.agentConfigPath()

	fc := agent.FileConfig{
		Port:         o.opts.LocalPort,
		PollInterval: "2s",
		Strategy:     "lru",
		Accounts:     []string{},
	}
	existing, err := agent.LoadFileConfig(configPath)
	switch {
	case err == nil:
		fc = existing
		if fc.Port == 0 || (o.opts.LocalPort != 0 && o.opts.LocalPort != DefaultOptions().LocalPort) {
			fc.Port = o.opts.LocalPort
		}
		if fc.Accounts == nil {
			fc.Accounts = []string{}
		}
	case !errors.Is(err, os.ErrNotExist):
		// Never clobber a config we cannot read; the user may have edited it.
		return "", fmt.Errorf("existing agent config %s: %w", configPath, err)
	}
	fc.Coordinators = mergeEndpoints(fc.Coordinators, endpoints)

	if o.opts.DryRun {
		o.logger.Info("[dry-run] would write local agent config",
			"path", configPath)
		data, err := json.MarshalIndent(redactedAgentConfig(fc), "", "  ")
		if err != nil {
			return "", err
		}
		fmt.Println("--- distributed-agent.json ---")
		fmt.Println(string(data))
		fmt.Println("---")
		return configPath, nil
	}

	if err := agent.WriteFileConfig(configPath, fc); err != nil {
		return "", err
	}

	o.logger.Info("wrote local agent config", "path", configPath, "coordinators", len(fc.Coordinators))
	return configPath, nil
}

// mergeEndpoints replaces existing coordinators by name and appends new ones.
func mergeEndpoints(existing, updates []*agent.CoordinatorEndpoint) []*agent.CoordinatorEndpoint {
	merged := make([]*agent.CoordinatorEndpoint, 0, len(existing)+len(updates))
	replaced := make(map[string]bool, len(updates))
	for _, cur := range existing {
		if cur == nil {
			continue
		}
		replacement := cur
		for _, u := range updates {
			if strings.EqualFold(u.Name, cur.Name) {
				replacement = u
				replaced[strings.ToLower(u.Name)] = true
				break
			}
		}
		merged = append(merged, replacement)
	}
	for _, u := range updates {
		if !replaced[strings.ToLower(u.Name)] {
			merged = append(merged, u)
		}
	}
	return merged
}

// redactedAgentConfig hides coordinator tokens for display.
func redactedAgentConfig(fc agent.FileConfig) agent.FileConfig {
	shown := fc
	shown.CoordinatorToken = redactToken(fc.CoordinatorToken)
	shown.Coordinators = make([]*agent.CoordinatorEndpoint, 0, len(fc.Coordinators))
	for _, c := range fc.Coordinators {
		shown.Coordinators = append(shown.Coordinators, &agent.CoordinatorEndpoint{
			Name:        c.Name,
			URL:         c.URL,
			DisplayName: c.DisplayName,
			Token:       redactToken(c.Token),
			SSH:         c.SSH,
		})
	}
	return shown
}

func redactToken(token string) string {
	if token == "" || token == dryRunTokenPlaceholder {
		return token
	}
	return "[REDACTED]"
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
		source := "--host"
		if m.WezTermDomain != "" {
			source = "WezTerm domain " + m.WezTermDomain
		}
		fmt.Printf("\n  %s (%s):\n", m.Name, source)
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
