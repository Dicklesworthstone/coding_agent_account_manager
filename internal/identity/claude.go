package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// claudeSettingsFile is the Claude Code settings file that carries the account
// identity block.
const claudeSettingsFile = ".claude.json"

// ExtractFromClaudeCredentials reads Claude .credentials.json and extracts identity.
//
// Current Claude auth files (as of early 2026) carry only expiresAt and
// subscriptionType in claudeAiOauth: accountId and email are no longer written
// there. The account identity lives in the .claude.json that accompanies the
// credentials, under oauthAccount (accountUuid / emailAddress /
// organizationName), so that file supplies whatever the credentials omit.
// Whatever the credentials still carry wins, and a missing or unparseable
// .claude.json simply leaves the identity fields empty.
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

	applyClaudeSettingsIdentity(identity, path)

	return identity, nil
}

// claudeOAuthAccount is the identity block of a .claude.json. The file also
// carries per-project session history and runs to hundreds of kilobytes, so
// it is decoded into this struct rather than a generic map: the decoder then
// walks the rest of the document without allocating it.
type claudeOAuthAccount struct {
	AccountUUID      string `json:"accountUuid"`
	EmailAddress     string `json:"emailAddress"`
	OrganizationName string `json:"organizationName"`
}

// applyClaudeSettingsIdentity fills the identity fields absent from
// .credentials.json out of the oauthAccount block of the .claude.json that
// accompanies it. Fields already read from the credentials are left alone.
func applyClaudeSettingsIdentity(identity *Identity, credentialsPath string) {
	for _, settingsPath := range claudeSettingsCandidates(credentialsPath) {
		account, ok := readClaudeOAuthAccount(settingsPath)
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

// claudeSettingsCandidates lists the .claude.json locations that pair with a
// .credentials.json at credentialsPath: the same directory (vault profile
// snapshots and CLAUDE_CONFIG_DIR layouts keep the two side by side) and its
// parent (the live ~/.claude/.credentials.json pairs with ~/.claude.json).
func claudeSettingsCandidates(credentialsPath string) []string {
	dir := filepath.Dir(credentialsPath)
	return []string{
		filepath.Join(dir, claudeSettingsFile),
		filepath.Join(filepath.Dir(dir), claudeSettingsFile),
	}
}

// readClaudeOAuthAccount parses the oauthAccount block out of a .claude.json.
// The bool reports whether the file yielded any identity at all: unreadable,
// unparseable, oauthAccount-less, and legacy bare-string oauthAccount files
// all report false so the next candidate path gets a turn.
func readClaudeOAuthAccount(path string) (claudeOAuthAccount, bool) {
	var root struct {
		OAuthAccount claudeOAuthAccount `json:"oauthAccount"`
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return claudeOAuthAccount{}, false
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return claudeOAuthAccount{}, false
	}
	return root.OAuthAccount, root.OAuthAccount != claudeOAuthAccount{}
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
