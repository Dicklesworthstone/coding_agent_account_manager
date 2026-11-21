//go:build unix

package cmd

import "syscall"

// defaultSpawnExec replaces the current process image on Unix so signals and
// exit status propagate natively without leaving an extra caam process.
func defaultSpawnExec(binPath string, args []string, env []string) error {
	return syscall.Exec(binPath, args, env)
}
