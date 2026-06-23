//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package codexd

import (
	"os/exec"
	"strconv"
	"strings"
)

func init() {
	scanProcesses = scanProcPS
}

// scanProcPS enumerates processes via `ps`, which is available on macOS/BSD and
// is the same approach the rest of caam uses for platform-specific shelling.
// Output format: one process per line, "<pid> <full command...>".
func scanProcPS() ([]rawProc, bool) {
	// -ax: all processes incl. those without a controlling terminal (a daemon
	//      has none). -ww: do not truncate the command line. -o pid=,command=:
	//      headerless pid + full command.
	cmd := exec.Command("ps", "-axww", "-o", "pid=,command=")
	out, err := cmd.Output()
	if err != nil {
		// ps unavailable / failed -> unsupported, so we don't falsely report
		// "no daemon".
		return nil, false
	}

	lines := strings.Split(string(out), "\n")
	procs := make([]rawProc, 0, 8)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split off the leading PID; the remainder is the command line.
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil || pid <= 0 {
			continue
		}
		procs = append(procs, rawProc{pid: pid, cmdline: strings.TrimSpace(fields[1])})
	}
	return procs, true
}
