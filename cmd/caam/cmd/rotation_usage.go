package cmd

import (
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// toRotationUsageInfo converts provider usage data into the rotation
// selector's format, including the earliest known quota reset time so the
// drain policy (issue #81) can rank profiles by reset horizon.
func toRotationUsageInfo(name string, u *usage.UsageInfo) *rotation.UsageInfo {
	if u == nil {
		return nil
	}

	info := &rotation.UsageInfo{
		ProfileName: name,
		AvailScore:  u.AvailabilityScore(),
		Error:       u.Error,
	}

	if u.PrimaryWindow != nil {
		info.PrimaryPercent = u.PrimaryWindow.UsedPercent
	}
	if u.SecondaryWindow != nil {
		info.SecondaryPercent = u.SecondaryWindow.UsedPercent
	}
	if resetsAt := u.EarliestReset(); !resetsAt.IsZero() {
		t := resetsAt
		info.ResetsAt = &t
	}

	return info
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
