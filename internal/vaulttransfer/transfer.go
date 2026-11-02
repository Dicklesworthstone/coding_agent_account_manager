package vaulttransfer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

// ClaudeIdentity assesses the selected native credential source offline. A
// fingerprint compares the complete access/refresh pair, never email or labels.
type ClaudeIdentity struct {
	Fingerprint string
	Binding     *identity.ClaudeIdentityBinding
	BindingFile string
}

// InspectClaude selects the same credential authority as the native runtime.
// Canonical vault ownership takes precedence over a redundant native cache.
// Expiry does not invalidate previously verified ownership of an exact pair.
func InspectClaude(snapshot *Snapshot) (*ClaudeIdentity, error) {
	// Native lookups may be case-insensitive on the receiving machine. A
	// differently cased auth/proof filename must not silently become a new
	// authoritative source after inspection on another platform.
	for name := range snapshot.entries {
		for _, canonical := range []string{".credentials.json", "auth.json", ".claude.json", "settings.json", "meta.json", identity.ClaudeIdentityCacheFile} {
			if name != canonical && strings.EqualFold(name, canonical) {
				return nil, fmt.Errorf("claude account file has noncanonical filename casing")
			}
		}
	}
	result := &ClaudeIdentity{}
	credentials, present, err := readClaudeFile(snapshot, ".credentials.json")
	if err != nil {
		return nil, err
	}
	if present {
		if err := authfile.ValidateCredentialData("claude", ".credentials.json", credentials); err != nil {
			return nil, fmt.Errorf("invalid primary Claude credentials")
		}
	} else {
		for _, name := range []string{"auth.json", "settings.json", ".claude.json"} {
			data, exists, err := readClaudeFile(snapshot, name)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
			if name == "settings.json" {
				hasAuth, err := authfile.ClaudeCredentialMaterial(data, name)
				if err != nil || len(data) == 0 {
					return nil, fmt.Errorf("invalid selected Claude settings")
				}
				if hasAuth {
					// Helpers, API keys and environment grants own this login,
					// but do not supply a verifiable OAuth account/org pair.
					// Never inherit ownership from ignored legacy credentials.
					break
				}
				continue // Policy alone does not replace the legacy authority.
			}
			var legacy struct {
				AccessToken  string          `json:"access_token"`
				RefreshToken string          `json:"refresh_token"`
				CamelAccess  string          `json:"accessToken"`
				CamelRefresh string          `json:"refreshToken"`
				OAuthToken   json.RawMessage `json:"oauthToken"`
			}
			if json.Unmarshal(data, &legacy) != nil {
				return nil, fmt.Errorf("invalid legacy Claude credentials")
			}
			if name == ".claude.json" && len(legacy.OAuthToken) > 0 && legacy.OAuthToken[0] == '{' {
				if json.Unmarshal(legacy.OAuthToken, &legacy) != nil {
					return nil, fmt.Errorf("invalid legacy Claude OAuth credentials")
				}
			}
			if legacy.AccessToken == "" {
				legacy.AccessToken = legacy.CamelAccess
			}
			if legacy.RefreshToken == "" {
				legacy.RefreshToken = legacy.CamelRefresh
			}
			if strings.TrimSpace(legacy.AccessToken) == "" {
				if name == "auth.json" {
					break // A present selected primary never falls back to legacy.
				}
				continue
			}
			credentials, err = json.Marshal(map[string]any{"claudeAiOauth": map[string]string{
				"accessToken": legacy.AccessToken, "refreshToken": legacy.RefreshToken,
			}})
			if err != nil {
				return nil, fmt.Errorf("cannot inspect legacy Claude credentials")
			}
			break
		}
	}
	result.Fingerprint, _ = identity.ClaudeCredentialFingerprint(credentials)
	for _, name := range []string{"meta.json", identity.ClaudeIdentityCacheFile} {
		metadata, exists, err := readClaudeFile(snapshot, name)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		assessment, err := identity.InspectClaudeIdentityBinding(metadata, credentials)
		if err != nil {
			return nil, fmt.Errorf("invalid Claude identity binding in %s", name)
		}
		if assessment.State == identity.ClaudeIdentityCredentialChanged {
			return nil, fmt.Errorf("claude identity binding in %s does not match its credential pair", name)
		}
		if assessment.Binding == nil {
			continue
		}
		result.Binding = assessment.Binding
		result.BindingFile = name
		return result, nil
	}
	return result, nil
}

