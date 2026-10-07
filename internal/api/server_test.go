package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
)

func TestSplitPath(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"claude/work", []string{"claude", "work"}},
		{"/claude/work/", []string{"claude", "work"}},
		{"codex/profile-1", []string{"codex", "profile-1"}},
		{"", []string{}},
		{"/", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitPath(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("splitPath(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadOrGenerateToken(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "subdir", ".api_token")

	s := &Server{tokenPath: tokenPath}

	// First call should generate
	token1, err := s.loadOrGenerateToken()
	if err != nil {
		t.Fatalf("loadOrGenerateToken() error = %v", err)
	}
	if len(token1) != 64 { // 32 bytes hex = 64 chars
		t.Errorf("token length = %d, want 64", len(token1))
	}

	// File should exist with restricted permissions
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("token file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Second call should return same token
	token2, err := s.loadOrGenerateToken()
	if err != nil {
		t.Fatalf("loadOrGenerateToken() second call error = %v", err)
	}
	if token2 != token1 {
		t.Errorf("second call returned different token")
	}
}

func TestLoadOrGenerateTokenIgnoresWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "subdir", ".api_token")

	if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
		t.Fatalf("mkdir token dir: %v", err)
	}
	if err := os.WriteFile(tokenPath, []byte("  \n\t"), 0600); err != nil {
		t.Fatalf("write whitespace token: %v", err)
	}

	s := &Server{tokenPath: tokenPath}
	token, err := s.loadOrGenerateToken()
	if err != nil {
		t.Fatalf("loadOrGenerateToken() error = %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != token {
		t.Errorf("token file not updated, got %q want %q", got, token)
	}
}

func TestNewServerWithNilHandlers(t *testing.T) {
	cfg := DefaultConfig()
	if _, err := NewServer(cfg, nil); err == nil {
		t.Error("NewServer() expected error with nil handlers")
	}
}

func TestHealthEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	handlers := &Handlers{}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")

	server, err := NewServer(cfg, handlers)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	server.handleHealth(w, req)

	// Check response
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("status = %v, want 'ok'", resp["status"])
	}
}

func TestAuthMiddleware(t *testing.T) {
	tmpDir := t.TempDir()
	handlers := &Handlers{}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")

	server, err := NewServer(cfg, handlers)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	protectedHandler := server.authMiddleware(dummyHandler)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "no auth header",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid format",
			authHeader: "Basic xyz",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong token",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid token",
			authHeader: "Bearer " + server.Token(),
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()

			protectedHandler(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	tmpDir := t.TempDir()
	handlers := &Handlers{}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")

	server, err := NewServer(cfg, handlers)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	corsHandler := server.corsMiddleware(dummyHandler)

	tests := []struct {
		name       string
		origin     string
		wantCORS   bool
		wantStatus int
	}{
		{
			name:       "localhost origin",
			origin:     "http://localhost:3000",
			wantCORS:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "127.0.0.1 origin",
			origin:     "http://127.0.0.1:3000",
			wantCORS:   true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "external origin",
			origin:     "http://example.com",
			wantCORS:   false,
			wantStatus: http.StatusOK,
		},
		{name: "IPv6 loopback", origin: "http://[::1]:3000", wantCORS: true, wantStatus: http.StatusOK},
		{name: "HTTPS localhost", origin: "https://localhost:3000", wantCORS: true, wantStatus: http.StatusOK},
		{name: "userinfo host confusion", origin: "http://localhost:3000@evil.example", wantStatus: http.StatusOK},
		{name: "loopback prefix", origin: "http://127.0.0.1.evil.example:3000", wantStatus: http.StatusOK},
		{name: "localhost suffix", origin: "http://localhost.evil.example", wantStatus: http.StatusOK},
		{name: "opaque origin", origin: "null", wantStatus: http.StatusOK},
		{name: "origin with path", origin: "http://localhost:3000/path", wantStatus: http.StatusOK},
		{name: "origin with query", origin: "http://localhost:3000?host=evil.example", wantStatus: http.StatusOK},
		{name: "origin with invalid port", origin: "http://localhost:not-a-port", wantStatus: http.StatusOK},
		{
			name:       "OPTIONS request",
			origin:     "http://localhost:3000",
			wantCORS:   true,
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := http.MethodGet
			if strings.Contains(tt.name, "OPTIONS") {
				method = http.MethodOptions
			}

			req := httptest.NewRequest(method, "/test", nil)
			req.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()

			corsHandler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			corsHeader := w.Header().Get("Access-Control-Allow-Origin")
			if tt.wantCORS && corsHeader == "" {
				t.Error("expected CORS header, got none")
			}
			if !tt.wantCORS && corsHeader != "" {
				t.Errorf("unexpected CORS header: %s", corsHeader)
			}
		})
	}
}

