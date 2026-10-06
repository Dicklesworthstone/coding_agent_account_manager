// Package claudesettings defines the lifecycle of Claude's mixed policy/auth
// settings.json. Shared workflow state comes from the live machine; account
// state comes exclusively from the selected profile, including absent keys.
package claudesettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Policy is stored in caam's config.json as claude_settings. The zero value
// shares non-auth settings. Env is conservatively account-scoped because
// arbitrary environment variables can select credentials, helpers or gateways.
type Policy struct {
	Mode          string   `json:"mode,omitempty"`
	ProfileKeys   []string `json:"profile_keys,omitempty"`
	SharedEnvKeys []string `json:"shared_env_keys,omitempty"`
}

var authKeys = []string{
	"apiKeyHelper", "apiKey", "api_key", "awsAuthRefresh", "awsCredentialExport",
	"forceLoginMethod", "forceLoginOrgUUID", "otelHeadersHelper",
}

// Validate rejects misspelled modes and unsafe attempts to share known auth or
// routing environment variables. Unknown variables require explicit operator
// classification; never infer safety from their current values.
func (p Policy) Validate() error {
	if p.Mode != "" && p.Mode != "shared" && p.Mode != "per-profile" {
		return fmt.Errorf("claude_settings.mode must be shared or per-profile")
	}
	for _, key := range p.ProfileKeys {
		if key == "" || key != strings.TrimSpace(key) || key == "env" {
			return fmt.Errorf("claude_settings.profile_keys requires non-empty top-level keys other than env")
		}
	}
	for _, key := range p.SharedEnvKeys {
		if key == "" || key != strings.TrimSpace(key) || strings.ContainsAny(key, "=\x00") || sensitiveEnv(key) {
			return fmt.Errorf("claude_settings.shared_env_keys contains an invalid or auth/routing variable: %q", key)
		}
	}
	return nil
}

func sensitiveEnv(key string) bool {
	key = strings.ToUpper(key)
	// Known model/effort controls are not credentials or backend selectors.
	// They still require explicit opt-in; prefix filtering must not make the
	// documented non-auth environment controls impossible to share.
	switch key {
	case "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_MAX_OUTPUT_TOKENS":
		return false
	}
	for _, prefix := range []string{"ANTHROPIC_", "AWS_", "GOOGLE_", "GCLOUD_", "CLOUD_ML_", "AZURE_", "CLAUDE_CODE_USE_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	for _, part := range []string{"AUTH", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "API_KEY", "BEARER", "HEADER", "BASE_URL", "PROXY", "CERT"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

func contains(keys []string, key string) bool {
	for _, candidate := range keys {
		if candidate == key {
			return true
		}
	}
	return false
}

func (p Policy) scoped(key string) bool {
	return key == "env" || contains(authKeys, key) || contains(p.ProfileKeys, key)
}

func object(data []byte) (map[string]json.RawMessage, error) {
	obj := make(map[string]json.RawMessage)
	if data == nil { // A missing file is distinct from an empty/corrupt file.
		return obj, nil
	}
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("settings must contain a JSON object")
	}
	if raw, ok := obj["env"]; ok {
		var env map[string]json.RawMessage
		if err := json.Unmarshal(raw, &env); err != nil || env == nil {
			return nil, fmt.Errorf("settings env must contain a JSON object")
		}
	}
	return obj, nil
}

func envOf(obj map[string]json.RawMessage) map[string]json.RawMessage {
	env := make(map[string]json.RawMessage)
	// object has already validated the shape.
	if raw, ok := obj["env"]; ok {
		_ = json.Unmarshal(raw, &env)
	}
	return env
}

// Merge combines shared policy with account settings. nil denotes a missing
// file. With no shared file, an existing snapshot bootstraps the settings for
// recovery/first use. An existing {} is authoritative: deleted rules are never
// resurrected from a stale snapshot. No recursive union of permissions occurs.
// In per-profile mode the account document wins in its entirety.
func Merge(shared, account []byte, p Policy) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	live, err := object(shared)
	if err != nil {
		return nil, fmt.Errorf("live settings: %w", err)
	}
	target, err := object(account)
	if err != nil {
		return nil, fmt.Errorf("profile settings: %w", err)
	}
	if shared == nil && account == nil {
		return nil, nil
	}
	result := make(map[string]json.RawMessage)
	if p.Mode == "per-profile" || shared == nil {
		result = target
	} else {
		for key, value := range live {
			if !p.scoped(key) {
				result[key] = value
			}
		}
		for key, value := range target {
			if p.scoped(key) && key != "env" {
				result[key] = value
			}
		}
		env := envOf(target)
		sharedEnv := envOf(live)
		for _, key := range p.SharedEnvKeys {
			delete(env, key)
			if value, ok := sharedEnv[key]; ok {
				env[key] = value
			}
		}
		if len(env) > 0 {
			result["env"], _ = json.Marshal(env)
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Identity returns a canonical representation of account-scoped fields for
// active-profile detection. Changes to shared policy must not change identity.
// The mode does not affect classification: even per-profile preferences drift.
func Identity(data []byte, p Policy) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	obj, err := object(data)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]json.RawMessage)
	for key, value := range obj {
		if p.scoped(key) && key != "env" {
			fields[key] = value
		}
	}
	env := envOf(obj)
	for _, key := range p.SharedEnvKeys {
		delete(env, key)
	}
	if len(env) > 0 {
		fields["env"], _ = json.Marshal(env)
	}
	// RawMessage preserves large numbers during merging. Canonicalize nested
	// objects too so harmless whitespace/key ordering does not change identity.
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var canonical interface{}
	if err := decoder.Decode(&canonical); err != nil {
		return nil, err
	}
	return json.Marshal(canonical)
}

