package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
	caamsync "github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
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

// fakeRemote stands in for a coordinator host: an SSH server that runs
// commands with /bin/sh in a private home directory, serves SFTP, and
// forwards direct-tcpip. Stubs for systemctl, loginctl, sudo, uname and curl
// come first on its PATH. The stub systemctl "starts" the coordinator by
// recording the --version of the binary the unit runs, and the fake
// coordinator API is healthy unless that version contains "broken".
type fakeRemote struct {
	home    string
	stubs   string
	addr    string
	keyPath string
	apiPort int
}

const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$HOME/systemctl.log"
case "$*" in
*restart*)
	bin=$(sed -n 's/^ExecStart=\([^ ]*\).*/\1/p' "$HOME/.config/systemd/user/caam-coordinator.service")
	"$bin" --version > "$HOME/running-version"
	;;
esac
exit 0
`

const fakeCurl = `#!/bin/sh
cat <<'EOS'
v=latest
for a in "$@"; do case "$a" in --version=*) v="${a#--version=}";; esac; done
mkdir -p "$INSTALL_DIR"
printf '#!/bin/sh\necho "caam %s (release) built on today"\n' "$v" > "$INSTALL_DIR/caam"
chmod 755 "$INSTALL_DIR/caam"
echo "$v" >> "$HOME/installs.log"
EOS
`

func startFakeRemote(t *testing.T, remoteOS string) *fakeRemote {
	t.Helper()
	r := &fakeRemote{home: t.TempDir(), stubs: t.TempDir()}
	stubs := map[string]string{
		"systemctl": fakeSystemctl,
		"loginctl":  "#!/bin/sh\necho yes\n",
		"sudo":      "#!/bin/sh\nexit 1\n",
		"curl":      fakeCurl,
	}
	if remoteOS != "" {
		stubs["uname"] = "#!/bin/sh\nif [ \"$1\" = -s ]; then echo " + remoteOS + "; exit 0; fi\n" +
			"for u in /usr/bin/uname /bin/uname; do [ -x \"$u\" ] && exec \"$u\" \"$@\"; done\n"
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(r.stubs, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		running, _ := os.ReadFile(filepath.Join(r.home, "running-version"))
		if len(running) == 0 || strings.Contains(string(running), "broken") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"running":true}`))
	}))
	t.Cleanup(api.Close)
	_, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	r.apiPort, _ = strconv.Atoi(port)

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	r.keyPath = filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(r.keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "")

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), sshPub.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	r.addr = listener.Addr().String()
	go func() {
		for {
			nc, err := listener.Accept()
			if err != nil {
				return
			}
			go r.serve(nc, config)
		}
	}()
	return r
}

func (r *fakeRemote) serve(nc net.Conn, config *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, config)
	if err != nil {
		nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		switch newCh.ChannelType() {
		case "session":
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			go r.session(ch, chReqs)
		case "direct-tcpip":
			var dest struct {
				Host     string
				Port     uint32
				OrigHost string
				OrigPort uint32
			}
			if err := ssh.Unmarshal(newCh.ExtraData(), &dest); err != nil {
				newCh.Reject(ssh.ConnectionFailed, "bad payload")
				continue
			}
			target, err := net.Dial("tcp", net.JoinHostPort(dest.Host, strconv.Itoa(int(dest.Port))))
			if err != nil {
				newCh.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				target.Close()
				continue
			}
			go ssh.DiscardRequests(chReqs)
			go func() {
				defer ch.Close()
				defer target.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(ch, target); done <- struct{}{} }()
				go func() { io.Copy(target, ch); done <- struct{}{} }()
				<-done
			}()
		default:
			newCh.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

func (r *fakeRemote) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				return
			}
			req.Reply(true, nil)
			cmd := exec.Command("/bin/sh", "-c", payload.Command)
			cmd.Env = []string{"HOME=" + r.home, "PATH=" + r.stubs + ":/usr/bin:/bin", "SHELL=/bin/sh", "USER=tester"}
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			status := 0
			if err := cmd.Run(); err != nil {
				status = 1
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					status = exitErr.ExitCode()
				}
			}
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
			return
		case "subsystem":
			var payload struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
				req.Reply(false, nil)
				return
			}
			req.Reply(true, nil)
			server, err := sftp.NewServer(ch)
			if err != nil {
				return
			}
			server.Serve()
			return
		default:
			req.Reply(false, nil)
		}
	}
}

