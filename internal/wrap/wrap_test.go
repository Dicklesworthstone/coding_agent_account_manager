package wrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
)

func TestWrapperRejectsExpiredCursorSessionBeforeChangingAuth(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(root, "cursor-config"))
	vault := authfile.NewVault(filepath.Join(root, "custom-vault"))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Hour).Unix())))
	expired := []byte(fmt.Sprintf(`{"accessToken":%q}`, "e30."+payload+".synthetic"))
	authPath := vault.BackupPath("cursor", "expired", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, expired, 0600); err != nil {
		t.Fatal(err)
	}
	livePath := authfile.CursorAuthFiles().Files[0].Path
	for _, f := range authfile.CursorAuthFiles().Files {
		if filepath.Base(f.Path) == "auth.json" {
			livePath = f.Path
		}
	}
	if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
		t.Fatal(err)
	}
	live := []byte(`{"apiKey":"synthetic-live-owner"}`)
	if err := os.WriteFile(livePath, live, 0600); err != nil {
		t.Fatal(err)
	}
	oldExec := ExecCommand
	called := false
	ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "caam-test-must-not-launch")
	}
	t.Cleanup(func() { ExecCommand = oldExec })
	cfg := DefaultConfig()
	cfg.Provider = "cursor"
	cfg.Stdout = &bytes.Buffer{}
	cfg.Stderr = &bytes.Buffer{}
	result := NewWrapper(vault, nil, nil, cfg).Run(context.Background())
	if result.Err == nil || result.ExitCode == 0 || len(result.ProfilesUsed) != 0 || called {
		t.Fatalf("expired session reached execution: result=%+v called=%v", result, called)
	}
	after, err := os.ReadFile(livePath)
	if err != nil || !bytes.Equal(after, live) {
		t.Fatalf("failed selection changed live login: %v", err)
	}
	profiles, err := vault.List("cursor")
	if err != nil || len(profiles) != 1 || profiles[0] != "expired" {
		t.Fatalf("failed selection changed vault: profiles=%v err=%v", profiles, err)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.CooldownDuration != 60*time.Minute {
		t.Errorf("CooldownDuration = %v, want 60m", cfg.CooldownDuration)
	}
	if !cfg.NotifyOnSwitch {
		t.Error("NotifyOnSwitch = false, want true")
	}
	if cfg.Algorithm != rotation.AlgorithmSmart {
		t.Errorf("Algorithm = %v, want smart", cfg.Algorithm)
	}
	if cfg.Stdout == nil {
		t.Error("Stdout = nil, want os.Stdout")
	}
	if cfg.Stderr == nil {
		t.Error("Stderr = nil, want os.Stderr")
	}
}

func TestNewWrapper(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider = "claude"
	cfg.Stdout = nil // Should default to os.Stdout
	cfg.Stderr = nil // Should default to os.Stderr

	w := NewWrapper(nil, nil, nil, cfg)

	if w == nil {
		t.Fatal("NewWrapper returned nil")
	}
	if w.config.Stdout == nil {
		t.Error("Stdout not defaulted")
	}
	if w.config.Stderr == nil {
		t.Error("Stderr not defaulted")
	}
}

func TestWrapper_Run_NoProfiles(t *testing.T) {
	// Create temp vault with no profiles
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	cfg := DefaultConfig()
	cfg.Provider = "claude"
	cfg.NotifyOnSwitch = false

	stderr := &bytes.Buffer{}
	cfg.Stderr = stderr

	w := NewWrapper(vault, nil, nil, cfg)

	result := w.Run(context.Background())

	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if result.Err == nil {
		t.Error("Err = nil, want error about no profiles")
	}
}

