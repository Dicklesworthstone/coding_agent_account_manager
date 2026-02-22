// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// DetectedAgent represents a single detected AI coding agent CLI.
type DetectedAgent struct {
	Name        string              `json:"name"`
	DisplayName string              `json:"display_name"`
	BinaryPath  string              `json:"binary_path,omitempty"`
	Version     string              `json:"version,omitempty"`
	Installed   bool                `json:"installed"`
	ConfigPaths []DetectedPath      `json:"config_paths,omitempty"`
	AuthPaths   []DetectedPath      `json:"auth_paths,omitempty"`
	HasAuth     bool                `json:"has_auth"`
	Status      DetectedAgentStatus `json:"status"`
	Error       string              `json:"error,omitempty"`
}

// DetectedPath represents a detected configuration or auth file path.
type DetectedPath struct {
	Path        string `json:"path"`
	Exists      bool   `json:"exists"`
	Readable    bool   `json:"readable"`
	Description string `json:"description,omitempty"`
}

// DetectedAgentStatus summarizes the agent's current state.
type DetectedAgentStatus string

const (
	StatusReady       DetectedAgentStatus = "ready"        // Binary installed, auth present
	StatusNeedsAuth   DetectedAgentStatus = "needs_auth"   // Binary installed, no auth
	StatusNotFound    DetectedAgentStatus = "not_found"    // Binary not found in PATH
	StatusUnavailable DetectedAgentStatus = "unavailable"  // Error checking status
)

// DetectReport contains the full detection results.
type DetectReport struct {
	Timestamp string           `json:"timestamp"`
	Agents    []DetectedAgent  `json:"agents"`
	Summary   DetectSummary    `json:"summary"`
}

// DetectSummary summarizes the detection results.
type DetectSummary struct {
	TotalAgents   int `json:"total_agents"`
	Installed     int `json:"installed"`
	Ready         int `json:"ready"`
	NeedAuth      int `json:"need_auth"`
	NotFound      int `json:"not_found"`
}

// AgentSpec defines how to detect a specific AI coding agent.
type AgentSpec struct {
	Name         string   // e.g., "claude", "codex", "gemini"
	DisplayName  string   // e.g., "Claude Code (Anthropic)"
	BinaryNames  []string // Binary names to search in PATH
	VersionArgs  []string // Arguments to get version (e.g., ["--version"])
	VersionRegex string   // Regex to extract version from output
	ConfigPaths  func() []PathSpec // Function to get config paths (uses home dir)
	AuthPaths    func() []PathSpec // Function to get auth paths (uses home dir)
}

// PathSpec describes a path to check.
type PathSpec struct {
	Path        string
	Description string
}

var detectCmd = &cobra.Command{
	Use:   "detect [agent...]",
	Short: "Detect installed AI coding agent CLIs and their configurations",
	Long: `Detect installed AI coding agent CLIs and their auth/config locations.

This command scans PATH for known AI coding CLI binaries (claude, codex, gemini,
opencode, aider, etc.), resolves their versions, and checks for existing auth
and config files.

The detection is read-only and never modifies any files or makes network calls.

Output includes:
  - Binary path and version for each detected agent
  - Config and auth file locations with readability status
  - Overall status: ready (has auth), needs_auth, or not_found

Examples:
  caam detect              # Detect all supported agents
  caam detect claude codex # Detect specific agents only
  caam detect --json       # Output as JSON for scripting

This is useful for:
  - First-run setup to understand what's already installed
  - Debugging auth issues
  - Scripting automated configuration`,
	RunE: runDetect,
}

func init() {
	rootCmd.AddCommand(detectCmd)
	detectCmd.Flags().Bool("json", false, "output in JSON format")
	detectCmd.Flags().Bool("verbose", false, "show all checked paths, even if not found")
}