func TestDashboardAPIUsesAuthenticatedRealDataAndActions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	live := []byte(`{"tokens":{"access_token":"synthetic-live-secret","refresh_token":"live-refresh"}}`)
	target := []byte(`{"tokens":{"access_token":"synthetic-target-secret","refresh_token":"target-refresh"}}`)
	writeAPIActivationFile(t, filepath.Join(root, "codex", "auth.json"), live)
	writeAPIActivationFile(t, vault.BackupPath("codex", "work", "auth.json"), target)
	d, err := caamdb.OpenAt(filepath.Join(root, "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.LogEvent(caamdb.Event{Timestamp: time.Now().Add(-time.Minute), Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"}); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(Config{TokenPath: filepath.Join(root, "api-token")}, NewHandlers(vault, nil, d))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.handler())
	t.Cleanup(ts.Close)
	request := func(method, path, body string, authorized bool) (*http.Response, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "http://localhost:3000")
		if authorized {
			req.Header.Set("Authorization", "Bearer "+server.Token())
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Error("authenticated state may be cached")
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
			t.Error("local dashboard origin cannot read response")
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{server.Token(), "synthetic-live-secret", "synthetic-target-secret"} {
			if bytes.Contains(encoded, []byte(secret)) {
				t.Errorf("API response exposed a credential on %s", path)
			}
		}
		return resp, result
	}
	resp, _ := request(http.MethodGet, "/api/v1/profiles", "", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated profiles status = %d", resp.StatusCode)
	}
	resp, profiles := request(http.MethodGet, "/api/v1/profiles", "", true)
	if resp.StatusCode != http.StatusOK || profiles["count"] != float64(1) {
		t.Fatalf("profiles = %v, status = %d", profiles, resp.StatusCode)
	}
	resp, usage := request(http.MethodGet, "/api/v1/usage?period=1h&tool=codex", "", true)
	rows, ok := usage["usage"].([]any)
	if resp.StatusCode != http.StatusOK || usage["available"] != true || !ok || len(rows) != 1 || rows[0].(map[string]any)["activations"] != float64(1) {
		t.Fatalf("actual usage = %v, status = %d", usage, resp.StatusCode)
	}
	for _, query := range []string{"period=invalid", "period=-1h", "period=24h&tool=unknown"} {
		resp, _ := request(http.MethodGet, "/api/v1/usage?"+query, "", true)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("query %q status = %d", query, resp.StatusCode)
		}
	}
	resp, backup := request(http.MethodPost, "/api/v1/actions/backup", `{"tool":"codex","profile":"work"}`, true)
	if resp.StatusCode != http.StatusOK || backup["success"] != false || len(server.eventCh) != 0 {
		t.Fatalf("unconfirmed backup = %v, events = %d", backup, len(server.eventCh))
	}
	resp, activation := request(http.MethodPost, "/api/v1/actions/activate", `{"tool":"codex","profile":"work"}`, false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized activation accepted: %v", activation)
	}
	before, err := os.ReadFile(filepath.Join(root, "codex", "auth.json"))
	if err != nil || !bytes.Equal(before, live) {
		t.Fatalf("unauthorized request changed live credentials: err = %v", err)
	}
	resp, activation = request(http.MethodPost, "/api/v1/actions/activate", `{"tool":"codex","profile":"work"}`, true)
	if resp.StatusCode != http.StatusOK || activation["success"] != true {
		t.Fatalf("activation = %v, status = %d", activation, resp.StatusCode)
	}
	after, err := os.ReadFile(filepath.Join(root, "codex", "auth.json"))
	if err != nil || !bytes.Equal(after, target) {
		t.Fatalf("successful activation did not publish selected credential: err = %v", err)
	}
	stats, err := d.GetStats("codex", "work")
	if err != nil || stats.TotalActivations != 2 {
		t.Fatalf("API activation not recorded: %+v, err = %v", stats, err)
	}
	if len(server.eventCh) != 1 || (<-server.eventCh).Type != "profile_activated" {
		t.Fatal("successful activation did not emit the correct event")
	}
	resp, backup = request(http.MethodPost, "/api/v1/actions/backup", `{"tool":"codex","profile":"saved-current"}`, true)
	if resp.StatusCode != http.StatusOK || backup["success"] != true {
		t.Fatalf("new backup = %v, status = %d", backup, resp.StatusCode)
	}
	saved, err := os.ReadFile(vault.BackupPath("codex", "saved-current", "auth.json"))
	if err != nil || !bytes.Equal(saved, target) {
		t.Fatalf("backup did not preserve current credentials: err = %v", err)
	}
	if len(server.eventCh) != 1 || (<-server.eventCh).Type != "profile_backed_up" {
		t.Fatal("successful backup did not emit the correct event")
	}
}

