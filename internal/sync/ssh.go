package sync

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ConnectOptions configures SSH connection behavior.
type ConnectOptions struct {
	// Timeout for connection establishment. Default: 10s.
	Timeout time.Duration

	// UseAgent enables ssh-agent authentication. Default: true.
	UseAgent bool

	// SkipHostKeyCheck disables host key verification (insecure, for testing only).
	SkipHostKeyCheck bool
}

// DefaultConnectOptions returns the default connection options.
func DefaultConnectOptions() ConnectOptions {
	return ConnectOptions{
		Timeout:  10 * time.Second,
		UseAgent: true,
	}
}

// SSHClient wraps an SSH connection with SFTP support.
type SSHClient struct {
	machine    *Machine
	client     *ssh.Client
	sftp       *sftp.Client
	agentConn  net.Conn // SSH agent connection (needs cleanup)
	connected  bool
	lastConnAt time.Time
	opts       ConnectOptions
}

// NewSSHClient creates a new SSH client for the given machine.
func NewSSHClient(m *Machine) *SSHClient {
	return &SSHClient{
		machine: m,
		opts:    DefaultConnectOptions(),
	}
}

// Connect establishes an SSH connection to the machine.
func (c *SSHClient) Connect(opts ConnectOptions) error {
	if c.connected {
		return nil // Already connected
	}

	c.opts = opts
	if c.opts.Timeout == 0 {
		c.opts.Timeout = 10 * time.Second
	}

	// Get authentication methods
	authMethods, err := c.getAuthMethods()
	if err != nil {
		return &SSHError{
			Machine:    c.machine,
			Operation:  "auth",
			Underlying: err,
		}
	}

	if len(authMethods) == 0 {
		return &SSHError{
			Machine:    c.machine,
			Operation:  "auth",
			Underlying: errors.New("no authentication methods available"),
		}
	}

	// Determine user
	user := c.machine.SSHUser
	if user == "" {
		user = os.Getenv("USER")
		if user == "" {
			user = os.Getenv("USERNAME") // Windows
		}
	}

	// Get host key callback
	hostKeyCallback, err := c.hostKeyCallback()
	if err != nil {
		return &SSHError{
			Machine:    c.machine,
			Operation:  "hostkey",
			Underlying: err,
		}
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         c.opts.Timeout,
	}

	// Connect
	addr := c.machine.HostPort()
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return &SSHError{
			Machine:    c.machine,
			Operation:  "connect",
			Underlying: err,
		}
	}

	c.client = client
	c.connected = true
	c.lastConnAt = time.Now()
	c.machine.SetOnline()

	return nil
}

// Disconnect closes the SSH connection.
func (c *SSHClient) Disconnect() error {
	if c.sftp != nil {
		c.sftp.Close()
		c.sftp = nil
	}
	if c.agentConn != nil {
		c.agentConn.Close()
		c.agentConn = nil
	}
	if c.client != nil {
		err := c.client.Close()
		c.client = nil
		c.connected = false
		return err
	}
	return nil
}

// IsConnected returns true if the connection is established.
func (c *SSHClient) IsConnected() bool {
	return c.connected && c.client != nil
}

// getAuthMethods returns available SSH authentication methods.
func (c *SSHClient) getAuthMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1. Try ssh-agent first
	if c.opts.UseAgent {
		if agentAuth, agentConn, err := getSSHAgentAuth(); err == nil {
			methods = append(methods, agentAuth)
			c.agentConn = agentConn // Store for cleanup in Disconnect()
		}
	}

	// 2. Add key file if specified
	if c.machine.SSHKeyPath != "" {
		if signer, err := loadSSHKey(c.machine.SSHKeyPath); err == nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	// 3. Try default keys
	for _, keyPath := range defaultKeyPaths() {
		if signer, err := loadSSHKey(keyPath); err == nil {
			methods = append(methods, ssh.PublicKeys(signer))
		}
	}

	return methods, nil
}

