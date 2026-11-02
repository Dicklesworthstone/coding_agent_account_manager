package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import <archive.tar.gz|->",
	Short: "Import profile(s) from an export archive",
	Long: `Import profile auth files from an export archive created by "caam export".

The complete archive and incoming Claude identity bindings are checked offline
before publication. Existing profiles require --force; replacement preserves the
complete previous profile in a private recovery directory and reports its path.
The --as option changes the profile name within the same provider.

Examples:
  caam import codex-work.tar.gz
  cat codex-work.tar.gz | caam import -
  caam import codex-work.tar.gz --as codex/server-work
`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().String("as", "", "import single-profile archive under a new tool/profile (e.g. codex/server-work)")
	importCmd.Flags().Bool("force", false, "replace existing profile(s), preserving and reporting each previous snapshot")
}

func runImport(cmd *cobra.Command, args []string) error {
	inPath := strings.TrimSpace(args[0])
	as, _ := cmd.Flags().GetString("as")
	force, _ := cmd.Flags().GetBool("force")

	var r io.Reader
	var close func() error
	if inPath == "-" {
		r = cmd.InOrStdin()
		close = func() error { return nil }
	} else {
		f, err := os.Open(inPath)
		if err != nil {
			return fmt.Errorf("open archive: %w", err)
		}
		r = f
		close = f.Close
	}
	defer func() { _ = close() }()

	var opt importOptions
	opt.Force = force
	if as != "" {
		tool, profile, err := parseToolProfileArg(as)
		if err != nil {
			return fmt.Errorf("invalid --as: %w", err)
		}
		opt.AsTool = tool
		opt.AsProfile = profile
	}

	result, err := importArchive(r, vault, opt)
	count := 0
	if result != nil {
		for _, profile := range result.Profiles {
			if profile.Imported {
				count++
			}
			if profile.RecoveryPath != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Previous profile preserved: %s\n", profile.RecoveryPath)
			}
		}
	}
	if err != nil {
		if count > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "Imported %d profile(s) before import stopped.\n", count)
		}
		return err
	}

	if opt.AsTool != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Imported 1 profile as %s/%s\n", opt.AsTool, opt.AsProfile)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Imported %d profile(s)\n", count)
	return nil
}
