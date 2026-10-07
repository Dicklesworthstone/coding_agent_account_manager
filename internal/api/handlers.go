package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
)

// Handlers provides the business logic for API endpoints.
type Handlers struct {
	vault       *authfile.Vault
	healthStore *health.Storage
	db          *caamdb.DB

	// agentConfigPath overrides the auth agent config location (tests).
	agentConfigPath string
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(vault *authfile.Vault, healthStore *health.Storage, db *caamdb.DB) *Handlers {
	if vault != nil && healthStore != nil {
		healthStore.SetVaultPath(vault.BasePath())
	}
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
	health.Signals
	Status            string `json:"status"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	ErrorCount        int    `json:"error_count"`
	CooldownRemaining string `json:"cooldown_remaining,omitempty"`
	Renewable         bool   `json:"renewable"`
	Recommendation    string `json:"recommendation,omitempty"`
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
	Tool      string       `json:"tool,omitempty"`
	Period    string       `json:"period"`
	Since     string       `json:"since"`
	Until     string       `json:"until"`
	Available bool         `json:"available"`
	Usage     []UsageEntry `json:"usage"`
}

// UsageEntry represents usage for a profile.
type UsageEntry struct {
	Tool          string `json:"tool"`
	Profile       string `json:"profile"`
	Activations   int    `json:"activations"`
	ErrorCount    int    `json:"error_count"`
	ActiveSeconds int64  `json:"active_seconds"`
	LastActivity  string `json:"last_activity,omitempty"`
}

var errInvalidUsageQuery = errors.New("invalid usage query")

func usagePeriod(period string) (string, time.Duration, error) {
	if period == "" {
		period = "24h"
	}
	periods := map[string]time.Duration{
		"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
	}
	duration, ok := periods[period]
	if !ok {
		return "", 0, fmt.Errorf("%w: period must be 1h, 24h, 7d, or 30d", errInvalidUsageQuery)
	}
	return period, duration, nil
}

// CoordinatorsResponse is the response for GET /coordinators.
type CoordinatorsResponse struct {
	Coordinators []CoordinatorStatus `json:"coordinators"`
}

// CoordinatorStatus represents coordinator health.
//
// Status is one of: healthy, unreachable, pending (the agent has not polled
// it yet), unknown (the running agent does not report it), agent_running
// (single-coordinator agent, no per-coordinator health), or
// agent_not_running.
type CoordinatorStatus struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Endpoint    string `json:"endpoint"`
	Transport   string `json:"transport"`
	Status      string `json:"status"`
	Backend     string `json:"backend,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
	LastChecked string `json:"last_checked,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ActivateRequest is the request for POST /actions/activate.
type ActivateRequest struct {
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	Force   bool   `json:"force,omitempty"`
}

// ActivateResponse is the response for POST /actions/activate.
type ActivateResponse struct {
	Success              bool     `json:"success"`
	Tool                 string   `json:"tool"`
	Profile              string   `json:"profile"`
	Message              string   `json:"message,omitempty"`
	KeptLive             bool     `json:"kept_live,omitempty"`
	PreviousProfile      string   `json:"previous_profile,omitempty"`
	AutoBackup           string   `json:"auto_backup,omitempty"`
	ResnapshottedProfile string   `json:"resnapshotted_profile,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
}

// BackupRequest is the request for POST /actions/backup.
type BackupRequest struct {
	Tool      string `json:"tool"`
	Profile   string `json:"profile"`
	Overwrite bool   `json:"overwrite,omitempty"`
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
	"codex":    authfile.CodexAuthFiles,
	"claude":   authfile.ClaudeAuthFiles,
	"gemini":   authfile.GeminiAuthFiles,
	"agy":      authfile.AntigravityAuthFiles,
	"cursor":   authfile.CursorAuthFiles,
	"grok":     authfile.GrokAuthFiles,
	"opencode": authfile.OpenCodeAuthFiles,
}

// GetStatus returns overall caam status.
func (h *Handlers) GetStatus() (*StatusResponse, error) {
	resp := &StatusResponse{
		Version:   version.Short(),
		Timestamp: time.Now().Format(time.RFC3339),
		Tools:     []ToolStatus{},
	}

	for tool, getFileSet := range tools {
		fileSet := getFileSet()
		hasAuth := authfile.HasAuthFilesReadOnly(fileSet)

		ts := ToolStatus{
			Tool:     tool,
			LoggedIn: hasAuth,
		}

		if hasAuth && h.vault != nil {
			activeProfile, err := h.vault.CurrentProfile(fileSet)
			if err == nil && activeProfile != "" {
				ts.ActiveProfile = activeProfile
				ts.Health = h.getProfileHealth(tool, activeProfile)
				ts.Identity = h.getProfileIdentity(tool, activeProfile)
			}
		}

		resp.Tools = append(resp.Tools, ts)
	}
	sort.Slice(resp.Tools, func(i, j int) bool { return resp.Tools[i].Tool < resp.Tools[j].Tool })

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
		activeProfile, _ := h.vault.CurrentProfile(fileSet)

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
			activeProfile, _ := h.vault.CurrentProfile(fileSet)

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

	sort.Slice(resp.Profiles, func(i, j int) bool {
		if resp.Profiles[i].Tool != resp.Profiles[j].Tool {
			return resp.Profiles[i].Tool < resp.Profiles[j].Tool
		}
		return resp.Profiles[i].Name < resp.Profiles[j].Name
	})
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
	activeProfile, _ := h.vault.CurrentProfile(fileSet)

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

// ActivityResponse is the response for GET /activity.
type ActivityResponse struct {
	Available bool            `json:"available"`
	Events    []ActivityEvent `json:"events"`
	Cooldowns []CooldownInfo  `json:"cooldowns"`
}

// ActivityEvent is one entry of caam's activity log: an activation, switch,
// refresh, login, or error.
type ActivityEvent struct {
	Timestamp       string         `json:"timestamp"`
	Type            string         `json:"type"`
	Tool            string         `json:"tool"`
	Profile         string         `json:"profile"`
	Details         map[string]any `json:"details,omitempty"`
	DurationSeconds int64          `json:"duration_seconds,omitempty"`
}

// CooldownInfo is a profile resting after it hit a limit.
type CooldownInfo struct {
	Tool    string `json:"tool"`
	Profile string `json:"profile"`
	HitAt   string `json:"hit_at"`
	Until   string `json:"until"`
	Notes   string `json:"notes,omitempty"`
}

// maxActivity bounds one /activity response.
const maxActivity = 200

// GetActivity returns the most recent activity log entries (newest first)
// and the profiles currently cooling down after a limit.
func (h *Handlers) GetActivity(limit int) (*ActivityResponse, error) {
	resp := &ActivityResponse{Events: []ActivityEvent{}, Cooldowns: []CooldownInfo{}}
	if h.db == nil {
		return resp, nil
	}
	if limit <= 0 || limit > maxActivity {
		limit = 50
	}
	events, err := h.db.ListRecentEvents(limit)
	if err != nil {
		return nil, fmt.Errorf("read activity: %w", err)
	}
	cooldowns, err := h.db.ListActiveCooldowns(time.Now())
	if err != nil {
		return nil, fmt.Errorf("read cooldowns: %w", err)
	}

	// A transition detail may name only a profile already disclosed by these
	// records. Merely looking like a profile name does not make an arbitrary
	// diagnostic string safe to publish: credentials can have the same syntax.
	profiles := make(map[[2]string]bool, len(events)+len(cooldowns))
	for _, e := range events {
		profiles[[2]string{e.Provider, e.ProfileName}] = true
	}
	for _, c := range cooldowns {
		profiles[[2]string{c.Provider, c.ProfileName}] = true
	}
	resp.Available = true
	for _, e := range events {
		resp.Events = append(resp.Events, ActivityEvent{
			Timestamp:       e.Timestamp.UTC().Format(time.RFC3339),
			Type:            e.Type,
			Tool:            e.Provider,
			Profile:         e.ProfileName,
			Details:         redactDetails(e.Details, e.Provider, profiles),
			DurationSeconds: int64(e.Duration / time.Second),
		})
	}
	for _, c := range cooldowns {
		resp.Cooldowns = append(resp.Cooldowns, CooldownInfo{
			Tool:    c.Provider,
			Profile: c.ProfileName,
			HitAt:   c.HitAt.UTC().Format(time.RFC3339),
			Until:   c.CooldownUntil.UTC().Format(time.RFC3339),
			Notes:   activityCooldownNote(c.Notes),
		})
	}
	return resp, nil
}

// redactDetails exposes only bounded metadata with known semantics. Diagnostic
// text, nested values, and arbitrary provider error codes may contain credentials
// even when their keys do not mention tokens or secrets.
func redactDetails(details map[string]any, provider string, profiles map[[2]string]bool) map[string]any {
	if len(details) == 0 {
		return nil
	}
	out := make(map[string]any)
	for _, key := range []string{"from", "previous_profile", "switched_to", "operation", "reason", "selection_source", "algorithm"} {
		value, ok := details[key].(string)
		if !ok || value == "" || len(value) > 256 {
			continue
		}
		safe := false
		switch key {
		case "from", "previous_profile", "switched_to":
			safe = profiles[[2]string{provider, value}]
		case "operation":
			switch value {
			case "refresh", "session":
				safe = true
			}
		case "reason":
			switch value {
			case "rate_limit", "exchange_failed", "refresh_token_reused",
				"refresh_token_invalidated", "refresh_token_expired", "invalid_grant",
				"refresh_rejected_http_400", "refresh_rejected_http_401", "refresh_rejected_http_403":
				safe = true
			}
		case "selection_source":
			switch value {
			case "next", "rotation", "rotation (default in cooldown)":
				safe = true
			}
		case "algorithm":
			switch value {
			case "smart", "round_robin", "random":
				safe = true
			}
		}
		if safe {
			out[key] = value
		}
	}
	return out
}

// Cooldown notes can include user input or raw provider diagnostics. Publish
// only recognized application messages, never arbitrary stored text.
func activityCooldownNote(note string) string {
	switch note {
	case "session limit":
		return "session limit"
	case "auto-detected via SmartRunner":
		return "automatically detected rate limit"
	case "manual via robot act":
		return "manually recorded cooldown"
	default:
		return ""
	}
}

// GetUsage returns recorded CAAM activity, never invented provider API-call
// counts or quota measurements. Missing storage is explicitly unavailable;
// an available empty interval means no activity was recorded in that interval.
func (h *Handlers) GetUsage(ctx context.Context, tool, period string) (*UsageResponse, error) {
	if tool != "" {
		if _, ok := tools[tool]; !ok {
			return nil, fmt.Errorf("%w: unknown tool: %s", errInvalidUsageQuery, tool)
		}
	}
	period, duration, err := usagePeriod(period)
	if err != nil {
		return nil, err
	}
	until := time.Now().UTC().Truncate(time.Second)
	since := until.Add(-duration)
	resp := &UsageResponse{
		Tool:   tool,
		Period: period,
		Since:  since.Format(time.RFC3339),
		Until:  until.Format(time.RFC3339),
		Usage:  []UsageEntry{},
	}

	if h.db == nil {
		return resp, nil
	}
	rows, err := h.db.SummarizeActivity(ctx, tool, since, until)
	if err != nil {
		return nil, err
	}
	resp.Available = true
	for _, row := range rows {
		if _, ok := tools[row.Provider]; !ok || authfile.IsSystemProfile(row.ProfileName) {
			continue
		}
		entry := UsageEntry{
			Tool: row.Provider, Profile: row.ProfileName, Activations: row.Activations,
			ErrorCount: row.Errors, ActiveSeconds: row.ActiveSeconds,
		}
		if !row.LastActivity.IsZero() {
			entry.LastActivity = row.LastActivity.Format(time.RFC3339)
		}
		resp.Usage = append(resp.Usage, entry)
	}

	return resp, nil
}

// GetCoordinators lists the distributed auth-recovery coordinators the local
// auth agent is configured for, with live health from the running agent when
// it answers. Tokens are never included.
func (h *Handlers) GetCoordinators() (*CoordinatorsResponse, error) {
	resp := &CoordinatorsResponse{Coordinators: []CoordinatorStatus{}}

	path := h.agentConfigPath
	if path == "" {
		path = agent.DefaultConfigPath()
	}
	fc, err := agent.LoadFileConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return resp, nil
		}
		return nil, err
	}
	port := fc.Port
	if port == 0 {
		port = agent.DefaultMultiConfig().Port
	}

	if len(fc.Coordinators) == 0 {
		url := fc.CoordinatorURL
		if url == "" {
			url = fc.Coordinator
		}
		if url == "" {
			return resp, nil
		}
		status := "agent_not_running"
		if h.agentAnswers(port, "/status") {
			status = "agent_running"
		}
		resp.Coordinators = append(resp.Coordinators, CoordinatorStatus{
			ID: "coordinator", Endpoint: coordinatorDisplayEndpoint(url), Transport: "direct", Status: status,
		})
		return resp, nil
	}

	live, running := h.agentCoordinatorHealth(port)
	for _, c := range fc.Coordinators {
		if c == nil {
			continue
		}
		st := CoordinatorStatus{
			ID:          c.Name,
			DisplayName: c.DisplayName,
			Endpoint:    coordinatorDisplayEndpoint(c.URL),
			Transport:   c.Transport(),
			Status:      "agent_not_running",
		}
		if c.SSH != nil {
			st.Endpoint += " via ssh " + c.SSH.Host
		}
		if running {
			l, ok := live[c.Name]
			switch {
			case !ok:
				st.Status = "unknown"
			case l.LastCheck.IsZero():
				st.Status = "pending"
			case l.IsHealthy:
				st.Status = "healthy"
				st.LastSeen = l.LastCheck.Format(time.RFC3339)
			default:
				st.Status = "unreachable"
				st.Error = coordinatorErrorSummary(l.LastError)
			}
			if !l.LastCheck.IsZero() {
				st.LastChecked = l.LastCheck.Format(time.RFC3339)
			}
		}
		resp.Coordinators = append(resp.Coordinators, st)
	}
	return resp, nil
}

