package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// tokenCmd is the parent command for token-based profiles.
var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage token profiles (env-injection, no file swapping)",
	Long: `Token profiles store a single long-lived token in the vault and inject it
into the wrapped tool's environment at run time instead of swapping auth files
on disk.

For Claude Code this is the recommended mechanism on macOS: credentials live
in the single-slot system Keychain there, so file-swap profiles cannot switch
accounts. Tokens minted with 'claude setup-token' bypass the Keychain and are
parallel-safe. caam injects:

  CLAUDE_CODE_OAUTH_TOKEN  the stored token
  CLAUDE_CONFIG_DIR        $HOME/.claude-<name> (per-profile isolation)

Token profiles are first-class: they appear in ls/status, participate in
rotation and cooldowns, and 'caam activate' on a token profile sets it as the
default for 'caam run'/'caam exec'.

Examples:
  claude setup-token | caam token add claude work    # pipe a fresh token
  caam token add claude personal                     # paste interactively
  caam token import                                  # import existing token files
  caam token ls                                      # list token profiles
  caam token rm claude old                           # delete a token profile`,
}

var tokenAddCmd = &cobra.Command{
	Use:   "add <provider> <name>",
	Short: "Add a token profile (token read from stdin or interactive paste)",
	Long: `Stores a token as a new token profile in the vault (mode 0600).

The token is read from stdin when piped, or prompted for (hidden input) when
run interactively. The token itself is never accepted as a command-line
argument so it cannot leak into shell history or process listings.

Examples:
  claude setup-token | caam token add claude work
  caam token add claude personal   # prompts for a hidden paste`,
	Args: cobra.ExactArgs(2),
	RunE: runTokenAdd,
}

var tokenImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import existing token files as token profiles",
	Long: `Scans a directory for existing token files and imports each as a token
profile. Recognized pattern: claude-<name>-token (e.g. claude-burst-token
imports as claude/burst).

The default directory is ~/.config/veup, the conventional home of tokens
minted with 'claude setup-token'. Source files are only read, never modified
or removed. Existing profiles are skipped unless --force is given.

Examples:
  caam token import
  caam token import --dir /path/to/tokens
  caam token import --force   # overwrite existing profiles`,
	Args: cobra.NoArgs,
	RunE: runTokenImport,
}

var tokenLsCmd = &cobra.Command{
	Use:     "ls [provider]",
	Aliases: []string{"list"},
	Short:   "List token profiles",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runTokenLs,
}

var tokenRmCmd = &cobra.Command{
	Use:     "rm <provider> <name>",
	Aliases: []string{"delete"},
	Short:   "Delete a token profile",
	Args:    cobra.ExactArgs(2),
	RunE:    runTokenRm,
}

func init() {
	rootCmd.AddCommand(tokenCmd)
	tokenCmd.AddCommand(tokenAddCmd)
	tokenCmd.AddCommand(tokenImportCmd)
	tokenCmd.AddCommand(tokenLsCmd)
	tokenCmd.AddCommand(tokenRmCmd)

	tokenAddCmd.Flags().Bool("json", false, "output as JSON")
	tokenAddCmd.Flags().Bool("force", false, "allow replacing an existing profile")
	tokenAddCmd.Flags().Bool("no-verify", false, "skip the passive token format check")

	tokenImportCmd.Flags().String("dir", "", "directory to scan (default ~/.config/veup)")
	tokenImportCmd.Flags().Bool("force", false, "overwrite existing profiles")
	tokenImportCmd.Flags().Bool("json", false, "output as JSON")

	tokenLsCmd.Flags().Bool("json", false, "output as JSON")

	tokenRmCmd.Flags().Bool("json", false, "output as JSON")
}

// tokenProfileJSON is the JSON representation of a token profile.
type tokenProfileJSON struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Status    string `json:"status"`
}

func ensureVault() {
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}
}

func validateTokenProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if _, ok := tools[provider]; !ok {
		return "", fmt.Errorf("unknown provider: %s (supported: %s)", provider, supportedToolsList())
	}
	return provider, nil
}