func TestEmitAfterStopDoesNotPanic(t *testing.T) {
	tmpDir := t.TempDir()
	handlers := &Handlers{}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")

	server, err := NewServer(cfg, handlers)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := server.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Emit() panicked after Stop(): %v", r)
		}
	}()

	server.Emit(Event{Type: "test", Timestamp: time.Now()})
}

func TestEventBroadcast(t *testing.T) {
	tmpDir := t.TempDir()
	handlers := &Handlers{}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")

	server, err := NewServer(cfg, handlers)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	// Register a client
	clientCh := make(chan Event, 10)
	server.sseMu.Lock()
	server.sseClients[clientCh] = struct{}{}
	server.sseMu.Unlock()

	// Start broadcaster
	go server.broadcastEvents()

	// Emit an event
	testEvent := Event{
		Type:      "test_event",
		Timestamp: time.Now(),
		Data:      map[string]string{"key": "value"},
	}
	server.Emit(testEvent)

	// Wait for event
	select {
	case received := <-clientCh:
		if received.Type != testEvent.Type {
			t.Errorf("event type = %s, want %s", received.Type, testEvent.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event")
	}

	// Cleanup
	server.sseMu.Lock()
	delete(server.sseClients, clientCh)
	server.sseMu.Unlock()
}

func TestDefaultTokenPath(t *testing.T) {
	// Test with CAAM_HOME set
	t.Setenv("CAAM_HOME", "/tmp/test-caam")
	path := defaultTokenPath()
	if path != "/tmp/test-caam/.api_token" {
		t.Errorf("with CAAM_HOME: path = %s, want /tmp/test-caam/.api_token", path)
	}

	// Test without CAAM_HOME (uses home directory)
	t.Setenv("CAAM_HOME", "")
	path = defaultTokenPath()
	if !strings.Contains(path, ".config/caam/.api_token") {
		t.Errorf("without CAAM_HOME: path = %s, expected .config/caam/.api_token", path)
	}
}

func TestActivityEndpointReportsEventsAndCooldowns(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := caamdb.OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	for _, e := range []caamdb.Event{
		{Type: caamdb.EventActivate, Provider: "claude", ProfileName: "work", Timestamp: now.Add(-2 * time.Minute)},
		{Type: caamdb.EventSwitch, Provider: "claude", ProfileName: "home", Timestamp: now.Add(-time.Minute),
			Details: map[string]any{"from": "work", "reason": "rate_limit", "access_token": "sk-secret", "authCode": "c0de"}},
	} {
		if err := db.LogEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.SetCooldown("claude", "work", now, time.Hour, "session limit"); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")
	server, err := NewServer(cfg, NewHandlers(nil, nil, db))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.handler()
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/activity?limit=10", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := get(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", w.Code)
	}

	w := get(server.Token())
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("activity response must not be cached: %v", w.Header())
	}
	var body ActivityResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || len(body.Events) != 2 || body.Events[0].Type != caamdb.EventSwitch || body.Events[0].Profile != "home" {
		t.Fatalf("events = %+v, want newest first", body.Events)
	}
	details := body.Events[0].Details
	if details["from"] != "work" || details["reason"] != "rate_limit" {
		t.Fatalf("details = %v", details)
	}
	if _, ok := details["access_token"]; ok {
		t.Fatal("a token-named detail reached the API")
	}
	if _, ok := details["authCode"]; ok {
		t.Fatal("a code-named detail reached the API")
	}
	if len(body.Cooldowns) != 1 || body.Cooldowns[0].Profile != "work" || body.Cooldowns[0].Notes != "session limit" {
		t.Fatalf("cooldowns = %+v", body.Cooldowns)
	}
}

