package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
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
	if strings.Contains(script, "curl -fsS http://100.100.118.85:7890") {
		t.Error("coordinator API is loopback-only; the script must not curl it remotely")
	}
	if !strings.Contains(script, "caam auth-coordinator status") {
		t.Error("expected authenticated on-host status check in script")
	}
	if !strings.Contains(script, "ssh -i /tmp/id_ed25519 -p 2222 ubuntu@100.100.118.85") {
		t.Error("expected ssh status command in script")
	}
	if !strings.Contains(script, "caam auth-agent --config \"$CONFIG_PATH\"") {
		t.Error("expected auth-agent start in script")
	}
}

// Test MachineOverride struct
func TestMachineOverrideFields(t *testing.T) {
	override := MachineOverride{
		PreferredIP:  "10.0.0.1",
		Username:     "admin",
		Port:         2222,
		IdentityFile: "/custom/key",
		Disabled:     false,
	}

	if override.PreferredIP != "10.0.0.1" {
		t.Errorf("expected PreferredIP=10.0.0.1, got %s", override.PreferredIP)
	}
	if override.Username != "admin" {
		t.Errorf("expected Username=admin, got %s", override.Username)
	}
	if override.Port != 2222 {
		t.Errorf("expected Port=2222, got %d", override.Port)
	}
	if override.Disabled {
		t.Error("expected Disabled=false")
	}
}

// Test DiscoveryWarning struct
func TestDiscoveryWarning(t *testing.T) {
	w := DiscoveryWarning{
		Machine: "csd",
		Code:    "NO_TAILSCALE_MATCH",
		Message: "no Tailscale peer found",
	}

	if w.Machine != "csd" {
		t.Errorf("expected Machine=csd, got %s", w.Machine)
	}
	if w.Code != "NO_TAILSCALE_MATCH" {
		t.Errorf("expected Code=NO_TAILSCALE_MATCH, got %s", w.Code)
	}
}

// Test discovery warning methods
func TestOrchestratorWarningMethods(t *testing.T) {
	orch := NewOrchestrator(DefaultOptions())

	// Initially no warnings
	if orch.HasDiscoveryWarnings() {
		t.Error("expected no warnings initially")
	}
	if len(orch.GetDiscoveryWarnings()) != 0 {
		t.Error("expected empty warnings list initially")
	}

	// Add a warning
	orch.addWarning("csd", "TEST_CODE", "test message")

	if !orch.HasDiscoveryWarnings() {
		t.Error("expected HasDiscoveryWarnings to be true after adding warning")
	}

	warnings := orch.GetDiscoveryWarnings()
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(warnings))
	}

	if warnings[0].Machine != "csd" {
		t.Errorf("expected warning machine=csd, got %s", warnings[0].Machine)
	}
	if warnings[0].Code != "TEST_CODE" {
		t.Errorf("expected warning code=TEST_CODE, got %s", warnings[0].Code)
	}
}

// Test GetTailscaleVersion
func TestGetTailscaleVersion(t *testing.T) {
	orch := NewOrchestrator(DefaultOptions())

	// Initially empty
	if orch.GetTailscaleVersion() != "" {
		t.Error("expected empty tailscale version initially")
	}

	// Set a version
	orch.tailscaleVersion = "1.56.1"

	if orch.GetTailscaleVersion() != "1.56.1" {
		t.Errorf("expected 1.56.1, got %s", orch.GetTailscaleVersion())
	}
}

// Test Options with ManualOverrides
func TestOptionsWithManualOverrides(t *testing.T) {
	opts := DefaultOptions()
	opts.ManualOverrides = map[string]MachineOverride{
		"csd": {
			PreferredIP: "192.168.1.100",
			Username:    "custom-user",
		},
		"css": {
			Disabled: true,
		},
	}

	if len(opts.ManualOverrides) != 2 {
		t.Errorf("expected 2 overrides, got %d", len(opts.ManualOverrides))
	}

	csdOverride := opts.ManualOverrides["csd"]
	if csdOverride.PreferredIP != "192.168.1.100" {
		t.Errorf("expected csd override IP=192.168.1.100, got %s", csdOverride.PreferredIP)
	}

	cssOverride := opts.ManualOverrides["css"]
	if !cssOverride.Disabled {
		t.Error("expected css override to be disabled")
	}
}

func setupWithRemotes(t *testing.T, configPath string) *Orchestrator {
	t.Helper()
	opts := DefaultOptions()
	opts.AgentConfigPath = configPath
	orch := NewOrchestrator(opts)
	orch.localMachine = &DiscoveredMachine{Name: "local", IsLocal: true}
	orch.remoteMachines = []*DiscoveredMachine{{
		Name:          "csd-host",
		WezTermDomain: "csd",
		PublicIP:      "1.2.3.4",
		TailscaleIP:   "100.64.0.5",
		Username:      "ubuntu",
		Port:          2222,
		IdentityFile:  "~/.ssh/id_ed25519",
	}}
	return orch
}

