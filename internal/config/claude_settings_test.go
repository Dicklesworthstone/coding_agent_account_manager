package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

func TestClaudeSettingsSurviveUnrelatedConfigSaves(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	original := []byte(`{"claude_settings":{"mode":"shared","profile_keys":["mcpServers","hooks"],"shared_env_keys":["EDITOR"]}}`)
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	want, err := claudesettings.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddAlias("claude", "team", "work")
	cfg.SetDefault("claude", "team")
	cfg.CreateWorkspace("work", map[string]string{"claude": "team"})
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := claudesettings.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("save changed lifecycle policy: got %+v, want %+v", got, want)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClaudeSettings.Mode = "per-profile"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err = claudesettings.LoadPolicy()
	if err != nil || got.Mode != "per-profile" {
		t.Fatalf("activation policy did not see config update: %+v, %v", got, err)
	}
}

func TestClaudeSettingsInvalidConfigDoesNotOverwriteDisk(t *testing.T) {
	for _, policy := range []claudesettings.Policy{
		{Mode: "sharedd"},
		{ProfileKeys: []string{"env"}},
		{SharedEnvKeys: []string{"ANTHROPIC_API_KEY"}},
	} {
		t.Run(policy.Mode+"/"+stringPolicyKey(policy), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			cfg := DefaultConfig()
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(ConfigPath())
			if err != nil {
				t.Fatal(err)
			}
			cfg.ClaudeSettings = policy
			if err := cfg.Save(); err == nil {
				t.Fatal("invalid policy was saved")
			}
			after, err := os.ReadFile(ConfigPath())
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected save changed config: %v", err)
			}
		})
	}
}

func stringPolicyKey(p claudesettings.Policy) string {
	if len(p.ProfileKeys) > 0 {
		return p.ProfileKeys[0]
	}
	if len(p.SharedEnvKeys) > 0 {
		return p.SharedEnvKeys[0]
	}
	return "mode"
}

func TestClaudeSettingsLoadRejectsInvalidPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte(`{"claude_settings":{"mode":"typo"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("config.Load accepted an invalid Claude settings policy")
	}
	if _, err := claudesettings.LoadPolicy(); err == nil {
		t.Fatal("activation accepted an invalid Claude settings policy")
	}
}
