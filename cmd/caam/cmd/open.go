// Package cmd implements the CLI commands for caam.
package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
)

// providerAccountURLs maps provider IDs to their account management URLs.
var providerAccountURLs = map[string]struct {
	URL         string
	Description string
}{
	"codex": {
		URL:         "https://platform.openai.com/account",
		Description: "OpenAI Platform account settings",
	},
	"claude": {
		URL:         "https://console.anthropic.com/",
		Description: "Anthropic Console dashboard",
	},
	"gemini": {
		URL:         "https://aistudio.google.com/",
		Description: "Google AI Studio dashboard",
	},
}

var openCmd = &cobra.Command{
	Use:   "open <tool> [profile]",
	Short: "Open provider account page in browser",
	Long: `Opens the account management page for a provider in your browser.

If a profile is specified and has browser configuration, the URL will be
opened in that browser profile (so you see the correct account's dashboard).

If no profile is specified, or the profile has no browser config, the URL
will be opened in your system's default browser.

Providers and their URLs:
  codex   - OpenAI Platform (https://platform.openai.com/account)
  claude  - Anthropic Console (https://console.anthropic.com/)
  gemini  - Google AI Studio (https://aistudio.google.com/)

Examples:
  caam open codex           # Open OpenAI account in default browser
  caam open claude work     # Open Anthropic console in work profile's browser
  caam open gemini personal # Open AI Studio in personal profile's browser`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])

		// Validate provider
		urlInfo, ok := providerAccountURLs[tool]
		if !ok {
			return fmt.Errorf("unknown provider: %s (supported: codex, claude, gemini)", tool)
		}

		// Allow custom URL override
		customURL, _ := cmd.Flags().GetString("url")
		url := urlInfo.URL
		if customURL != "" {
			url = customURL
		}

		// Determine launcher
		var launcher browser.Launcher

		if len(args) > 1 {
			// Profile specified - try to use its browser config
			profileName := args[1]
			prof, err := profileStore.Load(tool, profileName)
			if err != nil {
				return fmt.Errorf("load profile: %w", err)
			}

			if prof.HasBrowserConfig() {
				launcher = browser.NewLauncher(&browser.Config{
					Command:    prof.BrowserCommand,
					ProfileDir: prof.BrowserProfileDir,
				})
				fmt.Printf("Opening %s in browser profile: %s\n", urlInfo.Description, prof.BrowserDisplayName())
			} else {
				launcher = &browser.DefaultLauncher{}
				fmt.Printf("Opening %s in default browser (profile has no browser config)\n", urlInfo.Description)
			}
		} else {
			// No profile - use default browser
			launcher = &browser.DefaultLauncher{}
			fmt.Printf("Opening %s in default browser\n", urlInfo.Description)
		}

		fmt.Printf("  URL: %s\n", url)

		if err := launcher.Open(url); err != nil {
			return fmt.Errorf("open browser: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(openCmd)
	openCmd.Flags().String("url", "", "custom URL to open (overrides default)")
}
