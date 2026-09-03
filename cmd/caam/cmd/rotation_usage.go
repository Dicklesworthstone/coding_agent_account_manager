package cmd

import (
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// toRotationUsageInfo converts provider usage data into the rotation
// selector's format, including the earliest known quota reset time so the
// drain policy (issue #81) can rank profiles by reset horizon, and the worst
// model-scoped quota so an account out of, say, weekly Fable capacity is not
// offered for Fable work (issue #97).
//
// model is the model the work will run on, or "" when it is unknown — in which
// case every scoped quota counts, since an omitted one must never read as
// spare capacity.
func toRotationUsageInfo(name string, u *usage.UsageInfo, model string) *rotation.UsageInfo {
	if u == nil {
		return nil
	}

	info := &rotation.UsageInfo{
		ProfileName: name,
		AvailScore:  u.AvailabilityScoreForModel(model),
		Error:       u.Error,
	}

	if u.PrimaryWindow != nil {
		info.PrimaryPercent = u.PrimaryWindow.UsedPercent
	}
	if u.SecondaryWindow != nil {
		info.SecondaryPercent = u.SecondaryWindow.UsedPercent
	}
	if scoped := u.ScopedLimit(model); scoped != nil {
		info.ScopedPercent = scoped.UsedPercent
		info.ScopedLabel = scoped.Label
	}
	if resetsAt := u.EarliestReset(); !resetsAt.IsZero() {
		t := resetsAt
		info.ResetsAt = &t
	}

	return info
}

// modelFromArgs pulls the model out of the arguments being passed through to
// the agent CLI, so a `caam run claude --precheck -- --model opus …` honors
// the Opus quota rather than the account's general headroom (issue #97).
//
// Only the long form is recognized: a bare -m means different things to
// different CLIs, and guessing wrong is worse than not knowing.
func modelFromArgs(args []string) string {
	for i, a := range args {
		if a == "--" {
			continue
		}
		if v, ok := strings.CutPrefix(a, "--model="); ok {
			return strings.TrimSpace(v)
		}
		if a == "--model" && i+1 < len(args) {
			next := strings.TrimSpace(args[i+1])
			if next != "" && !strings.HasPrefix(next, "-") {
				return next
			}
		}
	}
	return ""
}

// applyRotationPolicy configures the selector's policy and drain ceiling from
// the SPM config, with an optional CLI override taking precedence. The
// availability policy (the default) leaves selection behavior unchanged;
// "drain" is strictly opt-in.
func applyRotationPolicy(selector *rotation.Selector, spmCfg *config.SPMConfig, policyOverride string) {
	policy := ""
	ceiling := 0
	if spmCfg != nil {
		policy = spmCfg.Stealth.Rotation.Policy
		ceiling = spmCfg.Stealth.Rotation.DrainHeadroomCeiling
	}
	if policyOverride != "" {
		policy = policyOverride
	}
	if policy != "" {
		selector.SetPolicy(rotation.Policy(policy))
	}
	if ceiling > 0 {
		selector.SetDrainCeiling(ceiling)
	}
}
