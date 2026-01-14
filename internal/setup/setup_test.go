package setup

import (
	"strings"
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()

	if !opts.UseTailscale {
		t.Error("expected UseTailscale=true by default")
	}
	if opts.LocalPort != 7891 {
		t.Errorf("expected LocalPort=7891, got %d", opts.LocalPort)
	}
	if opts.RemotePort != 7890 {
		t.Errorf("expected RemotePort=7890, got %d", opts.RemotePort)
	}
	if opts.DryRun {
		t.Error("expected DryRun=false by default")
	}
}

func TestContains(t *testing.T) {
	slice := []string{"csd", "css", "trj"}

	tests := []struct {
		item     string
		expected bool
	}{
		{"csd", true},
		{"CSS", true}, // Case insensitive
		{"TRJ", true},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		result := contains(slice, tt.item)
		if result != tt.expected {
			t.Errorf("contains(%v, %q) = %v, want %v", slice, tt.item, result, tt.expected)
		}
	}
}

func TestDiscoveredMachineFields(t *testing.T) {
	m := &DiscoveredMachine{
		Name:          "SuperServer",
		WezTermDomain: "css",
		PublicIP:      "1.2.3.4",
		TailscaleIP:   "100.90.148.85",
		Username:      "ubuntu",
		Port:          22,
		IdentityFile:  "/home/user/.ssh/id_ed25519",
		Role:          RoleCoordinator,
		IsReachable:   true,
		IsLocal:       false,
	}

	if m.Name != "SuperServer" {
		t.Errorf("expected name SuperServer, got %s", m.Name)
	}
	if m.Role != RoleCoordinator {
		t.Errorf("expected role coordinator, got %s", m.Role)
	}
	if m.IsLocal {
		t.Error("expected IsLocal=false")
	}
	if !m.IsReachable {
		t.Error("expected IsReachable=true")
	}
}

func TestRoleConstants(t *testing.T) {
	if RoleCoordinator != "coordinator" {
		t.Errorf("expected RoleCoordinator=coordinator, got %s", RoleCoordinator)
	}
	if RoleAgent != "agent" {
		t.Errorf("expected RoleAgent=agent, got %s", RoleAgent)
	}
}

func TestOrchestratorCreation(t *testing.T) {
	opts := DefaultOptions()
	opts.DryRun = true

	orch := NewOrchestrator(opts)

	if orch == nil {
		t.Fatal("NewOrchestrator returned nil")
	}
	if orch.logger == nil {
		t.Error("expected logger to be set")
	}
	if !orch.opts.DryRun {
		t.Error("expected DryRun=true")
	}
}

func TestGetAddress(t *testing.T) {
	orch := NewOrchestrator(DefaultOptions())

	// With Tailscale enabled and available
	m := &DiscoveredMachine{
		PublicIP:    "1.2.3.4",
		TailscaleIP: "100.100.100.100",
	}

	addr := orch.getAddress(m)
	if addr != "100.100.100.100" {
		t.Errorf("expected Tailscale IP, got %s", addr)
	}

	// Without Tailscale IP
	m2 := &DiscoveredMachine{
		PublicIP:    "5.6.7.8",
		TailscaleIP: "",
	}

	addr2 := orch.getAddress(m2)
	if addr2 != "5.6.7.8" {
		t.Errorf("expected public IP, got %s", addr2)
	}

	// With Tailscale disabled
	opts := DefaultOptions()
	opts.UseTailscale = false
	orch2 := NewOrchestrator(opts)

	addr3 := orch2.getAddress(m)
	if addr3 != "1.2.3.4" {
		t.Errorf("expected public IP when Tailscale disabled, got %s", addr3)
	}
}

func TestSetupProgressFields(t *testing.T) {
	progress := &SetupProgress{
		Machine: "test-machine",
		Step:    "deploy",
		Status:  "running",
		Message: "uploading binary",
	}

	if progress.Machine != "test-machine" {
		t.Errorf("expected machine test-machine, got %s", progress.Machine)
	}
	if progress.Step != "deploy" {
		t.Errorf("expected step deploy, got %s", progress.Step)
	}
	if progress.Status != "running" {
		t.Errorf("expected status running, got %s", progress.Status)
	}
}

func TestSetupResultFields(t *testing.T) {
	result := &SetupResult{
		LocalConfigPath:   "/home/user/.config/caam/distributed-agent.json",
		CoordinatorConfig: "{}",
	}

	if result.LocalConfigPath == "" {
		t.Error("expected LocalConfigPath to be set")
	}
	if result.DeployResults != nil && len(result.DeployResults) > 0 {
		t.Error("expected empty DeployResults initially")
	}
}

func TestGetDiscoveredMachines(t *testing.T) {
	orch := NewOrchestrator(DefaultOptions())

	// Set up local and remote machines
	orch.localMachine = &DiscoveredMachine{
		Name:    "local",
		IsLocal: true,
	}
	orch.remoteMachines = []*DiscoveredMachine{
		{Name: "remote1", IsLocal: false},
		{Name: "remote2", IsLocal: false},
	}

	all := orch.GetDiscoveredMachines()
	if len(all) != 3 {
		t.Errorf("expected 3 machines, got %d", len(all))
	}

	// First should be local
	if !all[0].IsLocal {
		t.Error("expected first machine to be local")
	}

	remotes := orch.GetRemoteMachines()
	if len(remotes) != 2 {
		t.Errorf("expected 2 remote machines, got %d", len(remotes))
	}

	local := orch.GetLocalMachine()
	if local == nil {
		t.Fatal("expected local machine")
	}
	if local.Name != "local" {
		t.Errorf("expected local machine name 'local', got %s", local.Name)
	}
}

func TestRemotesFilter(t *testing.T) {
	// Test that the contains function works correctly for filtering
	remotes := []string{"csd", "css"}

	// Should match
	if !contains(remotes, "csd") {
		t.Error("expected csd to be in remotes")
	}
	if !contains(remotes, "CSS") { // Case insensitive
		t.Error("expected CSS to be in remotes (case insensitive)")
	}

	// Should not match
	if contains(remotes, "trj") {
		t.Error("expected trj to not be in remotes")
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"a b", "'a b'"},
		{"a'b", "'a'\\''b'"},
		{"", "''"},
	}

	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.expected {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildSetupScript(t *testing.T) {
	orch := NewOrchestrator(DefaultOptions())
	orch.localMachine = &DiscoveredMachine{
		Name:    "local",
		IsLocal: true,
	}
	orch.remoteMachines = []*DiscoveredMachine{
		{
			Name:          "csd",
			WezTermDomain: "csd",
			PublicIP:      "1.2.3.4",
			TailscaleIP:   "100.100.118.85",
			Username:      "ubuntu",
			Port:          2222,
			IdentityFile:  "/tmp/id_ed25519",
		},
	}

	script, err := orch.BuildSetupScript(ScriptOptions{
		UseTailscale: true,
		RemotePort:   7890,
	})
	if err != nil {
		t.Fatalf("BuildSetupScript error: %v", err)
	}
	if !strings.Contains(script, "caam setup distributed --yes") {
		t.Error("expected setup command in script")
	}
	if !strings.Contains(script, "curl -fsS http://100.100.118.85:7890/status") {
		t.Error("expected tailscale status curl in script")
	}
	if !strings.Contains(script, "ssh -i /tmp/id_ed25519 -p 2222 ubuntu@100.100.118.85") {
		t.Error("expected ssh status command in script")
	}
	if !strings.Contains(script, "caam auth-agent --config \"$CONFIG_PATH\"") {
		t.Error("expected auth-agent start in script")
	}
}
