package cmd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// newRankTestCmd builds a throwaway command carrying the same rank flags the
// real `caam limits` does, so a test never mutates the shared global command.
func newRankTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "limits", RunE: func(*cobra.Command, []string) error { return nil }}
	addLimitsRankFlags(cmd)
	cmd.Flags().String("model", "", "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	return cmd
}

// TestHeadroomCeilingMatchesDrainCeiling pins the two constants together. The
// rank mode's headroom ceiling and the rotation drain policy's are the same
// concept and read the same config key, so they must not drift apart.
func TestHeadroomCeilingMatchesDrainCeiling(t *testing.T) {
	if usage.DefaultHeadroomCeiling != rotation.DefaultDrainCeiling {
		t.Errorf("usage.DefaultHeadroomCeiling = %d but rotation.DefaultDrainCeiling = %d; "+
			"they share stealth.rotation.drain_headroom_ceiling and must agree",
			usage.DefaultHeadroomCeiling, rotation.DefaultDrainCeiling)
	}
}

func TestRankOptionsFromFlags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		model       string
		wantMode    string
		wantCeiling int
		wantRequire bool
		wantErr     string
	}{
		{
			name:        "canonical mode with the default ceiling",
			args:        []string{"--rank", "earliest-reset-headroom"},
			wantMode:    usage.RankEarliestResetHeadroom,
			wantCeiling: usage.DefaultHeadroomCeiling,
		},
		{
			name:        "the issue's longer spelling is accepted",
			args:        []string{"--rank", "earliest-reset-with-headroom"},
			wantMode:    usage.RankEarliestResetHeadroom,
			wantCeiling: usage.DefaultHeadroomCeiling,
		},
		{
			name:        "availability names the historical ordering",
			args:        []string{"--rank", "availability"},
			wantMode:    usage.RankAvailability,
			wantCeiling: usage.DefaultHeadroomCeiling,
		},
		{
			name:        "an explicit headroom overrides the default",
			args:        []string{"--rank", "earliest-reset-headroom", "--headroom", "60"},
			wantMode:    usage.RankEarliestResetHeadroom,
			wantCeiling: 60,
		},
		{
			name:        "require-model-window can be forced on",
			args:        []string{"--rank", "earliest-reset-headroom", "--require-model-window"},
			model:       "fable",
			wantMode:    usage.RankEarliestResetHeadroom,
			wantCeiling: usage.DefaultHeadroomCeiling,
			wantRequire: true,
		},
		{
			name:    "an unknown mode is refused by name",
			args:    []string{"--rank", "best"},
			wantErr: `unknown --rank "best"`,
		},
		{
			name:    "the drain policy is not a rank mode",
			args:    []string{"--rank", "drain"},
			wantErr: "unknown --rank",
		},
		{
			name:    "a headroom of zero is refused",
			args:    []string{"--rank", "earliest-reset-headroom", "--headroom", "0"},
			wantErr: "--headroom must be between 1 and 100",
		},
		{
			name:    "a headroom over 100 is refused",
			args:    []string{"--rank", "earliest-reset-headroom", "--headroom", "101"},
			wantErr: "--headroom must be between 1 and 100",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRankTestCmd(t, tc.args...)
			opts, err := rankOptionsFromFlags(cmd, "codex", tc.model)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error mentioning %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", opts.Mode, tc.wantMode)
			}
			if opts.HeadroomCeiling != tc.wantCeiling {
				t.Errorf("ceiling = %d, want %d", opts.HeadroomCeiling, tc.wantCeiling)
			}
			if opts.RequireModelWindow != tc.wantRequire {
				t.Errorf("require-model-window = %v, want %v", opts.RequireModelWindow, tc.wantRequire)
			}
			if opts.Provider != "codex" {
				t.Errorf("provider = %q, want codex", opts.Provider)
			}
		})
	}
}

