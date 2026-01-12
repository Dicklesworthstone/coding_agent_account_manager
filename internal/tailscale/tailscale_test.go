package tailscale

import (
	"encoding/json"
	"testing"
)

// Sample JSON from actual tailscale status --json output
const sampleStatusJSON = `{
  "Version": "1.92.3-ta17f36b9b-ga4dc88aac",
  "TUN": true,
  "BackendState": "Running",
  "Self": {
    "ID": "n5vXP5MkcX11CNTRL",
    "HostName": "threadripperje",
    "DNSName": "threadripperje.tail1f21e.ts.net.",
    "OS": "linux",
    "TailscaleIPs": ["100.91.120.17", "fd7a:115c:a1e0::4534:7811"],
    "Online": true
  },
  "Peer": {
    "nodekey:abc123": {
      "ID": "n1234",
      "HostName": "SuperServer",
      "DNSName": "superserver.tail1f21e.ts.net.",
      "TailscaleIPs": ["100.90.148.85", "fd7a:115c:a1e0::f334:9455"],
      "Online": true,
      "OS": "linux"
    },
    "nodekey:def456": {
      "ID": "n5678",
      "HostName": "SenseDemoBox",
      "DNSName": "sensedemobox.tail1f21e.ts.net.",
      "TailscaleIPs": ["100.100.118.85", "fd7a:115c:a1e0::6a34:7655"],
      "Online": true,
      "OS": "linux"
    },
    "nodekey:ghi789": {
      "ID": "n9012",
      "HostName": "Jeffrey's Mac mini",
      "DNSName": "jeffreys-mac-mini.tail1f21e.ts.net.",
      "TailscaleIPs": ["100.114.183.31", "fd7a:115c:a1e0::9734:b71f"],
      "Online": true,
      "OS": "darwin"
    },
    "nodekey:jkl012": {
      "ID": "n3456",
      "HostName": "ubuntu-vm",
      "DNSName": "ubuntu-vm.tail1f21e.ts.net.",
      "TailscaleIPs": ["100.73.182.80"],
      "Online": false,
      "OS": "linux"
    }
  }
}`

func TestParseStatus(t *testing.T) {
	var status Status
	if err := json.Unmarshal([]byte(sampleStatusJSON), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	if status.BackendState != "Running" {
		t.Errorf("expected BackendState=Running, got %s", status.BackendState)
	}

	if status.Self == nil {
		t.Fatal("Self is nil")
	}

	if status.Self.HostName != "threadripperje" {
		t.Errorf("expected Self.HostName=threadripperje, got %s", status.Self.HostName)
	}

	if len(status.Peer) != 4 {
		t.Errorf("expected 4 peers, got %d", len(status.Peer))
	}
}

func TestPeerGetIPv4(t *testing.T) {
	peer := &Peer{
		TailscaleIPs: []string{"100.90.148.85", "fd7a:115c:a1e0::f334:9455"},
	}

	ipv4 := peer.GetIPv4()
	if ipv4 != "100.90.148.85" {
		t.Errorf("expected 100.90.148.85, got %s", ipv4)
	}
}

func TestPeerGetIPv4OnlyIPv6(t *testing.T) {
	peer := &Peer{
		TailscaleIPs: []string{"fd7a:115c:a1e0::f334:9455"},
	}

	ipv4 := peer.GetIPv4()
	if ipv4 != "" {
		t.Errorf("expected empty string for IPv6-only peer, got %s", ipv4)
	}
}

func TestPeerShortDNSName(t *testing.T) {
	tests := []struct {
		dnsName  string
		hostName string
		expected string
	}{
		{"superserver.tail1f21e.ts.net.", "SuperServer", "superserver"},
		{"", "SuperServer", "SuperServer"},
		{"jeffreys-mac-mini.tail1f21e.ts.net.", "Jeffrey's Mac mini", "jeffreys-mac-mini"},
	}

	for _, tt := range tests {
		peer := &Peer{DNSName: tt.dnsName, HostName: tt.hostName}
		got := peer.ShortDNSName()
		if got != tt.expected {
			t.Errorf("ShortDNSName(%q, %q) = %q, want %q", tt.dnsName, tt.hostName, got, tt.expected)
		}
	}
}

func TestFindPeerByHostnameInStatus(t *testing.T) {
	var status Status
	if err := json.Unmarshal([]byte(sampleStatusJSON), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	// Helper to find in parsed status
	findByHostname := func(hostname string) *Peer {
		for _, peer := range status.Peer {
			if peer.HostName == hostname {
				return peer
			}
		}
		return nil
	}

	tests := []struct {
		search   string
		expected string
	}{
		{"SuperServer", "SuperServer"},
		{"SenseDemoBox", "SenseDemoBox"},
	}

	for _, tt := range tests {
		peer := findByHostname(tt.search)
		if peer == nil {
			t.Errorf("findByHostname(%q) returned nil", tt.search)
			continue
		}
		if peer.HostName != tt.expected {
			t.Errorf("findByHostname(%q) = %q, want %q", tt.search, peer.HostName, tt.expected)
		}
	}
}

func TestOnlinePeersCount(t *testing.T) {
	var status Status
	if err := json.Unmarshal([]byte(sampleStatusJSON), &status); err != nil {
		t.Fatalf("failed to parse status JSON: %v", err)
	}

	onlineCount := 0
	for _, peer := range status.Peer {
		if peer.Online {
			onlineCount++
		}
	}

	if onlineCount != 3 {
		t.Errorf("expected 3 online peers, got %d", onlineCount)
	}
}
