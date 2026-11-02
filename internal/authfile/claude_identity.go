package authfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// ClaudeProfileIdentityOptions keeps ordinary diagnostics offline. Repair uses
// an exact cached proof where possible; Online explicitly requests a fresh
// provider check, even when a cached proof exists.
type ClaudeProfileIdentityOptions struct {
	Online bool
	Repair bool
}

// ClaudeProfileIdentityResult describes only a saved vault profile. No native
// credential, keychain, or isolated profile is consulted by this operation.
type ClaudeProfileIdentityResult struct {
	Provider         string             `json:"provider"`
	Profile          string             `json:"profile"`
	State            string             `json:"state"`
	Source           string             `json:"source"`
	Identity         *identity.Identity `json:"identity,omitempty"`
	RecordedIdentity *identity.Identity `json:"recorded_identity,omitempty"`
	VerifiedAt       *time.Time         `json:"verified_at,omitempty"`
	Repaired         bool               `json:"repaired"`
	Detail           string             `json:"detail,omitempty"`
}

// ClaudeProfileIdentity checks or repairs ownership without activating the
// profile. A changed grant must verify as the recorded account AND organization
// before its proof can be replaced. Metadata repair never changes tokens.
func (v *Vault) ClaudeProfileIdentity(ctx context.Context, profile string, opts ClaudeProfileIdentityOptions) (ClaudeProfileIdentityResult, error) {
	result := ClaudeProfileIdentityResult{Provider: "claude", Profile: profile, State: identity.ClaudeIdentityUnknown, Source: "none"}
	if opts.Repair && IsSystemProfile(profile) {
		err := fmt.Errorf("%w: recovery snapshots cannot be repaired", errProtectedSystemProfile)
		result.Detail = err.Error()
		return result, err
	}
	operation := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := v.existingProfileDir("claude", profile)
		if err != nil {
			return err
		}
		snapshot, err := captureClaudeProfileIdentity(dir)
		if err != nil {
			return err
		}
		defer snapshot.directory.Close()
		credentials := snapshot.files[claudeCredentialsFile].data
		assessment, err := identity.InspectClaudeIdentityBinding(snapshot.files["meta.json"].data, credentials)
		if err == nil && assessment.State == identity.ClaudeIdentityUnknown {
			// The same precedence is used by identity extraction: a saved owner
			// is authoritative, while a cache can establish an unbound profile.
			assessment, err = identity.InspectClaudeIdentityBinding(snapshot.files[identity.ClaudeIdentityCacheFile].data, credentials)
		}
		result.State, result.RecordedIdentity = assessment.State, assessment.RecordedIdentity
		if err != nil {
			return err
		}
		binding := assessment.Binding
		if credentials == nil && assessment.State == identity.ClaudeIdentityUnknown {
			result.Detail = "saved profile has no modern Claude OAuth credential to verify"
			if opts.Online || opts.Repair {
				return errors.New(result.Detail)
			}
			return nil
		}
		if _, err := identity.ClaudeCredentialFingerprint(credentials); err != nil {
			result.State = "invalid_credentials"
			return err
		}
		if opts.Online || opts.Repair && binding == nil {
			verify := v.ClaudeIdentityVerifier
			if verify == nil {
				verify = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
					return identity.VerifyClaudeCredentials(ctx, data, nil)
				}
			}
			verificationContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			binding, err = verify(verificationContext, bytes.Clone(credentials))
			if err != nil || binding == nil || !binding.MatchesCredentials(credentials) {
				// Do not surface transports, response bodies or injected verifier
				// diagnostics that could contain an Authorization header.
				result.State = "unavailable"
				switch {
				case errors.Is(err, identity.ErrClaudeTokenExpired):
					result.State = "expired"
					return identity.ErrClaudeTokenExpired
				case errors.Is(err, identity.ErrClaudeTokenRejected):
					result.State = "rejected"
					return identity.ErrClaudeTokenRejected
				default:
					return identity.ErrClaudeIdentityUnavailable
				}
			}
			result.Source = "provider"
		} else if binding != nil {
			result.Source = "cache"
		}
		if err := snapshot.check(true); err != nil {
			result.State = "source_changed"
			return err
		}
		if binding == nil {
			if assessment.State == identity.ClaudeIdentityCredentialChanged {
				return errors.New("saved Claude credentials changed since identity verification; verify before use")
			}
			result.Detail = "saved Claude account and organization have not been verified"
			return nil
		}
		confirmed := binding.Identity
		confirmed.Verified = true
		result.Identity, result.VerifiedAt = &confirmed, &binding.VerifiedAt
		if result.RecordedIdentity != nil && !identity.ClaudeSameAccount(result.RecordedIdentity, &confirmed) {
			result.State = "conflict"
			return errors.New("saved Claude credentials belong to a different account or organization; recorded ownership was preserved")
		}
		result.State = identity.ClaudeIdentityVerified
		if !opts.Repair {
			return nil
		}
		verifySource := func() error { return snapshot.check(false) }
		metadata, err := claudesettings.PrepareIdentityMetadata(filepath.Join(dir, "meta.json"),
			filepath.Join(dir, claudeCredentialsFile), snapshot.files["meta.json"].data, credentials, binding, verifySource)
		if err != nil {
			return err
		}
		state, err := claudesettings.PrepareOAuthAccount(filepath.Join(dir, claudeSettingsFile),
			snapshot.files[claudeSettingsFile].data, claudeOAuthAccount(binding), verifySource)
		if err != nil {
			return err
		}
		if err := claudesettings.ApplyUpdatesWithFinalizer([]*claudesettings.Update{metadata, state}, nil, verifySource); err != nil {
			return err
		}
		result.Repaired = metadata.Changed() || state.Changed()
		return nil
	}
	var err error
	if opts.Repair {
		err = v.WithMutationLock(operation)
	} else {
		err = operation()
	}
	if err != nil {
		result.Detail = err.Error()
	}
	return result, err
}

