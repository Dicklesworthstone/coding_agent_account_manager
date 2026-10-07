// Package deploy handles binary deployment and systemd service management on remote machines.
package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"golang.org/x/crypto/ssh"
)

// CoordinatorServiceName is the systemd user unit that runs the coordinator.
const CoordinatorServiceName = "caam-coordinator"

// coordinatorConfigRel is the coordinator config path relative to $HOME.
const coordinatorConfigRel = ".config/caam/coordinator.json"

// installScriptURL is the official installer, used when the local binary
// cannot run on the remote platform.
const installScriptURL = "https://raw.githubusercontent.com/Dicklesworthstone/coding_agent_account_manager/main/install.sh"

// Deployer handles deployment of caam to remote machines.
type Deployer struct {
	machine      *sync.Machine
	sshClient    *sync.SSHClient
	logger       *slog.Logger
	localVersion string
	localBinary  string
	remoteHome   string
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

// Connect establishes the SSH connection used for commands, file transfer,
// and verifying the deployed coordinator. It uses the same authentication
// (ssh-agent, configured identity, default keys) and known_hosts checking as
// every other caam SSH operation.
func (d *Deployer) Connect() error {
	opts := sync.ConnectOptions{
		Timeout:  30 * time.Second,
		UseAgent: true,
	}

	if err := d.sshClient.Connect(opts); err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	return nil
}

// Disconnect closes the SSH connection.
func (d *Deployer) Disconnect() error {
	return d.sshClient.Disconnect()
}

// RunCommand executes a command on the remote machine.
func (d *Deployer) RunCommand(ctx context.Context, cmd string) (string, error) {
	session, err := d.sshClient.NewSession()
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
			return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}
}

// RemoteHome returns the remote user's home directory.
func (d *Deployer) RemoteHome(ctx context.Context) (string, error) {
	if d.remoteHome != "" {
		return d.remoteHome, nil
	}
	out, err := d.RunCommand(ctx, `printf '%s' "$HOME"`)
	if err != nil {
		return "", fmt.Errorf("resolve remote home: %w", err)
	}
	home := strings.TrimSpace(out)
	if !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("resolve remote home: got %q", home)
	}
	d.remoteHome = home
	return home, nil
}

