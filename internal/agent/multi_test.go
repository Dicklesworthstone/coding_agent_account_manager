package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
	"golang.org/x/crypto/ssh"
)

func TestCoordinatorEndpointHealth(t *testing.T) {
	coord := &CoordinatorEndpoint{
		Name: "test",
		URL:  "http://localhost:7890",
	}

	// Initially unhealthy (never checked)
	healthy, errMsg, lastCheck := coord.GetHealth()
	if healthy {
		t.Error("expected initially unhealthy")
	}
	if lastCheck != (time.Time{}) {
		t.Error("expected zero time for lastCheck")
	}

	// Set healthy
	coord.SetHealth(true, "")
	healthy, errMsg, lastCheck = coord.GetHealth()
	if !healthy {
		t.Error("expected healthy after SetHealth(true)")
	}
	if errMsg != "" {
		t.Errorf("expected empty error, got %q", errMsg)
	}
	if lastCheck.IsZero() {
		t.Error("expected lastCheck to be set")
	}

	// Set unhealthy with error
	coord.SetHealth(false, "connection refused")
	healthy, errMsg, _ = coord.GetHealth()
	if healthy {
		t.Error("expected unhealthy")
	}
	if errMsg != "connection refused" {
		t.Errorf("expected 'connection refused', got %q", errMsg)
	}
}

func TestMultiAgentSelectAccount(t *testing.T) {
	config := DefaultMultiConfig()
	config.Accounts = []string{"a@test.com", "b@test.com", "c@test.com"}
	config.AccountStrategy = StrategyLRU

	agent := NewMulti(config)

	// Clear any loaded usage data to start fresh
	agent.mu.Lock()
	agent.accountUsage = make(map[string]*AccountUsage)
	agent.mu.Unlock()

	// First selection should return first account (none used)
	account := agent.selectAccount()
	if account != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", account)
	}

	// Record usage for first account
	agent.recordUsage("a@test.com", "success")
	time.Sleep(10 * time.Millisecond)

	// Should now return second account
	account = agent.selectAccount()
	if account != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", account)
	}

	// Record usage for second
	agent.recordUsage("b@test.com", "success")
	time.Sleep(10 * time.Millisecond)

	// Should return third
	account = agent.selectAccount()
	if account != "c@test.com" {
		t.Errorf("expected c@test.com, got %s", account)
	}
}

func TestMultiAgentRoundRobin(t *testing.T) {
	config := DefaultMultiConfig()
	config.Accounts = []string{"a@test.com", "b@test.com", "c@test.com"}
	config.AccountStrategy = StrategyRoundRobin

	agent := NewMulti(config)

	// Clear any loaded usage data to start fresh
	agent.mu.Lock()
	agent.accountUsage = make(map[string]*AccountUsage)
	agent.mu.Unlock()

	// First selection should return first (nothing used yet)
	account := agent.selectAccount()
	if account != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", account)
	}

	// Manually set usage times to control order
	now := time.Now()
	agent.mu.Lock()
	agent.accountUsage["a@test.com"] = &AccountUsage{Email: "a@test.com", LastUsed: now}
	agent.mu.Unlock()

	// Next should be second (a was most recent)
	account = agent.selectAccount()
	if account != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", account)
	}

	// Update b as most recent
	agent.mu.Lock()
	agent.accountUsage["b@test.com"] = &AccountUsage{Email: "b@test.com", LastUsed: now.Add(time.Second)}
	agent.mu.Unlock()

	// Next should be third
	account = agent.selectAccount()
	if account != "c@test.com" {
		t.Errorf("expected c@test.com, got %s", account)
	}

	// Update c as most recent
	agent.mu.Lock()
	agent.accountUsage["c@test.com"] = &AccountUsage{Email: "c@test.com", LastUsed: now.Add(2 * time.Second)}
	agent.mu.Unlock()

	// Should wrap to first
	account = agent.selectAccount()
	if account != "a@test.com" {
		t.Errorf("expected wrap to a@test.com, got %s", account)
	}
}

