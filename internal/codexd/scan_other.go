//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package codexd

// On platforms where we have no reliable, dependency-free process scanner
// (notably Windows), leave scanProcesses nil. Detect() then returns
// supported=false so callers surface a soft "could not check" rather than a
// false "no daemon running".
