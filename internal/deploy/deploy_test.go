package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

func TestServicePath(t *testing.T) {
	got := servicePath("/home/u/.local/bin:/usr/bin:relative::/opt/wez/bin:/usr/bin", "/home/u/.local/bin/caam")
	want := "/home/u/.local/bin:/usr/bin:/opt/wez/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/sbin:/bin"
	if got != want {
		t.Fatalf("servicePath = %q\nwant         %q", got, want)
	}
	if got := servicePath("", "/opt/caam/caam"); !strings.HasPrefix(got, "/opt/caam:/usr/local/sbin:") {
		t.Fatalf("without a login PATH = %q, want the binary dir then system dirs", got)
	}
}

func TestGenerateSystemdUnitSetsQuotedPath(t *testing.T) {
	unit, err := GenerateSystemdUnit(SystemdUnitConfig{
		Type:      "Auth Recovery Coordinator",
		ExecStart: "/usr/bin/caam auth-coordinator",
		Path:      `/home/u/my tools/bin:/opt/x%y:/usr/bin`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, "\nEnvironment=\"PATH=/home/u/my tools/bin:/opt/x%%y:/usr/bin\"\n") {
		t.Fatalf("unit lacks a quoted, specifier-escaped PATH:\n%s", unit)
	}

	unit, err = GenerateSystemdUnit(SystemdUnitConfig{Type: "x", ExecStart: "/usr/bin/caam"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unit, "PATH=") || !strings.Contains(unit, "Environment=HOME=%h\n\n[Install]") {
		t.Fatalf("unit without Path changed shape:\n%s", unit)
	}
}

// runLocally runs a remote command string the way sshd does: through the
// user's shell with -c.
func runLocally(t *testing.T, command string, env ...string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return string(out)
}

func TestLoginPathCommandReadsProfilePathPastNoise(t *testing.T) {
	home := t.TempDir()
	profile := "echo 'Welcome to the build box'\nPATH=\"$HOME/tools/bin:$PATH\"; export PATH\necho 'PATH=/decoy'\n"
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runLocally(t, loginPathCommand, "HOME="+home, "SHELL=/bin/sh", "PATH=/usr/bin:/bin")
	got := parseMarkedPath(out)
	if !strings.HasPrefix(got, home+"/tools/bin:") || !strings.Contains(got, "/usr/bin") {
		t.Fatalf("parseMarkedPath = %q from output:\n%s", got, out)
	}
	if parseMarkedPath("no markers here") != "" {
		t.Fatal("output without markers must give no PATH")
	}
}

func TestServiceEnvironmentCommandFindsMultiplexerOnServicePath(t *testing.T) {
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "tmux"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The caller's PATH lacks tools; the command must use the service PATH.
	out := runLocally(t, serviceEnvironmentCommand(tools+":/usr/bin:/bin"), "PATH=/usr/bin:/bin")
	if !strings.Contains(out, "found=tmux\n") {
		t.Fatalf("tmux on the service PATH not found:\n%s", out)
	}
	if w := serviceEnvironmentWarnings(out, "auto"); len(w) != 0 {
		t.Fatalf("warnings with tmux available = %q", w)
	}
	if w := serviceEnvironmentWarnings(out, "tmux"); len(w) != 0 {
		t.Fatalf("warnings for the tmux backend = %q", w)
	}
}

func TestServiceEnvironmentWarnings(t *testing.T) {
	tests := []struct {
		name, output, backend string
		want                  []string
	}{
		{"all good", "found=wezterm\nlinger=yes\nuser=bob\n", "auto", nil},
		{"no multiplexer", "linger=yes\nuser=bob\n", "auto", []string{"neither wezterm nor tmux"}},
		{"configured backend missing", "found=tmux\nlinger=yes\n", "wezterm", []string{"wezterm is not on the coordinator's PATH"}},
		{"no linger", "found=tmux\nlinger=no\nuser=bob\n", "auto", []string{"sudo loginctl enable-linger bob"}},
		{"no loginctl", "found=tmux\nlinger=\nuser=bob\n", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serviceEnvironmentWarnings(tt.output, tt.backend)
			if len(got) != len(tt.want) {
				t.Fatalf("warnings = %q, want %d matching %q", got, len(tt.want), tt.want)
			}
			for i, w := range tt.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("warning %d = %q, want it to mention %q", i, got[i], w)
				}
			}
		})
	}
}

func TestSHA256CommandMatchesLocalDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "it's caam") // quote and space survive
	data := []byte("caam binary bytes")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := parseSHA256Output(runLocally(t, sha256Command(path), "PATH=/usr/bin:/bin"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("remote digest = %s, want %s", got, want)
	}
}

func TestParseSHA256OutputRejectsGarbage(t *testing.T) {
	for _, out := range []string{"", "sha256sum: not found", "abc123  file", strings.Repeat("z", 64) + "  f"} {
		if _, err := parseSHA256Output(out); err == nil {
			t.Errorf("parseSHA256Output(%q) accepted garbage", out)
		}
	}
	upper := strings.Repeat("AB", 32) + "  /tmp/f\n"
	if got, err := parseSHA256Output(upper); err != nil || got != strings.Repeat("ab", 32) {
		t.Fatalf("parseSHA256Output(shasum style) = %q, %v", got, err)
	}
}
