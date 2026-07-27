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

Endpoint profiles are the second member of the family: they carry a service
endpoint URL plus an optional bearer token, created with --endpoint (alias
--base-url):

  ollama  injects OLLAMA_HOST (no auth)
  quick   injects VITE_AGENT_WS_URL + VITE_INSTANCE_TOKEN (Amazon Quick's
          local desktop agent)
  claude  with --base-url injects ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN
          (+ CLAUDE_CONFIG_DIR isolation) for anthropic-compatible endpoints
          such as GLM or Moonshot/Kimi

Examples:
  claude setup-token | caam token add claude work    # pipe a fresh token
  caam token add claude personal                     # paste interactively
  caam token add deepseek main                       # DeepSeek API key
  caam token add ollama local                        # default endpoint
  caam token add ollama gpu --endpoint http://gpu-box:11434
  caam token add quick desktop                       # paste VITE_INSTANCE_TOKEN
  caam token add claude glm --base-url https://api.z.ai/api/anthropic
  caam token import                                  # import existing token files
  caam token ls                                      # list token profiles
  caam token rm claude old                           # delete a token profile`,
}

var tokenAddCmd = &cobra.Command{
	Use:   "add <provider> <name>",
	Short: "Add a token or endpoint profile (token read from stdin or interactive paste)",
	Long: `Stores a token as a new token profile in the vault (mode 0600).

The token is read from stdin when piped, or prompted for (hidden input) when
run interactively. The token itself is never accepted as a command-line
argument so it cannot leak into shell history or process listings.

With --endpoint (alias --base-url) an ENDPOINT profile is stored instead:
the endpoint URL plus, when the provider requires auth, a bearer token from
stdin as above. Providers with a well-known default endpoint (ollama, quick)
may omit the flag; claude endpoint profiles (anthropic-compatible services
like GLM or Moonshot/Kimi) must always name their base URL.

Examples:
  claude setup-token | caam token add claude work
  caam token add claude personal   # prompts for a hidden paste
  caam token add ollama local      # endpoint profile, default endpoint
  caam token add claude kimi --base-url https://api.moonshot.ai/anthropic`,
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
	tokenAddCmd.Flags().String("endpoint", "", "endpoint URL: store an endpoint profile instead of a plain token profile")
	tokenAddCmd.Flags().String("base-url", "", "alias for --endpoint (anthropic-compatible base URL for claude)")

	tokenImportCmd.Flags().String("dir", "", "directory to scan (default ~/.config/veup)")
	tokenImportCmd.Flags().Bool("force", false, "overwrite existing profiles")
	tokenImportCmd.Flags().Bool("json", false, "output as JSON")

	tokenLsCmd.Flags().Bool("json", false, "output as JSON")

	tokenRmCmd.Flags().Bool("json", false, "output as JSON")
}

