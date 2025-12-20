// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
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
	Directories     []CheckResult `json:"directories"`
	Config          []CheckResult `json:"config"`
	Profiles        []CheckResult `json:"profiles"`
	Locks           []CheckResult `json:"locks"`
	AuthFiles       []CheckResult `json:"auth_files"`
	TokenValidation []CheckResult `json:"token_validation,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose setup issues",
	Long: `Runs diagnostic checks on your caam installation and reports any issues.

Checks performed:
  - CLI tools: Are codex, claude, gemini installed and in PATH?
  - Data directories: Do vault/profiles directories exist with correct permissions?
  - Config: Is the configuration valid?
  - Profiles: Are all isolated profiles valid? Any broken symlinks?
  - Locks: Are there any stale lock files from crashed processes?
  - Auth files: Do auth files exist for each provider?
  - Token validation (with --validate): Are auth tokens actually valid?

Flags:
  --fix       Attempt to fix issues (create directories, clean stale locks)
  --json      Output results in JSON format for scripting
  --validate  Validate that auth tokens actually work (passive check, no API calls)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fix, _ := cmd.Flags().GetBool("fix")
		jsonOutput, _ := cmd.Flags().GetBool("json")
		validate, _ := cmd.Flags().GetBool("validate")

		report := runDoctorChecks(fix, validate)

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
}

func runDoctorChecks(fix bool, validate bool) *DoctorReport {
	report := &DoctorReport{
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Check CLI tools
	report.CLITools = checkCLITools()

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
	allChecks := append(report.CLITools, report.Directories...)
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

func checkDirectories(fix bool) []CheckResult {
	var results []CheckResult

	homeDir, _ := os.UserHomeDir()
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(homeDir, ".local", "share")
	}

	dirs := []struct {
		path string
		name string
	}{
		{filepath.Join(xdgData, "caam"), "caam data directory"},
		{filepath.Join(xdgData, "caam", "vault"), "vault directory"},
		{filepath.Join(xdgData, "caam", "profiles"), "profiles directory"},
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
	}

	return results
}

// checkTokenValidation validates auth tokens for all profiles.
// This performs passive validation (no API calls) by checking token format and expiry.
func checkTokenValidation() []CheckResult {
	var results []CheckResult
	ctx := context.Background()

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
