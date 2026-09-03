package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// isolatedMarker is exported into the environment by IsolateEnv so that a
// test binary re-executed as a helper process (the GO_WANT_HELPER_PROCESS
// pattern) knows it is already inside an isolated tree and must not replace
// the HOME its parent test prepared for it.
const isolatedMarker = "CAAM_TEST_ISOLATED"

// homeEnv lists every variable through which caam, or a CLI it launches,
// resolves a per-user directory. IsolateEnv points HOME at a throwaway tree
// and unsets the rest so they derive from it again.
var homeEnv = []string{
	"HOME", "USERPROFILE",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	"CAAM_HOME", "CAAM_SHALLOW_HOMES_DIR",
	"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_HOME", "GROK_HOME",
}

// secretEnv lists ambient credentials a test must never pick up from the
// developer's shell.
var secretEnv = []string{
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "XAI_API_KEY",
}

// stubbedBinaries are the agent CLIs and desktop openers that production code
// launches. Tests get no-op stand-ins first on PATH, so a test that reaches a
// login or spawn path can neither start a real CLI (which rewrites its own
// config and begins an OAuth flow) nor open a browser window.
var stubbedBinaries = []string{
	"claude", "codex", "gemini", "agy", "grok", "opencode", "cursor", "npx",
	"open", "xdg-open", "sensible-browser", "x-www-browser",
}

// IsolatedMain is the TestMain body for every package in this module:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.IsolatedMain(m)) }
//
// It runs the package's tests with HOME (and every other per-user directory
// variable) pointed at a throwaway tree, ambient API keys unset, and no-op
// stand-ins for the agent CLIs first on PATH, then returns the exit code.
//
// Tests exercise production code that resolves os.UserHomeDir() and launches
// real CLIs: auth restore, `caam add`, wrap, the daemon, the TUI. On a machine
// with live logins a plain `go test ./...` has replaced the developer's
// ~/.claude.json with "{}" and opened OAuth tabs. The source scan in
// TestNoRealHomeWrites cannot see writers that live in production code, so
// the guard has to be a runtime one, and it has to be in place before any
// test runs. TestEveryTestPackageIsolatesHome fails the suite when a package
// with tests lacks it.
func IsolatedMain(m *testing.M) int {
	if os.Getenv(isolatedMarker) == "1" {
		// A helper process spawned by a test: keep the HOME it was given.
		return m.Run()
	}
	restore, err := IsolateEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil: cannot isolate test environment: %v\n", err)
		return 1
	}
	code := m.Run()
	restore()
	return code
}

// IsolateEnv redirects HOME to a fresh temporary directory, unsets the other
// per-user directory variables (so XDG and tool homes derive from the new
// HOME) and ambient API keys, and prepends a directory of no-op stand-ins for
// the agent CLIs to PATH. It returns a function that restores the previous
// environment and removes the directory. Individual tests still layer their
// own t.Setenv("HOME", ...) on top as before.
func IsolateEnv() (restore func(), err error) {
	dir, err := os.MkdirTemp("", "caam-test-home-")
	if err != nil {
		return nil, err
	}

	saved := map[string]*string{}
	remember := func(key string) {
		if _, done := saved[key]; done {
			return
		}
		if value, ok := os.LookupEnv(key); ok {
			v := value
			saved[key] = &v
		} else {
			saved[key] = nil
		}
	}
	for _, key := range homeEnv {
		remember(key)
		os.Unsetenv(key)
	}
	for _, key := range secretEnv {
		remember(key)
		os.Unsetenv(key)
	}
	remember("PATH")
	remember("BROWSER")
	remember(keychain.EnvDisable)
	remember(keychain.EnvBin)
	remember(isolatedMarker)

	os.Setenv("HOME", dir)
	os.Setenv("USERPROFILE", dir)
	os.Setenv(isolatedMarker, "1")
	// The macOS keychain is machine-global: an isolated HOME does not contain
	// it. Leaving the bridge on would let tests read (and rewrite) the
	// developer's real Claude tokens, so it stays off unless a test opts in by
	// setting CAAM_KEYCHAIN=1 with CAAM_KEYCHAIN_BIN pointed at a stub.
	os.Setenv(keychain.EnvDisable, "0")
	os.Unsetenv(keychain.EnvBin)

	if runtime.GOOS != "windows" {
		stubDir := filepath.Join(dir, "stub-bin")
		if err := writeStubBinaries(stubDir); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		os.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		// xdg-open and friends fall back to $BROWSER; point it at a stub too.
		os.Setenv("BROWSER", filepath.Join(stubDir, "open"))
	}

	restore = func() {
		for key, value := range saved {
			if value == nil {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, *value)
			}
		}
		os.RemoveAll(dir)
	}
	return restore, nil
}

// writeStubBinaries fills dir with executables that accept any arguments,
// print nothing, and exit 0.
func writeStubBinaries(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range stubbedBinaries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			return fmt.Errorf("write stub %s: %w", name, err)
		}
	}
	return nil
}