// Update is a validated, prepared write. Preparing every settings destination
// before swapping credentials makes malformed settings a preflight failure.
type Update struct {
	path   string
	before []byte
	after  []byte
	detach bool
	inputs settingsInputs
}

// settingsInputs captures each path once, including absence. In a refresh the
// account and destination are the same file: a second read could otherwise
// pair old account data with a newer "before" value and overwrite that edit.
// Paths are absolute so a later working-directory change cannot retarget them.
type settingsInputs map[string][]byte

func (inputs settingsInputs) read(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if data, ok := inputs[abs]; ok {
		return data, nil
	}
	data, err := Read(abs)
	if err != nil {
		return nil, err
	}
	inputs[abs] = data
	return data, nil
}

func (u *Update) checkUnchanged() error {
	current, err := Read(u.path)
	if err != nil {
		return err
	}
	if (current == nil) != (u.before == nil) || !bytes.Equal(current, u.before) {
		return fmt.Errorf("Claude settings changed during activation; retry")
	}
	for path, before := range u.inputs {
		current, err := Read(path)
		if err != nil {
			return fmt.Errorf("read Claude settings source during activation: %w", err)
		}
		if (current == nil) != (before == nil) || !bytes.Equal(current, before) {
			return fmt.Errorf("Claude settings source changed during activation; retry")
		}
	}
	return nil
}

// Changed reports whether the prepared document differs or a shared symlink
// must become a private file. Repeated refreshes leave private files untouched.
func (u *Update) Changed() bool {
	return u.detach || (u.before == nil) != (u.after == nil) || !bytes.Equal(u.before, u.after)
}

// RequirePrivateFile also breaks a recognized shared hard link, whose contents
// may already match the desired policy but whose future writes are not private.
func (u *Update) RequirePrivateFile() {
	u.detach = true
	if u.after == nil {
		u.after = []byte("{}\n")
	}
}