// rankRow builds a Codex-shaped row for the CLI-level tests.
func rankRow(name string, weeklyPct int, resetsIn time.Duration) usage.ProfileUsage {
	now := time.Now()
	return usage.ProfileUsage{
		Provider:    "codex",
		ProfileName: name,
		Usage: &usage.UsageInfo{
			Provider:    "codex",
			ProfileName: name,
			PrimaryWindow: &usage.UsageWindow{
				UsedPercent: 0, ResetsAt: now.Add(time.Hour), WindowDuration: 5 * time.Hour,
			},
			SecondaryWindow: &usage.UsageWindow{
				UsedPercent: weeklyPct, ResetsAt: now.Add(resetsIn), WindowDuration: 7 * 24 * time.Hour,
			},
		},
	}
}

// TestRunLimitsRank_JSONSelectsAndExitsZero is the happy path a spawn-time
// caller depends on.
func TestRunLimitsRank_JSONSelectsAndExitsZero(t *testing.T) {
	cmd := newRankTestCmd(t, "--rank", "earliest-reset-headroom")
	opts, err := rankOptionsFromFlags(cmd, "codex", "")
	if err != nil {
		t.Fatalf("options: %v", err)
	}

	rows := []usage.ProfileUsage{
		rankRow("reserve", 0, 120*time.Hour),
		rankRow("spend-me", 34, 20*time.Hour),
	}

	var out strings.Builder
	if err := runLimitsRank(cmd, &out, "json", rows, opts); err != nil {
		t.Fatalf("want a zero exit on a rankable pool, got %v; output=%s", err, out.String())
	}

	var payload usage.RankResult
	if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v; output=%s", err, out.String())
	}
	if payload.Selected == nil || payload.Selected.Profile != "spend-me" {
		t.Errorf("selected = %+v, want spend-me", payload.Selected)
	}
	if payload.Error != "" {
		t.Errorf("error = %q, want empty", payload.Error)
	}
}

// TestRunLimitsRank_UnreadableLimitsFailVisibly is the contract issue #105
// asks for: no silent fallthrough to a static pin. The payload still lands on
// stdout so a caller can see every reason, and the exit is non-zero.
func TestRunLimitsRank_UnreadableLimitsFailVisibly(t *testing.T) {
	cmd := newRankTestCmd(t, "--rank", "earliest-reset-headroom")
	opts, err := rankOptionsFromFlags(cmd, "codex", "")
	if err != nil {
		t.Fatalf("options: %v", err)
	}

	rows := []usage.ProfileUsage{
		{Provider: "codex", ProfileName: "a", Usage: &usage.UsageInfo{Error: "unauthorized: token expired or invalid"}},
		{Provider: "codex", ProfileName: "b", Usage: &usage.UsageInfo{Error: "request failed"}},
	}

	var out strings.Builder
	runErr := runLimitsRank(cmd, &out, "json", rows, opts)
	if runErr == nil {
		t.Fatalf("want a non-zero exit when nothing is selectable; output=%s", out.String())
	}

	var payload usage.RankResult
	if jerr := json.Unmarshal([]byte(out.String()), &payload); jerr != nil {
		t.Fatalf("failure output is not valid JSON: %v; output=%s", jerr, out.String())
	}
	if payload.Selected != nil {
		t.Errorf("selected = %+v, want null", payload.Selected)
	}
	if payload.Error == "" {
		t.Error("want a non-empty error field on the payload")
	}
	if payload.Error != runErr.Error() {
		t.Errorf("payload error %q and returned error %q must agree", payload.Error, runErr)
	}
	if len(payload.Profiles) != 2 {
		t.Errorf("profiles = %d, want both rows kept with their reasons", len(payload.Profiles))
	}
	for _, p := range payload.Profiles {
		if p.Error == "" {
			t.Errorf("profile %s dropped its fetch error", p.Profile)
		}
	}
	if !cmd.SilenceUsage {
		t.Error("the usage block must be silenced on a runtime rank failure")
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Errorf("did not expect usage text in the JSON failure output; output=%s", out.String())
	}
}

