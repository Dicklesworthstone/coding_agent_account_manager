package authfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackupIsolatedClaude saves a registered isolated OAuth login to a NEW vault
// name. It never reads the native login, calls a provider, changes environment
// variables, refreshes tokens, or modifies the isolated profile. Stop the source
// session first; two copies of an OAuth grant are not independent logins.
//
// Only the directory selected by caam exec's CLAUDE_CONFIG_DIR is authoritative.
// Legacy home/.claude files are deliberately not mixed into a newer XDG login.
// Existing destinations (including empty directories) are never replaced.
func (v *Vault) BackupIsolatedClaude(profileRoot, target string) error {
	return v.backupIsolatedClaude(profileRoot, target, func(prepared *Vault, files AuthFileSet, name string) error {
		return prepared.BackupWithOptions(files, name, BackupOptions{})
	})
}

// Preparation is injected for deterministic failure/concurrent-write tests.
// Production always uses BackupWithOptions: credential validation, account
// conflict checks, metadata generation and snapshot format have one owner.
func (v *Vault) backupIsolatedClaude(profileRoot, target string, prepare func(*Vault, AuthFileSet, string) error) error {
	target = strings.TrimSpace(target)
	destination, err := v.safeProfileDir("claude", target)
	if err != nil {
		return err
	}
	if IsSystemProfile(target) {
		return fmt.Errorf("%w: choose a user vault name", errProtectedSystemProfile)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("vault profile claude/%s already exists; choose a new name with --as: %w", target, os.ErrExist)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect vault destination: %w", err)
	}

	source, err := captureIsolatedClaude(profileRoot)
	if err != nil {
		return err
	}
	toolDir := filepath.Dir(destination)
	if err := os.MkdirAll(toolDir, 0700); err != nil {
		return fmt.Errorf("create vault directory: %w", err)
	}
	parent, err := inspectIsolatedSourceDir(toolDir)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(toolDir, vaultBackupPrefix+"isolated-*")
	if err != nil {
		return fmt.Errorf("prepare isolated backup: %w", err)
	}
	workDir, err := inspectIsolatedSourceDir(work)
	if err != nil {
		return err
	}
	defer func() {
		// A replaced parent must not redirect cleanup into somebody else's
		// directory. Leave the private transaction in place in that case.
		if parent.unchanged() == nil && workDir.unchanged() == nil {
			_ = os.RemoveAll(work)
		}
	}()

	// Freeze checked bytes, not source paths. The ordinary backup reader may
	// follow native adoption links and consult the default keychain; neither
	// behavior is appropriate for an isolated source. A private preparation
	// vault reuses the existing snapshot engine without exposing partial work.
	captureDir := filepath.Join(work, "source")
	if err := os.Mkdir(captureDir, 0700); err != nil {
		return err
	}
	files := AuthFileSet{Tool: "claude"}
	var originalPaths []string
	for _, file := range source.files {
		name := filepath.Base(file.path)
		path := filepath.Join(captureDir, name)
		files.Files = append(files.Files, AuthFileSpec{
			Tool: "claude", Path: path, Required: name == claudeCredentialsFile,
		})
		if file.info != nil {
			if err := writeSwitchFile(path, file.data, true); err != nil {
				return fmt.Errorf("prepare isolated %s: %w", name, err)
			}
			originalPaths = append(originalPaths, file.path)
		}
	}
	prepared := NewVault(filepath.Join(work, "vault"))
	if err := prepare(prepared, files, target); err != nil {
		return fmt.Errorf("prepare isolated vault snapshot: %w", err)
	}
	stage := prepared.ProfilePath("claude", target)
	metadataPath := filepath.Join(stage, "meta.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("read prepared metadata: %w", err)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata == nil {
		return fmt.Errorf("invalid prepared snapshot metadata")
	}
	// Keep the engine's derived identity and identity_keys, but record the
	// real source paths rather than the short-lived frozen input directory.
	metadata["original_paths"] = originalPaths
	metadata["source_type"] = "isolated-profile"
	metadata["source_profile"] = filepath.Base(source.root)
	metadata["source_profile_path"] = source.root
	if err := writeJSONFileAtomic(metadataPath, metadata, 0600); err != nil {
		return fmt.Errorf("prepare source metadata: %w", err)
	}

	// Serialize final publication with in-process switches/backups. Native
	// writers do not take this lock, so recheck every captured source artifact,
	// absent optional file, directory identity and the isolated session lock.
	switchMu.Lock()
	defer switchMu.Unlock()
	if err := source.unchanged(); err != nil {
		return err
	}
	if err := parent.unchanged(); err != nil {
		return fmt.Errorf("vault directory changed during isolated backup: %w", err)
	}
	if err := workDir.unchanged(); err != nil {
		return fmt.Errorf("snapshot transaction changed during isolated backup: %w", err)
	}
	return publishBackupSnapshot(stage, destination, "", nil, renameBackupNoReplace)
}