func readClaudeFile(snapshot *Snapshot, name string) ([]byte, bool, error) {
	limit := authfile.MaxDiscoveryFileBytes
	switch name {
	case ".claude.json", "settings.json":
		limit = authfile.MaxSettingsDocumentBytes
	case "meta.json", identity.ClaudeIdentityCacheFile:
		limit = identity.MaxClaudeIdentityBytes
	}
	if captured, exists := snapshot.entries[name]; exists && captured.info.Size() > limit {
		return nil, true, fmt.Errorf("claude account file exceeds its supported size: %s", name)
	}
	return snapshot.ReadFile(name)
}

// CheckClaudeReplacement authorizes unattended replacement by account and
// organization, or an exact complete credential pair. An explicit archive
// replacement can use a separate user-authorized policy while retaining the
// previous profile, but automatic synchronization must apply this boundary.
func CheckClaudeReplacement(current, incoming *ClaudeIdentity) error {
	if current == nil || incoming == nil {
		return fmt.Errorf("claude ownership is unavailable")
	}
	samePair := incoming.Fingerprint != "" && incoming.Fingerprint == current.Fingerprint
	sameOwner := current.Binding != nil && incoming.Binding != nil &&
		identity.ClaudeSameAccount(&current.Binding.Identity, &incoming.Binding.Identity)
	if current.Binding != nil && incoming.Binding != nil && !sameOwner {
		return fmt.Errorf("claude account or organization differs; use a separate profile name")
	}
	if !samePair && !sameOwner {
		return fmt.Errorf("claude ownership is unverified for different credentials")
	}
	if current.Binding != nil && incoming.Binding == nil {
		return fmt.Errorf("transfer would discard verified Claude account and organization")
	}
	return nil
}

// Prepare copies a captured profile into an empty private directory and binds
// its account labels to the selected credentials. It never contacts a provider
// or changes either token. Callers own the staging directory's lifetime.
func Prepare(source *Snapshot, destination, provider string) (*Snapshot, error) {
	if !source.Exists() {
		return nil, fmt.Errorf("source profile is missing")
	}
	if pathsOverlap(source.Path(), destination) {
		return nil, fmt.Errorf("profile staging overlaps its source")
	}
	if err := validateDirectory(destination, true); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, fmt.Errorf("profile staging directory is not empty")
	}
	if err := os.Chmod(destination, 0700); err != nil {
		return nil, err
	}
	if err := source.Check(); err != nil {
		return nil, err
	}
	var claude *ClaudeIdentity
	omittedFile := ""
	if strings.EqualFold(provider, "claude") {
		claude, err = InspectClaude(source)
		if err != nil {
			return nil, err
		}
		if claude.Binding != nil {
			omittedFile = identity.ClaudeIdentityCacheFile
		}
	}
	if err := source.copyTo(destination, omittedFile); err != nil {
		return nil, fmt.Errorf("copy profile: %w", err)
	}
	if claude != nil {
		if err := prepareClaudeMetadata(source, destination, claude); err != nil {
			return nil, err
		}
	}
	if err := source.Check(); err != nil {
		return nil, err
	}
	return Capture(destination)
}

