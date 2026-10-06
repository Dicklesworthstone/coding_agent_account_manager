package keepalive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// SyncResult explains whether one saved profile received a newer live
// credential. An unproven identity or age is a skip, never permission to copy.
type SyncResult struct {
	Profile string `json:"profile,omitempty"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// SnapshotIdentity reads a saved account only to explain which live grant owns
// it. It cannot create a runnable Grant, update the snapshot, or inspect an
// arbitrary caller-supplied path.
func SnapshotIdentity(vault *authfile.Vault, provider, profile string) (AccountIdentity, error) {
	if vault == nil || (provider != "claude" && provider != "grok") {
		return AccountIdentity{}, fmt.Errorf("unsupported keepalive vault provider")
	}
	profiles, err := vault.List(provider)
	if err != nil {
		return AccountIdentity{}, err
	}
	found := false
	for _, name := range profiles {
		if name == profile {
			found = true
			break
		}
	}
	if !found {
		return AccountIdentity{}, fmt.Errorf("saved profile does not exist")
	}
	dir := vault.ProfilePath(provider, profile)
	if err := regularSnapshotDirectory(filepath.Dir(dir)); err != nil {
		return AccountIdentity{}, err
	}
	if err := regularSnapshotDirectory(dir); err != nil {
		return AccountIdentity{}, err
	}
	authPath, identityPath := snapshotCredentialPaths(dir, provider)
	if err := regularSnapshotFile(authPath, false); err != nil {
		return AccountIdentity{}, err
	}
	if identityPath != "" {
		if err := regularSnapshotFile(identityPath, true); err != nil {
			return AccountIdentity{}, err
		}
	}
	snapshot, err := readCredentialFiles(provider, authPath, identityPath)
	if err != nil {
		return AccountIdentity{}, err
	}
	if !knownIdentity(snapshot.Identity) {
		return AccountIdentity{}, fmt.Errorf("saved account identity is unavailable")
	}
	return snapshot.Identity, nil
}

func snapshotCredentialPaths(dir, provider string) (authPath, identityPath string) {
	if provider == "claude" {
		return filepath.Join(dir, ".credentials.json"), filepath.Join(dir, ".claude.json")
	}
	return filepath.Join(dir, "auth.json"), ""
}

// SyncVault copies a proven newer OAuth credential from its live owner into
// existing user snapshots of the same account. It never restores or executes a
// vault credential. Call it after the native CLI exits: the native credential
// lock is acquired here only for the stable read and saved-copy update.
//
// Only the credential file is replaced. The already-matching saved Claude
// identity and all settings/metadata stay in place, so an unrelated auxiliary
// auth source or the calling process's macOS keychain cannot enter the copy.
func SyncVault(ctx context.Context, grant Grant, observed CredentialSnapshot, vault *authfile.Vault) ([]SyncResult, error) {
	if vault == nil {
		return nil, fmt.Errorf("keepalive vault is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Verify the live-source binding without reading credential bytes before
	// the native lock: the CLI may currently be replacing those bytes.
	if err := validateGrantSource(grant); err != nil {
		return syncSkipped("source_unavailable", "live credential source cannot be accessed safely"), nil
	}
	profiles, err := vault.List(grant.Provider)
	if err != nil {
		return nil, fmt.Errorf("list keepalive snapshots: %w", err)
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	if err := regularSnapshotDirectory(filepath.Join(vault.BasePath(), grant.Provider)); err != nil {
		return syncSkipped("snapshot_invalid", "saved provider directory is not a regular directory"), nil
	}
	lock, err := acquireNativeCredentialLock(ctx, grant)
	if err != nil {
		return syncSkipped("native_lock_unavailable", "native credential lock was unavailable; saved credentials were left unchanged"), err
	}
	defer closeCredentialLock(lock)

	live, err := ReadCredential(grant)
	if err != nil {
		return syncSkipped("source_unavailable", "live credential disappeared or became invalid before the copy"), nil
	}
	if !live.HasRefreshToken {
		return syncSkipped("source_not_renewable", "live credential has no refresh token; saved credentials were left unchanged"), nil
	}
	if !SameAccount(grant.Identity, live.Identity) || !SameAccount(observed.Identity, live.Identity) {
		return syncSkipped("source_owner_changed", "live account changed after renewal; saved credentials were left unchanged"), nil
	}
	if observed.Fingerprint == "" || (live.Fingerprint != observed.Fingerprint && !live.ExpiresAt.After(observed.ExpiresAt)) {
		return syncSkipped("source_changed", "live credential changed without a provably newer expiry"), nil
	}
	if live.ExpiresAt.IsZero() || !live.ExpiresAt.After(time.Now()) {
		return syncSkipped("source_not_valid", "live credential has no proven future expiry"), nil
	}
	if ambiguousSavedAccount(vault, grant.Provider, profiles, live.Identity) {
		return syncSkipped("ambiguous_saved_account", "saved profiles with this email have conflicting account IDs"), nil
	}

	results := make([]SyncResult, 0, len(profiles))
	var failures []error
	for _, profile := range profiles {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		result := SyncResult{Profile: profile, Status: "skipped"}
		if authfile.IsSystemProfile(profile) {
			result.Code, result.Reason = "system_profile", "system snapshots are immutable"
			results = append(results, result)
			continue
		}
		profileDir := vault.ProfilePath(grant.Provider, profile)
		if err := regularSnapshotDirectory(profileDir); err != nil {
			result.Code, result.Reason = "snapshot_invalid", "saved profile is not a regular directory"
			results = append(results, result)
			continue
		}
		if user, err := userSnapshotMetadata(profileDir); err != nil || !user {
			result.Code, result.Reason = "system_profile", "saved profile metadata does not identify a user snapshot"
			results = append(results, result)
			continue
		}
		authPath := filepath.Join(profileDir, filepath.Base(grant.AuthPath))
		identityPath := ""
		if grant.Provider == "claude" {
			identityPath = filepath.Join(profileDir, ".claude.json")
		}
		if err := regularSnapshotFile(authPath, false); err != nil {
			result.Code, result.Reason = "snapshot_invalid", "saved credential is missing or is not a regular file"
			results = append(results, result)
			continue
		}
		if identityPath != "" {
			if err := regularSnapshotFile(identityPath, true); err != nil {
				result.Code, result.Reason = "snapshot_invalid", "saved account identity is not a regular file"
				results = append(results, result)
				continue
			}
		}
		snapshot, err := readCredentialFiles(grant.Provider, authPath, identityPath)
		if err != nil {
			result.Code, result.Reason = "snapshot_invalid", "saved credential or account identity is invalid"
			results = append(results, result)
			continue
		}
		if !SameAccount(live.Identity, snapshot.Identity) {
			result.Code, result.Reason = "identity_mismatch", "saved account does not match the live owner"
			results = append(results, result)
			continue
		}
		if snapshot.ExpiresAt.IsZero() {
			result.Code, result.Reason = "snapshot_expiry_unknown", "saved credential age cannot be established"
			results = append(results, result)
			continue
		}
		if !live.ExpiresAt.After(snapshot.ExpiresAt) {
			result.Code, result.Reason = "not_newer", "saved credential has the same or a later expiry"
			results = append(results, result)
			continue
		}

		// Re-read both members of the Claude credential/identity pair before
		// the atomic replacement. The native lock protects compliant live
		// writers; the byte checks also detect independent settings changes
		// and vault updates that occurred while this copy was prepared.
		err = replaceSavedCredential(authPath, live.data, func(stagedPath string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Opaque modern Claude tokens may rely entirely on their paired
			// account state. Do not erase the only identity embedded in an
			// older saved credential when no saved sidecar can replace it.
			replacement, err := readCredentialFiles(grant.Provider, stagedPath, identityPath)
			if err != nil || !SameAccount(live.Identity, replacement.Identity) {
				return errSyncIdentityUnproven
			}
			if !credentialLockIsCurrent(lock, nativeCredentialLockPath(grant)) {
				return errSyncChanged
			}
			current, err := ReadCredential(grant)
			if err != nil || !sameCredentialSnapshot(current, live) || !current.ExpiresAt.After(time.Now()) {
				return errSyncChanged
			}
			if err := regularSnapshotDirectory(profileDir); err != nil {
				return errSyncChanged
			}
			if err := regularSnapshotFile(authPath, false); err != nil {
				return errSyncChanged
			}
			if identityPath != "" {
				if err := regularSnapshotFile(identityPath, true); err != nil {
					return errSyncChanged
				}
			}
			currentSaved, err := readCredentialFiles(grant.Provider, authPath, identityPath)
			if err != nil || !sameCredentialSnapshot(currentSaved, snapshot) {
				return errSyncChanged
			}
			if user, err := userSnapshotMetadata(profileDir); err != nil || !user {
				return errSyncChanged
			}
			return nil
		})
		switch {
		case errors.Is(err, errSyncIdentityUnproven):
			result.Code, result.Reason = "snapshot_identity_unproven", "new credential needs matching saved account state; back up this live home first"
		case errors.Is(err, errSyncChanged):
			result.Code, result.Reason = "credential_changed", "live or saved account files changed before the copy"
		case err != nil:
			result.Status, result.Code, result.Reason = "failed", "write_failed", "could not atomically save the newer credential"
			failures = append(failures, fmt.Errorf("sync %s/%s: %w", grant.Provider, profile, err))
		default:
			result.Status, result.Code, result.Reason = "synced", "newer_live_credential", "saved the newer credential from the same live account"
		}
		results = append(results, result)
	}
	return results, errors.Join(failures...)
}

var errSyncChanged = errors.New("credential files changed before copy")
var errSyncIdentityUnproven = errors.New("replacement cannot prove the saved account identity")

func syncSkipped(code, reason string) []SyncResult {
	return []SyncResult{{Status: "skipped", Code: code, Reason: reason}}
}

func sameCredentialSnapshot(a, b CredentialSnapshot) bool {
	return SameAccount(a.Identity, b.Identity) && bytes.Equal(a.data, b.data) && bytes.Equal(a.identityData, b.identityData)
}

func ambiguousSavedAccount(vault *authfile.Vault, provider string, profiles []string, live AccountIdentity) bool {
	if live.AccountID != "" {
		return false
	}
	var knownID string
	for _, profile := range profiles {
		if authfile.IsSystemProfile(profile) {
			continue
		}
		if user, err := userSnapshotMetadata(vault.ProfilePath(provider, profile)); err != nil || !user {
			continue
		}
		id, err := SnapshotIdentity(vault, provider, profile)
		if err != nil || id.AccountID == "" || !SameAccount(live, id) {
			continue
		}
		if knownID != "" && knownID != id.AccountID {
			return true
		}
		knownID = id.AccountID
	}
	return false
}

func regularSnapshotDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot path is not a regular directory")
	}
	return nil
}

func regularSnapshotFile(path string, optional bool) error {
	info, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("snapshot path is not a regular file")
	}
	return nil
}

func userSnapshotMetadata(dir string) (bool, error) {
	path := filepath.Join(dir, "meta.json")
	if err := regularSnapshotFile(path, true); err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true, nil // Older user profiles need not have metadata.
	}
	if err != nil {
		return false, err
	}
	var meta *struct {
		Type      string `json:"type"`
		CreatedBy string `json:"created_by"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false, err
	}
	if meta == nil {
		return false, fmt.Errorf("snapshot metadata must be a JSON object")
	}
	return (meta.Type == "" || meta.Type == "user") && meta.CreatedBy != "auto" && meta.CreatedBy != "first-activate", nil
}

