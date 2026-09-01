package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ambientEnv lists every variable through which caam, or a CLI it drives,
// resolves a per-user directory, reaches the desktop, or picks up a credential
// from the shell. IsolateHome points the directory variables at a fresh
// temporary tree and unsets the rest, so a test binary cannot reach the
// developer's real files, real keys, or real display.
var ambientEnv = []string{
	"HOME", "USERPROFILE",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	"CAAM_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_HOME",
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "XAI_API_KEY",
	"BROWSER", "DISPLAY", "WAYLAND_DISPLAY", "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE",
}

// isolatedMarker is set in the environment by IsolateHome so a process
// re-executed by a test inherits the fact that it is already isolated.
const isolatedMarker = "CAAM_TEST_ISOLATED"

// stubbedBinaries are the agent CLIs and browser openers that production code
// launches. Tests get no-op stand-ins for them first on PATH, so a test that
// reaches a login flow can neither start a real CLI (which rewrites its own
// config and starts OAuth) nor open a window on the developer's desktop.
var stubbedBinaries = []string{
	"claude", "codex", "gemini", "agy", "grok", "opencode", "cursor",
	"xdg-open", "open", "sensible-browser", "x-www-browser", "gio", "kde-open",
	"npx",
}

// RunIsolated runs a package's tests with HOME, and every other variable in
// ambientEnv, pointed away from the developer's account, and returns the exit
// code for os.Exit.
//
// Tests exercise backup, restore, clear, add, and rotation paths that resolve
// os.UserHomeDir() inside production code and launch the real agent CLIs. On
// a machine with live logins that makes a plain `go test ./...` overwrite
// ~/.claude.json, ~/.claude/.credentials.json, or ~/.codex/auth.json and open
// OAuth pages in the browser. It has happened: a test run replaced a
// developer's ~/.claude.json with "{}", logged every running Claude Code
// session out, and kept opening login tabs. The regex scan in
// TestNoRealHomeWrites cannot see writers that live in production code, so the
// guard has to be a runtime one.
//
// Every package with tests installs this through TestMain;
// TestEveryTestPackageIsolatesHome fails the suite when one does not.
func RunIsolated(m *testing.M) int {
	// Several e2e tests re-execute the test binary as the CLI under test and
	// hand it a HOME they built. That child is already inside an isolated
	// environment; isolating again would replace the paths its parent set.
	if os.Getenv(isolatedMarker) == "1" {
		return m.Run()
	}
	cleanup, err := IsolateHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: cannot isolate HOME for tests: %v\n", err)
		return 1
	}
	code := m.Run()
	cleanup()
	return code
}

// IsolateHome redirects HOME to a fresh temporary tree, clears the XDG base
// directories so they derive from it, unsets the tool-specific home, API-key, and display
// variables, puts no-op stand-ins for the agent CLIs and browser openers first
// on PATH, and returns a function that restores the previous environment and
// removes the tree. Individual tests that need a different HOME or PATH still
// call t.Setenv, which layers on top of this.
func IsolateHome() (func(), error) {
	dir, err := os.MkdirTemp("", "caam-testhome-")
	if err != nil {
		return nil, err
	}

	oldHome, _ := os.UserHomeDir()
	oldCache := os.Getenv("XDG_CACHE_HOME")
	if oldCache == "" && oldHome != "" {
		oldCache = filepath.Join(oldHome, ".cache")
	}

	saved := make(map[string]*string, len(ambientEnv)+3)
	remember := func(key string) {
		if value, ok := os.LookupEnv(key); ok {
			v := value
			saved[key] = &v
		} else {
			saved[key] = nil
		}
	}
	for _, key := range ambientEnv {
		remember(key)
		os.Unsetenv(key)
	}
	remember("PATH")

	// Keep the Go toolchain's caches where they were: tests that build or run
	// the caam binary must not re-download modules into the throwaway HOME.
	for key, value := range map[string]string{
		"GOPATH":  filepath.Join(oldHome, "go"),
		"GOCACHE": filepath.Join(oldCache, "go-build"),
	} {
		if _, ok := os.LookupEnv(key); !ok && oldHome != "" {
			remember(key)
			os.Setenv(key, value)
		}
	}

	// The XDG variables stay unset so every base directory derives from the
	// new HOME, for this process and for any caam binary a test spawns with a
	// HOME of its own.
	remember(isolatedMarker)
	os.Setenv(isolatedMarker, "1")
	os.Setenv("HOME", dir)
	os.Setenv("USERPROFILE", dir)

	if runtime.GOOS != "windows" {
		stubDir := filepath.Join(dir, "stub-bin")
		if err := writeStubs(stubDir); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		os.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		// xdg-open falls back to $BROWSER when no desktop is detected.
		os.Setenv("BROWSER", filepath.Join(stubDir, "xdg-open"))
	}

	cleanup := func() {
		for key, value := range saved {
			if value == nil {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, *value)
			}
		}
		os.RemoveAll(dir)
	}
	return cleanup, nil
}

// writeStubs fills dir with executables that accept any arguments, print
// nothing, and exit 0.
func writeStubs(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range stubbedBinaries {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			return fmt.Errorf("write stub %s: %w", name, err)
		}
	}
	return nil
}
