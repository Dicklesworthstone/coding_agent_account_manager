// Package deploy handles binary deployment and systemd service management on remote machines.
package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"golang.org/x/crypto/ssh"
)

// Deployer handles deployment of caam to remote machines.
type Deployer struct {
	machine      *sync.Machine
	sshClient    *sync.SSHClient
	client       *ssh.Client // For command execution
	logger       *slog.Logger
	localVersion string
	localBinary  string
}

// NewDeployer creates a new deployer for a machine.
func NewDeployer(m *sync.Machine, logger *slog.Logger) *Deployer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deployer{
		machine:   m,
		sshClient: sync.NewSSHClient(m),
		logger:    logger,
	}
}

// Connect establishes SSH connection for deployment.
func (d *Deployer) Connect() error {
	opts := sync.ConnectOptions{
		Timeout:  30 * time.Second,
		UseAgent: true,
	}

	if err := d.sshClient.Connect(opts); err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}

	// Get the underlying ssh.Client for command execution
	// We need to access it through the SFTP-enabled client
	// For now, we'll create a separate connection for commands
	d.client = d.getSSHClient()
	if d.client == nil {
		return fmt.Errorf("failed to establish command connection")
	}

	return nil
}

// getSSHClient creates a new SSH client for command execution.
// This is a workaround since the sync.SSHClient doesn't expose the underlying client.
func (d *Deployer) getSSHClient() *ssh.Client {
	// We need to reconnect for command execution since sync.SSHClient
	// doesn't expose the underlying connection for sessions
	// For simplicity, we'll use the existing connection's host/port

	home, _ := os.UserHomeDir()
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	if d.machine.SSHKeyPath != "" {
		keyPath = expandPath(d.machine.SSHKeyPath)
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		d.logger.Debug("failed to read SSH key", "path", keyPath, "error", err)
		return nil
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		d.logger.Debug("failed to parse SSH key", "error", err)
		return nil
	}

	user := d.machine.SSHUser
	if user == "" {
		user = os.Getenv("USER")
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Simplified for deploy
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", d.machine.HostPort(), config)
	if err != nil {
		d.logger.Debug("failed to create command client", "error", err)
		return nil
	}

	return client
}

// Disconnect closes the SSH connection.
func (d *Deployer) Disconnect() error {
	if d.client != nil {
		d.client.Close()
	}
	return d.sshClient.Disconnect()
}

// RunCommand executes a command on the remote machine.
func (d *Deployer) RunCommand(ctx context.Context, cmd string) (string, error) {
	if d.client == nil {
		return "", fmt.Errorf("not connected")
	}

	session, err := d.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Set up context cancellation
	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("%w: %s", err, stderr.String())
		}
		return stdout.String(), nil
	}
}

// GetRemoteVersion gets the caam version on the remote machine.
func (d *Deployer) GetRemoteVersion(ctx context.Context) (string, error) {
	// First find where caam is installed (could be /usr/local/bin or ~/bin)
	binaryPath := d.findBinaryPath(ctx)

	// Check if binary exists and get version
	output, err := d.RunCommand(ctx, binaryPath+" --version 2>/dev/null || echo ''")
	if err != nil {
		return "", nil // caam not installed
	}
	return strings.TrimSpace(output), nil
}

// GetLocalVersion gets the local caam version.
func (d *Deployer) GetLocalVersion() (string, error) {
	if d.localVersion != "" {
		return d.localVersion, nil
	}

	binary, err := d.findLocalBinary()
	if err != nil {
		return "", err
	}

	cmd := exec.Command(binary, "--version")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get local version: %w", err)
	}

	d.localVersion = strings.TrimSpace(string(output))
	return d.localVersion, nil
}

// findLocalBinary locates the local caam binary.
func (d *Deployer) findLocalBinary() (string, error) {
	if d.localBinary != "" {
		return d.localBinary, nil
	}

	// Try to find the binary
	candidates := []string{
		"/usr/local/bin/caam",
		"./caam",
		"./cmd/caam/caam",
	}

	// Add the current executable if it's caam
	if exe, err := os.Executable(); err == nil {
		if strings.Contains(filepath.Base(exe), "caam") {
			candidates = append([]string{exe}, candidates...)
		}
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			d.localBinary = path
			return path, nil
		}
	}

	// Try which command
	if path, err := exec.LookPath("caam"); err == nil {
		d.localBinary = path
		return path, nil
	}

	return "", fmt.Errorf("caam binary not found locally")
}

