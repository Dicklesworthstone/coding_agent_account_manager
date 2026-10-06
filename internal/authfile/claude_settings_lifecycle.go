package authfile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

// All vault callers (CLI, robot, API, TUI, wrap, workspace and smart runner)
// resolve the same policy here, rather than opting into it at each call site.
func claudeSettingsPolicy() (claudesettings.Policy, error) {
	return claudesettings.LoadPolicy()
}

func isClaudeSettingsDocument(tool, path string) bool {
	return isClaudeUserSettings(tool, path) || (tool == "claude" && filepath.Base(path) == ".claude.json")
}

type claudeSettingsSnapshot struct {
	data    []byte
	hasAuth bool
}

func readClaudeSettingsSnapshot(path string, policy claudesettings.Policy) (claudeSettingsSnapshot, error) {
	data, err := claudesettings.Read(path)
	if err != nil {
		return claudeSettingsSnapshot{}, err
	}
	_, err = claudesettings.Identity(data, policy)
	if filepath.Base(path) == ".claude.json" {
		_, err = claudesettings.LegacyIdentity(data)
	}
	if err != nil {
		return claudeSettingsSnapshot{}, fmt.Errorf("parse Claude settings %s: %w", path, err)
	}
	hasAuth, err := claudeCredentialMaterial(data, filepath.Base(path))
	if err != nil {
		return claudeSettingsSnapshot{}, fmt.Errorf("parse Claude settings %s: %w", path, err)
	}
	return claudeSettingsSnapshot{data: data, hasAuth: hasAuth}, nil
}

// readClaudeSettingsForBackup validates settings before any snapshot files are
// changed. Full settings remain in backups for first-use/recovery, but an
// absent live file must remove a previous snapshot, not revive deleted auth.
func readClaudeSettingsForBackup(fileSet AuthFileSet) (map[string]claudeSettingsSnapshot, error) {
	var result map[string]claudeSettingsSnapshot
	for _, spec := range fileSet.Files {
		if !isClaudeSettingsDocument(fileSet.Tool, spec.Path) {
			continue
		}
		policy, err := claudeSettingsPolicy()
		if err != nil {
			return nil, err
		}
		snapshot, err := readClaudeSettingsSnapshot(spec.Path, policy)
		if err != nil {
			return nil, err
		}
		if result == nil {
			result = make(map[string]claudeSettingsSnapshot)
		}
		result[spec.Path] = snapshot
	}
	return result, nil
}

type claudeSettingsRestore struct {
	update  *claudesettings.Update
	hasAuth bool
}

// prepareClaudeSettingsRestore runs before any credentials or keychain mirror
// are changed. Missing settings snapshots still produce a plan: their absence
// means the target has no settings auth, so outgoing helpers/env must go away.
func prepareClaudeSettingsRestore(fileSet AuthFileSet, profileDir string) (map[string]claudeSettingsRestore, error) {
	var result map[string]claudeSettingsRestore
	for _, spec := range fileSet.Files {
		if !isClaudeSettingsDocument(fileSet.Tool, spec.Path) {
			continue
		}
		policy, err := claudeSettingsPolicy()
		if err != nil {
			return nil, err
		}
		snapshotPath := filepath.Join(profileDir, filepath.Base(spec.Path))
		prepare := claudesettings.PrepareRestore
		if filepath.Base(spec.Path) == ".claude.json" {
			prepare = claudesettings.PrepareLegacyRestore
		}
		update, err := prepare(snapshotPath, spec.Path, policy)
		if err != nil {
			return nil, fmt.Errorf("prepare Claude settings restore: %w", err)
		}
		snapshot, err := readClaudeSettingsSnapshot(snapshotPath, policy)
		if err != nil {
			return nil, err
		}
		if result == nil {
			result = make(map[string]claudeSettingsRestore)
		}
		result[spec.Path] = claudeSettingsRestore{update: update, hasAuth: snapshot.hasAuth}
	}
	if len(result) == 0 {
		return result, nil
	}

	// Do not scrub the outgoing account for an empty/incomplete target. Shared
	// policy alone is not an optional-only authentication artifact.
	required, optional := false, false
	var missingRequired string
	for _, spec := range fileSet.Files {
		path := filepath.Join(profileDir, filepath.Base(spec.Path))
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			if spec.Required && missingRequired == "" {
				missingRequired = path
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat snapshot %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshot is not a regular file: %s", path)
		}
		if plan, ok := result[spec.Path]; ok && !plan.hasAuth {
			if spec.Required && missingRequired == "" {
				missingRequired = path
			}
			continue
		}
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			if _, ok, err := claudeDesktopTokenCache(path); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
		}
		if spec.Required {
			required = true
		} else {
			optional = true
		}
	}
	if missingRequired != "" && !(fileSet.AllowOptionalOnly && !required && optional) {
		return nil, fmt.Errorf("required backup not found: %s", missingRequired)
	}
	if !required && !optional {
		return nil, fmt.Errorf("no auth files restored for %s", fileSet.Tool)
	}
	return result, nil
}

func clearClaudeUserSettings(fileSet AuthFileSet) error {
	var updates []*claudesettings.Update
	for _, spec := range fileSet.Files {
		if !isClaudeSettingsDocument(fileSet.Tool, spec.Path) {
			continue
		}
		policy, err := claudeSettingsPolicy()
		if err != nil {
			return err
		}
		prepare := claudesettings.PrepareClear
		if filepath.Base(spec.Path) == ".claude.json" {
			prepare = claudesettings.PrepareLegacyClear
		}
		update, err := prepare(spec.Path, policy)
		if err != nil {
			return fmt.Errorf("prepare Claude settings logout: %w", err)
		}
		updates = append(updates, update)
	}
	for _, update := range updates {
		if err := update.Apply(); err != nil {
			return err
		}
	}
	return nil
}

func hasClaudeUserSettingsAuth(path string) bool {
	policy, err := claudeSettingsPolicy()
	if err != nil {
		return false
	}
	snapshot, err := readClaudeSettingsSnapshot(path, policy)
	return err == nil && snapshot.hasAuth
}
