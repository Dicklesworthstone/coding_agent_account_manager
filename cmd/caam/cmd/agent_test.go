package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/spf13/cobra"
)

func TestLoadAgentConfigMulti(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "agent.json")

	data := []byte(`{
  "port": 4567,
  "poll_interval": "3s",
  "strategy": "lru",
  "chrome_profile": "/tmp/profile",
  "accounts": ["a@example.com", "b@example.com"],
  "coordinators": [
    {"name": "csd", "url": "http://100.64.0.1:7890", "display_name": "CSD", "token": "abc123"}
  ]
}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	useMulti, _, multiCfg, err := loadAgentConfig(path)
	if err != nil {
		t.Fatalf("loadAgentConfig error: %v", err)
	}
	if !useMulti {
		t.Fatal("expected multi-agent config")
	}
	if multiCfg.Port != 4567 {
		t.Fatalf("Port = %d, want 4567", multiCfg.Port)
	}
	if multiCfg.PollInterval != 3*time.Second {
		t.Fatalf("PollInterval = %v, want 3s", multiCfg.PollInterval)
	}
	if multiCfg.AccountStrategy != agent.StrategyLRU {
		t.Fatalf("AccountStrategy = %s, want %s", multiCfg.AccountStrategy, agent.StrategyLRU)
	}
	if multiCfg.ChromeUserDataDir != "/tmp/profile" {
		t.Fatalf("ChromeUserDataDir = %q, want %q", multiCfg.ChromeUserDataDir, "/tmp/profile")
	}
	if len(multiCfg.Coordinators) != 1 || multiCfg.Coordinators[0].URL == "" {
		t.Fatalf("expected one coordinator, got %+v", multiCfg.Coordinators)
	}
	if multiCfg.Coordinators[0].Token != "abc123" {
		t.Fatalf("Coordinator token = %q, want %q", multiCfg.Coordinators[0].Token, "abc123")
	}
}

func TestLoadAgentConfigSingle(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "agent.json")

	data := []byte(`{
  "port": 9001,
  "poll_interval": "4s",
  "strategy": "round_robin",
  "chrome_profile": "/tmp/profile",
  "accounts": ["a@example.com", "b@example.com"],
  "coordinator_url": "http://localhost:7890",
  "coordinator_token": "shhh"
}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	useMulti, cfg, _, err := loadAgentConfig(path)
	if err != nil {
		t.Fatalf("loadAgentConfig error: %v", err)
	}
	if useMulti {
		t.Fatal("expected single-agent config")
	}
	if cfg.Port != 9001 {
		t.Fatalf("Port = %d, want 9001", cfg.Port)
	}
	if cfg.PollInterval != 4*time.Second {
		t.Fatalf("PollInterval = %v, want 4s", cfg.PollInterval)
	}
	if cfg.AccountStrategy != agent.StrategyRoundRobin {
		t.Fatalf("AccountStrategy = %s, want %s", cfg.AccountStrategy, agent.StrategyRoundRobin)
	}
	if cfg.ChromeUserDataDir != "/tmp/profile" {
		t.Fatalf("ChromeUserDataDir = %q, want %q", cfg.ChromeUserDataDir, "/tmp/profile")
	}
	if cfg.CoordinatorURL != "http://localhost:7890" {
		t.Fatalf("CoordinatorURL = %q, want %q", cfg.CoordinatorURL, "http://localhost:7890")
	}
	if cfg.CoordinatorToken != "shhh" {
		t.Fatalf("CoordinatorToken = %q, want %q", cfg.CoordinatorToken, "shhh")
	}
}

func newSignInCmd(t *testing.T) *cobra.Command {
	t.Helper()
	// Flags are package-level and shared; start clean and leave clean.
	reset := func() {
		for _, name := range []string{"config", "chrome-profile"} {
			f := agentSignInCmd.Flags().Lookup(name)
			f.Value.Set(f.DefValue)
			f.Changed = false
		}
	}
	reset()
	t.Cleanup(reset)
	c := &cobra.Command{}
	c.Flags().AddFlagSet(agentSignInCmd.Flags())
	return c
}

