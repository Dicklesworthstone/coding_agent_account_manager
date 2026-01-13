package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// APIServer exposes the coordinator's HTTP API.
type APIServer struct {
	coordinator *Coordinator
	server      *http.Server
	logger      *slog.Logger
}

// NewAPIServer creates a new API server.
func NewAPIServer(coordinator *Coordinator, port int, logger *slog.Logger) *APIServer {
	if logger == nil {
		logger = slog.Default()
	}

	api := &APIServer{
		coordinator: coordinator,
		logger:      logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", api.handleStatus)
	mux.HandleFunc("GET /auth/pending", api.handleGetPending)
	mux.HandleFunc("POST /auth/complete", api.handleComplete)
	mux.HandleFunc("GET /panes", api.handleListPanes)

	api.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      api.withLogging(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return api
}

// Start begins serving the API.
func (a *APIServer) Start() error {
	a.logger.Info("starting API server", "addr", a.server.Addr)
	return a.server.ListenAndServe()
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

// StatusResponse is the response from /status endpoint.
type StatusResponse struct {
	Running        bool                    `json:"running"`
	PaneCount      int                     `json:"pane_count"`
	PendingAuths   int                     `json:"pending_auths"`
	Panes          []PaneStatusResponse    `json:"panes"`
	PendingDetails []*AuthRequest          `json:"pending_details,omitempty"`
}

// PaneStatusResponse is the status of a single pane.
type PaneStatusResponse struct {
	PaneID       int       `json:"pane_id"`
	State        string    `json:"state"`
	StateEntered time.Time `json:"state_entered"`
	RequestID    string    `json:"request_id,omitempty"`
	Account      string    `json:"account,omitempty"`
	Error        string    `json:"error,omitempty"`
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
		})
		t.mu.RUnlock()
	}

	resp := StatusResponse{
		Running:        true,
		PaneCount:      len(trackers),
		PendingAuths:   len(pending),
		Panes:          panes,
		PendingDetails: pending,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (a *APIServer) handleGetPending(w http.ResponseWriter, r *http.Request) {
	pending := a.coordinator.GetPendingRequests()

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

func (a *APIServer) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req CompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.RequestID == "" {
		http.Error(w, "request_id required", http.StatusBadRequest)
		return
	}

	resp := AuthResponse{
		RequestID: req.RequestID,
		Code:      req.Code,
		Account:   req.Account,
		Error:     req.Error,
	}

	if err := a.coordinator.ReceiveAuthResponse(resp); err != nil {
		a.logger.Error("failed to process auth response",
			"request_id", req.RequestID,
			"error", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	a.logger.Info("auth response received",
		"request_id", req.RequestID,
		"account", req.Account)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (a *APIServer) handleListPanes(w http.ResponseWriter, r *http.Request) {
	panes, err := a.coordinator.paneClient.ListPanes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(panes)
}
