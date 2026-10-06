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
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return err
	}
	updates, err := claudesettings.PrepareIsolatedSettings(
		claudesettings.SharedPath(home), claudeSettingsPathsForProfile(prof), policy,
	)
	if err != nil {
		return err
	}
	sharedLegacy := filepath.Join(home, ".claude.json")
	profileLegacy := filepath.Join(prof.HomePath(), ".claude.json")
	if err := claudesettings.CheckPrivate(profileLegacy, sharedLegacy); err != nil {
		return err
	}
	legacy, err := claudesettings.PrepareLegacyRefresh(sharedLegacy, profileLegacy, policy)
	if err != nil {
		return err
	}
	updates = append(updates, legacy)
	if err := ctx.Err(); err != nil {
		return err
	}
	return claudesettings.ApplyUpdates(updates)
}

var _ provider.ProfileRunPreparer = (*Provider)(nil)
