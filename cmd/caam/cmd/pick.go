package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var errPickCanceled = errors.New("pick canceled")

var pickCmd = &cobra.Command{
	Use:   "pick [tool]",
	Short: "Pick a profile interactively and activate it",
	Long: `Pick a profile interactively and activate it.

If fzf is installed, caam uses it for a fast fuzzy picker.
Otherwise, caam shows a numbered list and prompts for a selection.

Examples:
  caam pick claude
  caam pick
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPick,
}

func init() {
	rootCmd.AddCommand(pickCmd)
}

func runPick(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load()

	tool := ""
	if len(args) > 0 {
		tool = strings.ToLower(strings.TrimSpace(args[0]))
	}
	if tool == "" {
		if cfg != nil && cfg.DefaultProvider != "" {
			tool = strings.ToLower(strings.TrimSpace(cfg.DefaultProvider))
		}
	}
	if tool == "" {
		inferred, providers, err := inferPickProvider()
		if err != nil {
			return err
		}
		if inferred == "" {
			return fmt.Errorf("tool required (available: %s)", strings.Join(providers, ", "))
		}
		tool = inferred
	}

	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini)", tool)
	}

	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	profiles, err := vault.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	var filtered []string
	for _, p := range profiles {
		if authfile.IsSystemProfile(p) {
			continue
		}
		filtered = append(filtered, p)
	}

	if len(filtered) == 0 {
		return fmt.Errorf("no profiles found for %s; create one with 'caam backup %s <name>'", tool, tool)
	}

	sort.Strings(filtered)

	selection, method, err := pickProfile(cmd, tool, filtered, cfg)
	if err != nil {
		if errors.Is(err, errPickCanceled) {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
		return err
	}

	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("profile selected", "tool", tool, "profile", selection, "method", method)

	activateCmd.SetOut(cmd.OutOrStdout())
	activateCmd.SetErr(cmd.ErrOrStderr())
	return runActivate(activateCmd, []string{tool, selection})
}

func inferPickProvider() (string, []string, error) {
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}

	var available []string
	for provider := range tools {
		profiles, err := vault.List(provider)
		if err != nil {
			continue
		}
		for _, p := range profiles {
			if !authfile.IsSystemProfile(p) {
				available = append(available, provider)
				break
			}
		}
	}

	sort.Strings(available)

	switch len(available) {
	case 0:
		return "", available, fmt.Errorf("no profiles found; run 'caam backup' or 'caam watch' first")
	case 1:
		return available[0], available, nil
	default:
		return "", available, nil
	}
}

func pickProfile(cmd *cobra.Command, tool string, profiles []string, cfg *config.Config) (string, string, error) {
	if hasFzf() && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		selection, err := pickWithFzf(tool, profiles, cfg)
		return selection, "fzf", err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return "", "", fmt.Errorf("no TTY available; use `caam activate %s <profile>`", tool)
	}
	selection, err := pickWithPrompt(cmd, tool, profiles, cfg)
	return selection, "prompt", err
}

func hasFzf() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

func pickWithFzf(tool string, profiles []string, cfg *config.Config) (string, error) {
	lines := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		line := profile
		if cfg != nil {
			aliases := cfg.GetAliases(tool, profile)
			if len(aliases) > 0 {
				line = fmt.Sprintf("%s\t(%s)", profile, strings.Join(aliases, ", "))
			}
		}
		lines = append(lines, line)
	}
	input := strings.Join(lines, "\n")
	cmd := exec.Command("fzf", "--prompt", tool+"> ")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Ctrl+C or Esc in fzf.
			if exitErr.ExitCode() == 130 || exitErr.ExitCode() == 1 {
				return "", errPickCanceled
			}
		}
		return "", fmt.Errorf("fzf failed: %w", err)
	}
	selection := strings.TrimSpace(string(out))
	if selection == "" {
		return "", errPickCanceled
	}
	if fields := strings.SplitN(selection, "\t", 2); len(fields) > 0 {
		selection = strings.TrimSpace(fields[0])
	}
	return selection, nil
}

func pickWithPrompt(cmd *cobra.Command, tool string, profiles []string, cfg *config.Config) (string, error) {
	out := cmd.OutOrStdout()
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintf(out, "Pick %s profile:\n", tool)
	for i, p := range profiles {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, p)
	}
	fmt.Fprint(out, "Select (number or name, blank to cancel): ")

	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errPickCanceled
	}

	if idx, err := strconv.Atoi(line); err == nil {
		if idx < 1 || idx > len(profiles) {
			return "", fmt.Errorf("selection out of range")
		}
		return profiles[idx-1], nil
	}

	name := line
	if cfg != nil {
		if resolved := cfg.ResolveAliasForProvider(tool, name); resolved != "" {
			return resolved, nil
		}
		matches := cfg.FuzzyMatch(tool, name, profiles)
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return "", fmt.Errorf("ambiguous match: %s", strings.Join(matches, ", "))
		}
	}

	for _, p := range profiles {
		if p == name {
			return name, nil
		}
	}

	return "", fmt.Errorf("profile not found: %s; run 'caam ls %s' to see available profiles", name, tool)
}