// Display only the network origin. Configured URLs and transport errors may
// contain credentials in userinfo, paths, queries, or upstream response bodies.
func coordinatorDisplayEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "invalid coordinator URL"
	}
	return u.Scheme + "://" + u.Host
}

func coordinatorErrorSummary(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "connection refused"):
		return "connection refused"
	case strings.Contains(lower, "no such host"):
		return "coordinator hostname could not be resolved"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return "coordinator check timed out"
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "status 401"), strings.Contains(lower, "status 403"):
		return "coordinator authentication was rejected"
	default:
		return "coordinator check failed; inspect local auth-agent logs"
	}
}

// agentCoordinator is one entry of the auth agent's GET /coordinators.
type agentCoordinator struct {
	Name      string    `json:"name"`
	IsHealthy bool      `json:"is_healthy"`
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error"`
}

// agentCoordinatorHealth asks the local auth agent for its coordinators'
// health. It reports whether the agent answered.
func (h *Handlers) agentCoordinatorHealth(port int) (map[string]agentCoordinator, bool) {
	resp, err := h.agentClient().Get(fmt.Sprintf("http://127.0.0.1:%d/coordinators", port))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var list []agentCoordinator
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, false
	}
	byName := make(map[string]agentCoordinator, len(list))
	for _, c := range list {
		byName[c.Name] = c
	}
	return byName, true
}