func TestSignInProfileDirPrecedence(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(cfgPath, []byte(`{"chrome_profile": "/from/config", "coordinators": []}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// No config anywhere: the agent's default profile ("" resolves to it).
	if dir, err := signInProfileDir(newSignInCmd(t)); err != nil || dir != "" {
		t.Fatalf("no config: dir=%q err=%v, want default", dir, err)
	}

	c := newSignInCmd(t)
	c.Flags().Set("config", cfgPath)
	if dir, err := signInProfileDir(c); err != nil || dir != "/from/config" {
		t.Fatalf("config: dir=%q err=%v, want the config's chrome_profile", dir, err)
	}

	c = newSignInCmd(t)
	c.Flags().Set("config", cfgPath)
	c.Flags().Set("chrome-profile", "/from/flag")
	if dir, err := signInProfileDir(c); err != nil || dir != "/from/flag" {
		t.Fatalf("flag: dir=%q err=%v, want --chrome-profile to win", dir, err)
	}

	c = newSignInCmd(t)
	c.Flags().Set("config", filepath.Join(t.TempDir(), "missing.json"))
	if _, err := signInProfileDir(c); err == nil {
		t.Fatal("an explicit --config that does not exist must be an error")
	}
}

func TestSignInProfileDirReadsDefaultConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := agent.DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"chrome_profile": "~/agent-chrome"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if dir, err := signInProfileDir(newSignInCmd(t)); err != nil || dir != "~/agent-chrome" {
		t.Fatalf("dir=%q err=%v, want the setup config's chrome_profile", dir, err)
	}
}

func TestAgentAccountsAddListRemove(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := agent.WriteFileConfig(path, agent.FileConfig{
		Strategy:     "lru",
		Accounts:     []string{"a@example.com"},
		Coordinators: []*agent.CoordinatorEndpoint{{Name: "csd", URL: "http://127.0.0.1:7890", Token: "tok"}},
	}); err != nil {
		t.Fatal(err)
	}
	saved := agentAccountsConfig
	agentAccountsConfig = path
	t.Cleanup(func() { agentAccountsConfig = saved })

	run := func(c *cobra.Command, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetContext(context.Background())
		t.Cleanup(func() { c.SetOut(nil); c.SetErr(nil) })
		err := c.RunE(c, args)
		return out.String(), err
	}
	accounts := func() []string {
		t.Helper()
		fc, err := agent.LoadFileConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return fc.Accounts
	}

	// Duplicates (in any case) are not added twice; the rest keep their order.
	if _, err := run(agentAccountsAddCmd, "b@example.com", "A@example.com", "c@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(accounts(), ","); got != "a@example.com,b@example.com,c@example.com" {
		t.Fatalf("accounts = %s", got)
	}
	if _, err := run(agentAccountsAddCmd, "not-an-email"); err == nil {
		t.Fatal("an invalid address was accepted")
	}
	// The rest of the config survives the edit.
	if fc, _ := agent.LoadFileConfig(path); len(fc.Coordinators) != 1 || fc.Coordinators[0].Token != "tok" || fc.Strategy != "lru" {
		t.Fatalf("config lost other settings: %+v", fc)
	}

	// The list shows holds from the agent's usage file.
	usage := `[{"email":"b@example.com","last_used":"2026-10-07T10:00:00Z","use_count":1,"last_result":"success","limited_until":"` +
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}]`
	if err := os.MkdirAll(filepath.Dir(agent.UsagePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agent.UsagePath(), []byte(usage), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(agentAccountsCmd)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. a@example.com", "2. b@example.com", "at its limit until", "3. c@example.com"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output lacks %q:\n%s", want, out)
		}
	}

	if _, err := run(agentAccountsRemoveCmd, "B@EXAMPLE.COM"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(accounts(), ","); got != "a@example.com,c@example.com" {
		t.Fatalf("accounts after remove = %s", got)
	}
	if out, _ := run(agentAccountsRemoveCmd, "nobody@example.com"); !strings.Contains(out, "Nothing to change") {
		t.Fatalf("removing an unknown account: %q", out)
	}
}
