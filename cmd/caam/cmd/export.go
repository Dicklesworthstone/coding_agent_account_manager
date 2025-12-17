package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export [tool/profile] [tool profile]",
	Short: "Export profile(s) for transfer to another machine",
	Long: `Export profile auth files for transfer to another machine.

This is the recommended way to set up profiles on headless servers:
1. Login normally on a machine with a browser
2. Export the profile: caam export codex/work > profile.tar.gz
3. Transfer to headless server: scp profile.tar.gz server:
4. Import on server: caam import profile.tar.gz
5. Activate: caam activate codex work

The exported file contains only the auth credentials, not session state.`,
	Args: cobra.RangeArgs(0, 2),
	RunE: runExport,
}

func init() {
	exportCmd.Flags().Bool("all", false, "export all profiles (use with optional <tool>)")
	exportCmd.Flags().StringP("output", "o", "", "write archive to file instead of stdout")
}

func runExport(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	outPath, _ := cmd.Flags().GetString("output")

	var req exportRequest
	switch {
	case all && len(args) == 0:
		req = exportRequest{All: true}
	case all && len(args) == 1:
		req = exportRequest{ToolAll: true, Tool: args[0]}
	case all:
		return fmt.Errorf("usage: caam export --all [tool]")
	case len(args) == 1:
		tool, profile, err := parseToolProfileArg(args[0])
		if err != nil {
			return err
		}
		req = exportRequest{Tool: tool, Profile: profile}
	case len(args) == 2:
		req = exportRequest{Tool: args[0], Profile: args[1]}
	default:
		return fmt.Errorf("usage: caam export <tool/profile> or caam export <tool> <profile> or caam export --all [tool]")
	}

	targets, err := resolveExportTargets(vault, req)
	if err != nil {
		return err
	}
	manifest, files, err := buildExportManifest(targets)
	if err != nil {
		return err
	}

	var (
		w     io.Writer
		close func() error
	)
	if outPath != "" {
		f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return fmt.Errorf("open output file: %w", err)
		}
		w = f
		close = f.Close
	} else {
		w = cmd.OutOrStdout()
		close = func() error { return nil }
	}

	if err := writeExportArchive(w, manifest, files); err != nil {
		_ = close()
		return err
	}
	if err := close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "Exported %d profile(s)\n", len(targets))
	if outPath != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "  Output: %s\n", outPath)
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), "  Output: stdout")
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "  Warning: archive contains OAuth tokens; treat it like a password.")
	fmt.Fprintln(cmd.ErrOrStderr(), "           Consider encrypting during transfer (e.g. `gpg -c`).")
	return nil
}

func parseToolProfileArg(arg string) (tool, profile string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", fmt.Errorf("tool/profile cannot be empty")
	}
	parts := strings.Split(arg, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected tool/profile, got %q", arg)
	}
	tool = strings.ToLower(strings.TrimSpace(parts[0]))
	profile = strings.TrimSpace(parts[1])
	if tool == "" || profile == "" {
		return "", "", fmt.Errorf("expected tool/profile, got %q", arg)
	}
	return tool, profile, nil
}