func TestCoordinatorEndpointUsesSSHTunnelToLoopback(t *testing.T) {
	orch := setupWithRemotes(t, filepath.Join(t.TempDir(), "agent.json"))
	ep := orch.coordinatorEndpoint(orch.remoteMachines[0], "tok")

	if ep.URL != "http://127.0.0.1:7890" {
		t.Fatalf("URL = %q; the coordinator only listens on remote loopback", ep.URL)
	}
	if ep.Token != "tok" {
		t.Fatalf("Token = %q", ep.Token)
	}
	want := agent.SSHTunnel{Host: "100.64.0.5", Port: 2222, User: "ubuntu", IdentityFile: "~/.ssh/id_ed25519"}
	if ep.SSH == nil || *ep.SSH != want {
		t.Fatalf("SSH = %+v, want %+v", ep.SSH, want)
	}
}

func TestGenerateLocalConfigMergesAndPreservesUserSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	existing := agent.FileConfig{
		Port:          7999,
		PollInterval:  "5s",
		Strategy:      "round_robin",
		ChromeProfile: "/Users/me/Chrome/Work",
		Accounts:      []string{"a@example.com", "b@example.com"},
		Coordinators: []*agent.CoordinatorEndpoint{
			{Name: "csd", URL: "http://1.2.3.4:7890", Token: "old"},
			{Name: "other", URL: "http://127.0.0.1:7890", Token: "keep", SSH: &agent.SSHTunnel{Host: "other.example"}},
		},
	}
	if err := agent.WriteFileConfig(path, existing); err != nil {
		t.Fatal(err)
	}

	orch := setupWithRemotes(t, path)
	if _, err := orch.generateLocalConfig([]*agent.CoordinatorEndpoint{orch.coordinatorEndpoint(orch.remoteMachines[0], "new")}); err != nil {
		t.Fatalf("generateLocalConfig: %v", err)
	}

	got, err := agent.LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 7999 || got.PollInterval != "5s" || got.Strategy != "round_robin" ||
		got.ChromeProfile != "/Users/me/Chrome/Work" || len(got.Accounts) != 2 {
		t.Fatalf("user settings lost: %+v", got)
	}
	if len(got.Coordinators) != 2 {
		t.Fatalf("coordinators = %d, want 2", len(got.Coordinators))
	}
	csd, other := got.Coordinators[0], got.Coordinators[1]
	if csd.Name != "csd" || csd.Token != "new" || csd.SSH == nil || csd.URL != "http://127.0.0.1:7890" {
		t.Fatalf("csd not replaced: %+v", csd)
	}
	if other.Name != "other" || other.Token != "keep" {
		t.Fatalf("unrelated coordinator changed: %+v", other)
	}
}

func TestGenerateLocalConfigCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caam", "agent.json")
	orch := setupWithRemotes(t, path)
	if _, err := orch.generateLocalConfig([]*agent.CoordinatorEndpoint{orch.coordinatorEndpoint(orch.remoteMachines[0], "tok")}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := agent.LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 7891 || got.Strategy != "lru" || got.Accounts == nil || len(got.Coordinators) != 1 {
		t.Fatalf("defaults = %+v", got)
	}
}

func TestGenerateLocalConfigRefusesUnreadableExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	orch := setupWithRemotes(t, path)
	if _, err := orch.generateLocalConfig([]*agent.CoordinatorEndpoint{orch.coordinatorEndpoint(orch.remoteMachines[0], "tok")}); err == nil {
		t.Fatal("a corrupt existing config must not be overwritten")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{not json" {
		t.Fatalf("existing config modified: %q", data)
	}
}

func TestRedactedAgentConfigHidesTokens(t *testing.T) {
	fc := agent.FileConfig{
		CoordinatorToken: "single-secret",
		Coordinators: []*agent.CoordinatorEndpoint{
			{Name: "a", Token: "secret-a"},
			{Name: "b", Token: dryRunTokenPlaceholder},
		},
	}
	shown := redactedAgentConfig(fc)
	data, err := json.Marshal(shown)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatalf("redacted config leaks tokens: %s", data)
	}
	if fc.Coordinators[0].Token != "secret-a" {
		t.Fatal("redaction must not modify the original config")
	}
	if shown.Coordinators[1].Token != dryRunTokenPlaceholder {
		t.Fatal("placeholder should stay visible")
	}
}

func TestGenerateTokenIsRandom(t *testing.T) {
	a, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 64 || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
}

