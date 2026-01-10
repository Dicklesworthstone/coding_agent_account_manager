package authpool

import (
	"sync"
	"testing"
	"time"
)

func TestNewAuthPool(t *testing.T) {
	p := NewAuthPool()
	if p == nil {
		t.Fatal("NewAuthPool() returned nil")
	}

	// Check defaults
	if p.refreshThreshold != 5*time.Minute {
		t.Errorf("refreshThreshold = %v, want 5m", p.refreshThreshold)
	}
	if p.cooldownDuration != 5*time.Minute {
		t.Errorf("cooldownDuration = %v, want 5m", p.cooldownDuration)
	}
	if p.maxRetries != 3 {
		t.Errorf("maxRetries = %d, want 3", p.maxRetries)
	}
}

func TestNewAuthPool_WithOptions(t *testing.T) {
	p := NewAuthPool(
		WithRefreshThreshold(10*time.Minute),
		WithCooldownDuration(15*time.Minute),
		WithMaxRetries(5),
	)

	if p.refreshThreshold != 10*time.Minute {
		t.Errorf("refreshThreshold = %v, want 10m", p.refreshThreshold)
	}
	if p.cooldownDuration != 15*time.Minute {
		t.Errorf("cooldownDuration = %v, want 15m", p.cooldownDuration)
	}
	if p.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", p.maxRetries)
	}
}

func TestAuthPool_AddProfile(t *testing.T) {
	p := NewAuthPool()

	// Add a profile
	profile := p.AddProfile("claude", "test")
	if profile == nil {
		t.Fatal("AddProfile() returned nil")
	}
	if profile.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", profile.Provider)
	}
	if profile.ProfileName != "test" {
		t.Errorf("ProfileName = %q, want test", profile.ProfileName)
	}
	if profile.Status != PoolStatusUnknown {
		t.Errorf("Status = %v, want Unknown", profile.Status)
	}

	// Adding same profile returns existing
	profile2 := p.AddProfile("claude", "test")
	if profile2.Provider != profile.Provider || profile2.ProfileName != profile.ProfileName {
		t.Error("AddProfile should return existing profile for duplicate")
	}

	// Count should be 1
	if p.Count() != 1 {
		t.Errorf("Count() = %d, want 1", p.Count())
	}
}

func TestAuthPool_RemoveProfile(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	if p.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", p.Count())
	}

	p.RemoveProfile("claude", "test")

	if p.Count() != 0 {
		t.Errorf("Count() = %d after remove, want 0", p.Count())
	}

	// Remove non-existent should not panic
	p.RemoveProfile("claude", "nonexistent")
}

func TestAuthPool_GetProfile(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Get existing
	profile := p.GetProfile("claude", "test")
	if profile == nil {
		t.Fatal("GetProfile() returned nil for existing profile")
	}
	if profile.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", profile.Provider)
	}

	// Get non-existent
	profile2 := p.GetProfile("claude", "nonexistent")
	if profile2 != nil {
		t.Errorf("GetProfile() = %v for non-existent, want nil", profile2)
	}
}

func TestAuthPool_GetStatus(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Default status is unknown
	status := p.GetStatus("claude", "test")
	if status != PoolStatusUnknown {
		t.Errorf("GetStatus() = %v, want Unknown", status)
	}

	// Non-existent profile
	status2 := p.GetStatus("claude", "nonexistent")
	if status2 != PoolStatusUnknown {
		t.Errorf("GetStatus() for non-existent = %v, want Unknown", status2)
	}
}

func TestAuthPool_SetStatus(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Set status
	err := p.SetStatus("claude", "test", PoolStatusReady)
	if err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}

	status := p.GetStatus("claude", "test")
	if status != PoolStatusReady {
		t.Errorf("GetStatus() = %v, want Ready", status)
	}

	// Set status on non-existent returns error
	err = p.SetStatus("claude", "nonexistent", PoolStatusReady)
	if err == nil {
		t.Error("SetStatus() on non-existent should return error")
	}
}