// NeedsUpdate checks if the remote needs a binary update.
func (d *Deployer) NeedsUpdate(ctx context.Context) (bool, string, string, error) {
	localVer, err := d.GetLocalVersion()
	if err != nil {
		return false, "", "", err
	}

	remoteVer, err := d.GetRemoteVersion(ctx)
	if err != nil {
		return true, localVer, "", nil // Assume needs update if can't get version
	}

	if remoteVer == "" {
		return true, localVer, "", nil // Not installed
	}

	return localVer != remoteVer, localVer, remoteVer, nil
}

// UploadBinary uploads the caam binary to the remote machine.
// Returns the path where the binary was installed.
func (d *Deployer) UploadBinary(ctx context.Context) (string, error) {
	binary, err := d.findLocalBinary()
	if err != nil {
		return "", err
	}

	d.logger.Info("uploading caam binary",
		"source", binary,
		"machine", d.machine.Name)

	// Read local binary
	data, err := os.ReadFile(binary)
	if err != nil {
		return "", fmt.Errorf("failed to read local binary: %w", err)
	}

	// Upload to temp location first (we might not have permissions for /usr/local/bin)
	home, _ := d.RunCommand(ctx, "echo $HOME")
	home = strings.TrimSpace(home)
	if home == "" {
		home = "/tmp"
	}

	tempPath := home + "/.caam_upload_tmp"

	// Write via SFTP
	if err := d.sshClient.WriteFile(tempPath, data, 0755); err != nil {
		return "", fmt.Errorf("failed to upload binary: %w", err)
	}

	// Move to final location with sudo if needed
	installPath := "/usr/local/bin/caam"
	_, err = d.RunCommand(ctx, fmt.Sprintf("sudo mv %s /usr/local/bin/caam && sudo chmod 755 /usr/local/bin/caam", tempPath))
	if err != nil {
		// Try without sudo - install to ~/bin
		installPath = home + "/bin/caam"
		_, err = d.RunCommand(ctx, fmt.Sprintf("mkdir -p %s/bin && mv %s %s/bin/caam && chmod 755 %s/bin/caam", home, tempPath, home, home))
		if err != nil {
			return "", fmt.Errorf("failed to install binary: %w", err)
		}
		d.logger.Info("installed caam to ~/bin/caam (no sudo access)")
	}

	d.logger.Info("binary uploaded successfully", "machine", d.machine.Name, "path", installPath)
	return installPath, nil
}

// findBinaryPath locates the existing caam binary on the remote machine.
func (d *Deployer) findBinaryPath(ctx context.Context) string {
	// Check common locations in order of preference
	locations := []string{
		"/usr/local/bin/caam",
		"/usr/bin/caam",
	}

	// Also check ~/bin/caam
	if home, err := d.RunCommand(ctx, "echo $HOME"); err == nil {
		home = strings.TrimSpace(home)
		if home != "" {
			locations = append(locations, home+"/bin/caam")
		}
	}

	for _, loc := range locations {
		if output, err := d.RunCommand(ctx, "test -x "+loc+" && echo yes"); err == nil {
			if strings.TrimSpace(output) == "yes" {
				return loc
			}
		}
	}

	// Fallback to which command
	if output, err := d.RunCommand(ctx, "which caam"); err == nil {
		path := strings.TrimSpace(output)
		if path != "" {
			return path
		}
	}

	// Default to /usr/local/bin/caam if nothing found (shouldn't happen if NeedsUpdate returned false)
	return "/usr/local/bin/caam"
}

// DeployConfig represents the generated configuration for a machine.
type DeployConfig struct {
	Type     string `json:"type"`      // "coordinator" or "agent"
	Port     int    `json:"port"`
	Settings any    `json:"settings"`
}