// getAgentSpecs returns the specifications for all known AI coding agents.
func getAgentSpecs() []AgentSpec {
	homeDir, _ := os.UserHomeDir()
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" {
		xdgConfigHome = filepath.Join(homeDir, ".config")
	}
	claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeConfigDir == "" {
		claudeConfigDir = filepath.Join(xdgConfigHome, "claude-code")
	}

	return []AgentSpec{
		{
			Name:         "claude",
			DisplayName:  "Claude Code (Anthropic)",
			BinaryNames:  []string{"claude"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(?:claude[- ]?code[- ]?|v?)(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".claude"), "Claude Code home directory"},
					{claudeConfigDir, "Claude Code XDG config"},
				}
			},
			AuthPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".claude", ".credentials.json"), "OAuth credentials (primary)"},
					{filepath.Join(homeDir, ".claude.json"), "OAuth session state (legacy)"},
					{filepath.Join(claudeConfigDir, "auth.json"), "Auth credentials (XDG)"},
					{filepath.Join(homeDir, ".claude", "settings.json"), "Settings (API key mode)"},
				}
			},
		},
		{
			Name:         "codex",
			DisplayName:  "Codex CLI (OpenAI)",
			BinaryNames:  []string{"codex"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(?:codex[- ]?|v?)(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				codexHome := os.Getenv("CODEX_HOME")
				if codexHome == "" {
					codexHome = filepath.Join(homeDir, ".codex")
				}
				return []PathSpec{
					{codexHome, "Codex home directory"},
				}
			},
			AuthPaths: func() []PathSpec {
				codexHome := os.Getenv("CODEX_HOME")
				if codexHome == "" {
					codexHome = filepath.Join(homeDir, ".codex")
				}
				return []PathSpec{
					{filepath.Join(codexHome, "auth.json"), "OAuth credentials"},
					{filepath.Join(codexHome, "config.toml"), "Codex configuration"},
				}
			},
		},
		{
			Name:         "gemini",
			DisplayName:  "Gemini CLI (Google)",
			BinaryNames:  []string{"gemini"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(?:gemini[- ]?|v?)(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".gemini"), "Gemini home directory"},
				}
			},
			AuthPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".gemini", "settings.json"), "OAuth/settings"},
					{filepath.Join(homeDir, ".gemini", "oauth_creds.json"), "OAuth credentials"},
					{filepath.Join(homeDir, ".gemini", ".env"), "API key (env mode)"},
				}
			},
		},
		{
			Name:         "aider",
			DisplayName:  "Aider (AI Pair Programming)",
			BinaryNames:  []string{"aider"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(?:aider[- ]?|v?)(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".aider"), "Aider home directory"},
					{filepath.Join(xdgConfigHome, "aider"), "Aider XDG config"},
				}
			},
			AuthPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".aider.conf.yml"), "Aider config (API keys)"},
					{filepath.Join(xdgConfigHome, "aider", "aider.conf.yml"), "Aider XDG config"},
				}
			},
		},
		{
			Name:         "opencode",
			DisplayName:  "OpenCode",
			BinaryNames:  []string{"opencode"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(?:opencode[- ]?|v?)(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".opencode"), "OpenCode home directory"},
					{filepath.Join(xdgConfigHome, "opencode"), "OpenCode XDG config"},
				}
			},
			AuthPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".opencode", "config.json"), "OpenCode config"},
					{filepath.Join(xdgConfigHome, "opencode", "config.json"), "OpenCode XDG config"},
				}
			},
		},
		{
			Name:         "cursor",
			DisplayName:  "Cursor",
			BinaryNames:  []string{"cursor"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".cursor"), "Cursor home directory"},
					{filepath.Join(xdgConfigHome, "Cursor"), "Cursor XDG config"},
				}
			},
			AuthPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(xdgConfigHome, "Cursor", "User", "globalStorage", "state.vscdb"), "Cursor state DB"},
				}
			},
		},
		{
			Name:         "continue",
			DisplayName:  "Continue.dev",
			BinaryNames:  []string{"continue"},
			VersionArgs:  []string{"--version"},
			VersionRegex: `(\d+\.\d+(?:\.\d+)?)`,
			ConfigPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".continue"), "Continue home directory"},
				}
			},
			AuthPaths: func() []PathSpec {
				return []PathSpec{
					{filepath.Join(homeDir, ".continue", "config.json"), "Continue config"},
				}
			},
		},
	}
}