func TestMultiAgentPollCoordinators(t *testing.T) {
	var requestCount atomic.Int32

	// Create test coordinators
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer ts1.Close()

	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer ts2.Close()

	config := DefaultMultiConfig()
	config.PollInterval = 50 * time.Millisecond
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "coord1", URL: ts1.URL},
		{Name: "coord2", URL: ts2.URL},
	}

	agent := NewMulti(config)

	// Manually poll once
	ctx := context.Background()
	agent.pollAllCoordinators(ctx)

	// Both should be polled
	count := requestCount.Load()
	if count != 2 {
		t.Errorf("expected 2 requests, got %d", count)
	}

	// Both should be healthy
	for _, coord := range config.Coordinators {
		healthy, _, _ := coord.GetHealth()
		if !healthy {
			t.Errorf("coordinator %s should be healthy", coord.Name)
		}
	}
}

func TestMultiAgentUnhealthyCoordinator(t *testing.T) {
	// One healthy, one unreachable
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer ts.Close()

	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "healthy", URL: ts.URL},
		{Name: "unhealthy", URL: "http://127.0.0.1:1"}, // Invalid port
	}

	agent := NewMulti(config)

	ctx := context.Background()
	agent.pollAllCoordinators(ctx)

	// Check health status
	h1, _, _ := config.Coordinators[0].GetHealth()
	h2, err2, _ := config.Coordinators[1].GetHealth()

	if !h1 {
		t.Error("expected first coordinator to be healthy")
	}
	if h2 {
		t.Error("expected second coordinator to be unhealthy")
	}
	if err2 == "" {
		t.Error("expected error message for unhealthy coordinator")
	}
}

func TestMultiAgentStatusEndpoint(t *testing.T) {
	config := DefaultMultiConfig()
	config.Port = 0 // Let OS assign port
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "coord1", URL: "http://localhost:7890"},
		{Name: "coord2", URL: "http://localhost:7891"},
	}
	config.Accounts = []string{"test@example.com"}

	agent := NewMulti(config)

	// Test handler directly
	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()

	agent.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if status["coordinator_count"].(float64) != 2 {
		t.Errorf("expected 2 coordinators, got %v", status["coordinator_count"])
	}
}

func TestMultiAgentCoordinatorsEndpoint(t *testing.T) {
	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "csd", URL: "http://100.100.118.85:7890", DisplayName: "Sense Demo"},
		{Name: "css", URL: "http://100.90.148.85:7890", DisplayName: "Super Server"},
	}

	// Mark one as healthy
	config.Coordinators[0].SetHealth(true, "")
	config.Coordinators[1].SetHealth(false, "connection refused")

	agent := NewMulti(config)

	req := httptest.NewRequest("GET", "/coordinators", nil)
	w := httptest.NewRecorder()

	agent.handleCoordinators(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var coords []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&coords); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(coords) != 2 {
		t.Fatalf("expected 2 coordinators, got %d", len(coords))
	}

	// Check first (healthy)
	if coords[0]["name"] != "csd" {
		t.Errorf("expected name=csd, got %v", coords[0]["name"])
	}
	if coords[0]["is_healthy"] != true {
		t.Errorf("expected csd to be healthy")
	}

	// Check second (unhealthy)
	if coords[1]["is_healthy"] != false {
		t.Errorf("expected css to be unhealthy")
	}
	if coords[1]["last_error"] != "connection refused" {
		t.Errorf("expected error message, got %v", coords[1]["last_error"])
	}
}

func TestMultiAgentAddRemoveCoordinator(t *testing.T) {
	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "initial", URL: "http://localhost:7890"},
	}

	agent := NewMulti(config)

	if len(agent.GetCoordinators()) != 1 {
		t.Fatalf("expected 1 coordinator")
	}

	// Add
	agent.AddCoordinator(&CoordinatorEndpoint{
		Name: "new",
		URL:  "http://localhost:7891",
	})

	if len(agent.GetCoordinators()) != 2 {
		t.Errorf("expected 2 coordinators after add")
	}

	// Remove
	removed := agent.RemoveCoordinator("initial")
	if !removed {
		t.Error("expected RemoveCoordinator to return true")
	}

	if len(agent.GetCoordinators()) != 1 {
		t.Errorf("expected 1 coordinator after remove")
	}

	// Try to remove non-existent
	removed = agent.RemoveCoordinator("nonexistent")
	if removed {
		t.Error("expected RemoveCoordinator to return false for non-existent")
	}
}