func TestWrapper_Run_WithProfile(t *testing.T) {
	// Create temp vault with a profile
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a fake profile
	profileDir := filepath.Join(tmpDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Create health storage
	healthPath := filepath.Join(tmpDir, "health.json")
	healthStore := health.NewStorage(healthPath)

	// Create temp database
	dbPath := filepath.Join(tmpDir, "caam.db")
	db, err := caamdb.OpenAt(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := DefaultConfig()
	cfg.Provider = "claude"
	cfg.Args = []string{"--version"} // Simple command that should work
	cfg.NotifyOnSwitch = false

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cfg.Stdout = stdout
	cfg.Stderr = stderr

	w := NewWrapper(vault, db, healthStore, cfg)

	// This will fail because claude isn't installed, but that's OK
	// We're testing the wrapper logic, not the actual CLI
	result := w.Run(context.Background())

	// Check that profile was used
	if len(result.ProfilesUsed) == 0 {
		t.Error("No profiles used")
	}
	if len(result.ProfilesUsed) > 0 && result.ProfilesUsed[0] != "test@example.com" {
		t.Errorf("ProfilesUsed[0] = %q, want test@example.com", result.ProfilesUsed[0])
	}
}

func TestWrapperRateLimitPreservesLatestLiveCredentials(t *testing.T) {
	for _, tc := range []struct {
		name      string
		liveOwner string
		malformed bool
	}{
		{name: "native rotation survives failover", liveOwner: "alice"},
		{name: "external login is backed up under its own identity", liveOwner: "carol"},
		{name: "malformed backup leaves running login intact", liveOwner: "alice", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
			t.Setenv("CAAM_KEYCHAIN", "0")
			vault := authfile.NewVault(filepath.Join(root, "vault"))
			credentials := func(token string, expiry int64) []byte {
				return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`, token, "refresh-"+token, expiry))
			}
			identity := func(name string) []byte {
				return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"emailAddress":%q}}`, "id-"+name, name+"@example.com"))
			}
			oldAlice := credentials("alice-old", 2000000000000)
			rotated := credentials(tc.liveOwner+"-rotated", 2000003600000)
			bob := credentials("bob", 2000007200000)
			if tc.malformed {
				bob = []byte(`{"claudeAiOauth":{"accessToken":null}}`)
			}
			for name, data := range map[string][]byte{"alice": oldAlice, "bob": bob} {
				writeWrapSwitchFile(t, vault.BackupPath("claude", name, ".credentials.json"), data)
				writeWrapSwitchFile(t, vault.BackupPath("claude", name, ".claude.json"), identity(name))
			}
			livePath := filepath.Join(home, ".claude", ".credentials.json")
			identityPath := filepath.Join(home, ".claude.json")
			writeWrapSwitchFile(t, livePath, oldAlice)
			writeWrapSwitchFile(t, identityPath, identity("alice"))

			calls := 0
			originalExec := ExecCommand
			t.Cleanup(func() { ExecCommand = originalExec })
			ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if name != "claude" {
					t.Fatalf("unexpected executable: %s", name)
				}
				calls++
				mode := "success"
				if calls == 1 {
					assertWrapSwitchFile(t, livePath, oldAlice)
					// Simulate the native CLI rotating tokens, or another login
					// replacing the account, before it reports the rate limit.
					writeWrapSwitchFile(t, livePath, rotated)
					writeWrapSwitchFile(t, identityPath, identity(tc.liveOwner))
					mode = "limited"
				} else {
					assertWrapSwitchFile(t, livePath, bob)
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWrapSwitchProcess$", "--")
				cmd.Env = append(os.Environ(), "CAAM_WRAP_SWITCH_PROCESS="+mode)
				return cmd
			}
			cfg := DefaultConfig()
			cfg.Provider = "claude"
			cfg.Algorithm = rotation.AlgorithmRoundRobin
			cfg.MaxRetries = 1
			cfg.InitialDelay = 0
			cfg.Jitter = false
			cfg.NotifyOnSwitch = false
			cfg.Stdout = &bytes.Buffer{}
			cfg.Stderr = &bytes.Buffer{}
			result := NewWrapper(vault, nil, nil, cfg).Run(context.Background())
			if !result.RateLimitHit || strings.Join(result.ProfilesUsed, ",") != "alice,bob" {
				t.Fatalf("failover was not exercised: %+v", result)
			}
			wantCalls := 2
			wantLive := bob
			if tc.malformed {
				wantCalls = 1
				wantLive = rotated
				if result.Err == nil || result.ExitCode == 0 {
					t.Fatalf("malformed target reported success: %+v", result)
				}
			} else if result.Err != nil || result.ExitCode != 0 {
				t.Fatalf("failover failed: %+v", result)
			}
			if calls != wantCalls {
				t.Fatalf("native executions = %d, want %d", calls, wantCalls)
			}
			assertWrapSwitchFile(t, livePath, wantLive)
			wantAlice := oldAlice
			if !tc.malformed && tc.liveOwner == "alice" {
				wantAlice = rotated
			}
			assertWrapSwitchFile(t, vault.BackupPath("claude", "alice", ".credentials.json"), wantAlice)
			assertWrapSwitchFile(t, vault.BackupPath("claude", "bob", ".credentials.json"), bob)
			profiles, err := vault.List("claude")
			if err != nil {
				t.Fatal(err)
			}
			backups := 0
			for _, name := range profiles {
				if authfile.IsSystemProfile(name) {
					backups++
					assertWrapSwitchFile(t, vault.BackupPath("claude", name, ".credentials.json"), rotated)
				}
			}
			wantBackup := tc.liveOwner == "carol" && !tc.malformed
			if (backups == 1) != wantBackup || backups > 1 {
				t.Errorf("recovery backups = %d, want backup %v", backups, wantBackup)
			}
		})
	}
}

