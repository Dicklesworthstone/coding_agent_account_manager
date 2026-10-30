//go:build !unix

package cmd

import (
	"context"
	"errors"
)

const shallowTokenFDSupported = false

// Inherited Unix descriptor numbers are not portable Windows handles. Refuse
// the option instead of guessing a handle or consuming interactive stdin.
func readShallowClaudeOAuthToken(_ context.Context, _ int) (string, error) {
	return "", errors.New("--claude-oauth-token-fd is supported only on Unix systems (including Linux, macOS, and WSL)")
}
