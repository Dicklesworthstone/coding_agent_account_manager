package shallow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

// .claude.json mixes workflow preferences with account and project session
// state. Enrollment strips the known account keys from a real-home seed; an
// explicit source is the selected account's own snapshot. Refresh uses the
// same claudesettings lifecycle as vault and isolated launches, including
// policy overrides and removal of revoked project approvals.

// claudeAccountKeys are stripped when seeding from the real HOME. The userID
// installation identifier is deliberately shared, not an account identifier.
var claudeAccountKeys = claudesettings.LegacyAccountKeys()

// Keep enrollment's account/shared invariant tied to the common classifier,
// not a second independently maintained list of refreshable preferences.
var claudeSharedPreferenceKeys = claudesettings.LegacySharedPolicyKeys()

// seedClaudeJSONFromRealHome returns the bytes to write as a fresh profile's
// .claude.json when the seed is the user's real ~/.claude.json: the file with
// known and configured private fields removed by the common classifier.
// Preserve the legacy non-object seed behavior; launch validation rejects it.
func seedClaudeJSONFromRealHome(src string) ([]byte, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return nil, err
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil || state == nil {
		return raw, nil
	}
	return claudesettings.SeedLegacy(raw, policy)
}

// SyncClaudeConfig prepares both Claude documents before writing either one.
// The common lifecycle preserves profile auth/session state and refreshes only
// shared workflow policy, including deletions and configurable private keys.
func (m *Manager) SyncClaudeConfig(name string) ([]string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return nil, err
	}
	provider, err := m.ResolveProvider(name)
	if err != nil {
		return nil, err
	}
	if provider != "claude" {
		return nil, nil
	}
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return nil, err
	}
	settings, err := m.prepareClaudeSettings(home, policy)
	if err != nil {
		return nil, err
	}
	legacy, changed, err := m.prepareClaudeJSON(home, policy)
	if err != nil {
		return nil, err
	}
	if err := claudesettings.ApplyUpdates([]*claudesettings.Update{settings, legacy}); err != nil {
		return nil, fmt.Errorf("write private Claude configuration: %w", err)
	}
	if settings.Changed() {
		changed = append(changed, ".claude/settings.json")
	}
	sort.Strings(changed)
	return changed, nil
}

// EnsureClaudeSettingsPrivate repairs old host-settings symlinks even when the
// caller opts out of policy refresh. Both documents must be valid and private
// before any repair is applied; --no-sync-config is not an auth-isolation bypass.
func (m *Manager) EnsureClaudeSettingsPrivate(name string) error {
	home, err := m.HomeFor(name)
	if err != nil {
		return err
	}
	provider, err := m.ResolveProvider(name)
	if err != nil || provider != "claude" {
		return err
	}
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return err
	}
	sharedSettings, sharedLegacy := claudesettings.SharedPaths(m.realHome)
	legacy, err := claudesettings.PreparePrivate(
		filepath.Join(home, ".claude.json"), sharedLegacy, filepath.Join(m.realHome, ".claude.json"),
	)
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "settings.json")
	account, err := m.claudeSettingsAccountPath(home)
	if err != nil {
		return err
	}
	var settings *claudesettings.Update
	if account == "" {
		settings, err = m.prepareClaudeSettings(home, policy)
	} else {
		settings, err = claudesettings.PreparePrivate(
			path, sharedSettings, filepath.Join(m.realHome, ".claude", "settings.json"),
		)
	}
	if err != nil {
		return err
	}
	return claudesettings.ApplyUpdates([]*claudesettings.Update{settings, legacy})
}

func (m *Manager) writeClaudeSettings(home string, opts CreateOptions) error {
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return err
	}
	shared, _ := claudesettings.SharedPaths(m.realHome)
	account := opts.ExtraSources[".claude/settings.json"]
	settings, err := claudesettings.PrepareImport(shared, account, filepath.Join(home, ".claude", "settings.json"), policy)
	if err != nil {
		return fmt.Errorf("prepare private Claude settings: %w", err)
	}
	return settings.Apply()
}

func (m *Manager) prepareClaudeSettings(home string, policy claudesettings.Policy) (*claudesettings.Update, error) {
	account, err := m.claudeSettingsAccountPath(home)
	if err != nil {
		return nil, err
	}
	shared, _ := claudesettings.SharedPaths(m.realHome)
	update, err := claudesettings.PrepareImport(shared, account, filepath.Join(home, ".claude", "settings.json"), policy)
	if err != nil {
		return nil, err
	}
	if account == "" {
		update.RequirePrivateFile()
	}
	return update, nil
}

// A historical link to the real user's settings has no profile-owned auth.
// Detach only those recognized links; never treat their helpers as this login's.
func (m *Manager) claudeSettingsAccountPath(home string) (string, error) {
	dir := filepath.Join(home, ".claude")
	info, err := os.Lstat(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("Claude config directory must be private: %s", dir)
	}
	path := filepath.Join(dir, "settings.json")
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	shared, _ := claudesettings.SharedPaths(m.realHome)
	for _, source := range []string{shared, filepath.Join(m.realHome, ".claude", "settings.json")} {
		if sourceInfo, serr := os.Stat(source); serr == nil {
			if targetInfo, terr := os.Stat(path); terr == nil && os.SameFile(sourceInfo, targetInfo) {
				return "", nil
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, rerr := os.Readlink(path)
			if rerr != nil {
				return "", rerr
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(dir, target)
			}
			if filepath.Clean(target) == filepath.Clean(source) {
				return "", nil
			}
		}
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Claude settings must be a private regular file: %s", path)
	}
	return path, nil
}

// prepareClaudeJSON uses the vault/isolated merger but keeps the shallow CLI's
// field-level change report. Preparing is read-only, including validation of
// the profile's private path and any malformed shared project maps.
func (m *Manager) prepareClaudeJSON(home string, policy claudesettings.Policy) (*claudesettings.Update, []string, error) {
	_, realState := claudesettings.SharedPaths(m.realHome)
	profilePath := filepath.Join(home, ".claude.json")
	if err := claudesettings.CheckPrivate(profilePath, realState); err != nil {
		return nil, nil, err
	}
	update, err := claudesettings.PrepareLegacyRefresh(realState, profilePath, policy)
	if err != nil {
		return nil, nil, err
	}
	changed, err := update.ChangedKeys()
	if err != nil {
		return nil, nil, err
	}
	return update, changed, nil
}

func (m *Manager) syncClaudeJSON(home string, policy claudesettings.Policy) ([]string, error) {
	update, changed, err := m.prepareClaudeJSON(home, policy)
	if err != nil {
		return nil, err
	}
	if err := update.Apply(); err != nil {
		return nil, fmt.Errorf("write profile .claude.json: %w", err)
	}
	return changed, nil
}

// rawJSONEqual compares two JSON values ignoring insignificant whitespace.
// A nil/empty side is only equal to another nil/empty side.
func rawJSONEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// marshalClaudeJSON renders a .claude.json object the way Claude Code writes
// it (two-space indent, trailing newline).
func marshalClaudeJSON(state map[string]json.RawMessage) ([]byte, error) {
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode .claude.json: %w", err)
	}
	return append(out, '\n'), nil
}