func (r *fakeRemote) path(rel string) string { return filepath.Join(r.home, rel) }

func (r *fakeRemote) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(r.path(rel))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// writeExecutable writes a stand-in caam that reports version.
func writeExecutable(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"caam %s (test) built on today\"\n", version)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// installCoordinator puts a running coordinator of version on the remote.
func (r *fakeRemote) installCoordinator(t *testing.T, version string) {
	t.Helper()
	bin := r.path(".local/bin/caam")
	writeExecutable(t, bin, version)
	cfg := fmt.Sprintf(`{"bind":"127.0.0.1","port":%d,"auth_token":"tok"}`, r.apiPort)
	unit := "[Service]\nExecStart=" + bin + " auth-coordinator --config %h/.config/caam/coordinator.json\n"
	for rel, content := range map[string]string{
		".config/caam/coordinator.json":                 cfg,
		".config/systemd/user/caam-coordinator.service": unit,
		"running-version":                               "caam " + version,
	} {
		if err := os.MkdirAll(filepath.Dir(r.path(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.path(rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// deployer connects to the fake remote with a local caam of localVersion.
func (r *fakeRemote) deployer(t *testing.T, localVersion string) *Deployer {
	t.Helper()
	host, port, _ := net.SplitHostPort(r.addr)
	portNum, _ := strconv.Atoi(port)
	d := NewDeployer(&caamsync.Machine{Name: "fake", Address: host, Port: portNum, SSHUser: "tester", SSHKeyPath: r.keyPath}, nil)
	d.localBinary = filepath.Join(t.TempDir(), "caam")
	writeExecutable(t, d.localBinary, localVersion)
	d.verifyTimeout = 2 * time.Second
	if err := d.Connect(); err != nil {
		t.Fatalf("connect to fake remote: %v", err)
	}
	t.Cleanup(func() { d.Disconnect() })
	return d
}

func TestUpgradeCoordinatorReplacesVerifiesAndKeepsLastKnownGood(t *testing.T) {
	r := startFakeRemote(t, "")
	r.installCoordinator(t, "v1.0.0")
	d := r.deployer(t, "v1.5.0")

	res := d.UpgradeCoordinator(context.Background(), UpgradeOptions{})
	if res.Action != UpgradeUpgraded || !res.Verified || res.Error != "" {
		t.Fatalf("result = %+v", res)
	}
	if res.FromVersion != "v1.0.0" || res.ToVersion != "v1.5.0" {
		t.Fatalf("versions %s -> %s", res.FromVersion, res.ToVersion)
	}
	if got := r.read(t, "running-version"); !strings.Contains(got, "v1.5.0") {
		t.Fatalf("running coordinator = %q, want v1.5.0", got)
	}
	unit := r.read(t, ".config/systemd/user/caam-coordinator.service")
	if bin := execStartBinary(unit); bin != r.path("bin/caam") {
		t.Fatalf("unit runs %q, want the uploaded %q:\n%s", bin, r.path("bin/caam"), unit)
	}
	if !strings.Contains(r.read(t, ".local/bin/caam"+prevBinarySuffix), "v1.0.0") {
		t.Fatal("previous binary not kept as last-known-good")
	}
	if cfg := r.read(t, ".config/caam/coordinator.json"); !strings.Contains(cfg, `"auth_token": "tok"`) {
		t.Fatalf("deployed config not preserved:\n%s", cfg)
	}
}

func TestUpgradeCoordinatorRollsBackWhenNewVersionFailsVerification(t *testing.T) {
	r := startFakeRemote(t, "")
	r.installCoordinator(t, "v1.0.0")
	d := r.deployer(t, "v1.6.0-broken")

	res := d.UpgradeCoordinator(context.Background(), UpgradeOptions{})
	if res.Action != UpgradeRolledBack || !res.Verified {
		t.Fatalf("result = %+v, want a verified rollback", res)
	}
	if !strings.Contains(res.Error, "verification failed") {
		t.Fatalf("error = %q, want the failed verification", res.Error)
	}
	if got := r.read(t, "running-version"); !strings.Contains(got, "v1.0.0") {
		t.Fatalf("running coordinator after rollback = %q, want v1.0.0", got)
	}
	unit := r.read(t, ".config/systemd/user/caam-coordinator.service")
	if bin := execStartBinary(unit); bin != r.path(".local/bin/caam") {
		t.Fatalf("restored unit runs %q:\n%s", bin, unit)
	}
	if got := r.read(t, ".local/bin/caam"); !strings.Contains(got, "v1.0.0") {
		t.Fatalf("restored binary = %q", got)
	}
}

func TestUpgradeCoordinatorDryRunAndUpToDateChangeNothing(t *testing.T) {
	r := startFakeRemote(t, "")
	r.installCoordinator(t, "v1.0.0")

	res := r.deployer(t, "v1.5.0").UpgradeCoordinator(context.Background(), UpgradeOptions{DryRun: true})
	if res.Action != UpgradeWouldApply || res.FromVersion != "v1.0.0" || res.ToVersion != "v1.5.0" {
		t.Fatalf("dry run = %+v", res)
	}

	res = r.deployer(t, "v1.0.0").UpgradeCoordinator(context.Background(), UpgradeOptions{})
	if res.Action != UpgradeUpToDate || !res.Verified {
		t.Fatalf("same version = %+v", res)
	}
	if log := r.read(t, "systemctl.log"); log != "" {
		t.Fatalf("systemctl was called without an upgrade:\n%s", log)
	}
	if _, err := os.Stat(r.path("bin/caam")); err == nil {
		t.Fatal("a binary was uploaded without an upgrade")
	}
}

func TestUpgradeCoordinatorOnOtherPlatformInstallsMatchingRelease(t *testing.T) {
	r := startFakeRemote(t, "FakeOS")
	r.installCoordinator(t, "v1.0.0")
	d := r.deployer(t, "v1.5.0")

	res := d.UpgradeCoordinator(context.Background(), UpgradeOptions{})
	if res.Action != UpgradeUpgraded || !res.Verified {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.TrimSpace(r.read(t, "installs.log")); got != "v1.5.0" {
		t.Fatalf("installer ran for %q, want the local release v1.5.0", got)
	}
	if got := r.read(t, "running-version"); !strings.Contains(got, "v1.5.0") {
		t.Fatalf("running coordinator = %q", got)
	}

	// A second run finds the release already in place.
	res = d.UpgradeCoordinator(context.Background(), UpgradeOptions{})
	if res.Action != UpgradeUpToDate {
		t.Fatalf("second run = %+v, want up to date", res)
	}
}

func TestReleaseTag(t *testing.T) {
	for in, want := range map[string]string{
		"caam v1.2.3 (abc) built on x":        "v1.2.3",
		"caam 1.2.3 (abc)":                    "v1.2.3",
		"caam v2.0.0-rc.1 (abc)":              "v2.0.0-rc.1",
		"caam dev (unknown) built on unknown": "",
		"":                                    "",
	} {
		if got := releaseTag(in); got != want {
			t.Errorf("releaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecStartBinary(t *testing.T) {
	for unit, want := range map[string]string{
		"[Service]\nExecStart=/home/u/bin/caam auth-coordinator --config %h/x\n": "/home/u/bin/caam",
		"ExecStart=" + CoordinatorExecStart("/opt/my tools/caam%1") + "\n":       "/opt/my tools/caam%1",
		"[Service]\nType=simple\n": "",
	} {
		if got := execStartBinary(unit); got != want {
			t.Errorf("execStartBinary(%q) = %q, want %q", unit, got, want)
		}
	}
}

func TestRollbackCoordinatorRestoresLastKnownGood(t *testing.T) {
	r := startFakeRemote(t, "")
	r.installCoordinator(t, "v1.0.0")
	d := r.deployer(t, "v1.5.0")

	if res := d.RollbackCoordinator(context.Background()); res.Action != UpgradeFailed || !strings.Contains(res.Error, "nothing to roll back") {
		t.Fatalf("rollback before any upgrade = %+v", res)
	}
	if res := d.UpgradeCoordinator(context.Background(), UpgradeOptions{}); res.Action != UpgradeUpgraded {
		t.Fatalf("upgrade = %+v", res)
	}

	res := d.RollbackCoordinator(context.Background())
	if res.Action != UpgradeRolledBack || !res.Verified || res.FromVersion != "v1.5.0" || res.ToVersion != "v1.0.0" {
		t.Fatalf("rollback = %+v", res)
	}
	if got := r.read(t, "running-version"); !strings.Contains(got, "v1.0.0") {
		t.Fatalf("running coordinator after rollback = %q", got)
	}
	if bin := execStartBinary(r.read(t, ".config/systemd/user/caam-coordinator.service")); bin != r.path(".local/bin/caam") {
		t.Fatalf("restored unit runs %q", bin)
	}
}
