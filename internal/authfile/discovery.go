package authfile

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

// MaxDiscoveryFileBytes bounds unattended reads of each native auth artifact.
// Watchers use the same limit while fingerprinting file changes.
const MaxDiscoveryFileBytes int64 = 16 << 20

// DiscoveryResult describes a native login captured without changing live
// files. Identity is extracted from the credential bytes that were saved;
// profile names and unrelated settings are never proof of account ownership.
type DiscoveryResult struct {
	Profile   string             `json:"profile"`
	Identity  *identity.Identity `json:"identity,omitempty"`
	Created   bool               `json:"created,omitempty"`
	Updated   bool               `json:"updated,omitempty"`
	Unchanged bool               `json:"unchanged,omitempty"`
	KeptNewer bool               `json:"kept_newer,omitempty"`
}

type discoverySnapshot struct {
	state           switchState
	material        map[string][]byte
	identity        *identity.Identity
	observedAccount switchAccount
}

// CaptureDiscovery saves a complete native authentication source, or a proven
// newer rotation of an existing source. It does not mirror keychains, execute
// helpers, refresh tokens, or change native files. Missing identity is normal:
// an unproven login receives its own user profile instead of replacing another
// account whose label happens to match. Absent credentials return
// ErrNoCredentials; present malformed or incomplete credentials return
// ErrInvalidCredentials and leave the vault untouched.
func (v *Vault) CaptureDiscovery(fileSet AuthFileSet) (*DiscoveryResult, error) {
	switchMu.Lock()
	defer switchMu.Unlock()

	liveState, err := readSwitchStateLimited(fileSet, "", MaxDiscoveryFileBytes)
	if err != nil {
		return nil, fmt.Errorf("read native %s credentials: %w", fileSet.Tool, err)
	}
	live, err := inspectDiscovery(fileSet, liveState)
	if err != nil {
		return nil, err
	}
	result := &DiscoveryResult{Identity: live.identity}

	// Reuse switch ownership to prioritize known named profiles. Inspecting
	// the captured candidates below additionally handles alternate credential
	// sources and ignores policy-only changes inside mixed settings files.
	preferred, _, err := v.switchOwnerLimited(fileSet, live.state, MaxDiscoveryFileBytes)
	if err != nil {
		return nil, err
	}
	profiles, err := v.List(fileSet.Tool)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		if IsSystemProfile(profiles[i]) != IsSystemProfile(profiles[j]) {
			return !IsSystemProfile(profiles[i])
		}
		return profiles[i] == preferred && profiles[j] != preferred
	})
	var owner string
	var saved discoverySnapshot
	ambiguous := false
	for _, profile := range profiles {
		dir, err := v.safeProfileDir(fileSet.Tool, profile)
		if err != nil {
			continue
		}
		state, err := readSwitchStateLimited(fileSet, dir, MaxDiscoveryFileBytes)
		if err != nil {
			continue
		}
		candidate, err := inspectDiscovery(fileSet, state)
		if err != nil {
			continue
		}
		if !IsSystemProfile(profile) && sameSwitchFiles(live.material, candidate.material) {
			result.Profile, result.Unchanged = profile, true
			return result, nil
		}
		if !IsSystemProfile(profile) && sameDiscoveryOwner(live, candidate) {
			if owner != "" {
				// Multiple named accounts can intentionally share credentials.
				// A fresh native grant must not silently choose one to replace.
				ambiguous = true
			}
			owner, saved = profile, candidate
		}
	}
	if owner != "" && !ambiguous {
		newerKnown := !saved.state.freshness.IsZero() && !live.state.freshness.IsZero()
		if newerKnown && saved.state.freshness.After(live.state.freshness) && onlyDiscoveryPrimaryChanged(live, saved) {
			result.Profile, result.Unchanged, result.KeptNewer = owner, true, true
			return result, nil
		}
		if newerKnown && live.state.freshness.After(saved.state.freshness) &&
			live.state.complete && saved.state.complete && onlyDiscoveryPrimaryChanged(live, saved) {
			dir, err := v.safeProfileDir(fileSet.Tool, owner)
			if err != nil {
				return nil, err
			}
			current, err := readSwitchStateLimited(fileSet, dir, MaxDiscoveryFileBytes)
			if err != nil || !sameSwitchFiles(current.files, saved.state.files) {
				return nil, fmt.Errorf("saved %s profile changed during discovery; retry", fileSet.Tool)
			}
			if err := checkDiscoveryUnchanged(fileSet, liveState); err != nil {
				return nil, err
			}
			// A single atomic credential replacement preserves the profile's
			// policy, aliases, tags, notes, and any unknown metadata verbatim.
			if err := writeSwitchFile(filepath.Join(dir, live.state.credentialName), live.state.credential, false); err != nil {
				return nil, fmt.Errorf("save discovered %s rotation: %w", fileSet.Tool, err)
			}
			result.Profile, result.Updated = owner, true
			return result, nil
		}
	}
	if err := checkDiscoveryUnchanged(fileSet, liveState); err != nil {
		return nil, err
	}
	profile, err := v.saveDiscovery(fileSet, liveState, live.identity)
	if err != nil {
		return nil, err
	}
	result.Profile, result.Created = profile, true
	return result, nil
}

