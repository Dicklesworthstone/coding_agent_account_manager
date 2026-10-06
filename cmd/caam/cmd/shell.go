package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// shellCmd is the parent command for shell integration.
var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Shell integration commands",
	Long: `Commands for integrating caam with your shell.

Use 'caam shell-init' to set up automatic profile management.`,
}

// shellInitCmd outputs shell initialization code.
var shellInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Output shell initialization code",
	Long: `Outputs shell initialization code that wraps AI CLI commands.

Add this to your shell's rc file:

  # For bash (~/.bashrc):
  eval "$(caam shell init)"

  # For zsh (~/.zshrc):
  eval "$(caam shell init)"

  # For fish (~/.config/fish/config.fish):
  caam shell init --fish | source

This creates wrapper functions for claude, codex, and gemini that:
- Automatically use the best available profile
- Handle rate limits transparently
- Record usage for analytics

After setup, just use the tools normally:
  claude "explain this code"
  codex "write tests"
  gemini "summarize this file"

Rate limits are handled automatically!`,
	RunE: runShellInit,
}

func init() {
	shellInitCmd.Flags().Bool("fish", false, "output fish shell syntax")
	shellInitCmd.Flags().Bool("bash", false, "output bash syntax (default)")
	shellInitCmd.Flags().Bool("zsh", false, "output zsh syntax")
	shellInitCmd.Flags().Bool("posix", false, "output POSIX shell syntax")
	shellInitCmd.Flags().Bool("no-wrap", false, "disable tool wrapping (only completions)")
	shellInitCmd.Flags().String("tools", "claude,codex,gemini", "comma-separated list of tools to wrap")

	shellCmd.AddCommand(shellInitCmd)
	rootCmd.AddCommand(shellCmd)
}

func runShellInit(cmd *cobra.Command, args []string) error {
	fish, _ := cmd.Flags().GetBool("fish")
	posix, _ := cmd.Flags().GetBool("posix")
	noWrap, _ := cmd.Flags().GetBool("no-wrap")
	toolsStr, _ := cmd.Flags().GetString("tools")

	// Parse tools list
	var tools []string
	for _, t := range strings.Split(toolsStr, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tools = append(tools, t)
		}
	}

	// Detect shell if not specified
	shell := detectShell()
	if fish {
		shell = "fish"
	} else if posix {
		shell = "sh"
	}

	// Find caam binary path
	caamPath, err := findCaamPath()
	if err != nil {
		caamPath = "caam" // Fallback to PATH lookup
	}

	// Generate output
	var output string
	switch shell {
	case "fish":
		output = generateFishInit(caamPath, tools, noWrap)
	default:
		output = generateBashInit(caamPath, tools, noWrap)
	}

	fmt.Print(output)
	return nil
}

func detectShell() string {
	// Check SHELL environment variable
	shell := os.Getenv("SHELL")
	if shell != "" {
		base := filepath.Base(shell)
		switch base {
		case "fish":
			return "fish"
		case "zsh":
			return "zsh"
		case "bash":
			return "bash"
		}
	}

	// Check parent process on Unix
	if runtime.GOOS != "windows" {
		ppid := os.Getppid()
		procPath := fmt.Sprintf("/proc/%d/comm", ppid)
		if data, err := os.ReadFile(procPath); err == nil {
			name := strings.TrimSpace(string(data))
			switch name {
			case "fish":
				return "fish"
			case "zsh":
				return "zsh"
			case "bash":
				return "bash"
			}
		}
	}

	return "bash" // Default
}

func findCaamPath() (string, error) {
	// Try to find the current executable
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return exe, nil
}