// hostKeyCallback returns the host key verification callback.
func (c *SSHClient) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if c.opts.SkipHostKeyCheck {
		return ssh.InsecureIgnoreHostKey(), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Can't determine home directory - fail secure rather than silently ignoring host keys
		return nil, fmt.Errorf("cannot determine home directory for known_hosts: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

	// Try to use existing known_hosts
	callback, err := knownhosts.New(knownHostsPath)
	if err == nil {
		// Wrap to auto-add unknown hosts (TOFU)
		return autoAddHostKeyCallback(callback, knownHostsPath), nil
	}

	// No known_hosts file - create directory and auto-add
	sshDir := filepath.Dir(knownHostsPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		// Can't create .ssh directory - fail secure rather than silently ignoring host keys
		return nil, fmt.Errorf("cannot create .ssh directory for known_hosts: %w", err)
	}

	return autoAddHostKeyCallback(nil, knownHostsPath), nil
}

// autoAddHostKeyCallback wraps a host key callback to auto-add unknown hosts (TOFU).
func autoAddHostKeyCallback(existing ssh.HostKeyCallback, knownHostsPath string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// First check if it's already known
		if existing != nil {
			err := existing(hostname, remote, key)
			if err == nil {
				return nil // Known and matches
			}

			// Check if it's a mismatch (security issue) vs just unknown
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
				// Key CHANGED - this is a security concern
				return fmt.Errorf("host key changed for %s - possible MITM attack", hostname)
			}
		}

		// Unknown host - auto-add (TOFU)
		if err := addToKnownHosts(knownHostsPath, hostname, key); err != nil {
			// Log but don't fail - the connection can still proceed
			fmt.Fprintf(os.Stderr, "Warning: could not add %s to known hosts: %v\n", hostname, err)
		}

		return nil
	}
}

// addToKnownHosts appends a host key to the known_hosts file.
func addToKnownHosts(path, hostname string, key ssh.PublicKey) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	line := knownhosts.Line([]string{hostname}, key)
	_, err = fmt.Fprintln(f, line)
	return err
}

// getSSHAgentAuth returns an authentication method using ssh-agent.
// Returns the auth method and the connection (caller must close when done).
func getSSHAgentAuth() (ssh.AuthMethod, net.Conn, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, nil, errors.New("SSH_AUTH_SOCK not set")
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, nil, err
	}

	agentClient := agent.NewClient(conn)
	return ssh.PublicKeysCallback(agentClient.Signers), conn, nil
}

// loadSSHKey loads an SSH private key from a file.
func loadSSHKey(keyPath string) (ssh.Signer, error) {
	keyPath = expandPath(keyPath)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		// Check if passphrase protected
		var passErr *ssh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, fmt.Errorf("key %s is passphrase-protected; use ssh-agent", keyPath)
		}
		return nil, err
	}

	return signer, nil
}

// defaultKeyPaths returns the default SSH key paths to try.
func defaultKeyPaths() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	sshDir := filepath.Join(homeDir, ".ssh")
	return []string{
		filepath.Join(sshDir, "id_ed25519"),
		filepath.Join(sshDir, "id_rsa"),
		filepath.Join(sshDir, "id_ecdsa"),
		filepath.Join(sshDir, "id_dsa"),
	}
}

// ensureSFTP initializes the SFTP client if needed.
func (c *SSHClient) ensureSFTP() error {
	if c.sftp != nil {
		return nil
	}

	if !c.connected {
		return errors.New("not connected")
	}

	sftpClient, err := sftp.NewClient(c.client)
	if err != nil {
		return fmt.Errorf("SFTP initialization failed: %w", err)
	}

	c.sftp = sftpClient
	return nil
}

// ReadFile reads a file from the remote machine.
func (c *SSHClient) ReadFile(remotePath string) ([]byte, error) {
	if err := c.ensureSFTP(); err != nil {
		return nil, &SSHError{Machine: c.machine, Operation: "read", Underlying: err}
	}

	f, err := c.sftp.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(f)
}