type isolatedClaudeSource struct {
	root     string
	dirs     []isolatedSourceDir
	metadata *isolatedSourceFile
	files    []*isolatedSourceFile
}

func captureIsolatedClaude(profileRoot string) (*isolatedClaudeSource, error) {
	if strings.TrimSpace(profileRoot) == "" {
		return nil, fmt.Errorf("isolated profile path is empty")
	}
	root, err := filepath.Abs(profileRoot)
	if err != nil {
		return nil, err
	}
	s := &isolatedClaudeSource{root: root}
	configDir := filepath.Join(root, "xdg_config", "claude-code")
	for _, path := range []string{filepath.Dir(root), root, filepath.Join(root, "xdg_config"), configDir} {
		dir, err := inspectIsolatedSourceDir(path)
		if err != nil {
			return nil, err
		}
		s.dirs = append(s.dirs, dir)
	}
	if err := s.checkSessionStopped(); err != nil {
		return nil, err
	}
	s.metadata, err = readIsolatedSourceFile(filepath.Join(root, "profile.json"), true, MaxDiscoveryFileBytes)
	if err != nil {
		return nil, err
	}
	if err := validateIsolatedClaudeMetadata(s.metadata.data, root); err != nil {
		return nil, err
	}
	state := switchState{files: make(map[string][]byte)}
	for _, name := range []string{claudeCredentialsFile, claudeSettingsFile, "auth.json", "settings.json"} {
		limit := int64(MaxDiscoveryFileBytes)
		if name == claudeSettingsFile || name == "settings.json" {
			limit = MaxSettingsDocumentBytes
		}
		file, err := readIsolatedSourceFile(filepath.Join(configDir, name),
			name == claudeCredentialsFile || name == claudeSettingsFile, limit)
		if err != nil {
			return nil, err
		}
		s.files = append(s.files, file)
		if file.info == nil {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(file.data, &object) != nil || object == nil {
			return nil, fmt.Errorf("%w: isolated %s must contain a JSON object", ErrInvalidCredentials, name)
		}
		state.files[name] = file.data
	}
	state.identify(AuthFileSet{Tool: "claude"})
	if state.identityConflict {
		return nil, fmt.Errorf("%w: isolated credentials and account identity conflict", ErrInvalidCredentials)
	}
	if !state.complete {
		return nil, fmt.Errorf("%w: isolated Claude login needs both access and refresh tokens; finish /login first", ErrNoCredentials)
	}
	var settings map[string]interface{}
	_ = json.Unmarshal(state.files[claudeSettingsFile], &settings)
	if len(claudeIdentityKeys(settings)) == 0 {
		return nil, fmt.Errorf("%w: isolated .claude.json has no account identity; finish /login first", ErrNoCredentials)
	}
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *isolatedClaudeSource) checkSessionStopped() error {
	if _, err := os.Lstat(filepath.Join(s.root, ".lock")); err == nil {
		return fmt.Errorf("isolated profile is locked; exit its caam exec/login session before backing it up (the live Claude session may keep running)")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect isolated session lock: %w", err)
	}
	return nil
}

func (s *isolatedClaudeSource) unchanged() error {
	if err := s.checkSessionStopped(); err != nil {
		return err
	}
	for _, dir := range s.dirs {
		if err := dir.unchanged(); err != nil {
			return err
		}
	}
	if err := s.metadata.unchanged(); err != nil {
		return err
	}
	for _, file := range s.files {
		if err := file.unchanged(); err != nil {
			return err
		}
	}
	// A parent can be replaced while the individual files are being checked.
	for _, dir := range s.dirs {
		if err := dir.unchanged(); err != nil {
			return err
		}
	}
	return s.checkSessionStopped()
}
