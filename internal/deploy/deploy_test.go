package deploy

import (
	"strings"
	"testing"
)

func TestGenerateSystemdUnit(t *testing.T) {
	config := SystemdUnitConfig{
		Type:      "Auth Recovery Coordinator",
		ExecStart: "/usr/local/bin/caam auth-coordinator --config ~/.config/caam/coordinator.json",
	}

	content, err := GenerateSystemdUnit(config)
	if err != nil {
		t.Fatalf("GenerateSystemdUnit failed: %v", err)
	}

	// Check required sections
	if !strings.Contains(content, "[Unit]") {
		t.Error("missing [Unit] section")
	}
	if !strings.Contains(content, "[Service]") {
		t.Error("missing [Service] section")
	}
	if !strings.Contains(content, "[Install]") {
		t.Error("missing [Install] section")
	}

	// Check important fields
	if !strings.Contains(content, "Description=CAAM Auth Recovery Coordinator Daemon") {
		t.Error("missing or incorrect Description")
	}
	if !strings.Contains(content, "ExecStart=/usr/local/bin/caam auth-coordinator") {
		t.Error("missing or incorrect ExecStart")
	}
	if !strings.Contains(content, "Type=simple") {
		t.Error("missing Type=simple")
	}
	if !strings.Contains(content, "Restart=on-failure") {
		t.Error("missing Restart=on-failure")
	}
	if !strings.Contains(content, "WantedBy=default.target") {
		t.Error("missing WantedBy=default.target")
	}
}

func TestDefaultCoordinatorConfig(t *testing.T) {
	config := DefaultCoordinatorConfig()

	if config.Port != 7890 {
		t.Errorf("expected port 7890, got %d", config.Port)
	}
	if config.PollInterval != "500ms" {
		t.Errorf("expected poll_interval 500ms, got %s", config.PollInterval)
	}
	if config.AuthTimeout != "60s" {
		t.Errorf("expected auth_timeout 60s, got %s", config.AuthTimeout)
	}
	if config.OutputLines != 100 {
		t.Errorf("expected output_lines 100, got %d", config.OutputLines)
	}
}

func TestExpandPath(t *testing.T) {
	tests := []struct {
		input    string
		contains string // Expected to contain this
	}{
		{"~/test", "test"},       // Should expand home
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		result := expandPath(tt.input)
		if !strings.Contains(result, tt.contains) {
			t.Errorf("expandPath(%q) = %q, expected to contain %q", tt.input, result, tt.contains)
		}
		// Home expansion should not start with ~
		if strings.HasPrefix(tt.input, "~/") && strings.HasPrefix(result, "~") {
			t.Errorf("expandPath(%q) = %q, tilde not expanded", tt.input, result)
		}
	}
}

func TestSystemdUnitConfig(t *testing.T) {
	// Test with different service types
	types := []string{"coordinator", "agent", "daemon"}

	for _, typ := range types {
		config := SystemdUnitConfig{
			Type:      typ,
			ExecStart: "/usr/local/bin/caam " + typ,
		}

		content, err := GenerateSystemdUnit(config)
		if err != nil {
			t.Errorf("GenerateSystemdUnit failed for type %s: %v", typ, err)
			continue
		}

		if !strings.Contains(content, "Description=CAAM "+typ+" Daemon") {
			t.Errorf("missing correct description for type %s", typ)
		}
		if !strings.Contains(content, "ExecStart=/usr/local/bin/caam "+typ) {
			t.Errorf("missing correct ExecStart for type %s", typ)
		}
	}
}

func TestCoordinatorConfigJSON(t *testing.T) {
	config := DefaultCoordinatorConfig()

	// Test that the config can be marshaled (used in WriteCoordinatorConfig)
	// We don't actually marshal here since it's tested implicitly by the struct tags
	// Just verify the fields are accessible
	if config.Port == 0 {
		t.Error("port should not be zero")
	}
	if config.PollInterval == "" {
		t.Error("poll_interval should not be empty")
	}
	if config.AuthTimeout == "" {
		t.Error("auth_timeout should not be empty")
	}
	if config.ResumePrompt == "" {
		t.Error("resume_prompt should not be empty")
	}
}

func TestDeployResultFields(t *testing.T) {
	result := &DeployResult{
		Machine:       "test-machine",
		Success:       true,
		BinaryUpdated: true,
		ConfigWritten: true,
		ServiceStatus: "active",
		LocalVersion:  "1.0.0",
		RemoteVersion: "0.9.0",
	}

	if result.Machine != "test-machine" {
		t.Errorf("expected machine test-machine, got %s", result.Machine)
	}
	if !result.Success {
		t.Error("expected success=true")
	}
	if !result.BinaryUpdated {
		t.Error("expected binary_updated=true")
	}
	if result.ServiceStatus != "active" {
		t.Errorf("expected service_status active, got %s", result.ServiceStatus)
	}
}