func TestWrapSwitchProcess(t *testing.T) {
	switch os.Getenv("CAAM_WRAP_SWITCH_PROCESS") {
	case "limited":
		fmt.Fprintln(os.Stderr, "Error: rate limit exceeded")
		os.Exit(1)
	case "success":
		fmt.Fprintln(os.Stdout, "request completed")
		os.Exit(0)
	}
}

func writeWrapSwitchFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertWrapSwitchFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("unexpected credential content at %s", path)
	}
}

func TestAuthFileSetForProvider(t *testing.T) {
	tests := []struct {
		provider string
		wantOK   bool
	}{
		{"claude", true},
		{"codex", true},
		{"gemini", true},
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			_, ok := AuthFileSetForProvider(tt.provider)
			if ok != tt.wantOK {
				t.Errorf("AuthFileSetForProvider(%q) ok = %v, want %v", tt.provider, ok, tt.wantOK)
			}
		})
	}
}

func TestBinForProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"claude", "claude"},
		{"codex", "codex"},
		{"gemini", "gemini"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := binForProvider(tt.provider)
			if got != tt.want {
				t.Errorf("binForProvider(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestTeeWriter(t *testing.T) {
	// Test that teeWriter properly buffers and detects patterns split across writes
	t.Run("split write detection", func(t *testing.T) {
		// Create a detector with a pattern that could be split
		detector, err := ratelimit.NewDetector(ratelimit.ProviderClaude, nil)
		if err != nil {
			t.Fatalf("create detector: %v", err)
		}

		dest := &bytes.Buffer{}
		tw := &teeWriter{
			dest:     dest,
			detector: detector,
		}

		// Write "rate limit" in two parts to simulate split output
		tw.Write([]byte("Error: rate li"))
		tw.Write([]byte("mit exceeded\n"))
		tw.Flush()

		if !detector.Detected() {
			t.Error("Detector failed to detect 'rate limit' split across writes")
		}

		// Verify output was forwarded correctly
		if dest.String() != "Error: rate limit exceeded\n" {
			t.Errorf("Output = %q, want 'Error: rate limit exceeded\\n'", dest.String())
		}
	})

	t.Run("complete line detection", func(t *testing.T) {
		detector, err := ratelimit.NewDetector(ratelimit.ProviderClaude, nil)
		if err != nil {
			t.Fatalf("create detector: %v", err)
		}

		dest := &bytes.Buffer{}
		tw := &teeWriter{
			dest:     dest,
			detector: detector,
		}

		// Write complete line with rate limit
		tw.Write([]byte("429 Too Many Requests\n"))
		tw.Flush()

		if !detector.Detected() {
			t.Error("Detector failed to detect '429' in complete line")
		}
	})

	t.Run("no false positives", func(t *testing.T) {
		detector, err := ratelimit.NewDetector(ratelimit.ProviderClaude, nil)
		if err != nil {
			t.Fatalf("create detector: %v", err)
		}

		dest := &bytes.Buffer{}
		tw := &teeWriter{
			dest:     dest,
			detector: detector,
		}

		// Write normal output
		tw.Write([]byte("Hello world\n"))
		tw.Write([]byte("Everything is working fine\n"))
		tw.Flush()

		if detector.Detected() {
			t.Error("Detector falsely detected rate limit in normal output")
		}
	})

	t.Run("partial line at end", func(t *testing.T) {
		detector, err := ratelimit.NewDetector(ratelimit.ProviderClaude, nil)
		if err != nil {
			t.Fatalf("create detector: %v", err)
		}

		dest := &bytes.Buffer{}
		tw := &teeWriter{
			dest:     dest,
			detector: detector,
		}

		// Write output without trailing newline (common in JSON errors)
		tw.Write([]byte(`{"error": "rate limit exceeded"}`))
		tw.Flush()

		if !detector.Detected() {
			t.Error("Detector failed to detect rate limit in partial line")
		}
	})
}

func TestResult(t *testing.T) {
	r := &Result{
		ExitCode:     0,
		ProfilesUsed: []string{"a", "b"},
		RateLimitHit: true,
		RetryCount:   1,
	}

	if r.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", r.ExitCode)
	}
	if len(r.ProfilesUsed) != 2 {
		t.Errorf("len(ProfilesUsed) = %d, want 2", len(r.ProfilesUsed))
	}
	if !r.RateLimitHit {
		t.Error("RateLimitHit = false, want true")
	}
	if r.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", r.RetryCount)
	}
}