// DiscoveryFileFingerprint lets a watcher coalesce authentication changes
// without being kept busy by unrelated settings writes. Malformed credential
// states retain a fingerprint so changing bad input can be retried. This is an
// observation hint, never a substitute for CaptureDiscovery validation.
func DiscoveryFileFingerprint(tool, filename string, data []byte) string {
	filename = filepath.Base(filename)
	material, err := discoveryMaterial(tool, filename, data)
	if tool == "claude" && filename == "settings.json" && err == nil {
		material, err = discoveryClaudeSettingsMaterial(data)
	}
	if tool == "claude" && filename == claudeSettingsFile && err == nil {
		if obj, parseErr := discoveryObject(data); parseErr == nil {
			if raw, exists := obj["oauthAccount"]; exists {
				selected := map[string]json.RawMessage{"credential": material, "oauthAccount": raw}
				if len(material) == 0 {
					delete(selected, "credential")
				}
				material, _ = json.Marshal(selected)
			}
		}
	}
	if err != nil {
		material = data
	}
	if len(material) == 0 && err == nil {
		return ""
	}
	hash := sha256.Sum256(material)
	return hex.EncodeToString(hash[:])
}

func onlyDiscoveryPrimaryChanged(live, saved discoverySnapshot) bool {
	if live.state.credentialName != saved.state.credentialName || len(live.material) != len(saved.material) {
		return false
	}
	for name, material := range live.material {
		if name == live.state.credentialName {
			continue
		}
		if !bytes.Equal(material, saved.material[name]) {
			return false
		}
	}
	return true
}

func sameDiscoveryOwner(live, saved discoverySnapshot) bool {
	if discoveryAccountsConflict(live.observedAccount, saved.observedAccount) {
		return false
	}
	if live.state.sameAccount(saved.state) {
		return true
	}
	if live.state.credentialName != saved.state.credentialName || live.state.provider != saved.state.provider ||
		live.state.identityConflict || saved.state.identityConflict || discoveryAccountsConflict(live.state.account, saved.state.account) {
		return false
	}
	if live.state.provider == "codex" && live.state.account.account != saved.state.account.account {
		return false // A shared person or refresh token does not change workspace scope.
	}
	// Some native CLIs replace only the access token and expiry. A stable
	// nonempty refresh token proves grant continuity even when tokens are
	// opaque and the account has no discoverable email or ID.
	refresh := discoveryRefreshToken(live.state)
	return refresh != "" && refresh == discoveryRefreshToken(saved.state)
}

func discoveryAccountsConflict(a, b switchAccount) bool {
	return (a.account != "" && b.account != "" && a.account != b.account) ||
		(a.subject != "" && b.subject != "" && a.subject != b.subject) ||
		(a.email != "" && b.email != "" && !strings.EqualFold(a.email, b.email))
}

func discoveryRefreshToken(state switchState) string {
	var root map[string]interface{}
	if json.Unmarshal(state.credential, &root) != nil {
		return ""
	}
	entry := root
	if state.provider == "claude" && state.credentialName == claudeCredentialsFile {
		entry, _ = root["claudeAiOauth"].(map[string]interface{})
	} else if state.provider == "codex" {
		if nested, ok := root["tokens"].(map[string]interface{}); ok {
			entry = nested
		}
	} else if state.provider == "grok" && jsonString(root, "key") == "" && jsonString(root, "access_token") == "" && jsonString(root, "accessToken") == "" {
		entry = nil
		for _, value := range root {
			if nested, ok := value.(map[string]interface{}); ok {
				if entry != nil {
					return ""
				}
				entry = nested
			}
		}
	}
	for _, key := range []string{"refresh_token", "refreshToken", "refresh"} {
		if token := jsonString(entry, key); strings.TrimSpace(token) != "" {
			return token
		}
	}
	return ""
}