// CoordinatorConfig is the configuration for a coordinator daemon.
type CoordinatorConfig struct {
	Port          int    `json:"port"`
	PollInterval  string `json:"poll_interval"`
	AuthTimeout   string `json:"auth_timeout"`
	StateTimeout  string `json:"state_timeout"`
	ResumePrompt  string `json:"resume_prompt"`
	OutputLines   int    `json:"output_lines"`
}

// DefaultCoordinatorConfig returns the default coordinator configuration.
func DefaultCoordinatorConfig() CoordinatorConfig {
	return CoordinatorConfig{
		Port:          7890,
		PollInterval:  "500ms",
		AuthTimeout:   "60s",
		StateTimeout:  "30s",
		ResumePrompt:  "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		OutputLines:   100,
	}
}

// WriteCoordinatorConfig writes the coordinator config to the remote machine.
func (d *Deployer) WriteCoordinatorConfig(ctx context.Context, config CoordinatorConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	// Get remote config directory
	home, _ := d.RunCommand(ctx, "echo $HOME")
	home = strings.TrimSpace(home)
	configPath := home + "/.config/caam/coordinator.json"

	d.logger.Info("writing coordinator config",
		"path", configPath,
		"machine", d.machine.Name)

	if err := d.sshClient.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// SystemdUnit generates a systemd user unit file.
const systemdUnitTemplate = `[Unit]
Description=CAAM {{.Type}} Daemon
After=network.target

[Service]
Type=simple
ExecStart={{.ExecStart}}
Restart=on-failure
RestartSec=5
Environment=HOME=%h

[Install]
WantedBy=default.target
`

// SystemdUnitConfig holds the configuration for generating a systemd unit.
type SystemdUnitConfig struct {
	Type      string // "coordinator" or "agent"
	ExecStart string // Full command to run
}

// GenerateSystemdUnit generates a systemd unit file content.
func GenerateSystemdUnit(config SystemdUnitConfig) (string, error) {
	tmpl, err := template.New("systemd").Parse(systemdUnitTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// WriteSystemdUnit writes a systemd user unit to the remote machine.
func (d *Deployer) WriteSystemdUnit(ctx context.Context, name string, config SystemdUnitConfig) error {
	content, err := GenerateSystemdUnit(config)
	if err != nil {
		return err
	}

	// Get home directory
	home, _ := d.RunCommand(ctx, "echo $HOME")
	home = strings.TrimSpace(home)

	unitDir := home + "/.config/systemd/user"
	unitPath := unitDir + "/" + name + ".service"

	d.logger.Info("writing systemd unit",
		"path", unitPath,
		"machine", d.machine.Name)

	// Create directory
	if _, err := d.RunCommand(ctx, fmt.Sprintf("mkdir -p %s", unitDir)); err != nil {
		return fmt.Errorf("failed to create systemd directory: %w", err)
	}

	// Write unit file
	if err := d.sshClient.WriteFile(unitPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write unit file: %w", err)
	}

	return nil
}

// EnableAndStartService enables and starts a systemd user service.
func (d *Deployer) EnableAndStartService(ctx context.Context, name string) error {
	d.logger.Info("enabling systemd service",
		"service", name,
		"machine", d.machine.Name)

	// Enable linger so services run after logout
	if _, err := d.RunCommand(ctx, "loginctl enable-linger $(whoami) 2>/dev/null || true"); err != nil {
		d.logger.Debug("failed to enable linger", "error", err)
	}

	// Reload systemd
	if _, err := d.RunCommand(ctx, "systemctl --user daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload failed: %w", err)
	}

	// Enable service
	if _, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user enable %s", name)); err != nil {
		return fmt.Errorf("enable failed: %w", err)
	}

	// Start service
	if _, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user restart %s", name)); err != nil {
		return fmt.Errorf("start failed: %w", err)
	}

	return nil
}

// GetServiceStatus gets the status of a systemd user service.
func (d *Deployer) GetServiceStatus(ctx context.Context, name string) (string, error) {
	output, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user status %s --no-pager 2>/dev/null | head -3 || echo 'not found'", name))
	if err != nil {
		return "unknown", nil
	}
	return strings.TrimSpace(output), nil
}

