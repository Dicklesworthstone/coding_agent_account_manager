package api

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

// Handlers provides the business logic for API endpoints.
type Handlers struct {
	vault       *authfile.Vault
	healthStore *health.Storage
	db          *caamdb.DB
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(vault *authfile.Vault, healthStore *health.Storage, db *caamdb.DB) *Handlers {
	return &Handlers{
		vault:       vault,
		healthStore: healthStore,
		db:          db,
	}
}

// Event represents a server-sent event.
type Event struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// StatusResponse is the response for GET /status.
type StatusResponse struct {
	Version   string       `json:"version"`
	Timestamp string       `json:"timestamp"`
	Tools     []ToolStatus `json:"tools"`
}

// ToolStatus represents the status of a single tool.
type ToolStatus struct {
	Tool          string             `json:"tool"`
	LoggedIn      bool               `json:"logged_in"`
	ActiveProfile string             `json:"active_profile,omitempty"`
	Health        *HealthStatus      `json:"health,omitempty"`
	Identity      *identity.Identity `json:"identity,omitempty"`
}

// HealthStatus represents profile health.
type HealthStatus struct {
	Status            string `json:"status"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	ErrorCount        int    `json:"error_count"`
	CooldownRemaining string `json:"cooldown_remaining,omitempty"`
}

// ProfilesResponse is the response for GET /profiles.
type ProfilesResponse struct {
	Profiles []ProfileInfo `json:"profiles"`
	Count    int           `json:"count"`
}

// ProfileInfo represents a profile.
type ProfileInfo struct {
	Tool     string             `json:"tool"`
	Name     string             `json:"name"`
	Active   bool               `json:"active"`
	System   bool               `json:"system"`
	Health   *HealthStatus      `json:"health,omitempty"`
	Identity *identity.Identity `json:"identity,omitempty"`
}

// UsageResponse is the response for GET /usage.
type UsageResponse struct {
	Tool   string       `json:"tool,omitempty"`
	Period string       `json:"period"`
	Usage  []UsageEntry `json:"usage"`
}

// UsageEntry represents usage for a profile.
type UsageEntry struct {
	Tool       string `json:"tool"`
	Profile    string `json:"profile"`
	TotalCalls int    `json:"total_calls"`
	ErrorCount int    `json:"error_count"`
	LastUsed   string `json:"last_used,omitempty"`
}

// CoordinatorsResponse is the response for GET /coordinators.
type CoordinatorsResponse struct {
	Coordinators []CoordinatorStatus `json:"coordinators"`
}

// CoordinatorStatus represents coordinator health.
type CoordinatorStatus struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
	Backend  string `json:"backend,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
}

// ActivateRequest is the request for POST /actions/activate.
type ActivateRequest struct {
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	Force   bool   `json:"force,omitempty"`
}

// ActivateResponse is the response for POST /actions/activate.
type ActivateResponse struct {
	Success bool   `json:"success"`
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	Message string `json:"message,omitempty"`
}

// BackupRequest is the request for POST /actions/backup.
type BackupRequest struct {
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
}

// BackupResponse is the response for POST /actions/backup.
type BackupResponse struct {
	Success bool   `json:"success"`
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

// Tools supported for auth file swapping.
var tools = map[string]func() authfile.AuthFileSet{
	"codex":  authfile.CodexAuthFiles,
	"claude": authfile.ClaudeAuthFiles,
	"gemini": authfile.GeminiAuthFiles,
}

// GetStatus returns overall caam status.
func (h *Handlers) GetStatus() (*StatusResponse, error) {
	resp := &StatusResponse{
		Version:   "1.0.0",
		Timestamp: time.Now().Format(time.RFC3339),
		Tools:     []ToolStatus{},
	}

	for tool, getFileSet := range tools {
		fileSet := getFileSet()
		hasAuth := authfile.HasAuthFiles(fileSet)

		ts := ToolStatus{
			Tool:     tool,
			LoggedIn: hasAuth,
		}

		if hasAuth && h.vault != nil {
			activeProfile, err := h.vault.ActiveProfile(fileSet)
			if err == nil && activeProfile != "" {
				ts.ActiveProfile = activeProfile
				ts.Health = h.getProfileHealth(tool, activeProfile)
				ts.Identity = h.getProfileIdentity(tool, activeProfile)
			}
		}

		resp.Tools = append(resp.Tools, ts)
	}

	return resp, nil
}

// GetProfiles returns profiles, optionally filtered by tool.
func (h *Handlers) GetProfiles(tool string) (*ProfilesResponse, error) {
	resp := &ProfilesResponse{
		Profiles: []ProfileInfo{},
	}

	if tool != "" {
		getFileSet, ok := tools[tool]
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tool)
		}
		if h.vault == nil {
			return nil, fmt.Errorf("vault not available")
		}
		profiles, err := h.vault.List(tool)
		if err != nil {
			return nil, err
		}

		fileSet := getFileSet()
		activeProfile, _ := h.vault.ActiveProfile(fileSet)

		for _, name := range profiles {
			pi := ProfileInfo{
				Tool:     tool,
				Name:     name,
				Active:   name == activeProfile,
				System:   authfile.IsSystemProfile(name),
				Health:   h.getProfileHealth(tool, name),
				Identity: h.getProfileIdentity(tool, name),
			}
			resp.Profiles = append(resp.Profiles, pi)
		}
	} else {
		if h.vault == nil {
			return nil, fmt.Errorf("vault not available")
		}
		allProfiles, err := h.vault.ListAll()
		if err != nil {
			return nil, err
		}

		for tool, profiles := range allProfiles {
			getFileSet, ok := tools[tool]
			if !ok {
				continue
			}
			fileSet := getFileSet()
			activeProfile, _ := h.vault.ActiveProfile(fileSet)

			for _, name := range profiles {
				pi := ProfileInfo{
					Tool:     tool,
					Name:     name,
					Active:   name == activeProfile,
					System:   authfile.IsSystemProfile(name),
					Health:   h.getProfileHealth(tool, name),
					Identity: h.getProfileIdentity(tool, name),
				}
				resp.Profiles = append(resp.Profiles, pi)
			}
		}
	}

	resp.Count = len(resp.Profiles)
	return resp, nil
}

