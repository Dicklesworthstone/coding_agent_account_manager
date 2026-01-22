package wezterm

import (
	"os"
	"path/filepath"
	"testing"
)

// Sample WezTerm config based on user's actual config
const sampleConfig = `local wezterm = require 'wezterm'
local config = wezterm.config_builder()

-- SSH Domains for Contabo servers
config.ssh_domains = {
  {
    name = 'csd',
    remote_address = '144.126.137.164',
    username = 'ubuntu',
    multiplexing = 'WezTerm',
    assume_shell = 'Posix',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/contabo_new_baremetal_sense_demo_box.pem',
    },
  },
  {
    name = 'css',
    remote_address = '209.145.54.164',
    username = 'ubuntu',
    multiplexing = 'WezTerm',
    assume_shell = 'Posix',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/contabo_new_baremetal_superserver_box.pem',
    },
  },
  {
    name = 'trj',
    remote_address = '10.10.10.1',
    username = 'ubuntu',
    multiplexing = 'WezTerm',
    assume_shell = 'Posix',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/trj_ed25519',
    },
  },
}

-- Rest of config...
config.font_size = 16.0
return config
`

func TestExtractSSHDomains(t *testing.T) {
	domains := extractSSHDomains(sampleConfig)

	if len(domains) != 3 {
		t.Fatalf("expected 3 domains, got %d", len(domains))
	}

	// Check first domain (csd)
	csd := domains[0]
	if csd.Name != "csd" {
		t.Errorf("expected name=csd, got %s", csd.Name)
	}
	if csd.RemoteAddress != "144.126.137.164" {
		t.Errorf("expected remote_address=144.126.137.164, got %s", csd.RemoteAddress)
	}
	if csd.Username != "ubuntu" {
		t.Errorf("expected username=ubuntu, got %s", csd.Username)
	}
	if csd.Multiplexing != "WezTerm" {
		t.Errorf("expected multiplexing=WezTerm, got %s", csd.Multiplexing)
	}

	// Check second domain (css)
	css := domains[1]
	if css.Name != "css" {
		t.Errorf("expected name=css, got %s", css.Name)
	}
	if css.RemoteAddress != "209.145.54.164" {
		t.Errorf("expected remote_address=209.145.54.164, got %s", css.RemoteAddress)
	}

	// Check third domain (trj)
	trj := domains[2]
	if trj.Name != "trj" {
		t.Errorf("expected name=trj, got %s", trj.Name)
	}
	if trj.RemoteAddress != "10.10.10.1" {
		t.Errorf("expected remote_address=10.10.10.1, got %s", trj.RemoteAddress)
	}
}

func TestExtractIdentityFile(t *testing.T) {
	domains := extractSSHDomains(sampleConfig)

	home, _ := os.UserHomeDir()

	// Check identity file expansion
	csd := domains[0]
	expectedPath := filepath.Join(home, ".ssh", "contabo_new_baremetal_sense_demo_box.pem")
	if csd.IdentityFile != expectedPath {
		t.Errorf("expected identityfile=%s, got %s", expectedPath, csd.IdentityFile)
	}
}

func TestExtractLuaString(t *testing.T) {
	tests := []struct {
		content  string
		key      string
		expected string
	}{
		{`name = 'test'`, "name", "test"},
		{`name = "test"`, "name", "test"},
		{`username = 'ubuntu'`, "username", "ubuntu"},
		{`remote_address = '192.168.1.1'`, "remote_address", "192.168.1.1"},
		{`multiplexing = 'WezTerm'`, "multiplexing", "WezTerm"},
		{`name = 'test', other = 'value'`, "name", "test"},
		{`no_match_here`, "name", ""},
	}

	for _, tt := range tests {
		got := extractLuaString(tt.content, tt.key)
		if got != tt.expected {
			t.Errorf("extractLuaString(%q, %q) = %q, want %q", tt.content, tt.key, got, tt.expected)
		}
	}
}

