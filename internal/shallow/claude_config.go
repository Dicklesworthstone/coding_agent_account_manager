package shallow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Key policy for a shallow claude profile's <home>/.claude.json.
//
// The file is the one piece of claude state that is REAL (private) per
// profile, because Claude Code writes the login identity into it. But it also
// carries plain preferences (theme, editor mode, notification channel, global
// MCP servers) and per-project approvals (trust, allowed tools, project MCP
// servers). Two operations touch it, and they share one classification so
// they can never disagree about which side of the boundary a key is on:
//
//   - Seeding (Create without an explicit .claude.json source): the real
//     ~/.claude.json is copied MINUS claudeAccountKeys, so a fresh profile
//     starts with the user's preferences and onboarding state but no other
//     account's identity or usage cache (issue #92). An explicit source
//     (--from-claude-json, or a vault snapshot) is copied verbatim: there the
//     identity is the point.
//   - Refresh on spawn (SyncClaudeConfig): claudeSharedPreferenceKeys and, per
//     project, claudeSharedProjectKeys are copied from the real file into the
//     profile, real side winning, so a setting changed in a plain `claude`
//     session reaches every shallow lane (issue #93). Everything else in the
//     profile — identity, usage caches, prompt history, per-project session
//     stats — is left exactly as the profile has it.
//
// The refresh is an allowlist on purpose: the file is written by a fast-moving
// tool, and copying "everything except identity" would let the real lane's
// volatile session state overwrite each profile's own on every spawn.

// claudeAccountKeys are the top-level .claude.json keys bound to the logged-in
// account: who it is, what it may use, and how much it has used. They are
// stripped when a profile is seeded from the real HOME and never refreshed.
//
// The top-level userID is deliberately NOT here: it is the installation's
// anonymous id, shared by every account logged in on the same machine (see
// the authfile package's identity-key notes), so carrying it over is correct.
var claudeAccountKeys = []string{
	"oauthAccount",            // accountUuid, emailAddress, organization…
	"cachedUsageUtilization",  // the account's quota/usage snapshot
	"modelAccessCache",        // models this account may use
	"orgModelDefaultCache",    // the account's org default model
	"passesEligibilityCache",  // per-account pass credits
	"passesLastSeenRemaining", // "
	"cachedExtraUsageDisabledReason",
}

// claudeSharedPreferenceKeys are top-level user preferences that describe how
// the operator likes Claude Code to behave, independent of which account is
// logged in. They are refreshed from the real HOME on every spawn.
var claudeSharedPreferenceKeys = []string{
	"theme",
	"editorMode",
	"preferredNotifChannel",
	"autoUpdates",
	"verbose",
	"autoCompactEnabled",
	"diffTool",
	"parallelTasksCount",
	"todoFeatureEnabled",
	"messageIdleNotifThresholdMs",
	"autoConnectIde",
	"shiftEnterKeyBindingInstalled",
	"mcpServers", // user-scope MCP servers
}

// claudeSharedProjectKeys are the per-project (projects.<path>.*) approvals
// and configuration that the same operator made for the same directory on
// the same machine; they are refreshed from the real HOME on every spawn.
// Session state under a project (history, lastCost, lastSessionId, …) is
// never touched.
var claudeSharedProjectKeys = []string{
	"allowedTools",
	"hasTrustDialogAccepted",
	"mcpServers",
	"mcpContextUris",
	"enabledMcpjsonServers",
	"disabledMcpjsonServers",
	"hasClaudeMdExternalIncludesApproved",
	"hasClaudeMdExternalIncludesWarningShown",
}

// seedClaudeJSONFromRealHome returns the bytes to write as a fresh profile's
// .claude.json when the seed is the user's real ~/.claude.json: the file with
// claudeAccountKeys removed. A source that is not a JSON object cannot carry
// an identity, so it is returned verbatim.
func seedClaudeJSONFromRealHome(src string) ([]byte, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	var state map[string]json.RawMessage
	if json.Unmarshal(raw, &state) != nil || state == nil {
		return raw, nil
	}
	for _, k := range claudeAccountKeys {
		delete(state, k)
	}
	return marshalClaudeJSON(state)
}

