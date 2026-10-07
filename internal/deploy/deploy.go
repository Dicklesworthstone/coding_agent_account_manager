// Package deploy handles binary deployment and systemd service management on remote machines.
package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"regexp"
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
	// verifyTimeout bounds waiting for a (re)started coordinator to answer.
	verifyTimeout time.Duration
}

// NewDeployer creates a new deployer for a machine.
func NewDeployer(m *sync.Machine, logger *slog.Logger) *Deployer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Deployer{
		machine:       m,
		sshClient:     sync.NewSSHClient(m),
		logger:        logger,
		verifyTimeout: 20 * time.Second,
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

// sha256Command prints a file's SHA-256 with whichever tool the host has.
func sha256Command(path string) string {
	q := shellEscape(path)
	return "sh -c " + shellEscape("sha256sum "+q+" 2>/dev/null || shasum -a 256 "+q)
}

// parseSHA256Output returns the digest from sha256sum or shasum output.
func parseSHA256Output(out string) (string, error) {
	fields := strings.Fields(out)
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return "", fmt.Errorf("unexpected checksum output %q", strings.TrimSpace(out))
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", fmt.Errorf("unexpected checksum output %q", strings.TrimSpace(out))
	}
	return strings.ToLower(fields[0]), nil
}

// verifyRemoteSHA256 checks that the remote file at path has digest want.
func (d *Deployer) verifyRemoteSHA256(ctx context.Context, path, want string) error {
	out, err := d.RunCommand(ctx, sha256Command(path))
	if err != nil {
		return fmt.Errorf("checksum uploaded binary: %w", err)
	}
	got, err := parseSHA256Output(out)
	if err != nil {
		return fmt.Errorf("checksum uploaded binary: %w", err)
	}
	if got != want {
		return fmt.Errorf("uploaded binary is corrupt: sha256 %s, want %s", got, want)
	}
	return nil
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

// releaseTagPattern matches a release version such as v1.2.3 or 1.2.3-rc.1.
var releaseTagPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+([-+][0-9A-Za-z.-]+)?$`)

// releaseTag returns the release tag ("v1.2.3") in `caam --version` output,
// or "" for a development build.
func releaseTag(versionOutput string) string {
	fields := strings.Fields(versionOutput)
	if len(fields) < 2 || !releaseTagPattern.MatchString(fields[1]) {
		return ""
	}
	return "v" + strings.TrimPrefix(fields[1], "v")
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
	// A cut-short upload would install a truncated binary that crash-loops
	// the service; check it before it replaces anything.
	sum := sha256.Sum256(data)
	if err := d.verifyRemoteSHA256(ctx, tempPath, hex.EncodeToString(sum[:])); err != nil {
		d.RunCommand(ctx, "rm -f "+shellEscape(tempPath))
		return "", err
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

// InstallFromRelease installs the published release tag (the latest when
// empty) on the remote machine with the official installer. It is used when
// the local binary is built for a different OS or architecture (for example
// a macOS agent deploying to a Linux coordinator host).
func (d *Deployer) InstallFromRelease(ctx context.Context, tag string) (string, error) {
	home, err := d.RemoteHome(ctx)
	if err != nil {
		return "", err
	}
	installDir := home + "/.local/bin"

	d.logger.Info("installing caam release on remote",
		"machine", d.machine.Name,
		"install_dir", installDir,
		"version", tag)

	installArgs := ""
	if tag != "" {
		installArgs = " -s -- --version=" + shellEscape(tag)
	}
	cmd := "sh -c " + shellEscape(fmt.Sprintf("command -v curl >/dev/null 2>&1 || { echo 'curl is required to install caam' >&2; exit 127; }; "+
		"curl -fsSL %s | INSTALL_DIR=%s bash%s",
		shellEscape(installScriptURL), shellEscape(installDir), installArgs))
	if _, err := d.RunCommand(ctx, cmd); err != nil {
		return "", fmt.Errorf("install caam release on %s: %w", d.machine.Name, err)
	}

	installPath := installDir + "/caam"
	if _, err := d.RunCommand(ctx, "test -x "+shellEscape(installPath)); err != nil {
		return "", fmt.Errorf("installer finished but %s is not executable", installPath)
	}
	return installPath, nil
}

// existingBinary locates an installed caam binary on the remote machine,
// preferring the one the coordinator service runs: version checks must
// describe the deployed coordinator, not another copy on the host.
func (d *Deployer) existingBinary(ctx context.Context) (string, bool) {
	var locations []string
	if unit, err := d.readCoordinatorUnit(ctx); err == nil {
		if bin := execStartBinary(unit); bin != "" {
			locations = append(locations, bin)
		}
	}
	locations = append(locations, "/usr/local/bin/caam", "/usr/bin/caam")
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
{{- if .Path}}
Environment={{.Path}}
{{- end}}

[Install]
WantedBy=default.target
`

// SystemdUnitConfig holds the configuration for generating a systemd unit.
type SystemdUnitConfig struct {
	Type      string // "coordinator" or "agent"
	ExecStart string // Full command to run
	// Path, when set, is the service's PATH. systemd user services otherwise
	// get only the system directories, which misses a wezterm or tmux
	// installed under the home directory or /opt.
	Path string
}

// GenerateSystemdUnit generates a systemd unit file content.
func GenerateSystemdUnit(config SystemdUnitConfig) (string, error) {
	tmpl, err := template.New("systemd").Parse(systemdUnitTemplate)
	if err != nil {
		return "", err
	}

	data := config
	if data.Path != "" {
		data.Path = systemdEnvQuote("PATH=" + data.Path)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// systemdEnvQuote quotes a VAR=value assignment for an Environment= line.
func systemdEnvQuote(assignment string) string {
	assignment = strings.ReplaceAll(assignment, "%", "%%")
	assignment = strings.ReplaceAll(assignment, `\`, `\\`)
	assignment = strings.ReplaceAll(assignment, `"`, `\"`)
	return `"` + assignment + `"`
}

const (
	pathMarkerStart = "__CAAM_PATH__"
	pathMarkerEnd   = "__CAAM_END__"
)

// loginPathCommand prints the login shell's environment between markers, so
// profile scripts that write to stdout do not corrupt it. env prints PATH
// colon-joined in every shell, fish included. Remote commands run through
// the user's shell, so POSIX syntax is wrapped in sh -c.
var loginPathCommand = "sh -c " + shellEscape(`"${SHELL:-/bin/sh}" -lc 'echo `+pathMarkerStart+`; env; echo `+pathMarkerEnd+`' 2>/dev/null </dev/null`)

// parseMarkedPath extracts PATH from loginPathCommand output.
func parseMarkedPath(output string) string {
	start := strings.LastIndex(output, pathMarkerStart)
	if start < 0 {
		return ""
	}
	rest := output[start+len(pathMarkerStart):]
	end := strings.Index(rest, pathMarkerEnd)
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if value, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "PATH="); ok {
			return value
		}
	}
	return ""
}

// servicePath builds the coordinator's PATH: the user's login PATH, the
// directory holding the caam binary, and the system directories, deduplicated
// in that order. Relative entries are dropped; a service has no meaningful
// working directory.
func servicePath(loginPath, binaryPath string) string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || !strings.HasPrefix(dir, "/") || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	for _, dir := range strings.Split(loginPath, ":") {
		add(dir)
	}
	if i := strings.LastIndex(binaryPath, "/"); i > 0 {
		add(binaryPath[:i])
	}
	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		add(dir)
	}
	return strings.Join(dirs, ":")
}