func runDetect(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	verbose, _ := cmd.Flags().GetBool("verbose")

	specs := getAgentSpecs()

	// Filter to requested agents if specified
	if len(args) > 0 {
		filtered := make([]AgentSpec, 0, len(args))
		for _, arg := range args {
			name := strings.ToLower(arg)
			found := false
			for _, spec := range specs {
				if spec.Name == name {
					filtered = append(filtered, spec)
					found = true
					break
				}
			}
			if !found {
				names := make([]string, len(specs))
				for i, s := range specs {
					names[i] = s.Name
				}
				return fmt.Errorf("unknown agent: %s (supported: %s)", arg, strings.Join(names, ", "))
			}
		}
		specs = filtered
	}

	report := runDetection(cmd.Context(), specs, verbose)

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	printDetectReport(report, verbose)
	return nil
}

func runDetection(ctx context.Context, specs []AgentSpec, verbose bool) *DetectReport {
	report := &DetectReport{
		Timestamp: time.Now().Format(time.RFC3339),
		Agents:    make([]DetectedAgent, 0, len(specs)),
	}

	for _, spec := range specs {
		agent := detectAgent(ctx, spec, verbose)
		report.Agents = append(report.Agents, agent)

		// Update summary
		report.Summary.TotalAgents++
		if agent.Installed {
			report.Summary.Installed++
			if agent.Status == StatusReady {
				report.Summary.Ready++
			} else if agent.Status == StatusNeedsAuth {
				report.Summary.NeedAuth++
			}
		} else {
			report.Summary.NotFound++
		}
	}

	return report
}

func detectAgent(ctx context.Context, spec AgentSpec, verbose bool) DetectedAgent {
	agent := DetectedAgent{
		Name:        spec.Name,
		DisplayName: spec.DisplayName,
	}

	// Find binary in PATH
	binaryPath, err := findBinary(spec.BinaryNames)
	if err != nil {
		agent.Installed = false
		agent.Status = StatusNotFound

		// Still check config/auth paths if verbose
		if verbose {
			agent.ConfigPaths = checkPaths(spec.ConfigPaths())
			agent.AuthPaths = checkPaths(spec.AuthPaths())
		}
		return agent
	}

	agent.Installed = true
	agent.BinaryPath = binaryPath

	// Get version
	version, err := getVersion(ctx, binaryPath, spec.VersionArgs, spec.VersionRegex)
	if err == nil {
		agent.Version = version
	}

	// Check config paths
	agent.ConfigPaths = checkPaths(spec.ConfigPaths())

	// Check auth paths
	agent.AuthPaths = checkPaths(spec.AuthPaths())

	// Determine if we have auth
	agent.HasAuth = hasValidAuth(agent.AuthPaths)

	// Set status
	if agent.HasAuth {
		agent.Status = StatusReady
	} else {
		agent.Status = StatusNeedsAuth
	}

	return agent
}

// findBinary searches for any of the given binary names in PATH.
func findBinary(names []string) (string, error) {
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("binary not found in PATH")
}

// getVersion runs the binary with version args and extracts the version.
func getVersion(ctx context.Context, binaryPath string, args []string, versionRegex string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("no version args specified")
	}

	// Create context with timeout to avoid hanging
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Some CLIs return non-zero for --version but still output version
		if len(output) == 0 {
			return "", err
		}
	}

	// Extract version using regex
	re, err := regexp.Compile(versionRegex)
	if err != nil {
		return "", err
	}

	matches := re.FindStringSubmatch(string(output))
	if len(matches) >= 2 {
		return matches[1], nil
	}

	// Fallback: try to find any version-like string
	fallbackRe := regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
	if match := fallbackRe.FindString(string(output)); match != "" {
		return match, nil
	}

	return "", fmt.Errorf("version not found in output")
}

// checkPaths checks each path spec and returns detection results.
func checkPaths(specs []PathSpec) []DetectedPath {
	paths := make([]DetectedPath, 0, len(specs))
	for _, spec := range specs {
		dp := DetectedPath{
			Path:        spec.Path,
			Description: spec.Description,
		}

		info, err := os.Stat(spec.Path)
		if err != nil {
			dp.Exists = false
			dp.Readable = false
		} else {
			dp.Exists = true
			// Check if readable (for files, try to open; for dirs, check permissions)
			if info.IsDir() {
				// Try to list directory
				_, err := os.ReadDir(spec.Path)
				dp.Readable = err == nil
			} else {
				// Try to open file
				f, err := os.Open(spec.Path)
				if err == nil {
					f.Close()
					dp.Readable = true
				}
			}
		}

		paths = append(paths, dp)
	}
	return paths
}