func TestAuthPool_SetStatus_ClearsErrorOnReady(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Set error first
	p.SetError("claude", "test", errTest("test error"))
	profile := p.GetProfile("claude", "test")
	if profile.ErrorCount == 0 {
		t.Fatal("ErrorCount should be > 0 after SetError")
	}

	// Set status to ready should clear error
	p.SetStatus("claude", "test", PoolStatusReady)
	profile = p.GetProfile("claude", "test")
	if profile.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d after SetStatus(Ready), want 0", profile.ErrorCount)
	}
	if profile.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q after SetStatus(Ready), want empty", profile.ErrorMessage)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestAuthPool_SetError(t *testing.T) {
	p := NewAuthPool(WithMaxRetries(3))
	p.AddProfile("claude", "test")

	// First error
	p.SetError("claude", "test", errTest("error 1"))
	profile := p.GetProfile("claude", "test")
	if profile.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", profile.ErrorCount)
	}
	if profile.Status == PoolStatusError {
		t.Error("Status should not be Error after 1 error")
	}

	// Second error
	p.SetError("claude", "test", errTest("error 2"))
	profile = p.GetProfile("claude", "test")
	if profile.ErrorCount != 2 {
		t.Errorf("ErrorCount = %d, want 2", profile.ErrorCount)
	}

	// Third error should trigger error status
	p.SetError("claude", "test", errTest("error 3"))
	profile = p.GetProfile("claude", "test")
	if profile.ErrorCount != 3 {
		t.Errorf("ErrorCount = %d, want 3", profile.ErrorCount)
	}
	if profile.Status != PoolStatusError {
		t.Errorf("Status = %v, want Error after 3 errors", profile.Status)
	}

	// SetError on non-existent should not panic
	p.SetError("claude", "nonexistent", errTest("error"))
}

func TestAuthPool_SetCooldown(t *testing.T) {
	p := NewAuthPool(WithCooldownDuration(5 * time.Minute))
	p.AddProfile("claude", "test")

	// Set cooldown with explicit duration
	p.SetCooldown("claude", "test", 10*time.Minute)
	profile := p.GetProfile("claude", "test")
	if profile.Status != PoolStatusCooldown {
		t.Errorf("Status = %v, want Cooldown", profile.Status)
	}
	if profile.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be set")
	}

	// Clear and test default duration
	p.ClearCooldown("claude", "test")
	p.SetCooldown("claude", "test", 0) // Use default
	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusCooldown {
		t.Errorf("Status = %v, want Cooldown", profile.Status)
	}

	// SetCooldown on non-existent should not panic
	p.SetCooldown("claude", "nonexistent", time.Minute)
}

func TestAuthPool_ClearCooldown(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetCooldown("claude", "test", time.Minute)

	profile := p.GetProfile("claude", "test")
	if profile.Status != PoolStatusCooldown {
		t.Fatal("Profile should be in cooldown")
	}

	p.ClearCooldown("claude", "test")
	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v after ClearCooldown, want Ready", profile.Status)
	}
	if !profile.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero after ClearCooldown")
	}

	// ClearCooldown on non-existent should not panic
	p.ClearCooldown("claude", "nonexistent")
}

func TestAuthPool_UpdateTokenExpiry(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetStatus("claude", "test", PoolStatusReady)

	// Set future expiry
	future := time.Now().Add(time.Hour)
	p.UpdateTokenExpiry("claude", "test", future)
	profile := p.GetProfile("claude", "test")
	if profile.TokenExpiry.IsZero() {
		t.Error("TokenExpiry should be set")
	}

	// Set past expiry should mark as expired
	past := time.Now().Add(-time.Hour)
	p.UpdateTokenExpiry("claude", "test", past)
	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusExpired {
		t.Errorf("Status = %v after past expiry, want Expired", profile.Status)
	}

	// UpdateTokenExpiry on non-existent should not panic
	p.UpdateTokenExpiry("claude", "nonexistent", future)
}