// readTokenFromInput reads a token from stdin (piped) or via a hidden
// interactive prompt.
func readTokenFromInput(in io.Reader) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(os.Stderr, "Paste token (input hidden): ")
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	data, err := io.ReadAll(io.LimitReader(in, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func runTokenAdd(cmd *cobra.Command, args []string) error {
	ensureVault()

	provider, err := validateTokenProvider(args[0])
	if err != nil {
		return err
	}
	name := args[1]

	jsonOutput, _ := cmd.Flags().GetBool("json")
	force, _ := cmd.Flags().GetBool("force")
	noVerify, _ := cmd.Flags().GetBool("no-verify")

	if !force {
		if existing, err := vault.List(provider); err == nil {
			for _, p := range existing {
				if p == name {
					return fmt.Errorf("profile %s/%s already exists (use --force to replace)", provider, name)
				}
			}
		}
	}

	token, err := readTokenFromInput(cmd.InOrStdin())
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("no token provided on stdin")
	}

	if !noVerify {
		if err := authfile.ValidateTokenFormat(provider, token); err != nil {
			return fmt.Errorf("token failed format check: %w (use --no-verify to store anyway)", err)
		}
	}

	if err := vault.SaveTokenProfile(provider, name, token, "stdin"); err != nil {
		return err
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"success":  true,
			"provider": provider,
			"name":     name,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Saved token profile %s/%s\n", provider, name)
	fmt.Fprintf(cmd.OutOrStdout(), "  Activate it with: caam activate %s %s\n", provider, name)
	return nil
}

// defaultTokenImportDir is the conventional location of pre-existing token
// files minted with `claude setup-token`.
func defaultTokenImportDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "veup")
}

// tokenImportCandidate is one importable token file found by the scanner.
type tokenImportCandidate struct {
	Provider string
	Name     string
	Path     string
}

// scanTokenImportDir finds importable token files in dir. Recognized pattern:
// claude-<name>-token.
func scanTokenImportDir(dir string) ([]tokenImportCandidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []tokenImportCandidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		if !strings.HasPrefix(base, "claude-") || !strings.HasSuffix(base, "-token") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(base, "claude-"), "-token")
		if name == "" {
			continue
		}
		out = append(out, tokenImportCandidate{
			Provider: "claude",
			Name:     name,
			Path:     filepath.Join(dir, base),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// tokenImportResult records the outcome for one candidate.
type tokenImportResult struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Action   string `json:"action"` // imported, skipped, error
	Error    string `json:"error,omitempty"`
}

func runTokenImport(cmd *cobra.Command, args []string) error {
	ensureVault()

	dir, _ := cmd.Flags().GetString("dir")
	force, _ := cmd.Flags().GetBool("force")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if dir == "" {
		dir = defaultTokenImportDir()
	}
	if dir == "" {
		return fmt.Errorf("cannot determine import directory (no home dir); use --dir")
	}

	candidates, err := scanTokenImportDir(dir)
	if err != nil {
		return fmt.Errorf("scan %s: %w", dir, err)
	}

	var results []tokenImportResult
	imported := 0
	for _, c := range candidates {
		res := tokenImportResult{Provider: c.Provider, Name: c.Name, Source: c.Path}

		if !force && vault.IsTokenProfile(c.Provider, c.Name) {
			res.Action = "skipped"
			res.Error = "profile already exists (use --force to overwrite)"
			results = append(results, res)
			continue
		}

		data, err := os.ReadFile(c.Path)
		if err != nil {
			res.Action = "error"
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		token := strings.TrimSpace(string(data))
		if err := authfile.ValidateTokenFormat(c.Provider, token); err != nil {
			res.Action = "error"
			res.Error = fmt.Sprintf("format check failed: %v", err)
			results = append(results, res)
			continue
		}
		if err := vault.SaveTokenProfile(c.Provider, c.Name, token, c.Path); err != nil {
			res.Action = "error"
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		res.Action = "imported"
		imported++
		results = append(results, res)
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"dir":      dir,
			"imported": imported,
			"results":  results,
		})
	}

	out := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintf(out, "No importable token files found in %s\n", dir)
		fmt.Fprintf(out, "  (looking for claude-<name>-token files)\n")
		return nil
	}
	for _, r := range results {
		switch r.Action {
		case "imported":
			fmt.Fprintf(out, "Imported %s/%s from %s\n", r.Provider, r.Name, r.Source)
		case "skipped":
			fmt.Fprintf(out, "Skipped %s/%s: %s\n", r.Provider, r.Name, r.Error)
		default:
			fmt.Fprintf(out, "Error %s/%s: %s\n", r.Provider, r.Name, r.Error)
		}
	}
	fmt.Fprintf(out, "%d imported\n", imported)
	if imported > 0 {
		fmt.Fprintf(out, "  Activate one with: caam activate claude <name>\n")
	}
	return nil
}