type claudeIdentityFile struct {
	data []byte
	info os.FileInfo
}

type claudeProfileIdentitySnapshot struct {
	path      string
	directory *os.Root
	info      os.FileInfo
	parent    os.FileInfo
	files     map[string]claudeIdentityFile
}

func captureClaudeProfileIdentity(path string) (_ *claudeProfileIdentitySnapshot, resultErr error) {
	snapshot := &claudeProfileIdentitySnapshot{path: path, files: make(map[string]claudeIdentityFile)}
	for _, directory := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() {
			return nil, errors.New("saved Claude profile must use private directories")
		}
		if directory == path {
			snapshot.info = info
		} else {
			snapshot.parent = info
		}
	}
	var err error
	snapshot.directory, err = os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			snapshot.directory.Close()
		}
	}()
	for _, name := range []string{claudeCredentialsFile, "meta.json", identity.ClaudeIdentityCacheFile, claudeSettingsFile} {
		file, err := readClaudeIdentityFile(snapshot.directory, name)
		if err != nil {
			return nil, err
		}
		snapshot.files[name] = file
	}
	return snapshot, snapshot.check(true)
}

func readClaudeIdentityFile(directory *os.Root, name string) (claudeIdentityFile, error) {
	var result claudeIdentityFile
	info, err := directory.Lstat(name)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return result, fmt.Errorf("saved Claude %s must be a private regular file", name)
	}
	file, err := directory.Open(name)
	if err != nil {
		return result, fmt.Errorf("cannot read saved Claude %s", name)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, errors.New("saved Claude identity source changed while opening; retry")
	}
	result.data, err = io.ReadAll(file)
	if err != nil {
		return result, fmt.Errorf("cannot read saved Claude %s", name)
	}
	result.info = info
	return result, nil
}

func (snapshot *claudeProfileIdentitySnapshot) check(includeMetadata bool) error {
	for path, expected := range map[string]os.FileInfo{snapshot.path: snapshot.info, filepath.Dir(snapshot.path): snapshot.parent} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || !os.SameFile(expected, info) {
			return errors.New("saved Claude profile directory changed during identity verification; retry")
		}
	}
	for name, expected := range snapshot.files {
		if !includeMetadata && name != claudeCredentialsFile && name != identity.ClaudeIdentityCacheFile {
			continue
		}
		current, err := readClaudeIdentityFile(snapshot.directory, name)
		if err != nil || (current.info == nil) != (expected.info == nil) || !bytes.Equal(current.data, expected.data) ||
			current.info != nil && (!os.SameFile(current.info, expected.info) || current.info.Mode() != expected.info.Mode() || !current.info.ModTime().Equal(expected.info.ModTime())) {
			return errors.New("saved Claude profile changed during identity verification; retry")
		}
	}
	return nil
}

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
