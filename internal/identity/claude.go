package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ExtractFromClaudeCredentials reads Claude .credentials.json and extracts identity.
//
// IMPORTANT: Current Claude auth files (as of early 2026) do NOT include:
//   - accountId: No longer present in claudeAiOauth
//   - email: No longer present in claudeAiOauth
//
// Those fields are instead enriched from a sibling .claude.json file (same
// directory), which carries richer identity data under oauthAccount
// (emailAddress, accountUuid, organizationName, planDisplayName, etc.).
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

	raw, ok := root["claudeAiOauth"].(map[string]interface{})
	if ok {
		identity.AccountID = valueAsString(raw["accountId"])
		identity.PlanType = valueAsString(raw["subscriptionType"])
		identity.Email = valueAsString(raw["email"])
		if exp, ok := parseEpoch(raw["expiresAt"]); ok {
			identity.ExpiresAt = exp
		}
	}

	// If email/accountID still empty, try sibling .claude.json which has
	// oauthAccount with emailAddress, accountUuid, planDisplayName, etc.
	if identity.Email == "" || identity.AccountID == "" {
		enrichFromClaudeJSON(path, identity)
	}

	return identity, nil
}

// ExtractFromClaudeJSON reads a .claude.json file directly and extracts
// identity from the oauthAccount object.
func ExtractFromClaudeJSON(path string) (*Identity, error) {
	identity := &Identity{Provider: "claude"}
	if err := enrichFromClaudeJSONPath(path, identity); err != nil {
		return nil, err
	}
	return identity, nil
}

// enrichFromClaudeJSON looks for .claude.json as a sibling of the given
// .credentials.json path (same directory) and enriches the identity.
func enrichFromClaudeJSON(credentialsPath string, id *Identity) {
	dir := filepath.Dir(credentialsPath)
	_ = enrichFromClaudeJSONPath(filepath.Join(dir, ".claude.json"), id)
}

func enrichFromClaudeJSONPath(path string, id *Identity) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("parse .claude.json: %w", err)
	}

	acct, ok := root["oauthAccount"].(map[string]interface{})
	if !ok {
		return nil
	}

	if id.Email == "" {
		id.Email = valueAsString(acct["emailAddress"])
	}
	if id.AccountID == "" {
		id.AccountID = valueAsString(acct["accountUuid"])
	}
	if id.Organization == "" {
		id.Organization = valueAsString(acct["organizationName"])
	}
	if id.PlanType == "" {
		id.PlanType = valueAsString(acct["planDisplayName"])
		if id.PlanType == "" {
			// Infer from billing type
			if valueAsString(acct["billingType"]) == "stripe_subscription" {
				id.PlanType = "max"
			}
		}
	}

	return nil
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
