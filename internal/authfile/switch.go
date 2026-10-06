package authfile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// SwitchOptions controls preservation of the outgoing login. Named profiles
// always receive a proven newer credential; BackupMode controls additional
// immutable recovery copies of live state that is not already safely saved.
type SwitchOptions struct {
	BackupMode       string
	MaxAutoBackups   int
	PreserveOriginal bool
}

// SwitchResult describes preservation and activation without exposing secrets.
// A non-nil result is also returned on failure so a caller can locate a recovery
// copy if RestoreStarted is true and restoring the target subsequently fails.
type SwitchResult struct {
	PreviousProfile      string   `json:"previous_profile,omitempty"`
	AutoBackup           string   `json:"auto_backup,omitempty"`
	ResnapshottedProfile string   `json:"resnapshotted_profile,omitempty"`
	OriginalBackup       bool     `json:"original_backup,omitempty"`
	KeptLive             bool     `json:"kept_live,omitempty"`
	RestoreStarted       bool     `json:"restore_started,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
}

// Serialize switches performed by different Vault instances in the same
// process (notably concurrent API requests). Native CLI writes are additionally
// checked against the captured bytes immediately before preservation/restore.
var switchMu sync.Mutex

// Switch validates an incoming profile, preserves the outgoing grant, then
// restores the target. All account-switching callers should use this operation;
// Restore remains the lower-level operation for deliberate snapshot recovery.
// No refresh token is exchanged here and an unproven live identity is never
// permission to overwrite a named profile.
func (v *Vault) Switch(fileSet AuthFileSet, target string, opts SwitchOptions) (result *SwitchResult, err error) {
	result = &SwitchResult{}
	var recoveryProfile string
	defer func() {
		if err != nil && recoveryProfile != "" {
			err = fmt.Errorf("%w; outgoing auth is saved in %s/%s", err, fileSet.Tool, recoveryProfile)
		}
	}()
	mode := strings.TrimSpace(opts.BackupMode)
	if mode == "" {
		mode = "smart"
	}
	if mode != "smart" && mode != "always" && mode != "never" {
		return result, fmt.Errorf("invalid switch backup mode: %s", mode)
	}
	if opts.MaxAutoBackups < 0 {
		return result, fmt.Errorf("maximum automatic backups cannot be negative")
	}

	switchMu.Lock()
	defer switchMu.Unlock()

	// Validate the target before even mirroring a keychain or creating a
	// recovery directory. Preparing Claude settings is read-only.
	if err := v.ValidateProfileCredentials(fileSet, target); err != nil {
		return result, err
	}
	targetDir, err := v.safeProfileDir(fileSet.Tool, target)
	if err != nil {
		return result, err
	}
	incoming, err := readSwitchState(fileSet, targetDir)
	if err != nil {
		return result, err
	}
	if incoming.identityConflict {
		return result, fmt.Errorf("%w: incoming credential has conflicting account identities", ErrInvalidCredentials)
	}
	for name, data := range incoming.files {
		if filepath.Ext(name) == ".json" {
			var obj map[string]json.RawMessage
			if json.Unmarshal(data, &obj) != nil || obj == nil {
				return result, fmt.Errorf("%w: incoming %s must be a JSON object", ErrInvalidCredentials, name)
			}
		}
	}
	if _, err := prepareClaudeSettingsRestore(fileSet, targetDir); err != nil {
		return result, err
	}
	live, err := readSwitchState(fileSet, "")
	if err != nil {
		return result, fmt.Errorf("read outgoing auth: %w", err)
	}

	// This guard precedes all writes, including metadata and the keychain
	// mirror. Activating an older copy of the current account is a no-op.
	if live.sameAccount(incoming) && live.complete && !live.freshness.IsZero() &&
		!incoming.freshness.IsZero() && live.freshness.After(incoming.freshness) {
		result.PreviousProfile = target
		result.KeptLive = true
		return result, nil
	}

	outgoing, saved, err := v.switchOwner(fileSet, live)
	if err != nil {
		return result, err
	}
	result.PreviousProfile = outgoing
	preserved := saved != nil && sameSwitchFiles(live.files, saved.files)
	if preserved {
		recoveryProfile = outgoing
	}
	if outgoing == target && preserved {
		result.KeptLive = true
		return result, nil
	}

	if live.hasAuth() {
		if err := live.checkUnchanged(fileSet); err != nil {
			return result, err
		}
		if opts.PreserveOriginal && outgoing == "" {
			exists, err := v.HasOriginalBackup(fileSet.Tool)
			if err != nil {
				return result, err
			}
			if !exists {
				if _, err := v.saveSwitchRecovery(fileSet, live, originalProfileName); err != nil {
					return result, fmt.Errorf("preserve original auth: %w", err)
				}
				result.OriginalBackup = true
				recoveryProfile = originalProfileName
				preserved = true
			}
		}

		// Only update the credential file of a proven same-account owner.
		// Settings and user metadata are intentionally left as saved. A partial,
		// older, or unmeasured live credential goes into a separate recovery
		// snapshot instead of weakening a possibly working named profile.
		if outgoing != "" && outgoing != target && !IsSystemProfile(outgoing) && !preserved &&
			saved != nil && live.sameAccount(*saved) && live.complete && saved.complete &&
			!live.freshness.IsZero() && !saved.freshness.IsZero() && live.freshness.After(saved.freshness) {
			outgoingDir, err := v.safeProfileDir(fileSet.Tool, outgoing)
			if err != nil {
				return result, err
			}
			current, err := readSwitchState(fileSet, outgoingDir)
			if err != nil || !sameSwitchFiles(saved.files, current.files) {
				return result, fmt.Errorf("outgoing saved profile changed before preservation")
			}
			if err := live.checkUnchanged(fileSet); err != nil {
				return result, err
			}
			if err := writeSwitchFile(filepath.Join(outgoingDir, live.credentialName), live.credential, false); err != nil {
				return result, fmt.Errorf("preserve outgoing profile %s: %w", outgoing, err)
			}
			result.ResnapshottedProfile = outgoing
			recoveryProfile = outgoing
			// Auxiliary credentials can rotate independently. Updating the
			// primary file alone does not preserve a changed secondary source.
			saved.files[live.credentialName] = live.credential
			preserved = sameSwitchFiles(live.files, saved.files)
		}

		if mode == "always" || (mode == "smart" && !preserved) {
			name, err := v.saveSwitchRecovery(fileSet, live, "")
			if err != nil {
				return result, fmt.Errorf("preserve outgoing auth: %w", err)
			}
			result.AutoBackup = name
			recoveryProfile = name
		}
	}

	// Do not clobber a login or target that changed while it was being saved.
	// Successful preservation stays available when this check stops the switch.
	if err := live.checkUnchanged(fileSet); err != nil {
		return result, err
	}
	currentTarget, err := readSwitchState(fileSet, targetDir)
	if err != nil || !sameSwitchFiles(incoming.files, currentTarget.files) {
		return result, fmt.Errorf("incoming profile changed before activation")
	}
	result.RestoreStarted = true
	if err := v.Restore(fileSet, target); err != nil {
		return result, fmt.Errorf("restore target: %w", err)
	}
	if result.AutoBackup != "" && opts.MaxAutoBackups > 0 {
		if err := v.rotateAutoBackups(fileSet.Tool, opts.MaxAutoBackups, result.AutoBackup); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("rotate automatic backups: %v", err))
		}
	}
	return result, nil
}

// CurrentProfile identifies the captured live credential without writing a
// keychain mirror, migrating files, or changing the vault. It can read the
// authoritative keychain where configured. An unproven owner returns an empty
// name; callers must not use an identity hint as permission to overwrite it.
func (v *Vault) CurrentProfile(fileSet AuthFileSet) (string, error) {
	live, err := readSwitchState(fileSet, "")
	if err != nil {
		return "", err
	}
	owner, _, err := v.switchOwner(fileSet, live)
	return owner, err
}

type switchAccount struct {
	account string
	subject string
	email   string
}

func (a switchAccount) matches(b switchAccount) bool {
	// A shared email must never override a conflicting account/workspace ID.
	if a.account != "" && b.account != "" && a.account != b.account {
		return false
	}
	if a.subject != "" && b.subject != "" && a.subject != b.subject {
		return false
	}
	return (a.account != "" && a.account == b.account) ||
		(a.subject != "" && a.subject == b.subject) ||
		(a.email != "" && b.email != "" && strings.EqualFold(a.email, b.email))
}

type switchState struct {
	files            map[string][]byte
	provider         string
	credentialName   string
	credential       []byte
	account          switchAccount
	freshness        time.Time
	complete         bool
	identityConflict bool
}

func (s switchState) hasAuth() bool { return len(bytes.TrimSpace(s.credential)) != 0 }

func (s switchState) sameCredential(other switchState) bool {
	if s.identityConflict || other.identityConflict {
		return false
	}
	if s.account != (switchAccount{}) && other.account != (switchAccount{}) && !s.account.matches(other.account) {
		return false
	}
	return s.hasAuth() && s.credentialName == other.credentialName && bytes.Equal(s.credential, other.credential)
}

func (s switchState) sameAccount(other switchState) bool {
	// OpenAI's subject identifies a person, not their selected workspace.
	// A workspace-scoped grant cannot match an unscoped snapshot by email.
	if s.provider == "codex" && (s.account.account != "" || other.account.account != "") &&
		(s.account.account == "" || other.account.account == "" || s.account.account != other.account.account) {
		return false
	}
	return !s.identityConflict && !other.identityConflict && s.credentialName != "" &&
		s.credentialName == other.credentialName && s.account.matches(other.account)
}

func (s switchState) checkUnchanged(fileSet AuthFileSet) error {
	current, err := readSwitchState(fileSet, "")
	if err != nil || !sameSwitchFiles(s.files, current.files) {
		return fmt.Errorf("live auth changed during switch; retry after the native CLI finishes updating it")
	}
	return nil
}

func sameSwitchFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		other, exists := b[name]
		if !exists || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}

// readSwitchState captures exact bytes without migrations or keychain mirrors.
func readSwitchState(fileSet AuthFileSet, profileDir string) (switchState, error) {
	return readSwitchStateLimited(fileSet, profileDir, 0)
}

// readSwitchStateLimited also bounds unattended discovery reads. A positive
// limit is enforced on the opened file, including growth after the size check.
func readSwitchStateLimited(fileSet AuthFileSet, profileDir string, maxFileBytes int64) (switchState, error) {
	state := switchState{files: make(map[string][]byte)}
	if profileDir != "" {
		for _, dir := range []string{filepath.Dir(profileDir), profileDir} {
			info, err := os.Lstat(dir)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return state, fmt.Errorf("saved auth directory is not a regular directory: %s", dir)
			}
		}
	}
	for _, spec := range fileSet.Files {
		name := filepath.Base(spec.Path)
		path := spec.Path
		if profileDir != "" {
			path = filepath.Join(profileDir, name)
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && profileDir != "" && fileSet.Tool == "gemini" && name == "oauth_creds.json" {
			path = filepath.Join(profileDir, "oauth_credentials.json")
			info, err = os.Lstat(path)
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return state, fmt.Errorf("inspect auth file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 && profileDir == "" {
			info, err = os.Stat(path)
			if err != nil {
				return state, fmt.Errorf("inspect live auth link: %w", err)
			}
		}
		if !info.Mode().IsRegular() {
			return state, fmt.Errorf("auth source is not a regular file: %s", path)
		}
		data, err := readSwitchSource(path, info, maxFileBytes)
		if err != nil {
			return state, fmt.Errorf("read auth file: %w", err)
		}
		if _, exists := state.files[name]; exists {
			return state, fmt.Errorf("auth file set contains duplicate filename: %s", name)
		}
		state.files[name] = data
	}
	if profileDir == "" && claudeKeychainPath(fileSet) != "" {
		data, err := keychain.ReadClaude()
		if err == nil {
			if maxFileBytes > 0 && int64(len(data)) > maxFileBytes {
				return state, fmt.Errorf("%w: Claude keychain credential exceeds discovery size limit", ErrInvalidCredentials)
			}
			state.files[claudeCredentialsFile] = data
		} else if !errors.Is(err, keychain.ErrNoKeychain) && !errors.Is(err, keychain.ErrNotFound) {
			return state, fmt.Errorf("read outgoing Claude keychain: %w", err)
		}
	}
	state.identify(fileSet)
	return state, nil
}

func readSwitchSource(path string, info os.FileInfo, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return os.ReadFile(path)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%w: auth file exceeds discovery size limit", ErrInvalidCredentials)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: auth file exceeds discovery size limit", ErrInvalidCredentials)
	}
	return data, nil
}

func (s *switchState) identify(fileSet AuthFileSet) {
	s.provider = fileSet.Tool
	names := []string{"auth.json", "oauth_creds.json", "antigravity-oauth-token"}
	if fileSet.Tool == "claude" {
		names = []string{claudeCredentialsFile, "auth.json", "settings.json", claudeSettingsFile, "config.json"}
	} else if fileSet.Tool == "agy" {
		names = []string{"antigravity-oauth-token"}
	}
	for _, name := range names {
		data := s.files[name]
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		if fileSet.Tool == "claude" && name != claudeCredentialsFile && name != "auth.json" {
			if ok, _ := claudeCredentialMaterial(data, name); !ok {
				continue
			}
		}
		s.credentialName, s.credential = name, data
		break
	}
	if s.credentialName == "" && fileSet.Tool != "claude" {
		// Opaque provider formats can still be preserved and matched exactly;
		// they never qualify for a rotation-based named snapshot update.
		for _, required := range []bool{true, false} {
			for _, spec := range fileSet.Files {
				if data := s.files[filepath.Base(spec.Path)]; spec.Required == required && len(data) > 0 {
					s.credentialName, s.credential = filepath.Base(spec.Path), data
					break
				}
			}
			if s.credentialName != "" {
				break
			}
		}
	}
	var root map[string]interface{}
	if json.Unmarshal(s.credential, &root) != nil || root == nil {
		return
	}
	entry := root
	switch fileSet.Tool {
	case "claude":
		if s.credentialName != claudeCredentialsFile {
			return
		}
		entry, _ = root["claudeAiOauth"].(map[string]interface{})
		var settings map[string]interface{}
		_ = json.Unmarshal(s.files[claudeSettingsFile], &settings)
		var identityOK bool
		s.account, identityOK = claudeSwitchAccount(entry, claudeIdentityKeys(settings))
		s.identityConflict = !identityOK
		s.complete = jsonString(entry, "accessToken") != "" && jsonString(entry, "refreshToken") != ""
		s.freshness = switchExpiry(entry["expiresAt"], true)
		return
	case "codex":
		if nested, ok := root["tokens"].(map[string]interface{}); ok {
			entry = nested
		}
		s.account = switchAccount{account: jsonString(entry, "account_id")}
		for _, source := range []map[string]interface{}{root, entry} {
			for _, name := range []string{"id_token", "idToken", "access_token", "accessToken"} {
				if account, ok := switchJWTAccount(jsonString(source, name)); ok {
					var consistent bool
					s.account, consistent = mergeSwitchAccount(s.account, account)
					if !consistent {
						s.identityConflict = true
						return
					}
				}
			}
		}
		s.complete = jsonString(entry, "access_token") != "" && jsonString(entry, "refresh_token") != ""
		s.freshness, _ = codexFreshness(s.credential)
		return
	case "grok":
		if jsonString(entry, "access_token") == "" {
			entry = nil
			for _, value := range root {
				candidate, ok := value.(map[string]interface{})
				if !ok || jsonString(candidate, "access_token") == "" {
					continue
				}
				if entry != nil {
					return // Multiple grants have no single proven owner.
				}
				entry = candidate
			}
		}
		s.account = switchAccount{account: jsonString(entry, "user_id"), email: jsonString(entry, "email")}
		s.complete = jsonString(entry, "access_token") != "" && jsonString(entry, "refresh_token") != ""
		s.freshness = switchExpiry(entry["expires_at"], false)
		return
	case "gemini":
		if s.credentialName != "oauth_creds.json" {
			return
		}
		s.account, _ = switchJWTAccount(jsonString(entry, "id_token"))
		s.complete = jsonString(entry, "access_token") != "" && jsonString(entry, "refresh_token") != ""
		s.freshness = switchExpiry(entry["expiry_date"], true)
	}
}

func mergeSwitchAccount(a, b switchAccount) (switchAccount, bool) {
	if (a.account != "" && b.account != "" && a.account != b.account) ||
		(a.subject != "" && b.subject != "" && a.subject != b.subject) ||
		(a.email != "" && b.email != "" && !strings.EqualFold(a.email, b.email)) {
		return switchAccount{}, false
	}
	if a.account == "" {
		a.account = b.account
	}
	if a.subject == "" {
		a.subject = b.subject
	}
	if a.email == "" {
		a.email = b.email
	}
	return a, true
}

// Claude's paired settings and embedded token metadata must agree before
// either may identify the owner of a rotated opaque credential.
func claudeSwitchAccount(oauth map[string]interface{}, paired []string) (switchAccount, bool) {
	var account switchAccount
	for _, key := range []string{"accountId", "email"} {
		if value, exists := oauth[key]; exists {
			if _, ok := value.(string); !ok {
				return switchAccount{}, false
			}
		}
	}
	account.account, account.email = jsonString(oauth, "accountId"), jsonString(oauth, "email")
	for _, key := range paired {
		var other switchAccount
		if value, ok := strings.CutPrefix(key, "uuid:"); ok {
			other.account = value
		} else if value, ok := strings.CutPrefix(key, "email:"); ok {
			other.email = value
		}
		var consistent bool
		account, consistent = mergeSwitchAccount(account, other)
		if !consistent {
			return switchAccount{}, false
		}
	}
	return account, true
}

func switchJWTAccount(token string) (switchAccount, bool) {
	var account switchAccount
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return account, false
	}
	data, err := decodeBase64Segment(parts[1])
	if err != nil {
		return account, false
	}
	var claims map[string]interface{}
	if json.Unmarshal(data, &claims) != nil || claims == nil {
		return account, false
	}
	account.subject, account.email = jsonString(claims, "sub"), jsonString(claims, "email")
	account.account = jsonString(claims, "account_id")
	if nested, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if id := jsonString(nested, "chatgpt_account_id"); id != "" {
			account.account = id
		}
	}
	if account.email == "" {
		if nested, ok := claims["https://api.openai.com/profile"].(map[string]interface{}); ok {
			account.email = jsonString(nested, "email")
		}
	}
	return account, account.account != "" || account.subject != "" || account.email != ""
}

func switchExpiry(raw interface{}, milliseconds bool) time.Time {
	if value, ok := raw.(string); ok {
		parsed, _ := time.Parse(time.RFC3339Nano, value)
		return parsed
	}
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value >= float64(1<<63-1) || math.Trunc(value) != value {
		return time.Time{}
	}
	if milliseconds {
		return time.UnixMilli(int64(value))
	}
	return time.Unix(int64(value), 0)
}

func (v *Vault) switchOwner(fileSet AuthFileSet, live switchState) (string, *switchState, error) {
	return v.switchOwnerLimited(fileSet, live, 0)
}

func (v *Vault) switchOwnerLimited(fileSet AuthFileSet, live switchState, maxFileBytes int64) (string, *switchState, error) {
	profiles, err := v.List(fileSet.Tool)
	if err != nil {
		return "", nil, err
	}
	// Prefer exact credentials over an identity-only match, and named profiles
	// over safety artifacts. Ambiguous identity-only owners are left untouched.
	sort.SliceStable(profiles, func(i, j int) bool { return !IsSystemProfile(profiles[i]) && IsSystemProfile(profiles[j]) })
	var owner string
	var saved *switchState
	var systemOwner string
	var systemSaved *switchState
	ambiguous := false
	for _, name := range profiles {
		dir, err := v.safeProfileDir(fileSet.Tool, name)
		if err != nil {
			continue
		}
		candidate, err := readSwitchStateLimited(fileSet, dir, maxFileBytes)
		if err != nil {
			continue
		}
		if live.sameCredential(candidate) {
			if !IsSystemProfile(name) {
				return name, &candidate, nil
			}
			if systemOwner == "" {
				systemOwner, systemSaved = name, &candidate
			}
		}
		if !IsSystemProfile(name) && live.sameAccount(candidate) {
			if owner != "" {
				ambiguous = true
			}
			owner, saved = name, &candidate
		}
	}
	if ambiguous {
		return systemOwner, systemSaved, nil
	}
	if owner == "" {
		return systemOwner, systemSaved, nil
	}
	return owner, saved, nil
}

// saveSwitchRecovery writes the bytes already inspected, not a second backup
// read that could capture a different account. Exclusive directory creation
// ensures even two switches within a single clock tick cannot overwrite a
// safety artifact. Partial failures retain their bytes and stop activation.
func (v *Vault) saveSwitchRecovery(fileSet AuthFileSet, state switchState, name string) (string, error) {
	toolDir, err := v.safeToolDir(fileSet.Tool)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(toolDir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(toolDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("recovery directory is not a regular directory")
	}
	if name == "" {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", err
		}
		name = "_backup_" + time.Now().Format("20060102_150405.000000000") + "_" + hex.EncodeToString(suffix[:])
	}
	dir, err := v.safeProfileDir(fileSet.Tool, name)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", fmt.Errorf("create immutable recovery snapshot: %w", err)
	}
	var paths []string
	for _, spec := range fileSet.Files {
		filename := filepath.Base(spec.Path)
		data, exists := state.files[filename]
		if !exists {
			continue
		}
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			var root map[string]json.RawMessage
			if err := json.Unmarshal(data, &root); err != nil {
				return "", fmt.Errorf("parse outgoing desktop auth: %w", err)
			}
			fields := make(map[string]json.RawMessage)
			for _, key := range claudeDesktopTokenKeys {
				if value, ok := root[key]; ok {
					fields[key] = value
				}
			}
			if len(fields) == 0 {
				continue
			}
			data, err = json.Marshal(fields)
			if err != nil {
				return "", err
			}
		}
		if err := writeSwitchFile(filepath.Join(dir, filename), data, true); err != nil {
			return "", err
		}
		paths = append(paths, spec.Path)
	}
	createdBy := "auto"
	if name == originalProfileName {
		createdBy = "first-activate"
	}
	meta := map[string]interface{}{
		"tool": fileSet.Tool, "profile": name, "type": "system", "created_by": createdBy,
		"backed_up_at": time.Now().Format(time.RFC3339Nano), "files": len(paths), "original_paths": paths,
	}
	if fileSet.Tool == "claude" {
		var settings map[string]interface{}
		_ = json.Unmarshal(state.files[claudeSettingsFile], &settings)
		keys := claudeIdentityKeys(settings)
		if len(keys) > 0 {
			meta["identity_keys"], meta["identity"] = keys, claudeIdentityLabel(keys)
		}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	if err := writeSwitchFile(filepath.Join(dir, "meta.json"), data, true); err != nil {
		return "", err
	}
	return name, nil
}

func writeSwitchFile(path string, data []byte, exclusive bool) error {
	var file *os.File
	var err error
	if exclusive {
		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	} else {
		file, err = os.CreateTemp(filepath.Dir(path), ".caam-switch-*")
	}
	if err != nil {
		return err
	}
	if !exclusive {
		defer os.Remove(file.Name())
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !exclusive {
		return os.Rename(file.Name(), path)
	}
	return nil
}
