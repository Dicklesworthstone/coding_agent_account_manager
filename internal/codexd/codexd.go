// Package codexd detects whether a persistent Codex daemon process (such as
// `codex app-server` or `codex mcp-server`) is currently running.
//
// Why this exists (issue #21):
//
// caam switches the active Codex account by swapping the on-disk auth file
// ($CODEX_HOME/auth.json). New `codex` invocations read that file at startup,
// so the swap takes effect immediately for fresh sessions. However, the Codex
// CLI can also run as a long-lived daemon -- most notably `codex app-server`
// (the persistent server that editors / `cod`-style wrappers talk to), and the
// MCP server mode (`codex mcp-server` / `codex mcp serve`). Such a daemon reads
// auth.json once at startup and caches the credentials in-process. When that
// daemon is already running, swapping auth.json on disk does NOT change the
// account the daemon uses: any session served by the daemon keeps using the
// OLD account until the daemon is reloaded/restarted.
//
// Previously caam gave no indication of this, so `caam switch/next/activate
// codex` appeared to silently no-op for daemon-backed sessions. This package
// lets the switch commands detect a running daemon and warn the user clearly
// (and, opt-in, offer to restart it).
package codexd

import (
	"path/filepath"
	"sort"
	"strings"
)

// Process describes a detected Codex daemon process.
type Process struct {
	PID int
	// Cmdline is the (best-effort) full command line of the process, used only
	// for display so the user can identify which daemon caam found.
	Cmdline string
	// Subcommand is the recognized daemon mode, e.g. "app-server" or
	// "mcp-server". May be empty if we matched a generic persistent codex
	// process we could not classify precisely.
	Subcommand string

	// --- environment attribution (issue #47) ---
	//
	// These are populated by reading the process environment AFTER the process
	// has been classified as a Codex daemon, so we never inspect the environ of
	// unrelated processes. They let a scoped reload (shallow-spawn) target only
	// the daemon serving a specific CODEX_HOME instead of every Codex daemon on
	// the host.

	// EnvKnown is true when the process environment was successfully read. It is
	// false on platforms/paths without an environment-inspection mechanism
	// (macOS/BSD `ps` scanning, or when /proc/<pid>/environ is unreadable). A
	// scoped reload treats an unknown environment as "cannot attribute" and
	// leaves such a daemon alone rather than risk disrupting another profile.
	EnvKnown bool
	// CodexHome is the daemon's CODEX_HOME (may be empty even when EnvKnown).
	CodexHome string
	// Home is the daemon's HOME (used for the HOME/.codex fallback).
	Home string
	// ShallowProfile is the daemon's SHALLOW_PROFILE marker, if it was spawned
	// under a caam shallow profile.
	ShallowProfile string
}

// EffectiveCodexHome returns the CODEX_HOME the daemon is effectively using,
// applying the HOME/.codex fallback when CODEX_HOME is unset. The second return
// value is false when the environment is unknown or gives no basis to attribute
// the daemon to a codex home (so a scoped reload will skip it, fail-safe).
func (p Process) EffectiveCodexHome() (string, bool) {
	if !p.EnvKnown {
		return "", false
	}
	if h := strings.TrimSpace(p.CodexHome); h != "" {
		return filepath.Clean(h), true
	}
	if home := strings.TrimSpace(p.Home); home != "" {
		return filepath.Clean(filepath.Join(home, ".codex")), true
	}
	return "", false
}

// FilterByCodexHome returns only the daemons whose effective CODEX_HOME matches
// the target (issue #47). It is fail-safe: a daemon whose environment cannot be
// inspected (EnvKnown == false) is EXCLUDED, so a scoped reload never SIGTERMs a
// process it could not positively attribute to the target profile. An empty
// target returns the input unchanged (host-wide behavior for activate/next).
func FilterByCodexHome(procs []Process, codexHome string) []Process {
	target := strings.TrimSpace(codexHome)
	if target == "" {
		return procs
	}
	target = filepath.Clean(target)
	out := make([]Process, 0, len(procs))
	for _, p := range procs {
		eff, ok := p.EffectiveCodexHome()
		if !ok {
			continue
		}
		if eff == target {
			out = append(out, p)
		}
	}
	return out
}

// rawProc is the platform-agnostic representation a scanner yields.
type rawProc struct {
	pid     int
	cmdline string
}

// scanProcesses is set by a per-platform file (process_unix.go /
// process_darwin.go / process_other.go). It returns the candidate processes to
// inspect, or (nil, false) if process scanning is unavailable on this platform.
var scanProcesses func() ([]rawProc, bool)

// readProcEnviron is set by a per-platform file. Given a PID it returns that
// process's environment as a KEY->VALUE map and whether it could be read. It is
// nil on platforms with no dependency-free environment-inspection mechanism
// (macOS/BSD, Windows); Detect() then leaves Process.EnvKnown false and scoped
// reloads fall back to leaving unattributable daemons alone. It is ONLY called
// for a process already classified as a Codex daemon (issue #47).
var readProcEnviron func(pid int) (map[string]string, bool)

