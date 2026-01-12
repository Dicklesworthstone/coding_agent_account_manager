// Package tailscale provides Tailscale network detection and peer discovery.
package tailscale

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
)

// Status represents the output of 'tailscale status --json'.
type Status struct {
	Version      string           `json:"Version"`
	BackendState string           `json:"BackendState"`
	Self         *Peer            `json:"Self"`
	Peer         map[string]*Peer `json:"Peer"`
}

// Peer represents a machine on the Tailscale network.
type Peer struct {
	ID           string   `json:"ID"`
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	OS           string   `json:"OS"`
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

// GetStatus returns the current Tailscale status.
func (c *Client) GetStatus(ctx context.Context) (*Status, error) {
	cmd := exec.CommandContext(ctx, c.binaryPath, "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var status Status
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, err
	}

	return &status, nil
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