// shellQuote returns a properly shell-quoted string for bash/zsh.
// This prevents command injection when paths contain spaces or special characters.
func shellQuote(s string) string {
	// If the string contains no special characters, return as-is
	needsQuote := false
	for _, c := range s {
		if c == ' ' || c == '\'' || c == '"' || c == '\\' || c == '$' ||
			c == '`' || c == '!' || c == '*' || c == '?' || c == '[' ||
			c == ']' || c == '(' || c == ')' || c == '{' || c == '}' ||
			c == '|' || c == '&' || c == ';' || c == '<' || c == '>' ||
			c == '\n' || c == '\t' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return s
	}
	// Use single quotes and escape any embedded single quotes
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func generateBashInit(caamPath string, tools []string, noWrap bool) string {
	var sb strings.Builder

	sb.WriteString("# caam shell integration\n")
	sb.WriteString("# Add to your ~/.bashrc or ~/.zshrc:\n")
	sb.WriteString("#   eval \"$(caam shell init)\"\n\n")

	// Quote the caam path for safe shell interpolation
	quotedPath := shellQuote(caamPath)

	// Tool wrapper functions
	if !noWrap {
		for _, tool := range tools {
			sb.WriteString(fmt.Sprintf(`# %s wrapper with automatic rate limit handling
%s() {
  %s run %s -- "$@"
}

`, tool, tool, quotedPath, tool))
		}
	}

	// Completion for caam command
	sb.WriteString(`# caam command completion
_caam_completions() {
  local cur prev words cword
  _init_completion || return

  case "$prev" in
    caam)
      COMPREPLY=($(compgen -W "activate backup clear cooldown delete exec export import init login ls paths profile project refresh reload resume run shell status sync tui uninstall usage" -- "$cur"))
      return
      ;;
    activate|backup|clear|delete|ls|paths|status)
      COMPREPLY=($(compgen -W "claude codex gemini" -- "$cur"))
      return
      ;;
  esac

  # Profile completion for activate
  if [[ "${words[1]}" == "activate" && ${#words[@]} -ge 3 ]]; then
    local tool="${words[2]}"
    local profiles
    profiles=$(caam ls "$tool" 2>/dev/null | tr '\n' ' ')
    COMPREPLY=($(compgen -W "$profiles" -- "$cur"))
    return
  fi
}

complete -F _caam_completions caam
`)

	return sb.String()
}

// fishQuote returns a properly quoted string for fish shell.
func fishQuote(s string) string {
	if s == "" {
		return "''"
	}
	// If the string contains no special characters, return as-is
	needsQuote := false
	for _, c := range s {
		if c == ' ' || c == '\'' || c == '"' || c == '\\' || c == '$' ||
			c == '(' || c == ')' || c == '{' || c == '}' || c == '[' ||
			c == ']' || c == '*' || c == '?' || c == '~' || c == '#' ||
			c == '|' || c == '&' || c == ';' || c == '<' || c == '>' ||
			c == '\n' || c == '\t' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return s
	}
	// Fish interprets both \\ and \' inside single quotes, so preserve
	// existing backslashes before escaping embedded quotes.
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'"
}

func generateFishInit(caamPath string, tools []string, noWrap bool) string {
	var sb strings.Builder

	sb.WriteString("# caam shell integration for fish\n")
	sb.WriteString("# Add to your ~/.config/fish/config.fish:\n")
	sb.WriteString("#   caam shell init --fish | source\n\n")

	// Quote the caam path for safe shell interpolation
	quotedPath := fishQuote(caamPath)

	// Tool wrapper functions
	if !noWrap {
		for _, tool := range tools {
			sb.WriteString(fmt.Sprintf(`# %s wrapper with automatic rate limit handling
function %s
  %s run %s -- $argv
end

`, tool, tool, quotedPath, tool))
		}
	}

	// Completion for caam command
	sb.WriteString(`# caam command completion
complete -c caam -f
complete -c caam -n "__fish_use_subcommand" -a "activate" -d "Activate a profile"
complete -c caam -n "__fish_use_subcommand" -a "backup" -d "Backup current auth"
complete -c caam -n "__fish_use_subcommand" -a "clear" -d "Clear auth files"
complete -c caam -n "__fish_use_subcommand" -a "cooldown" -d "Manage cooldowns"
complete -c caam -n "__fish_use_subcommand" -a "delete" -d "Delete a profile"
complete -c caam -n "__fish_use_subcommand" -a "ls" -d "List profiles"
complete -c caam -n "__fish_use_subcommand" -a "paths" -d "Show auth file paths"
complete -c caam -n "__fish_use_subcommand" -a "run" -d "Run with auto-switching"
complete -c caam -n "__fish_use_subcommand" -a "shell" -d "Shell integration"
complete -c caam -n "__fish_use_subcommand" -a "status" -d "Show current status"
complete -c caam -n "__fish_use_subcommand" -a "tui" -d "Open terminal UI"

# Tool completion
complete -c caam -n "__fish_seen_subcommand_from activate backup clear delete ls paths status" -a "claude codex gemini"

# Profile completion for activate
complete -c caam -n "__fish_seen_subcommand_from activate; and __fish_seen_subcommand_from claude codex gemini" -a "(caam ls (commandline -opc | tail -n1) 2>/dev/null)"
`)

	return sb.String()
}

// shellCompletionCmd generates completion scripts.
var shellCompletionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate completion script",
	Long: `Generate shell completion script.

To load completions:

Bash:
  source <(caam shell completion bash)

  # To load completions for each session, execute once:
  # Linux:
  caam shell completion bash > /etc/bash_completion.d/caam
  # macOS:
  caam shell completion bash > /usr/local/etc/bash_completion.d/caam

Zsh:
  # If shell completion is not already enabled, enable it:
  echo "autoload -U compinit; compinit" >> ~/.zshrc

  # To load completions for each session:
  caam shell completion zsh > "${fpath[1]}/_caam"

Fish:
  caam shell completion fish > ~/.config/fish/completions/caam.fish
`,
	ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
	Args:      cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(os.Stdout)
		case "zsh":
			return rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			return rootCmd.GenFishCompletion(os.Stdout, true)
		case "powershell":
			return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
		default:
			return fmt.Errorf("unknown shell: %s", args[0])
		}
	},
}

func init() {
	shellCmd.AddCommand(shellCompletionCmd)
}