func TestExtractDomainEntries(t *testing.T) {
	block := `
  {
    name = 'first',
  },
  {
    name = 'second',
    nested = {
      key = 'value',
    },
  },
`
	entries := extractDomainEntries(block)

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestParseConfigFromFile(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "wezterm.lua")

	if err := os.WriteFile(configPath, []byte(sampleConfig), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := ParseConfig(configPath)
	if err != nil {
		t.Fatalf("ParseConfig failed: %v", err)
	}

	if len(cfg.SSHDomains) != 3 {
		t.Errorf("expected 3 domains, got %d", len(cfg.SSHDomains))
	}

	if cfg.Path != configPath {
		t.Errorf("expected path=%s, got %s", configPath, cfg.Path)
	}
}

func TestConfigGetDomainByName(t *testing.T) {
	cfg := &Config{
		SSHDomains: []SSHDomain{
			{Name: "csd", RemoteAddress: "1.1.1.1"},
			{Name: "css", RemoteAddress: "2.2.2.2"},
		},
	}

	domain := cfg.GetDomainByName("css")
	if domain == nil {
		t.Fatal("expected to find domain css")
	}
	if domain.RemoteAddress != "2.2.2.2" {
		t.Errorf("expected remote_address=2.2.2.2, got %s", domain.RemoteAddress)
	}

	notFound := cfg.GetDomainByName("nonexistent")
	if notFound != nil {
		t.Error("expected nil for nonexistent domain")
	}
}

func TestConfigGetMultiplexedDomains(t *testing.T) {
	cfg := &Config{
		SSHDomains: []SSHDomain{
			{Name: "csd", Multiplexing: "WezTerm"},
			{Name: "css", Multiplexing: "None"},
			{Name: "trj", Multiplexing: "WezTerm"},
		},
	}

	multiplexed := cfg.GetMultiplexedDomains()
	if len(multiplexed) != 2 {
		t.Errorf("expected 2 multiplexed domains, got %d", len(multiplexed))
	}
}

func TestEmptyConfig(t *testing.T) {
	config := `local wezterm = require 'wezterm'
local config = {}
return config
`
	domains := extractSSHDomains(config)
	if len(domains) != 0 {
		t.Errorf("expected 0 domains for empty config, got %d", len(domains))
	}
}

// Fixture-based tests for comprehensive coverage

func TestParseFixture_Basic(t *testing.T) {
	cfg, err := ParseConfig("testdata/basic.lua")
	if err != nil {
		t.Fatalf("failed to parse basic.lua: %v", err)
	}

	if len(cfg.SSHDomains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(cfg.SSHDomains))
	}

	// Verify first domain
	csd := cfg.GetDomainByName("csd")
	if csd == nil {
		t.Fatal("expected to find domain 'csd'")
	}
	if csd.RemoteAddress != "192.168.1.100" {
		t.Errorf("csd.RemoteAddress = %s, want 192.168.1.100", csd.RemoteAddress)
	}
	if csd.Username != "ubuntu" {
		t.Errorf("csd.Username = %s, want ubuntu", csd.Username)
	}
	if csd.Multiplexing != "WezTerm" {
		t.Errorf("csd.Multiplexing = %s, want WezTerm", csd.Multiplexing)
	}

	// Verify identity file expansion
	home, _ := os.UserHomeDir()
	expectedKey := filepath.Join(home, ".ssh", "csd.pem")
	if csd.IdentityFile != expectedKey {
		t.Errorf("csd.IdentityFile = %s, want %s", csd.IdentityFile, expectedKey)
	}
}

func TestParseFixture_MultipleDomains(t *testing.T) {
	cfg, err := ParseConfig("testdata/multiple_domains.lua")
	if err != nil {
		t.Fatalf("failed to parse multiple_domains.lua: %v", err)
	}

	if len(cfg.SSHDomains) != 5 {
		t.Fatalf("expected 5 domains, got %d", len(cfg.SSHDomains))
	}

	// Verify multiplexing filtering
	multiplexed := cfg.GetMultiplexedDomains()
	if len(multiplexed) != 3 {
		t.Errorf("expected 3 WezTerm-multiplexed domains, got %d", len(multiplexed))
	}

	// Verify hostname-based domain
	staging := cfg.GetDomainByName("staging")
	if staging == nil {
		t.Fatal("expected to find domain 'staging'")
	}
	if staging.RemoteAddress != "staging.example.com" {
		t.Errorf("staging.RemoteAddress = %s, want staging.example.com", staging.RemoteAddress)
	}

	// Verify tilde expansion
	dev := cfg.GetDomainByName("dev")
	if dev == nil {
		t.Fatal("expected to find domain 'dev'")
	}
	home, _ := os.UserHomeDir()
	expectedKey := filepath.Join(home, ".ssh", "dev_key")
	if dev.IdentityFile != expectedKey {
		t.Errorf("dev.IdentityFile = %s, want %s", dev.IdentityFile, expectedKey)
	}
}

func TestParseFixture_TailscaleIPs(t *testing.T) {
	cfg, err := ParseConfig("testdata/tailscale_ips.lua")
	if err != nil {
		t.Fatalf("failed to parse tailscale_ips.lua: %v", err)
	}

	if len(cfg.SSHDomains) != 3 {
		t.Fatalf("expected 3 domains, got %d", len(cfg.SSHDomains))
	}

	// Verify Tailscale IP
	ts1 := cfg.GetDomainByName("ts-server1")
	if ts1 == nil {
		t.Fatal("expected to find domain 'ts-server1'")
	}
	if ts1.RemoteAddress != "100.90.148.85" {
		t.Errorf("ts-server1.RemoteAddress = %s, want 100.90.148.85", ts1.RemoteAddress)
	}

	// Verify public IP
	pub := cfg.GetDomainByName("public-server")
	if pub == nil {
		t.Fatal("expected to find domain 'public-server'")
	}
	if pub.RemoteAddress != "203.0.113.50" {
		t.Errorf("public-server.RemoteAddress = %s, want 203.0.113.50", pub.RemoteAddress)
	}
}

func TestParseFixture_Minimal(t *testing.T) {
	cfg, err := ParseConfig("testdata/minimal.lua")
	if err != nil {
		t.Fatalf("failed to parse minimal.lua: %v", err)
	}

	if len(cfg.SSHDomains) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(cfg.SSHDomains))
	}

	minimal := cfg.SSHDomains[0]
	if minimal.Name != "minimal" {
		t.Errorf("minimal.Name = %s, want minimal", minimal.Name)
	}
	if minimal.RemoteAddress != "10.0.0.1" {
		t.Errorf("minimal.RemoteAddress = %s, want 10.0.0.1", minimal.RemoteAddress)
	}
	// Username should be empty (not specified)
	if minimal.Username != "" {
		t.Errorf("minimal.Username = %s, want empty", minimal.Username)
	}
	// Port should default to 22
	if minimal.Port != 22 {
		t.Errorf("minimal.Port = %d, want 22", minimal.Port)
	}
}

func TestParseFixture_NoSSHDomains(t *testing.T) {
	cfg, err := ParseConfig("testdata/no_ssh_domains.lua")
	if err != nil {
		t.Fatalf("failed to parse no_ssh_domains.lua: %v", err)
	}

	if len(cfg.SSHDomains) != 0 {
		t.Errorf("expected 0 domains, got %d", len(cfg.SSHDomains))
	}
}