func checkDiscoveryUnchanged(fileSet AuthFileSet, captured switchState) error {
	current, err := readSwitchStateLimited(fileSet, "", MaxDiscoveryFileBytes)
	if err != nil {
		return fmt.Errorf("native %s credentials changed during discovery; retry after the CLI finishes writing", fileSet.Tool)
	}
	before, beforeErr := inspectDiscovery(fileSet, captured)
	after, afterErr := inspectDiscovery(fileSet, current)
	if beforeErr != nil || afterErr != nil || before.observedAccount != after.observedAccount || !sameSwitchFiles(before.material, after.material) {
		return fmt.Errorf("native %s credentials changed during discovery; retry after the CLI finishes writing", fileSet.Tool)
	}
	return nil
}

func inspectDiscovery(fileSet AuthFileSet, state switchState) (discoverySnapshot, error) {
	snapshot := discoverySnapshot{state: state, material: make(map[string][]byte), observedAccount: state.account}
	if state.identityConflict {
		return snapshot, fmt.Errorf("%w: %s credential identity conflicts with its captured account state", ErrInvalidCredentials, fileSet.Tool)
	}
	for _, spec := range fileSet.Files {
		name := filepath.Base(spec.Path)
		data, exists := state.files[name]
		if !exists {
			continue
		}
		material, err := discoveryMaterial(fileSet.Tool, name, data)
		if err != nil {
			return snapshot, fmt.Errorf("%w: %s %s: %v", ErrInvalidCredentials, fileSet.Tool, name, err)
		}
		if len(material) != 0 {
			snapshot.material[name] = material
		}
	}
	if len(snapshot.material) == 0 {
		return snapshot, fmt.Errorf("%w: no complete native %s authentication source", ErrNoCredentials, fileSet.Tool)
	}
	if fileSet.Tool == "claude" {
		// Private helper/routing/environment state belongs to the selected
		// account even when OAuth is the primary credential. Settings alone
		// still cannot establish a login: completeness was checked above.
		if data, exists := state.files["settings.json"]; exists {
			material, err := discoveryClaudeSettingsMaterial(data)
			if err != nil {
				return snapshot, fmt.Errorf("%w: Claude account settings: %v", ErrInvalidCredentials, err)
			}
			if len(material) > 0 {
				snapshot.material["settings.json"] = material
			}
		}
	}
	// settings.json is required by the historic Gemini fileset, but OAuth or
	// .env API keys authenticate independently. For agy the shared Google
	// cache never substitutes for the authoritative Antigravity token.
	if fileSet.Tool == "agy" && len(snapshot.material["antigravity-oauth-token"]) == 0 {
		return snapshot, fmt.Errorf("%w: Antigravity token is missing", ErrNoCredentials)
	}
	if len(snapshot.material[state.credentialName]) == 0 {
		for _, name := range []string{claudeCredentialsFile, "auth.json", "antigravity-oauth-token", "oauth_creds.json", ".env", "settings.json", claudeSettingsFile, "config.json"} {
			if len(snapshot.material[name]) > 0 {
				snapshot.state.credentialName, snapshot.state.credential = name, state.files[name]
				break
			}
		}
	}
	if err := discoveryIdentity(&snapshot); err != nil {
		return snapshot, fmt.Errorf("%w: %s credential identity is conflicting or malformed", ErrInvalidCredentials, fileSet.Tool)
	}
	return snapshot, nil
}

