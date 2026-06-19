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

// Capability identifiers advertised by this build. Tooling (e.g. ntm global
// rotation) can gate behavior on the presence of a capability rather than on a
// version-number comparison, which is more robust across forks/builds.
const (
	// CapSafeRestore indicates this build implements the Codex/ChatGPT
	// refresh-token-rotation safe-restore guards: the restore path will not
	// clobber a live auth file that holds the same identity with strictly
	// newer tokens, and switches re-snapshot the outgoing profile's rotated
	// tokens first. See issue #19 and ntm#194.
	CapSafeRestore = "safe-restore"
)

// Capabilities returns the stable capability identifiers advertised by this
// build, in a deterministic order. Surfaced in `caam robot status` JSON so
// orchestrators can detect feature support programmatically.
func Capabilities() []string {
	return []string{CapSafeRestore}
}

// HasCapability reports whether this build advertises the named capability.
func HasCapability(name string) bool {
	for _, c := range Capabilities() {
		if c == name {
			return true
		}
	}
	return false
}
