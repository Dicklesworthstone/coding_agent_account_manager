package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/spf13/cobra"
)

const isolatedBackupWarning = "This is a snapshot, not an independent OAuth login. Keep the isolated session stopped while using the vault copy; refreshing either copy can invalidate the other. No tokens were refreshed."

func init() {
	profileCmd.AddCommand(newProfileBackupCommand())
	configureBackupSource(backupCmd)
}

// configureBackupSource adds isolated source selection to the ordinary backup
// command. Keep the native backup implementation and its startup behavior when
// the flag is absent. An explicitly empty flag must fail, never fall back to
// reading the live account. Both isolated interfaces use the same store API.
func configureBackupSource(cmd *cobra.Command) {
	cmd.Flags().String("from-profile", "", "Read an isolated Claude profile instead of the live login; destination must be new")
	cmd.Long += `

To save an isolated Claude login without touching the live account:
  caam backup claude new-vault-name --from-profile isolated-name

Finish /login and exit the isolated session first. Existing vault names are
never overwritten in this mode. The source is retained unchanged; this is a
snapshot, not an independent OAuth login. Keep the isolated session stopped
while using the vault copy because refreshing either can invalidate the other.`
	liveRun := cmd.RunE
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("from-profile") {
			// Do not run migrations or live token/keychain warnings, even for
			// an invalid source, unsupported tool, or malformed destination.
			return nil
		}
		root := cmd.Root()
		if root != cmd && root.PersistentPreRunE != nil {
			return root.PersistentPreRunE(cmd, args)
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("from-profile") {
			return liveRun(cmd, args)
		}
		source, err := cmd.Flags().GetString("from-profile")
		if err != nil {
			return err
		}
		return runIsolatedVaultBackup(cmd, args[0], source, args[1])
	}
}

func newProfileBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup <tool> <profile-name>",
		Short: "Save an isolated Claude login to a new vault entry without switching accounts",
		Long: `Save an existing isolated Claude OAuth login to the account vault.

Unlike 'caam backup' without --from-profile, this command reads the named
isolated profile, not the currently active login. It captures .credentials.json,
.claude.json, settings.json and auth.json from that profile's xdg_config/claude-code
directory. It does not clear, replace, refresh or activate the live Claude login.

Finish /login and exit the isolated session first. Other agents using the live
login may keep running. Existing vault names are never overwritten; use --as to
choose a different name. The isolated profile is retained unchanged.

Example:
  caam profile add claude second
  caam exec claude second
  # Inside Claude: /login, then /exit
  caam profile backup claude second
  caam profile backup claude second --as second-snapshot
  # Equivalent through the ordinary backup command:
  caam backup claude second-snapshot --from-profile second
  # Later, when you intend to switch the live login:
  caam activate claude second

The two saved copies share an OAuth grant, not independent refresh tokens.
Do not keep using the isolated session after handing its snapshot to the vault.
'caam auth import' creates isolated profiles; this command creates vault entries.`,
		Args: cobra.ExactArgs(2),
		// The root hook runs live token warnings (which may mirror a keychain)
		// and legacy store migration. Neither belongs in a read-only-source
		// workflow. Initialize only the two stores below, without that hook.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
		RunE:              runProfileBackup,
	}
	cmd.Flags().String("as", "", "New vault profile name (defaults to the isolated profile name)")
	cmd.Flags().Bool("json", false, "Output a JSON result")
	return cmd
}

type profileBackupResult struct {
	Success       bool     `json:"success"`
	Tool          string   `json:"tool"`
	Profile       string   `json:"profile"`
	SourceProfile string   `json:"source_profile"`
	Path          string   `json:"path,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	Error         string   `json:"error,omitempty"`
}

func runProfileBackup(cmd *cobra.Command, args []string) error {
	target, err := cmd.Flags().GetString("as")
	if err != nil {
		return err
	}
	if !cmd.Flags().Changed("as") {
		target = args[1]
	}
	return runIsolatedVaultBackup(cmd, args[0], args[1], target)
}

func runIsolatedVaultBackup(cmd *cobra.Command, tool, source, target string) error {
	jsonOutput, err := cmd.Flags().GetBool("json")
	if err != nil {
		return err
	}
	tool = strings.ToLower(strings.TrimSpace(tool))
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	result := profileBackupResult{Tool: tool, Profile: target, SourceProfile: source}

	backup := func() error {
		if tool != "claude" {
			return fmt.Errorf("isolated-to-vault backup currently supports only claude")
		}
		store := profile.NewStore(profile.DefaultStorePath())
		vault := authfile.NewVault(authfile.DefaultVaultPath())
		if err := store.BackupClaudeToVault(vault, source, target); err != nil {
			return err
		}
		result.Success = true
		result.Path = vault.ProfilePath("claude", target)
		result.Warnings = []string{isolatedBackupWarning}
		return nil
	}
	err = backup()
	if err != nil {
		result.Error = err.Error()
	}
	if jsonOutput {
		// Match ordinary backup's machine-readable failure contract: one JSON
		// result and a nonzero exit, without Cobra appending usage or errors.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(result))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Saved isolated claude/%s to vault claude/%s. The live login was not changed.\n", source, target)
	fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", isolatedBackupWarning)
	return nil
}
