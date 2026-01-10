// Package handoff provides login handlers for PTY-based profile switching.
// It enables the smart session handoff feature where we can inject login
// commands into a running CLI session to switch accounts.
package handoff

import (
	"sync"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
)

// LoginHandler defines the interface for provider-specific login handling.
// Each provider (Claude, Codex, Gemini) has different login flows and
// success/failure patterns.
type LoginHandler interface {
	// Provider returns the provider ID this handler supports (e.g., "claude").
	Provider() string

	// LoginCommand returns the command to trigger login (e.g., "/login").
	LoginCommand() string

	// TriggerLogin injects the login command into the PTY.
	TriggerLogin(ctrl pty.Controller) error

	// IsLoginInProgress returns true if the output indicates
	// a login flow has started (e.g., waiting for browser auth).
	IsLoginInProgress(output string) bool

	// IsLoginComplete returns true if the output indicates
	// the login succeeded.
	IsLoginComplete(output string) bool

	// IsLoginFailed returns true if the output indicates
	// login failed, along with an error message extracted from output.
	IsLoginFailed(output string) (failed bool, message string)

	// ExpectedPatterns returns regex patterns for common login states.
	// Keys: "progress", "success", "failure"
	ExpectedPatterns() map[string]string
}

// Registry manages provider-to-handler mappings.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]LoginHandler
}

// NewRegistry creates a new handler registry with default handlers.
func NewRegistry() *Registry {
	r := &Registry{
		handlers: make(map[string]LoginHandler),
	}

	// Register default handlers
	r.Register(&ClaudeLoginHandler{})
	r.Register(&CodexLoginHandler{})
	r.Register(&GeminiLoginHandler{})

	return r
}

// Register adds a handler to the registry.
func (r *Registry) Register(h LoginHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[h.Provider()] = h
}

// Get returns the handler for a provider, or nil if not found.
func (r *Registry) Get(provider string) LoginHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.handlers[provider]
}

// Providers returns a list of registered provider IDs.
func (r *Registry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]string, 0, len(r.handlers))
	for p := range r.handlers {
		providers = append(providers, p)
	}
	return providers
}

// DefaultRegistry is the global handler registry.
var DefaultRegistry = NewRegistry()

// GetHandler returns the handler for a provider from the default registry.
func GetHandler(provider string) LoginHandler {
	return DefaultRegistry.Get(provider)
}

// LoginState represents the current state of a login attempt.
type LoginState int

const (
	// LoginStateUnknown indicates we don't know the login state.
	LoginStateUnknown LoginState = iota
	// LoginStateIdle indicates no login is in progress.
	LoginStateIdle
	// LoginStateInProgress indicates login is waiting for user action.
	LoginStateInProgress
	// LoginStateComplete indicates login succeeded.
	LoginStateComplete
	// LoginStateFailed indicates login failed.
	LoginStateFailed
)

// String returns a human-readable state name.
func (s LoginState) String() string {
	switch s {
	case LoginStateIdle:
		return "idle"
	case LoginStateInProgress:
		return "in_progress"
	case LoginStateComplete:
		return "complete"
	case LoginStateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// DetermineState analyzes output using a handler to determine login state.
func DetermineState(handler LoginHandler, output string) LoginState {
	if handler == nil {
		return LoginStateUnknown
	}

	// Check in order of specificity
	if failed, _ := handler.IsLoginFailed(output); failed {
		return LoginStateFailed
	}
	if handler.IsLoginComplete(output) {
		return LoginStateComplete
	}
	if handler.IsLoginInProgress(output) {
		return LoginStateInProgress
	}

	return LoginStateIdle
}
