package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
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