// serviceEnvironmentCommand reports which multiplexers the service can run
// with path and whether the user's services survive logout.
func serviceEnvironmentCommand(path string) string {
	return "sh -c " + shellEscape("PATH="+shellEscape(path)+`; export PATH; `+
		`for b in wezterm tmux; do command -v "$b" >/dev/null 2>&1 && echo "found=$b"; done; `+
		`echo "linger=$(loginctl show-user "$(id -un)" --property=Linger --value 2>/dev/null)"; `+
		`echo "user=$(id -un)"`)
}

// serviceEnvironmentWarnings turns serviceEnvironmentCommand output into
// operator warnings for a coordinator using backend.
func serviceEnvironmentWarnings(output, backend string) []string {
	found := map[string]bool{}
	var linger, user string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "found="):
			found[strings.TrimPrefix(line, "found=")] = true
		case strings.HasPrefix(line, "linger="):
			linger = strings.TrimPrefix(line, "linger=")
		case strings.HasPrefix(line, "user="):
			user = strings.TrimPrefix(line, "user=")
		}
	}

	var warnings []string
	switch backend {
	case "wezterm", "tmux":
		if !found[backend] {
			warnings = append(warnings, fmt.Sprintf("%s is not on the coordinator's PATH; it cannot see any panes until %s is installed there", backend, backend))
		}
	default:
		if !found["wezterm"] && !found["tmux"] {
			warnings = append(warnings, "neither wezterm nor tmux is on the coordinator's PATH; it cannot see any panes until one is installed")
		}
	}
	if linger == "no" {
		if user == "" {
			user = "$USER"
		}
		warnings = append(warnings, fmt.Sprintf("lingering is off, so the coordinator stops when you log out; enable it with: sudo loginctl enable-linger %s", user))
	}
	return warnings
}