// discoveryMaterial returns only authentication material. Policy and display
// identity alone are never evidence of a login, including in required files.
func discoveryMaterial(tool, name string, data []byte) ([]byte, error) {
	if tool == "agy" {
		if name != "antigravity-oauth-token" {
			return nil, nil
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return nil, fmt.Errorf("authoritative token is empty")
		}
		if trimmed[0] != '{' && trimmed[0] != '[' {
			return trimmed, nil // The native file can be an opaque token.
		}
		obj, err := discoveryObject(data)
		if err != nil {
			return nil, err
		}
		ok, err := nonemptyCredentialString(obj, "token", "access_token", "accessToken")
		if err != nil || !ok {
			return nil, fmt.Errorf("authoritative token object has no usable token")
		}
		return json.Marshal(obj)
	}
	if tool == "gemini" && name == ".env" {
		return discoveryEnvMaterial(data)
	}
	primary := name == "auth.json" || name == claudeCredentialsFile || name == "oauth_creds.json"
	if !primary && !(tool == "claude" && (name == "settings.json" || name == claudeSettingsFile || name == "config.json")) &&
		!(tool == "gemini" && name == "settings.json") {
		return nil, nil
	}
	obj, err := discoveryObject(data)
	if err != nil {
		if !primary {
			return nil, nil // A partially written policy file is not a login.
		}
		return nil, err
	}
	if tool == "claude" {
		ok, err := claudeCredentialMaterial(data, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			if primary {
				return nil, fmt.Errorf("credential source has no access credential")
			}
			return nil, nil
		}
		if name == claudeCredentialsFile {
			oauth, err := discoveryObject(obj["claudeAiOauth"])
			if err != nil {
				return nil, err
			}
			if _, err := discoveryGrant(oauth, []string{"accessToken"}, []string{"refreshToken"}, nil); err != nil {
				return nil, err
			}
		} else if name == "auth.json" {
			if _, err := discoveryGrant(obj, []string{"access_token", "accessToken"}, []string{"refresh_token", "refreshToken"}, []string{"apiKey", "api_key"}); err != nil {
				return nil, err
			}
		}
		if name == "settings.json" || name == claudeSettingsFile || name == "config.json" {
			if name == "settings.json" {
				return discoveryClaudeSettingsMaterial(data)
			}
			keys := []string{"apiKeyHelper", "apiKey", "api_key", "primaryApiKey", "oauthToken", "sessionKey"}
			if name == "config.json" {
				keys = claudeDesktopTokenKeys
			}
			material := discoveryFields(obj, keys...)
			return json.Marshal(material)
		}
		return json.Marshal(obj)
	}
	entry := obj
	if tool == "codex" {
		if raw, exists := obj["last_refresh"]; exists {
			var stamp string
			if json.Unmarshal(raw, &stamp) != nil {
				return nil, fmt.Errorf("last_refresh must be a timestamp")
			}
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				return nil, fmt.Errorf("last_refresh must be a timestamp")
			}
		}
		if raw, exists := obj["tokens"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			entry, err = discoveryObject(raw)
			if err != nil {
				return nil, fmt.Errorf("tokens must be an object")
			}
		} else if ok, _ := nonemptyCredentialString(obj, "OPENAI_API_KEY"); ok {
			return json.Marshal(obj)
		}
	}
	access := []string{"access_token", "accessToken"}
	refresh := []string{"refresh_token", "refreshToken"}
	apiKeys := []string{"api_key", "apiKey", "OPENAI_API_KEY"}
	if tool == "opencode" || tool == "grok" {
		if tool == "grok" {
			access = []string{"key", "access_token", "accessToken"}
			apiKeys = []string{"api_key", "apiKey"}
		}
		if tool == "opencode" {
			access, refresh, apiKeys = []string{"access", "access_token", "accessToken"}, []string{"refresh", "refresh_token", "refreshToken"}, []string{"key", "api_key", "apiKey"}
		}
		if discoveryHasFields(obj, append(append(append([]string{}, access...), refresh...), apiKeys...)...) {
			ok, err := discoveryGrant(obj, access, refresh, apiKeys)
			if err != nil || !ok {
				return nil, fmt.Errorf("incomplete credential entry")
			}
			return json.Marshal(obj)
		}
		found := false
		for _, raw := range obj {
			candidate, err := discoveryObject(raw)
			if err != nil {
				return nil, fmt.Errorf("credential entry must be an object")
			}
			ok, err := discoveryGrant(candidate, access, refresh, apiKeys)
			if err != nil || !ok {
				return nil, fmt.Errorf("incomplete credential entry")
			}
			found = true
		}
		if !found {
			return nil, fmt.Errorf("credential source is empty")
		}
		return json.Marshal(obj)
	}
	ok, err := discoveryGrant(entry, access, refresh, apiKeys)
	if err != nil {
		return nil, err
	}
	if !ok {
		if primary {
			return nil, fmt.Errorf("credential source has no access credential")
		}
		return nil, nil
	}
	if tool == "gemini" && name == "settings.json" {
		return json.Marshal(discoveryFields(obj, append(append(access, refresh...), apiKeys...)...))
	}
	return json.Marshal(obj)
}