func TestActivityWithoutDatabaseIsEmpty(t *testing.T) {
	got, err := NewHandlers(nil, nil, nil).GetActivity(0)
	if err != nil || got == nil || got.Available || got.Events == nil || len(got.Events) != 0 || got.Cooldowns == nil {
		t.Fatalf("GetActivity = %+v, %v; want empty lists", got, err)
	}
}

func TestActivityEndpointHidesDiagnosticText(t *testing.T) {
	const diagnostic = "synthetic-activity-sensitive-value"
	tmpDir := t.TempDir()
	db, err := caamdb.OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.LogEvent(caamdb.Event{
		Type: caamdb.EventError, Provider: "claude", ProfileName: "work", Timestamp: now,
		Details: map[string]any{
			"operation": "refresh", "reason": diagnostic, "from": diagnostic,
			"previous_profile": map[string]any{"profile": diagnostic},
			"switched_to":      []any{diagnostic},
			"selection_source": "rotation " + diagnostic, "algorithm": diagnostic,
			"error": "Bearer " + diagnostic, "message": "https://example.test/" + diagnostic,
			"context": map[string]any{"response": diagnostic}, "access_token": diagnostic,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetCooldown("claude", "work", now, time.Hour, "session limit "+diagnostic); err != nil {
		t.Fatal(err)
	}
	beforeEvents, err := db.ListRecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	beforeCooldowns, err := db.ListActiveCooldowns(now)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal([]any{beforeEvents, beforeCooldowns})
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.TokenPath = filepath.Join(tmpDir, ".api_token")
	server, err := NewServer(cfg, NewHandlers(nil, nil, db))
	if err != nil {
		t.Fatal(err)
	}
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/activity", nil)
		req.Header.Set("Authorization", "Bearer "+server.Token())
		w := httptest.NewRecorder()
		server.handler().ServeHTTP(w, req)
		return w
	}
	w := get()
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), diagnostic) {
		t.Fatalf("unsafe activity response: status=%d body=%s", w.Code, w.Body)
	}
	var body ActivityResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Available || len(body.Events) != 1 || len(body.Events[0].Details) != 1 || body.Events[0].Details["operation"] != "refresh" {
		t.Fatalf("safe event context was lost: %+v", body)
	}
	if len(body.Cooldowns) != 1 || body.Cooldowns[0].Notes != "" || body.Cooldowns[0].Until != now.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("cooldown timing must remain usable without diagnostic notes: %+v", body.Cooldowns)
	}
	afterEvents, err := db.ListRecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	afterCooldowns, err := db.ListActiveCooldowns(now)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal([]any{afterEvents, afterCooldowns})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("GET /activity changed stored events or cooldowns")
	}

	// Database parsing failures can also include raw stored values. The HTTP
	// error must not echo them into the dashboard.
	if _, err := db.Conn().Exec("UPDATE activity_log SET timestamp = ?", diagnostic); err != nil {
		t.Fatal(err)
	}
	w = get()
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), diagnostic) || !strings.Contains(w.Body.String(), "activity data could not be read") {
		t.Fatalf("unsafe activity error: status=%d body=%s", w.Code, w.Body)
	}
}
