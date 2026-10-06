package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keepalive"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newKeepaliveCmd())
}

func newKeepaliveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keepalive [claude|grok|live-profile ...]",
		Short: "Renew idle Claude and Grok logins in their live homes",
		Long: `Run each provider's native CLI against the live home that owns its OAuth
grant. Claude uses a minimal Haiku prompt; Grok uses its models command.
Credentials are re-read afterward, so a successful CLI exit alone is not success.

With no selectors, inspect all Claude and Grok live homes. Select a provider,
an unambiguous live profile name, or an exact reference:
  host:claude                 host:grok
  shallow:claude/alice         isolated:grok/work

A Claude shallow home owns the account in preference to its host copy.
Competing live owners and unknown identities are refused. Vault snapshots
are never executed; only a newer credential from the same live account may
update an existing user snapshot after renewal.

Examples:
  caam keepalive --dry-run --json
  caam keepalive claude grok
  caam keepalive shallow:claude/alice --ttl 1h
  caam keepalive host:grok --min-gap 0
  caam keepalive --print-systemd

--print-systemd prints a user service and a persistent half-hour timer for
manual installation. It neither installs a timer nor runs a provider CLI.`,
		SilenceUsage: true,
		// The ordinary root hook migrates data and may mirror keychain state.
		// Discovery and dry runs must be read-only and use the original live home.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE:              runKeepalive,
	}
	cmd.Flags().Duration("ttl", keepalive.DefaultTTL, "run when the access token has this much time left or less")
	cmd.Flags().Duration("min-gap", keepalive.DefaultMinGap, "minimum interval while a token remains valid (0 disables; expired tokens bypass it)")
	cmd.Flags().Duration("timeout", keepalive.DefaultTimeout, "maximum time for each native CLI call")
	cmd.Flags().String("base", "", "shallow profiles base directory (same default as shallow-spawn)")
	cmd.Flags().String("state-dir", "", "keepalive attempt state directory (default: CAAM data directory/keepalive)")
	cmd.Flags().Bool("dry-run", false, "report eligibility without running CLIs or writing files")
	cmd.Flags().Bool("json", false, "output structured results")
	cmd.Flags().Bool("print-systemd", false, "print a systemd user service and timer without installing them")
	return cmd
}

type keepaliveOutput struct {
	Success bool               `json:"success"`
	DryRun  bool               `json:"dry_run"`
	Results []keepalive.Result `json:"results"`
	Error   string             `json:"error,omitempty"`
}

func runKeepalive(cmd *cobra.Command, args []string) error {
	ttl, _ := cmd.Flags().GetDuration("ttl")
	gap, _ := cmd.Flags().GetDuration("min-gap")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	printSystemd, _ := cmd.Flags().GetBool("print-systemd")
	base, _ := cmd.Flags().GetString("base")
	stateDir, _ := cmd.Flags().GetString("state-dir")
	if ttl <= 0 || timeout <= 0 || gap < 0 {
		return finishKeepalive(cmd, dryRun, nil, errors.New("--ttl and --timeout must be positive; --min-gap must not be negative"))
	}
	if printSystemd {
		if dryRun {
			return finishKeepalive(cmd, dryRun, nil, errors.New("choose --dry-run or --print-systemd"))
		}
		return printKeepaliveSystemd(cmd, args)
	}
	localVault := authfile.NewVault(authfile.DefaultVaultPath())
	// Resolve every owner before selecting names. A narrow selector must not
	// conceal a second live home that could replay the same token family.
	grants, err := keepalive.Discover(cmd.Context(), keepalive.DiscoverOptions{
		ShallowBase: base, VaultPath: localVault.BasePath(),
	})
	if err != nil {
		return finishKeepalive(cmd, dryRun, nil, err)
	}
	selected, err := selectKeepaliveGrants(grants, args, localVault)
	if err != nil {
		return finishKeepalive(cmd, dryRun, nil, err)
	}
	if len(selected) == 0 {
		return finishKeepalive(cmd, dryRun, nil, errors.New("no Claude or Grok live logins found; sign in through the native CLI in its owning home"))
	}
	results, runErr := keepalive.Run(cmd.Context(), selected, keepalive.Options{
		TTL: ttl, MinGap: gap, DisableMinGap: gap == 0, Timeout: timeout,
		DryRun: dryRun, StateDir: stateDir,
		AfterRenew: func(ctx context.Context, grant keepalive.Grant, snapshot keepalive.CredentialSnapshot) ([]keepalive.SyncResult, error) {
			return keepalive.SyncVault(ctx, grant, snapshot, localVault)
		},
	})
	var failures []string
	for _, result := range results {
		if !result.Success {
			failures = append(failures, result.Ref+": "+result.Reason)
		}
	}
	if len(failures) > 0 {
		runErr = errors.Join(runErr, fmt.Errorf("keepalive could not renew %s", strings.Join(failures, "; ")))
	}
	return finishKeepalive(cmd, dryRun, results, runErr)
}

