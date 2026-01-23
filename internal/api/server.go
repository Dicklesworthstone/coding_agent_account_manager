// Package api provides a local HTTP API server for caam.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Server is the local HTTP API server.
type Server struct {
	port       int
	token      string
	tokenPath  string
	logger     *slog.Logger
	httpServer *http.Server
	handlers   *Handlers

	// SSE clients for live updates
	sseClients   map[chan Event]struct{}
	sseMu        sync.RWMutex
	eventCh      chan Event
	shutdownOnce sync.Once
}

// Config holds server configuration.
type Config struct {
	Port      int
	TokenPath string
	Logger    *slog.Logger
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Port:      7891,
		TokenPath: defaultTokenPath(),
		Logger:    slog.Default(),
	}
}

// defaultTokenPath returns the default path for storing the API token.
func defaultTokenPath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, ".api_token")
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config", "caam", ".api_token")
}

// NewServer creates a new API server.
func NewServer(cfg Config, handlers *Handlers) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	s := &Server{
		port:       cfg.Port,
		tokenPath:  cfg.TokenPath,
		logger:     cfg.Logger,
		handlers:   handlers,
		sseClients: make(map[chan Event]struct{}),
		eventCh:    make(chan Event, 100),
	}

	// Load or generate token
	token, err := s.loadOrGenerateToken()
	if err != nil {
		return nil, fmt.Errorf("token setup: %w", err)
	}
	s.token = token

	return s, nil
}

// loadOrGenerateToken loads an existing token or generates a new one.
func (s *Server) loadOrGenerateToken() (string, error) {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.tokenPath), 0700); err != nil {
		return "", err
	}

	// Try to read existing token
	data, err := os.ReadFile(s.tokenPath)
	if err == nil && len(data) > 0 {
		return string(data), nil
	}

	// Generate new token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)

	// Save token with restricted permissions
	if err := os.WriteFile(s.tokenPath, []byte(token), 0600); err != nil {
		return "", err
	}

	return token, nil
}

// Token returns the API authentication token.
func (s *Server) Token() string {
	return s.token
}

// Start starts the server.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Health check (no auth required)
	mux.HandleFunc("/health", s.handleHealth)

	// Auth-protected endpoints
	mux.HandleFunc("/api/v1/status", s.authMiddleware(s.handleStatus))
	mux.HandleFunc("/api/v1/profiles", s.authMiddleware(s.handleProfiles))
	mux.HandleFunc("/api/v1/profiles/", s.authMiddleware(s.handleProfileAction))
	mux.HandleFunc("/api/v1/usage", s.authMiddleware(s.handleUsage))
	mux.HandleFunc("/api/v1/coordinators", s.authMiddleware(s.handleCoordinators))
	mux.HandleFunc("/api/v1/actions/activate", s.authMiddleware(s.handleActivate))
	mux.HandleFunc("/api/v1/actions/backup", s.authMiddleware(s.handleBackup))
	mux.HandleFunc("/api/v1/events", s.authMiddleware(s.handleSSE))

	// CORS middleware for localhost only
	handler := s.corsMiddleware(mux)

	// Bind to localhost only
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	s.httpServer = &http.Server{
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start SSE broadcaster
	go s.broadcastEvents()

	s.logger.Info("API server starting", "addr", addr)
	return s.httpServer.Serve(listener)
}

// Stop gracefully stops the server.
func (s *Server) Stop(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		close(s.eventCh)
	})
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Port returns the configured port.
func (s *Server) Port() int {
	return s.port
}

// Emit sends an event to all SSE clients.
func (s *Server) Emit(event Event) {
	select {
	case s.eventCh <- event:
	default:
		// Channel full, drop event
		s.logger.Warn("event channel full, dropping event", "type", event.Type)
	}
}

// broadcastEvents sends events to all SSE clients.
func (s *Server) broadcastEvents() {
	for event := range s.eventCh {
		s.sseMu.RLock()
		for clientCh := range s.sseClients {
			select {
			case clientCh <- event:
			default:
				// Client slow, skip
			}
		}
		s.sseMu.RUnlock()
	}
}

