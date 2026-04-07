// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/gemini"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// CheckResult represents the result of a single diagnostic check.
type CheckResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass", "warn", "fail", "fixed"
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// DoctorReport contains all diagnostic check results.
type DoctorReport struct {
	Timestamp       string        `json:"timestamp"`
	OverallOK       bool          `json:"overall_ok"`
	PassCount       int           `json:"pass_count"`
	WarnCount       int           `json:"warn_count"`
	FailCount       int           `json:"fail_count"`
	FixedCount      int           `json:"fixed_count"`
	CLITools        []CheckResult `json:"cli_tools"`
	Dependencies    []CheckResult `json:"dependencies"`
	Directories     []CheckResult `json:"directories"`
	Config          []CheckResult `json:"config"`
	Profiles        []CheckResult `json:"profiles"`
	Locks           []CheckResult `json:"locks"`
	AuthFiles       []CheckResult `json:"auth_files"`
	TokenValidation []CheckResult `json:"token_validation,omitempty"`
}

// DependencySpec defines an optional external dependency with install hints.
type DependencySpec struct {
	// Name is the dependency name (for display).
	Name string

	// Binaries are the executable names to search for in PATH.
	Binaries []string

	// Description explains what this dependency enables.
	Description string

	// Required means caam won't work properly without it.
	Required bool

	// Feature describes which caam feature needs this dependency.
	Feature string

	// InstallLinux is the command to install on Linux.
	InstallLinux string

	// InstallMacOS is the command to install on macOS.
	InstallMacOS string

	// InstallWindows is the command to install on Windows.
	InstallWindows string

	// CustomCheck allows for special validation logic (e.g., checking playwright browsers).
	CustomCheck func() (bool, string)
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose setup issues and check dependencies",
	Long: `Runs diagnostic checks on your caam installation and reports any issues.

Checks performed:
  - CLI tools: Are codex, claude, gemini installed and in PATH?
  - Dependencies: Are optional tools (gum, wezterm, tailscale, playwright, etc.) available?
  - Data directories: Do vault/profiles directories exist with correct permissions?
  - Config: Is the configuration valid?
  - Profiles: Are all isolated profiles valid? Any broken symlinks?
  - Locks: Are there any stale lock files from crashed processes?
  - Auth files: Do auth files exist for each provider?
  - Token validation (with --validate): Are auth tokens actually valid?

Flags:
  --fix       Attempt to fix issues (create directories, clean stale locks)
  --json      Output results in JSON format for scripting
  --validate  Validate that auth tokens actually work (passive check, no API calls)
  --auto      Automatically install missing optional dependencies (prompts for confirmation unless --yes)
  --yes       Skip confirmation prompts when using --auto`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fix, _ := cmd.Flags().GetBool("fix")
		jsonOutput, _ := cmd.Flags().GetBool("json")
		validate, _ := cmd.Flags().GetBool("validate")
		autoInstall, _ := cmd.Flags().GetBool("auto")
		skipConfirm, _ := cmd.Flags().GetBool("yes")

		report := runDoctorChecks(fix, validate, autoInstall, skipConfirm)

		if jsonOutput {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		printDoctorReport(report, validate)

		if !report.OverallOK {
			return fmt.Errorf("found %d issues (%d warnings, %d failures)",
				report.WarnCount+report.FailCount, report.WarnCount, report.FailCount)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().Bool("fix", false, "attempt to fix issues")
	doctorCmd.Flags().Bool("json", false, "output in JSON format")
	doctorCmd.Flags().Bool("validate", false, "validate that auth tokens actually work")
	doctorCmd.Flags().Bool("auto", false, "automatically install missing optional dependencies")
	doctorCmd.Flags().BoolP("yes", "y", false, "skip confirmation prompts when using --auto")
}

func runDoctorChecks(fix bool, validate bool, autoInstall bool, skipConfirm bool) *DoctorReport {
	report := &DoctorReport{
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Check CLI tools
	report.CLITools = checkCLITools()

	// Check external dependencies
	report.Dependencies = checkDependencies(autoInstall, skipConfirm)

	// Check directories
	report.Directories = checkDirectories(fix)

	// Check config
	report.Config = checkConfig()

	// Check profiles
	report.Profiles = checkProfiles(fix)

	// Check locks
	report.Locks = checkLocks(fix)

	// Check auth files
	report.AuthFiles = checkAuthFiles()

	// Check token validation (if requested)
	if validate {
		report.TokenValidation = checkTokenValidation()
	}

	// Calculate totals
	allChecks := append(report.CLITools, report.Dependencies...)
	allChecks = append(allChecks, report.Directories...)
	allChecks = append(allChecks, report.Config...)
	allChecks = append(allChecks, report.Profiles...)
	allChecks = append(allChecks, report.Locks...)
	allChecks = append(allChecks, report.AuthFiles...)
	allChecks = append(allChecks, report.TokenValidation...)

	for _, check := range allChecks {
		switch check.Status {
		case "pass":
			report.PassCount++
		case "warn":
			report.WarnCount++
		case "fail":
			report.FailCount++
		case "fixed":
			report.FixedCount++
			report.PassCount++
		}
	}

	report.OverallOK = report.FailCount == 0

	return report
}

func checkCLITools() []CheckResult {
	var results []CheckResult

	toolBinaries := map[string][]string{
		"codex":  {"codex"},
		"claude": {"claude"},
		"gemini": {"gemini"},
	}

	for tool, binaries := range toolBinaries {
		found := false
		var foundPath string

		for _, bin := range binaries {
			path, err := osexec.LookPath(bin)
			if err == nil {
				found = true
				foundPath = path
				break
			}
		}

		if found {
			results = append(results, CheckResult{
				Name:    tool,
				Status:  "pass",
				Message: fmt.Sprintf("found at %s", foundPath),
			})
		} else {
			results = append(results, CheckResult{
				Name:    tool,
				Status:  "warn",
				Message: "not found in PATH",
				Details: fmt.Sprintf("Install %s to use caam with this tool", tool),
			})
		}
	}

	return results
}

// getDependencySpecs returns the list of optional dependencies to check.
func getDependencySpecs() []DependencySpec {
	return []DependencySpec{
		{
			Name:           "gum",
			Binaries:       []string{"gum"},
			Description:    "Charm's tool for glamorous shell scripts (enhanced TUI prompts)",
			Required:       false,
			Feature:        "enhanced interactive prompts",
			InstallLinux:   "sudo mkdir -p /etc/apt/keyrings && curl -fsSL https://repo.charm.sh/apt/gpg.key | sudo gpg --dearmor -o /etc/apt/keyrings/charm.gpg && echo 'deb [signed-by=/etc/apt/keyrings/charm.gpg] https://repo.charm.sh/apt/ * *' | sudo tee /etc/apt/sources.list.d/charm.list && sudo apt update && sudo apt install gum",
			InstallMacOS:   "brew install gum",
			InstallWindows: "scoop install charm-gum",
		},
		{
			Name:           "wezterm",
			Binaries:       []string{"wezterm"},
			Description:    "WezTerm terminal emulator CLI for session management",
			Required:       false,
			Feature:        "session recovery and batch login automation (caam wezterm recover)",
			InstallLinux:   "# See https://wezfurlong.org/wezterm/install/linux.html",
			InstallMacOS:   "brew install --cask wezterm",
			InstallWindows: "scoop install wezterm",
		},
		{
			Name:           "tailscale",
			Binaries:       []string{"tailscale"},
			Description:    "Mesh VPN for secure distributed setup",
			Required:       false,
			Feature:        "distributed auth coordination across machines",
			InstallLinux:   "curl -fsSL https://tailscale.com/install.sh | sh",
			InstallMacOS:   "brew install tailscale",
			InstallWindows: "# Download from https://tailscale.com/download",
		},
		{
			Name:           "node",
			Binaries:       []string{"node", "nodejs"},
			Description:    "Node.js runtime (required for Playwright)",
			Required:       false,
			Feature:        "Playwright browser automation",
			InstallLinux:   "curl -fsSL https://deb.nodesource.com/setup_lts.x | sudo -E bash - && sudo apt-get install -y nodejs",
			InstallMacOS:   "brew install node",
			InstallWindows: "scoop install nodejs-lts",
		},
		{
			Name:           "playwright",
			Binaries:       []string{"playwright", "npx"},
			Description:    "Browser automation framework for OAuth flows",
			Required:       false,
			Feature:        "automated browser-based OAuth login",
			InstallLinux:   "npm install -g playwright && npx playwright install chromium",
			InstallMacOS:   "npm install -g playwright && npx playwright install chromium",
			InstallWindows: "npm install -g playwright && npx playwright install chromium",
			CustomCheck:    checkPlaywright,
		},
		{
			Name:           "jq",
			Binaries:       []string{"jq"},
			Description:    "Command-line JSON processor",
			Required:       false,
			Feature:        "JSON output processing in scripts",
			InstallLinux:   "sudo apt install jq",
			InstallMacOS:   "brew install jq",
			InstallWindows: "scoop install jq",
		},
		{
			Name:           "gh",
			Binaries:       []string{"gh"},
			Description:    "GitHub CLI for repository operations",
			Required:       false,
			Feature:        "GitHub integration",
			InstallLinux:   "sudo apt install gh",
			InstallMacOS:   "brew install gh",
			InstallWindows: "scoop install gh",
		},
		{
			Name:           "ssh",
			Binaries:       []string{"ssh"},
			Description:    "Secure Shell client",
			Required:       false,
			Feature:        "remote profile execution",
			InstallLinux:   "sudo apt install openssh-client",
			InstallMacOS:   "# Pre-installed on macOS",
			InstallWindows: "# Use Windows OpenSSH or Git Bash",
		},
		{
			Name:           "chrome",
			Binaries:       []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"},
			Description:    "Chrome or Chromium browser for OAuth flows",
			Required:       false,
			Feature:        "browser-based OAuth login",
			InstallLinux:   "# Download from https://www.google.com/chrome/ or: sudo apt install chromium-browser",
			InstallMacOS:   "brew install --cask google-chrome",
			InstallWindows: "# Download from https://www.google.com/chrome/",
		},
	}
}

// checkPlaywright performs a custom check for Playwright installation including browser availability.
func checkPlaywright() (bool, string) {
	// First check if npx is available
	if _, err := osexec.LookPath("npx"); err != nil {
		return false, "npx not found (install Node.js first)"
	}

	// Check if playwright can show version
	cmd := osexec.Command("npx", "playwright", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, "playwright not installed (run: npm install -g playwright)"
	}

	version := strings.TrimSpace(string(output))

	// Check if Chromium browser is installed
	cmd = osexec.Command("npx", "playwright", "install", "--dry-run", "chromium")
	if err := cmd.Run(); err != nil {
		return true, fmt.Sprintf("v%s (chromium browser not installed, run: npx playwright install chromium)", version)
	}

	return true, fmt.Sprintf("v%s with chromium", version)
}

// checkDependencies verifies optional external dependencies are available.
func checkDependencies(autoInstall bool, skipConfirm bool) []CheckResult {
	var results []CheckResult
	specs := getDependencySpecs()

	for _, spec := range specs {
		result := checkSingleDependency(spec, autoInstall, skipConfirm)
		results = append(results, result)
	}

	return results
}

// checkSingleDependency checks if a single dependency is available.
func checkSingleDependency(spec DependencySpec, autoInstall bool, skipConfirm bool) CheckResult {
	// First, if there's a custom check, use it
	if spec.CustomCheck != nil {
		ok, details := spec.CustomCheck()
		if ok {
			return CheckResult{
				Name:    spec.Name,
				Status:  "pass",
				Message: details,
			}
		}
		// Custom check failed, but might have partial info in details
		return CheckResult{
			Name:    spec.Name,
			Status:  "warn",
			Message: "partially installed",
			Details: fmt.Sprintf("%s. Feature: %s", details, spec.Feature),
		}
	}

	// Standard binary lookup
	var foundPath string
	for _, bin := range spec.Binaries {
		path, err := osexec.LookPath(bin)
		if err == nil {
			foundPath = path
			break
		}
	}

	if foundPath != "" {
		// Get version if possible
		version := getToolVersion(spec.Name, foundPath)
		msg := fmt.Sprintf("found at %s", foundPath)
		if version != "" {
			msg = fmt.Sprintf("%s (%s)", version, foundPath)
		}
		return CheckResult{
			Name:    spec.Name,
			Status:  "pass",
			Message: msg,
		}
	}

	// Not found - determine install command based on OS
	installCmd := getInstallCommand(spec)
	status := "warn"
	if spec.Required {
		status = "fail"
	}

	details := fmt.Sprintf("Feature: %s\nInstall: %s", spec.Feature, installCmd)

	// If autoInstall is enabled, try to install
	if autoInstall && installCmd != "" && !strings.HasPrefix(installCmd, "#") {
		if !skipConfirm {
			// In non-interactive mode we would prompt, but for now just show what would be installed
			details = fmt.Sprintf("Feature: %s\nWould install: %s\n(Use --yes to skip confirmation)", spec.Feature, installCmd)
		} else {
			// Actually try to install
			installResult := tryInstallDependency(spec.Name, installCmd)
			if installResult.success {
				return CheckResult{
					Name:    spec.Name,
					Status:  "fixed",
					Message: "installed successfully",
					Details: installResult.output,
				}
			}
			details = fmt.Sprintf("Installation failed: %s\nCommand: %s", installResult.output, installCmd)
		}
	}

	return CheckResult{
		Name:    spec.Name,
		Status:  status,
		Message: "not found in PATH",
		Details: details,
	}
}

// getInstallCommand returns the OS-appropriate install command for a dependency.
func getInstallCommand(spec DependencySpec) string {
	switch runtime.GOOS {
	case "linux":
		return spec.InstallLinux
	case "darwin":
		return spec.InstallMacOS
	case "windows":
		return spec.InstallWindows
	default:
		return spec.InstallLinux // Default to Linux
	}
}

// getToolVersion attempts to get the version of an installed tool.
func getToolVersion(name, path string) string {
	var cmd *osexec.Cmd

	switch name {
	case "gum":
		cmd = osexec.Command(path, "--version")
	case "wezterm":
		cmd = osexec.Command(path, "--version")
	case "tailscale":
		cmd = osexec.Command(path, "version")
	case "node":
		cmd = osexec.Command(path, "--version")
	case "jq":
		cmd = osexec.Command(path, "--version")
	case "gh":
		cmd = osexec.Command(path, "--version")
	case "ssh":
		cmd = osexec.Command(path, "-V")
	case "chrome":
		cmd = osexec.Command(path, "--version")
	default:
		return ""
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	version := strings.TrimSpace(string(output))
	// Take just the first line if multi-line
	if idx := strings.Index(version, "\n"); idx > 0 {
		version = version[:idx]
	}
	// Limit length
	if len(version) > 50 {
		version = version[:50] + "..."
	}
	return version
}

// installResult holds the result of an installation attempt.
type installResult struct {
	success bool
	output  string
}

// tryInstallDependency attempts to install a dependency using the provided command.
func tryInstallDependency(name, installCmd string) installResult {
	// For safety, we only execute certain well-known package manager commands
	// This prevents arbitrary command execution
	allowedPrefixes := []string{
		"brew install",
		"brew install --cask",
		"sudo apt install",
		"sudo apt-get install",
		"scoop install",
		"npm install",
		"npx playwright install",
	}

	isAllowed := false
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(installCmd, prefix) {
			isAllowed = true
			break
		}
	}

	if !isAllowed {
		return installResult{
			success: false,
			output:  "complex install command - please run manually",
		}
	}

	// Parse the command
	parts := strings.Fields(installCmd)
	if len(parts) < 2 {
		return installResult{
			success: false,
			output:  "invalid install command",
		}
	}

	fmt.Printf("  Installing %s: %s\n", name, installCmd)

	cmd := osexec.Command(parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		return installResult{
			success: false,
			output:  err.Error(),
		}
	}

	return installResult{
		success: true,
		output:  "installation complete",
	}
}

func checkDirectories(fix bool) []CheckResult {
	var results []CheckResult

	dataDir := config.DefaultDataPath()

	dirs := []struct {
		path string
		name string
	}{
		{dataDir, "caam data directory"},
		{filepath.Join(dataDir, "vault"), "vault directory"},
		{filepath.Join(dataDir, "profiles"), "profiles directory"},
	}

	for _, dir := range dirs {
		info, err := os.Stat(dir.path)
		if os.IsNotExist(err) {
			if fix {
				if err := os.MkdirAll(dir.path, 0700); err != nil {
					results = append(results, CheckResult{
						Name:    dir.name,
						Status:  "fail",
						Message: "missing and could not create",
						Details: err.Error(),
					})
				} else {
					results = append(results, CheckResult{
						Name:    dir.name,
						Status:  "fixed",
						Message: fmt.Sprintf("created %s", dir.path),
					})
				}
			} else {
				results = append(results, CheckResult{
					Name:    dir.name,
					Status:  "warn",
					Message: fmt.Sprintf("missing: %s", dir.path),
					Details: "Run with --fix to create",
				})
			}
		} else if err != nil {
			results = append(results, CheckResult{
				Name:    dir.name,
				Status:  "fail",
				Message: fmt.Sprintf("error checking: %s", dir.path),
				Details: err.Error(),
			})
		} else if !info.IsDir() {
			results = append(results, CheckResult{
				Name:    dir.name,
				Status:  "fail",
				Message: fmt.Sprintf("exists but is not a directory: %s", dir.path),
			})
		} else {
			// Check permissions
			mode := info.Mode().Perm()
			if mode&0077 != 0 {
				results = append(results, CheckResult{
					Name:    dir.name,
					Status:  "warn",
					Message: fmt.Sprintf("permissions too open: %s (mode %04o)", dir.path, mode),
					Details: "Consider running: chmod 700 " + dir.path,
				})
			} else {
				results = append(results, CheckResult{
					Name:    dir.name,
					Status:  "pass",
					Message: fmt.Sprintf("exists (mode %04o)", mode),
				})
			}
		}
	}

	return results
}

func checkConfig() []CheckResult {
	var results []CheckResult

	homeDir, _ := os.UserHomeDir()
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(homeDir, ".config")
	}

	configPath := filepath.Join(xdgConfig, "caam", "config.json")

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		results = append(results, CheckResult{
			Name:    "config.json",
			Status:  "pass",
			Message: "not present (using defaults)",
			Details: "Optional: create " + configPath,
		})
	} else if err != nil {
		results = append(results, CheckResult{
			Name:    "config.json",
			Status:  "fail",
			Message: "error checking config",
			Details: err.Error(),
		})
	} else {
		// Try to load config
		_, err := config.Load()
		if err != nil {
			results = append(results, CheckResult{
				Name:    "config.json",
				Status:  "fail",
				Message: "invalid configuration",
				Details: err.Error(),
			})
		} else {
			results = append(results, CheckResult{
				Name:    "config.json",
				Status:  "pass",
				Message: "valid",
			})
		}
	}

	return results
}

func checkProfiles(fix bool) []CheckResult {
	var results []CheckResult

	// Guard against nil profileStore (e.g., in tests)
	if profileStore == nil {
		results = append(results, CheckResult{
			Name:    "profiles",
			Status:  "warn",
			Message: "profile store not initialized",
		})
		return results
	}

	allProfiles, err := profileStore.ListAll()
	if err != nil {
		results = append(results, CheckResult{
			Name:    "profiles",
			Status:  "fail",
			Message: "error listing profiles",
			Details: err.Error(),
		})
		return results
	}

	if len(allProfiles) == 0 {
		results = append(results, CheckResult{
			Name:    "profiles",
			Status:  "pass",
			Message: "no isolated profiles configured",
		})
		return results
	}

	for provider, profiles := range allProfiles {
		for _, prof := range profiles {
			// Check if home directory exists
			homePath := prof.HomePath()
			if _, err := os.Stat(homePath); os.IsNotExist(err) {
				results = append(results, CheckResult{
					Name:    fmt.Sprintf("%s/%s", provider, prof.Name),
					Status:  "warn",
					Message: "missing home directory",
					Details: homePath,
				})
			} else if err != nil {
				results = append(results, CheckResult{
					Name:    fmt.Sprintf("%s/%s", provider, prof.Name),
					Status:  "fail",
					Message: "error checking home directory",
					Details: err.Error(),
				})
			} else {
				// Check for broken symlinks in home
				brokenLinks := checkBrokenSymlinks(homePath)
				if len(brokenLinks) > 0 {
					results = append(results, CheckResult{
						Name:    fmt.Sprintf("%s/%s", provider, prof.Name),
						Status:  "warn",
						Message: fmt.Sprintf("%d broken symlink(s)", len(brokenLinks)),
						Details: strings.Join(brokenLinks, ", "),
					})
				} else {
					results = append(results, CheckResult{
						Name:    fmt.Sprintf("%s/%s", provider, prof.Name),
						Status:  "pass",
						Message: "OK",
					})
				}
			}
		}
	}

	return results
}

func checkBrokenSymlinks(dir string) []string {
	var broken []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return broken
	}

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			// It's a symlink - check if target exists
			target, err := os.Readlink(path)
			if err != nil {
				broken = append(broken, entry.Name())
				continue
			}

			// Resolve relative to symlink location
			if !filepath.IsAbs(target) {
				target = filepath.Join(dir, target)
			}

			if _, err := os.Stat(target); os.IsNotExist(err) {
				broken = append(broken, entry.Name())
			}
		}
	}

	return broken
}

