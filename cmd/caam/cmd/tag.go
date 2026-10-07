package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

var tagCmd = &cobra.Command{
	Use:   "tag",
	Short: "Manage profile tags",
	Long: `Manage tags for profile categorization.

Tags are unordered labels for organizing profiles into categories.
Unlike favorites (which are ordered), tags let you group profiles
by project, team, environment, client, etc.

Tags work on vault profiles (the ones 'caam ls' lists and 'caam activate'
switches) and on isolated profiles. A name that is both carries the same tags.

Tag format: lowercase letters, numbers, and hyphens only (max 32 chars).
Each profile can have up to 10 tags.

Examples:
  caam tag add claude work project-x    # Add tags to a profile
  caam tag remove claude work personal  # Remove a tag from a profile
  caam tag list claude work             # List tags for a profile
  caam tag clear claude work            # Remove all tags from a profile
  caam tag all claude                   # List all tags used for a provider
  caam ls --tag project-x               # List profiles with a tag`,
}

var tagAddCmd = &cobra.Command{
	Use:   "add <tool> <profile> <tag> [tag...]",
	Short: "Add tags to a profile",
	Long: `Add one or more tags to a profile.

Tags must be lowercase, alphanumeric with hyphens only, max 32 characters.
Each profile can have up to 10 tags.

Examples:
  caam tag add claude work project-x
  caam tag add codex main testing dev`,
	Args: cobra.MinimumNArgs(3),
	RunE: runTagAdd,
}

var tagRemoveCmd = &cobra.Command{
	Use:   "remove <tool> <profile> <tag> [tag...]",
	Short: "Remove tags from a profile",
	Long: `Remove one or more tags from a profile.

Examples:
  caam tag remove claude work project-x
  caam tag remove codex main testing`,
	Args: cobra.MinimumNArgs(3),
	RunE: runTagRemove,
}

var tagListCmd = &cobra.Command{
	Use:   "list <tool> <profile>",
	Short: "List tags for a profile",
	Long: `List all tags assigned to a profile.

Examples:
  caam tag list claude work
  caam tag list codex main --json`,
	Args: cobra.ExactArgs(2),
	RunE: runTagList,
}

var tagClearCmd = &cobra.Command{
	Use:   "clear <tool> <profile>",
	Short: "Remove all tags from a profile",
	Long: `Remove all tags from a profile.

Examples:
  caam tag clear claude work`,
	Args: cobra.ExactArgs(2),
	RunE: runTagClear,
}

var tagAllCmd = &cobra.Command{
	Use:   "all <tool>",
	Short: "List all tags used for a provider",
	Long: `List all unique tags used across all profiles for a provider.

Examples:
  caam tag all claude
  caam tag all codex --json`,
	Args: cobra.ExactArgs(1),
	RunE: runTagAll,
}

func init() {
	rootCmd.AddCommand(tagCmd)
	tagCmd.AddCommand(tagAddCmd)
	tagCmd.AddCommand(tagRemoveCmd)
	tagCmd.AddCommand(tagListCmd)
	tagCmd.AddCommand(tagClearCmd)
	tagCmd.AddCommand(tagAllCmd)

	// Add --json flag to list and all commands
	tagListCmd.Flags().Bool("json", false, "output in JSON format")
	tagAllCmd.Flags().Bool("json", false, "output in JSON format")
}

// profileLabels are the tags and description of one named profile. Vault
// profiles keep them in the vault's meta.json; isolated profiles keep them in
// their profile.json. A name that is both is labeled in both, so 'caam ls'
// and isolated-profile listings agree.
type profileLabels struct {
	tool, name  string
	inVault     bool
	isolated    *profile.Profile
	Tags        []string
	Description string
}

