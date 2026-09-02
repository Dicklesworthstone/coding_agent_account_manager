package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// claudeSettingsFile is the Claude Code state file that carries the account
// identity block (oauthAccount) next to the credentials.
const claudeSettingsFile = ".claude.json"

// ExtractFromClaudeCredentials reads Claude .credentials.json and extracts identity.
//
// Current Claude auth files (early 2026 onward) carry only expiresAt and
// subscriptionType in claudeAiOauth; accountId and email are no longer written
// there. Claude Code records the account identity in the .claude.json that
// accompanies the credentials instead, under "oauthAccount" (emailAddress,
// accountUuid, organizationName). Whatever claudeAiOauth still provides wins;
// the .claude.json fills in only the fields it left empty, and a missing or
// unreadable .claude.json simply leaves them empty.
//
// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-001)
func ExtractFromClaudeCredentials(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read claude credentials: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse claude credentials: %w", err)
	}

	identity := &Identity{Provider: "claude"}

	if raw, ok := root["claudeAiOauth"].(map[string]interface{}); ok {
		identity.AccountID = valueAsString(raw["accountId"])
		identity.PlanType = valueAsString(raw["subscriptionType"])
		identity.Email = valueAsString(raw["email"])
		if exp, ok := parseEpoch(raw["expiresAt"]); ok {
			identity.ExpiresAt = exp
		}
	}

	fillFromClaudeSettings(identity, claudeSettingsCandidates(path))

	return identity, nil
}

// claudeOAuthAccount is the identity block Claude Code writes into
// .claude.json. The file also holds per-project session state and grows to
// hundreds of kilobytes, so it is decoded into this narrow struct rather than
// a generic map.
type claudeOAuthAccount struct {
	AccountUUID      string `json:"accountUuid"`
	EmailAddress     string `json:"emailAddress"`
	OrganizationName string `json:"organizationName"`
}

type claudeSettingsIdentity struct {
	OAuthAccount *claudeOAuthAccount `json:"oauthAccount"`
}

// claudeSettingsCandidates lists, in precedence order, where the .claude.json
// paired with a credentials file lives. It mirrors how Claude Code itself
// resolves the file: the credentials always sit in the config directory, and
// the state file is <config dir>/.claude.json when CLAUDE_CONFIG_DIR is set
// but ~/.claude.json — one level ABOVE the default ~/.claude config dir — when
// it is not.
//
//   - Credentials in a directory not named ".claude" (a vault snapshot, or an
//     explicit CLAUDE_CONFIG_DIR): only the sibling .claude.json is paired.
//   - Credentials in a ".claude" directory (the live ~/.claude, or a shallow
//     profile's <home>/.claude): the parent's .claude.json is canonical and
//     is consulted first. A .claude.json inside the directory is only a
//     fallback — it is what an older tool that exported CLAUDE_CONFIG_DIR=
//     ~/.claude left behind, and the shallow symlink farm mirrors such a
//     stray file into every profile's .claude/, where it would otherwise
//     outrank the profile's own identity (issue #91). The one layout in which
//     the nested file IS canonical — CLAUDE_CONFIG_DIR currently pointing at
//     that very directory — puts it first again.
func claudeSettingsCandidates(credentialsPath string) []string {
	dir := filepath.Dir(credentialsPath)
	sibling := filepath.Join(dir, claudeSettingsFile)
	if filepath.Base(dir) != ".claude" {
		return []string{sibling}
	}
	parent := filepath.Join(filepath.Dir(dir), claudeSettingsFile)
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" && samePath(cfg, dir) {
		return []string{sibling, parent}
	}
	return []string{parent, sibling}
}

// samePath reports whether a and b name the same directory, resolving
// symlinks when possible.
func samePath(a, b string) bool {
	return canonicalPath(a) == canonicalPath(b)
}

func canonicalPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Clean(p)
}

// fillFromClaudeSettings copies the identity fields that are still empty out
// of the first candidate .claude.json that parses and carries an
// oauthAccount block.
func fillFromClaudeSettings(identity *Identity, candidates []string) {
	for _, path := range candidates {
		account, ok := readClaudeOAuthAccount(path)
		if !ok {
			continue
		}
		if identity.Email == "" {
			identity.Email = account.EmailAddress
		}
		if identity.AccountID == "" {
			identity.AccountID = account.AccountUUID
		}
		if identity.Organization == "" {
			identity.Organization = account.OrganizationName
		}
		return
	}
}

// readClaudeOAuthAccount returns the oauthAccount block of the .claude.json
// at path. ok is false when the file is missing, unparseable, or has no
// oauthAccount.
func readClaudeOAuthAccount(path string) (*claudeOAuthAccount, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var settings claudeSettingsIdentity
	if err := json.Unmarshal(data, &settings); err != nil || settings.OAuthAccount == nil {
		return nil, false
	}
	return settings.OAuthAccount, true
}

func parseEpoch(value interface{}) (time.Time, bool) {
	secs, ok := epochSeconds(value)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

func epochSeconds(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return normalizeEpoch(n), true
	case float64:
		return normalizeEpoch(int64(v)), true
	case float32:
		return normalizeEpoch(int64(v)), true
	case int64:
		return normalizeEpoch(v), true
	case int:
		return normalizeEpoch(int64(v)), true
	case string:
		n, err := json.Number(v).Int64()
		if err != nil {
			return 0, false
		}
		return normalizeEpoch(n), true
	default:
		return 0, false
	}
}

func normalizeEpoch(value int64) int64 {
	// Treat values in milliseconds (13+ digits) as ms since epoch.
	if value > 1_000_000_000_000 {
		return value / 1000
	}
	return value
}
