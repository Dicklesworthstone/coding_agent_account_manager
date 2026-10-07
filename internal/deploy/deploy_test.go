package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
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

func TestCoordinatorExecStartUsesSystemdHomeSpecifier(t *testing.T) {
	got := CoordinatorExecStart("/home/u/.local/bin/caam")
	want := "/home/u/.local/bin/caam auth-coordinator --config %h/.config/caam/coordinator.json"
	if got != want {
		t.Fatalf("CoordinatorExecStart = %q, want %q", got, want)
	}
	if strings.Contains(got, "~") {
		t.Fatalf("ExecStart must not rely on tilde expansion: %q", got)
	}

	unit, err := GenerateSystemdUnit(SystemdUnitConfig{Type: "Auth Recovery Coordinator", ExecStart: got})
	if err != nil {
		t.Fatalf("GenerateSystemdUnit: %v", err)
	}
	if !strings.Contains(unit, "ExecStart="+want+"\n") {
		t.Fatalf("unit missing ExecStart line:\n%s", unit)
	}
}

func TestSystemdQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/usr/local/bin/caam", "/usr/local/bin/caam"},
		{"/home/a b/bin/caam", `"/home/a b/bin/caam"`},
		{"/opt/100%/caam", "/opt/100%%/caam"},
		{`/opt/q"x/caam`, `"/opt/q\"x/caam"`},
		{"/opt/$x/caam", `"/opt/$$x/caam"`},
	}
	for _, tt := range tests {
		if got := systemdQuote(tt.in); got != tt.want {
			t.Errorf("systemdQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDefaultCoordinatorConfigStaysOnLoopback(t *testing.T) {
	config := DefaultCoordinatorConfig()
	if config.Bind != "127.0.0.1" {
		t.Fatalf("Bind = %q, want loopback", config.Bind)
	}
	if _, err := config.Apply(coordinator.DefaultConfig()); err != nil {
		t.Fatalf("default deploy config must load in the coordinator: %v", err)
	}

	// The deployer writes and the coordinator reads the same JSON keys.
	config.AuthToken = "secret"
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip coordinator.FileConfig
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip != config {
		t.Fatalf("round trip = %+v, want %+v", roundTrip, config)
	}
}

func TestNormalizeArch(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64", "arm64": "arm64", "riscv64": "riscv64"} {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbeCoordinatorStatus(t *testing.T) {
	var sawToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken = r.Header.Get("Authorization")
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"running":true}`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}}

	if err := probeCoordinatorStatus(context.Background(), client, "good"); err != nil {
		t.Fatalf("probe with correct token: %v", err)
	}
	if sawToken != "Bearer good" {
		t.Fatalf("Authorization = %q", sawToken)
	}
	if err := probeCoordinatorStatus(context.Background(), client, "bad"); !errors.Is(err, errCoordinatorUnauthorized) {
		t.Fatalf("probe with wrong token = %v, want errCoordinatorUnauthorized", err)
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
