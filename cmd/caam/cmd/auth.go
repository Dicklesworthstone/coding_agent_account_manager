// Package cmd implements the CLI commands for caam.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// AuthDetectResult represents the result of auth detection for one provider.
type AuthDetectResult struct {
	Provider  string               `json:"provider"`
	Found     bool                 `json:"found"`
	Locations []AuthDetectLocation `json:"locations"`
	Primary   *AuthDetectLocation  `json:"primary,omitempty"`
	Warning   string               `json:"warning,omitempty"`
	Error     string               `json:"error,omitempty"`
}

// AuthDetectLocation represents a detected auth file location.
type AuthDetectLocation struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	LastModified    string `json:"last_modified,omitempty"`
	FileSize        int64  `json:"file_size,omitempty"`
	IsValid         bool   `json:"is_valid"`
	ValidationError string `json:"validation_error,omitempty"`
	Description     string `json:"description"`
}

// AuthDetectReport contains the results of auth detection for all providers.
type AuthDetectReport struct {
	Timestamp string             `json:"timestamp"`
	Results   []AuthDetectResult `json:"results"`
	Summary   AuthDetectSummary  `json:"summary"`
}

// AuthDetectSummary provides a summary of detected auth.
type AuthDetectSummary struct {
	TotalProviders int `json:"total_providers"`
	FoundCount     int `json:"found_count"`
	NotFoundCount  int `json:"not_found_count"`
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication credentials",
	Long: `Commands for managing authentication credentials.

Subcommands:
  detect  - Detect existing auth files in system locations
  import  - Import detected auth into a caam profile`,
}