func TestCoordinatorTokenReusesExistingUnlessRotating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	orch := setupWithRemotes(t, path)
	machine := orch.remoteMachines[0]

	existing := strings.Repeat("a", 64)
	if err := agent.WriteFileConfig(path, agent.FileConfig{Coordinators: []*agent.CoordinatorEndpoint{
		{Name: "CSD", URL: "http://127.0.0.1:7890", Token: existing},
	}}); err != nil {
		t.Fatal(err)
	}

	token, err := orch.coordinatorToken(machine)
	if err != nil || token != existing {
		t.Fatalf("token = %q, %v; re-running setup must keep the running agent's token", token, err)
	}

	orch.opts.RotateTokens = true
	rotated, err := orch.coordinatorToken(machine)
	if err != nil || rotated == existing || len(rotated) != 64 {
		t.Fatalf("rotated token = %q, %v", rotated, err)
	}

	// A short or missing token is never reused.
	orch.opts.RotateTokens = false
	if err := agent.WriteFileConfig(path, agent.FileConfig{Coordinators: []*agent.CoordinatorEndpoint{
		{Name: "csd", URL: "http://127.0.0.1:7890", Token: "weak"},
	}}); err != nil {
		t.Fatal(err)
	}
	if token, _ := orch.coordinatorToken(machine); token == "weak" {
		t.Fatal("a weak token must be replaced")
	}
}

func TestHostsToMachines(t *testing.T) {
	sshConfig := map[string]*sync.Machine{
		"build": {Name: "build", Address: "10.1.2.3", SSHUser: "deploy", Port: 2200, SSHKeyPath: "/keys/build"},
	}
	tests := []struct {
		spec                string
		name, address, user string
		port                int
		identity            string
	}{
		{spec: "box1", name: "box1", address: "box1", port: 22},
		{spec: "ubuntu@10.0.0.5:2222", name: "10.0.0.5", address: "10.0.0.5", user: "ubuntu", port: 2222},
		{spec: "[::1]:2022", name: "::1", address: "::1", port: 2022},
		{spec: "fe80::1", name: "fe80::1", address: "fe80::1", port: 22},
		// An ssh_config alias supplies HostName, User, Port and IdentityFile...
		{spec: "BUILD", name: "BUILD", address: "10.1.2.3", user: "deploy", port: 2200, identity: "/keys/build"},
		// ...unless the spec names them itself.
		{spec: "root@build:22", name: "build", address: "10.1.2.3", user: "root", port: 22, identity: "/keys/build"},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			machines, err := hostsToMachines([]string{tt.spec}, sshConfig)
			if err != nil {
				t.Fatalf("hostsToMachines(%q): %v", tt.spec, err)
			}
			if len(machines) != 1 {
				t.Fatalf("got %d machines", len(machines))
			}
			m := machines[0]
			if m.Name != tt.name || m.PublicIP != tt.address || m.Username != tt.user || m.Port != tt.port || m.IdentityFile != tt.identity {
				t.Errorf("got name=%q address=%q user=%q port=%d identity=%q", m.Name, m.PublicIP, m.Username, m.Port, m.IdentityFile)
			}
			if m.Role != RoleCoordinator || m.WezTermDomain != "" {
				t.Errorf("role=%q domain=%q; --host machines are coordinators outside WezTerm", m.Role, m.WezTermDomain)
			}
		})
	}

	for _, bad := range []string{"box:0", "box:70000", "box:ssh", "user@", "a b", "host/path"} {
		if _, err := hostsToMachines([]string{bad}, nil); err == nil {
			t.Errorf("hostsToMachines(%q) accepted an invalid spec", bad)
		}
	}
	if machines, err := hostsToMachines([]string{"", "  "}, nil); err != nil || len(machines) != 0 {
		t.Errorf("blank specs = %v, %v; want none", machines, err)
	}
}

func TestDiscoverWithHostsNeedsNoWezTerm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	sshConfig := "Host gpu\n  HostName 192.0.2.7\n  User ml\n  Port 2201\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(sshConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without hosts, a missing WezTerm config is an error that points at --host.
	orch := NewOrchestrator(Options{})
	err := orch.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("Discover without WezTerm or hosts = %v; want an error naming --host", err)
	}

	orch = NewOrchestrator(Options{Hosts: []string{"ubuntu@build1:2222", "gpu"}})
	if err := orch.Discover(context.Background()); err != nil {
		t.Fatalf("Discover with --host and no WezTerm config: %v", err)
	}
	remotes := orch.GetRemoteMachines()
	if len(remotes) != 2 {
		t.Fatalf("got %d remotes, want 2", len(remotes))
	}
	if r := remotes[0]; r.Name != "build1" || r.Username != "ubuntu" || r.Port != 2222 {
		t.Errorf("first remote = %+v", r)
	}
	if r := remotes[1]; r.Name != "gpu" || r.PublicIP != "192.0.2.7" || r.Username != "ml" || r.Port != 2201 {
		t.Errorf("ssh_config alias = %+v", r)
	}

	// An unreadable WezTerm config is only a warning when hosts are given.
	orch = NewOrchestrator(Options{Hosts: []string{"build1"}, WezTermConfig: filepath.Join(home, "missing.lua")})
	if err := orch.Discover(context.Background()); err != nil {
		t.Fatalf("Discover with a broken WezTerm config and --host: %v", err)
	}
	found := false
	for _, w := range orch.GetDiscoveryWarnings() {
		found = found || w.Code == "WEZTERM_CONFIG_ERROR"
	}
	if !found {
		t.Error("expected a WEZTERM_CONFIG_ERROR warning")
	}
}