// GetRemoteVersion gets the caam version on the remote machine.
func (d *Deployer) GetRemoteVersion(ctx context.Context) (string, error) {
	binaryPath, ok := d.existingBinary(ctx)
	if !ok {
		return "", nil // caam not installed
	}

	output, err := d.RunCommand(ctx, shellEscape(binaryPath)+" --version 2>/dev/null || echo ''")
	if err != nil {
		return "", nil
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

	home, err := d.RemoteHome(ctx)
	if err != nil {
		return "", err
	}

	// Upload to a temp location first (we might not have permissions for /usr/local/bin)
	tempPath := home + "/.caam_upload_tmp"
	if err := d.sshClient.WriteFile(tempPath, data, 0755); err != nil {
		return "", fmt.Errorf("failed to upload binary: %w", err)
	}

	// Move to final location with sudo if it works without a password prompt.
	// Shell-escape paths to prevent command injection.
	installPath := "/usr/local/bin/caam"
	escapedTempPath := shellEscape(tempPath)
	escapedHome := shellEscape(home)
	_, err = d.RunCommand(ctx, fmt.Sprintf("sudo -n mv %s /usr/local/bin/caam && sudo -n chmod 755 /usr/local/bin/caam", escapedTempPath))
	if err != nil {
		// Install to ~/bin without sudo
		installPath = home + "/bin/caam"
		_, err = d.RunCommand(ctx, fmt.Sprintf("mkdir -p %s/bin && mv %s %s/bin/caam && chmod 755 %s/bin/caam", escapedHome, escapedTempPath, escapedHome, escapedHome))
		if err != nil {
			return "", fmt.Errorf("failed to install binary: %w", err)
		}
		d.logger.Info("installed caam to ~/bin/caam (no sudo access)")
	}

	d.logger.Info("binary uploaded successfully", "machine", d.machine.Name, "path", installPath)
	return installPath, nil
}

// InstallFromRelease installs the published release on the remote machine
// with the official installer. It is used when the local binary is built for
// a different OS or architecture (for example a macOS agent deploying to a
// Linux coordinator host).
func (d *Deployer) InstallFromRelease(ctx context.Context) (string, error) {
	home, err := d.RemoteHome(ctx)
	if err != nil {
		return "", err
	}
	installDir := home + "/.local/bin"

	d.logger.Info("installing caam release on remote",
		"machine", d.machine.Name,
		"install_dir", installDir)

	cmd := fmt.Sprintf("command -v curl >/dev/null 2>&1 || { echo 'curl is required to install caam' >&2; exit 127; }; "+
		"curl -fsSL %s | INSTALL_DIR=%s bash",
		shellEscape(installScriptURL), shellEscape(installDir))
	if _, err := d.RunCommand(ctx, cmd); err != nil {
		return "", fmt.Errorf("install caam release on %s: %w", d.machine.Name, err)
	}

	installPath := installDir + "/caam"
	if _, err := d.RunCommand(ctx, "test -x "+shellEscape(installPath)); err != nil {
		return "", fmt.Errorf("installer finished but %s is not executable", installPath)
	}
	return installPath, nil
}

// existingBinary locates an installed caam binary on the remote machine.
func (d *Deployer) existingBinary(ctx context.Context) (string, bool) {
	locations := []string{
		"/usr/local/bin/caam",
		"/usr/bin/caam",
	}
	if home, err := d.RemoteHome(ctx); err == nil {
		locations = append(locations, home+"/bin/caam", home+"/.local/bin/caam")
	}

	for _, loc := range locations {
		if _, err := d.RunCommand(ctx, "test -x "+shellEscape(loc)); err == nil {
			return loc, true
		}
	}

	// Fall back to the login PATH
	if output, err := d.RunCommand(ctx, "command -v caam"); err == nil {
		if path := strings.TrimSpace(output); strings.HasPrefix(path, "/") {
			return path, true
		}
	}
	return "", false
}

// DefaultCoordinatorConfig returns the default coordinator configuration.
// The API stays on loopback; agents reach it over SSH.
func DefaultCoordinatorConfig() coordinator.FileConfig {
	return coordinator.FileConfig{
		Bind:         coordinator.DefaultBindAddress,
		Port:         7890,
		PollInterval: "500ms",
		AuthTimeout:  "60s",
		StateTimeout: "30s",
		ResumePrompt: "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		OutputLines:  100,
	}
}

// WriteCoordinatorConfig writes the coordinator config to the remote machine.
// The file carries the API token, so it is readable only by its owner.
func (d *Deployer) WriteCoordinatorConfig(ctx context.Context, config coordinator.FileConfig) error {
	if err := coordinator.ValidateListenSecurity(config.Bind, config.AuthToken); err != nil {
		return err
	}
	if _, err := config.Apply(coordinator.DefaultConfig()); err != nil {
		return fmt.Errorf("invalid coordinator config: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	home, err := d.RemoteHome(ctx)
	if err != nil {
		return err
	}
	configPath := home + "/" + coordinatorConfigRel

	d.logger.Info("writing coordinator config",
		"path", configPath,
		"machine", d.machine.Name)

	if err := d.sshClient.WriteFile(configPath, data, 0600); err != nil {
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

// CoordinatorExecStart builds the coordinator unit's ExecStart line. systemd
// does not expand "~", so the config path uses the %h (home) specifier.
func CoordinatorExecStart(binaryPath string) string {
	return systemdQuote(binaryPath) + " auth-coordinator --config %h/" + coordinatorConfigRel
}

// systemdQuote quotes a literal argument for an ExecStart line: "%" starts a
// specifier and whitespace, quotes, and backslashes need double quoting.
func systemdQuote(arg string) string {
	arg = strings.ReplaceAll(arg, "%", "%%")
	if !strings.ContainsAny(arg, " \t\"'\\;$") {
		return arg
	}
	arg = strings.ReplaceAll(arg, `\`, `\\`)
	arg = strings.ReplaceAll(arg, `"`, `\"`)
	arg = strings.ReplaceAll(arg, "$", "$$")
	return `"` + arg + `"`
}

// WriteSystemdUnit writes a systemd user unit to the remote machine.
func (d *Deployer) WriteSystemdUnit(ctx context.Context, name string, config SystemdUnitConfig) error {
	content, err := GenerateSystemdUnit(config)
	if err != nil {
		return err
	}

	home, err := d.RemoteHome(ctx)
	if err != nil {
		return err
	}

	unitPath := home + "/.config/systemd/user/" + name + ".service"

	d.logger.Info("writing systemd unit",
		"path", unitPath,
		"machine", d.machine.Name)

	if err := d.sshClient.WriteFile(unitPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write unit file: %w", err)
	}

	return nil
}

// EnableAndStartService enables and (re)starts a systemd user service.
func (d *Deployer) EnableAndStartService(ctx context.Context, name string) error {
	d.logger.Info("enabling systemd service",
		"service", name,
		"machine", d.machine.Name)

	// Enable linger so services run after logout
	if _, err := d.RunCommand(ctx, "loginctl enable-linger \"$(whoami)\" 2>/dev/null || true"); err != nil {
		d.logger.Debug("failed to enable linger", "error", err)
	}

	// Reload systemd
	if _, err := d.RunCommand(ctx, "systemctl --user daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload failed: %w", err)
	}

	// Enable service (shell-escape name to prevent injection)
	escapedName := shellEscape(name)
	if _, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user enable %s", escapedName)); err != nil {
		return fmt.Errorf("enable failed: %w", err)
	}

	// Restart so a rewritten config or binary takes effect
	if _, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user restart %s", escapedName)); err != nil {
		return fmt.Errorf("start failed: %w", err)
	}

	return nil
}

// GetServiceStatus gets the status of a systemd user service.
func (d *Deployer) GetServiceStatus(ctx context.Context, name string) (string, error) {
	// Shell-escape name to prevent injection
	output, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user status %s --no-pager 2>/dev/null | head -3 || echo 'not found'", shellEscape(name)))
	if err != nil {
		return "unknown", nil
	}
	return strings.TrimSpace(output), nil
}

// StopService stops a systemd user service.
func (d *Deployer) StopService(ctx context.Context, name string) error {
	// Shell-escape name to prevent injection
	_, err := d.RunCommand(ctx, fmt.Sprintf("systemctl --user stop %s 2>/dev/null || true", shellEscape(name)))
	return err
}

// VerifyCoordinator calls the coordinator's authenticated /status endpoint
// through the SSH connection, exactly as the local agent will reach it. It
// retries until the service answers or the context ends.
func (d *Deployer) VerifyCoordinator(ctx context.Context, config coordinator.FileConfig) error {
	bind := config.Bind
	if bind == "" || bind == "0.0.0.0" || bind == "::" {
		bind = coordinator.DefaultBindAddress
	}
	target := coordinator.ListenAddress(bind, config.Port)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return d.sshClient.Dial(network, target)
			},
			DisableKeepAlives: true,
		},
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = probeCoordinatorStatus(ctx, client, config.AuthToken)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, errCoordinatorUnauthorized) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("coordinator on %s did not answer at %s: %w", d.machine.Name, target, lastErr)
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
}

