//go:build !windows

package signals

import "syscall"

func SendHUP(pid int) error {
	return syscall.Kill(pid, syscall.SIGHUP)
}

// SendTerm sends SIGTERM for a graceful shutdown request. Used to reload a
// Codex daemon (app-server) so it respawns with freshly-swapped auth.
func SendTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