func prepareClaudeMetadata(source *Snapshot, destination string, claude *ClaudeIdentity) error {
	if claude.Binding != nil && claude.BindingFile != "meta.json" {
		metadata, exists, err := readClaudeFile(source, "meta.json")
		if err != nil {
			return err
		}
		var envelope map[string]json.RawMessage
		if exists {
			if json.Unmarshal(metadata, &envelope) != nil || envelope == nil {
				return fmt.Errorf("invalid Claude identity metadata")
			}
		} else {
			envelope = make(map[string]json.RawMessage)
		}
		envelope["claude_identity"], err = json.Marshal(claude.Binding)
		if err != nil {
			return fmt.Errorf("encode Claude identity binding")
		}
		metadata, err = json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			return fmt.Errorf("encode Claude identity metadata")
		}
		if err := writeBytes(filepath.Join(destination, "meta.json"), metadata); err != nil {
			return err
		}
	}
	settings, exists, err := readClaudeFile(source, ".claude.json")
	if err != nil || !exists {
		return err
	}
	var account json.RawMessage
	if claude.Binding != nil {
		account, err = json.Marshal(struct {
			AccountID      string `json:"accountUuid"`
			OrganizationID string `json:"organizationUuid"`
			Email          string `json:"emailAddress,omitempty"`
			Organization   string `json:"organizationName,omitempty"`
		}{claude.Binding.Identity.AccountID, claude.Binding.Identity.OrganizationID,
			claude.Binding.Identity.Email, claude.Binding.Identity.Organization})
		if err != nil {
			return fmt.Errorf("encode verified Claude account")
		}
	}
	settings, err = claudesettings.WithOAuthAccount(settings, account)
	if err != nil {
		return fmt.Errorf("invalid Claude account settings")
	}
	return writeBytes(filepath.Join(destination, ".claude.json"), settings)
}

// Options configures one complete profile publication. Callers must already
// hold the destination vault mutation lock and authorize replacement. Hooks
// also let remote peers guard the source held under their separate vault lock.
type Options struct {
	Provider      string
	CheckSource   func() error
	BeforePublish func(source, destination string)
	Rename        func(source, destination string) error
}

