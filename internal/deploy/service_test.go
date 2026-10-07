package deploy

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recordedRunner struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string
}

func (r *recordedRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	return []byte(r.out[call]), nil
}

func newTestService(t *testing.T, goos string) (*AgentService, *recordedRunner) {
	t.Helper()
	home := t.TempDir()
	config := filepath.Join(home, ".config", "caam", "distributed-agent.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"coordinators":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &recordedRunner{out: map[string]string{}}
	return &AgentService{
		GOOS:       goos,
		Home:       home,
		Executable: "/opt/caam & co/bin/caam",
		ConfigPath: config,
		Run:        runner.run,
		UID:        501,
	}, runner
}

func TestLaunchdServiceInstall(t *testing.T) {
	svc, runner := newTestService(t, "darwin")

	path, err := svc.Install(context.Background())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want := filepath.Join(svc.Home, "Library", "LaunchAgents", AgentServiceLabel+".plist"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The plist is well-formed XML with escaped arguments.
	var parsed struct {
		Dict struct {
			Keys    []string `xml:"key"`
			Strings []string `xml:"string"`
			Arrays  []struct {
				Strings []string `xml:"string"`
			} `xml:"array"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("plist is not valid XML: %v\n%s", err, data)
	}
	if len(parsed.Dict.Arrays) != 1 {
		t.Fatalf("ProgramArguments missing:\n%s", data)
	}
	args := parsed.Dict.Arrays[0].Strings
	want := []string{"/opt/caam & co/bin/caam", "auth-agent", "--config", svc.ConfigPath}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("ProgramArguments = %q, want %q", args, want)
	}
	for _, k := range []string{"Label", "RunAtLoad", "KeepAlive", "StandardErrorPath"} {
		if !strings.Contains(string(data), "<key>"+k+"</key>") {
			t.Errorf("plist missing %s", k)
		}
	}

	wantCalls := []string{
		"launchctl bootout gui/501/" + AgentServiceLabel,
		"launchctl bootstrap gui/501 " + path,
		"launchctl enable gui/501/" + AgentServiceLabel,
	}
	if strings.Join(runner.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("launchctl calls:\n%s\nwant:\n%s", strings.Join(runner.calls, "\n"), strings.Join(wantCalls, "\n"))
	}

	// Reinstalling replaces the definition idempotently.
	if _, err := svc.Install(context.Background()); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
}

func TestSystemdServiceInstallAndUninstall(t *testing.T) {
	svc, runner := newTestService(t, "linux")
	svc.Executable = "/home/u/.local/bin/caam"

	path, err := svc.Install(context.Background())
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExecStart=/home/u/.local/bin/caam auth-agent --config "+svc.ConfigPath+"\n") {
		t.Fatalf("unit ExecStart wrong:\n%s", data)
	}
	if got := runner.calls; len(got) != 3 || got[2] != "systemctl --user restart caam-auth-agent.service" {
		t.Fatalf("systemctl calls = %q", got)
	}

	runner.out["systemctl --user is-active caam-auth-agent.service"] = "active\n"
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Running {
		t.Fatalf("status = %+v", st)
	}

	if _, err := svc.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit still present after uninstall: %v", err)
	}
	// Uninstalling again is a no-op.
	if _, err := svc.Uninstall(context.Background()); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}

func TestServiceInstallRequiresConfig(t *testing.T) {
	svc, runner := newTestService(t, "linux")
	svc.ConfigPath = filepath.Join(svc.Home, "missing.json")
	if _, err := svc.Install(context.Background()); err == nil {
		t.Fatal("install without a config must fail")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("no service commands should run: %q", runner.calls)
	}
}

func TestServiceUnsupportedPlatform(t *testing.T) {
	svc, _ := newTestService(t, "windows")
	if _, err := svc.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Install on windows = %v", err)
	}
}

func TestServiceDefinitionRequiresAbsolutePaths(t *testing.T) {
	svc, _ := newTestService(t, "linux")
	svc.Executable = "caam"
	if _, err := svc.Definition(); err == nil {
		t.Fatal("relative executable must be rejected")
	}
}
