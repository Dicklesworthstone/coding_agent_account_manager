// Package main is the entry point for caam - Coding Agent Account Manager.
package main

import (
	"os"

	"github.com/user/caam/cmd/caam/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