var authDetectCmd = &cobra.Command{
	Use:   "detect [tool]",
	Short: "Detect existing auth files",
	Long: `Detect existing authentication files in standard system locations.

This scans for existing auth files from direct CLI tool usage:
  - Claude: ~/.claude.json, ~/.config/claude-code/auth.json, $CLAUDE_CONFIG_DIR/auth.json
  - Codex: ~/.codex/auth.json
  - Gemini: ~/.gemini/settings.json, ~/.gemini/.env, gcloud ADC

If a tool argument is provided, only that tool is checked.
Otherwise, all supported tools are scanned.

Examples:
  caam auth detect           # Detect all providers
  caam auth detect claude    # Detect Claude auth only
  caam auth detect --json    # Output as JSON

This is useful for first-run experience to discover and import existing credentials.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")

		var providersToCheck []provider.Provider

		if len(args) > 0 {
			// Check specific provider
			tool := strings.ToLower(args[0])
			p, ok := registry.Get(tool)
			if !ok {
				return fmt.Errorf("unknown tool: %s (supported: claude, codex, gemini)", tool)
			}
			providersToCheck = append(providersToCheck, p)
		} else {
			// Check all providers
			providersToCheck = registry.All()
		}

		report := runAuthDetection(providersToCheck)

		if jsonOutput {
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		printAuthDetectReport(report)
		return nil
	},
}

// AuthImportResult represents the result of an auth import operation.
type AuthImportResult struct {
	Provider    string   `json:"provider"`
	ProfileName string   `json:"profile_name"`
	ProfilePath string   `json:"profile_path"`
	SourceFile  string   `json:"source_file"`
	CopiedFiles []string `json:"copied_files"`
	BackupPath  string   `json:"backup_path,omitempty"`
	Success     bool     `json:"success"`
	Error       string   `json:"error,omitempty"`
}

var authImportCmd = &cobra.Command{
	Use:   "import <tool>",
	Short: "Import detected auth into a profile",
	Long: `Import existing authentication files into a new caam profile.

This detects existing auth credentials and imports them into a new profile,
allowing you to manage multiple accounts without re-authenticating.

Credentials are validated before the profile becomes available. With --force,
the previous profile is retained at the backup path printed in the result.
An in-use profile cannot be replaced.

Supported tools: claude, codex, gemini, grok, opencode, cursor, agy.

Examples:
  caam auth import claude                    # Import Claude auth to 'default' profile
  caam auth import codex -n work             # Import Codex auth to 'work' profile
  caam auth import gemini --source ~/.gemini/settings.json  # Import specific file
  caam auth import claude --force            # Replace and retain previous profile
  caam auth import claude --json             # Output as JSON

Use 'caam auth detect' first to see what auth files are available.`,
	Args: cobra.ExactArgs(1),
	RunE: runAuthImport,
}

func init() {
	rootCmd.AddCommand(authCmd)
	authCmd.AddCommand(authDetectCmd)
	authDetectCmd.Flags().Bool("json", false, "output in JSON format")

	authCmd.AddCommand(authImportCmd)
	authImportCmd.Flags().StringP("name", "n", "default", "profile name")
	authImportCmd.Flags().StringP("description", "d", "", "profile description")
	authImportCmd.Flags().Bool("force", false, "replace an existing profile after validation, retaining its previous data")
	authImportCmd.Flags().String("source", "", "path to auth file (overrides detection)")
	authImportCmd.Flags().Bool("json", false, "output in JSON format")
}

func runAuthDetection(providers []provider.Provider) *AuthDetectReport {
	report := &AuthDetectReport{
		Timestamp: time.Now().Format(time.RFC3339),
		Results:   make([]AuthDetectResult, 0, len(providers)),
	}

	for _, p := range providers {
		result := AuthDetectResult{
			Provider:  p.ID(),
			Locations: []AuthDetectLocation{},
		}

		detection, err := p.DetectExistingAuth()
		if err != nil {
			result.Error = err.Error()
			report.Results = append(report.Results, result)
			continue
		}

		result.Found = detection.Found
		result.Warning = detection.Warning

		for _, loc := range detection.Locations {
			detectLoc := AuthDetectLocation{
				Path:            loc.Path,
				Exists:          loc.Exists,
				FileSize:        loc.FileSize,
				IsValid:         loc.IsValid,
				ValidationError: loc.ValidationError,
				Description:     loc.Description,
			}
			if !loc.LastModified.IsZero() {
				detectLoc.LastModified = loc.LastModified.Format(time.RFC3339)
			}
			result.Locations = append(result.Locations, detectLoc)
		}

		if detection.Primary != nil {
			primary := AuthDetectLocation{
				Path:            detection.Primary.Path,
				Exists:          detection.Primary.Exists,
				FileSize:        detection.Primary.FileSize,
				IsValid:         detection.Primary.IsValid,
				ValidationError: detection.Primary.ValidationError,
				Description:     detection.Primary.Description,
			}
			if !detection.Primary.LastModified.IsZero() {
				primary.LastModified = detection.Primary.LastModified.Format(time.RFC3339)
			}
			result.Primary = &primary
		}

		report.Results = append(report.Results, result)

		if detection.Found {
			report.Summary.FoundCount++
		} else {
			report.Summary.NotFoundCount++
		}
	}

	report.Summary.TotalProviders = len(providers)

	return report
}

func printAuthDetectReport(report *AuthDetectReport) {
	fmt.Println("Detecting existing auth credentials...")
	fmt.Println()

	for _, result := range report.Results {
		displayName := getProviderDisplayName(result.Provider)
		fmt.Printf("%s:\n", displayName)

		if result.Error != "" {
			fmt.Printf("  ✗ Error: %s\n", result.Error)
			fmt.Println()
			continue
		}

		if !result.Found {
			fmt.Println("  ✗ No existing auth detected")
			// Show checked locations
			if len(result.Locations) > 0 {
				fmt.Println("    Checked:")
				for _, loc := range result.Locations {
					fmt.Printf("    - %s\n", shortenPath(loc.Path))
				}
			}
			fmt.Println()
			continue
		}

		// Show found auth files
		for _, loc := range result.Locations {
			if !loc.Exists {
				continue
			}

			statusIcon := "✓"
			status := "Valid"
			if !loc.IsValid {
				statusIcon = "⚠"
				status = loc.ValidationError
			}

			fmt.Printf("  %s %s\n", statusIcon, shortenPath(loc.Path))
			if loc.LastModified != "" {
				t, err := time.Parse(time.RFC3339, loc.LastModified)
				if err == nil {
					fmt.Printf("    Last modified: %s\n", t.Format("2006-01-02 15:04:05"))
				}
			}
			fmt.Printf("    Size: %s\n", formatFileSize(loc.FileSize))
			fmt.Printf("    Status: %s\n", status)
		}

		if result.Warning != "" {
			fmt.Printf("  ⚠ %s\n", result.Warning)
		}

		fmt.Println()
	}

	// Summary
	fmt.Printf("Summary: %d provider(s) checked, %d with auth, %d without\n",
		report.Summary.TotalProviders,
		report.Summary.FoundCount,
		report.Summary.NotFoundCount)

	if report.Summary.FoundCount > 0 {
		fmt.Println("\nRun 'caam auth import <tool>' to import detected credentials into a profile.")
	}
}

func getProviderDisplayName(id string) string {
	meta, ok := provider.GetProviderMeta(id)
	if ok {
		return meta.DisplayName
	}
	return capitalizeFirst(id)
}

// capitalizeFirst returns the string with its first letter capitalized.
// This is a replacement for the deprecated strings.Title function.
// Uses Unicode-aware rune handling for proper UTF-8 support.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func shortenPath(path string) string {
	// Replace home directory with ~
	homeDir := ""
	if h, err := getHomeDir(); err == nil {
		homeDir = h
	}
	if homeDir != "" && strings.HasPrefix(path, homeDir) {
		return "~" + path[len(homeDir):]
	}
	return path
}

func getHomeDir() (string, error) {
	// Use os.UserHomeDir - this is a wrapper to avoid importing os here
	// since we already have access to it via provider implementations
	// For now, just try to get it from environment
	if home := getEnv("HOME"); home != "" {
		return home, nil
	}
	if userProfile := getEnv("USERPROFILE"); userProfile != "" {
		return userProfile, nil
	}
	return "", fmt.Errorf("home directory not found")
}

func getEnv(key string) string {
	// Simple wrapper - we can use os.Getenv directly but this keeps the code clean
	// and allows for future abstraction if needed
	return envLookup(key)
}

// envLookup is a variable so it can be mocked in tests
var envLookup = func(key string) string {
	return os.Getenv(key)
}

func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// runAuthImport implements the auth import command.
func runAuthImport(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	name, _ := cmd.Flags().GetString("name")
	description, _ := cmd.Flags().GetString("description")
	force, _ := cmd.Flags().GetBool("force")
	source, _ := cmd.Flags().GetString("source")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Validate provider
	prov, ok := registry.Get(tool)
	if !ok {
		return fmt.Errorf("unknown tool: %s (supported: claude, codex, gemini)", tool)
	}

	// Check if profile exists
	if profileStore.Exists(tool, name) && !force {
		return fmt.Errorf("profile %s/%s already exists (use --force to overwrite)", tool, name)
	}

	// Determine source file
	var sourcePath string
	if source != "" {
		// Use explicit source path
		sourcePath = source
		// Expand ~ to home directory
		if strings.HasPrefix(sourcePath, "~/") {
			if home, err := getHomeDir(); err == nil {
				sourcePath = home + sourcePath[1:]
			}
		}
		// Verify source exists
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			return fmt.Errorf("source file not found: %s", source)
		}
	} else {
		// Auto-detect auth
		detection, err := prov.DetectExistingAuth()
		if err != nil {
			return fmt.Errorf("detect auth: %w", err)
		}
		if detection == nil || !detection.Found || detection.Primary == nil || !detection.Primary.IsValid {
			return fmt.Errorf("no existing auth detected for %s; run 'caam auth detect %s' to see details or use --source", tool, tool)
		}
		sourcePath = detection.Primary.Path
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := importAuthProfile(ctx, prov, name, sourcePath, description, force)
	if jsonOutput {
		return outputImportResult(cmd, result)
	}
	if err != nil {
		return err
	}

	// Print success message
	displayName := getProviderDisplayName(tool)
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Successfully imported %s auth to profile '%s'\n\n", displayName, name)
	fmt.Fprintf(out, "  Profile: %s/%s\n", tool, name)
	fmt.Fprintf(out, "  Path: %s\n", result.ProfilePath)
	fmt.Fprintf(out, "  Source: %s\n", shortenPath(sourcePath))
	if result.BackupPath != "" {
		fmt.Fprintf(out, "  Previous profile retained at: %s\n", result.BackupPath)
	}
	fmt.Fprintln(out, "  Files copied:")
	for _, f := range result.CopiedFiles {
		fmt.Fprintf(out, "    - %s\n", shortenPath(f))
	}
	fmt.Fprint(out, "\nNext steps:\n")
	fmt.Fprintf(out, "  Run your CLI with: caam exec %s %s -- <your command>\n", tool, name)
	fmt.Fprintf(out, "  Or activate profile: eval \"$(caam env %s %s)\"\n", tool, name)

	return nil
}

// importAuthProfile is the shared intake path for explicit imports and setup.
// Credentials are checked inside an unpublished profile, so errors cannot
// remove a working account or leave a partially imported account routable.
func importAuthProfile(ctx context.Context, prov provider.Provider, name, sourcePath, description string, force bool) (result AuthImportResult, err error) {
	result = AuthImportResult{Provider: prov.ID(), ProfileName: name, SourceFile: sourcePath}
	defer func() {
		if err != nil {
			result.Error = err.Error()
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return result, fmt.Errorf("inspect source credential: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > authfile.MaxDiscoveryFileBytes {
		return result, fmt.Errorf("source credential must be a regular file of at most %d bytes", authfile.MaxDiscoveryFileBytes)
	}
	var relativeFiles []string
	imported, err := profileStore.Import(ctx, prov.ID(), name, string(provider.AuthModeOAuth), force, func(prof *profile.Profile) error {
		prof.Description = description
		if err := prov.PrepareProfile(ctx, prof); err != nil {
			return fmt.Errorf("prepare profile: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		files, err := prov.ImportAuth(ctx, sourcePath, prof)
		if err != nil {
			return fmt.Errorf("import auth: %w", err)
		}
		relativeFiles, err = validateImportedFiles(prov.ID(), prof.BasePath, files)
		if err != nil {
			return err
		}
		if force && prov.ID() == "claude" {
			helpers, err := preserveImportedClaudeHelper(prof, profileStore.ProfilePath(prov.ID(), name), relativeFiles)
			if err != nil {
				return fmt.Errorf("preserve imported Claude helper: %w", err)
			}
			if len(helpers) > 0 {
				helperFiles, err := validateImportedFiles(prov.ID(), prof.BasePath, helpers)
				if err != nil {
					return err
				}
				relativeFiles = append(relativeFiles, helperFiles...)
			}
		}
		if err := prov.ValidateProfile(ctx, prof); err != nil {
			return fmt.Errorf("validate imported profile: %w", err)
		}
		validation, err := prov.ValidateToken(ctx, prof, true)
		if err != nil {
			return fmt.Errorf("validate imported credentials: %w", err)
		}
		if validation == nil || !validation.Valid {
			reason := "provider did not confirm usable credentials"
			if validation != nil && validation.Error != "" {
				reason = validation.Error
			}
			return fmt.Errorf("validate imported credentials: %s", reason)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Success = true
	result.ProfilePath = imported.Profile.BasePath
	result.BackupPath = imported.BackupPath
	for _, path := range relativeFiles {
		result.CopiedFiles = append(result.CopiedFiles, filepath.Join(result.ProfilePath, path))
	}
	return result, nil
}

// Preserve a literal helper inside the profile being replaced. Its old directory
// moves into the retained backup at publication, so the imported settings must
// refer to a private copy in the replacement. External commands are unchanged.
func preserveImportedClaudeHelper(prof *profile.Profile, finalPath string, files []string) ([]string, error) {
	finalPath, err := filepath.Abs(finalPath)
	if err != nil {
		return nil, err
	}
	var helpers []string
	for _, relative := range files {
		if filepath.Base(relative) != "settings.json" {
			continue
		}
		settingsPath := filepath.Join(prof.BasePath, relative)
		data, err := claudesettings.Read(settingsPath)
		if err != nil {
			return nil, err
		}
		var settings struct {
			Helper string `json:"apiKeyHelper"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, err
		}
		helperPath := settings.Helper
		// Recognize only a literal path, including our own quoting convention.
		// Do not interpret arguments, expansions, or arbitrary shell programs.
		if strings.HasPrefix(helperPath, "'") && strings.HasSuffix(helperPath, "'") && len(helperPath) >= 2 {
			unquoted := strings.ReplaceAll(helperPath[1:len(helperPath)-1], "'\"'\"'", "'")
			if shellQuote(unquoted) == helperPath {
				helperPath = unquoted
			}
		}
		inside, relErr := filepath.Rel(finalPath, helperPath)
		if !filepath.IsAbs(helperPath) || relErr != nil || !filepath.IsLocal(inside) {
			if strings.Contains(settings.Helper, finalPath+string(filepath.Separator)) {
				return nil, fmt.Errorf("profile-owned apiKeyHelper must be a literal absolute path")
			}
			continue
		}
		info, err := os.Lstat(helperPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() > authfile.MaxDiscoveryFileBytes {
			return nil, fmt.Errorf("profile-owned apiKeyHelper must be a regular file of at most %d bytes", authfile.MaxDiscoveryFileBytes)
		}
		resolvedRoot, err := filepath.EvalSymlinks(finalPath)
		if err != nil {
			return nil, err
		}
		resolvedHelper, err := filepath.EvalSymlinks(helperPath)
		if err != nil {
			return nil, err
		}
		inside, err = filepath.Rel(resolvedRoot, resolvedHelper)
		if err != nil || !filepath.IsLocal(inside) {
			return nil, fmt.Errorf("profile-owned apiKeyHelper points outside the profile")
		}
		source, err := os.Open(helperPath)
		if err != nil {
			return nil, err
		}
		opened, statErr := source.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			source.Close()
			return nil, fmt.Errorf("profile-owned apiKeyHelper changed during import")
		}
		body, readErr := io.ReadAll(io.LimitReader(source, authfile.MaxDiscoveryFileBytes+1))
		source.Close()
		if readErr != nil || int64(len(body)) > authfile.MaxDiscoveryFileBytes {
			return nil, fmt.Errorf("read profile-owned apiKeyHelper: invalid or oversized file")
		}
		target, err := os.CreateTemp(prof.BasePath, "imported-api-key-helper-*")
		if err != nil {
			return nil, err
		}
		if err := target.Chmod(0700); err != nil {
			target.Close()
			return nil, err
		}
		if _, err := target.Write(body); err != nil {
			target.Close()
			return nil, err
		}
		if err := target.Sync(); err != nil {
			target.Close()
			return nil, err
		}
		if err := target.Close(); err != nil {
			return nil, err
		}
		command := shellQuote(filepath.Join(finalPath, filepath.Base(target.Name())))
		update, err := claudesettings.PrepareAPIKeyHelper(settingsPath, command)
		if err != nil {
			return nil, err
		}
		if err := update.Apply(); err != nil {
			return nil, err
		}
		helpers = append(helpers, target.Name())
	}
	return helpers, nil
}

