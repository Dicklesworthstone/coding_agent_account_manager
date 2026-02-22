// Package warnings provides CLI warnings for token expiry and other proactive alerts.
// This implements Alternative 1 from caam-jwgx.1: show warnings when users run commands
// instead of requiring external desktop notification dependencies.
package warnings

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Warning represents a proactive warning to show the user.
type Warning struct {
	Level   Level  // Warning severity
	Tool    string // Provider ID (claude, codex, gemini)
	Profile string // Profile name
	Message string // Human-readable message
	Action  string // Suggested action (e.g., "caam refresh claude work")
}

// Level represents warning severity.
type Level int

const (
	LevelInfo Level = iota
	LevelWarning
	LevelCritical
)

// String returns the level name.
func (l Level) String() string {
	switch l {
	case LevelInfo:
		return "info"
	case LevelWarning:
		return "warning"
	case LevelCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Checker checks for warnings across all profiles.
type Checker struct {
	vault    *authfile.Vault
	registry *provider.Registry
	profiles *profile.Store

	// Thresholds for warnings
	CriticalThreshold time.Duration // Default: 1 hour
	WarningThreshold  time.Duration // Default: 24 hours
}

// NewChecker creates a new warning checker.
func NewChecker(vault *authfile.Vault, registry *provider.Registry, profiles *profile.Store) *Checker {
	return &Checker{
		vault:             vault,
		registry:          registry,
		profiles:          profiles,
		CriticalThreshold: 1 * time.Hour,
		WarningThreshold:  24 * time.Hour,
	}
}

// CheckAll checks all vault profiles for token expiry warnings.
func (c *Checker) CheckAll(ctx context.Context) []Warning {
	var warnings []Warning

	// Check vault profiles (auth file swapping)
	for _, tool := range []string{"codex", "claude", "gemini"} {
		profiles, err := c.vault.List(tool)
		if err != nil {
			continue
		}

		for _, profileName := range profiles {
			if authfile.IsSystemProfile(profileName) {
				continue
			}
			w := c.checkVaultProfile(ctx, tool, profileName)
			warnings = append(warnings, w...)
		}
	}

	return warnings
}

// CheckActive checks only currently active profiles for warnings.
// This is faster than CheckAll and more relevant for immediate user action.
func (c *Checker) CheckActive(ctx context.Context) []Warning {
	var warnings []Warning

	tools := map[string]func() authfile.AuthFileSet{
		"codex":  authfile.CodexAuthFiles,
		"claude": authfile.ClaudeAuthFiles,
		"gemini": authfile.GeminiAuthFiles,
	}

	for tool, getFileSet := range tools {
		fileSet := getFileSet()

		// Skip if not logged in
		if !authfile.HasAuthFiles(fileSet) {
			continue
		}

		// Find active profile
		activeProfile, err := c.vault.ActiveProfile(fileSet)
		if err != nil || activeProfile == "" {
			continue
		}

		w := c.checkVaultProfile(ctx, tool, activeProfile)
		warnings = append(warnings, w...)
	}

	return warnings
}

// checkVaultProfile checks a single vault profile for expiry.
func (c *Checker) checkVaultProfile(ctx context.Context, tool, profileName string) []Warning {
	var warnings []Warning

	vaultPath := c.vault.ProfilePath(tool, profileName)

	// Parse expiry based on tool type
	var expInfo *health.ExpiryInfo
	var err error

	switch tool {
	case "claude":
		expInfo, err = health.ParseClaudeExpiry(vaultPath)
	case "codex":
		authPath := filepath.Join(vaultPath, "auth.json")
		expInfo, err = health.ParseCodexExpiry(authPath)
	case "gemini":
		// Migrate legacy vault filename before reading.
		_ = authfile.MigrateGeminiVaultDir(vaultPath)
		expInfo, err = health.ParseGeminiExpiry(vaultPath)
	}

	if err != nil || expInfo == nil || expInfo.ExpiresAt.IsZero() {
		return warnings
	}

	// Check expiry
	remaining := time.Until(expInfo.ExpiresAt)

	if remaining <= 0 {
		// Token expired
		warnings = append(warnings, Warning{
			Level:   LevelCritical,
			Tool:    tool,
			Profile: profileName,
			Message: "Token EXPIRED",
			Action:  fmt.Sprintf("caam login %s %s", tool, profileName),
		})
	} else if remaining <= c.CriticalThreshold {
		// Expires very soon
		warnings = append(warnings, Warning{
			Level:   LevelCritical,
			Tool:    tool,
			Profile: profileName,
			Message: fmt.Sprintf("Token expires in %s", formatDuration(remaining)),
			Action:  fmt.Sprintf("caam refresh %s %s", tool, profileName),
		})
	} else if remaining <= c.WarningThreshold {
		// Expires within warning threshold
		warnings = append(warnings, Warning{
			Level:   LevelWarning,
			Tool:    tool,
			Profile: profileName,
			Message: fmt.Sprintf("Token expires in %s", formatDuration(remaining)),
			Action:  fmt.Sprintf("caam refresh %s %s", tool, profileName),
		})
	}

	return warnings
}

// formatDuration formats a duration in a human-friendly way.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}

// Print prints warnings to the given writer.
// Returns true if any warnings were printed.
func Print(w io.Writer, warnings []Warning, noColor bool) bool {
	if len(warnings) == 0 {
		return false
	}

	for _, warn := range warnings {
		var prefix, colorStart, colorEnd string

		if noColor {
			colorStart = ""
			colorEnd = ""
		} else {
			colorEnd = "\033[0m"
			switch warn.Level {
			case LevelCritical:
				colorStart = "\033[31m" // Red
			case LevelWarning:
				colorStart = "\033[33m" // Yellow
			default:
				colorStart = "\033[36m" // Cyan
			}
		}

		switch warn.Level {
		case LevelCritical:
			prefix = "!!!"
		case LevelWarning:
			prefix = "!!!"
		default:
			prefix = "---"
		}

		fmt.Fprintf(w, "%s%s Warning: %s/%s: %s%s\n", colorStart, prefix, warn.Tool, warn.Profile, warn.Message, colorEnd)
		if warn.Action != "" {
			fmt.Fprintf(w, "    Run: %s\n", warn.Action)
		}
	}

	fmt.Fprintln(w) // Blank line after warnings
	return true
}

// PrintToStderr prints warnings to stderr.
// This is the typical usage for CLI commands - print warnings to stderr
// so they don't interfere with normal command output.
func PrintToStderr(warnings []Warning, noColor bool) bool {
	return Print(os.Stderr, warnings, noColor)
}

// Filter returns warnings matching the given level or higher.
func Filter(warnings []Warning, minLevel Level) []Warning {
	var filtered []Warning
	for _, w := range warnings {
		if w.Level >= minLevel {
			filtered = append(filtered, w)
		}
	}
	return filtered
}