var errCoordinatorUnauthorized = errors.New("coordinator rejected the deployed auth token")

func probeCoordinatorStatus(ctx context.Context, client *http.Client, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://coordinator/status", nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return errCoordinatorUnauthorized
	default:
		return fmt.Errorf("status endpoint returned %s", resp.Status)
	}
}

// GetRemoteOS returns the operating system of the remote machine.
func (d *Deployer) GetRemoteOS(ctx context.Context) string {
	output, _ := d.RunCommand(ctx, "uname -s")
	return strings.TrimSpace(strings.ToLower(output))
}

// GetRemoteArch returns the architecture of the remote machine.
func (d *Deployer) GetRemoteArch(ctx context.Context) string {
	output, _ := d.RunCommand(ctx, "uname -m")
	return normalizeArch(output)
}

func normalizeArch(arch string) string {
	arch = strings.TrimSpace(strings.ToLower(arch))
	switch arch {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return arch
	}
}

// CanDeploy checks if the local binary can run on the remote (same OS/arch).
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
	BinaryPath    string
	ConfigWritten bool
	Verified      bool
	ServiceStatus string
	Error         error
	LocalVersion  string
	RemoteVersion string
}

// ensureBinary makes a runnable caam available on the remote and returns its
// path. The local binary is uploaded when it matches the remote platform;
// otherwise an existing install is reused or the published release installed.
func (d *Deployer) ensureBinary(ctx context.Context, result *DeployResult) (string, error) {
	canUpload, reason := d.CanDeploy(ctx)
	if !canUpload {
		if path, ok := d.existingBinary(ctx); ok {
			d.logger.Info("using existing remote caam (local binary targets another platform)",
				"machine", d.machine.Name,
				"path", path,
				"reason", reason)
			result.RemoteVersion, _ = d.GetRemoteVersion(ctx)
			return path, nil
		}
		path, err := d.InstallFromRelease(ctx)
		if err != nil {
			return "", fmt.Errorf("%s and no caam installed remotely: %w", reason, err)
		}
		result.BinaryUpdated = true
		return path, nil
	}

	needsUpdate, localVer, remoteVer, err := d.NeedsUpdate(ctx)
	result.LocalVersion = localVer
	result.RemoteVersion = remoteVer
	if err != nil {
		return "", err
	}
	if !needsUpdate {
		if path, ok := d.existingBinary(ctx); ok {
			return path, nil
		}
	}

	path, err := d.UploadBinary(ctx)
	if err != nil {
		return "", fmt.Errorf("binary upload failed: %w", err)
	}
	result.BinaryUpdated = true
	return path, nil
}