func checkLocks(fix bool) []CheckResult {
	var results []CheckResult

	// Guard against nil profileStore (e.g., in tests)
	if profileStore == nil {
		return results
	}

	allProfiles, err := profileStore.ListAll()
	if err != nil {
		return results
	}

	for provider, profiles := range allProfiles {
		for _, prof := range profiles {
			if !prof.IsLocked() {
				continue
			}

			info, err := prof.GetLockInfo()
			if err != nil {
				results = append(results, CheckResult{
					Name:    fmt.Sprintf("%s/%s lock", provider, prof.Name),
					Status:  "warn",
					Message: "could not read lock file",
					Details: err.Error(),
				})
				continue
			}

			if info == nil {
				// Lock file exists but couldn't parse it (corrupt or empty)
				results = append(results, CheckResult{
					Name:    fmt.Sprintf("%s/%s lock", provider, prof.Name),
					Status:  "warn",
					Message: "lock file exists but is empty or corrupt",
					Details: "Run with --fix to remove",
				})
				if fix {
					if err := prof.Unlock(); err == nil {
						results[len(results)-1].Status = "fixed"
						results[len(results)-1].Message = "removed corrupt lock file"
					}
				}
			} else if !profile.IsProcessAlive(info.PID) {
				// Stale lock
				if fix {
					if err := prof.Unlock(); err != nil {
						results = append(results, CheckResult{
							Name:    fmt.Sprintf("%s/%s lock", provider, prof.Name),
							Status:  "fail",
							Message: fmt.Sprintf("stale lock (PID %d) - could not remove", info.PID),
							Details: err.Error(),
						})
					} else {
						results = append(results, CheckResult{
							Name:    fmt.Sprintf("%s/%s lock", provider, prof.Name),
							Status:  "fixed",
							Message: fmt.Sprintf("removed stale lock (PID %d not running)", info.PID),
						})
					}
				} else {
					results = append(results, CheckResult{
						Name:    fmt.Sprintf("%s/%s lock", provider, prof.Name),
						Status:  "warn",
						Message: fmt.Sprintf("stale lock (PID %d not running)", info.PID),
						Details: "Run with --fix to remove",
					})
				}
			} else {
				results = append(results, CheckResult{
					Name:    fmt.Sprintf("%s/%s lock", provider, prof.Name),
					Status:  "pass",
					Message: fmt.Sprintf("active (PID %d)", info.PID),
				})
			}
		}
	}

	if len(results) == 0 {
		results = append(results, CheckResult{
			Name:    "locks",
			Status:  "pass",
			Message: "no lock files found",
		})
	}

	return results
}