func TestAuthPool_MarkRefreshed(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetError("claude", "test", errTest("error"))
	p.SetError("claude", "test", errTest("error"))

	profile := p.GetProfile("claude", "test")
	if profile.ErrorCount != 2 {
		t.Fatal("ErrorCount should be 2")
	}

	// Mark refreshed
	expiry := time.Now().Add(time.Hour)
	p.MarkRefreshed("claude", "test", expiry)

	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v, want Ready", profile.Status)
	}
	if profile.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d, want 0", profile.ErrorCount)
	}
	if profile.LastRefresh.IsZero() {
		t.Error("LastRefresh should be set")
	}

	// MarkRefreshed on non-existent should not panic
	p.MarkRefreshed("claude", "nonexistent", expiry)
}

func TestAuthPool_MarkUsed(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	profile := p.GetProfile("claude", "test")
	if !profile.LastUsed.IsZero() {
		t.Error("LastUsed should be zero initially")
	}

	p.MarkUsed("claude", "test")
	profile = p.GetProfile("claude", "test")
	if profile.LastUsed.IsZero() {
		t.Error("LastUsed should be set after MarkUsed")
	}

	// MarkUsed on non-existent should not panic
	p.MarkUsed("claude", "nonexistent")
}

func TestAuthPool_GetReadyProfiles(t *testing.T) {
	p := NewAuthPool()

	// Add profiles with different statuses
	p.AddProfile("claude", "ready1")
	p.SetStatus("claude", "ready1", PoolStatusReady)

	p.AddProfile("claude", "ready2")
	p.SetStatus("claude", "ready2", PoolStatusReady)

	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	p.AddProfile("codex", "ready")
	p.SetStatus("codex", "ready", PoolStatusReady)

	// Get all ready profiles
	ready := p.GetReadyProfiles("")
	if len(ready) != 3 {
		t.Errorf("GetReadyProfiles('') = %d profiles, want 3", len(ready))
	}

	// Get ready profiles for specific provider
	claudeReady := p.GetReadyProfiles("claude")
	if len(claudeReady) != 2 {
		t.Errorf("GetReadyProfiles('claude') = %d profiles, want 2", len(claudeReady))
	}
}

func TestAuthPool_GetReadyProfiles_SortOrder(t *testing.T) {
	p := NewAuthPool()

	// Add profiles with different priorities
	p.AddProfile("claude", "low")
	p.SetStatus("claude", "low", PoolStatusReady)

	p.AddProfile("claude", "high")
	p.SetStatus("claude", "high", PoolStatusReady)

	// Manually set priority (normally done through vault or other means)
	p.mu.Lock()
	p.profiles["claude:high"].Priority = 10
	p.profiles["claude:low"].Priority = 1
	p.mu.Unlock()

	ready := p.GetReadyProfiles("claude")
	if len(ready) != 2 {
		t.Fatalf("GetReadyProfiles() = %d profiles, want 2", len(ready))
	}

	// High priority should be first
	if ready[0].ProfileName != "high" {
		t.Errorf("First profile = %s, want 'high' (higher priority)", ready[0].ProfileName)
	}
}

func TestAuthPool_GetProfilesNeedingRefresh(t *testing.T) {
	p := NewAuthPool(WithRefreshThreshold(10 * time.Minute))

	// Ready profile with distant expiry
	p.AddProfile("claude", "ok")
	p.SetStatus("claude", "ok", PoolStatusReady)
	p.UpdateTokenExpiry("claude", "ok", time.Now().Add(time.Hour))

	// Expired profile
	p.AddProfile("claude", "expired")
	p.SetStatus("claude", "expired", PoolStatusExpired)

	// Error profile
	p.AddProfile("claude", "error")
	p.SetStatus("claude", "error", PoolStatusError)

	// Profile expiring soon
	p.AddProfile("claude", "soon")
	p.SetStatus("claude", "soon", PoolStatusReady)
	p.UpdateTokenExpiry("claude", "soon", time.Now().Add(5*time.Minute))

	needsRefresh := p.GetProfilesNeedingRefresh("claude")
	// Should include: expired, error, soon (expiring within threshold)
	if len(needsRefresh) != 3 {
		t.Errorf("GetProfilesNeedingRefresh() = %d profiles, want 3", len(needsRefresh))
	}
}