func (h *Handlers) agentAnswers(port int, path string) bool {
	resp, err := h.agentClient().Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (h *Handlers) agentClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

// Activate activates a profile.
func (h *Handlers) Activate(req ActivateRequest) (*ActivateResponse, error) {
	req.Profile = strings.TrimSpace(req.Profile)
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

	spmConfig, err := config.LoadSPMConfig()
	if err != nil {
		return nil, fmt.Errorf("load activation safety settings: %w", err)
	}
	result, err := h.vault.Switch(fileSet, req.Profile, authfile.SwitchOptions{
		BackupMode:     spmConfig.Safety.AutoBackupBeforeSwitch,
		MaxAutoBackups: spmConfig.Safety.MaxAutoBackups,
	})
	if err != nil {
		return nil, fmt.Errorf("activate failed: %w", err)
	}
	message := fmt.Sprintf("activated %s/%s", req.Tool, req.Profile)
	if result.KeptLive {
		message = fmt.Sprintf("kept live credentials for %s/%s", req.Tool, req.Profile)
	}
	if h.db != nil && !result.KeptLive {
		if err := h.db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: req.Tool, ProfileName: req.Profile}); err != nil {
			result.Warnings = append(result.Warnings, "profile activated, but activity could not be recorded")
		}
	}

	return &ActivateResponse{
		Success:              true,
		Tool:                 req.Tool,
		Profile:              req.Profile,
		Message:              message,
		KeptLive:             result.KeptLive,
		PreviousProfile:      result.PreviousProfile,
		AutoBackup:           result.AutoBackup,
		ResnapshottedProfile: result.ResnapshottedProfile,
		Warnings:             result.Warnings,
	}, nil
}

