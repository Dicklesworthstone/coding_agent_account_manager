package agent

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Port != 7891 {
		t.Errorf("expected port 7891, got %d", config.Port)
	}
	if config.CoordinatorURL != "http://localhost:7890" {
		t.Errorf("unexpected coordinator URL: %s", config.CoordinatorURL)
	}
	if config.PollInterval != 2*time.Second {
		t.Errorf("unexpected poll interval: %v", config.PollInterval)
	}
	if config.Headless != false {
		t.Error("headless should default to false")
	}
	if config.AccountStrategy != StrategyLRU {
		t.Errorf("expected LRU strategy, got %v", config.AccountStrategy)
	}
}

func TestAgentSelectLRU(t *testing.T) {
	agent := &Agent{
		config: Config{
			Accounts:        []string{"a@test.com", "b@test.com", "c@test.com"},
			AccountStrategy: StrategyLRU,
		},
		accountUsage: make(map[string]*AccountUsage),
	}

	// First select should return first account (never used)
	selected := agent.selectLRU(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", selected)
	}

	// Mark a@test.com as used
	agent.accountUsage["a@test.com"] = &AccountUsage{
		Email:    "a@test.com",
		LastUsed: time.Now(),
	}

	// Next select should return b@test.com (never used)
	selected = agent.selectLRU(agent.config.Accounts)
	if selected != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", selected)
	}

	// Mark all as used with different times
	now := time.Now()
	agent.accountUsage["a@test.com"] = &AccountUsage{
		Email:    "a@test.com",
		LastUsed: now.Add(-1 * time.Hour), // oldest
	}
	agent.accountUsage["b@test.com"] = &AccountUsage{
		Email:    "b@test.com",
		LastUsed: now.Add(-30 * time.Minute),
	}
	agent.accountUsage["c@test.com"] = &AccountUsage{
		Email:    "c@test.com",
		LastUsed: now.Add(-10 * time.Minute), // most recent
	}

	// LRU should return a@test.com (oldest)
	selected = agent.selectLRU(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com (oldest), got %s", selected)
	}
}

func TestAgentSelectRoundRobin(t *testing.T) {
	agent := &Agent{
		config: Config{
			Accounts:        []string{"a@test.com", "b@test.com", "c@test.com"},
			AccountStrategy: StrategyRoundRobin,
		},
		accountUsage: make(map[string]*AccountUsage),
	}

	// First select with no usage should return first account
	selected := agent.selectRoundRobin(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", selected)
	}

	// Mark a@test.com as most recent
	agent.accountUsage["a@test.com"] = &AccountUsage{
		Email:    "a@test.com",
		LastUsed: time.Now(),
	}

	// Round robin should return b@test.com (next after a)
	selected = agent.selectRoundRobin(agent.config.Accounts)
	if selected != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", selected)
	}

	// Mark b@test.com as most recent
	agent.accountUsage["b@test.com"] = &AccountUsage{
		Email:    "b@test.com",
		LastUsed: time.Now(),
	}

	// Round robin should return c@test.com (next after b)
	selected = agent.selectRoundRobin(agent.config.Accounts)
	if selected != "c@test.com" {
		t.Errorf("expected c@test.com, got %s", selected)
	}

	// Mark c@test.com as most recent
	agent.accountUsage["c@test.com"] = &AccountUsage{
		Email:    "c@test.com",
		LastUsed: time.Now(),
	}

	// Round robin should wrap back to a@test.com
	selected = agent.selectRoundRobin(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com (wrap around), got %s", selected)
	}
}

func TestAgentSelectAccountNoAccounts(t *testing.T) {
	agent := &Agent{
		config: Config{
			Accounts:        nil, // no accounts configured
			AccountStrategy: StrategyLRU,
		},
		accountUsage: make(map[string]*AccountUsage),
	}

	// With no accounts, should return empty string
	selected := agent.selectAccount()
	if selected != "" {
		t.Errorf("expected empty string with no accounts, got %s", selected)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		limit    int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.limit)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.limit, result, tt.expected)
		}
	}
}

func TestAccountUsage(t *testing.T) {
	usage := &AccountUsage{
		Email:      "test@example.com",
		LastUsed:   time.Now(),
		UseCount:   5,
		LastResult: "success",
	}

	if usage.Email != "test@example.com" {
		t.Errorf("unexpected email: %s", usage.Email)
	}
	if usage.UseCount != 5 {
		t.Errorf("unexpected use count: %d", usage.UseCount)
	}
	if usage.LastResult != "success" {
		t.Errorf("unexpected last result: %s", usage.LastResult)
	}
}