func finishKeepalive(cmd *cobra.Command, dryRun bool, results []keepalive.Result, runErr error) error {
	if results == nil {
		results = []keepalive.Result{}
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		// The error is part of the JSON contract, not a second Cobra error line.
		cmd.SilenceErrors = true
		output := keepaliveOutput{Success: runErr == nil, DryRun: dryRun, Results: results}
		if runErr != nil {
			output.Error = runErr.Error()
		}
		return errors.Join(runErr, json.NewEncoder(cmd.OutOrStdout()).Encode(output))
	}
	for _, result := range results {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s", result.Ref, result.Status)
		if result.Reason != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " (%s)", result.Reason)
		}
		fmt.Fprintln(cmd.OutOrStdout())
		for _, sync := range result.Sync {
			fmt.Fprintf(cmd.OutOrStdout(), "  vault %s: %s", sync.Profile, sync.Status)
			if sync.Reason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (%s)", sync.Reason)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
	}
	return runErr
}

func selectKeepaliveGrants(grants []keepalive.Grant, selectors []string, localVault *authfile.Vault) ([]keepalive.Grant, error) {
	if len(selectors) == 0 {
		return grants, nil
	}
	selected := make(map[string]bool)
	for _, selector := range selectors {
		if strings.HasPrefix(selector, "vault:") {
			provider, name, _ := strings.Cut(strings.TrimPrefix(selector, "vault:"), "/")
			return nil, refuseKeepaliveVault(localVault, provider, name, grants)
		}
		providerSelector := selector == "claude" || selector == "grok"
		var matches []keepalive.Grant
		for _, grant := range grants {
			if (providerSelector && grant.Provider == selector) || grant.Ref() == selector || (!providerSelector && !strings.ContainsAny(selector, ":/") && grant.Name == selector) {
				matches = append(matches, grant)
			}
		}
		if len(matches) == 0 {
			if localVault != nil && !strings.ContainsAny(selector, ":/\\") {
				for _, provider := range []string{"claude", "grok"} {
					names, err := localVault.List(provider)
					if err != nil {
						return nil, err
					}
					for _, name := range names {
						if name == selector {
							return nil, refuseKeepaliveVault(localVault, provider, name, grants)
						}
					}
				}
			}
			return nil, fmt.Errorf("no live grant matches %q; use claude, grok, or an exact host:, shallow:, or isolated: reference", selector)
		}
		if !providerSelector && len(matches) != 1 {
			refs := make([]string, 0, len(matches))
			for _, grant := range matches {
				refs = append(refs, grant.Ref())
			}
			return nil, fmt.Errorf("ambiguous live profile %q; select %s", selector, strings.Join(refs, " or "))
		}
		for _, grant := range matches {
			if !providerSelector && grant.Owner != "" {
				return nil, fmt.Errorf("%s does not own the live grant; use %s", grant.Ref(), grant.Owner)
			}
			selected[grant.Ref()] = true
		}
	}
	result := make([]keepalive.Grant, 0, len(selected))
	for _, grant := range grants {
		if selected[grant.Ref()] {
			result = append(result, grant)
		}
	}
	return result, nil
}

