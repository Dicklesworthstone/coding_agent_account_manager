// Package tailscale provides Tailscale network detection and peer discovery.
package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ParseWarning represents a non-fatal issue during JSON parsing.
type ParseWarning struct {
	Field   string
	Message string
}

func (w ParseWarning) String() string {
	return fmt.Sprintf("%s: %s", w.Field, w.Message)
}

// Status represents the output of 'tailscale status --json'.
// Fields are pointers/optional to tolerate schema drift in tailscale CLI output.
type Status struct {
	Version      string           `json:"Version"`
	BackendState string           `json:"BackendState"`
	Self         *Peer            `json:"Self"`
	Peer         map[string]*Peer `json:"Peer"`

	// Warnings collects non-fatal parsing issues for logging.
	Warnings []ParseWarning `json:"-"`
}

// Peer represents a machine on the Tailscale network.
// Uses pointers for optional fields to distinguish missing from empty values.
type Peer struct {
	ID           string   `json:"ID"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	OS           string   `json:"OS"`

	// Additional optional fields that may appear in newer tailscale versions.
	UserID      *int64  `json:"UserID,omitempty"`
	PublicKey   *string `json:"PublicKey,omitempty"`
	ExitNode    *bool   `json:"ExitNode,omitempty"`
	Active      *bool   `json:"Active,omitempty"`
	LastSeen    *string `json:"LastSeen,omitempty"`
	LastWrite   *string `json:"LastWrite,omitempty"`
	LastHandshake *string `json:"LastHandshake,omitempty"`
}

// VersionInfo contains tailscale CLI version information.
type VersionInfo struct {
	CLIVersion   string // Version from 'tailscale version'
	Short        string // Short version (e.g., "1.56.1")
	Long         string // Full version string
	Commit       string // Git commit if available
	StatusVersion string // Version from status JSON (may differ)
}

// Client provides access to Tailscale status information.
type Client struct {
	binaryPath string
}

// NewClient creates a new Tailscale client.
func NewClient() *Client {
	return &Client{
		binaryPath: "tailscale",
	}
}

// IsAvailable checks if tailscaled is running and accessible.
func (c *Client) IsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, c.binaryPath, "status", "--json")
	err := cmd.Run()
	return err == nil
}

// GetVersion returns version information from the tailscale CLI.
// This is useful for logging to help debug schema drift issues.
func (c *Client) GetVersion(ctx context.Context) (*VersionInfo, error) {
	cmd := exec.CommandContext(ctx, c.binaryPath, "version")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale version: %w", err)
	}

	info := &VersionInfo{
		CLIVersion: strings.TrimSpace(string(output)),
	}

	// Parse the version output - typically:
	// 1.56.1
	//   tailscale commit: abc123...
	lines := strings.Split(info.CLIVersion, "\n")
	if len(lines) > 0 {
		info.Short = strings.TrimSpace(lines[0])
		info.Long = info.Short
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "tailscale commit:") {
			info.Commit = strings.TrimSpace(strings.TrimPrefix(line, "tailscale commit:"))
		}
	}

	return info, nil
}

// GetVersionString returns just the short version string for logging.
func (c *Client) GetVersionString(ctx context.Context) string {
	info, err := c.GetVersion(ctx)
	if err != nil {
		return "unknown"
	}
	return info.Short
}

// GetStatus returns the current Tailscale status.
// Uses lenient JSON parsing to tolerate schema drift between tailscale versions.
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	cmd := exec.CommandContext(ctx, c.binaryPath, "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}

	return ParseStatus(output)
}

// ParseStatus parses tailscale status JSON with lenient handling.
// This is exported for testing with fixture data.
func ParseStatus(data []byte) (*Status, error) {
	var status Status
	status.Warnings = make([]ParseWarning, 0)

	// First, try standard unmarshal
	if err := json.Unmarshal(data, &status); err != nil {
		// Try lenient parsing - decode what we can
		status.Warnings = append(status.Warnings, ParseWarning{
			Field:   "root",
			Message: fmt.Sprintf("partial parse due to: %v", err),
		})

		// Attempt to extract just the essential fields using a map
		var raw map[string]json.RawMessage
		if err2 := json.Unmarshal(data, &raw); err2 != nil {
			return nil, fmt.Errorf("tailscale status JSON: %w", err)
		}

		// Extract Version
		if v, ok := raw["Version"]; ok {
			json.Unmarshal(v, &status.Version)
		}

		// Extract BackendState
		if v, ok := raw["BackendState"]; ok {
			json.Unmarshal(v, &status.BackendState)
		}

		// Extract Self
		if v, ok := raw["Self"]; ok {
			var self Peer
			if err := json.Unmarshal(v, &self); err == nil {
				status.Self = &self
			}
		}

		// Extract Peer map with lenient peer parsing
		if v, ok := raw["Peer"]; ok {
			var peerMap map[string]json.RawMessage
			if err := json.Unmarshal(v, &peerMap); err == nil {
				status.Peer = make(map[string]*Peer)
				for k, pv := range peerMap {
					var peer Peer
					if err := json.Unmarshal(pv, &peer); err == nil {
						status.Peer[k] = &peer
					} else {
						status.Warnings = append(status.Warnings, ParseWarning{
							Field:   fmt.Sprintf("Peer[%s]", k),
							Message: fmt.Sprintf("skipped: %v", err),
						})
					}
				}
			}
		}
	}

	// Validate essential fields
	if status.BackendState == "" {
		status.Warnings = append(status.Warnings, ParseWarning{
			Field:   "BackendState",
			Message: "missing or empty",
		})
	}

	return &status, nil
}

// HasWarnings returns true if parsing produced any warnings.
func (s *Status) HasWarnings() bool {
	return len(s.Warnings) > 0
}

// IsRunning returns true if tailscale backend is in a running state.
func (s *Status) IsRunning() bool {
	return s.BackendState == "Running"
}

// GetSelf returns information about the local machine.
func (c *Client) GetSelf(ctx context.Context) (*Peer, error) {
	status, err := c.GetStatus(ctx)
	if err != nil {
		return nil, err
	}
	return status.Self, nil
}

// GetPeers returns all peers on the Tailscale network.
func (c *Client) GetPeers(ctx context.Context) ([]*Peer, error) {
	status, err := c.GetStatus(ctx)
	if err != nil {
		return nil, err
	}

	peers := make([]*Peer, 0, len(status.Peer))
	for _, peer := range status.Peer {
		peers = append(peers, peer)
	}
	return peers, nil
}

// GetOnlinePeers returns only online peers.
func (c *Client) GetOnlinePeers(ctx context.Context) ([]*Peer, error) {
	peers, err := c.GetPeers(ctx)
	if err != nil {
		return nil, err
	}

	online := make([]*Peer, 0)
	for _, peer := range peers {
		if peer.Online {
			online = append(online, peer)
		}
	}
	return online, nil
}

// FindPeerByHostname finds a peer by hostname (case-insensitive, fuzzy).
func (c *Client) FindPeerByHostname(ctx context.Context, hostname string) (*Peer, error) {
	peers, err := c.GetPeers(ctx)
	if err != nil {
		return nil, err
	}

	hostname = strings.ToLower(hostname)

	// Try exact match first
	for _, peer := range peers {
		if strings.ToLower(peer.HostName) == hostname {
			return peer, nil
		}
	}

	// Try prefix match
	for _, peer := range peers {
		if strings.HasPrefix(strings.ToLower(peer.HostName), hostname) {
			return peer, nil
		}
	}

	// Try contains match
	for _, peer := range peers {
		if strings.Contains(strings.ToLower(peer.HostName), hostname) {
			return peer, nil
		}
	}

	return nil, nil
}

// FindPeerByIP finds a peer by any of its IPs.
func (c *Client) FindPeerByIP(ctx context.Context, ip string) (*Peer, error) {
	peers, err := c.GetPeers(ctx)
	if err != nil {
		return nil, err
	}

	for _, peer := range peers {
		for _, peerIP := range peer.TailscaleIPs {
			if peerIP == ip {
				return peer, nil
			}
		}
	}

	return nil, nil
}

// GetIPv4 returns the first IPv4 address from a peer's TailscaleIPs.
func (p *Peer) GetIPv4() string {
	for _, ip := range p.TailscaleIPs {
		// IPv4 addresses don't contain ':'
		if !strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}

// ShortDNSName returns the hostname portion of the DNS name.
func (p *Peer) ShortDNSName() string {
	if p.DNSName == "" {
		return p.HostName
	}
	// DNSName is like "superserver.tail1f21e.ts.net."
	parts := strings.Split(p.DNSName, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return p.HostName
}