func TestAuthPool_GetProfilesInCooldown(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "ready")
	p.SetStatus("claude", "ready", PoolStatusReady)

	p.AddProfile("claude", "cool1")
	p.SetCooldown("claude", "cool1", time.Hour)

	p.AddProfile("claude", "cool2")
	p.SetCooldown("claude", "cool2", time.Hour)

	inCooldown := p.GetProfilesInCooldown("claude")
	if len(inCooldown) != 2 {
		t.Errorf("GetProfilesInCooldown() = %d profiles, want 2", len(inCooldown))
	}
}

func TestAuthPool_GetAllProfiles(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "a")
	p.AddProfile("claude", "b")
	p.AddProfile("codex", "c")

	all := p.GetAllProfiles("")
	if len(all) != 3 {
		t.Errorf("GetAllProfiles('') = %d profiles, want 3", len(all))
	}

	claudeOnly := p.GetAllProfiles("claude")
	if len(claudeOnly) != 2 {
		t.Errorf("GetAllProfiles('claude') = %d profiles, want 2", len(claudeOnly))
	}
}

func TestAuthPool_SelectBest(t *testing.T) {
	p := NewAuthPool()

	// No profiles
	best := p.SelectBest("claude")
	if best != nil {
		t.Errorf("SelectBest() with no profiles = %v, want nil", best)
	}

	// Add non-ready profiles
	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	best = p.SelectBest("claude")
	if best != nil {
		t.Errorf("SelectBest() with no ready profiles = %v, want nil", best)
	}

	// Add ready profile
	p.AddProfile("claude", "ready")
	p.SetStatus("claude", "ready", PoolStatusReady)

	best = p.SelectBest("claude")
	if best == nil {
		t.Fatal("SelectBest() = nil, want profile")
	}
	if best.ProfileName != "ready" {
		t.Errorf("SelectBest() = %s, want 'ready'", best.ProfileName)
	}
}

func TestAuthPool_CountByStatus(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "ready1")
	p.SetStatus("claude", "ready1", PoolStatusReady)

	p.AddProfile("claude", "ready2")
	p.SetStatus("claude", "ready2", PoolStatusReady)

	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	p.AddProfile("claude", "error")
	p.SetStatus("claude", "error", PoolStatusError)

	counts := p.CountByStatus()
	if counts[PoolStatusReady] != 2 {
		t.Errorf("Ready count = %d, want 2", counts[PoolStatusReady])
	}
	if counts[PoolStatusCooldown] != 1 {
		t.Errorf("Cooldown count = %d, want 1", counts[PoolStatusCooldown])
	}
	if counts[PoolStatusError] != 1 {
		t.Errorf("Error count = %d, want 1", counts[PoolStatusError])
	}
}

func TestAuthPool_CheckAndUpdateCooldowns(t *testing.T) {
	p := NewAuthPool()

	// Add profile with expired cooldown
	p.AddProfile("claude", "test")
	p.mu.Lock()
	p.profiles["claude:test"].Status = PoolStatusCooldown
	p.profiles["claude:test"].CooldownUntil = time.Now().Add(-time.Minute) // Already expired
	p.mu.Unlock()

	cleared := p.CheckAndUpdateCooldowns()
	if cleared != 1 {
		t.Errorf("CheckAndUpdateCooldowns() = %d, want 1", cleared)
	}

	profile := p.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v after cooldown cleared, want Ready", profile.Status)
	}
}