func validateImportedFiles(tool, basePath string, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("import produced no credential files")
	}
	root, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		return nil, fmt.Errorf("resolve imported profile: %w", err)
	}
	var relativeFiles []string
	for _, path := range files {
		if !filepath.IsAbs(path) {
			path = filepath.Join(basePath, path)
		}
		relative, err := filepath.Rel(basePath, path)
		if err != nil || !filepath.IsLocal(relative) {
			return nil, fmt.Errorf("imported file is outside the profile")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("resolve imported file: %w", err)
		}
		inside, err := filepath.Rel(root, resolved)
		if err != nil || !filepath.IsLocal(inside) {
			return nil, fmt.Errorf("imported file points outside the profile")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > authfile.MaxDiscoveryFileBytes {
			return nil, fmt.Errorf("imported file must be a regular file of at most %d bytes", authfile.MaxDiscoveryFileBytes)
		}
		// These adapters retain permissive passive validators for historical
		// formats. Apply the native discovery credential checks before intake.
		if tool == "grok" || tool == "opencode" || tool == "agy" {
			f, err := os.Open(path)
			if err != nil {
				return nil, fmt.Errorf("read imported credential: %w", err)
			}
			data, readErr := io.ReadAll(io.LimitReader(f, authfile.MaxDiscoveryFileBytes+1))
			closeErr := f.Close()
			if readErr != nil || closeErr != nil {
				return nil, fmt.Errorf("cannot read imported credential")
			}
			if err := authfile.ValidateCredentialData(tool, filepath.Base(path), data); err != nil {
				return nil, fmt.Errorf("validate imported credential: %w", err)
			}
		}
		relativeFiles = append(relativeFiles, relative)
	}
	return relativeFiles, nil
}

func outputImportResult(cmd *cobra.Command, result AuthImportResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(data)); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}