// loadProfileLabels resolves a profile name to its label storage. It fails
// only when neither a vault nor an isolated profile has the name.
func loadProfileLabels(tool, name string) (*profileLabels, error) {
	pl := &profileLabels{tool: tool, name: name}

	if vault != nil {
		labels, err := vault.Labels(tool, name)
		switch {
		case err == nil:
			pl.inVault = true
			pl.Tags = append([]string(nil), labels.Tags...)
			pl.Description = labels.Description
		case !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("load vault profile %s/%s: %w", tool, name, err)
		}
	}

	store := profileStore
	if store == nil {
		store = profile.NewStore(profile.DefaultStorePath())
	}
	if prof, err := store.Load(tool, name); err == nil {
		pl.isolated = prof
		for _, tag := range prof.Tags {
			if !pl.HasTag(tag) {
				pl.Tags = append(pl.Tags, profile.NormalizeTag(tag))
			}
		}
		if pl.Description == "" {
			pl.Description = prof.Description
		}
	} else if !errors.Is(err, profile.ErrNotFound) && !pl.inVault {
		// A vault profile name the isolated store cannot represent simply
		// has no isolated counterpart.
		return nil, fmt.Errorf("load profile %s/%s: %w", tool, name, err)
	}

	if !pl.inVault && pl.isolated == nil {
		return nil, fmt.Errorf("profile %s/%s not found (see 'caam ls')", tool, name)
	}
	return pl, nil
}

// HasTag reports whether the profile carries tag.
func (pl *profileLabels) HasTag(tag string) bool {
	tag = profile.NormalizeTag(tag)
	for _, t := range pl.Tags {
		if profile.NormalizeTag(t) == tag {
			return true
		}
	}
	return false
}

// AddTag adds a valid tag, enforcing the per-profile limit.
func (pl *profileLabels) AddTag(tag string) error {
	tag = profile.NormalizeTag(tag)
	if err := profile.ValidateTag(tag); err != nil {
		return err
	}
	if pl.HasTag(tag) {
		return nil
	}
	if len(pl.Tags) >= profile.MaxTagCount {
		return fmt.Errorf("cannot add tag: maximum of %d tags allowed", profile.MaxTagCount)
	}
	pl.Tags = append(pl.Tags, tag)
	return nil
}

// RemoveTag removes tag and reports whether it was present.
func (pl *profileLabels) RemoveTag(tag string) bool {
	tag = profile.NormalizeTag(tag)
	for i, t := range pl.Tags {
		if profile.NormalizeTag(t) == tag {
			pl.Tags = append(pl.Tags[:i], pl.Tags[i+1:]...)
			return true
		}
	}
	return false
}

// Save writes the labels to every store that holds the profile.
func (pl *profileLabels) Save() error {
	if pl.inVault {
		if err := vault.SetLabels(pl.tool, pl.name, authfile.ProfileLabels{Tags: pl.Tags, Description: pl.Description}); err != nil {
			return fmt.Errorf("save vault profile labels: %w", err)
		}
	}
	if pl.isolated != nil {
		pl.isolated.Tags = append([]string(nil), pl.Tags...)
		pl.isolated.Description = pl.Description
		if err := pl.isolated.Save(); err != nil {
			return fmt.Errorf("save profile: %w", err)
		}
	}
	return nil
}

// vaultProfileTags returns the tags of a vault profile (nil when unlabeled
// or not a vault profile).
func vaultProfileTags(tool, name string) []string {
	if vault == nil {
		return nil
	}
	labels, err := vault.Labels(tool, name)
	if err != nil {
		return nil
	}
	return labels.Tags
}

func runTagAdd(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	tags := args[2:]

	labels, err := loadProfileLabels(tool, profileName)
	if err != nil {
		return err
	}

	added := 0
	for _, tag := range tags {
		if labels.HasTag(tag) {
			continue
		}
		if err := labels.AddTag(tag); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot add tag %q: %v\n", tag, err)
			continue
		}
		added++
	}

	if err := labels.Save(); err != nil {
		return err
	}

	if added == 1 {
		fmt.Printf("Added 1 tag to %s/%s\n", tool, profileName)
	} else {
		fmt.Printf("Added %d tags to %s/%s\n", added, tool, profileName)
	}
	fmt.Printf("Tags: %s\n", strings.Join(labels.Tags, ", "))

	return nil
}

