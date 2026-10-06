package claudesettings

import (
	"encoding/json"
	"fmt"
)

// .claude.json is NOT settings.json: it also holds internal account/session
// caches. Share only known workflow fields here, not arbitrary internal state.
// User/project MCP registrations and trust/permission state live in this file.
// Credential-bearing MCP/hooks can be opted into profile_keys (projects scopes
// the complete per-project map, including nested MCP configurations).
var legacySharedKeys = []string{
	"mcpServers", "projects", "enabledMcpServers", "disabledMcpServers",
	"permissions", "autoMode", "model", "effortLevel", "hooks", "enabledPlugins",
}

var legacyAuthKeys = []string{
	"oauthAccount", "oauthToken", "sessionKey", "apiKey", "api_key", "primaryApiKey",
}

// MergeLegacy preserves known live workflow fields in ~/.claude.json while
// replacing all account/session state with the selected profile's snapshot.
func MergeLegacy(shared, account []byte, p Policy) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	live, err := object(shared)
	if err != nil {
		return nil, fmt.Errorf("live .claude.json: %w", err)
	}
	target, err := object(account)
	if err != nil {
		return nil, fmt.Errorf("profile .claude.json: %w", err)
	}
	if shared == nil && account == nil {
		return nil, nil
	}
	if p.Mode != "per-profile" && shared != nil {
		for _, key := range legacySharedKeys {
			if contains(p.ProfileKeys, key) {
				continue
			}
			delete(target, key)
			if value, ok := live[key]; ok {
				target[key] = value
			}
		}
	}
	data, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// LegacyIdentity excludes installation IDs and workflow state. A retained
// policy-only .claude.json is not evidence of authentication after logout.
func LegacyIdentity(data []byte) ([]byte, error) {
	obj, err := object(data)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]json.RawMessage)
	for _, key := range legacyAuthKeys {
		if value, ok := obj[key]; ok {
			fields[key] = value
		}
	}
	// Canonicalize nested account objects using the same lossless machinery.
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return Identity(raw, Policy{ProfileKeys: legacyAuthKeys})
}

func PrepareLegacyRestore(snapshotPath, livePath string, p Policy) (*Update, error) {
	return prepare(snapshotPath, livePath, livePath, p, MergeLegacy)
}

func PrepareLegacyRefresh(sharedPath, profilePath string, p Policy) (*Update, error) {
	return PrepareLegacyImport(sharedPath, profilePath, profilePath, p)
}

func PrepareLegacyImport(sharedPath, accountPath, destination string, p Policy) (*Update, error) {
	return prepare(accountPath, sharedPath, destination, p, MergeLegacy)
}

func PrepareLegacyClear(path string, p Policy) (*Update, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	p.Mode = "shared"
	return PrepareLegacyRestore("", path, p)
}
