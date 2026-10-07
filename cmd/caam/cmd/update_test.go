package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/deploy"
	"github.com/spf13/cobra"
)

// stubCoordinatorUpgrades replaces the per-host upgrade with outcomes keyed
// by host and records the options each host received.
func stubCoordinatorUpgrades(t *testing.T, outcomes map[string]deploy.UpgradeResult) map[string]deploy.UpgradeOptions {
	t.Helper()
	seen := map[string]deploy.UpgradeOptions{}
	orig := upgradeCoordinatorHost
	upgradeCoordinatorHost = func(ctx context.Context, ep *agent.CoordinatorEndpoint, opts deploy.UpgradeOptions, logger *slog.Logger) deploy.UpgradeResult {
		seen[ep.SSH.Host] = opts
		res := outcomes[ep.SSH.Host]
		res.Machine = ep.Name
		return res
	}
	t.Cleanup(func() { upgradeCoordinatorHost = orig })
	return seen
}

func writeRemoteUpdateConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	err := agent.WriteFileConfig(path, agent.FileConfig{Coordinators: []*agent.CoordinatorEndpoint{
		{Name: "alpha", URL: "http://127.0.0.1:7890", SSH: &agent.SSHTunnel{Host: "alpha.example"}},
		{Name: "beta", URL: "http://127.0.0.1:7890", SSH: &agent.SSHTunnel{Host: "beta.example"}},
		{Name: "tailnet", URL: "http://100.64.0.9:7890"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemoteUpdateReportsEachCoordinator(t *testing.T) {
	seen := stubCoordinatorUpgrades(t, map[string]deploy.UpgradeResult{
		"alpha.example": {Action: deploy.UpgradeUpgraded, FromVersion: "v1.0.0", ToVersion: "v1.5.0", Verified: true},
		"beta.example":  {Action: deploy.UpgradeRolledBack, FromVersion: "v1.0.0", ToVersion: "v1.5.0", Verified: true, Error: "coordinator verification failed"},
	})
	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)

	err := runRemoteUpdate(c, writeRemoteUpdateConfig(t), deploy.UpgradeOptions{Force: true}, false)
	if err == nil || !strings.Contains(err.Error(), "1 of 3 coordinators were not upgraded") {
		t.Fatalf("err = %v, want the rolled-back host to fail the command", err)
	}
	text := out.String()
	for _, want := range []string{
		"✓ alpha: v1.0.0 -> v1.5.0",
		"↺ beta: rolled back to v1.0.0: coordinator verification failed",
		"- tailnet: skipped: no ssh block",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if !seen["alpha.example"].Force || len(seen) != 2 {
		t.Errorf("hosts upgraded with %+v, want both ssh hosts with Force", seen)
	}
}

func TestRemoteUpdateJSONDryRun(t *testing.T) {
	seen := stubCoordinatorUpgrades(t, map[string]deploy.UpgradeResult{
		"alpha.example": {Action: deploy.UpgradeWouldApply, FromVersion: "v1.0.0", ToVersion: "v1.5.0"},
		"beta.example":  {Action: deploy.UpgradeUpToDate, FromVersion: "v1.5.0", ToVersion: "v1.5.0", Verified: true},
	})
	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)

	if err := runRemoteUpdate(c, writeRemoteUpdateConfig(t), deploy.UpgradeOptions{DryRun: true}, true); err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	var got RemoteUpdateOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if !got.DryRun || len(got.Results) != 3 || got.Results[0].Action != deploy.UpgradeWouldApply || got.Results[2].Action != upgradeSkipped {
		t.Fatalf("output = %+v", got)
	}
	if !seen["beta.example"].DryRun {
		t.Error("dry run not passed to the hosts")
	}
}

func TestRemoteUpdateWithoutAgentConfig(t *testing.T) {
	err := runRemoteUpdate(&cobra.Command{}, filepath.Join(t.TempDir(), "missing.json"), deploy.UpgradeOptions{}, false)
	if err == nil || !strings.Contains(err.Error(), "caam setup distributed") {
		t.Fatalf("err = %v, want a pointer to setup", err)
	}
}