func TestAuthPool_Summary(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "ready")
	p.SetStatus("claude", "ready", PoolStatusReady)

	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	p.AddProfile("codex", "error")
	p.SetStatus("codex", "error", PoolStatusError)

	summary := p.Summary()
	if summary.TotalProfiles != 3 {
		t.Errorf("TotalProfiles = %d, want 3", summary.TotalProfiles)
	}
	if summary.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1", summary.ReadyCount)
	}
	if summary.CooldownCount != 1 {
		t.Errorf("CooldownCount = %d, want 1", summary.CooldownCount)
	}
	if summary.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", summary.ErrorCount)
	}
	if summary.ByProvider["claude"] != 2 {
		t.Errorf("ByProvider[claude] = %d, want 2", summary.ByProvider["claude"])
	}
	if summary.ByProvider["codex"] != 1 {
		t.Errorf("ByProvider[codex] = %d, want 1", summary.ByProvider["codex"])
	}
}

func TestAuthPool_OnStateChangeCallback(t *testing.T) {
	var callbackCalled bool
	var callbackProfile *PooledProfile
	var callbackOldStatus, callbackNewStatus PoolStatus
	var wg sync.WaitGroup

	wg.Add(1)
	p := NewAuthPool(
		WithOnStateChange(func(profile *PooledProfile, oldStatus, newStatus PoolStatus) {
			callbackCalled = true
			callbackProfile = profile
			callbackOldStatus = oldStatus
			callbackNewStatus = newStatus
			wg.Done()
		}),
	)

	p.AddProfile("claude", "test")
	p.SetStatus("claude", "test", PoolStatusReady)

	// Wait for callback (it's async)
	wg.Wait()

	if !callbackCalled {
		t.Error("OnStateChange callback was not called")
	}
	if callbackProfile == nil || callbackProfile.ProfileName != "test" {
		t.Error("callback received wrong profile")
	}
	if callbackOldStatus != PoolStatusUnknown {
		t.Errorf("oldStatus = %v, want Unknown", callbackOldStatus)
	}
	if callbackNewStatus != PoolStatusReady {
		t.Errorf("newStatus = %v, want Ready", callbackNewStatus)
	}
}

func TestAuthPool_Concurrency(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetStatus("claude", "test", PoolStatusReady)

	var wg sync.WaitGroup
	const goroutines = 100

	// Concurrent reads
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.GetProfile("claude", "test")
			p.GetStatus("claude", "test")
			p.GetReadyProfiles("claude")
			p.Count()
		}()
	}

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p.MarkUsed("claude", "test")
			if n%2 == 0 {
				p.SetStatus("claude", "test", PoolStatusReady)
			}
		}(i)
	}

	wg.Wait()
}

// TestPooledProfile tests the PooledProfile methods.
func TestPooledProfile_Key(t *testing.T) {
	p := &PooledProfile{
		Provider:    "claude",
		ProfileName: "test",
	}
	if p.Key() != "claude:test" {
		t.Errorf("Key() = %q, want 'claude:test'", p.Key())
	}
}

