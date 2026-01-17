package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

func TestPickProfileNonTTY(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stdout.Fd())) {
		t.Skip("requires non-tty stdin/stdout")
	}

	cmd := &cobra.Command{}
	_, _, err := pickProfile(cmd, "claude", []string{"work"}, &config.Config{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no TTY")
}

func TestPickWithPromptAlias(t *testing.T) {
	cfg := &config.Config{}
	cfg.AddAlias("claude", "work-account", "work")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	withStdin(t, "work\n", func() {
		selection, err := pickWithPrompt(cmd, "claude", []string{"work-account", "personal"}, cfg)
		require.NoError(t, err)
		require.Equal(t, "work-account", selection)
	})
}

func TestPickWithPromptNumber(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	withStdin(t, "2\n", func() {
		selection, err := pickWithPrompt(cmd, "claude", []string{"work", "personal"}, nil)
		require.NoError(t, err)
		require.Equal(t, "personal", selection)
	})
}

func TestPickWithFzfParsesAliasLine(t *testing.T) {
	tmpDir := t.TempDir()
	fzfPath := filepath.Join(tmpDir, "fzf")
	script := "#!/bin/sh\ncat | head -n 1\n"
	require.NoError(t, os.WriteFile(fzfPath, []byte(script), 0755))

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{}
	cfg.AddAlias("claude", "beta", "b")

	selection, err := pickWithFzf("claude", []string{"beta"}, cfg)
	require.NoError(t, err)
	require.Equal(t, "beta", selection)
}

func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	_, err = writer.Write([]byte(input))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	orig := os.Stdin
	os.Stdin = reader
	defer func() {
		os.Stdin = orig
		_ = reader.Close()
	}()

	fn()
}
