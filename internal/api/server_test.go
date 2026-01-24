package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
