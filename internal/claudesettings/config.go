package claudesettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SharedPaths returns the native user's canonical settings and session-state
// paths. An explicit CLAUDE_CONFIG_DIR remains authoritative when a file is
// absent; ignored legacy policy must not be resurrected as a fallback.
func SharedPaths(realHome string) (settingsPath, statePath string) {
	settingsPath = SharedPath(realHome)
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return settingsPath, filepath.Join(dir, ".claude.json")
	}
	return settingsPath, filepath.Join(realHome, ".claude.json")
}

// CAAMConfigPath is shared with config.ConfigPath so every activation entry
// point reads the same policy, including long-lived API/TUI/wrap processes.
func CAAMConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "caam", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "caam", "config.json")
	}
	return filepath.Join(home, ".config", "caam", "config.json")
}

// LoadPolicy reads only the Claude settings policy from caam's JSON config.
// Keeping this reader independent of the CLI and SPM configuration prevents
// callers from silently falling back to defaults or caching a stale policy.
func LoadPolicy() (Policy, error) {
	data, err := Read(CAAMConfigPath())
	if err != nil {
		return Policy{}, fmt.Errorf("read Claude settings policy: %w", err)
	}
	var cfg struct {
		ClaudeSettings Policy `json:"claude_settings"`
	}
	if data != nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Policy{}, fmt.Errorf("parse Claude settings policy: %w", err)
		}
	}
	if err := cfg.ClaudeSettings.Validate(); err != nil {
		return Policy{}, err
	}
	return cfg.ClaudeSettings, nil
}