// Backup backs up current auth to a profile.
func (h *Handlers) Backup(req BackupRequest) (*BackupResponse, error) {
	req.Profile = strings.TrimSpace(req.Profile)
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
	if authfile.IsSystemProfile(req.Profile) {
		return nil, fmt.Errorf("cannot replace a system profile")
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

	if err := h.vault.BackupWithOptions(fileSet, req.Profile, authfile.BackupOptions{Overwrite: req.Overwrite}); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &BackupResponse{Tool: req.Tool, Profile: req.Profile, Message: "profile already exists; choose a new name or explicitly confirm overwrite"}, nil
		}
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

	hs := &HealthStatus{
		ErrorCount: ph.ErrorCount1h,
		Renewable:  ph.CredentialRenewable(),
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
				ph.RateLimitedUntil = cooldown.CooldownUntil
			}
		}
	}
	hs.Status = health.CalculateStatus(ph).String()
	hs.Signals = health.CredentialSignals(ph, health.DefaultHealthConfig())
	if tool == "cursor" && !ph.CredentialRenewable() &&
		(ph.ProviderRejected() || (!ph.TokenExpiresAt.IsZero() && time.Until(ph.TokenExpiresAt) <= ph.ReloginWarningLead)) {
		hs.Recommendation = health.CursorReloginInstructions(name)
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
			id, err = identity.ExtractFromGeminiConfig(vaultPath + "/oauth_creds.json")
			if errors.Is(err, os.ErrNotExist) {
				// Polling must recognize legacy snapshots without migrating them.
				id, err = identity.ExtractFromGeminiConfig(vaultPath + "/oauth_credentials.json")
			}
		}
	case "grok":
		id, err = identity.ExtractFromGrokAuth(vaultPath + "/auth.json")
	case "opencode":
		id, err = identity.ExtractFromGenericAuth(vaultPath + "/auth.json")
	case "cursor":
		id, err = identity.ExtractFromGenericAuth(vaultPath + "/auth.json")
		if err != nil {
			id, err = identity.ExtractFromGenericAuth(vaultPath + "/settings.json")
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