// StopService stops a systemd user service.
func (d *Deployer) StopService(ctx context.Context, name string) error {
	_, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user stop %s 2>/dev/null || true", name))
	return err
}

// GetRemoteOS returns the operating system of the remote machine.
func (d *Deployer) GetRemoteOS(ctx context.Context) string {
	output, _ := d.RunCommand(ctx, "uname -s")
	return strings.TrimSpace(strings.ToLower(output))
}

// GetRemoteArch returns the architecture of the remote machine.
func (d *Deployer) GetRemoteArch(ctx context.Context) string {
	output, _ := d.RunCommand(ctx, "uname -m")
	arch := strings.TrimSpace(strings.ToLower(output))

	// Normalize architecture names
	switch arch {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return arch
	}
}

// CanDeploy checks if we can deploy to the remote (same OS/arch).
func (d *Deployer) CanDeploy(ctx context.Context) (bool, string) {
	remoteOS := d.GetRemoteOS(ctx)
	remoteArch := d.GetRemoteArch(ctx)
	localOS := runtime.GOOS
	localArch := runtime.GOARCH

	if remoteOS != localOS {
		return false, fmt.Sprintf("OS mismatch: local=%s remote=%s", localOS, remoteOS)
	}
	if remoteArch != localArch {
		return false, fmt.Sprintf("arch mismatch: local=%s remote=%s", localArch, remoteArch)
	}

	return true, ""
}

// DeployResult contains the result of a deployment operation.
type DeployResult struct {
	Machine       string
	Success       bool
	BinaryUpdated bool
	ConfigWritten bool
	ServiceStatus string
	Error         error
	LocalVersion  string
	RemoteVersion string
}

// DeployCoordinator performs a full coordinator deployment.
func (d *Deployer) DeployCoordinator(ctx context.Context, config CoordinatorConfig) (*DeployResult, error) {
	result := &DeployResult{
		Machine: d.machine.Name,
	}

	// Check compatibility
	canDeploy, reason := d.CanDeploy(ctx)
	if !canDeploy {
		result.Error = fmt.Errorf("cannot deploy: %s", reason)
		return result, result.Error
	}

	// Check if update needed
	needsUpdate, localVer, remoteVer, err := d.NeedsUpdate(ctx)
	result.LocalVersion = localVer
	result.RemoteVersion = remoteVer

	if err != nil {
		result.Error = err
		return result, err
	}

	// Determine binary install path
	var installPath string

	// Upload binary if needed
	if needsUpdate {
		var err error
		installPath, err = d.UploadBinary(ctx)
		if err != nil {
			result.Error = fmt.Errorf("binary upload failed: %w", err)
			return result, result.Error
		}
		result.BinaryUpdated = true
	} else {
		// Binary already exists - find where it is
		installPath = d.findBinaryPath(ctx)
	}

	// Write coordinator config
	if err := d.WriteCoordinatorConfig(ctx, config); err != nil {
		result.Error = fmt.Errorf("config write failed: %w", err)
		return result, result.Error
	}
	result.ConfigWritten = true

	// Write systemd unit
	unitConfig := SystemdUnitConfig{
		Type:      "Auth Recovery Coordinator",
		ExecStart: installPath + " auth-coordinator --config ~/.config/caam/coordinator.json",
	}
	if err := d.WriteSystemdUnit(ctx, "caam-coordinator", unitConfig); err != nil {
		result.Error = fmt.Errorf("systemd unit write failed: %w", err)
		return result, result.Error
	}

	// Enable and start service
	if err := d.EnableAndStartService(ctx, "caam-coordinator"); err != nil {
		result.Error = fmt.Errorf("service start failed: %w", err)
		return result, result.Error
	}

	// Get status
	status, _ := d.GetServiceStatus(ctx, "caam-coordinator")
	result.ServiceStatus = status
	result.Success = true

	return result, nil
}

// Helper functions

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// CopyFile copies a file using io for larger files.
func CopyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}
