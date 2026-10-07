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

// readClaudeSettingsForBackup classifies every Claude source, including absence,
// before Backup changes the vault. Otherwise updating an OAuth profile after
// switching it to helper/env auth would retain its obsolete OAuth snapshots.
// Mixed settings are validated before touching the keychain mirror. The mirror
// is pulled before raw credentials are captured, so keychain-only logins are
// never mistaken for an absent source and removed from an existing backup.
func readClaudeSettingsForBackup(fileSet AuthFileSet) (map[string]claudeSettingsSnapshot, error) {
	if fileSet.Tool != "claude" {
		return nil, nil
	}
	policy, err := claudeSettingsPolicy()
	if err != nil {
		return nil, err
	}
	result := make(map[string]claudeSettingsSnapshot)
	for _, spec := range fileSet.Files {
		if !isClaudeSettingsDocument(fileSet.Tool, spec.Path) {
			continue
		}
		snapshot, err := readClaudeSettingsSnapshot(spec.Path, policy)
		if err != nil {
			return nil, err
		}
		result[spec.Path] = snapshot
	}
	if err := pullClaudeKeychain(fileSet); err != nil {
		return nil, err
	}
	for _, spec := range fileSet.Files {
		filename := filepath.Base(spec.Path)
		var fields []string
		switch {
		case filename == claudeCredentialsFile || filename == "auth.json":
			// Validate the complete raw credential, not just its access token.
		case isClaudeDesktopConfig(fileSet.Tool, spec.Path):
			fields = claudeDesktopTokenKeys
		default:
			continue
		}
		data, err := readClaudeBackupSource(spec.Path, fields)
		if err != nil {
			return nil, fmt.Errorf("capture Claude backup source %s: %w", spec.Path, err)
		}
		hasAuth, err := claudeCredentialMaterial(data, filename)
		if err != nil {
			return nil, fmt.Errorf("validate Claude backup source %s: %w", spec.Path, err)
		}
		if data != nil && !hasAuth {
			return nil, fmt.Errorf("%w: %s contains no access credential", ErrNoCredentials, spec.Path)
		}
		if fields == nil && data != nil {
			// Existing raw sources retain Backup's verbatim copy path, including
			// refreshed keychain bytes. Only their absence needs a new plan.
			continue
		}
		// A nil snapshot deliberately removes the previous vault file through
		// Backup's existing snapshot branch, rather than skipping that file.
		result[spec.Path] = claudeSettingsSnapshot{data: data, hasAuth: hasAuth}
	}
	return result, nil
}

type claudeSettingsRestore struct {
	// All planned files share one batch; hasAuth remains specific to this file
	// for Restore's required/optional-source accounting.
	update  interface{ Apply() error }
	hasAuth bool
}

// prepareClaudeSettingsRestore runs before any credentials or keychain mirror
// are changed. Missing settings snapshots still produce a plan: their absence
// means the target has no settings auth, so outgoing helpers/env must go away.
// The same rule applies to raw OAuth files and Desktop's token caches: leaving
// an absent source in place could authenticate as the account we just left.
func prepareClaudeSettingsRestore(fileSet AuthFileSet, profileDir string) (map[string]claudeSettingsRestore, error) {
	if fileSet.Tool != "claude" {
		return nil, nil
	}
	policy, err := claudeSettingsPolicy()
	if err != nil {
		return nil, err
	}
	var result map[string]claudeSettingsRestore
	for _, spec := range fileSet.Files {
		filename := filepath.Base(spec.Path)
		snapshotPath := filepath.Join(profileDir, filename)
		var plan claudeSettingsRestore
		switch {
		case isClaudeSettingsDocument(fileSet.Tool, spec.Path):
			prepare := claudesettings.PrepareRestore
			if filename == ".claude.json" {
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
			plan = claudeSettingsRestore{update: update, hasAuth: snapshot.hasAuth}
		case filename == claudeCredentialsFile || filename == "auth.json":
			retirement, err := prepareClaudeCredentialRetirement(snapshotPath, spec.Path, claudeRetirementMirror(fileSet, spec.Path))
			if err != nil {
				return nil, fmt.Errorf("prepare Claude credential retirement: %w", err)
			}
			if retirement == nil {
				continue // Keep the existing same-account freshness guard for real snapshots.
			}
			plan.update = retirement
		case isClaudeDesktopConfig(fileSet.Tool, spec.Path):
			update, err := claudesettings.PrepareFieldsRestore(snapshotPath, spec.Path, claudeDesktopTokenKeys)
			if err != nil {
				return nil, fmt.Errorf("prepare Claude Desktop cache restore: %w", err)
			}
			_, hasAuth, err := claudeDesktopTokenCache(snapshotPath)
			if err != nil {
				return nil, err
			}
			plan = claudeSettingsRestore{update: update, hasAuth: hasAuth}
		default:
			continue
		}
		if result == nil {
			result = make(map[string]claudeSettingsRestore)
		}
		result[spec.Path] = plan
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
	// Restore visits plans in file-set order. Do not let its first visit
	// retire the outgoing OAuth token before another document can be staged.
	batch := &claudeRestoreBatch{}
	for _, spec := range fileSet.Files {
		plan, ok := result[spec.Path]
		if !ok {
			continue
		}
		switch update := plan.update.(type) {
		case *claudesettings.Update:
			batch.updates = append(batch.updates, update)
		case *claudeCredentialRetirement:
			batch.retirements = append(batch.retirements, update)
		default:
			return nil, fmt.Errorf("unsupported Claude restore plan for %s", spec.Path)
		}
		plan.update = batch
		result[spec.Path] = plan
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
	return claudesettings.ApplyUpdates(updates)
}

func hasClaudeUserSettingsAuth(path string) bool {
	policy, err := claudeSettingsPolicy()
	if err != nil {
		return false
	}
	snapshot, err := readClaudeSettingsSnapshot(path, policy)
	return err == nil && snapshot.hasAuth
}