// GetProfile returns a single profile.
func (h *Handlers) GetProfile(tool, name string) (*ProfileInfo, error) {
	if _, ok := tools[tool]; !ok {
		return nil, fmt.Errorf("unknown tool: %s", tool)
	}
	if h.vault == nil {
		return nil, fmt.Errorf("vault not available")
	}
	profiles, err := h.vault.List(tool)
	if err != nil {
		return nil, err
	}

	found := false
	for _, p := range profiles {
		if p == name {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("profile not found: %s/%s", tool, name)
	}

	fileSet := tools[tool]()
	activeProfile, _ := h.vault.ActiveProfile(fileSet)

	return &ProfileInfo{
		Tool:     tool,
		Name:     name,
		Active:   name == activeProfile,
		System:   authfile.IsSystemProfile(name),
		Health:   h.getProfileHealth(tool, name),
		Identity: h.getProfileIdentity(tool, name),
	}, nil
}

// DeleteProfile deletes a profile.
func (h *Handlers) DeleteProfile(tool, name string) error {
	if _, ok := tools[tool]; !ok {
		return fmt.Errorf("unknown tool: %s", tool)
	}
	if name == "" {
		return fmt.Errorf("profile is required")
	}
	if h.vault == nil {
		return fmt.Errorf("vault not available")
	}
	if authfile.IsSystemProfile(name) {
		return fmt.Errorf("cannot delete system profile: %s/%s", tool, name)
	}
	return h.vault.Delete(tool, name)
}

// GetUsage returns usage statistics.
func (h *Handlers) GetUsage(tool string) (*UsageResponse, error) {
	resp := &UsageResponse{
		Tool:   tool,
		Period: "1h",
		Usage:  []UsageEntry{},
	}

	if h.healthStore == nil {
		return resp, nil
	}
	if h.vault == nil {
		return nil, fmt.Errorf("vault not available")
	}

	// Get all health data
	toolsToCheck := []string{"codex", "claude", "gemini"}
	if tool != "" {
		toolsToCheck = []string{tool}
	}

	for _, t := range toolsToCheck {
		profiles, err := h.vault.List(t)
		if err != nil {
			continue
		}

		for _, name := range profiles {
			ph, err := h.healthStore.GetProfile(t, name)
			if err != nil || ph == nil {
				continue
			}

			entry := UsageEntry{
				Tool:       t,
				Profile:    name,
				TotalCalls: 0, // Not tracked in ProfileHealth
				ErrorCount: ph.ErrorCount1h,
			}
			if !ph.LastChecked.IsZero() {
				entry.LastUsed = ph.LastChecked.Format(time.RFC3339)
			}
			resp.Usage = append(resp.Usage, entry)
		}
	}

	return resp, nil
}

// GetCoordinators returns coordinator status.
func (h *Handlers) GetCoordinators() (*CoordinatorsResponse, error) {
	// For now, return empty - coordinator discovery could be added later
	return &CoordinatorsResponse{
		Coordinators: []CoordinatorStatus{},
	}, nil
}

// Activate activates a profile.
func (h *Handlers) Activate(req ActivateRequest) (*ActivateResponse, error) {
	getFileSet, ok := tools[req.Tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", req.Tool)
	}
	if req.Profile == "" {
		return nil, fmt.Errorf("profile is required")
	}
	if h.vault == nil {
		return nil, fmt.Errorf("vault not available")
	}

	fileSet := getFileSet()

	// Check cooldown if not forcing
	if !req.Force && h.db != nil {
		now := time.Now()
		cooldown, err := h.db.ActiveCooldown(req.Tool, req.Profile, now)
		if err == nil && cooldown != nil {
			remaining := cooldown.CooldownUntil.Sub(now)
			if remaining > 0 {
				return &ActivateResponse{
					Success: false,
					Tool:    req.Tool,
					Profile: req.Profile,
					Message: fmt.Sprintf("profile in cooldown (%s remaining), use force to override", formatDuration(remaining)),
				}, nil
			}
		}
	}

	// Restore the profile (activating it)
	if err := h.vault.Restore(fileSet, req.Profile); err != nil {
		return nil, fmt.Errorf("activate failed: %w", err)
	}

	return &ActivateResponse{
		Success: true,
		Tool:    req.Tool,
		Profile: req.Profile,
		Message: fmt.Sprintf("activated %s/%s", req.Tool, req.Profile),
	}, nil
}

// Backup backs up current auth to a profile.
func (h *Handlers) Backup(req BackupRequest) (*BackupResponse, error) {
	getFileSet, ok := tools[req.Tool]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", req.Tool)
	}
	if req.Profile == "" {
		return nil, fmt.Errorf("profile is required")
	}
	if h.vault == nil {
		return nil, fmt.Errorf("vault not available")
	}

	fileSet := getFileSet()

	if !authfile.HasAuthFiles(fileSet) {
		return &BackupResponse{
			Success: false,
			Tool:    req.Tool,
			Profile: req.Profile,
			Message: fmt.Sprintf("no auth files found for %s", req.Tool),
		}, nil
	}

	if err := h.vault.Backup(fileSet, req.Profile); err != nil {
		return nil, fmt.Errorf("backup failed: %w", err)
	}

	return &BackupResponse{
		Success: true,
		Tool:    req.Tool,
		Profile: req.Profile,
		Path:    h.vault.ProfilePath(req.Tool, req.Profile),
		Message: fmt.Sprintf("backed up %s to %s", req.Tool, req.Profile),
	}, nil
}