func discoveryClaudeSettingsMaterial(data []byte) ([]byte, error) {
	if _, err := discoveryObject(data); err != nil {
		return nil, nil // Unfinished optional policy is not a credential change.
	}
	policy, err := claudeSettingsPolicy()
	if err != nil {
		return nil, err
	}
	material, err := claudesettings.Identity(data, policy)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(material, []byte("{}")) {
		return nil, nil
	}
	return material, nil
}

func discoveryObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return obj, nil
}

func discoveryFields(obj map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	selected := make(map[string]json.RawMessage)
	for _, key := range keys {
		if raw, exists := obj[key]; exists {
			selected[key] = raw
		}
	}
	return selected
}

func discoveryHasFields(obj map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, exists := obj[key]; exists {
			return true
		}
	}
	return false
}

func discoveryGrant(obj map[string]json.RawMessage, accessKeys, refreshKeys, apiKeys []string) (bool, error) {
	if _, err := nonemptyCredentialString(obj, "account_id", "accountId", "user_id", "email", "user_email", "client_email", "id_token", "idToken"); err != nil {
		return false, err
	}
	for _, key := range []string{"id_token", "idToken", "access_token", "accessToken", "access", "key"} {
		var token string
		_ = json.Unmarshal(obj[key], &token)
		if err := validateDiscoveryJWTIdentity(token); err != nil {
			return false, err
		}
	}
	access, err := discoveryCredentialAliases(obj, accessKeys)
	if err != nil {
		return false, err
	}
	refresh, err := discoveryCredentialAliases(obj, refreshKeys)
	if err != nil {
		return false, err
	}
	if discoveryHasFields(obj, refreshKeys...) && !refresh {
		return false, fmt.Errorf("refresh credential is empty")
	}
	if access {
		for _, key := range []string{"expiresAt", "expires_at", "expiry_date", "expires"} {
			if raw, exists := obj[key]; exists {
				var value interface{}
				if json.Unmarshal(raw, &value) != nil || switchExpiry(value, false).IsZero() {
					return false, fmt.Errorf("credential expiry is malformed")
				}
			}
		}
		return true, nil
	}
	if refresh || discoveryHasFields(obj, accessKeys...) {
		return false, fmt.Errorf("access credential is missing")
	}
	return discoveryCredentialAliases(obj, apiKeys)
}

func discoveryCredentialAliases(obj map[string]json.RawMessage, keys []string) (bool, error) {
	var selected string
	for _, key := range keys {
		raw, exists := obj[key]
		if !exists {
			continue
		}
		var value string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return false, fmt.Errorf("%s must be a string", key)
		}
		value = strings.TrimSpace(value)
		if selected != "" && value != "" && value != selected {
			return false, fmt.Errorf("credential aliases contain conflicting values")
		}
		if value != "" {
			selected = value
		}
	}
	return selected != "", nil
}

func validateDiscoveryJWTIdentity(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil // Opaque credentials need not have JWT identity metadata.
	}
	data, err := decodeBase64Segment(parts[1])
	if err != nil {
		return nil
	}
	claims, err := discoveryObject(data)
	if err != nil {
		return nil
	}
	if _, err := nonemptyCredentialString(claims, "sub", "account_id", "email"); err != nil {
		return fmt.Errorf("token identity field is malformed")
	}
	for _, key := range []string{"iat", "exp"} {
		if raw, exists := claims[key]; exists {
			var stamp float64
			if json.Unmarshal(raw, &stamp) != nil || switchExpiry(stamp, false).IsZero() {
				return fmt.Errorf("token timestamp is malformed")
			}
		}
	}
	for _, namespace := range []string{"https://api.openai.com/auth", "https://api.openai.com/profile"} {
		if raw, exists := claims[namespace]; exists {
			nested, err := discoveryObject(raw)
			if err != nil {
				return fmt.Errorf("token identity namespace is malformed")
			}
			if _, err := nonemptyCredentialString(nested, "chatgpt_account_id", "email"); err != nil {
				return fmt.Errorf("token identity field is malformed")
			}
		}
	}
	return nil
}