func checkAuthFiles() []CheckResult {
	var results []CheckResult

	// Guard against nil vault (e.g., in tests)
	if vault == nil {
		results = append(results, CheckResult{
			Name:    "auth files",
			Status:  "warn",
			Message: "vault not initialized",
		})
		return results
	}

	for tool, getFileSet := range tools {
		fileSet := getFileSet()
		hasAuth := authfile.HasAuthFiles(fileSet)

		if hasAuth {
			// Check which profile is active
			activeProfile, _ := vault.ActiveProfile(fileSet)
			msg := "logged in"
			if activeProfile != "" {
				msg = fmt.Sprintf("logged in (profile: %s)", activeProfile)
			}
			results = append(results, CheckResult{
				Name:    tool,
				Status:  "pass",
				Message: msg,
			})
		} else {
			results = append(results, CheckResult{
				Name:    tool,
				Status:  "warn",
				Message: "no auth files",
				Details: "Login with the tool first, then use 'caam backup' to save",
			})
		}

		// Check vault profile token expiry for all non-system profiles.
		// This catches profiles that have drifted into expired/unusable state
		// (e.g., refresh_token_reused) which the basic auth file check misses.
		vaultProfiles, err := vault.List(tool)
		if err == nil {
			for _, profileName := range vaultProfiles {
				if authfile.IsSystemProfile(profileName) {
					continue
				}
				ph := buildProfileHealth(tool, profileName)
				name := fmt.Sprintf("%s/%s token", tool, profileName)
				if ph == nil || ph.TokenExpiresAt.IsZero() {
					// Cannot determine expiry: probe the token with a live API
					// call to detect broken/reused tokens that look valid on disk.
					if result := probeVaultToken(tool, profileName); result != nil {
						results = append(results, *result)
					}
					continue
				}
				if ph.TokenExpiresAt.Before(time.Now()) {
					results = append(results, CheckResult{
						Name:    name,
						Status:  "fail",
						Message: "token expired",
						Details: fmt.Sprintf("Expired at %s; re-login with 'caam login %s %s'", ph.TokenExpiresAt.Format(time.RFC3339), tool, profileName),
					})
				} else if time.Until(ph.TokenExpiresAt) < 15*time.Minute {
					results = append(results, CheckResult{
						Name:    name,
						Status:  "warn",
						Message: fmt.Sprintf("token expiring soon (%s remaining)", formatExpiryDuration(ph.TokenExpiresAt)),
						Details: fmt.Sprintf("Consider refreshing: 'caam refresh %s %s'", tool, profileName),
					})
				} else {
					// Token not expired and not expiring soon -- still probe Codex
					// tokens with a live API call to catch refresh_token_reused state
					// where the access token appears valid by expiry but the refresh
					// token has been consumed, making the profile a ticking time bomb.
					if tool == "codex" {
						if result := probeVaultToken(tool, profileName); result != nil {
							results = append(results, *result)
						}
					}
				}
			}
		}

		// Check for duplicate identities between named and system/backup profiles.
		// This catches the confusing scenario where a backup profile shares the
		// same underlying account as a named profile, which can cause the active
		// profile to unexpectedly flip to the backup after re-authentication.
		results = append(results, checkDuplicateIdentities(tool)...)
	}

	return results
}

