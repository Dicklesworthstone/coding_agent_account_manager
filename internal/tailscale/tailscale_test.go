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

// Test resilient JSON parsing with ParseStatus
func TestParseStatusResilient(t *testing.T) {
	status, err := ParseStatus([]byte(sampleStatusJSON))
	if err != nil {
		t.Fatalf("ParseStatus failed: %v", err)
	}

	if status.BackendState != "Running" {
		t.Errorf("expected BackendState=Running, got %s", status.BackendState)
	}

	if !status.IsRunning() {
		t.Error("expected IsRunning() to return true")
	}

	if status.HasWarnings() {
		t.Errorf("expected no warnings for valid JSON, got %d", len(status.Warnings))
	}
}

// Test parsing JSON with unknown fields (schema drift simulation)
const sampleStatusWithUnknownFields = `{
  "Version": "2.0.0-beta",
  "BackendState": "Running",
  "NewUnknownField": "should be ignored",
  "Self": {
    "ID": "n123",
    "HostName": "testhost",
    "TailscaleIPs": ["100.1.2.3"],
    "Online": true,
    "SomeNewField": 42
  },
  "Peer": {
    "key1": {
      "ID": "n456",
      "HostName": "peer1",
      "TailscaleIPs": ["100.4.5.6"],
      "Online": true,
      "ExperimentalField": true
    }
  }
}`

func TestParseStatusWithUnknownFields(t *testing.T) {
	status, err := ParseStatus([]byte(sampleStatusWithUnknownFields))
	if err != nil {
		t.Fatalf("ParseStatus failed with unknown fields: %v", err)
	}

	if status.BackendState != "Running" {
		t.Errorf("expected BackendState=Running, got %s", status.BackendState)
	}

	if status.Self == nil {
		t.Fatal("Self is nil")
	}

	if status.Self.HostName != "testhost" {
		t.Errorf("expected Self.HostName=testhost, got %s", status.Self.HostName)
	}

	if len(status.Peer) != 1 {
		t.Errorf("expected 1 peer, got %d", len(status.Peer))
	}
}

// Test parsing partial/malformed JSON
func TestParseStatusPartialJSON(t *testing.T) {
	// JSON with some valid fields but missing others
	partialJSON := `{
  "Version": "1.50.0",
  "BackendState": "Running"
}`

	status, err := ParseStatus([]byte(partialJSON))
	if err != nil {
		t.Fatalf("ParseStatus failed with partial JSON: %v", err)
	}

	if status.Version != "1.50.0" {
		t.Errorf("expected Version=1.50.0, got %s", status.Version)
	}

	if status.Self != nil {
		t.Error("expected Self to be nil for partial JSON")
	}

	if status.Peer != nil && len(status.Peer) > 0 {
		t.Error("expected empty Peer map for partial JSON")
	}
}

// Test missing BackendState generates warning
func TestParseStatusMissingBackendState(t *testing.T) {
	noBackendState := `{
  "Version": "1.50.0",
  "Self": {
    "HostName": "test"
  }
}`

	status, err := ParseStatus([]byte(noBackendState))
	if err != nil {
		t.Fatalf("ParseStatus failed: %v", err)
	}

	if !status.HasWarnings() {
		t.Error("expected warning for missing BackendState")
	}

	foundWarning := false
	for _, w := range status.Warnings {
		if w.Field == "BackendState" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Error("expected BackendState warning in warnings list")
	}
}

// Test ParseWarning String method
func TestParseWarningString(t *testing.T) {
	w := ParseWarning{
		Field:   "Peer[abc123]",
		Message: "missing required field",
	}

	expected := "Peer[abc123]: missing required field"
	if w.String() != expected {
		t.Errorf("ParseWarning.String() = %q, want %q", w.String(), expected)
	}
}

// Test Status.IsRunning for various states
func TestStatusIsRunning(t *testing.T) {
	tests := []struct {
		state    string
		expected bool
	}{
		{"Running", true},
		{"Stopped", false},
		{"Starting", false},
		{"", false},
	}

	for _, tt := range tests {
		status := &Status{BackendState: tt.state}
		if status.IsRunning() != tt.expected {
			t.Errorf("IsRunning() for state %q = %v, want %v", tt.state, status.IsRunning(), tt.expected)
		}
	}
}

// Test optional Peer fields
func TestPeerOptionalFields(t *testing.T) {
	jsonWithOptionalFields := `{
  "ID": "n123",
  "HostName": "testpeer",
  "TailscaleIPs": ["100.1.2.3"],
  "Online": true,
  "UserID": 12345,
  "ExitNode": true,
  "LastSeen": "2025-01-20T10:30:00Z"
}`

	var peer Peer
	if err := json.Unmarshal([]byte(jsonWithOptionalFields), &peer); err != nil {
		t.Fatalf("failed to parse peer JSON: %v", err)
	}

	if peer.UserID == nil {
		t.Error("expected UserID to be set")
	} else if *peer.UserID != 12345 {
		t.Errorf("expected UserID=12345, got %d", *peer.UserID)
	}

	if peer.ExitNode == nil {
		t.Error("expected ExitNode to be set")
	} else if !*peer.ExitNode {
		t.Error("expected ExitNode=true")
	}

	if peer.LastSeen == nil {
		t.Error("expected LastSeen to be set")
	}
}