func TestPooledProfile_IsExpired(t *testing.T) {
	tests := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero", time.Time{}, false},
		{"future", time.Now().Add(time.Hour), false},
		{"past", time.Now().Add(-time.Hour), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PooledProfile{TokenExpiry: tc.expiry}
			if got := p.IsExpired(); got != tc.want {
				t.Errorf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPooledProfile_IsExpiringSoon(t *testing.T) {
	threshold := 10 * time.Minute

	tests := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero", time.Time{}, false},
		{"far future", time.Now().Add(time.Hour), false},
		{"within threshold", time.Now().Add(5 * time.Minute), true},
		{"past", time.Now().Add(-time.Minute), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PooledProfile{TokenExpiry: tc.expiry}
			if got := p.IsExpiringSoon(threshold); got != tc.want {
				t.Errorf("IsExpiringSoon(%v) = %v, want %v", threshold, got, tc.want)
			}
		})
	}
}

func TestPooledProfile_IsInCooldown(t *testing.T) {
	tests := []struct {
		name    string
		until   time.Time
		want    bool
	}{
		{"zero", time.Time{}, false},
		{"future", time.Now().Add(time.Hour), true},
		{"past", time.Now().Add(-time.Hour), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PooledProfile{CooldownUntil: tc.until}
			if got := p.IsInCooldown(); got != tc.want {
				t.Errorf("IsInCooldown() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPooledProfile_TimeUntilExpiry(t *testing.T) {
	p := &PooledProfile{}

	// Zero expiry
	if p.TimeUntilExpiry() != 0 {
		t.Error("TimeUntilExpiry() should be 0 for zero expiry")
	}

	// Future expiry
	p.TokenExpiry = time.Now().Add(time.Hour)
	ttl := p.TimeUntilExpiry()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Errorf("TimeUntilExpiry() = %v, expected ~1h", ttl)
	}

	// Past expiry
	p.TokenExpiry = time.Now().Add(-time.Hour)
	if p.TimeUntilExpiry() != 0 {
		t.Error("TimeUntilExpiry() should be 0 for past expiry")
	}
}

func TestPooledProfile_TimeInCooldown(t *testing.T) {
	p := &PooledProfile{}

	// Zero cooldown
	if p.TimeInCooldown() != 0 {
		t.Error("TimeInCooldown() should be 0 for zero cooldown")
	}

	// Future cooldown
	p.CooldownUntil = time.Now().Add(time.Hour)
	ttl := p.TimeInCooldown()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Errorf("TimeInCooldown() = %v, expected ~1h", ttl)
	}

	// Past cooldown
	p.CooldownUntil = time.Now().Add(-time.Hour)
	if p.TimeInCooldown() != 0 {
		t.Error("TimeInCooldown() should be 0 for past cooldown")
	}
}

func TestPooledProfile_Clone(t *testing.T) {
	original := &PooledProfile{
		Provider:     "claude",
		ProfileName:  "test",
		Status:       PoolStatusReady,
		TokenExpiry:  time.Now().Add(time.Hour),
		ErrorCount:   2,
		ErrorMessage: "test error",
		Priority:     5,
	}

	clone := original.Clone()
	if clone == original {
		t.Error("Clone() should return a different pointer")
	}
	if clone.Provider != original.Provider {
		t.Error("Clone() Provider mismatch")
	}
	if clone.ProfileName != original.ProfileName {
		t.Error("Clone() ProfileName mismatch")
	}
	if clone.Status != original.Status {
		t.Error("Clone() Status mismatch")
	}
	if clone.ErrorCount != original.ErrorCount {
		t.Error("Clone() ErrorCount mismatch")
	}
	if clone.Priority != original.Priority {
		t.Error("Clone() Priority mismatch")
	}

	// Test nil clone
	var nilProfile *PooledProfile
	if nilProfile.Clone() != nil {
		t.Error("Clone() of nil should return nil")
	}
}

func TestPoolStatus_String(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   string
	}{
		{PoolStatusUnknown, "unknown"},
		{PoolStatusReady, "ready"},
		{PoolStatusRefreshing, "refreshing"},
		{PoolStatusExpired, "expired"},
		{PoolStatusCooldown, "cooldown"},
		{PoolStatusError, "error"},
		{PoolStatus(99), "unknown"},
	}

	for _, tc := range tests {
		if got := tc.status.String(); got != tc.want {
			t.Errorf("PoolStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestPoolStatus_IsUsable(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   bool
	}{
		{PoolStatusUnknown, false},
		{PoolStatusReady, true},
		{PoolStatusRefreshing, false},
		{PoolStatusExpired, false},
		{PoolStatusCooldown, false},
		{PoolStatusError, false},
	}

	for _, tc := range tests {
		if got := tc.status.IsUsable(); got != tc.want {
			t.Errorf("PoolStatus(%v).IsUsable() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestPoolStatus_NeedsRefresh(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   bool
	}{
		{PoolStatusUnknown, false},
		{PoolStatusReady, false},
		{PoolStatusRefreshing, false},
		{PoolStatusExpired, true},
		{PoolStatusCooldown, false},
		{PoolStatusError, true},
	}

	for _, tc := range tests {
		if got := tc.status.NeedsRefresh(); got != tc.want {
			t.Errorf("PoolStatus(%v).NeedsRefresh() = %v, want %v", tc.status, got, tc.want)
		}
	}
}
