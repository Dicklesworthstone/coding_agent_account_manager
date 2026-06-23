package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/codexd"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
)

// codexDaemonWarning is the structured result of a post-switch daemon check,
// surfaced both to humans (stderr) and to JSON consumers (activate/next output).
type codexDaemonWarning struct {
	// Detected is true when at least one running Codex daemon was found.
	Detected bool `json:"detected"`
	// PIDs lists the process IDs of the detected daemon(s).
	PIDs []int `json:"pids,omitempty"`
	// Subcommands lists the daemon kinds found (e.g. "app-server").
	Subcommands []string `json:"subcommands,omitempty"`
	// Message is the human-readable warning (empty when nothing was detected).
	Message string `json:"message,omitempty"`
	// Reloaded is true when the daemon was restarted on the user's behalf
	// (only when --reload-daemon was passed).
	Reloaded bool `json:"reloaded,omitempty"`
}

// checkCodexDaemon runs ONLY for the codex tool. It detects whether a
// persistent Codex daemon (app-server / mcp-server) is running after an
// on-disk auth swap and, if so, warns the user that the swap will not take
// effect for daemon-backed sessions until the daemon is reloaded.
//
// tool: the provider being switched ("codex", "claude", ...). Non-codex tools
//
//	return a zero warning and do nothing.
//
// reload: when true, caam will send SIGTERM to the detected daemon(s) so the
//
//	next session respawns one with the new auth. This is opt-in because
//	killing a daemon can disrupt an in-flight session.
func checkCodexDaemon(tool string, reload bool) codexDaemonWarning {
	if strings.ToLower(strings.TrimSpace(tool)) != "codex" {
		return codexDaemonWarning{}
	}

	procs, supported := codexd.Detect()
	if !supported || len(procs) == 0 {
		// Either we cannot scan (e.g. Windows) or no daemon is running. In both
		// cases there is nothing actionable to warn about; a switch with no
		// daemon present is exactly the common, correct case.
		return codexDaemonWarning{}
	}

	w := codexDaemonWarning{Detected: true}
	kinds := map[string]struct{}{}
	for _, p := range procs {
		w.PIDs = append(w.PIDs, p.PID)
		kind := p.Subcommand
		if kind == "" {
			kind = "daemon"
		}
		if _, ok := kinds[kind]; !ok {
			kinds[kind] = struct{}{}
			w.Subcommands = append(w.Subcommands, kind)
		}
	}

	pidStrs := make([]string, len(w.PIDs))
	for i, pid := range w.PIDs {
		pidStrs[i] = fmt.Sprintf("%d", pid)
	}
	w.Message = fmt.Sprintf(
		"a Codex daemon (%s, pid %s) is running and has the previous account's auth cached in memory; "+
			"the account switch will NOT take effect for sessions served by that daemon until it is reloaded. "+
			"Restart it (e.g. quit and relaunch your editor/`codex app-server`) or re-run with --reload-daemon.",
		strings.Join(w.Subcommands, ", "), strings.Join(pidStrs, ", "),
	)

	if reload {
		var reloadedAny bool
		var failures []string
		for _, p := range procs {
			// SIGTERM (graceful) rather than SIGKILL: let the daemon shut down
			// cleanly; whatever supervises it (editor, wrapper) will respawn it
			// with the freshly-swapped auth on next use.
			if err := signals.SendTerm(p.PID); err != nil {
				failures = append(failures, fmt.Sprintf("pid %d: %v", p.PID, err))
				continue
			}
			reloadedAny = true
		}
		w.Reloaded = reloadedAny
		if len(failures) > 0 {
			w.Message = fmt.Sprintf(
				"detected Codex daemon (%s); failed to reload some processes: %s. "+
					"Restart the daemon manually for the switch to take effect.",
				strings.Join(w.Subcommands, ", "), strings.Join(failures, "; "),
			)
		} else if reloadedAny {
			w.Message = fmt.Sprintf(
				"reloaded Codex daemon (%s, pid %s) via SIGTERM so the new account takes effect; "+
					"it will respawn on next use with the switched auth.",
				strings.Join(w.Subcommands, ", "), strings.Join(pidStrs, ", "),
			)
		}
	}

	return w
}

// printCodexDaemonWarning writes the human-readable warning to stderr. It is a
// no-op when nothing was detected. Use stderr so JSON-on-stdout consumers are
// unaffected (callers in JSON mode embed the structured warning instead).
func printCodexDaemonWarning(out io.Writer, w codexDaemonWarning) {
	if !w.Detected || w.Message == "" {
		return
	}
	if out == nil {
		out = os.Stderr
	}
	if w.Reloaded {
		fmt.Fprintf(out, "Codex daemon: %s\n", w.Message)
		return
	}
	fmt.Fprintf(out, "Warning: %s\n", w.Message)
}
