package shallow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Key policy for a shallow codex profile's <home>/.codex/config.toml
// (issue #103), the Codex counterpart to claude_config.go.
//
// A shallow codex profile's config.toml is a REAL, private file: CODEX_HOME is
// pinned at it, Codex rewrites parts of it at runtime, and caam forces
// file-based credential storage into it. Since #46 it is seeded from the real
// ~/.codex/config.toml once, at creation — and then never reconciled. A real
// MCP entry that moves from the stdio transport to streamable HTTP therefore
// leaves every shallow profile holding the old command/args block, and Codex
// aborts config parsing with "url is not supported for stdio in
// mcp_servers.<name>".
//
// Three classes of key, and the boundary between them is the deliverable:
//
//  1. caam-enforced. `cli_auth_credentials_store = "file"` is what keeps a
//     shallow profile's credentials in its own HOME rather than a shared
//     keychain. It is forced on every sync and is never taken from the real
//     config, whatever the real config says.
//
//  2. Profile-local mutable. Runtime state, hook trust and project trust are
//     decisions made inside THIS profile's home, about this profile. They are
//     never read from the real config and never overwritten:
//
//     - [hooks.state.*]  — per-home trust hashes for hook scripts
//     - [projects.*]     — per-home workspace trust levels
//     - [notice.*]       — per-home dismissal of one-time notices
//
//  3. Shared. Everything else in the real config describes how the operator
//     wants Codex to behave, is identical across lanes, and is refreshed with
//     the real side winning: root keys (model, reasoning effort, personality,
//     notify, service_tier, …) and whole tables ([mcp_servers.*], [features],
//     [skills], [hooks] proper, [model_providers.*], [tui], [history], …).
//
// Two rules make this safe to run repeatedly:
//
//   - Sections are replaced as a UNIT, never merged key-by-key. For an MCP
//     server that is the whole point: [mcp_servers.kernel] and its subtables
//     are dropped and re-inserted together, so a stale `command`/`args` pair
//     cannot survive beside a new `url`. That is the exact corruption in #103.
//   - Nothing is deleted. A table the profile has and the real config does not
//     is left alone; the real side wins only where it has an opinion.
//
// The edit is a structural splice over the raw text, not a parse-and-reserialize:
// blocks are copied verbatim, so comments, key order and formatting survive on
// both sides and an unchanged region stays byte-identical. Go has no
// comment-preserving TOML editor to depend on, and pulling in a plain TOML
// library would mean re-emitting the whole file and losing exactly what the
// issue asks to keep.

// codexEnforcedRootKey is the credential-store setting caam owns outright.
const (
	codexCredentialStoreKey  = "cli_auth_credentials_store"
	codexCredentialStoreLine = `cli_auth_credentials_store = "file"`
)

// codexProfileLocalSections are table paths whose entire subtree belongs to the
// profile. Neither read from the real config nor overwritten in the profile.
var codexProfileLocalSections = [][]string{
	{"hooks", "state"},
	{"projects"},
	{"notice"},
}

// codexContainerTables are tables whose direct children are independent
// entries rather than a single settings block. Their replacement unit is one
// level deeper, so refreshing one MCP server does not delete another that only
// this profile has.
var codexContainerTables = map[string]bool{
	"mcp_servers":     true,
	"model_providers": true,
	"profiles":        true,
}