// WriteFile writes a file to the remote machine atomically.
func (c *SSHClient) WriteFile(remotePath string, data []byte, mode os.FileMode) error {
	if err := c.ensureSFTP(); err != nil {
		return &SSHError{Machine: c.machine, Operation: "write", Underlying: err}
	}

	// Atomic write: temp file + rename
	dir := posixDir(remotePath)
	tmpName := fmt.Sprintf(".caam_tmp_%s", randomString(8))
	tmpPath := posixJoin(dir, tmpName)

	// Ensure directory exists
	if err := c.MkdirAll(dir); err != nil {
		// Directory might already exist, continue
	}

	f, err := c.sftp.Create(tmpPath)
	if err != nil {
		return err
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		c.sftp.Remove(tmpPath)
		return err
	}

	if err := f.Close(); err != nil {
		c.sftp.Remove(tmpPath)
		return err
	}

	if err := c.sftp.Chmod(tmpPath, mode); err != nil {
		c.sftp.Remove(tmpPath)
		return err
	}

	// Rename to final path (atomic on POSIX)
	if err := c.sftp.Rename(tmpPath, remotePath); err != nil {
		c.sftp.Remove(tmpPath)
		return err
	}

	return nil
}

// FileExists checks if a file exists on the remote machine.
func (c *SSHClient) FileExists(remotePath string) (bool, error) {
	if err := c.ensureSFTP(); err != nil {
		return false, &SSHError{Machine: c.machine, Operation: "stat", Underlying: err}
	}

	_, err := c.sftp.Stat(remotePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FileModTime returns the modification time of a remote file.
func (c *SSHClient) FileModTime(remotePath string) (time.Time, error) {
	if err := c.ensureSFTP(); err != nil {
		return time.Time{}, &SSHError{Machine: c.machine, Operation: "stat", Underlying: err}
	}

	info, err := c.sftp.Stat(remotePath)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// ListDir lists files in a directory on the remote machine.
func (c *SSHClient) ListDir(remotePath string) ([]os.FileInfo, error) {
	if err := c.ensureSFTP(); err != nil {
		return nil, &SSHError{Machine: c.machine, Operation: "readdir", Underlying: err}
	}

	return c.sftp.ReadDir(remotePath)
}

// MkdirAll creates a directory with parents on the remote machine.
func (c *SSHClient) MkdirAll(remotePath string) error {
	if err := c.ensureSFTP(); err != nil {
		return &SSHError{Machine: c.machine, Operation: "mkdir", Underlying: err}
	}

	return c.sftp.MkdirAll(remotePath)
}

// Remove deletes a file on the remote machine.
func (c *SSHClient) Remove(remotePath string) error {
	if err := c.ensureSFTP(); err != nil {
		return &SSHError{Machine: c.machine, Operation: "remove", Underlying: err}
	}

	return c.sftp.Remove(remotePath)
}

// BatchRead reads multiple files efficiently (single SFTP session).
func (c *SSHClient) BatchRead(paths []string) (map[string][]byte, error) {
	if err := c.ensureSFTP(); err != nil {
		return nil, &SSHError{Machine: c.machine, Operation: "batch_read", Underlying: err}
	}

	result := make(map[string][]byte)
	for _, path := range paths {
		data, err := c.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Skip missing files
			}
			return result, err
		}
		result[path] = data
	}
	return result, nil
}

// BatchWrite writes multiple files efficiently (single SFTP session).
func (c *SSHClient) BatchWrite(files map[string][]byte, mode os.FileMode) error {
	if err := c.ensureSFTP(); err != nil {
		return &SSHError{Machine: c.machine, Operation: "batch_write", Underlying: err}
	}

	for path, data := range files {
		if err := c.WriteFile(path, data, mode); err != nil {
			return err
		}
	}
	return nil
}

// posixJoin joins path elements using forward slashes (for SFTP/remote paths).
// Unlike filepath.Join, this always uses forward slashes regardless of OS.
func posixJoin(elem ...string) string {
	if len(elem) == 0 {
		return ""
	}
	// Filter empty elements and join with forward slash
	var parts []string
	for _, e := range elem {
		if e != "" {
			parts = append(parts, e)
		}
	}
	result := strings.Join(parts, "/")
	// Clean up double slashes
	for strings.Contains(result, "//") {
		result = strings.ReplaceAll(result, "//", "/")
	}
	return result
}

// posixDir returns the directory portion of a path using forward slashes.
func posixDir(path string) string {
	// Find last forward slash
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash < 0 {
		return "."
	}
	if lastSlash == 0 {
		return "/"
	}
	return path[:lastSlash]
}

// randomString generates a random string of the given length.
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

// SSHError represents an SSH operation error.
type SSHError struct {
	Machine    *Machine
	Operation  string
	Underlying error
}

func (e *SSHError) Error() string {
	return fmt.Sprintf("SSH error on %s during %s: %v",
		e.Machine.Name, e.Operation, e.Underlying)
}

func (e *SSHError) Unwrap() error {
	return e.Underlying
}

// IsTimeout checks if the error is a timeout error.
func (e *SSHError) IsTimeout() bool {
	if e.Underlying == nil {
		return false
	}
	var netErr net.Error
	if errors.As(e.Underlying, &netErr) {
		return netErr.Timeout()
	}
	return strings.Contains(e.Underlying.Error(), "timeout")
}

// IsAuthFailure checks if the error is an authentication failure.
func (e *SSHError) IsAuthFailure() bool {
	if e.Operation == "auth" {
		return true
	}
	if e.Underlying == nil {
		return false
	}
	msg := e.Underlying.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "permission denied")
}