// normalizeIdentity extracts the primary identifier from an identity string
// and lowercases it for case-insensitive comparison.
// identityFromJWT returns "email|accountID|org" — this extracts the email,
// falling back to accountID if email is empty.
// Plain email strings are returned lowercased as-is.
func normalizeIdentity(id string) string {
	if id == "" {
		return ""
	}
	// identityFromJWT returns "email|accountID|org" — extract the best identifier
	if strings.Contains(id, "|") {
		parts := strings.SplitN(id, "|", 3)
		// Prefer email (parts[0]), fall back to accountID (parts[1])
		if parts[0] != "" {
			return strings.ToLower(parts[0])
		}
		if len(parts) > 1 && parts[1] != "" {
			return strings.ToLower(parts[1])
		}
		return ""
	}
	return strings.ToLower(id)
}

// checkDuplicateIdentities detects when named and system/backup profiles share
// the same identity subject (email/account). This is not inherently broken but
// causes confusion: after re-auth, the active profile may flip to the backup
// instead of the named profile the user expects.
func checkDuplicateIdentities(tool string) []CheckResult {
	var results []CheckResult

	profiles, err := vault.List(tool)
	if err != nil || len(profiles) < 2 {
		return results
	}

	// Extract identity for each profile, grouping by identity string.
	// identityGroups maps identity -> list of profile names with that identity.
	type profileInfo struct {
		name     string
		isSystem bool
	}
	identityGroups := make(map[string][]profileInfo)

	for _, prof := range profiles {
		identity := normalizeIdentity(vault.ProfileIdentity(tool, prof))
		if identity == "" {
			continue // Cannot determine identity, skip
		}
		identityGroups[identity] = append(identityGroups[identity], profileInfo{
			name:     prof,
			isSystem: authfile.IsSystemProfile(prof),
		})
	}

	// For each identity group that has both named and system profiles, emit a warning.
	for identity, group := range identityGroups {
		if len(group) < 2 {
			continue
		}

		var named, system []string
		for _, p := range group {
			if p.isSystem {
				system = append(system, p.name)
			} else {
				named = append(named, p.name)
			}
		}

		if len(named) == 0 || len(system) == 0 {
			continue // All named or all system -- not the problematic case
		}

		// Identity is already normalized (email only, lowercased).
		identityLabel := identity

		for _, sysProf := range system {
			results = append(results, CheckResult{
				Name:   fmt.Sprintf("%s/%s identity", tool, sysProf),
				Status: "warn",
				Message: fmt.Sprintf("shares identity with named profile(s): %s",
					strings.Join(named, ", ")),
				Details: fmt.Sprintf(
					"Identity: %s\n"+
						"Backup profile %q has the same account as %s.\n"+
						"This can cause the active profile to flip to the backup after re-auth.\n"+
						"Consider removing the backup: 'caam delete --force %s %s'",
					identityLabel, sysProf, strings.Join(named, ", "), tool, sysProf),
			})
		}
	}

	return results
}