// CoordinatorExecStart builds the coordinator unit's ExecStart line. systemd
// does not expand "~", so the config path uses the %h (home) specifier.
func CoordinatorExecStart(binaryPath string) string {
	return systemdQuote(binaryPath) + " auth-coordinator --config %h/" + coordinatorConfigRel
}

// execStartBinary returns the executable of a unit's ExecStart line,
// undoing systemdQuote.
func execStartBinary(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		var arg string
		if rest, quoted := strings.CutPrefix(value, `"`); quoted {
			var b strings.Builder
			for i := 0; i < len(rest); i++ {
				c := rest[i]
				if c == '\\' && i+1 < len(rest) {
					i++
					b.WriteByte(rest[i])
					continue
				}
				if c == '"' {
					break
				}
				b.WriteByte(c)
			}
			arg = strings.ReplaceAll(b.String(), "$$", "$")
		} else {
			arg, _, _ = strings.Cut(value, " ")
		}
		return strings.ReplaceAll(arg, "%%", "%")
	}
	return ""
}

// coordinatorUnitPath returns the remote path of the coordinator unit file.
func (d *Deployer) coordinatorUnitPath(ctx context.Context) (string, error) {
	home, err := d.RemoteHome(ctx)
	if err != nil {
		return "", err
	}
	return home + "/.config/systemd/user/" + CoordinatorServiceName + ".service", nil
}

// readCoordinatorUnit returns the installed coordinator unit file.
func (d *Deployer) readCoordinatorUnit(ctx context.Context) (string, error) {
	path, err := d.coordinatorUnitPath(ctx)
	if err != nil {
		return "", err
	}
	return d.RunCommand(ctx, "cat "+shellEscape(path))
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
	// Warnings are conditions that let the deploy succeed but will stop the
	// coordinator from working later (no multiplexer, no lingering).
	Warnings []string
}