func discoveryEnvMaterial(data []byte) ([]byte, error) {
	keys := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if key != "GEMINI_API_KEY" && key != "GOOGLE_API_KEY" {
			continue
		}
		value = strings.TrimSpace(value)
		if !found || value == "" {
			return nil, fmt.Errorf("API key assignment is empty")
		}
		if value[0] == '\'' || value[0] == '"' {
			if len(value) < 2 || value[len(value)-1] != value[0] {
				return nil, fmt.Errorf("API key assignment has an unterminated quote")
			}
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("API key assignment is empty")
		}
		keys[key] = value
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return json.Marshal(keys)
}

func discoveryIdentity(snapshot *discoverySnapshot) error {
	s := &snapshot.state
	var root map[string]interface{}
	_ = json.Unmarshal(s.credential, &root)
	entry := root
	if s.provider == "codex" {
		if nested, ok := root["tokens"].(map[string]interface{}); ok {
			entry = nested
		}
	}
	if s.provider == "claude" {
		// Paired settings can still describe the previous login during an
		// atomic credential replacement. Only credential-carried identity
		// authorizes overwriting a previously saved grant during discovery.
		s.account, s.identityConflict = switchAccount{}, false
		if s.credentialName == claudeCredentialsFile {
			entry, _ = root["claudeAiOauth"].(map[string]interface{})
			var valid bool
			s.account, valid = claudeSwitchAccount(entry, nil)
			if !valid {
				return fmt.Errorf("invalid account fields")
			}
		}
	}
	if s.provider == "gemini" {
		// Legacy settings may contain an actual credential and its own
		// identity. Separate settings never label an OAuth or .env source.
		if s.credentialName == "settings.json" || s.credentialName == "oauth_creds.json" {
			for _, key := range []string{"email", "user_email", "client_email"} {
				if email := jsonString(entry, key); email != "" {
					var valid bool
					s.account, valid = mergeSwitchAccount(s.account, switchAccount{email: email})
					if !valid {
						return fmt.Errorf("conflicting account fields")
					}
				}
			}
		}
	}
	if s.provider == "cursor" {
		for _, key := range []string{"accessToken", "access_token"} {
			if account, ok := switchJWTAccount(jsonString(entry, key)); ok {
				var valid bool
				s.account, valid = mergeSwitchAccount(s.account, account)
				if !valid {
					return fmt.Errorf("conflicting account fields")
				}
			}
		}
		s.complete = (jsonString(entry, "accessToken") != "" || jsonString(entry, "access_token") != "") &&
			(jsonString(entry, "refreshToken") != "" || jsonString(entry, "refresh_token") != "")
	}
	if s.provider == "grok" {
		// Native Grok versions use both key and access_token for access
		// credentials, in flat and dynamically keyed credential objects.
		if jsonString(root, "key") == "" && jsonString(root, "access_token") == "" && jsonString(root, "accessToken") == "" {
			entry = nil
			for _, value := range root {
				candidate, ok := value.(map[string]interface{})
				if !ok {
					continue
				}
				if entry != nil {
					entry = nil
					break // Multiple grants have no single proven owner.
				}
				entry = candidate
			}
		}
		s.account = switchAccount{account: jsonString(entry, "user_id"), email: jsonString(entry, "email")}
		s.complete = (jsonString(entry, "key") != "" || jsonString(entry, "access_token") != "" || jsonString(entry, "accessToken") != "") &&
			(jsonString(entry, "refresh_token") != "" || jsonString(entry, "refreshToken") != "")
		s.freshness = switchExpiry(entry["expires_at"], false)
	}
	if s.identityConflict {
		return fmt.Errorf("conflicting account fields")
	}
	id := &identity.Identity{Provider: s.provider, Email: s.account.email, AccountID: s.account.account}
	if id.AccountID == "" {
		id.AccountID = s.account.subject
	}
	if s.provider == "claude" {
		id.PlanType = jsonString(entry, "subscriptionType")
		id.ExpiresAt = switchExpiry(entry["expiresAt"], true)
	}
	for _, key := range []string{"id_token", "idToken", "access_token", "accessToken"} {
		if tokenID, err := identity.ExtractFromJWT(jsonString(entry, key)); err == nil {
			if id.PlanType == "" {
				id.PlanType = tokenID.PlanType
			}
			if id.Organization == "" {
				id.Organization = tokenID.Organization
			}
			if id.ExpiresAt.IsZero() {
				id.ExpiresAt = tokenID.ExpiresAt
			}
		}
	}
	if s.provider == "cursor" {
		s.freshness = id.ExpiresAt
	}
	snapshot.identity = id
	return nil
}