// getProfileHealth returns health status for a profile.
func (h *Handlers) getProfileHealth(tool, name string) *HealthStatus {
	if h.healthStore == nil {
		return nil
	}

	ph, err := h.healthStore.GetProfile(tool, name)
	if err != nil || ph == nil {
		return nil
	}

	status := health.CalculateStatus(ph)
	hs := &HealthStatus{
		Status:     status.String(),
		ErrorCount: ph.ErrorCount1h,
	}

	if !ph.TokenExpiresAt.IsZero() {
		hs.ExpiresAt = ph.TokenExpiresAt.Format(time.RFC3339)
	}

	// Check cooldown
	if h.db != nil {
		now := time.Now()
		cooldown, err := h.db.ActiveCooldown(tool, name, now)
		if err == nil && cooldown != nil {
			remaining := cooldown.CooldownUntil.Sub(now)
			if remaining > 0 {
				hs.CooldownRemaining = formatDuration(remaining)
			}
		}
	}

	return hs
}

// getProfileIdentity returns identity info for a profile.
func (h *Handlers) getProfileIdentity(tool, name string) *identity.Identity {
	if h.vault == nil {
		return nil
	}

	vaultPath := h.vault.ProfilePath(tool, name)
	var id *identity.Identity
	var err error

	switch tool {
	case "codex":
		id, err = identity.ExtractFromCodexAuth(vaultPath + "/auth.json")
	case "claude":
		id, err = identity.ExtractFromClaudeCredentials(vaultPath + "/.credentials.json")
	case "gemini":
		id, err = identity.ExtractFromGeminiConfig(vaultPath + "/settings.json")
		if err != nil {
			id, err = identity.ExtractFromGeminiConfig(vaultPath + "/oauth_credentials.json")
		}
	}

	if err != nil {
		return nil
	}

	// Identity fields are already safe for API responses
	// (no sensitive tokens are included in the Identity struct)

	return id
}

// formatDuration formats a duration for display.
func formatDuration(d time.Duration) string {
	if d >= time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	mins := int(d.Minutes())
	if mins < 1 {
		return "<1m"
	}
	return fmt.Sprintf("%dm", mins)
}
