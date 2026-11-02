package authfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

// A Claude label is a hint until the provider has associated the exact opaque
// credential generation with both an account and an organization. Native state
// files are independently rewritten by running Claude sessions.
const claudeIdentityUnknownWarning = "Claude account and organization could not be verified; unverified account labels were omitted and saved profiles were not matched by email"

func (s *switchState) setClaudeIdentity(binding *identity.ClaudeIdentityBinding) {
	s.claudeIdentity = nil
	if binding == nil || !binding.MatchesCredentials(s.credential) {
		return
	}
	s.claudeIdentity = binding
	s.account = switchAccount{account: binding.Identity.AccountID, organization: binding.Identity.OrganizationID, email: binding.Identity.Email}
	// The provider proof supersedes independently rewritten local labels.
	s.identityConflict = false
}

func readStoredClaudeBinding(profileDir string) (*identity.ClaudeIdentityBinding, error) {
	path := filepath.Join(profileDir, "meta.json")
	data, err := claudesettings.Read(path)
	if err != nil || data == nil {
		return nil, err
	}
	var meta struct {
		Binding *identity.ClaudeIdentityBinding `json:"claude_identity"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("invalid Claude identity metadata")
	}
	return meta.Binding, nil
}

// verifyClaudeState is called only at mutation boundaries. Cached proofs and
// exact copies of a proven grant need no network, including after expiry or a
// restart. A provider outage never turns a settings label into ownership proof.
func (v *Vault) verifyClaudeState(fileSet AuthFileSet, profileDir string, state *switchState) (string, error) {
	if fileSet.Tool != "claude" || state.credentialName != claudeCredentialsFile {
		return "", nil
	}
	var stored *identity.ClaudeIdentityBinding
	if profileDir != "" {
		var err error
		stored, err = readStoredClaudeBinding(profileDir)
		if err != nil {
			return "", err
		}
	}
	if state.claudeIdentity != nil {
		if stored != nil && !identity.ClaudeSameAccount(&stored.Identity, &state.claudeIdentity.Identity) {
			return "", fmt.Errorf("%w: Claude tokens do not belong to the saved account and organization", ErrInvalidCredentials)
		}
		return "", nil
	}
	// Reuse only a proof of these exact tokens, never an email or a refresh
	// token alone. This also works when the live cache was not persisted.
	profiles, err := v.List("claude")
	if err != nil {
		return "", err
	}
	for _, name := range profiles {
		binding, err := readStoredClaudeBinding(v.ProfilePath("claude", name))
		if err != nil || binding == nil || !binding.MatchesCredentials(state.credential) {
			continue
		}
		if stored != nil && !identity.ClaudeSameAccount(&stored.Identity, &binding.Identity) {
			return "", fmt.Errorf("%w: Claude tokens do not belong to the saved account and organization", ErrInvalidCredentials)
		}
		state.setClaudeIdentity(binding)
		return "", nil
	}
	verify := v.ClaudeIdentityVerifier
	if verify == nil {
		verify = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
			return identity.VerifyClaudeCredentials(ctx, data, nil)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	binding, verifyErr := verify(ctx, state.credential)
	if verifyErr != nil || binding == nil || !binding.MatchesCredentials(state.credential) {
		if stored != nil {
			return "", fmt.Errorf("%w: saved Claude credentials changed since identity verification; verify the selected account before activation", ErrInvalidCredentials)
		}
		return claudeIdentityUnknownWarning, nil
	}
	if stored != nil && !identity.ClaudeSameAccount(&stored.Identity, &binding.Identity) {
		return "", fmt.Errorf("%w: Claude tokens do not belong to the saved account and organization", ErrInvalidCredentials)
	}
	state.setClaudeIdentity(binding)
	return "", nil
}

func claudeOAuthAccount(binding *identity.ClaudeIdentityBinding) json.RawMessage {
	if binding == nil {
		return nil
	}
	account := map[string]string{
		"accountUuid":      binding.Identity.AccountID,
		"organizationUuid": binding.Identity.OrganizationID,
	}
	if binding.Identity.Email != "" {
		account["emailAddress"] = binding.Identity.Email
	}
	if binding.Identity.Organization != "" {
		account["organizationName"] = binding.Identity.Organization
	}
	data, _ := json.Marshal(account)
	return data
}

func claudeSnapshotSettings(data []byte, state switchState) ([]byte, error) {
	if state.provider != "claude" || state.credentialName != claudeCredentialsFile {
		return data, nil
	}
	return claudesettings.WithOAuthAccount(data, claudeOAuthAccount(state.claudeIdentity))
}

func addClaudeIdentityMetadata(meta map[string]interface{}, binding *identity.ClaudeIdentityBinding) {
	delete(meta, "identity")
	delete(meta, "identity_keys")
	delete(meta, "claude_identity")
	if binding == nil {
		return
	}
	meta["claude_identity"] = binding
	meta["identity"] = binding.Identity.Email
	meta["identity_keys"] = []string{"uuid:" + binding.Identity.AccountID, "org:" + binding.Identity.OrganizationID}
}

// persistClaudeKeptIdentity keeps both credential generations untouched while
// retaining their verified owners. Otherwise a first-time legacy verification
// is lost on a no-op, and a later offline activation can replay an older grant.
// Account labels are normalized independently in the saved and live documents;
// their unrelated settings and session state never cross between them.
func (v *Vault) persistClaudeKeptIdentity(fileSet AuthFileSet, profileDir string, incoming, live switchState) error {
	if fileSet.Tool != "claude" || incoming.credentialName != claudeCredentialsFile || live.credentialName != claudeCredentialsFile {
		return nil
	}
	if !live.sameCredential(incoming) && !live.sameAccount(incoming) {
		return fmt.Errorf("cannot retain Claude metadata without proving matching credentials or ownership")
	}
	if incoming.claudeIdentity == nil {
		incoming.setClaudeIdentity(live.claudeIdentity)
	}
	if live.claudeIdentity == nil {
		live.setClaudeIdentity(incoming.claudeIdentity)
	}
	if incoming.claudeIdentity != nil && live.claudeIdentity != nil &&
		!identity.ClaudeSameAccount(&incoming.claudeIdentity.Identity, &live.claudeIdentity.Identity) {
		return fmt.Errorf("%w: kept Claude credentials have conflicting verified owners", ErrInvalidCredentials)
	}
	if err := live.checkUnchanged(fileSet); err != nil {
		return err
	}
	current, err := readSwitchState(fileSet, profileDir)
	if err != nil || !sameSwitchFiles(incoming.files, current.files) {
		return fmt.Errorf("saved Claude profile changed before identity publication")
	}
	metaPath := filepath.Join(profileDir, "meta.json")
	metadata, err := claudesettings.Read(metaPath)
	if err != nil {
		return err
	}
	var expected struct {
		Binding *identity.ClaudeIdentityBinding `json:"claude_identity"`
	}
	if metadata != nil && json.Unmarshal(metadata, &expected) != nil {
		return fmt.Errorf("invalid saved Claude identity metadata")
	}
	if expected.Binding != nil && (incoming.claudeIdentity == nil ||
		!identity.ClaudeSameAccount(&expected.Binding.Identity, &incoming.claudeIdentity.Identity)) {
		return fmt.Errorf("%w: saved Claude identity changed before publication", ErrInvalidCredentials)
	}

	// .claude.json participates as a separately guarded destination below.
	// All other files, including keychain-sourced credentials, must remain the
	// exact captured generation before every metadata write and at completion.
	verify := func() error {
		for _, source := range []struct {
			dir   string
			state switchState
		}{{"", live}, {profileDir, incoming}} {
			current, err := readSwitchState(fileSet, source.dir)
			if err != nil || !sameClaudeIdentitySources(source.state.files, current.files) {
				return fmt.Errorf("claude credentials changed during identity publication; retry")
			}
		}
		return nil
	}
	savedCredential := filepath.Join(profileDir, claudeCredentialsFile)
	update, err := claudesettings.PrepareIdentityMetadata(metaPath, savedCredential, metadata, incoming.credential, incoming.claudeIdentity, verify)
	if err != nil {
		return err
	}
	updates := []*claudesettings.Update{update}
	liveCredential := resolveLiveParent(claudeFileSetPath(fileSet, claudeCredentialsFile))
	if live.claudeIdentity != nil {
		path := filepath.Join(filepath.Dir(liveCredential), identity.ClaudeIdentityCacheFile)
		before, err := claudesettings.Read(path)
		if err != nil {
			return err
		}
		update, err := claudesettings.PrepareIdentityMetadata(path, liveCredential, before, live.credential, live.claudeIdentity, verify)
		if err != nil {
			return err
		}
		updates = append(updates, update)
	}
	if statePath := claudeFileSetPath(fileSet, claudeSettingsFile); statePath != "" {
		for _, state := range []struct {
			path    string
			before  []byte
			binding *identity.ClaudeIdentityBinding
		}{
			{filepath.Join(profileDir, claudeSettingsFile), incoming.files[claudeSettingsFile], incoming.claudeIdentity},
			{resolveLiveParent(statePath), live.files[claudeSettingsFile], live.claudeIdentity},
		} {
			update, err := claudesettings.PrepareOAuthAccount(state.path, state.before, claudeOAuthAccount(state.binding), verify)
			if err != nil {
				return err
			}
			updates = append(updates, update)
		}
	}
	return claudesettings.ApplyUpdatesWithFinalizer(updates, nil, verify)
}

func sameClaudeIdentitySources(before, current map[string][]byte) bool {
	for _, pair := range [][2]map[string][]byte{{before, current}, {current, before}} {
		for name, data := range pair[0] {
			if name == claudeSettingsFile {
				continue
			}
			other, present := pair[1][name]
			if !present || !bytes.Equal(data, other) {
				return false
			}
		}
	}
	return true
}

// SyncClaudeCredentials publishes a newer, proven same-account generation,
// its proof and corrected account labels. checkSource validates only the live
// source: it runs after acquiring managed locks, before publication, and as a
// finalizer while the saved artifacts can still be rolled back. It must not
// compare the previous saved generation, which publication deliberately changes.
func (v *Vault) SyncClaudeCredentials(profile string, expected, incoming []byte, binding *identity.ClaudeIdentityBinding, checkSource func() error) error {
	switchMu.Lock()
	defer switchMu.Unlock()
	release, err := v.lockMutation()
	if err != nil {
		return err
	}
	defer release()
	return v.syncClaudeCredentials(profile, expected, incoming, binding, checkSource)
}

func (v *Vault) syncClaudeCredentials(profile string, expected, incoming []byte, binding *identity.ClaudeIdentityBinding, checkSource func() error) error {
	if IsSystemProfile(profile) || binding == nil || !binding.MatchesCredentials(incoming) {
		return fmt.Errorf("claude synchronization requires a verified user profile credential")
	}
	if checkSource == nil {
		checkSource = func() error { return nil }
	}
	if err := checkSource(); err != nil {
		return err
	}
	dir, err := v.existingProfileDir("claude", profile)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, claudeCredentialsFile)
	current, err := claudesettings.Read(path)
	if err != nil || !bytes.Equal(current, expected) {
		return fmt.Errorf("saved Claude credential changed before synchronization")
	}
	previous, err := readStoredClaudeBinding(dir)
	if err != nil || previous == nil || !previous.MatchesCredentials(expected) || !identity.ClaudeSameAccount(&previous.Identity, &binding.Identity) {
		return fmt.Errorf("claude synchronization cannot prove the saved account and organization")
	}
	oldState := switchState{files: map[string][]byte{claudeCredentialsFile: expected}}
	newState := switchState{files: map[string][]byte{claudeCredentialsFile: incoming}}
	oldState.identify(AuthFileSet{Tool: "claude"})
	newState.identify(AuthFileSet{Tool: "claude"})
	if !oldState.complete || !newState.complete || oldState.freshness.IsZero() || !newState.freshness.After(oldState.freshness) {
		return fmt.Errorf("claude synchronization requires a complete, strictly newer credential")
	}
	metaPath := filepath.Join(dir, "meta.json")
	metaData, err := claudesettings.Read(metaPath)
	if err != nil {
		return err
	}
	meta := make(map[string]interface{})
	if json.Unmarshal(metaData, &meta) != nil || meta == nil {
		return fmt.Errorf("invalid saved Claude metadata")
	}
	addClaudeIdentityMetadata(meta, binding)
	updatedMeta, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(dir, claudeSettingsFile)
	settingsData, err := claudesettings.Read(settingsPath)
	if err != nil {
		return err
	}
	updatedSettings, err := claudesettings.WithOAuthAccount(settingsData, claudeOAuthAccount(binding))
	if err != nil {
		return err
	}
	// Reuse the same guarded, recoverable publication as activation. All
	// files are staged before any is installed; a returned error rolls back
	// our writes without overwriting an intervening native write.
	work, err := os.MkdirTemp(filepath.Dir(dir), vaultBackupPrefix+"identity-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var updates []*claudesettings.Update
	for _, file := range []struct {
		name          string
		before, after []byte
	}{
		{claudeCredentialsFile, expected, incoming},
		{"meta.json", metaData, updatedMeta},
		{claudeSettingsFile, settingsData, updatedSettings},
	} {
		snapshot := filepath.Join(work, file.name)
		if err := writeSwitchFile(snapshot, file.after, true); err != nil {
			return err
		}
		captured := map[string][]byte{snapshot: file.after}
		update, err := claudesettings.PrepareCredentialReplacement(snapshot, filepath.Join(dir, file.name), file.before, file.after, captured, nil)
		if err != nil {
			return err
		}
		updates = append(updates, update)
	}
	if err := checkSource(); err != nil {
		return err
	}
	return claudesettings.ApplyUpdatesWithFinalizer(updates, nil, checkSource)
}
