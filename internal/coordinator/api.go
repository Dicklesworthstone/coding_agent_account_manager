package coordinator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// APIServer exposes the coordinator's HTTP API.
type APIServer struct {
	coordinator *Coordinator
	server      *http.Server
	logger      *slog.Logger
	token       string
}

// DefaultBindAddress keeps the coordinator API reachable only from the host
// itself; agents reach it through an SSH tunnel.
const DefaultBindAddress = "127.0.0.1"

// ListenAddress joins a bind host and port into a listen address.
func ListenAddress(bind string, port int) string {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		bind = DefaultBindAddress
	}
	return net.JoinHostPort(bind, strconv.Itoa(port))
}

// IsLoopbackBind reports whether a bind host only accepts local connections.
func IsLoopbackBind(bind string) bool {
	bind = strings.TrimSpace(bind)
	if bind == "" || strings.EqualFold(bind, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ValidateListenSecurity refuses to expose the API beyond loopback without a
// shared secret: the API hands out OAuth URLs and accepts login codes.
func ValidateListenSecurity(bind, token string) error {
	if !IsLoopbackBind(bind) && strings.TrimSpace(token) == "" {
		return fmt.Errorf("coordinator API bound to %q requires an auth token (set --auth-token, auth_token, or CAAM_COORDINATOR_TOKEN)", bind)
	}
	return nil
}

// NewAPIServer creates a new API server listening on bind:port.
func NewAPIServer(coordinator *Coordinator, bind string, port int, logger *slog.Logger) *APIServer {
	if logger == nil {
		logger = slog.Default()
	}

	api := &APIServer{
		coordinator: coordinator,
		logger:      logger,
		token:       "",
	}
	if coordinator != nil {
		api.token = strings.TrimSpace(coordinator.config.AuthToken)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.handleHealth)
	mux.HandleFunc("GET /status", api.authMiddleware(api.handleStatus))
	mux.HandleFunc("GET /auth/pending", api.authMiddleware(api.handleGetPending))
	mux.HandleFunc("POST /auth/complete", api.authMiddleware(api.handleComplete))
	mux.HandleFunc("POST /auth/submit", api.authMiddleware(api.handleComplete)) // alias
	mux.HandleFunc("GET /panes", api.authMiddleware(api.handleListPanes))

	api.server = &http.Server{
		Addr:         ListenAddress(bind, port),
		Handler:      api.withLogging(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return api
}

func (a *APIServer) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	token := strings.TrimSpace(a.token)
	if token == "" {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		provided := strings.TrimSpace(auth[len(prefix):])
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			a.logger.Warn("rejected coordinator API request",
				"path", r.URL.Path,
				"remote", r.RemoteAddr,
				"reason", "invalid_token")
			writeAPIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// Addr returns the configured listen address.
func (a *APIServer) Addr() string {
	return a.server.Addr
}

// Start begins serving the API.
func (a *APIServer) Start() error {
	a.logger.Info("starting API server", "addr", a.server.Addr)
	return a.server.ListenAndServe()
}

// Serve serves the API on an existing listener.
func (a *APIServer) Serve(listener net.Listener) error {
	a.logger.Info("starting API server", "addr", listener.Addr().String())
	return a.server.Serve(listener)
}

// Shutdown gracefully stops the server.
func (a *APIServer) Shutdown(ctx context.Context) error {
	return a.server.Shutdown(ctx)
}

func (a *APIServer) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start))
	})
}

// HealthResponse is the response from /health endpoint.
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Backend   string    `json:"backend"`
	Uptime    string    `json:"uptime,omitempty"`
}

func (a *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:    "ok",
		Timestamp: time.Now(),
		Backend:   a.coordinator.Backend(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// StatusResponse is the response from /status endpoint.
type StatusResponse struct {
	Running        bool                 `json:"running"`
	Backend        string               `json:"backend"`
	PaneCount      int                  `json:"pane_count"`
	PendingAuths   int                  `json:"pending_auths"`
	Panes          []PaneStatusResponse `json:"panes"`
	PendingDetails []*AuthRequest       `json:"pending_details,omitempty"`
}

// PaneStatusResponse is the status of a single pane.
type PaneStatusResponse struct {
	PaneID       int       `json:"pane_id"`
	State        string    `json:"state"`
	StateEntered time.Time `json:"state_entered"`
	RequestID    string    `json:"request_id,omitempty"`
	Account      string    `json:"account,omitempty"`
	Error        string    `json:"error,omitempty"`
	Retries      int       `json:"retries,omitempty"` // login retries used this rate-limit episode
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	trackers := a.coordinator.GetTrackers()
	pending := a.coordinator.GetPendingRequests()

	panes := make([]PaneStatusResponse, 0, len(trackers))
	for _, t := range trackers {
		t.mu.RLock()
		panes = append(panes, PaneStatusResponse{
			PaneID:       t.PaneID,
			State:        t.State.String(),
			StateEntered: t.StateEntered,
			RequestID:    t.RequestID,
			Account:      t.UsedAccount,
			Error:        t.ErrorMessage,
			Retries:      t.RetryCount,
		})
		t.mu.RUnlock()
	}

	resp := StatusResponse{
		Running:        true,
		Backend:        a.coordinator.Backend(),
		PaneCount:      len(trackers),
		PendingAuths:   len(pending),
		Panes:          panes,
		PendingDetails: pending,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleGetPending serves agents: fetching a request claims it, which
// starts its AuthTimeout.
func (a *APIServer) handleGetPending(w http.ResponseWriter, r *http.Request) {
	pending := a.coordinator.ClaimPendingRequests()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pending)
}

// CompleteRequest is the request body for /auth/complete.
type CompleteRequest struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Account   string `json:"account"`
	Error     string `json:"error,omitempty"`
}

// CompleteAck acknowledges an accepted (or identically redelivered) response.
type CompleteAck struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id"`
}

// maxCompleteBody bounds /auth/complete bodies; a response is a few hundred bytes.
const maxCompleteBody = 64 << 10

func (a *APIServer) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req CompleteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCompleteBody)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.RequestID) == "" {
		writeAPIError(w, http.StatusBadRequest, "request_id required")
		return
	}

	if err := a.coordinator.ReceiveAuthResponse(AuthResponse(req)); err != nil {
		status := completeErrorStatus(err)
		a.logger.Warn("auth response rejected",
			"request_id", req.RequestID,
			"status", status,
			"error", err)
		writeAPIError(w, status, err.Error())
		return
	}

	a.logger.Info("auth response acknowledged",
		"request_id", req.RequestID,
		"account", req.Account)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CompleteAck{Status: "accepted", RequestID: req.RequestID})
}

// completeErrorStatus maps acceptance errors to HTTP statuses. Agents retry
// only transport failures and 5xx; every status here is final.
func completeErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalidAuthResponse):
		return http.StatusBadRequest
	case errors.Is(err, ErrAuthResponseConflict):
		return http.StatusConflict
	case errors.Is(err, ErrAuthRequestClosed):
		return http.StatusGone
	case errors.Is(err, ErrUnknownAuthRequest):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": msg})
}

func (a *APIServer) handleListPanes(w http.ResponseWriter, r *http.Request) {
	panes, err := a.coordinator.paneClient.ListPanes(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(panes)
}
