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
	dir, err := inspectOptionalIsolatedSourceDir(path)
	if err == nil && dir.info == nil {
		err = fmt.Errorf("inspect isolated directory %s: %w", path, os.ErrNotExist)
	}
	return dir, err
}

// Absence participates in source selection too: creating an XDG directory
// while a legacy snapshot is being prepared must invalidate that capture.
func inspectOptionalIsolatedSourceDir(path string) (isolatedSourceDir, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return isolatedSourceDir{path: path}, nil
	}
	if err != nil {
		return isolatedSourceDir{}, fmt.Errorf("inspect isolated directory %s: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return isolatedSourceDir{}, fmt.Errorf("isolated source must use non-symlink directories: %s", path)
	}
	return isolatedSourceDir{path: path, info: info}, nil
}

func (s isolatedSourceDir) unchanged() error {
	current, err := inspectOptionalIsolatedSourceDir(s.path)
	if err != nil {
		return err
	}
	if s.info == nil && current.info == nil {
		return nil
	}
	if s.info == nil || current.info == nil || !os.SameFile(s.info, current.info) {
		return fmt.Errorf("isolated source directory changed; retry: %s", s.path)
	}
	return nil
}

// isolatedClaudeLayout is one complete source namespace. files are eligible
// for export; guards only prove which source was selected. In particular, a
// missing nested legacy state file must stay missing when home/.claude.json
// supplies its identity. No identity or setting is borrowed from XDG.
type isolatedClaudeLayout struct {
	configDir string
	dirs      []isolatedSourceDir
	files     []*isolatedSourceFile
	guards    []*isolatedSourceFile
}

func readIsolatedClaudeLayout(root string, legacy bool, credentialLimit, settingsLimit int64) (*isolatedClaudeLayout, error) {
	parent := filepath.Join(root, "xdg_config")
	dir := filepath.Join(parent, "claude-code")
	if legacy {
		parent = filepath.Join(root, "home")
		dir = filepath.Join(parent, ".claude")
	}
	layout := &isolatedClaudeLayout{configDir: dir}
	for _, path := range []string{parent, dir} {
		captured, err := inspectOptionalIsolatedSourceDir(path)
		if err != nil {
			return nil, err
		}
		layout.dirs = append(layout.dirs, captured)
	}
	for _, name := range []string{".credentials.json", ".claude.json", "auth.json", "settings.json"} {
		limit := credentialLimit
		if name == ".claude.json" || name == "settings.json" {
			limit = settingsLimit
		}
		file, err := readIsolatedSourceFile(filepath.Join(dir, name), false, limit)
		if err != nil {
			return nil, err
		}
		if legacy && name == ".claude.json" && file.info == nil {
			// Match legacyClaudeStatePath: the nested state wins by presence,
			// not validity. A malformed nested state cannot use the home copy.
			layout.guards = append(layout.guards, file)
			file, err = readIsolatedSourceFile(filepath.Join(parent, name), false, limit)
			if err != nil {
				return nil, err
			}
		}
		layout.files = append(layout.files, file)
	}
	return layout, nil
}

func (s *isolatedClaudeLayout) data() map[string][]byte {
	files := make(map[string][]byte)
	for _, file := range s.files {
		if file.info != nil {
			files[filepath.Base(file.path)] = file.data
		}
	}
	return files
}

func (s *isolatedClaudeLayout) hasPrimary() bool {
	for _, file := range s.files {
		name := filepath.Base(file.path)
		if file.info != nil && (name == ".credentials.json" || name == "auth.json") {
			return true
		}
	}
	return false
}

