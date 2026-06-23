//go:build linux

package codexd

import (
	"os"
	"path/filepath"
	"strconv"
)

func init() {
	scanProcesses = scanProcLinux
}

// scanProcLinux enumerates processes by reading /proc/<pid>/cmdline. This is
// allocation-light and needs no external binary. We only read the cmdline
// (NUL-separated argv) which is exactly what classifyCodexDaemon wants.
func scanProcLinux() ([]rawProc, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// No /proc (unusual on Linux) -> report unsupported rather than
		// claiming "no daemon running".
		return nil, false
	}

	procs := make([]rawProc, 0, 8)
	for _, e := range entries {
		name := e.Name()
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil || len(data) == 0 {
			// Process may have exited, or we lack permission; skip it.
			continue
		}
		// /proc/<pid>/cmdline is NUL-separated argv; splitCmdline handles the
		// NULs. Trim a trailing NUL to avoid an empty final field.
		procs = append(procs, rawProc{pid: pid, cmdline: string(data)})
	}
	return procs, true
}
