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
	"apiKeyHelper", "awsAuthRefresh", "awsCredentialExport",
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
	account, err := Read(snapshotPath)
	if err != nil {
		return nil, fmt.Errorf("read profile settings: %w", err)
	}
	live, err := Read(livePath)
	if err != nil {
		return nil, fmt.Errorf("read live settings: %w", err)
	}
	merged, err := Merge(live, account, p)
	if err != nil {
		return nil, err
	}
	return &Update{path: livePath, before: live, after: merged}, nil
}

// PrepareRefresh applies real-home policy to an isolated profile while taking
// auth only from that profile. It never copies the real home's credentials.
func PrepareRefresh(sharedPath, profilePath string, p Policy) (*Update, error) {
	shared, err := Read(sharedPath)
	if err != nil {
		return nil, err
	}
	account, err := Read(profilePath)
	if err != nil {
		return nil, err
	}
	merged, err := Merge(shared, account, p)
	if err != nil {
		return nil, err
	}
	return &Update{path: profilePath, before: account, after: merged}, nil
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

// Apply checks for edits since preparation, then writes via a private, fsynced
// temporary file and atomic rename. It does not follow a destination symlink
// when writing, so profile settings can never overwrite a shared source file.
func (u *Update) Apply() error {
	current, err := Read(u.path)
	if err != nil {
		return err
	}
	if (current == nil) != (u.before == nil) || !bytes.Equal(current, u.before) {
		return fmt.Errorf("Claude settings changed during activation; retry")
	}
	if u.after == nil {
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
	return os.Rename(tmp, u.path)
}