// hasAuth mirrors the provider's presence check for conflict detection. An
// identity-only .claude.json is not a login, but a credential cache (even an
// invalid one), a helper or environment credential is. Malformed documents
// participate too; they cannot hide a known conflicting account identity.
func (s *isolatedClaudeLayout) hasAuth() bool {
	if s.hasPrimary() {
		return true
	}
	files := s.data()
	for _, name := range []string{"settings.json", ".claude.json"} {
		data, present := files[name]
		if !present {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil || object == nil {
			return true
		}
		keys := []string{"oauthToken", "sessionKey", "apiKey", "api_key", "primaryApiKey"}
		if name == "settings.json" {
			keys = []string{"apiKeyHelper", "apiKey", "api_key"}
		}
		for _, key := range keys {
			value, err := isolatedSelectionString(object, key)
			if err != nil || strings.TrimSpace(value) != "" {
				return true
			}
		}
		if name == "settings.json" {
			if raw, present := object["env"]; present {
				var env map[string]json.RawMessage
				if json.Unmarshal(raw, &env) != nil || env == nil {
					return true
				}
				for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
					value, err := isolatedSelectionString(env, key)
					if err != nil || strings.TrimSpace(value) != "" {
						return true
					}
				}
			}
		}
	}
	return false
}

// accountID is only conflict evidence, not credential validation or proof of
// account equivalence. Prefer embedded accountId, then the layout's own paired
// state, like the provider. Even an otherwise unusable cache must not conceal
// an explicit conflicting ID. Never return token values in diagnostics.
func (s *isolatedClaudeLayout) accountID() string {
	files := s.data()
	var credentials struct {
		OAuth struct {
			AccountID string `json:"accountId"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(files[".credentials.json"], &credentials) == nil {
		if id := strings.TrimSpace(credentials.OAuth.AccountID); id != "" {
			return id
		}
	}
	var state struct {
		Account struct {
			ID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(files[".claude.json"], &state) == nil {
		return strings.TrimSpace(state.Account.ID)
	}
	return ""
}

// selectIsolatedClaudeLayout follows the provider's XDG-first selection for
// file-backed OAuth profiles, without calling provider code that may follow
// adoption links or update state. Malformed/alternate-auth settings fail closed;
// an expired, empty, or incomplete XDG credential never falls back to legacy.
// The caller must keep EVERY returned capture unchanged through publication.
func selectIsolatedClaudeLayout(root string, credentialLimit, settingsLimit int64) (*isolatedClaudeLayout, []*isolatedClaudeLayout, error) {
	xdg, err := readIsolatedClaudeLayout(root, false, credentialLimit, settingsLimit)
	if err != nil {
		return nil, nil, err
	}
	if err := validateIsolatedClaudeOAuthSelection(xdg.data()); err != nil {
		return nil, nil, err
	}
	legacy, err := readIsolatedClaudeLayout(root, true, credentialLimit, settingsLimit)
	if err != nil {
		return nil, nil, err
	}
	captures := []*isolatedClaudeLayout{xdg, legacy}
	if xdg.hasPrimary() {
		// caam exec rejects known conflicting accounts even when XDG would
		// otherwise win. Capture legacy as a guard, not as exported material,
		// and keep its absence/identity stable through final publication.
		if legacy.hasAuth() {
			xdgID, legacyID := xdg.accountID(), legacy.accountID()
			if xdgID != "" && legacyID != "" && xdgID != legacyID {
				return nil, nil, fmt.Errorf("isolated Claude profile contains conflicting accounts in legacy and XDG config directories; resolve the source accounts before backing up")
			}
		}
		return xdg, captures, nil
	}
	if err := validateIsolatedClaudeOAuthSelection(legacy.data()); err != nil {
		return nil, nil, err
	}
	if legacy.hasPrimary() {
		return legacy, captures, nil
	}
	return xdg, captures, nil // Neither store has a login; validation will fail.
}

func (s *isolatedClaudeLayout) unchanged() error {
	checkDirs := func() error {
		for _, dir := range s.dirs {
			if err := dir.unchanged(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkDirs(); err != nil {
		return err
	}
	for _, files := range [][]*isolatedSourceFile{s.files, s.guards} {
		for _, file := range files {
			if err := file.unchanged(); err != nil {
				return err
			}
		}
	}
	return checkDirs()
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
