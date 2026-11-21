//go:build !unix

package cmd

import (
	"context"
	"os"
	"os/signal"
)

// defaultSpawnExec runs the target command as a child process on platforms
// without syscall.Exec (notably native Windows, where syscall.Exec returns
// EWINDOWS). Interactive streams, environment selection, cancellation, and
// exact exit status are preserved.
func defaultSpawnExec(binPath string, args []string, env []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runShallowChild(ctx, binPath, args, env, os.Stdin, os.Stdout, os.Stderr)
}