// SyncCodexConfig refreshes the shared settings of a codex shallow profile's
// config.toml from the user's real ~/.codex/config.toml.
//
// It is a no-op for non-codex profiles and when the real HOME has no
// config.toml. It returns the names of the root keys and table units it
// changed, sorted — names only, never values. Nothing is written when there is
// no change, so a second sync is a genuine no-op.
func (m *Manager) SyncCodexConfig(name string) ([]string, error) {
	home, err := m.HomeFor(name)
	if err != nil {
		return nil, err
	}
	provider, err := m.ResolveProvider(name)
	if err != nil {
		return nil, err
	}
	if NormalizeProvider(provider) != "codex" {
		return nil, nil
	}

	realRaw, err := os.ReadFile(filepath.Join(m.realHome, ".codex", "config.toml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read real .codex/config.toml: %w", err)
	}

	// The profile's config must be a real, private file. A symlink would turn
	// this into a write to the user's real config.
	profilePath := filepath.Join(home, ".codex", "config.toml")
	st, err := os.Lstat(profilePath)
	switch {
	case err == nil && st.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symlink; refusing to refresh a shared file", profilePath)
	case err == nil && !st.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", profilePath)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("stat profile config.toml: %w", err)
	}

	var profileRaw []byte
	if err == nil {
		profileRaw, err = os.ReadFile(profilePath)
		if err != nil {
			return nil, fmt.Errorf("read profile config.toml: %w", err)
		}
	}

	merged, changed, err := mergeCodexConfig(realRaw, profileRaw)
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		return nil, fmt.Errorf("create profile .codex: %w", err)
	}
	if err := writeFileAtomic(profilePath, merged, 0o600); err != nil {
		return nil, fmt.Errorf("write profile config.toml: %w", err)
	}
	return changed, nil
}

// mergeCodexConfig splices the shared parts of realRaw into profileRaw and
// returns the result plus the names of what changed.
func mergeCodexConfig(realRaw, profileRaw []byte) ([]byte, []string, error) {
	real, err := parseTOMLBlocks(realRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("real .codex/config.toml: %w", err)
	}
	profile, err := parseTOMLBlocks(profileRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("profile .codex/config.toml: %w", err)
	}

	changedSet := map[string]bool{}
	mergeCodexRoot(real, profile, changedSet)
	mergeCodexSections(real, profile, changedSet)

	changed := make([]string, 0, len(changedSet))
	for k := range changedSet {
		changed = append(changed, k)
	}
	sort.Strings(changed)

	// Blocks are concatenated, so every piece except the last must end in a
	// newline or two regions would run together — a real config.toml need not
	// end with one. Only missing separators are added, so a file that is
	// already in sync is never rewritten just for this.
	profile.root.text = ensureTrailingNewline(profile.root.text)
	for i := range profile.sections {
		if i < len(profile.sections)-1 {
			profile.sections[i].text = ensureTrailingNewline(profile.sections[i].text)
		}
	}
	if len(profile.sections) > 0 {
		last := len(profile.sections) - 1
		profile.sections[last].text = ensureTrailingNewline(profile.sections[last].text)
	}
	return []byte(profile.render()), changed, nil
}

// mergeCodexRoot refreshes the root (pre-header) keys, real side winning, and
// forces the credential-store setting.
func mergeCodexRoot(real, profile *tomlDoc, changed map[string]bool) {
	profEntries := parseRootEntries(profile.root.text)
	byKey := map[string]int{}
	for i, e := range profEntries {
		if e.key != "" {
			byKey[e.key] = i
		}
	}

	for _, e := range parseRootEntries(real.root.text) {
		if e.key == "" || e.key == codexCredentialStoreKey {
			// caam owns the credential store; the real config's value for it
			// is irrelevant and must never be copied.
			continue
		}
		if idx, ok := byKey[e.key]; ok {
			if !tomlTextEqual(profEntries[idx].text, e.text) {
				// The real entry may be the last line of a file with no
				// trailing newline; entries are concatenated, so an entry in
				// the middle must always end in one.
				profEntries[idx].text = ensureTrailingNewline(e.text)
				changed[e.key] = true
			}
			continue
		}
		profEntries = appendRootEntry(profEntries, rootEntry{key: e.key, text: ensureTrailingNewline(e.text)})
		byKey[e.key] = len(profEntries) - 1
		changed[e.key] = true
	}

	// Enforce file-based credential storage. Everything before the first table
	// header is root scope, so appending here is always a top-level key.
	enforced := codexCredentialStoreLine + "\n"
	if idx, ok := byKey[codexCredentialStoreKey]; ok {
		if !tomlTextEqual(profEntries[idx].text, enforced) {
			profEntries[idx].text = enforced
			changed[codexCredentialStoreKey] = true
		}
	} else {
		profEntries = appendRootEntry(profEntries, rootEntry{key: codexCredentialStoreKey, text: enforced})
		changed[codexCredentialStoreKey] = true
	}

	var b strings.Builder
	for _, e := range profEntries {
		b.WriteString(e.text)
	}
	profile.root.text = b.String()
}

