package authfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// isolatedSourceFile records absence as well as presence. A settings file
// appearing during a login is as important as an existing token being replaced.
// No reader in this file follows a source-file symlink or writes to the source.
type isolatedSourceFile struct {
	path  string
	info  os.FileInfo
	data  []byte
	limit int64
}

func readIsolatedSourceFile(path string, required bool, limit int64) (*isolatedSourceFile, error) {
	s := &isolatedSourceFile{path: path, limit: limit}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !required {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read isolated source %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("isolated source must be a regular, non-symlink file: %s", path)
	}
	if limit <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("isolated source exceeds its read limit: %s", path)
	}
	f, err := openIsolatedSourceFile(path)
	if err != nil {
		return nil, fmt.Errorf("open isolated source %s: %w", path, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameIsolatedFileInfo(info, opened) {
		return nil, fmt.Errorf("isolated source changed while opening: %s", path)
	}
	// Check the logical path too: the opened handle alone cannot detect an
	// adoption link or a replaced parent redirecting a future read elsewhere.
	current, err := os.Lstat(path)
	if err != nil || !sameIsolatedFileInfo(opened, current) {
		return nil, fmt.Errorf("isolated source path changed while opening: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read isolated source %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("isolated source exceeds its read limit: %s", path)
	}
	after, err := f.Stat()
	current, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !sameIsolatedFileInfo(opened, after) ||
		!sameIsolatedFileInfo(opened, current) || int64(len(data)) != opened.Size() {
		return nil, fmt.Errorf("isolated source changed while reading: %s", path)
	}
	s.info, s.data = opened, data
	return s, nil
}

func sameIsolatedFileInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && b.Mode().IsRegular() &&
		os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (s *isolatedSourceFile) unchanged() error {
	current, err := readIsolatedSourceFile(s.path, false, s.limit)
	if err != nil {
		return err
	}
	if s.info == nil && current.info == nil {
		return nil
	}
	if !sameIsolatedFileInfo(s.info, current.info) || !bytes.Equal(s.data, current.data) {
		return fmt.Errorf("isolated source changed; finish login/refresh and retry: %s", s.path)
	}
	return nil
}

type isolatedSourceDir struct {
	path string
	info os.FileInfo
}

func inspectIsolatedSourceDir(path string) (isolatedSourceDir, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return isolatedSourceDir{}, fmt.Errorf("inspect isolated directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return isolatedSourceDir{}, fmt.Errorf("isolated source must use non-symlink directories: %s", path)
	}
	return isolatedSourceDir{path: path, info: info}, nil
}

func (s isolatedSourceDir) unchanged() error {
	current, err := inspectIsolatedSourceDir(s.path)
	if err != nil {
		return err
	}
	if !os.SameFile(s.info, current.info) {
		return fmt.Errorf("isolated source directory changed; retry: %s", s.path)
	}
	return nil
}

// validateIsolatedClaudeMetadata checks the registered source before any
// credential is read. A complete OAuth cache is not evidence that the profile
// selects OAuth: an API-key profile may retain credentials from an older login
// while caam exec deliberately permits API-key environment/helper inputs.
// Never reinterpret those leftovers as the account the user asked to save.
func validateIsolatedClaudeMetadata(data []byte, root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("isolated profile root must be absolute")
	}
	var metadata struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		AuthMode string `json:"auth_mode"`
		BasePath string `json:"base_path"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil ||
		metadata.Provider != "claude" || metadata.Name != filepath.Base(root) {
		return fmt.Errorf("isolated source metadata does not identify this Claude profile")
	}
	if metadata.AuthMode != "oauth" {
		return fmt.Errorf("isolated source must select OAuth (auth_mode=oauth); cached OAuth files in other profile modes cannot be imported")
	}
	if metadata.BasePath != "" {
		base, err := filepath.Abs(metadata.BasePath)
		if err != nil || base != root {
			return fmt.Errorf("isolated profile metadata points outside its store location")
		}
	}
	return nil
}

// validateIsolatedClaudeOAuthSelection checks the frozen settings, not the
// caller's environment. auth_mode=oauth and a complete token cache do not prove
// that Claude selects that cache: settings can supply a different credential,
// provider, or endpoint after CAAM sanitizes the inherited environment.
//
// Fail before staging rather than stripping settings, executing a helper, or
// exporting an account whose identity describes an ignored OAuth cache. Only
// known selectors are inspected; unrelated workflow settings remain verbatim.
func validateIsolatedClaudeOAuthSelection(files map[string][]byte) error {
	for _, name := range []string{"settings.json", ".claude.json"} {
		data, present := files[name]
		if !present {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return fmt.Errorf("isolated %s must contain a JSON object", name)
		}
		for _, key := range []string{"apiKeyHelper", "apiKey", "api_key", "primaryApiKey", "oauthToken", "sessionKey", "forceLoginGatewayUrl"} {
			value, err := isolatedSelectionString(object, key)
			if err != nil {
				return fmt.Errorf("isolated %s: %w", name, err)
			}
			if value != "" {
				return fmt.Errorf("isolated %s selects %s instead of an unambiguous stored OAuth login", name, key)
			}
		}
		method, err := isolatedSelectionString(object, "forceLoginMethod")
		if err != nil {
			return fmt.Errorf("isolated %s: %w", name, err)
		}
		if method != "" && method != "claudeai" {
			return fmt.Errorf("isolated %s forceLoginMethod does not select Claude subscription OAuth", name)
		}
		raw, present := object["env"]
		if !present {
			continue
		}
		var env map[string]json.RawMessage
		if json.Unmarshal(raw, &env) != nil || env == nil {
			return fmt.Errorf("isolated %s env must contain a JSON object", name)
		}
		// Portable vault entries must not become different accounts on Windows,
		// where environment variable names are case-insensitive. Inspect every
		// spelling, including duplicate case variants, in deterministic order.
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			canonical := strings.ToUpper(key)
			cloudSwitch := false
			switch canonical {
			case "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
				"CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_MANTLE":
				cloudSwitch = true
			case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS",
				"CLAUDE_CONFIG_DIR",
				"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
				"CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
				"ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN_FILE":
			default:
				continue
			}
			value, err := isolatedSelectionString(env, key)
			if err != nil {
				return fmt.Errorf("isolated %s env.%s must be a string", name, canonical)
			}
			if value == "" {
				continue
			}
			if cloudSwitch {
				switch strings.ToLower(value) {
				case "0", "false", "no", "off":
					continue
				}
			}
			return fmt.Errorf("isolated %s env.%s overrides the stored OAuth login; use a profile with unambiguous OAuth settings", name, canonical)
		}
	}
	return nil
}

// Never embed JSON values or decoder errors in diagnostics: selectors may hold
// API keys, bearer tokens, custom headers, or helper commands containing secrets.
func isolatedSelectionString(object map[string]json.RawMessage, key string) (string, error) {
	raw, present := object[key]
	if !present {
		return "", nil
	}
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return *value, nil
}