func TestMultiAgentDuplicateRequestPrevention(t *testing.T) {
	// Test the processing map logic directly without browser
	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "test", URL: "http://localhost:7890"},
	}

	agent := NewMulti(config)

	// Simulate marking a request as processing
	agent.procMu.Lock()
	agent.processing["request-123"] = true
	agent.procMu.Unlock()

	// Check that it's in the map
	agent.procMu.Lock()
	processing := agent.processing["request-123"]
	agent.procMu.Unlock()

	if !processing {
		t.Error("expected request to be marked as processing")
	}

	// Simulate completion (removing from map)
	agent.procMu.Lock()
	delete(agent.processing, "request-123")
	agent.procMu.Unlock()

	// Should no longer be processing
	agent.procMu.Lock()
	processing = agent.processing["request-123"]
	agent.procMu.Unlock()

	if processing {
		t.Error("expected request to no longer be processing after delete")
	}
}

func TestMultiAgentCheckCoordinatorDeduplication(t *testing.T) {
	var pollCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/pending" {
			pollCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			// Return same request ID every time
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"id":         "dup-request-456",
					"pane_id":    1,
					"url":        "https://example.com/oauth",
					"created_at": time.Now(),
				},
			})
		}
	}))
	defer ts.Close()

	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "test", URL: ts.URL},
	}

	agent := NewMulti(config)

	// Pre-mark the request as processing
	agent.procMu.Lock()
	agent.processing["dup-request-456"] = true
	agent.procMu.Unlock()

	ctx := context.Background()

	// Poll should skip the already-processing request
	agent.checkCoordinator(ctx, config.Coordinators[0])

	// Give any goroutines a chance to start (they shouldn't)
	time.Sleep(50 * time.Millisecond)

	// Verify we polled but didn't spawn new processing
	if pollCount.Load() != 1 {
		t.Errorf("expected 1 poll, got %d", pollCount.Load())
	}

	// Coordinator should be healthy
	healthy, _, _ := config.Coordinators[0].GetHealth()
	if !healthy {
		t.Error("expected coordinator to be healthy after poll")
	}
}

// =============================================================================
// Distributed transport end to end (caam-3ezz.8)
// =============================================================================

// scriptedPane plays a Claude Code pane through the login flow: it is rate
// limited until /login arrives, then shows the OAuth URL until a code is
// typed, then reports success.
type scriptedPane struct {
	mu       sync.Mutex
	loggedIn bool
	codes    []string
	sent     []string
}

func (p *scriptedPane) ListPanes(ctx context.Context) ([]coordinator.Pane, error) {
	return []coordinator.Pane{{PaneID: 1, Title: "claude"}}, nil
}

func (p *scriptedPane) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case len(p.codes) > 0:
		return "Logged in as a@example.com", nil
	case p.loggedIn:
		return "Browse to https://claude.ai/oauth/authorize?code=true&state=abc\nPaste code here if prompted >", nil
	default:
		return "You've hit your limit · resets 2pm", nil
	}
}

func (p *scriptedPane) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, text)
	switch {
	case text == "/login\n":
		p.loggedIn = true
	case p.loggedIn && strings.HasPrefix(text, "CODE"):
		p.codes = append(p.codes, text)
	}
	return nil
}

func (p *scriptedPane) IsAvailable(ctx context.Context) bool { return true }
func (p *scriptedPane) Backend() string                      { return "scripted" }

func (p *scriptedPane) injectedCodes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.codes...)
}