func (v *Vault) saveDiscovery(fileSet AuthFileSet, state switchState, ident *identity.Identity) (string, error) {
	toolDir, err := v.safeToolDir(fileSet.Tool)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(toolDir, 0700); err != nil {
		return "", fmt.Errorf("create discovery vault: %w", err)
	}
	info, err := os.Lstat(toolDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("discovery vault is not a regular directory")
	}
	// Build next to the vault, outside List/ListAll's visible profile tree.
	// Rename publishes a complete directory on every supported platform.
	stage, err := os.MkdirTemp(filepath.Dir(filepath.Dir(toolDir)), ".caam-discovery-*")
	if err != nil {
		return "", fmt.Errorf("stage discovered profile: %w", err)
	}
	defer os.RemoveAll(stage)
	var paths []string
	for _, spec := range fileSet.Files {
		name := filepath.Base(spec.Path)
		data, exists := state.files[name]
		if !exists {
			continue
		}
		if filepath.Ext(name) == ".json" {
			obj, err := discoveryObject(data)
			if err != nil {
				continue // Ignore unfinished optional policy already vetted above.
			}
			if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
				fields := discoveryFields(obj, claudeDesktopTokenKeys...)
				if len(fields) == 0 {
					continue
				}
				data, _ = json.Marshal(fields)
			}
			if fileSet.Tool == "claude" && name == claudeSettingsFile {
				policy, err := claudeSettingsPolicy()
				if err != nil {
					return "", err
				}
				// Validate against the same merge semantics Restore will use,
				// including project maps and account/session field isolation.
				if _, err := claudesettings.MergeLegacy(data, data, policy); err != nil {
					return "", fmt.Errorf("%w: captured Claude session state: %v", ErrInvalidCredentials, err)
				}
			}
		}
		if err := writeSwitchFile(filepath.Join(stage, name), data, true); err != nil {
			return "", fmt.Errorf("stage discovered credential: %w", err)
		}
		paths = append(paths, spec.Path)
	}
	if err := validateCredentialFiles(fileSet, stage); err != nil {
		return "", fmt.Errorf("validate discovered profile: %w", err)
	}
	name := strings.TrimSpace(ident.Email)
	if name == "" {
		name = strings.TrimSpace(ident.AccountID)
	}
	if _, err := validateVaultSegment("profile", name); err != nil || IsSystemProfile(name) || len(name) > 200 {
		name = ""
	}
	for attempts := 0; attempts < 5; attempts++ {
		if name == "" {
			var suffix [8]byte
			if _, err := rand.Read(suffix[:]); err != nil {
				return "", err
			}
			name = "auto-" + time.Now().UTC().Format("20060102-150405.000000000") + "-" + hex.EncodeToString(suffix[:])
		}
		dest, err := v.safeProfileDir(fileSet.Tool, name)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(dest); err == nil {
			name = ""
			continue
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect discovered profile destination: %w", err)
		}
		meta := map[string]interface{}{
			"tool": fileSet.Tool, "profile": name, "type": "user", "created_by": "auto",
			"backed_up_at": time.Now().UTC().Format(time.RFC3339Nano), "files": len(paths), "original_paths": paths,
		}
		if ident.Email != "" {
			meta["identity"] = ident.Email
		}
		data, err := json.Marshal(meta)
		if err != nil {
			return "", err
		}
		if err := writeSwitchFile(filepath.Join(stage, "meta.json"), data, false); err != nil {
			return "", fmt.Errorf("stage discovery metadata: %w", err)
		}
		if err := checkDiscoveryUnchanged(fileSet, state); err != nil {
			return "", err
		}
		if err := os.Rename(stage, dest); err != nil {
			return "", fmt.Errorf("publish discovered profile: %w", err)
		}
		return name, nil
	}
	return "", fmt.Errorf("cannot allocate a unique discovered profile name")
}