func nativeCredentialLockPath(grant Grant) string {
	if grant.Provider == "claude" {
		return filepath.Join(filepath.Dir(grant.AuthPath), ".credentials.lock")
	}
	return filepath.Join(filepath.Dir(grant.AuthPath), "auth.json.lock")
}

// Claude's per-identity flock is part of the shallow-profile layout. Grok's
// auth.json.lock uses the same advisory lock, with PID:epoch holder metadata
// and an inode check (xai-org/grok-build, xai-grok-workspace/src/hub_auth.rs).
// Never unlink a contended native lock or reclaim it by age.
func acquireNativeCredentialLock(ctx context.Context, grant Grant) (*os.File, error) {
	if grant.Provider != "claude" && grant.Provider != "grok" {
		return nil, fmt.Errorf("native keepalive is unsupported for %s", grant.Provider)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path := nativeCredentialLockPath(grant)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := regularSnapshotFile(path, true); err != nil {
			return nil, fmt.Errorf("native credential lock is not a regular file: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("open native credential lock: %w", err)
		}
		err = tryFileLock(file)
		if err == nil {
			if credentialLockIsCurrent(file, path) {
				if grant.Provider == "grok" {
					if err := writeNativeLockHolder(file); err != nil {
						closeCredentialLock(file)
						return nil, err
					}
				}
				return file, nil
			}
			closeCredentialLock(file)
		} else {
			_ = file.Close()
			if !errors.Is(err, errLockBusy) {
				return nil, fmt.Errorf("lock native credentials: %w", err)
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func credentialLockIsCurrent(file *os.File, path string) bool {
	opened, err := file.Stat()
	if err != nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && current.Mode().IsRegular() && os.SameFile(opened, current)
}

func writeNativeLockHolder(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("record native credential lock owner: %w", err)
	}
	owner := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().Unix())
	if _, err := file.WriteAt([]byte(owner), 0); err != nil {
		return fmt.Errorf("record native credential lock owner: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync native credential lock owner: %w", err)
	}
	return nil
}

func closeCredentialLock(file *os.File) {
	_ = unlockFile(file)
	_ = file.Close()
}

func replaceSavedCredential(path string, data []byte, unchanged func(stagedPath string) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".keepalive-credential-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unchanged(tempPath); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