func runTagRemove(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	tags := args[2:]

	labels, err := loadProfileLabels(tool, profileName)
	if err != nil {
		return err
	}

	removed := 0
	for _, tag := range tags {
		if labels.RemoveTag(tag) {
			removed++
		} else {
			fmt.Fprintf(os.Stderr, "Warning: tag %q not found on profile\n", tag)
		}
	}

	if err := labels.Save(); err != nil {
		return err
	}

	if removed == 1 {
		fmt.Printf("Removed 1 tag from %s/%s\n", tool, profileName)
	} else {
		fmt.Printf("Removed %d tags from %s/%s\n", removed, tool, profileName)
	}

	if len(labels.Tags) > 0 {
		fmt.Printf("Remaining tags: %s\n", strings.Join(labels.Tags, ", "))
	} else {
		fmt.Println("No tags remaining")
	}

	return nil
}

func runTagList(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]
	jsonOutput, _ := cmd.Flags().GetBool("json")

	labels, err := loadProfileLabels(tool, profileName)
	if err != nil {
		return err
	}

	if jsonOutput {
		output := struct {
			Tool    string   `json:"tool"`
			Profile string   `json:"profile"`
			Tags    []string `json:"tags"`
		}{
			Tool:    tool,
			Profile: profileName,
			Tags:    labels.Tags,
		}
		if output.Tags == nil {
			output.Tags = []string{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	if len(labels.Tags) == 0 {
		fmt.Printf("No tags for %s/%s\n", tool, profileName)
		return nil
	}

	fmt.Printf("Tags for %s/%s:\n", tool, profileName)
	for _, tag := range labels.Tags {
		fmt.Printf("  %s\n", tag)
	}

	return nil
}

func runTagClear(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	profileName := args[1]

	labels, err := loadProfileLabels(tool, profileName)
	if err != nil {
		return err
	}

	count := len(labels.Tags)
	labels.Tags = nil

	if err := labels.Save(); err != nil {
		return err
	}

	if count == 0 {
		fmt.Printf("No tags to clear for %s/%s\n", tool, profileName)
	} else if count == 1 {
		fmt.Printf("Cleared 1 tag from %s/%s\n", tool, profileName)
	} else {
		fmt.Printf("Cleared %d tags from %s/%s\n", count, tool, profileName)
	}

	return nil
}

func runTagAll(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(args[0])
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if profileStore == nil {
		profileStore = profile.NewStore(profile.DefaultStorePath())
	}

	// Isolated profile tags
	isolatedTags, err := profileStore.AllTags(tool)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}
	seen := make(map[string]bool)
	var tags []string
	add := func(tag string) {
		tag = profile.NormalizeTag(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	for _, tag := range isolatedTags {
		add(tag)
	}

	// Vault profile tags
	if vault != nil {
		if names, err := vault.List(tool); err == nil {
			for _, name := range names {
				for _, tag := range vaultProfileTags(tool, name) {
					add(tag)
				}
			}
		}
	}

	// Sort tags alphabetically
	sort.Strings(tags)

	if jsonOutput {
		output := struct {
			Tool string   `json:"tool"`
			Tags []string `json:"tags"`
		}{
			Tool: tool,
			Tags: tags,
		}
		if output.Tags == nil {
			output.Tags = []string{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	if len(tags) == 0 {
		fmt.Printf("No tags used for %s profiles\n", tool)
		return nil
	}

	fmt.Printf("Tags used for %s profiles:\n", tool)
	for _, tag := range tags {
		fmt.Printf("  %s\n", tag)
	}

	return nil
}
