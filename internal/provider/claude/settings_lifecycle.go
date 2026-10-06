package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// PrepareRun brings live workflow policy into both legacy and XDG profile
// settings before execution. Auth is read only from this profile. In particular,
// creating a new profile must not import the real home's apiKeyHelper or env.
func (p *Provider) PrepareRun(ctx context.Context, prof *profile.Profile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if prof == nil {
		return fmt.Errorf("Claude profile is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if filepath.Clean(prof.HomePath()) == filepath.Clean(home) {
		return nil // Vault/global launches already use the live settings.
	}
	configDir, err := effectiveClaudeConfigDir(prof)
	if err != nil {
		return err
	}
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return err
	}
	sharedSettings, sharedLegacy := claudesettings.SharedPaths(home)
	updates, err := claudesettings.PrepareIsolatedSettings(
		sharedSettings, claudeSettingsPathsForProfile(prof), policy,
	)
	if err != nil {
		return err
	}
	profileLegacy := filepath.Join(prof.HomePath(), ".claude.json")
	if err := claudesettings.CheckPrivate(profileLegacy, sharedLegacy); err != nil {
		return err
	}
	legacy, err := claudesettings.PrepareLegacyRefresh(sharedLegacy, profileLegacy, policy)
	if err != nil {
		return err
	}
	// CLAUDE_CONFIG_DIR makes native session state live inside the selected
	// config directory. Only a legacy-only profile may seed it from its old
	// home-level state; an XDG login must not borrow another store's account.
	stateDestination := filepath.Join(configDir, ".claude.json")
	stateSource := stateDestination
	if configDir == claudeLegacyDirForProfile(prof) {
		stateSource = legacyClaudeStatePath(prof)
	}
	for _, path := range []string{stateSource, stateDestination} {
		if err := claudesettings.CheckPrivate(path, sharedLegacy); err != nil {
			return err
		}
	}
	nativeState, err := claudesettings.PrepareLegacyImport(sharedLegacy, stateSource, stateDestination, policy)
	if err != nil {
		return fmt.Errorf("prepare Claude native session state: %w", err)
	}
	// A legacy-only profile may use profileLegacy as nativeState's captured
	// account source. Consume that snapshot before refreshing it in place;
	// otherwise our own earlier write trips the source-change protection.
	// Both updates still preflight together and recheck their inputs on apply.
	updates = append(updates, nativeState, legacy)
	if err := ctx.Err(); err != nil {
		return err
	}
	return claudesettings.ApplyUpdates(updates)
}

var _ provider.ProfileRunPreparer = (*Provider)(nil)