func runTokenLs(cmd *cobra.Command, args []string) error {
	ensureVault()

	jsonOutput, _ := cmd.Flags().GetBool("json")

	providers := supportedTools()
	if len(args) == 1 {
		p, err := validateTokenProvider(args[0])
		if err != nil {
			return err
		}
		providers = []string{p}
	}

	var rows []tokenProfileJSON
	for _, provider := range providers {
		names, err := vault.ListTokenProfiles(provider)
		if err != nil {
			continue
		}
		active, _ := vault.ActiveTokenProfile(provider)
		for _, name := range names {
			row := tokenProfileJSON{
				Provider: provider,
				Name:     name,
				Active:   name == active,
			}
			_, meta, err := vault.ReadTokenProfile(provider, name)
			if err != nil {
				row.Status = "unreadable"
			} else {
				row.Status = "ok"
				row.Source = meta.Source
				if !meta.CreatedAt.IsZero() {
					row.CreatedAt = meta.CreatedAt.Format("2006-01-02")
				}
			}
			rows = append(rows, row)
		}
	}

	if jsonOutput {
		if rows == nil {
			rows = []tokenProfileJSON{}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"profiles": rows,
			"count":    len(rows),
		})
	}

	out := cmd.OutOrStdout()
	if len(rows) == 0 {
		fmt.Fprintln(out, "No token profiles saved")
		fmt.Fprintln(out, "  Add one with: caam token add <provider> <name>")
		fmt.Fprintln(out, "  Or import existing token files: caam token import")
		return nil
	}
	fmt.Fprintf(out, "%-10s  %-20s  %-10s  %-12s  %s\n", "PROVIDER", "PROFILE", "STATUS", "ADDED", "SOURCE")
	for _, r := range rows {
		marker := "  "
		if r.Active {
			marker = "● "
		}
		fmt.Fprintf(out, "%s%-8s  %-20s  %-10s  %-12s  %s\n", marker, r.Provider, r.Name, r.Status, r.CreatedAt, r.Source)
	}
	return nil
}

func runTokenRm(cmd *cobra.Command, args []string) error {
	ensureVault()

	provider, err := validateTokenProvider(args[0])
	if err != nil {
		return err
	}
	name := args[1]
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if !vault.IsTokenProfile(provider, name) {
		return fmt.Errorf("%s/%s is not a token profile (use 'caam delete' for file-swap profiles)", provider, name)
	}

	// If this was the default token profile, clear the marker too.
	if active, _ := vault.ActiveTokenProfile(provider); active == name {
		_ = vault.ClearActiveTokenProfile(provider)
	}

	if err := vault.Delete(provider, name); err != nil {
		return err
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"success":  true,
			"provider": provider,
			"name":     name,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Deleted token profile %s/%s\n", provider, name)
	return nil
}

// claudeTokenProbeURL is the endpoint used for cheap active token validation.
// It is a var so tests can point it at a local server; the default is Claude's
// OAuth usage endpoint, which validates the token without consuming quota.
var claudeTokenProbeURL = usage.ClaudeUsageURL

// probeToken actively validates a token with a single cheap API call.
// Returns (valid, nil) on a conclusive answer; a non-nil error means the
// probe was inconclusive (network problem, unexpected status) and says
// nothing about the token.
func probeToken(ctx context.Context, tool, token string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "claude":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeTokenProbeURL, nil)
		if err != nil {
			return false, fmt.Errorf("create probe request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-beta", usage.ClaudeAPIBeta)
		req.Header.Set("User-Agent", usage.ClaudeUserAgent)
		req.Header.Set("Accept", "application/json")

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return false, fmt.Errorf("probe request failed: %w", err)
		}
		defer resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return true, nil
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return false, nil
		default:
			return false, fmt.Errorf("inconclusive probe: status %d", resp.StatusCode)
		}
	default:
		return false, fmt.Errorf("active validation not supported for %s token profiles", tool)
	}
}

// tokenProfileHealth builds a ProfileHealth for a token profile from the
// stored health record enriched with a passive token check: missing or
// malformed tokens degrade the profile. No network calls are made.
func tokenProfileHealth(tool, name string) (*health.ProfileHealth, health.HealthStatus) {
	var ph *health.ProfileHealth
	if healthStore != nil {
		ph, _ = healthStore.GetProfile(tool, name)
	}
	if ph == nil {
		ph = &health.ProfileHealth{}
	}

	token, _, err := vault.ReadTokenProfile(tool, name)
	if err != nil {
		return ph, health.StatusCritical
	}
	if err := authfile.ValidateTokenFormat(tool, token); err != nil {
		return ph, health.StatusWarning
	}
	// Long-lived tokens carry no expiry metadata, which the generic scorer
	// treats as neutral (and thus below the healthy threshold). A token that
	// passed the format check with no recorded errors or penalty is healthy.
	status := health.CalculateStatus(ph)
	if status == health.StatusWarning && ph.ErrorCount1h == 0 && ph.Penalty == 0 {
		status = health.StatusHealthy
	}
	return ph, status
}