// Publish stages, normalizes, and publishes one captured profile. A replacement
// retains the complete previous generation outside selectable profile names.
// On failure, rollback only touches a generation still owned by this operation;
// an independently changed login stays in place, with the old recovery retained.
func Publish(source, destination *Snapshot, opts *Options) (string, error) {
	if opts == nil {
		return "", fmt.Errorf("profile transfer options are required")
	}
	src, dst := source.Path(), destination.Path()
	if pathsOverlap(src, dst) {
		return "", fmt.Errorf("profile transfer source and destination overlap")
	}
	if err := destination.Check(); err != nil {
		return "", err
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", fmt.Errorf("create profile parent: %w", err)
	}
	if err := destination.checkAncestors(); err != nil {
		return "", err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() {
		return "", changed(parent)
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return "", fmt.Errorf("open profile parent: %w", err)
	}
	defer parentRoot.Close()
	openedParent, err := parentRoot.Stat(".")
	if err != nil || !sameObject(parentInfo, openedParent) {
		return "", changed(parent)
	}
	checkParent := func() error {
		if err := destination.checkAncestors(); err != nil {
			return err
		}
		current, err := os.Lstat(parent)
		if err != nil || !sameObject(parentInfo, current) {
			return changed(parent)
		}
		return nil
	}
	if err := checkParent(); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(parent, "_caam-vault-backup~import-*")
	if err != nil {
		return "", fmt.Errorf("create profile stage: %w", err)
	}
	stageName := filepath.Base(tmpDir)
	stageInfo, err := os.Lstat(tmpDir)
	if err != nil || !stageInfo.IsDir() {
		return "", changed(tmpDir)
	}
	ownedStage, err := parentRoot.Lstat(stageName)
	if err != nil || !sameObject(stageInfo, ownedStage) {
		// The parent changed while the name was reserved; preserve any stage
		// whose ownership cannot be established through the captured parent.
		return "", changed(parent)
	}
	keepStage := false
	defer func() {
		if keepStage || checkParent() != nil {
			return
		}
		current, err := parentRoot.Lstat(stageName)
		if err == nil && sameObject(ownedStage, current) {
			// Never remove an independent directory that reused this stage name.
			parentRoot.RemoveAll(stageName)
		}
	}()
	if err := checkParent(); err != nil {
		return "", err
	}
	prepared, err := Prepare(source, tmpDir, opts.Provider)
	if err != nil {
		return "", err
	}
	if opts.BeforePublish != nil {
		opts.BeforePublish(src, dst)
	}
	checkSource := func() error {
		if err := source.Check(); err != nil {
			return err
		}
		if opts.CheckSource != nil {
			return opts.CheckSource()
		}
		return nil
	}
	if err := checkSource(); err != nil {
		return "", err
	}
	if err := destination.Check(); err != nil {
		return "", err
	}
	if err := prepared.Check(); err != nil {
		return "", err
	}
	rename := opts.Rename
	if rename == nil {
		rename = os.Rename
	}
	var backupDir, backupPath string
	if destination.Exists() {
		backupDir, err = os.MkdirTemp(parent, "_caam-vault-backup~bundle-*")
		if err != nil {
			return "", fmt.Errorf("reserve previous profile: %w", err)
		}
		backupPath = filepath.Join(backupDir, "profile")
		if err := rename(dst, backupPath); err != nil {
			os.Remove(backupDir)
			return "", fmt.Errorf("preserve previous profile: %w", err)
		}
	}
	restore := func(cause error) (string, error) {
		if backupPath != "" {
			if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				return backupPath, fmt.Errorf("%w; destination changed; previous profile preserved at %s", cause, backupPath)
			}
			if err := rename(backupPath, dst); err != nil {
				return backupPath, fmt.Errorf("%w; restore failed: %v; previous profile preserved at %s", cause, err, backupPath)
			}
			os.Remove(backupDir)
		}
		return "", cause
	}
	if backupPath != "" {
		if err := destination.CheckAt(backupPath); err != nil {
			return restore(err)
		}
	}
	if err := checkSource(); err != nil {
		return restore(err)
	}
	if err := prepared.Check(); err != nil {
		return restore(err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		return restore(fmt.Errorf("destination changed before profile publication; retry"))
	}
	if err := rename(tmpDir, dst); err != nil {
		return restore(fmt.Errorf("publish profile: %w", err))
	}
	rollback := func(cause error) (string, error) {
		if err := prepared.CheckAt(dst); err != nil {
			return backupPath, fmt.Errorf("%w; destination changed independently; previous profile retained", cause)
		}
		if err := rename(dst, tmpDir); err != nil {
			return backupPath, fmt.Errorf("%w; cannot withdraw prepared profile: %v", cause, err)
		}
		// A writer racing the previous check may have installed its own login.
		// Restore that generation instead of deleting it during rollback.
		if err := prepared.CheckAt(tmpDir); err != nil {
			if _, statErr := os.Lstat(dst); os.IsNotExist(statErr) {
				if renameErr := rename(tmpDir, dst); renameErr == nil {
					return backupPath, fmt.Errorf("%w; concurrent destination preserved", cause)
				}
			}
			keepStage = true
			return backupPath, fmt.Errorf("%w; concurrent profile preserved at %s", cause, tmpDir)
		}
		return restore(cause)
	}
	if err := prepared.CheckAt(dst); err != nil {
		return backupPath, err
	}
	if err := checkSource(); err != nil {
		return rollback(err)
	}
	if backupPath != "" {
		if err := destination.CheckAt(backupPath); err != nil {
			return rollback(err)
		}
	}
	if err := prepared.CheckAt(dst); err != nil {
		return backupPath, err
	}
	return backupPath, nil
}

func pathsOverlap(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return true
	}
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

func writeBytes(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".caam-transfer-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
