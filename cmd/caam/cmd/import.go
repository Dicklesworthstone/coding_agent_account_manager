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
	importCmd.Flags().Bool("force", false, "overwrite existing profile(s) if they already exist")
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

	manifest, err := importArchive(r, vault, opt)
	if err != nil {
		return err
	}

	count := len(manifest.Items)
	if opt.AsTool != "" {
		fmt.Printf("Imported 1 profile as %s/%s\n", opt.AsTool, opt.AsProfile)
		return nil
	}

	fmt.Printf("Imported %d profile(s)\n", count)
	return nil
}