func TestDiscoverPrefersWezTermDomainOverDuplicateHost(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "wezterm.lua")
	lua := `local config = {}
config.ssh_domains = {
  { name = 'csd', remote_address = '203.0.113.9', username = 'ubuntu' },
}
return config
`
	if err := os.WriteFile(config, []byte(lua), 0o600); err != nil {
		t.Fatal(err)
	}
	orch := NewOrchestrator(Options{WezTermConfig: config, Hosts: []string{"CSD", "extra"}})
	if err := orch.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range orch.GetRemoteMachines() {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "csd,extra" {
		t.Fatalf("remotes = %v; want the WezTerm domain plus the new host", names)
	}
	found := false
	for _, w := range orch.GetDiscoveryWarnings() {
		found = found || (w.Code == "DUPLICATE_HOST" && w.Machine == "CSD")
	}
	if !found {
		t.Errorf("expected a DUPLICATE_HOST warning, got %+v", orch.GetDiscoveryWarnings())
	}
}

func TestHostMachineKeysAgentConfigByHostName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	orch := setupWithRemotes(t, path)
	hosts, err := hostsToMachines([]string{"ubuntu@build1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := hosts[0]

	existing := strings.Repeat("b", 64)
	if err := agent.WriteFileConfig(path, agent.FileConfig{Coordinators: []*agent.CoordinatorEndpoint{
		{Name: "build1", URL: "http://127.0.0.1:7890", Token: existing},
	}}); err != nil {
		t.Fatal(err)
	}
	if token, err := orch.coordinatorToken(host); err != nil || token != existing {
		t.Fatalf("token = %q, %v; a --host machine keeps its token across setup runs", token, err)
	}
	if ep := orch.coordinatorEndpoint(host, existing); ep.Name != "build1" {
		t.Fatalf("endpoint name = %q, want build1", ep.Name)
	}
	if ep := orch.coordinatorEndpoint(orch.remoteMachines[0], "tok"); ep.Name != "csd" {
		t.Fatalf("endpoint name = %q; a WezTerm machine keeps its domain name even when Tailscale renames it", ep.Name)
	}
}

func TestBuildSetupScriptRepeatsHosts(t *testing.T) {
	orch := NewOrchestrator(DefaultOptions())
	orch.localMachine = &DiscoveredMachine{Name: "local", IsLocal: true}
	orch.remoteMachines = []*DiscoveredMachine{{Name: "box", PublicIP: "box", Port: 22}}
	script, err := orch.BuildSetupScript(ScriptOptions{UseTailscale: true, Hosts: []string{"ubuntu@box:2222", "gpu"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "caam setup distributed --yes --host ubuntu@box:2222 --host gpu\n") {
		t.Errorf("script does not repeat --host:\n%s", script)
	}
}

// TestSetupDryRunTouchesNothing: --dry-run plans every host without
// connecting to it and writes no agent config.
func TestSetupDryRunTouchesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	opts := DefaultOptions()
	opts.AgentConfigPath = path
	opts.DryRun = true
	orch := NewOrchestrator(opts)
	orch.localMachine = &DiscoveredMachine{Name: "local", IsLocal: true}
	// TEST-NET addresses: a real connection attempt would hang or fail.
	hosts, err := hostsToMachines([]string{"ubuntu@192.0.2.10", "192.0.2.11:2222"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	orch.remoteMachines = hosts

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var steps []string
	result, err := orch.Setup(ctx, func(p *SetupProgress) {
		if p.Status != "running" {
			steps = append(steps, p.Machine+": "+p.Status+" "+p.Message)
		}
	})
	if err != nil || ctx.Err() != nil {
		t.Fatalf("dry-run setup: %v (ctx %v)", err, ctx.Err())
	}
	want := []string{"192.0.2.10: success dry-run: skipped", "192.0.2.11: success dry-run: skipped"}
	if strings.Join(steps, "|") != strings.Join(want, "|") {
		t.Fatalf("progress = %q, want %q", steps, want)
	}
	if len(result.DeployResults) != 0 || len(result.Errors) != 0 {
		t.Fatalf("dry run deployed: %+v", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote the agent config (stat: %v)", err)
	}
}
