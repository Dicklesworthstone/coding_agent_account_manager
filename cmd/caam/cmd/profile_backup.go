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
}

func newProfileBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup <tool> <profile-name>",
		Short: "Save an isolated Claude login to a new vault entry without switching accounts",
		Long: `Save an existing isolated Claude OAuth login to the account vault.

Unlike 'caam backup', this command reads the named isolated profile, not the
currently active login. It captures .credentials.json, .claude.json, settings.json
and auth.json from that profile's xdg_config/claude-code directory. It does not
clear, replace, refresh or activate the live Claude login.

Finish /login and exit the isolated session first. Other agents using the live
login may keep running. Existing vault names are never overwritten; use --as to
choose a different name. The isolated profile is retained unchanged.

Example:
  caam profile add claude second
  caam exec claude second
  # Inside Claude: /login, then /exit
  caam profile backup claude second
  caam profile backup claude second --as second-snapshot
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
	jsonOutput, err := cmd.Flags().GetBool("json")
	if err != nil {
		return err
	}
	target, err := cmd.Flags().GetString("as")
	if err != nil {
		return err
	}
	source := strings.TrimSpace(args[1])
	if !cmd.Flags().Changed("as") {
		target = source
	}
	target = strings.TrimSpace(target)
	result := profileBackupResult{Tool: args[0], Profile: target, SourceProfile: source}

	backup := func() error {
		if args[0] != "claude" {
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
		return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(result))
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Saved isolated claude/%s to vault claude/%s. The live login was not changed.\n", source, target)
	fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", isolatedBackupWarning)
	return nil
}