// dropFirstAck forwards requests to the coordinator API but, for the first
// /auth/complete, lets the coordinator process it and then cuts the
// connection before the acknowledgement reaches the agent.
func dropFirstAck(t *testing.T, backend string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	target, err := url.Parse(backend)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	var completes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/complete" || completes.Add(1) != 1 {
			proxy.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		req, _ := http.NewRequest(http.MethodPost, backend+"/auth/complete", bytes.NewReader(body))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("forward first completion: %v", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("coordinator rejected first completion: %s", resp.Status)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	return srv, &completes
}

func TestDroppedAcknowledgementIsRedeliveredAndInjectedOnce(t *testing.T) {
	pane := &scriptedPane{}
	cfg := coordinator.DefaultConfig()
	cfg.PaneClient = pane
	cfg.PollInterval = 10 * time.Millisecond
	cfg.LoginCooldown = time.Millisecond
	cfg.ResumeCooldown = time.Millisecond
	cfg.AuthToken = "secret-token"
	cfg.Logger = discardLogger()
	coord := coordinator.New(cfg)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := coordinator.NewAPIServer(coord, "127.0.0.1", 0, discardLogger())
	go api.Serve(listener)
	defer api.Shutdown(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := coord.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer coord.Stop()

	proxy, completes := dropFirstAck(t, "http://"+listener.Addr().String())
	defer proxy.Close()

	endpoint := &CoordinatorEndpoint{Name: "remote", URL: proxy.URL, Token: "secret-token"}

	// Wait for the coordinator to publish the pane's auth request.
	var pending []pendingRequest
	deadline := time.Now().Add(5 * time.Second)
	for len(pending) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("coordinator never published an auth request")
		}
		time.Sleep(10 * time.Millisecond)
		pending, err = fetchPending(ctx, endpoint.httpClient(), endpoint.URL, endpoint.Token)
		if err != nil {
			t.Fatalf("fetch pending: %v", err)
		}
	}

	mcfg := DefaultMultiConfig()
	mcfg.Coordinators = []*CoordinatorEndpoint{endpoint}
	mcfg.Logger = discardLogger()
	ma := NewMulti(mcfg)
	ma.delivery = fastDelivery
	ma.oauth = &fakeOAuth{code: "CODE-123", account: "a@example.com"}

	var completed, failed atomic.Int32
	ma.OnAuthComplete = func(c, account, code string) { completed.Add(1) }
	ma.OnAuthFailed = func(c, account string, err error) {
		t.Errorf("unexpected failure: %v", err)
		failed.Add(1)
	}

	ma.processAuthRequest(ctx, endpoint, pending[0].ID, pending[0].URL)

	if completes.Load() < 2 {
		t.Fatalf("completion attempts = %d; the dropped acknowledgement must be redelivered", completes.Load())
	}
	if completed.Load() != 1 || failed.Load() != 0 {
		t.Fatalf("completed=%d failed=%d, want exactly one acknowledged success", completed.Load(), failed.Load())
	}

	// The pane receives the code once and returns to idle.
	deadline = time.Now().Add(5 * time.Second)
	for {
		status := coord.GetStatus()
		if len(pane.injectedCodes()) > 0 && status[1] == coordinator.StateIdle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never completed login: state=%v injected=%v", status[1], pane.injectedCodes())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := pane.injectedCodes(); len(got) != 1 || got[0] != "CODE-123\n" {
		t.Fatalf("injected codes = %q, want exactly one", got)
	}
}

func TestCoordinatorRejectsAgentWithWrongToken(t *testing.T) {
	cfg := coordinator.DefaultConfig()
	cfg.PaneClient = &scriptedPane{}
	cfg.AuthToken = "right"
	cfg.Logger = discardLogger()
	coord := coordinator.New(cfg)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := coordinator.NewAPIServer(coord, "127.0.0.1", 0, discardLogger())
	go api.Serve(listener)
	defer api.Shutdown(context.Background())

	endpoint := &CoordinatorEndpoint{Name: "remote", URL: "http://" + listener.Addr().String(), Token: "wrong"}
	mcfg := DefaultMultiConfig()
	mcfg.Coordinators = []*CoordinatorEndpoint{endpoint}
	mcfg.Logger = discardLogger()
	NewMulti(mcfg).checkCoordinator(context.Background(), endpoint)

	healthy, lastErr, _ := endpoint.GetHealth()
	if healthy || !strings.Contains(lastErr, "401") {
		t.Fatalf("healthy=%v lastErr=%q, want unauthorized", healthy, lastErr)
	}

	err = deliverCompletion(context.Background(), endpoint.httpClient(), endpoint.URL, endpoint.Token,
		completion{RequestID: "r1", Code: "CODE"}, fastDelivery, discardLogger())
	var rejected *DeliveryRejectedError
	if !errors.As(err, &rejected) || rejected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("delivery with wrong token = %v, want 401 rejection", err)
	}
}

// testSSHServer is a minimal SSH server that accepts one public key and
// forwards direct-tcpip channels, like sshd with AllowTcpForwarding.
type testSSHServer struct {
	addr     string
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
	accepted atomic.Int32
}

func startTestSSHServer(t *testing.T, clientKey ssh.PublicKey) *testSSHServer {
	t.Helper()
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
			if bytes.Equal(key.Marshal(), clientKey.Marshal()) {
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
	s := &testSSHServer{addr: listener.Addr().String(), listener: listener}
	go func() {
		for {
			nc, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, nc)
			s.mu.Unlock()
			go s.serve(nc, config)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		s.dropConnections()
	})
	return s
}

