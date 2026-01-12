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