// Detect scans running processes and returns any that look like a persistent
// Codex daemon (app-server / mcp-server). The second return value is false when
// process scanning is not supported on the current platform, so callers can
// distinguish "scanned, found nothing" from "could not scan" and avoid making
// false promises.
func Detect() (procs []Process, supported bool) {
	if scanProcesses == nil {
		return nil, false
	}
	raw, ok := scanProcesses()
	if !ok {
		return nil, false
	}

	seen := make(map[int]struct{}, len(raw))
	for _, p := range raw {
		if _, dup := seen[p.pid]; dup {
			continue
		}
		sub, match := classifyCodexDaemon(p.cmdline)
		if !match {
			continue
		}
		seen[p.pid] = struct{}{}
		proc := Process{
			PID:        p.pid,
			Cmdline:    strings.TrimSpace(p.cmdline),
			Subcommand: sub,
		}
		// Inspect the environment ONLY now that the process is a confirmed Codex
		// daemon, so a scoped reload can attribute it to a specific CODEX_HOME
		// (issue #47). Skipped when no inspection mechanism exists on this
		// platform (readProcEnviron == nil) or the environ is unreadable.
		if readProcEnviron != nil {
			if env, ok := readProcEnviron(p.pid); ok {
				proc.EnvKnown = true
				proc.CodexHome = env["CODEX_HOME"]
				proc.Home = env["HOME"]
				proc.ShallowProfile = env["SHALLOW_PROFILE"]
			}
		}
		procs = append(procs, proc)
	}

	sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	return procs, true
}

// daemonSubcommands maps a recognized first token of a codex daemon subcommand
// to a friendly label. Order matters only for display; matching is exact on the
// token. `app-server` is the primary culprit behind issue #21.
var daemonSubcommands = map[string]string{
	"app-server": "app-server",
	"app_server": "app-server",
	"mcp-server": "mcp-server",
	"mcp_server": "mcp-server",
	"proto":      "proto", // `codex proto` is the older persistent protocol stream
}

// classifyCodexDaemon decides whether a command line belongs to a persistent
// Codex daemon and, if so, which kind. It deliberately requires BOTH that the
// executable is codex AND that a known daemon subcommand is present, so it does
// not flag ordinary interactive `codex` TUI sessions (which re-read auth.json
// on every launch and are therefore unaffected by an on-disk swap).
func classifyCodexDaemon(cmdline string) (subcommand string, match bool) {
	fields := splitCmdline(cmdline)
	if len(fields) == 0 {
		return "", false
	}
	if !execIsCodex(fields[0]) {
		return "", false
	}

	// Walk the remaining tokens looking for a known daemon subcommand. We scan
	// all tokens (not just fields[1]) so we tolerate global flags appearing
	// before the subcommand, e.g. `codex --cd /x app-server`, and the
	// `codex mcp serve` two-word form.
	for i := 1; i < len(fields); i++ {
		tok := strings.ToLower(strings.TrimSpace(fields[i]))
		if tok == "" || strings.HasPrefix(tok, "-") {
			continue
		}
		if label, ok := daemonSubcommands[tok]; ok {
			return label, true
		}
		// Two-word form: `codex mcp serve` / `codex mcp-server`.
		if tok == "mcp" {
			for j := i + 1; j < len(fields); j++ {
				next := strings.ToLower(strings.TrimSpace(fields[j]))
				if next == "" || strings.HasPrefix(next, "-") {
					continue
				}
				if next == "serve" || next == "server" {
					return "mcp-server", true
				}
				break
			}
		}
	}
	return "", false
}

// execIsCodex reports whether the argv[0] of a process is the codex binary.
// It tolerates absolute paths (/usr/local/bin/codex), version-manager shims,
// and a `.exe` suffix on Windows.
func execIsCodex(arg0 string) bool {
	a := strings.TrimSpace(arg0)
	if a == "" {
		return false
	}
	// Take the basename.
	if idx := strings.LastIndexAny(a, "/\\"); idx >= 0 {
		a = a[idx+1:]
	}
	a = strings.ToLower(a)
	a = strings.TrimSuffix(a, ".exe")
	return a == "codex"
}

// splitCmdline splits a command line into fields. Process scanners on
// /proc/<pid>/cmdline yield NUL-separated argv which the unix scanner converts
// to spaces; the ps-based scanner yields a space-joined command. Either way a
// simple whitespace split is sufficient for our matching purposes (we only look
// at the executable basename and bare subcommand tokens).
func splitCmdline(cmdline string) []string {
	cmdline = strings.ReplaceAll(cmdline, "\x00", " ")
	return strings.Fields(cmdline)
}
