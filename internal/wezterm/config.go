// Package wezterm provides parsing of WezTerm Lua configuration files.
package wezterm

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SSHDomain represents an SSH domain from WezTerm config.
type SSHDomain struct {
	Name          string // Domain name (e.g., "csd", "css", "trj")
	RemoteAddress string // IP or hostname
	Username      string // SSH username
	Port          int    // SSH port (default 22)
	Multiplexing  string // "WezTerm", "None", etc.
	IdentityFile  string // Path to SSH key
}

// Config represents parsed WezTerm configuration.
type Config struct {
	SSHDomains []SSHDomain
	Path       string // Path to the config file
}

// FindConfigPath finds the WezTerm config file.
func FindConfigPath() string {
	// Check common locations
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	paths := []string{
		filepath.Join(home, ".config", "wezterm", "wezterm.lua"),
		filepath.Join(home, ".wezterm.lua"),
	}

	// On macOS, also check Application Support
	paths = append(paths, filepath.Join(home, "Library", "Application Support", "wezterm", "wezterm.lua"))

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// ParseConfig parses a WezTerm Lua config file.
func ParseConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Path: path,
	}

	cfg.SSHDomains = extractSSHDomains(string(data))
	return cfg, nil
}

// extractSSHDomains extracts ssh_domains from the Lua config.
func extractSSHDomains(content string) []SSHDomain {
	var domains []SSHDomain

	// Find the ssh_domains block
	// Pattern: ssh_domains = { ... }
	sshDomainsPattern := regexp.MustCompile(`(?s)ssh_domains\s*=\s*\{(.+?)\n\}`)
	match := sshDomainsPattern.FindStringSubmatch(content)
	if match == nil {
		return domains
	}

	domainsBlock := match[1]

	// Find individual domain entries: { name = '...', ... },
	// Use a simpler approach: find all { ... } blocks within the domains block
	domainEntries := extractDomainEntries(domainsBlock)

	for _, entry := range domainEntries {
		domain := parseSSHDomainEntry(entry)
		if domain.Name != "" {
			domains = append(domains, domain)
		}
	}

	return domains
}

// extractDomainEntries extracts individual domain entries from the ssh_domains block.
func extractDomainEntries(block string) []string {
	var entries []string
	depth := 0
	start := -1

	for i, ch := range block {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				entries = append(entries, block[start:i+1])
				start = -1
			}
		}
	}

	return entries
}

// parseSSHDomainEntry parses a single domain entry.
func parseSSHDomainEntry(entry string) SSHDomain {
	domain := SSHDomain{
		Port: 22, // default
	}

	// Extract name
	domain.Name = extractLuaString(entry, "name")

	// Extract remote_address
	domain.RemoteAddress = extractLuaString(entry, "remote_address")

	// Extract username
	domain.Username = extractLuaString(entry, "username")

	// Extract multiplexing
	domain.Multiplexing = extractLuaString(entry, "multiplexing")

	// Extract identity file from ssh_option block
	domain.IdentityFile = extractIdentityFile(entry)

	return domain
}

// extractLuaString extracts a string value for a key from Lua.
func extractLuaString(content, key string) string {
	// Pattern: key = 'value' or key = "value"
	pattern := regexp.MustCompile(key + `\s*=\s*['"]([^'"]+)['"]`)
	match := pattern.FindStringSubmatch(content)
	if match != nil {
		return match[1]
	}
	return ""
}

// extractIdentityFile extracts the identity file from ssh_option block.
func extractIdentityFile(entry string) string {
	// Look for identityfile within ssh_option block
	// Pattern: identityfile = wezterm.home_dir .. '/.ssh/keyname.pem'
	// or: identityfile = '/full/path/to/key'

	// First, try the wezterm.home_dir pattern
	homePattern := regexp.MustCompile(`identityfile\s*=\s*wezterm\.home_dir\s*\.\.\s*['"]([^'"]+)['"]`)
	match := homePattern.FindStringSubmatch(entry)
	if match != nil {
		// Expand home_dir
		home, _ := os.UserHomeDir()
		return filepath.Join(home, match[1])
	}

	// Try simple string pattern
	simplePattern := regexp.MustCompile(`identityfile\s*=\s*['"]([^'"]+)['"]`)
	match = simplePattern.FindStringSubmatch(entry)
	if match != nil {
		path := match[1]
		// Expand ~ if present
		if strings.HasPrefix(path, "~/") {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, path[2:])
		}
		return path
	}

	return ""
}

// GetDomainByName finds a domain by name.
func (c *Config) GetDomainByName(name string) *SSHDomain {
	for i := range c.SSHDomains {
		if c.SSHDomains[i].Name == name {
			return &c.SSHDomains[i]
		}
	}
	return nil
}

// GetMultiplexedDomains returns domains using WezTerm multiplexing.
func (c *Config) GetMultiplexedDomains() []SSHDomain {
	var domains []SSHDomain
	for _, d := range c.SSHDomains {
		if d.Multiplexing == "WezTerm" {
			domains = append(domains, d)
		}
	}
	return domains
}