func preparedUpdate(path string, before, after []byte) (*Update, error) {
	if path == "" {
		return nil, fmt.Errorf("Claude settings destination is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat destination settings: %w", err)
	}
	detach := err == nil && info.Mode()&os.ModeSymlink != 0
	if detach && after == nil {
		// A dangling shared link must not start exposing another account's
		// settings when its target is created later.
		after = []byte("{}\n")
	}
	return &Update{path: path, before: before, after: after, detach: detach}, nil
}

// Read returns nil for a missing file, but rejects unreadable files.
func Read(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// PrepareRestore plans a vault-to-live activation, even if the snapshot has no
// settings file. In that case account fields are removed, not carried over.
func PrepareRestore(snapshotPath, livePath string, p Policy) (*Update, error) {
	return prepare(snapshotPath, livePath, livePath, p, Merge)
}

// PrepareImport combines canonical shared policy with an explicitly selected
// auth source, rather than treating the destination's previous auth as current.
func PrepareImport(sharedPath, accountPath, destination string, p Policy) (*Update, error) {
	return prepare(accountPath, sharedPath, destination, p, Merge)
}

func prepare(accountPath, sharedPath, destination string, p Policy, merge func([]byte, []byte, Policy) ([]byte, error)) (*Update, error) {
	inputs := make(settingsInputs)
	account, err := inputs.read(accountPath)
	if err != nil {
		return nil, fmt.Errorf("read profile settings: %w", err)
	}
	live, err := inputs.read(sharedPath)
	if err != nil {
		return nil, fmt.Errorf("read live settings: %w", err)
	}
	before, err := inputs.read(destination)
	if err != nil {
		return nil, fmt.Errorf("read destination settings: %w", err)
	}
	if live == nil && account == nil && before != nil {
		// An explicitly imported account without a settings document must
		// still clear the previous account's helpers and routing variables.
		// The destination can supply policy, but never account authentication.
		live = before
	}
	merged, err := merge(live, account, p)
	if err != nil {
		return nil, err
	}
	update, err := preparedUpdate(destination, before, merged)
	if err != nil {
		return nil, err
	}
	update.inputs = inputs
	return update, nil
}

// PrepareRefresh applies real-home policy to an isolated profile while taking
// auth only from that profile. It never copies the real home's credentials.
func PrepareRefresh(sharedPath, profilePath string, p Policy) (*Update, error) {
	return PrepareImport(sharedPath, profilePath, profilePath, p)
}

// PrepareClear scrubs account fields on logout without deleting policy. The
// shared/per-profile switch governs activation, not destructive logout behavior.
func PrepareClear(path string, p Policy) (*Update, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	p.Mode = "shared"
	return PrepareRestore("", path, p)
}

// Apply checks all source and destination inputs, then writes via a private,
// fsynced temporary file and atomic rename. Sources are rechecked after staging
// too: a revoked permission or rotated helper must not be silently reinstalled.
// These checks detect intervening edits, not a filesystem-wide transaction.
func (u *Update) Apply() error {
	if err := u.checkUnchanged(); err != nil {
		return err
	}
	if u.after == nil {
		return nil
	}
	info, err := os.Lstat(u.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	detach := err == nil && info.Mode()&os.ModeSymlink != 0
	if !u.Changed() && !detach {
		return nil
	}
	dir := filepath.Dir(u.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "settings.json.tmp.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(u.after); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := u.checkUnchanged(); err != nil {
		return err
	}
	return os.Rename(tmp, u.path)
}

// PrepareAPIKeyHelper changes only this profile's helper during enrollment;
// setup must not erase already initialized permissions, MCP or hook policy.
func PrepareAPIKeyHelper(path, helper string) (*Update, error) {
	before, err := Read(path)
	if err != nil {
		return nil, err
	}
	obj, err := object(before)
	if err != nil {
		return nil, err
	}
	obj["apiKeyHelper"], _ = json.Marshal(helper)
	after, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return nil, err
	}
	return preparedUpdate(path, before, append(after, '\n'))
}
