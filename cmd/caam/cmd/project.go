package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// projectCmd is the parent command for project association management.
var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage project-profile associations",
	Long: `Project associations let you pin a provider profile to a directory.

This enables workflows like:
  - Different accounts per client repo
  - Work/personal separation by directory
  - Team repos using shared accounts

Examples:
  caam project set claude client-a@work.com
  caam project show
  caam project list
`,
}

func init() {
	rootCmd.AddCommand(projectCmd)
	projectCmd.AddCommand(projectSetCmd)
	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectShowCmd)
	projectCmd.AddCommand(projectRemoveCmd)
	projectCmd.AddCommand(projectClearCmd)
}

var projectSetCmd = &cobra.Command{
	Use:   "set <tool> <profile>",
	Short: "Associate current directory with a profile",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		profileName := args[1]

		if _, ok := tools[tool]; !ok {
			return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini, cursor)", tool)
		}
		if projectStore == nil {
			return fmt.Errorf("project store not initialized")
		}

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}

		if err := projectStore.SetAssociation(cwd, tool, profileName); err != nil {
			return err
		}

		fmt.Printf("Associated %s with %s/%s\n", cwd, tool, profileName)
		return nil
	},
}

// ProjectAssociation represents a project's associations for JSON output.
type ProjectAssociation struct {
	Path      string            `json:"path"`
	Providers map[string]string `json:"providers"`
}

// ProjectListOutput represents the complete project list JSON output.
type ProjectListOutput struct {
	Projects []ProjectAssociation `json:"projects"`
	Count    int                  `json:"count"`
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all project associations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOutput, _ := cmd.Flags().GetBool("json")

		if projectStore == nil {
			return fmt.Errorf("project store not initialized")
		}

		data, err := projectStore.Load()
		if err != nil {
			return err
		}

		if jsonOutput {
			output := ProjectListOutput{
				Projects: make([]ProjectAssociation, 0, len(data.Associations)),
				Count:    len(data.Associations),
			}
			for projectPath, assoc := range data.Associations {
				output.Projects = append(output.Projects, ProjectAssociation{
					Path:      projectPath,
					Providers: assoc,
				})
			}
			jsonData, err := json.MarshalIndent(output, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(jsonData))
			return nil
		}

		if len(data.Associations) == 0 {
			fmt.Println("No project associations set.")
			return nil
		}

		projects := make([]string, 0, len(data.Associations))
		for p := range data.Associations {
			projects = append(projects, p)
		}
		sort.Strings(projects)

		for i, projectPath := range projects {
			if i > 0 {
				fmt.Println()
			}

			fmt.Println(projectPath)

			assoc := data.Associations[projectPath]
			providers := make([]string, 0, len(assoc))
			for provider := range assoc {
				providers = append(providers, provider)
			}
			sort.Strings(providers)

			for _, provider := range providers {
				fmt.Printf("  %s: %s\n", provider, assoc[provider])
			}
		}

		return nil
	},
}

func init() {
	projectListCmd.Flags().Bool("json", false, "output as JSON")
}

var projectShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show resolved associations for current directory",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if projectStore == nil {
			return fmt.Errorf("project store not initialized")
		}

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}

		resolved, err := projectStore.Resolve(cwd)
		if err != nil {
			return err
		}
		if len(resolved.Profiles) == 0 {
			fmt.Printf("Project: %s\n", cwd)
			fmt.Println("No associations.")
			return nil
		}

		fmt.Printf("Project: %s\n", cwd)
		fmt.Println("Associations:")

		providers := make([]string, 0, len(resolved.Profiles))
		for provider := range resolved.Profiles {
			providers = append(providers, provider)
		}
		sort.Strings(providers)

		for _, provider := range providers {
			profileName := resolved.Profiles[provider]
			source := resolved.Sources[provider]
			if source != "" && source != cwd {
				fmt.Printf("  %s: %s  (from %s)\n", provider, profileName, source)
				continue
			}
			fmt.Printf("  %s: %s\n", provider, profileName)
		}

		return nil
	},
}

var projectRemoveCmd = &cobra.Command{
	Use:   "remove <tool>",
	Short: "Remove a single association for the current directory",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tool := strings.ToLower(args[0])
		if _, ok := tools[tool]; !ok {
			return fmt.Errorf("unknown tool: %s (supported: codex, claude, gemini, cursor)", tool)
		}
		if projectStore == nil {
			return fmt.Errorf("project store not initialized")
		}

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}

		if err := projectStore.RemoveAssociation(cwd, tool); err != nil {
			return err
		}

		fmt.Printf("Removed %s association from %s\n", tool, cwd)
		return nil
	},
}

var projectClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all associations for the current directory",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if projectStore == nil {
			return fmt.Errorf("project store not initialized")
		}

		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get current directory: %w", err)
		}

		if err := projectStore.DeleteProject(cwd); err != nil {
			return err
		}

		fmt.Printf("Cleared all associations from %s\n", cwd)
		return nil
	},
}
