// Package version provides version information for the application.
package version

import (
	"fmt"
	"runtime"
)

// These variables are set at build time via ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info returns a formatted version string.
func Info() string {
	return fmt.Sprintf("caam %s (%s) built on %s with %s",
		Version, Commit, Date, runtime.Version())
}

// Short returns just the version number.
func Short() string {
	return Version
}
