package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// addLimitsRankFlags registers the rank flags. It is shared by the real
// command and by tests, so the two can never define them differently.
func addLimitsRankFlags(cmd *cobra.Command) {
	cmd.Flags().String("rank", "", "rank profiles for new work: earliest-reset-headroom (spend soonest-refreshing included quota first) or availability (idlest first, as --best)")
	cmd.Flags().Int("headroom", usage.DefaultHeadroomCeiling, "percent-used ceiling below which an included allowance still counts as usable (1-100); defaults to stealth.rotation.drain_headroom_ceiling")
	cmd.Flags().Bool("require-model-window", false, "with --rank and --model, require each profile to publish that model's own allowance; an omitted row is reported as unknown, never as capacity (default: on when the provider publishes per-model allowances)")
}

// rankOptionsFromFlags reads the rank-related flags off `caam limits`.
//
// The headroom ceiling comes from the same setting the rotation drain policy
// uses (stealth.rotation.drain_headroom_ceiling) — it is one concept, so it
// gets one number — with --headroom overriding it for a single call.
func rankOptionsFromFlags(cmd *cobra.Command, provider, model string) (usage.RankOptions, error) {
	rank, _ := cmd.Flags().GetString("rank")
	mode, ok := usage.NormalizeRankMode(rank)
	if !ok {
		return usage.RankOptions{}, fmt.Errorf("unknown --rank %q (want one of: %s)",
			rank, strings.Join(usage.RankModes, ", "))
	}

	ceiling := usage.DefaultHeadroomCeiling
	if cfg, err := config.LoadSPMConfig(); err == nil && cfg != nil && cfg.Stealth.Rotation.DrainHeadroomCeiling > 0 {
		ceiling = cfg.Stealth.Rotation.DrainHeadroomCeiling
	}
	if cmd.Flags().Changed("headroom") {
		v, _ := cmd.Flags().GetInt("headroom")
		if v < 1 || v > 100 {
			return usage.RankOptions{}, fmt.Errorf("--headroom must be between 1 and 100, got %d", v)
		}
		ceiling = v
	}

	opts := usage.RankOptions{
		Mode:            mode,
		Provider:        provider,
		Model:           model,
		HeadroomCeiling: ceiling,
	}

	// Requiring the model's own window defaults on whenever a model is named
	// AND the provider demonstrably publishes per-model allowances: that is
	// the case where an omitted row means "nobody read this quota", not "this
	// provider has no such quota". An explicit --require-model-window wins
	// either way.
	if cmd.Flags().Changed("require-model-window") {
		opts.RequireModelWindow, _ = cmd.Flags().GetBool("require-model-window")
	}
	return opts, nil
}

// anyProfilePublishesScopedWindow reports whether at least one row carries a
// model-scoped allowance. It is how the rank mode tells "this provider does
// not do per-model quotas" apart from "this seat's per-model quota is
// missing".
// The legacy per-model window (an Opus allowance on accounts that still report
// it that way) counts too, so a pool on the older response shape is not
// mistaken for a provider without per-model quotas.
func anyProfilePublishesScopedWindow(rows []usage.ProfileUsage) bool {
	for _, r := range rows {
		if r.Usage == nil {
			continue
		}
		if len(r.Usage.ModelWindows) > 0 || r.Usage.TertiaryWindow != nil {
			return true
		}
	}
	return false
}

// runLimitsRank ranks the fetched rows and renders the result. It returns a
// non-nil error when nothing is selectable, so a caller gets a non-zero exit
// alongside the payload rather than a confident empty answer.
func runLimitsRank(cmd *cobra.Command, out io.Writer, format string, rows []usage.ProfileUsage, opts usage.RankOptions) error {
	if opts.Model != "" && !cmd.Flags().Changed("require-model-window") {
		opts.RequireModelWindow = anyProfilePublishesScopedWindow(rows)
	}

	result := usage.RankProfiles(rows, opts)

	if err := renderRank(out, format, result); err != nil {
		return err
	}
	if result.Error != "" {
		// The payload is the guidance; cobra's usage block would bury it.
		// Errors themselves stay un-silenced so the message also reaches
		// stderr, which leaves stdout as pure JSON for a machine caller.
		cmd.SilenceUsage = true
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

func renderRank(w io.Writer, format string, result *usage.RankResult) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(data))
		return nil

	case "table", "":
		return renderRankTable(w, result)

	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

func renderRankTable(w io.Writer, result *usage.RankResult) error {
	fmt.Fprintf(w, "Rank: %s (headroom ceiling %d%%", result.Rank, result.HeadroomCeiling)
	if result.Model != "" {
		fmt.Fprintf(w, ", model %s", result.Model)
		if result.RequireModelWindow {
			fmt.Fprint(w, ", model window required")
		}
	}
	fmt.Fprintln(w, ")")
	fmt.Fprintln(w, strings.Repeat("─", 90))

	if len(result.Profiles) == 0 {
		fmt.Fprintln(w, "No profiles found.")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tPROFILE\tTIER\tUSED\tRESETS IN\tWHY")
		for _, p := range result.Profiles {
			pos := "-"
			if p.Rank > 0 {
				pos = fmt.Sprintf("%d", p.Rank)
			}
			resets := "-"
			if p.ResetsInSeconds != nil {
				resets = formatLimitsDuration(time.Duration(*p.ResetsInSeconds) * time.Second)
			}
			fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%d%%\t%s\t%s\n",
				pos, p.Provider, p.Profile, p.Tier, p.UsedPercent, resets, p.Reason)
		}
		tw.Flush()
	}

	fmt.Fprintln(w)
	if result.Selected != nil {
		fmt.Fprintf(w, "Selected: %s/%s — %s\n",
			result.Selected.Provider, result.Selected.Profile, result.Selected.Reason)
	} else {
		fmt.Fprintf(w, "Selected: none — %s\n", result.Error)
	}
	return nil
}
