package claudesettings

import (
	"encoding/json"
	"fmt"
)

// .claude.json is NOT settings.json: it also holds internal account/session
// caches. Share only known workflow fields here, not arbitrary internal state.
// Per-project policy is merged separately so history and session/usage caches
// never cross accounts. profile_keys can keep a whole top-level field private;
// "projects" opts the complete per-project map out of sharing.
var legacySharedKeys = []string{
	"mcpServers", "enabledMcpServers", "disabledMcpServers",
	"permissions", "autoMode", "model", "effortLevel", "hooks", "enabledPlugins",
	"theme", "editorMode", "preferredNotifChannel", "autoUpdates", "verbose",
	"autoCompactEnabled", "diffTool", "parallelTasksCount", "todoFeatureEnabled",
	"messageIdleNotifThresholdMs", "autoConnectIde", "shiftEnterKeyBindingInstalled",
}

var legacySharedProjectKeys = []string{
	"allowedTools", "hasTrustDialogAccepted", "mcpServers", "mcpContextUris",
	"enabledMcpjsonServers", "disabledMcpjsonServers",
	"hasClaudeMdExternalIncludesApproved", "hasClaudeMdExternalIncludesWarningShown",
}

var legacyAuthKeys = []string{
	"oauthAccount", "oauthToken", "sessionKey", "apiKey", "api_key", "primaryApiKey",
}

// LegacySharedPolicyKeys returns an independent copy of the legacy workflow
// allowlist for callers checking enrollment invariants. MergeLegacy remains
// the authority for applying policy, including configurable profile scopes.
func LegacySharedPolicyKeys() []string {
	return append([]string(nil), legacySharedKeys...)
}

// MergeLegacy preserves known live workflow fields in ~/.claude.json while
// replacing all account/session state with the selected profile's snapshot.
// Absence is authoritative for shared keys, including project approvals: a
// deleted permission or MCP registration must not return on the next switch.
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
		if !contains(p.ProfileKeys, "projects") {
			if err := mergeLegacyProjects(live, target); err != nil {
				return nil, err
			}
		}
	}
	data, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// legacyProjects validates every entry before the caller changes anything.
// A missing/null map or null entry carries no workflow policy; other non-object
// values are corruption, not a reason to silently discard existing approvals.
func legacyProjects(obj map[string]json.RawMessage) (map[string]map[string]json.RawMessage, error) {
	projects := make(map[string]map[string]json.RawMessage)
	if raw, ok := obj["projects"]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return nil, fmt.Errorf("projects must contain a JSON object with object entries")
		}
	}
	if projects == nil {
		projects = make(map[string]map[string]json.RawMessage)
	}
	return projects, nil
}

func mergeLegacyProjects(live, target map[string]json.RawMessage) error {
	shared, err := legacyProjects(live)
	if err != nil {
		return fmt.Errorf("live .claude.json: %w", err)
	}
	private, err := legacyProjects(target)
	if err != nil {
		return fmt.Errorf("profile .claude.json: %w", err)
	}

	// Start with only the selected account's project-local state, stripping
	// every previously shared field even for projects deleted in the real home.
	for path, entry := range private {
		for _, key := range legacySharedProjectKeys {
			delete(entry, key)
		}
		if len(entry) == 0 {
			delete(private, path)
		}
	}
	for path, entry := range shared {
		for _, key := range legacySharedProjectKeys {
			if value, ok := entry[key]; ok {
				if private[path] == nil {
					private[path] = make(map[string]json.RawMessage)
				}
				private[path][key] = value
			}
		}
	}
	delete(target, "projects")
	if len(private) > 0 {
		raw, err := json.Marshal(private)
		if err != nil {
			return fmt.Errorf("encode profile projects: %w", err)
		}
		target["projects"] = raw
	}
	return nil
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
	update, err := PrepareLegacyImport(sharedPath, profilePath, profilePath, p)
	if err != nil {
		return nil, err
	}
	changed, err := update.ChangedKeys()
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 && (update.before == nil) == (update.after == nil) {
		// A refresh with no semantic changes must not reformat a private
		// document on every launch, including per-profile mode. Apply still
		// checks for intervening edits and detaches shared links when needed.
		update.after = update.before
	}
	return update, nil
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
