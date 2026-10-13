package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestClaudeLoginGuidancePointsToNonDisruptiveVaultImport(t *testing.T) {
	called := false
	cmd := &cobra.Command{
		Use:  "login <tool> <profile>",
		Long: "Login help.",
		RunE: func(*cobra.Command, []string) error {
			called = true
			return nil
		},
	}
	configureClaudeLoginVaultGuidance(cmd)

	err := cmd.RunE(cmd, []string{"CLAUDE", "second"})
	if err == nil {
		t.Fatal("Claude login unexpectedly succeeded")
	}
	if called {
		t.Fatal("Claude login fell through to the provider login path")
	}
	msg := err.Error()
	for _, want := range []string{
		"caam exec claude second",
		"/login followed by /exit",
		"caam backup claude <new-vault-name> --from-profile second",
		"share one OAuth grant",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Claude login guidance missing %q: %s", want, msg)
		}
	}
	for _, want := range []string{
		"caam backup claude new-vault-name --from-profile isolated-name",
		"does not activate or refresh",
		"share one OAuth grant",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Fatalf("login help missing %q: %s", want, cmd.Long)
		}
	}
}

func TestClaudeLoginGuidanceLeavesOtherProvidersUntouched(t *testing.T) {
	sentinel := errors.New("provider login result")
	cmd := &cobra.Command{
		Use:  "login <tool> <profile>",
		Long: "Login help.",
		RunE: func(*cobra.Command, []string) error {
			return sentinel
		},
	}
	configureClaudeLoginVaultGuidance(cmd)

	if err := cmd.RunE(cmd, []string{"codex", "work"}); !errors.Is(err, sentinel) {
		t.Fatalf("non-Claude login behavior changed: %v", err)
	}
}

func TestRealClaudeLoginHelpIncludesVaultHandoff(t *testing.T) {
	for _, want := range []string{
		"caam backup claude new-vault-name --from-profile isolated-name",
		"share one OAuth grant",
	} {
		if !strings.Contains(loginCmd.Long, want) {
			t.Fatalf("real login help missing %q", want)
		}
	}
}