// corsMiddleware adds CORS headers for localhost.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Only allow localhost origins
		allowedOrigins := []string{
			"http://localhost",
			"http://127.0.0.1",
			"http://localhost:3000",
			"http://127.0.0.1:3000",
		}

		allowed := false
		for _, ao := range allowedOrigins {
			if origin == ao || (len(origin) > len(ao) && origin[:len(ao)+1] == ao+":") {
				allowed = true
				break
			}
		}

		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// authMiddleware validates the bearer token.
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			s.jsonError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}

		const prefix = "Bearer "
		if len(auth) < len(prefix) || auth[:len(prefix)] != prefix {
			s.jsonError(w, http.StatusUnauthorized, "invalid authorization format")
			return
		}

		token := auth[len(prefix):]
		if token != s.token {
			s.jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		next(w, r)
	}
}

// jsonError writes a JSON error response.
func (s *Server) jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// jsonResponse writes a JSON response.
func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		s.logger.Error("json encode failed", "error", err)
	}
}

// handleHealth returns server health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.jsonResponse(w, map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// handleStatus returns overall caam status.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	status, err := s.handlers.GetStatus()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, status)
}

// handleProfiles returns profiles list.
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tool := r.URL.Query().Get("tool")
	profiles, err := s.handlers.GetProfiles(tool)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, profiles)
}

// handleProfileAction handles per-profile operations.
func (s *Server) handleProfileAction(w http.ResponseWriter, r *http.Request) {
	// Parse /api/v1/profiles/{tool}/{name}
	path := r.URL.Path[len("/api/v1/profiles/"):]
	parts := splitPath(path)
	if len(parts) < 2 {
		s.jsonError(w, http.StatusBadRequest, "invalid path: expected /profiles/{tool}/{name}")
		return
	}
	tool, name := parts[0], parts[1]

	switch r.Method {
	case http.MethodGet:
		profile, err := s.handlers.GetProfile(tool, name)
		if err != nil {
			s.jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		s.jsonResponse(w, profile)

	case http.MethodDelete:
		if err := s.handlers.DeleteProfile(tool, name); err != nil {
			s.jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.jsonResponse(w, map[string]string{"status": "deleted"})

	default:
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleUsage returns usage statistics.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tool := r.URL.Query().Get("tool")
	usage, err := s.handlers.GetUsage(tool)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, usage)
}

// handleCoordinators returns coordinator status.
func (s *Server) handleCoordinators(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	coordinators, err := s.handlers.GetCoordinators()
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.jsonResponse(w, coordinators)
}

// handleActivate handles profile activation.
func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req ActivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	result, err := s.handlers.Activate(req)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Emit event
	s.Emit(Event{
		Type:      "profile_activated",
		Timestamp: time.Now(),
		Data:      result,
	})

	s.jsonResponse(w, result)
}

// handleBackup handles profile backup.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req BackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	result, err := s.handlers.Backup(req)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Emit event
	s.Emit(Event{
		Type:      "profile_backed_up",
		Timestamp: time.Now(),
		Data:      result,
	})

	s.jsonResponse(w, result)
}

// handleSSE handles Server-Sent Events for live updates.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Check if client supports SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.jsonError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create client channel
	clientCh := make(chan Event, 10)
	s.sseMu.Lock()
	s.sseClients[clientCh] = struct{}{}
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, clientCh)
		s.sseMu.Unlock()
		close(clientCh)
	}()

	// Send initial ping
	fmt.Fprintf(w, "event: ping\ndata: {\"time\":\"%s\"}\n\n", time.Now().Format(time.RFC3339))
	flusher.Flush()

	// Stream events
	for {
		select {
		case event, ok := <-clientCh:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()

		case <-r.Context().Done():
			return

		case <-time.After(30 * time.Second):
			// Keep-alive ping
			fmt.Fprintf(w, "event: ping\ndata: {\"time\":\"%s\"}\n\n", time.Now().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

// splitPath splits a URL path into segments.
func splitPath(path string) []string {
	var parts []string
	for _, p := range split(path, '/') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func split(s string, sep rune) []string {
	var parts []string
	var current []rune
	for _, r := range s {
		if r == sep {
			if len(current) > 0 {
				parts = append(parts, string(current))
				current = nil
			}
		} else {
			current = append(current, r)
		}
	}
	if len(current) > 0 {
		parts = append(parts, string(current))
	}
	return parts
}