// DeployCoordinator performs a full coordinator deployment: binary, config,
// systemd service, and an authenticated end-to-end check of the API.
func (d *Deployer) DeployCoordinator(ctx context.Context, config coordinator.FileConfig) (*DeployResult, error) {
	result := &DeployResult{
		Machine: d.machine.Name,
	}
	fail := func(err error) (*DeployResult, error) {
		result.Error = err
		return result, err
	}

	installPath, err := d.ensureBinary(ctx, result)
	if err != nil {
		return fail(err)
	}
	result.BinaryPath = installPath

	if err := d.WriteCoordinatorConfig(ctx, config); err != nil {
		return fail(fmt.Errorf("config write failed: %w", err))
	}
	result.ConfigWritten = true

	unitConfig := SystemdUnitConfig{
		Type:      "Auth Recovery Coordinator",
		ExecStart: CoordinatorExecStart(installPath),
	}
	if err := d.WriteSystemdUnit(ctx, CoordinatorServiceName, unitConfig); err != nil {
		return fail(fmt.Errorf("systemd unit write failed: %w", err))
	}

	if err := d.EnableAndStartService(ctx, CoordinatorServiceName); err != nil {
		return fail(fmt.Errorf("service start failed: %w", err))
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	verifyErr := d.VerifyCoordinator(verifyCtx, config)
	result.ServiceStatus, _ = d.GetServiceStatus(ctx, CoordinatorServiceName)
	if verifyErr != nil {
		return fail(fmt.Errorf("coordinator verification failed: %w", verifyErr))
	}
	result.Verified = true
	result.Success = true

	return result, nil
}

// Helper functions

// shellEscape escapes a string for safe use in shell commands.
// This prevents command injection when interpolating user-controlled values.
func shellEscape(s string) string {
	// Use single quotes and escape any embedded single quotes
	// 'foo' -> 'foo'
	// foo'bar -> 'foo'\''bar'
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// CopyFile copies a file using io for larger files with atomic write pattern.
func CopyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Use atomic write pattern: write to temp file, sync, then rename
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	out, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := out.Name()
	defer os.Remove(tmpPath) // Clean up on error; no-op after successful rename

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}

	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}

	if err := out.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, dst)
}