func (s *testSSHServer) serve(nc net.Conn, config *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, config)
	if err != nil {
		nc.Close()
		return
	}
	s.accepted.Add(1)
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "direct-tcpip" {
			newCh.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
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
	}
}

// dropConnections severs every SSH connection, as a network change would.
func (s *testSSHServer) dropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func TestSSHTunnelReachesLoopbackCoordinatorAndReconnects(t *testing.T) {
	// The coordinator listens only on loopback of the "remote" host.
	var polls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		polls.Add(1)
		json.NewEncoder(w).Encode([]pendingRequest{})
	}))
	defer backend.Close()

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
	keyPath := filepath.Join(t.TempDir(), "id_test")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "")

	server := startTestSSHServer(t, sshPub)
	host, portStr, _ := net.SplitHostPort(server.addr)
	port, _ := strconv.Atoi(portStr)

	endpoint := &CoordinatorEndpoint{
		Name:  "remote",
		URL:   backend.URL, // resolved on the SSH server's side
		Token: "tok",
		SSH:   &SSHTunnel{Host: host, Port: port, User: "tester", IdentityFile: keyPath},
	}
	defer endpoint.close()
	mcfg := DefaultMultiConfig()
	mcfg.Coordinators = []*CoordinatorEndpoint{endpoint}
	mcfg.Logger = discardLogger()
	ma := NewMulti(mcfg)

	ma.checkCoordinator(context.Background(), endpoint)
	if healthy, lastErr, _ := endpoint.GetHealth(); !healthy {
		t.Fatalf("poll over SSH failed: %s", lastErr)
	}

	// Sever the SSH connection; the next poll reconnects transparently.
	server.dropConnections()
	endpoint.httpClient().CloseIdleConnections()
	ma.checkCoordinator(context.Background(), endpoint)
	if healthy, lastErr, _ := endpoint.GetHealth(); !healthy {
		t.Fatalf("poll after reconnect failed: %s", lastErr)
	}
	if polls.Load() != 2 {
		t.Fatalf("coordinator polls = %d, want 2", polls.Load())
	}
	if server.accepted.Load() < 2 {
		t.Fatalf("ssh connections = %d, want a reconnect", server.accepted.Load())
	}
}

func TestAgentFileConfigRoundTripKeepsTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	fc := FileConfig{
		Port:          7891,
		PollInterval:  "2s",
		Strategy:      "lru",
		ChromeProfile: "/profiles/work",
		Accounts:      []string{"a@example.com"},
		Coordinators: []*CoordinatorEndpoint{{
			Name:  "csd",
			URL:   "http://127.0.0.1:7890",
			Token: "tok",
			SSH:   &SSHTunnel{Host: "100.64.0.5", Port: 22, User: "ubuntu", IdentityFile: "~/.ssh/id_ed25519"},
		}},
	}
	if err := WriteFileConfig(path, fc); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %v, want 0600 (it holds tokens)", info.Mode().Perm())
	}
	got, err := LoadFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Coordinators[0]
	if c.Token != "tok" || c.SSH == nil || *c.SSH != *fc.Coordinators[0].SSH || got.ChromeUserDataDir() != "/profiles/work" {
		t.Fatalf("round trip lost data: %+v ssh=%+v", got, c.SSH)
	}
}

// trackingOAuth records concurrency and the accounts it was asked to use.
type trackingOAuth struct {
	mu       sync.Mutex
	active   int
	maxSeen  int
	accounts []string
}

func (f *trackingOAuth) CompleteOAuth(ctx context.Context, oauthURL, preferredAccount string) (string, string, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxSeen {
		f.maxSeen = f.active
	}
	f.accounts = append(f.accounts, preferredAccount)
	f.mu.Unlock()

	time.Sleep(20 * time.Millisecond) // a browser flow takes a while

	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return "CODE-" + preferredAccount, preferredAccount, nil
}