// hasValidAuth checks if any auth path exists and is readable.
func hasValidAuth(paths []DetectedPath) bool {
	for _, p := range paths {
		if p.Exists && p.Readable {
			return true
		}
	}
	return false
}

func printDetectReport(report *DetectReport, verbose bool) {
	fmt.Println("Detecting installed AI coding agents...")
	fmt.Println()

	for _, agent := range report.Agents {
		// Status icon
		var icon string
		switch agent.Status {
		case StatusReady:
			icon = "✓"
		case StatusNeedsAuth:
			icon = "⚠"
		case StatusNotFound:
			icon = "✗"
		default:
			icon = "?"
		}

		fmt.Printf("%s %s\n", icon, agent.DisplayName)

		if agent.Status == StatusNotFound {
			fmt.Println("  Binary not found in PATH")
			if verbose && len(agent.ConfigPaths) > 0 {
				fmt.Println("  Config paths checked:")
				for _, p := range agent.ConfigPaths {
					existsStr := "missing"
					if p.Exists {
						existsStr = "exists"
					}
					fmt.Printf("    - %s (%s)\n", shortenPath(p.Path), existsStr)
				}
			}
			fmt.Println()
			continue
		}

		// Show binary path and version
		fmt.Printf("  Binary: %s\n", agent.BinaryPath)
		if agent.Version != "" {
			fmt.Printf("  Version: %s\n", agent.Version)
		}

		// Show status
		switch agent.Status {
		case StatusReady:
			fmt.Println("  Status: Ready (auth configured)")
		case StatusNeedsAuth:
			fmt.Println("  Status: Needs authentication")
		}

		// Show auth paths (existing ones, or all if verbose)
		if len(agent.AuthPaths) > 0 {
			hasExisting := false
			for _, p := range agent.AuthPaths {
				if p.Exists {
					hasExisting = true
					break
				}
			}

			if hasExisting || verbose {
				fmt.Println("  Auth files:")
				for _, p := range agent.AuthPaths {
					if !p.Exists && !verbose {
						continue
					}
					var status string
					if p.Exists && p.Readable {
						status = "✓ found"
					} else if p.Exists {
						status = "⚠ not readable"
					} else {
						status = "missing"
					}
					fmt.Printf("    - %s (%s)\n", shortenPath(p.Path), status)
				}
			}
		}

		// Show config paths if verbose
		if verbose && len(agent.ConfigPaths) > 0 {
			fmt.Println("  Config directories:")
			for _, p := range agent.ConfigPaths {
				var status string
				if p.Exists && p.Readable {
					status = "✓ found"
				} else if p.Exists {
					status = "⚠ not readable"
				} else {
					status = "missing"
				}
				fmt.Printf("    - %s (%s)\n", shortenPath(p.Path), status)
			}
		}

		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d agent(s) checked\n", report.Summary.TotalAgents)
	fmt.Printf("  - %d installed (%d ready, %d need auth)\n",
		report.Summary.Installed, report.Summary.Ready, report.Summary.NeedAuth)
	fmt.Printf("  - %d not found\n", report.Summary.NotFound)

	// Actionable suggestions
	if report.Summary.NeedAuth > 0 {
		fmt.Println("\nTo set up authentication:")
		for _, agent := range report.Agents {
			if agent.Status == StatusNeedsAuth {
				switch agent.Name {
				case "claude":
					fmt.Printf("  %s: Run 'claude' then use /login\n", agent.Name)
				case "codex":
					fmt.Printf("  %s: Run 'codex login'\n", agent.Name)
				case "gemini":
					fmt.Printf("  %s: Run 'gemini' and select Login with Google\n", agent.Name)
				default:
					fmt.Printf("  %s: See tool documentation for login\n", agent.Name)
				}
			}
		}
	}

	if report.Summary.Ready > 0 {
		fmt.Println("\nTo manage profiles with caam:")
		fmt.Println("  caam backup <tool> <profile-name>  # Save current auth")
		fmt.Println("  caam activate <tool> <profile>     # Switch profiles")
	}
}