// SyncClaudeConfig refreshes the shared preference keys of a claude shallow
// profile's .claude.json from the user's real ~/.claude.json (issue #93). It
// is a no-op for non-claude profiles and when the real HOME has no
// .claude.json. It returns a description of every key it changed, sorted;
// nothing is written when there is no change. The profile's identity, usage
// caches and session state are never touched (see the key policy above).
func (m *Manager) SyncClaudeConfig(name string) ([]string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return nil, err
	}
	provider, err := m.ResolveProvider(name)
	if err != nil {
		return nil, err
	}
	if provider != "claude" {
		return nil, nil
	}

	realRaw, err := os.ReadFile(filepath.Join(m.realHome, ".claude.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read real .claude.json: %w", err)
	}
	var real map[string]json.RawMessage
	if err := json.Unmarshal(realRaw, &real); err != nil || real == nil {
		return nil, fmt.Errorf("real .claude.json is not a JSON object")
	}

	// The profile's file must be a real, private file. Refusing a symlink
	// means a corrupted profile can never turn this into a write to the real
	// HOME (writeFileAtomic replaces the path, but be explicit).
	profilePath := filepath.Join(home, ".claude.json")
	st, err := os.Lstat(profilePath)
	switch {
	case err == nil && st.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symlink; refusing to refresh a shared file", profilePath)
	case err == nil && !st.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", profilePath)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("stat profile .claude.json: %w", err)
	}
	profile := map[string]json.RawMessage{}
	if err == nil {
		raw, rerr := os.ReadFile(profilePath)
		if rerr != nil {
			return nil, fmt.Errorf("read profile .claude.json: %w", rerr)
		}
		if strings.TrimSpace(string(raw)) != "" {
			if json.Unmarshal(raw, &profile) != nil || profile == nil {
				return nil, fmt.Errorf("profile .claude.json is not a JSON object; leaving it alone")
			}
		}
	}

	var changed []string
	for _, k := range claudeSharedPreferenceKeys {
		v, ok := real[k]
		if !ok {
			continue
		}
		if !rawJSONEqual(v, profile[k]) {
			profile[k] = v
			changed = append(changed, k)
		}
	}

	projChanged, err := syncClaudeProjects(real, profile)
	if err != nil {
		return nil, err
	}
	changed = append(changed, projChanged...)

	if len(changed) == 0 {
		return nil, nil
	}
	sort.Strings(changed)
	out, err := marshalClaudeJSON(profile)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(profilePath, out, 0o600); err != nil {
		return nil, fmt.Errorf("write profile .claude.json: %w", err)
	}
	return changed, nil
}

// syncClaudeProjects copies claudeSharedProjectKeys for every project the
// real file knows into the profile's projects map (creating the project entry
// when the profile has none), mutating profile in place. It returns a
// "projects.<path>.<key>" entry per changed field.
func syncClaudeProjects(real, profile map[string]json.RawMessage) ([]string, error) {
	realRaw, ok := real["projects"]
	if !ok {
		return nil, nil
	}
	var realProjects map[string]map[string]json.RawMessage
	if json.Unmarshal(realRaw, &realProjects) != nil || len(realProjects) == 0 {
		return nil, nil
	}
	profileProjects := map[string]map[string]json.RawMessage{}
	if raw, ok := profile["projects"]; ok && len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &profileProjects); err != nil {
			return nil, fmt.Errorf("profile .claude.json has a malformed \"projects\" map; leaving it alone")
		}
	}

	var changed []string
	for path, realEntry := range realProjects {
		if realEntry == nil {
			continue
		}
		entry := profileProjects[path]
		if entry == nil {
			entry = map[string]json.RawMessage{}
		}
		for _, k := range claudeSharedProjectKeys {
			v, ok := realEntry[k]
			if !ok {
				continue
			}
			if !rawJSONEqual(v, entry[k]) {
				entry[k] = v
				changed = append(changed, "projects."+path+"."+k)
			}
		}
		if len(entry) > 0 {
			profileProjects[path] = entry
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	out, err := json.Marshal(profileProjects)
	if err != nil {
		return nil, fmt.Errorf("encode projects: %w", err)
	}
	profile["projects"] = out
	return changed, nil
}

// rawJSONEqual compares two JSON values ignoring insignificant whitespace.
// A nil/empty side is only equal to another nil/empty side.
func rawJSONEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// marshalClaudeJSON renders a .claude.json object the way Claude Code writes
// it (two-space indent, trailing newline).
func marshalClaudeJSON(state map[string]json.RawMessage) ([]byte, error) {
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode .claude.json: %w", err)
	}
	return append(out, '\n'), nil
}