func TestNextDelay(t *testing.T) {
	t.Run("exponential backoff without jitter", func(t *testing.T) {
		cfg := Config{
			InitialDelay:      10 * time.Second,
			MaxDelay:          5 * time.Minute,
			BackoffMultiplier: 2.0,
			Jitter:            false,
		}

		// attempt 0: 10s * 2^0 = 10s
		d0 := cfg.NextDelay(0)
		if d0 != 10*time.Second {
			t.Errorf("NextDelay(0) = %v, want 10s", d0)
		}

		// attempt 1: 10s * 2^1 = 20s
		d1 := cfg.NextDelay(1)
		if d1 != 20*time.Second {
			t.Errorf("NextDelay(1) = %v, want 20s", d1)
		}

		// attempt 2: 10s * 2^2 = 40s
		d2 := cfg.NextDelay(2)
		if d2 != 40*time.Second {
			t.Errorf("NextDelay(2) = %v, want 40s", d2)
		}
	})

	t.Run("respects max delay cap", func(t *testing.T) {
		cfg := Config{
			InitialDelay:      30 * time.Second,
			MaxDelay:          1 * time.Minute,
			BackoffMultiplier: 2.0,
			Jitter:            false,
		}

		// attempt 5: 30s * 2^5 = 960s = 16m, but capped at 1m
		d := cfg.NextDelay(5)
		if d != 1*time.Minute {
			t.Errorf("NextDelay(5) = %v, want 1m (max)", d)
		}
	})

	t.Run("jitter adds variation", func(t *testing.T) {
		cfg := Config{
			InitialDelay:      10 * time.Second,
			MaxDelay:          5 * time.Minute,
			BackoffMultiplier: 2.0,
			Jitter:            true,
		}

		// With jitter, delay should be within ±20% of 10s (8s to 12s)
		d := cfg.NextDelay(0)
		if d < 8*time.Second || d > 12*time.Second {
			t.Errorf("NextDelay(0) with jitter = %v, want 8s-12s", d)
		}
	})

	t.Run("negative attempt treated as zero", func(t *testing.T) {
		cfg := Config{
			InitialDelay:      10 * time.Second,
			MaxDelay:          5 * time.Minute,
			BackoffMultiplier: 2.0,
			Jitter:            false,
		}

		d := cfg.NextDelay(-5)
		if d != 10*time.Second {
			t.Errorf("NextDelay(-5) = %v, want 10s", d)
		}
	})

	t.Run("zero multiplier uses default of 2", func(t *testing.T) {
		cfg := Config{
			InitialDelay:      10 * time.Second,
			MaxDelay:          5 * time.Minute,
			BackoffMultiplier: 0, // Should default to 2.0
			Jitter:            false,
		}

		d := cfg.NextDelay(1)
		if d != 20*time.Second {
			t.Errorf("NextDelay(1) with zero multiplier = %v, want 20s", d)
		}
	})
}

