package cmd

import (
	"context"
	"errors"
	"io"
	"os/exec"

	caamexec "github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
)

// runShallowChild executes binPath as a child process with the selected
// environment, attached streams, context cancellation, and exact exit-code
// propagation. args follows the syscall.Exec convention where args[0] is the
// command name and args[1:] are the arguments passed to the child.
func runShallowChild(ctx context.Context, binPath string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var childArgs []string
	if len(args) > 1 {
		childArgs = args[1:]
	}
	child := exec.CommandContext(ctx, binPath, childArgs...)
	child.Env = env
	child.Stdin = stdin
	child.Stdout = stdout
	child.Stderr = stderr

	err := child.Run()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code == 0 {
			code = 1
		}
		return &caamexec.ExitCodeError{Code: code}
	}
	return err
}
