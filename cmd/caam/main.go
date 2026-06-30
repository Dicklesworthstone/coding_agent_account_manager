// Package main is the entry point for caam - Coding Agent Account Manager.
package main

import (
	"os"

	"github.com/Dicklesworthstone/coding_agent_account_manager/cmd/caam/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}