// ensureBinary makes a runnable caam available on the remote and returns its
// path. The local binary is uploaded when it matches the remote platform;
// otherwise an existing install is reused or the published release installed.
func (d *Deployer) ensureBinary(ctx context.Context, result *DeployResult) (string, error) {
	canUpload, reason := d.CanDeploy(ctx)
	if !canUpload {
		// The local binary cannot run there: install the release matching
		// this caam (any release when this is a development build).
		localVer, _ := d.GetLocalVersion()
		result.LocalVersion = localVer
		tag := releaseTag(localVer)
		if path, ok := d.existingBinary(ctx); ok {
			result.RemoteVersion, _ = d.GetRemoteVersion(ctx)
			if tag == "" || releaseTag(result.RemoteVersion) == tag {
				d.logger.Info("using existing remote caam (local binary targets another platform)",
					"machine", d.machine.Name,
					"path", path,
					"reason", reason)
				return path, nil
			}
		}
		path, err := d.InstallFromRelease(ctx, tag)
		if err != nil {
			return "", fmt.Errorf("%s; installing the caam release remotely failed: %w", reason, err)
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

	// Run the service with the user's login PATH so it finds the
	// multiplexer the user runs.
	pathCtx, cancelPath := context.WithTimeout(ctx, 15*time.Second)
	loginOut, err := d.RunCommand(pathCtx, loginPathCommand)
	cancelPath()
	if err != nil {
		d.logger.Debug("could not read login PATH", "machine", d.machine.Name, "error", err)
	}
	path := servicePath(parseMarkedPath(loginOut), installPath)

	unitConfig := SystemdUnitConfig{
		Type:      "Auth Recovery Coordinator",
		ExecStart: CoordinatorExecStart(installPath),
		Path:      path,
	}
	if err := d.WriteSystemdUnit(ctx, CoordinatorServiceName, unitConfig); err != nil {
		return fail(fmt.Errorf("systemd unit write failed: %w", err))
	}

	if err := d.EnableAndStartService(ctx, CoordinatorServiceName); err != nil {
		return fail(fmt.Errorf("service start failed: %w", err))
	}

	if envOut, err := d.RunCommand(ctx, serviceEnvironmentCommand(path)); err == nil {
		result.Warnings = serviceEnvironmentWarnings(envOut, config.Backend)
	} else {
		d.logger.Debug("could not check service environment", "machine", d.machine.Name, "error", err)
	}

	verifyCtx, cancel := context.WithTimeout(ctx, d.verifyTimeout)
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

// Upgrade actions reported by UpgradeCoordinator.
const (
	UpgradeUpToDate   = "up_to_date"
	UpgradeWouldApply = "would_upgrade"
	UpgradeUpgraded   = "upgraded"
	UpgradeRolledBack = "rolled_back"
	UpgradeFailed     = "failed"
)

// UpgradeOptions controls UpgradeCoordinator.
type UpgradeOptions struct {
	DryRun bool // report the plan without changing the host
	Force  bool // redeploy and restart even when the versions match
}

// UpgradeResult reports one coordinator upgrade.
type UpgradeResult struct {
	Machine     string   `json:"machine"`
	Action      string   `json:"action"`
	FromVersion string   `json:"from_version,omitempty"`
	ToVersion   string   `json:"to_version,omitempty"`
	Verified    bool     `json:"verified"`
	Warnings    []string `json:"warnings,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// ReadCoordinatorConfig reads the coordinator config deployed on the host.
func (d *Deployer) ReadCoordinatorConfig(ctx context.Context) (coordinator.FileConfig, error) {
	var config coordinator.FileConfig
	home, err := d.RemoteHome(ctx)
	if err != nil {
		return config, err
	}
	out, err := d.RunCommand(ctx, "cat "+shellEscape(home+"/"+coordinatorConfigRel))
	if err != nil {
		return config, fmt.Errorf("no coordinator config on %s (deploy one with 'caam setup distributed'): %w", d.machine.Name, err)
	}
	if err := json.Unmarshal([]byte(out), &config); err != nil {
		return config, fmt.Errorf("parse coordinator config on %s: %w", d.machine.Name, err)
	}
	return config, nil
}

// binaryPlan reports whether deploying would replace the remote binary and
// the versions involved, using the same rules as ensureBinary.
func (d *Deployer) binaryPlan(ctx context.Context) (needs bool, from, to string, err error) {
	from, _ = d.GetRemoteVersion(ctx)
	local, localErr := d.GetLocalVersion()
	if canUpload, _ := d.CanDeploy(ctx); !canUpload {
		tag := releaseTag(local)
		switch {
		case from == "":
			if tag == "" {
				tag = "latest release"
			}
			return true, from, tag, nil
		case tag == "":
			return false, from, from, nil
		default:
			return releaseTag(from) != tag, from, tag, nil
		}
	}
	if localErr != nil {
		return false, from, "", localErr
	}
	return from != local, from, local, nil
}

// shortVersion reduces `caam --version` output to its version field.
func shortVersion(versionOutput string) string {
	fields := strings.Fields(versionOutput)
	if len(fields) >= 2 && fields[0] == "caam" {
		return fields[1]
	}
	return strings.TrimSpace(versionOutput)
}

// prevBinarySuffix marks the last-known-good binary kept during an upgrade.
const prevBinarySuffix = ".caam-prev"

// coordinatorSnapshot is what an upgrade restores when the new coordinator
// fails verification.
type coordinatorSnapshot struct {
	unit   string // installed unit file; "" if none
	binary string // binary backed up to binary+prevBinarySuffix; "" if none
}

// snapshotCoordinator backs up the installed binary and unit. Both stay on
// the host (binary and unit path + prevBinarySuffix) for RollbackCoordinator.
func (d *Deployer) snapshotCoordinator(ctx context.Context) (coordinatorSnapshot, error) {
	var snap coordinatorSnapshot
	if unit, err := d.readCoordinatorUnit(ctx); err == nil {
		snap.unit = unit
		path, err := d.coordinatorUnitPath(ctx)
		if err != nil {
			return snap, err
		}
		if err := d.sshClient.WriteFile(path+prevBinarySuffix, []byte(unit), 0644); err != nil {
			return snap, fmt.Errorf("back up unit: %w", err)
		}
	}
	if bin, ok := d.existingBinary(ctx); ok {
		if _, err := d.RunCommand(ctx, withSudoFallback("cp -p "+shellEscape(bin)+" "+shellEscape(bin+prevBinarySuffix))); err != nil {
			return snap, fmt.Errorf("back up %s: %w", bin, err)
		}
		snap.binary = bin
	}
	return snap, nil
}

// withSudoFallback retries cmd with passwordless sudo, for binaries
// installed into root-owned directories.
func withSudoFallback(cmd string) string {
	return "sh -c " + shellEscape(cmd+" 2>/dev/null || sudo -n "+cmd)
}

// restoreCoordinator puts back the snapshotted binary and unit and restarts
// the service.
func (d *Deployer) restoreCoordinator(ctx context.Context, snap coordinatorSnapshot) error {
	if snap.binary != "" {
		// rename, not copy: a running executable cannot be overwritten.
		if _, err := d.RunCommand(ctx, withSudoFallback("mv -f "+shellEscape(snap.binary+prevBinarySuffix)+" "+shellEscape(snap.binary))); err != nil {
			return fmt.Errorf("restore %s: %w", snap.binary, err)
		}
	}
	if snap.unit != "" {
		path, err := d.coordinatorUnitPath(ctx)
		if err != nil {
			return err
		}
		if err := d.sshClient.WriteFile(path, []byte(snap.unit), 0644); err != nil {
			return fmt.Errorf("restore unit: %w", err)
		}
	}
	if _, err := d.RunCommand(ctx, "systemctl --user daemon-reload && systemctl --user restart "+shellEscape(CoordinatorServiceName)); err != nil {
		return fmt.Errorf("restart restored coordinator: %w", err)
	}
	return nil
}

// RollbackCoordinator restores the coordinator that the last upgrade
// replaced (its unit, and its binary when the backup is still there).
func (d *Deployer) RollbackCoordinator(ctx context.Context) *UpgradeResult {
	res := &UpgradeResult{Machine: d.machine.Name}
	fail := func(err error) *UpgradeResult {
		res.Action = UpgradeFailed
		res.Error = err.Error()
		return res
	}

	config, err := d.ReadCoordinatorConfig(ctx)
	if err != nil {
		return fail(err)
	}
	unitPath, err := d.coordinatorUnitPath(ctx)
	if err != nil {
		return fail(err)
	}
	prevUnit, err := d.RunCommand(ctx, "cat "+shellEscape(unitPath+prevBinarySuffix))
	if err != nil || strings.TrimSpace(prevUnit) == "" {
		return fail(fmt.Errorf("no previous coordinator recorded on %s; nothing to roll back", d.machine.Name))
	}
	from, _ := d.GetRemoteVersion(ctx)
	res.FromVersion = shortVersion(from)

	snap := coordinatorSnapshot{unit: prevUnit}
	if bin := execStartBinary(prevUnit); bin != "" {
		if _, err := d.RunCommand(ctx, "test -e "+shellEscape(bin+prevBinarySuffix)); err == nil {
			snap.binary = bin
		}
	}
	if err := d.restoreCoordinator(ctx, snap); err != nil {
		return fail(err)
	}
	to, _ := d.GetRemoteVersion(ctx)
	res.ToVersion = shortVersion(to)
	res.Action = UpgradeRolledBack

	vctx, cancel := context.WithTimeout(ctx, d.verifyTimeout)
	defer cancel()
	res.Verified = d.VerifyCoordinator(vctx, config) == nil
	return res
}

// UpgradeCoordinator brings the host's coordinator to this caam's version,
// keeping its deployed config. The previous binary and unit are restored
// when the upgraded coordinator does not pass verification.
func (d *Deployer) UpgradeCoordinator(ctx context.Context, opts UpgradeOptions) *UpgradeResult {
	res := &UpgradeResult{Machine: d.machine.Name}
	fail := func(err error) *UpgradeResult {
		res.Action = UpgradeFailed
		res.Error = err.Error()
		return res
	}
	verify := func(config coordinator.FileConfig) bool {
		vctx, cancel := context.WithTimeout(ctx, d.verifyTimeout)
		defer cancel()
		return d.VerifyCoordinator(vctx, config) == nil
	}

	config, err := d.ReadCoordinatorConfig(ctx)
	if err != nil {
		return fail(err)
	}
	needs, from, to, err := d.binaryPlan(ctx)
	if err != nil {
		return fail(err)
	}
	res.FromVersion, res.ToVersion = shortVersion(from), shortVersion(to)

	if !needs && !opts.Force {
		res.Action = UpgradeUpToDate
		res.Verified = verify(config)
		return res
	}
	if opts.DryRun {
		res.Action = UpgradeWouldApply
		return res
	}

	snap, err := d.snapshotCoordinator(ctx)
	if err != nil {
		return fail(err)
	}
	deployed, err := d.DeployCoordinator(ctx, config)
	if err == nil {
		res.Action = UpgradeUpgraded
		res.Verified = deployed.Verified
		res.Warnings = deployed.Warnings
		return res
	}

	d.logger.Warn("coordinator upgrade failed; rolling back", "machine", d.machine.Name, "error", err)
	res.Error = err.Error()
	if rbErr := d.restoreCoordinator(ctx, snap); rbErr != nil {
		res.Action = UpgradeFailed
		res.Error += "; rollback failed: " + rbErr.Error()
		return res
	}
	res.Action = UpgradeRolledBack
	res.Verified = verify(config)
	return res
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