// probeVaultToken performs a lightweight live API call to verify that a vault
// profile's access token actually works. This catches the scenario from issue #14
// where doctor reports "OK" based on JWT expiry alone, but the token is actually
// unusable because the refresh token has been consumed (refresh_token_reused).
//
// Currently only supports Codex profiles (GET /v1/me).
// Returns nil if the probe succeeds or the tool is not supported for probing.
func probeVaultToken(tool, profileName string) *CheckResult {
	if tool != "codex" {
		return nil // Only Codex has a lightweight verification endpoint
	}

	vaultPath := vault.ProfilePath(tool, profileName)
	authPath := filepath.Join(vaultPath, "auth.json")

	data, err := os.ReadFile(authPath)
	if err != nil {
		return nil // Can't read auth file; other checks will catch this
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil
	}

	// Extract access token (try nested tokens first, then root)
	accessToken := ""
	if tokens, ok := auth["tokens"].(map[string]interface{}); ok {
		if t, ok := tokens["access_token"].(string); ok {
			accessToken = t
		}
	}
	if accessToken == "" {
		if t, ok := auth["access_token"].(string); ok {
			accessToken = t
		}
	}
	if accessToken == "" {
		return nil // No access token to probe
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := refresh.VerifyCodexToken(ctx, accessToken); err != nil {
		name := fmt.Sprintf("%s/%s token", tool, profileName)
		errMsg := err.Error()
		// Distinguish network/timeout errors from actual auth rejections.
		// Network errors should be warnings (tokens may be fine), not failures.
		if strings.Contains(errMsg, "request failed:") || strings.Contains(errMsg, "context deadline exceeded") {
			return &CheckResult{
				Name:    name,
				Status:  "warn",
				Message: "could not verify token (network error)",
				Details: fmt.Sprintf(
					"Live API probe for %s/%s failed with: %s\n"+
						"This may be a transient network issue. The token may still be valid.",
					tool, profileName, errMsg),
			}
		}
		return &CheckResult{
			Name:   name,
			Status: "fail",
			Message: "access token rejected by API",
			Details: fmt.Sprintf(
				"The stored access token for %s/%s was rejected by the Codex API.\n"+
					"This usually means the refresh token was consumed by another process\n"+
					"(refresh_token_reused). Re-authenticate with: 'caam login %s %s'",
				tool, profileName, tool, profileName),
		}
	}

	return nil // Token is valid
}

// checkTokenValidation validates auth tokens for all profiles.
// This performs passive validation (no API calls) by checking token format and expiry.
func checkTokenValidation() []CheckResult {
	var results []CheckResult
	ctx := context.Background()

	// Guard against nil profileStore (e.g., in tests)
	if profileStore == nil {
		results = append(results, CheckResult{
			Name:    "token validation",
			Status:  "warn",
			Message: "profile store not initialized",
		})
		return results
	}

	// Build provider registry for validation
	reg := provider.NewRegistry()
	reg.Register(claude.New())
	reg.Register(codex.New())
	reg.Register(gemini.New())

	// Get all profiles and validate tokens
	allProfiles, err := profileStore.ListAll()
	if err != nil {
		results = append(results, CheckResult{
			Name:    "token validation",
			Status:  "warn",
			Message: "could not list profiles",
			Details: err.Error(),
		})
		return results
	}

	if len(allProfiles) == 0 {
		results = append(results, CheckResult{
			Name:    "token validation",
			Status:  "pass",
			Message: "no profiles to validate",
		})
		return results
	}

	for providerID, profiles := range allProfiles {
		prov, ok := reg.Get(providerID)
		if !ok {
			continue
		}

		for _, prof := range profiles {
			name := fmt.Sprintf("%s/%s", providerID, prof.Name)

			// Perform passive validation (no API calls)
			result, err := prov.ValidateToken(ctx, prof, true)
			if err != nil {
				results = append(results, CheckResult{
					Name:    name,
					Status:  "warn",
					Message: "validation error",
					Details: err.Error(),
				})
				continue
			}

			if result.Valid {
				msg := "valid"
				if !result.ExpiresAt.IsZero() {
					msg = fmt.Sprintf("valid (expires %s)", formatExpiryDuration(result.ExpiresAt))
				}
				results = append(results, CheckResult{
					Name:    name,
					Status:  "pass",
					Message: msg,
				})
			} else {
				results = append(results, CheckResult{
					Name:    name,
					Status:  "fail",
					Message: "invalid token",
					Details: result.Error,
				})
			}
		}
	}

	return results
}

// formatExpiryDuration formats an expiry time relative to now.
func formatExpiryDuration(t time.Time) string {
	now := time.Now()
	diff := t.Sub(now)

	if diff < 0 {
		return "expired"
	}

	if diff < time.Hour {
		return fmt.Sprintf("in %d minutes", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("in %d hours", int(diff.Hours()))
	}
	return fmt.Sprintf("in %d days", int(diff.Hours()/24))
}

func printDoctorReport(report *DoctorReport, validate bool) {
	fmt.Println("caam doctor")
	fmt.Println()

	// CLI Tools
	fmt.Println("Checking CLI tools...")
	for _, check := range report.CLITools {
		printCheck(check)
	}
	fmt.Println()

	// Dependencies
	if len(report.Dependencies) > 0 {
		fmt.Println("Checking optional dependencies...")
		for _, check := range report.Dependencies {
			printCheck(check)
		}
		fmt.Println()
	}

	// Directories
	fmt.Println("Checking data directories...")
	for _, check := range report.Directories {
		printCheck(check)
	}
	fmt.Println()

	// Config
	fmt.Println("Checking configuration...")
	for _, check := range report.Config {
		printCheck(check)
	}
	fmt.Println()

	// Profiles
	fmt.Println("Checking isolated profiles...")
	for _, check := range report.Profiles {
		printCheck(check)
	}
	fmt.Println()

	// Locks
	fmt.Println("Checking lock files...")
	for _, check := range report.Locks {
		printCheck(check)
	}
	fmt.Println()

	// Auth Files
	fmt.Println("Checking auth files...")
	for _, check := range report.AuthFiles {
		printCheck(check)
	}
	fmt.Println()

	// Token Validation (only if --validate was used)
	if validate && len(report.TokenValidation) > 0 {
		fmt.Println("Validating tokens...")
		for _, check := range report.TokenValidation {
			printCheck(check)
		}
		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d passed", report.PassCount)
	if report.FixedCount > 0 {
		fmt.Printf(", %d fixed", report.FixedCount)
	}
	if report.WarnCount > 0 {
		fmt.Printf(", %d warnings", report.WarnCount)
	}
	if report.FailCount > 0 {
		fmt.Printf(", %d failures", report.FailCount)
	}
	fmt.Println()

	if report.OverallOK {
		fmt.Println("\n✓ All checks passed!")
	} else {
		fmt.Println("\n✗ Some issues found. Run with --fix to attempt repairs.")
	}
}

func printCheck(check CheckResult) {
	var symbol string
	switch check.Status {
	case "pass":
		symbol = "  ✓"
	case "warn":
		symbol = "  ⚠"
	case "fail":
		symbol = "  ✗"
	case "fixed":
		symbol = "  ✓"
	}

	fmt.Printf("%s %s: %s\n", symbol, check.Name, check.Message)
	if check.Details != "" && check.Status != "pass" {
		fmt.Printf("      %s\n", check.Details)
	}
}