// mergeCodexSections replaces every shared table unit the real config defines,
// as a unit, preserving profile-local subtrees and profile-only units.
func mergeCodexSections(real, profile *tomlDoc, changed map[string]bool) {
	// Group the real config's shared blocks by replacement unit, in the order
	// the units first appear.
	realByUnit := map[string][]tomlBlock{}
	var unitOrder []string
	unitDisplay := map[string]string{}
	for _, b := range real.sections {
		if isCodexProfileLocal(b.path) {
			continue
		}
		unit := codexUnitPath(b.path)
		key := pathKey(unit)
		if _, seen := realByUnit[key]; !seen {
			unitOrder = append(unitOrder, key)
			unitDisplay[key] = strings.Join(unit, ".")
		}
		realByUnit[key] = append(realByUnit[key], b)
	}

	// Which units actually differ.
	profByUnit := map[string][]tomlBlock{}
	for _, b := range profile.sections {
		if isCodexProfileLocal(b.path) {
			continue
		}
		key := pathKey(codexUnitPath(b.path))
		profByUnit[key] = append(profByUnit[key], b)
	}

	targets := map[string]bool{}
	for _, key := range unitOrder {
		if !tomlTextEqual(concatBlocks(profByUnit[key]), concatBlocks(realByUnit[key])) {
			targets[key] = true
			changed[unitDisplay[key]] = true
		}
	}
	if len(targets) == 0 {
		return
	}

	inserted := map[string]bool{}
	var out []tomlBlock
	for _, b := range profile.sections {
		// Profile-local subtrees are kept in place even when they sit inside a
		// unit being replaced (e.g. [hooks.state] under [hooks]).
		if isCodexProfileLocal(b.path) {
			out = append(out, b)
			continue
		}
		key := pathKey(codexUnitPath(b.path))
		if !targets[key] {
			out = append(out, b)
			continue
		}
		if !inserted[key] {
			out = append(out, realByUnit[key]...)
			inserted[key] = true
		}
		// The profile's own blocks for this unit are dropped: replacement is
		// wholesale so no stale key can survive beside its replacement.
	}
	// Units the profile did not have at all go on the end, in the real
	// config's order.
	for _, key := range unitOrder {
		if targets[key] && !inserted[key] {
			out = append(out, realByUnit[key]...)
			inserted[key] = true
		}
	}
	profile.sections = out
}

// isCodexProfileLocal reports whether a table path lies in a profile-local
// subtree.
func isCodexProfileLocal(path []string) bool {
	for _, prefix := range codexProfileLocalSections {
		if len(path) < len(prefix) {
			continue
		}
		match := true
		for i, seg := range prefix {
			if path[i] != seg {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// codexUnitPath returns the path of the block group a table belongs to: one
// entry of a container table (mcp_servers.<name>), or the whole top-level
// table otherwise.
func codexUnitPath(path []string) []string {
	switch {
	case len(path) == 0:
		return nil
	case codexContainerTables[path[0]] && len(path) >= 2:
		return path[:2]
	default:
		return path[:1]
	}
}

func concatBlocks(blocks []tomlBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		b.WriteString(blk.text)
	}
	return b.String()
}

// pathKey renders a table path as a comparison key that cannot be confused by
// a dot inside a quoted segment.
func pathKey(path []string) string { return strings.Join(path, "\x00") }

// tomlTextEqual compares two stretches of TOML for meaningful equality: it
// ignores leading/trailing whitespace and per-line trailing whitespace, so a
// cosmetic difference does not force a rewrite.
func tomlTextEqual(a, b string) bool {
	return normalizeTOMLText(a) == normalizeTOMLText(b)
}

func normalizeTOMLText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// appendRootEntry adds an entry to the root block, first making sure the block
// so far ends in a newline — a config.toml need not, and running two
// assignments together would corrupt both.
func appendRootEntry(entries []rootEntry, e rootEntry) []rootEntry {
	if n := len(entries); n > 0 {
		entries[n-1].text = ensureTrailingNewline(entries[n-1].text)
	}
	return append(entries, e)
}

func ensureTrailingNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