// IsNetworkError checks if the error is a network error.
func (e *SSHError) IsNetworkError() bool {
	if e.Operation == "connect" && e.Underlying != nil {
		return true
	}
	var netErr net.Error
	return errors.As(e.Underlying, &netErr)
}

// IsHostKeyMismatch checks if the error is a host key mismatch.
func (e *SSHError) IsHostKeyMismatch() bool {
	if e.Underlying == nil {
		return false
	}
	return strings.Contains(e.Underlying.Error(), "host key changed")
}

// ConnectivityResult contains the results of a connectivity test.
type ConnectivityResult struct {
	Machine      *Machine
	Success      bool
	Latency      time.Duration
	SSHVersion   string
	SFTPWorks    bool
	CAAMFound    bool
	CAAMVersion  string
	ProfileCount int
	Error        error
	ErrorType    string
}

// TestMachineConnectivity tests SSH connectivity to a machine.
func TestMachineConnectivity(m *Machine, opts ConnectOptions) *ConnectivityResult {
	result := &ConnectivityResult{
		Machine: m,
	}

	client := NewSSHClient(m)
	start := time.Now()

	// Test connection
	if err := client.Connect(opts); err != nil {
		result.Error = err
		result.Success = false

		var sshErr *SSHError
		if errors.As(err, &sshErr) {
			if sshErr.IsTimeout() {
				result.ErrorType = "timeout"
			} else if sshErr.IsAuthFailure() {
				result.ErrorType = "auth"
			} else if sshErr.IsNetworkError() {
				result.ErrorType = "network"
			} else if sshErr.IsHostKeyMismatch() {
				result.ErrorType = "hostkey"
			} else {
				result.ErrorType = "unknown"
			}
		} else {
			result.ErrorType = "unknown"
		}

		return result
	}
	defer client.Disconnect()

	result.Latency = time.Since(start)
	result.Success = true

	// Get SSH version
	if client.client != nil {
		result.SSHVersion = string(client.client.ServerVersion())
	}

	// Test SFTP
	if err := client.ensureSFTP(); err != nil {
		result.SFTPWorks = false
		result.Error = err
		result.ErrorType = "sftp"
		return result
	}
	result.SFTPWorks = true

	// Check for caam data directory
	caamDataDir := SyncDataDir()
	if m.RemotePath != "" {
		caamDataDir = m.RemotePath
	}

	exists, err := client.FileExists(caamDataDir)
	if err == nil && exists {
		result.CAAMFound = true

		// Count profiles (basic check)
		// Use posixJoin for remote paths since SFTP always uses forward slashes
		vaultDir := posixJoin(posixDir(caamDataDir), "vault")
		if entries, err := client.ListDir(vaultDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					// Count profiles in each provider
					providerDir := posixJoin(vaultDir, e.Name())
					if profiles, err := client.ListDir(providerDir); err == nil {
						result.ProfileCount += len(profiles)
					}
				}
			}
		}
	}

	return result
}