// TestRunLimitsRank_RequireModelWindowDefaultsOnlyWhereMeaningful covers the
// auto-default: a provider that publishes per-model allowances makes an
// omitted row suspicious; one that publishes none makes it meaningless.
func TestRunLimitsRank_RequireModelWindowDefaultsOnlyWhereMeaningful(t *testing.T) {
	scoped := func(name string, pct int) usage.ProfileUsage {
		row := rankRow(name, 5, 30*time.Hour)
		row.Provider = "claude"
		row.Usage.Provider = "claude"
		row.Usage.ModelWindows = map[string]*usage.UsageWindow{
			"Fable": {
				UsedPercent: pct, ResetsAt: time.Now().Add(30 * time.Hour),
				WindowDuration: 7 * 24 * time.Hour, Label: "Fable",
			},
		}
		return row
	}

	t.Run("a missing row is refused when siblings publish one", func(t *testing.T) {
		cmd := newRankTestCmd(t, "--rank", "earliest-reset-headroom", "--model", "fable")
		opts, err := rankOptionsFromFlags(cmd, "claude", "fable")
		if err != nil {
			t.Fatalf("options: %v", err)
		}
		missing := rankRow("no-fable-row", 5, 10*time.Hour)
		missing.Provider = "claude"
		missing.Usage.Provider = "claude"

		var out strings.Builder
		if err := runLimitsRank(cmd, &out, "json", []usage.ProfileUsage{missing, scoped("has-fable", 20)}, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var payload usage.RankResult
		if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if !payload.RequireModelWindow {
			t.Fatal("require_model_window should default on when a sibling publishes a scoped row")
		}
		if payload.Selected == nil || payload.Selected.Profile != "has-fable" {
			t.Errorf("selected = %+v, want has-fable", payload.Selected)
		}
	})

	t.Run("a provider with no per-model allowances is unaffected", func(t *testing.T) {
		cmd := newRankTestCmd(t, "--rank", "earliest-reset-headroom", "--model", "gpt-5")
		opts, err := rankOptionsFromFlags(cmd, "codex", "gpt-5")
		if err != nil {
			t.Fatalf("options: %v", err)
		}

		var out strings.Builder
		if err := runLimitsRank(cmd, &out, "json", []usage.ProfileUsage{rankRow("a", 10, 20*time.Hour)}, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var payload usage.RankResult
		if err := json.Unmarshal([]byte(out.String()), &payload); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if payload.RequireModelWindow {
			t.Error("require_model_window should stay off for a provider that publishes no per-model allowances")
		}
		if payload.Selected == nil {
			t.Errorf("selected = null, want the seat to remain rankable; error=%q", payload.Error)
		}
	})
}

// TestRunLimitsRank_TableRendersEveryProfile keeps the human output honest
// about the seats it did not pick.
func TestRunLimitsRank_TableRendersEveryProfile(t *testing.T) {
	cmd := newRankTestCmd(t, "--rank", "earliest-reset-headroom")
	opts, err := rankOptionsFromFlags(cmd, "codex", "")
	if err != nil {
		t.Fatalf("options: %v", err)
	}

	rows := []usage.ProfileUsage{rankRow("reserve", 0, 120*time.Hour), rankRow("spend-me", 34, 20*time.Hour)}
	var out strings.Builder
	if err := runLimitsRank(cmd, &out, "table", rows, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := out.String()
	for _, want := range []string{"earliest-reset-headroom", "spend-me", "reserve", "Selected: codex/spend-me"} {
		if !strings.Contains(got, want) {
			t.Errorf("table output missing %q; got:\n%s", want, got)
		}
	}
}

// TestLimitsRankAndBestAreAlternatives verifies the two are refused together
// rather than one silently winning.
func TestLimitsRankAndBestAreAlternatives(t *testing.T) {
	t.Cleanup(func() {
		_ = limitsCmd.Flags().Set("rank", "")
		_ = limitsCmd.Flags().Set("best", "false")
		limitsCmd.SilenceUsage = false
	})
	if err := limitsCmd.Flags().Set("rank", "earliest-reset-headroom"); err != nil {
		t.Fatalf("set rank: %v", err)
	}
	if err := limitsCmd.Flags().Set("best", "true"); err != nil {
		t.Fatalf("set best: %v", err)
	}

	err := runLimits(limitsCmd, []string{"codex"})
	if err == nil {
		t.Fatal("want an error when --rank and --best are combined")
	}
	if !strings.Contains(err.Error(), "--rank and --best are alternatives") {
		t.Errorf("error = %q, want it to explain the two are alternatives", err)
	}
}

// TestLimitsRankRejectsBadModeBeforeFetching keeps a typo from costing a
// minute of API calls.
func TestLimitsRankRejectsBadModeBeforeFetching(t *testing.T) {
	t.Cleanup(func() {
		_ = limitsCmd.Flags().Set("rank", "")
		limitsCmd.SilenceUsage = false
	})
	if err := limitsCmd.Flags().Set("rank", "nonsense"); err != nil {
		t.Fatalf("set rank: %v", err)
	}

	start := time.Now()
	err := runLimits(limitsCmd, []string{"codex"})
	if err == nil {
		t.Fatal("want an error for an unknown rank mode")
	}
	if !strings.Contains(err.Error(), "unknown --rank") {
		t.Errorf("error = %q, want it to name the unknown mode", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("validation took %s; it must reject before fetching", elapsed)
	}
}

// TestLimitsRankRequiresAProvider keeps a bare `caam limits --rank ...` from
// ranking Claude and Codex seats against each other, which has no meaning: a
// reset time on one provider says nothing about a seat on the other.
func TestLimitsRankRequiresAProvider(t *testing.T) {
	t.Cleanup(func() {
		_ = limitsCmd.Flags().Set("rank", "")
		limitsCmd.SilenceUsage = false
	})
	if err := limitsCmd.Flags().Set("rank", "earliest-reset-headroom"); err != nil {
		t.Fatalf("set rank: %v", err)
	}

	err := runLimits(limitsCmd, nil)
	if err == nil {
		t.Fatal("want an error when --rank is used without a provider")
	}
	if !strings.Contains(err.Error(), "--rank needs a provider") {
		t.Errorf("error = %q, want it to ask for a provider", err)
	}
	for _, p := range []string{"claude", "codex"} {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error = %q, want it to name %q", err, p)
		}
	}
}

// TestAnyProfilePublishesScopedWindow covers both response shapes: the
// current per-model map and the legacy single premium window.
func TestAnyProfilePublishesScopedWindow(t *testing.T) {
	tests := []struct {
		name string
		rows []usage.ProfileUsage
		want bool
	}{
		{name: "no rows", rows: nil, want: false},
		{
			name: "a row with no usage",
			rows: []usage.ProfileUsage{{Provider: "codex", ProfileName: "a"}},
			want: false,
		},
		{
			name: "general windows only",
			rows: []usage.ProfileUsage{rankRow("a", 10, time.Hour)},
			want: false,
		},
		{
			name: "the current per-model map",
			rows: []usage.ProfileUsage{{Provider: "claude", ProfileName: "a", Usage: &usage.UsageInfo{
				ModelWindows: map[string]*usage.UsageWindow{"Fable": {UsedPercent: 10, Label: "Fable"}},
			}}},
			want: true,
		},
		{
			name: "the legacy premium window",
			rows: []usage.ProfileUsage{{Provider: "claude", ProfileName: "a", Usage: &usage.UsageInfo{
				TertiaryWindow: &usage.UsageWindow{UsedPercent: 10},
			}}},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyProfilePublishesScopedWindow(tc.rows); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