func TestConcurrentRequestsSerializeBrowserAndSpreadAccounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer srv.Close()

	accounts := []string{"a@example.com", "b@example.com", "c@example.com"}
	mcfg := DefaultMultiConfig()
	mcfg.Accounts = accounts
	mcfg.Logger = discardLogger()
	ma := NewMulti(mcfg)
	// Start from a clean usage history regardless of earlier tests.
	ma.accountUsage = map[string]*AccountUsage{}
	ma.delivery = fastDelivery
	oauth := &trackingOAuth{}
	ma.oauth = oauth
	endpoint := &CoordinatorEndpoint{Name: "remote", URL: srv.URL}

	var wg sync.WaitGroup
	for i := 0; i < len(accounts); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ma.processAuthRequest(context.Background(), endpoint, fmt.Sprintf("req-%d", i), "https://claude.ai/oauth/authorize?x")
		}(i)
	}
	wg.Wait()

	if oauth.maxSeen != 1 {
		t.Fatalf("max concurrent browser flows = %d, want 1 (they share a Chrome profile)", oauth.maxSeen)
	}
	seen := map[string]bool{}
	for _, acc := range oauth.accounts {
		seen[acc] = true
	}
	if len(seen) != len(accounts) {
		t.Fatalf("simultaneous rate limits used accounts %v, want each of %v once", oauth.accounts, accounts)
	}
}

func TestCoordinatorEndpointProbe(t *testing.T) {
	cfg := coordinator.DefaultConfig()
	cfg.PaneClient = &scriptedPane{}
	cfg.AuthToken = "tok"
	cfg.Logger = discardLogger()
	coord := coordinator.New(cfg)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := coordinator.NewAPIServer(coord, "127.0.0.1", 0, discardLogger())
	go api.Serve(listener)
	defer api.Shutdown(context.Background())

	good := &CoordinatorEndpoint{Name: "c", URL: "http://" + listener.Addr().String(), Token: "tok"}
	probe := good.Probe(context.Background())
	if !probe.Healthy || probe.Backend != "scripted" || probe.Error != "" {
		t.Fatalf("probe = %+v", probe)
	}

	bad := &CoordinatorEndpoint{Name: "c", URL: good.URL, Token: "wrong"}
	if probe := bad.Probe(context.Background()); probe.Healthy || !strings.Contains(probe.Error, "401") {
		t.Fatalf("probe with wrong token = %+v", probe)
	}

	down := &CoordinatorEndpoint{Name: "c", URL: "http://127.0.0.1:1"}
	if probe := down.Probe(context.Background()); probe.Healthy || probe.Error == "" {
		t.Fatalf("probe of a closed port = %+v", probe)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestInfoLogsNeverContainCodesOrOAuthQueries(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pane := &scriptedPane{}
	cfg := coordinator.DefaultConfig()
	cfg.PaneClient = pane
	cfg.PollInterval = 10 * time.Millisecond
	cfg.LoginCooldown = time.Millisecond
	cfg.ResumeCooldown = time.Millisecond
	cfg.AuthToken = "secret-token"
	cfg.Logger = logger
	coord := coordinator.New(cfg)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := coordinator.NewAPIServer(coord, "127.0.0.1", 0, logger)
	go api.Serve(listener)
	defer api.Shutdown(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := coord.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer coord.Stop()

	endpoint := &CoordinatorEndpoint{Name: "remote", URL: "http://" + listener.Addr().String(), Token: "secret-token"}
	var pending []pendingRequest
	deadline := time.Now().Add(5 * time.Second)
	for len(pending) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		pending, _ = fetchPending(ctx, endpoint.httpClient(), endpoint.URL, endpoint.Token)
	}
	if len(pending) == 0 {
		t.Fatal("no auth request published")
	}

	mcfg := DefaultMultiConfig()
	mcfg.Logger = logger
	ma := NewMulti(mcfg)
	ma.delivery = fastDelivery
	ma.oauth = &fakeOAuth{code: "CODE-SECRET-9f8e7d", account: "a@example.com"}
	ma.processAuthRequest(ctx, endpoint, pending[0].ID, pending[0].URL)

	deadline = time.Now().Add(5 * time.Second)
	for len(pane.injectedCodes()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := pane.injectedCodes(); len(got) != 1 || got[0] != "CODE-SECRET-9f8e7d\n" {
		t.Fatalf("injected codes = %q, want the delivered code once", got)
	}

	out := logs.String()
	for _, secret := range []string{"CODE-SECRET-9f8e7d", "state=abc", "secret-token"} {
		if strings.Contains(out, secret) {
			t.Errorf("info-level logs contain %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "request_id") {
		t.Errorf("logs should correlate by request_id:\n%s", out)
	}
}