func refuseKeepaliveVault(localVault *authfile.Vault, provider, name string, grants []keepalive.Grant) error {
	message := fmt.Sprintf("vault:%s/%s is a saved snapshot and cannot be renewed", provider, name)
	id, err := keepalive.SnapshotIdentity(localVault, provider, name)
	if err == nil {
		owners := make(map[string]bool)
		for _, grant := range grants {
			if grant.Provider == provider && keepalive.SameAccount(id, grant.Identity) {
				ref := grant.Ref()
				if grant.Owner != "" {
					ref = grant.Owner
				}
				owners[ref] = true
			}
		}
		if len(owners) > 0 {
			refs := make([]string, 0, len(owners))
			for ref := range owners {
				refs = append(refs, ref)
			}
			sort.Strings(refs)
			return fmt.Errorf("%s; matching live homes: %s", message, strings.Join(refs, ", "))
		}
	}
	return fmt.Errorf("%s; no verified live owner was found, so sign in through the native CLI in the intended home", message)
}

type keepaliveSystemdOutput struct {
	Success bool   `json:"success"`
	Service string `json:"service"`
	Timer   string `json:"timer"`
}

func printKeepaliveSystemd(cmd *cobra.Command, selectors []string) error {
	for _, selector := range selectors {
		if strings.HasPrefix(selector, "vault:") {
			return finishKeepalive(cmd, false, nil, errors.New("keepalive timers cannot execute vault snapshots; select a live home"))
		}
	}
	bin, err := os.Executable()
	if err != nil {
		return finishKeepalive(cmd, false, nil, err)
	}
	args := []string{bin, "keepalive", "--json"}
	for _, name := range []string{"ttl", "min-gap", "timeout"} {
		value, _ := cmd.Flags().GetDuration(name)
		args = append(args, "--"+name, value.String())
	}
	for _, name := range []string{"base", "state-dir"} {
		value, _ := cmd.Flags().GetString(name)
		if value != "" {
			value, err = filepath.Abs(value)
			if err != nil {
				return finishKeepalive(cmd, false, nil, err)
			}
			args = append(args, "--"+name, value)
		}
	}
	args = append(args, "--")
	args = append(args, selectors...)
	// Preserve only location and executable-search settings. Auth overrides and
	// other ambient values must never be written into service files.
	env := make(map[string]string)
	for _, name := range []string{"HOME", "PATH", "CAAM_HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "CAAM_SHALLOW_HOMES_DIR", "CLAUDE_CONFIG_DIR", "GROK_HOME"} {
		value := os.Getenv(name)
		if value != "" && name != "PATH" {
			value, err = filepath.Abs(value)
			if err != nil {
				return finishKeepalive(cmd, false, nil, err)
			}
		}
		// Explicit empty overrides prevent a user manager's inherited value from
		// silently selecting a different credential home than this command.
		env[name] = value
	}
	service, timer, err := renderKeepaliveSystemd(args, env)
	if err != nil {
		return finishKeepalive(cmd, false, nil, err)
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(keepaliveSystemdOutput{Success: true, Service: service, Timer: timer})
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "# caam-keepalive.service\n%s\n# caam-keepalive.timer\n%s", service, timer)
	return err
}

func renderKeepaliveSystemd(args []string, env map[string]string) (string, string, error) {
	if len(args) == 0 || !filepath.IsAbs(args[0]) {
		return "", "", errors.New("systemd keepalive requires an absolute executable path")
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == ";" {
			return "", "", errors.New("use an exact live-home reference instead of a bare semicolon in systemd selectors")
		}
		value, err := quoteKeepaliveSystemd(arg, true)
		if err != nil {
			return "", "", err
		}
		quoted = append(quoted, value)
	}
	var service strings.Builder
	service.WriteString("[Unit]\nDescription=Renew idle CAAM live logins\n\n[Service]\nType=oneshot\nTimeoutStartSec=infinity\n")
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, err := quoteKeepaliveSystemd(key+"="+env[key], false)
		if err != nil {
			return "", "", err
		}
		fmt.Fprintf(&service, "Environment=%s\n", value)
	}
	fmt.Fprintf(&service, "ExecStart=%s\n", strings.Join(quoted, " "))
	timer := "[Unit]\nDescription=Check CAAM live logins every half hour\n\n[Timer]\nOnCalendar=*:0/30\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n"
	return service.String(), timer, nil
}

func quoteKeepaliveSystemd(value string, execArgument bool) (string, error) {
	if strings.ContainsFunc(value, unicode.IsControl) {
		return "", errors.New("systemd paths and settings must not contain control characters")
	}
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(value)
	if execArgument {
		value = strings.ReplaceAll(value, "$", "$$")
	}
	return `"` + value + `"`, nil
}
