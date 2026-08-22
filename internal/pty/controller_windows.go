//go:build windows

package pty

import (
	"context"
	"os/exec"
	"regexp"
	"time"
)

// windowsController is a stub implementation that returns ErrNotSupported.
// Windows support would require ConPTY (Console Pseudo Terminal) which
// is available in Windows 10 1809+ but requires significant additional
// implementation work.
type windowsController struct{}

// NewController returns ErrNotSupported on Windows.
// To use PTY features on Windows, the system must support ConPTY and
// a proper implementation must be added.
func NewController(cmd *exec.Cmd, opts *Options) (Controller, error) {
	return nil, ErrNotSupported
}

// NewControllerFromArgs returns ErrNotSupported on Windows.
func NewControllerFromArgs(name string, args []string, opts *Options) (Controller, error) {
	return nil, ErrNotSupported
}

// The following methods exist to satisfy the interface but should never
// be called since NewController returns an error.

func (c *windowsController) Start() error {
	return ErrNotSupported
}

func (c *windowsController) InjectCommand(cmd string) error {
	return ErrNotSupported
}

func (c *windowsController) InjectRaw(data []byte) error {
	return ErrNotSupported
}

func (c *windowsController) ReadOutput() (string, error) {
	return "", ErrNotSupported
}

func (c *windowsController) ReadLine(ctx context.Context) (string, error) {
	return "", ErrNotSupported
}

func (c *windowsController) WaitForPattern(ctx context.Context, pattern *regexp.Regexp, timeout time.Duration) (string, error) {
	return "", ErrNotSupported
}

func (c *windowsController) Wait() (int, error) {
	return -1, ErrNotSupported
}

func (c *windowsController) Signal(sig Signal) error {
	return ErrNotSupported
}

func (c *windowsController) Resize(rows, cols uint16) error {
	return ErrNotSupported
}

func (c *windowsController) Close() error {
	return nil
}

func (c *windowsController) Fd() int {
	return -1
}
