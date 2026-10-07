package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
	"github.com/spf13/cobra"
)

func TestLoadCoordinatorConfig(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "coordinator.json")

	data := []byte(`{
  "port": 9999,
  "poll_interval": "750ms",
  "auth_timeout": "45s",
  "state_timeout": "15s",
  "resume_prompt": "resume now",
  "output_lines": 55,
  "backend": "tmux"
}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, listen, err := loadCoordinatorConfig(path)
	if err != nil {
		t.Fatalf("loadCoordinatorConfig error: %v", err)
	}
	if listen.Port != 9999 {
		t.Fatalf("port = %d, want 9999", listen.Port)
	}
	if listen.Bind != coordinator.DefaultBindAddress {
		t.Fatalf("bind = %q, want loopback default", listen.Bind)
	}
	if cfg.PollInterval != 750*time.Millisecond {
		t.Fatalf("PollInterval = %v, want 750ms", cfg.PollInterval)
	}
	if cfg.AuthTimeout != 45*time.Second {
		t.Fatalf("AuthTimeout = %v, want 45s", cfg.AuthTimeout)
	}
	if cfg.StateTimeout != 15*time.Second {
		t.Fatalf("StateTimeout = %v, want 15s", cfg.StateTimeout)
	}
	if cfg.ResumePrompt != "resume now" {
		t.Fatalf("ResumePrompt = %q, want %q", cfg.ResumePrompt, "resume now")
	}
	if cfg.OutputLines != 55 {
		t.Fatalf("OutputLines = %d, want 55", cfg.OutputLines)
	}
	if cfg.Backend != coordinator.BackendTmux {
		t.Fatalf("Backend = %s, want %s", cfg.Backend, coordinator.BackendTmux)
	}
}

func TestLoadCoordinatorConfigBindAndToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.json")
	data := []byte(`{"bind": "100.64.0.5", "port": 7890, "auth_token": "tok"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, listen, err := loadCoordinatorConfig(path)
	if err != nil {
		t.Fatalf("loadCoordinatorConfig error: %v", err)
	}
	if listen.Bind != "100.64.0.5" || listen.Port != 7890 {
		t.Fatalf("listen = %+v", listen)
	}
	if cfg.AuthToken != "tok" {
		t.Fatalf("AuthToken = %q", cfg.AuthToken)
	}
	if err := coordinator.ValidateListenSecurity(listen.Bind, cfg.AuthToken); err != nil {
		t.Fatalf("tokened non-loopback bind rejected: %v", err)
	}
	if err := coordinator.ValidateListenSecurity(listen.Bind, ""); err == nil {
		t.Fatal("non-loopback bind without token must be rejected")
	}
}

func TestLoadCoordinatorConfigRejectsBadDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.json")
	if err := os.WriteFile(path, []byte(`{"poll_interval": "-1s"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCoordinatorConfig(path); err == nil {
		t.Fatal("negative poll_interval must be rejected")
	}
}

func TestCoordinatorStatusUsesConfigAddressAndToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(coordinator.StatusResponse{
			Running:      true,
			Backend:      "wezterm",
			PaneCount:    1,
			PendingAuths: 1,
			Panes:        []coordinator.PaneStatusResponse{{PaneID: 7, State: "AUTH_PENDING", RequestID: "req-1"}},
		})
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "coordinator.json")
	if err := os.WriteFile(path, []byte(`{"bind":"0.0.0.0","port":`+port+`,"auth_token":"from-config"}`), 0600); err != nil {
		t.Fatal(err)
	}

	coordinatorStatusConfig, coordinatorStatusURL, coordinatorStatusToken, coordinatorStatusJSON = path, "", "", false
	t.Cleanup(func() { coordinatorStatusConfig = "" })

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	if err := runCoordinatorStatus(cmd, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
	if gotAuth != "Bearer from-config" {
		t.Fatalf("Authorization = %q, want token from config", gotAuth)
	}
	if !strings.Contains(out.String(), "pane 7") || !strings.Contains(out.String(), "request=req-1") {
		t.Fatalf("unexpected status output:\n%s", out.String())
	}
}

func TestCheckCoordinatorsUsesConfiguredEndpoints(t *testing.T) {
	cfg := coordinator.DefaultConfig()
	cfg.PaneClient = &fakeCoordinatorPanes{}
	cfg.AuthToken = "local-token"
	coord := coordinator.New(cfg)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := coordinator.NewAPIServer(coord, "127.0.0.1", 0, nil)
	go api.Serve(listener)
	defer api.Shutdown(context.Background())
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	coordPath := filepath.Join(configDir, "caam", "coordinator.json")
	if err := os.MkdirAll(filepath.Dir(coordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coordPath, []byte(`{"port":`+port+`,"auth_token":"local-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(coordPath) })

	coords := checkCoordinators()
	if len(coords) != 1 {
		t.Fatalf("coords = %+v", coords)
	}
	c := coords[0]
	if c.Source != "local" || !c.Healthy || c.Backend != "fake" || c.Transport != "direct" || c.Error != "" {
		t.Fatalf("local coordinator = %+v", c)
	}
}

type fakeCoordinatorPanes struct{}

func (fakeCoordinatorPanes) ListPanes(context.Context) ([]coordinator.Pane, error) { return nil, nil }
func (fakeCoordinatorPanes) GetText(context.Context, int, int) (string, error)    { return "", nil }
func (fakeCoordinatorPanes) SendText(context.Context, int, string, bool) error    { return nil }
func (fakeCoordinatorPanes) IsAvailable(context.Context) bool                     { return true }
func (fakeCoordinatorPanes) Backend() string                                      { return "fake" }