func TestShouldRetry(t *testing.T) {
	cfg := Config{MaxRetries: 3}

	tests := []struct {
		attempt int
		want    bool
	}{
		{0, true},
		{1, true},
		{2, true},
		{3, false},
		{4, false},
	}

	for _, tt := range tests {
		got := cfg.ShouldRetry(tt.attempt)
		if got != tt.want {
			t.Errorf("ShouldRetry(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestConfigFromGlobal(t *testing.T) {
	// Create a global config with custom values
	globalCfg := &config.Config{
		Wrap: config.WrapConfig{
			MaxRetries:        5,
			InitialDelay:      config.Duration(45 * time.Second),
			MaxDelay:          config.Duration(10 * time.Minute),
			BackoffMultiplier: 1.5,
			Jitter:            false,
			CooldownDuration:  config.Duration(30 * time.Minute),
		},
	}

	cfg := ConfigFromGlobal(globalCfg, "claude")

	if cfg.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", cfg.Provider)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", cfg.MaxRetries)
	}
	if cfg.InitialDelay != 45*time.Second {
		t.Errorf("InitialDelay = %v, want 45s", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 10*time.Minute {
		t.Errorf("MaxDelay = %v, want 10m", cfg.MaxDelay)
	}
	if cfg.BackoffMultiplier != 1.5 {
		t.Errorf("BackoffMultiplier = %v, want 1.5", cfg.BackoffMultiplier)
	}
	if cfg.Jitter != false {
		t.Error("Jitter = true, want false")
	}
	if cfg.CooldownDuration != 30*time.Minute {
		t.Errorf("CooldownDuration = %v, want 30m", cfg.CooldownDuration)
	}
}

// Test Run with context cancellation
func TestWrapper_Run_ContextCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	// Create a profile
	profileDir := filepath.Join(tmpDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), []byte(`{}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Provider = "claude"
	cfg.NotifyOnSwitch = false

	stderr := &bytes.Buffer{}
	cfg.Stderr = stderr

	w := NewWrapper(vault, nil, nil, cfg)

	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result := w.Run(ctx)

	// With cancelled context, we should still get a result
	if result == nil {
		t.Fatal("Result is nil")
	}
}

// Test recordSession with nil DB
func TestWrapper_RecordSession_NilDB(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider = "claude"

	w := NewWrapper(nil, nil, nil, cfg)

	result := &Result{
		StartTime:    time.Now(),
		Duration:     5 * time.Second,
		ProfilesUsed: []string{"test"},
		ExitCode:     0,
	}

	// Should not panic with nil DB
	w.recordSession(result)
}

// Test recordSession with empty ProfilesUsed
func TestWrapper_RecordSession_EmptyProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := caamdb.OpenAt(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := DefaultConfig()
	cfg.Provider = "claude"

	w := NewWrapper(nil, db, nil, cfg)

	result := &Result{
		StartTime:    time.Now(),
		Duration:     5 * time.Second,
		ProfilesUsed: []string{}, // Empty
		ExitCode:     0,
	}

	// Should not panic with empty profiles
	w.recordSession(result)
}

// Test recordSession with retry count
func TestWrapper_RecordSession_WithRetries(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := caamdb.OpenAt(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := DefaultConfig()
	cfg.Provider = "claude"

	stderr := &bytes.Buffer{}
	cfg.Stderr = stderr

	w := NewWrapper(nil, db, nil, cfg)

	result := &Result{
		StartTime:    time.Now(),
		Duration:     10 * time.Second,
		ProfilesUsed: []string{"test@example.com"},
		ExitCode:     0,
		RetryCount:   2,
		RateLimitHit: true,
	}

	// Should record session with retry info
	w.recordSession(result)
}

// Test runOnce with unknown provider
func TestWrapper_RunOnce_UnknownProvider(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	cfg := DefaultConfig()
	cfg.Provider = "unknown_provider"
	cfg.NotifyOnSwitch = false

	w := NewWrapper(vault, nil, nil, cfg)

	exitCode, rateLimitHit, err := w.runOnce(context.Background(), "test")

	if err == nil {
		t.Error("Expected error for unknown provider")
	}
	if exitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", exitCode)
	}
	if rateLimitHit {
		t.Error("rateLimitHit should be false for unknown provider")
	}
}

// Test teeWriter Flush with empty buffer
func TestTeeWriter_Flush_EmptyBuffer(t *testing.T) {
	detector, err := ratelimit.NewDetector(ratelimit.ProviderClaude, nil)
	if err != nil {
		t.Fatalf("create detector: %v", err)
	}

	dest := &bytes.Buffer{}
	tw := &teeWriter{
		dest:     dest,
		detector: detector,
		buffer:   nil, // Empty buffer
	}

	// Flush with empty buffer should not panic
	tw.Flush()

	if detector.Detected() {
		t.Error("Detector should not detect anything in empty buffer")
	}
}

// Test teeWriter with multiple complete lines
func TestTeeWriter_MultipleLines(t *testing.T) {
	detector, err := ratelimit.NewDetector(ratelimit.ProviderClaude, nil)
	if err != nil {
		t.Fatalf("create detector: %v", err)
	}

	dest := &bytes.Buffer{}
	tw := &teeWriter{
		dest:     dest,
		detector: detector,
	}

	// Write multiple lines at once
	tw.Write([]byte("line1\nline2\nline3\n"))

	// Verify output
	if dest.String() != "line1\nline2\nline3\n" {
		t.Errorf("Output = %q, want 'line1\\nline2\\nline3\\n'", dest.String())
	}

	// No rate limit detected
	if detector.Detected() {
		t.Error("Detector should not detect rate limit in normal lines")
	}
}

// Test Run with Vault that returns error (not writable vault dir)
func TestWrapper_Run_VaultListError(t *testing.T) {
	// Create a vault pointing to a file (not a directory) to force list error
	tmpDir := t.TempDir()
	vaultPath := filepath.Join(tmpDir, "vault")
	if err := os.WriteFile(vaultPath, []byte("not a directory"), 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	vault := authfile.NewVault(vaultPath)

	cfg := DefaultConfig()
	cfg.Provider = "claude"
	cfg.NotifyOnSwitch = false

	stderr := &bytes.Buffer{}
	cfg.Stderr = stderr

	w := NewWrapper(vault, nil, nil, cfg)

	result := w.Run(context.Background())

	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	if result.Err == nil {
		t.Error("Expected error from vault list")
	}
}

// Test DefaultConfig initialization details
func TestDefaultConfig_AllFields(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.InitialDelay != 30*time.Second {
		t.Errorf("InitialDelay = %v, want 30s", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 5*time.Minute {
		t.Errorf("MaxDelay = %v, want 5m", cfg.MaxDelay)
	}
	if cfg.BackoffMultiplier != 2.0 {
		t.Errorf("BackoffMultiplier = %v, want 2.0", cfg.BackoffMultiplier)
	}
	if !cfg.Jitter {
		t.Error("Jitter = false, want true")
	}
}

// Test NextDelay with negative multiplier
func TestNextDelay_NegativeMultiplier(t *testing.T) {
	cfg := Config{
		InitialDelay:      10 * time.Second,
		MaxDelay:          5 * time.Minute,
		BackoffMultiplier: -1.0, // Negative, should default to 2.0
		Jitter:            false,
	}

	d := cfg.NextDelay(1)
	// With default multiplier of 2.0: 10s * 2^1 = 20s
	if d != 20*time.Second {
		t.Errorf("NextDelay(1) with negative multiplier = %v, want 20s", d)
	}
}