// tokenProfileJSON is the JSON representation of a token/endpoint profile.
type tokenProfileJSON struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Endpoint  string `json:"endpoint,omitempty"`
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
	endpoint, _ := cmd.Flags().GetString("endpoint")
	if endpoint == "" {
		endpoint, _ = cmd.Flags().GetString("base-url")
	}

	// Decide the profile kind. An explicit --endpoint/--base-url always means
	// an endpoint profile; endpoint-native providers (those with a well-known
	// default endpoint, e.g. ollama and quick) store endpoint profiles even
	// without the flag. claude has no default endpoint, so plain token
	// profiles remain its default and --base-url opts into the
	// anthropic-compatible endpoint variant.
	spec, endpointCapable := authfile.EndpointSpecFor(provider)
	isEndpoint := endpoint != ""
	if endpointCapable && spec.DefaultEndpoint != "" {
		isEndpoint = true
		if endpoint == "" {
			endpoint = spec.DefaultEndpoint
		}
	}
	if isEndpoint && !endpointCapable {
		return fmt.Errorf("endpoint profiles are not supported for %s (supported: claude, ollama, quick)", provider)
	}

	if !force {
		if existing, err := vault.List(provider); err == nil {
			for _, p := range existing {
				if p == name {
					return fmt.Errorf("profile %s/%s already exists (use --force to replace)", provider, name)
				}
			}
		}
	}

	// Endpoint profiles for auth-less providers (ollama) take no token, so
	// nothing is read from stdin for them.
	needToken := !isEndpoint || spec.TokenRequired
	var token string
	if needToken {
		token, err = readTokenFromInput(cmd.InOrStdin())
		if err != nil {
			return err
		}
		if token == "" {
			return fmt.Errorf("no token provided on stdin")
		}
	}

	if isEndpoint {
		if err := authfile.ValidateEndpointURL(provider, endpoint); err != nil {
			return err
		}
		if err := vault.SaveEndpointProfile(provider, name, endpoint, token, "stdin"); err != nil {
			return err
		}
	} else {
		if !noVerify {
			if err := authfile.ValidateTokenFormat(provider, token); err != nil {
				return fmt.Errorf("token failed format check: %w (use --no-verify to store anyway)", err)
			}
		}
		if err := vault.SaveTokenProfile(provider, name, token, "stdin"); err != nil {
			return err
		}
	}

	if jsonOutput {
		out := map[string]interface{}{
			"success":  true,
			"provider": provider,
			"name":     name,
		}
		if isEndpoint {
			out["type"] = authfile.ProfileTypeEndpoint
			out["endpoint"] = endpoint
		} else {
			out["type"] = authfile.ProfileTypeToken
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if isEndpoint {
		fmt.Fprintf(cmd.OutOrStdout(), "Saved endpoint profile %s/%s (%s)\n", provider, name, endpoint)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Saved token profile %s/%s\n", provider, name)
	}
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
				Type:     authfile.ProfileTypeToken,
				Active:   name == active,
			}
			_, meta, err := vault.ReadTokenProfile(provider, name)
			if err != nil {
				row.Status = "unreadable"
			} else {
				row.Status = "ok"
				row.Type = meta.Type
				row.Endpoint = meta.Endpoint
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
	fmt.Fprintf(out, "%-10s  %-20s  %-10s  %-10s  %-12s  %s\n", "PROVIDER", "PROFILE", "TYPE", "STATUS", "ADDED", "ENDPOINT/SOURCE")
	for _, r := range rows {
		marker := "  "
		if r.Active {
			marker = "● "
		}
		detail := r.Source
		if r.Endpoint != "" {
			detail = r.Endpoint
		}
		fmt.Fprintf(out, "%s%-8s  %-20s  %-10s  %-10s  %-12s  %s\n", marker, r.Provider, r.Name, r.Type, r.Status, r.CreatedAt, detail)
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

// endpointProbeTimeout bounds the reachability probes for endpoint profiles.
// Local services (ollama, the Amazon Quick desktop agent) answer in
// milliseconds when up; anything slower is as good as down.
const endpointProbeTimeout = 3 * time.Second

// probeProfile actively validates an env-injection profile. Token profiles
// get a single cheap authenticated API call; endpoint profiles get a cheap
// reachability probe of their stored endpoint. Returns (valid, nil) on a
// conclusive answer; a non-nil error means the probe was inconclusive and
// says nothing about the profile.
func probeProfile(ctx context.Context, tool, token string, meta *authfile.TokenMeta) (bool, error) {
	if meta != nil && meta.Type == authfile.ProfileTypeEndpoint {
		switch strings.ToLower(strings.TrimSpace(tool)) {
		case "ollama":
			// GET <endpoint>/api/tags is Ollama's canonical liveness check.
			return probeHTTPReachable(ctx, strings.TrimSuffix(meta.Endpoint, "/")+"/api/tags")
		case "quick":
			// The Quick agent speaks WebSocket; any HTTP answer from its port
			// (including an upgrade/auth rejection) proves the instance is up.
			return probeHTTPReachable(ctx, wsToHTTP(meta.Endpoint))
		default:
			// Anthropic-compatible endpoints (claude --base-url) have no
			// uniformly cheap, quota-free probe; passive verdict stands.
			return false, fmt.Errorf("active validation not supported for %s endpoint profiles", tool)
		}
	}
	return probeToken(ctx, tool, token)
}

// probeHTTPReachable reports whether an HTTP GET of url receives ANY response
// within the probe timeout. Every HTTP status — including 4xx/5xx — counts as
// reachable: the probe asserts the service is up, not that the request is
// authorized. Connection errors mean down (false, nil); only a malformed URL
// is inconclusive.
func probeHTTPReachable(ctx context.Context, url string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("create probe request: %w", err)
	}
	client := &http.Client{Timeout: endpointProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// Connection refused/timeout: conclusive "not reachable".
		return false, nil
	}
	defer resp.Body.Close()
	return true, nil
}

// wsToHTTP maps a WebSocket endpoint to its HTTP origin for reachability
// probing (ws → http, wss → https); http(s) URLs pass through unchanged.
func wsToHTTP(endpoint string) string {
	switch {
	case strings.HasPrefix(endpoint, "ws://"):
		return "http://" + strings.TrimPrefix(endpoint, "ws://")
	case strings.HasPrefix(endpoint, "wss://"):
		return "https://" + strings.TrimPrefix(endpoint, "wss://")
	default:
		return endpoint
	}
}

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

	token, meta, err := vault.ReadTokenProfile(tool, name)
	if err != nil {
		return ph, health.StatusCritical
	}
	if err := authfile.ValidateProfileToken(tool, token, meta); err != nil {
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
