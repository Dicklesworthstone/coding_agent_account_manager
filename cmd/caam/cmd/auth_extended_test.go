package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to capture stdout/stderr
func captureStdout(t *testing.T, f func() error) (string, error) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	err := f()
	
	w.Close()
	os.Stdout = oldStdout
	
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return strings.TrimSpace(buf.String()), err
}

// MockProvider implements provider.Provider for testing.
type MockProvider struct {
	id              string
	mockDetection   *provider.AuthDetection
	mockImportFiles []string
	importError     error
	prepareError    error
	calledPrepare   bool
	calledImport    bool
}

func (m *MockProvider) ID() string { return m.id }
func (m *MockProvider) DisplayName() string { return strings.Title(m.id) }
func (m *MockProvider) DefaultBin() string { return m.id }
func (m *MockProvider) SupportedAuthModes() []provider.AuthMode { return []provider.AuthMode{provider.AuthModeOAuth} }
func (m *MockProvider) AuthFiles() []provider.AuthFileSpec { return nil }
func (m *MockProvider) PrepareProfile(ctx context.Context, p *profile.Profile) error {
	m.calledPrepare = true
	return m.prepareError
}
func (m *MockProvider) Env(ctx context.Context, p *profile.Profile) (map[string]string, error) { return nil, nil }
func (m *MockProvider) Login(ctx context.Context, p *profile.Profile) error { return nil }
func (m *MockProvider) Logout(ctx context.Context, p *profile.Profile) error { return nil }
func (m *MockProvider) Status(ctx context.Context, p *profile.Profile) (*provider.ProfileStatus, error) { return nil, nil }
func (m *MockProvider) ValidateProfile(ctx context.Context, p *profile.Profile) error { return nil }
func (m *MockProvider) DetectExistingAuth() (*provider.AuthDetection, error) {
	if m.mockDetection != nil {
		return m.mockDetection, nil
	}
	return &provider.AuthDetection{Provider: m.id, Found: false}, nil
}
func (m *MockProvider) ImportAuth(ctx context.Context, sourcePath string, targetProfile *profile.Profile) ([]string, error) {
	m.calledImport = true
	if m.importError != nil {
		return nil, m.importError
	}
	// Simulate copy by creating files
	for _, f := range m.mockImportFiles {
		fullPath := filepath.Join(targetProfile.BasePath, f)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		os.WriteFile(fullPath, []byte("mock-content"), 0600)
	}
	return m.mockImportFiles, nil
}
func (m *MockProvider) ValidateToken(ctx context.Context, p *profile.Profile, passive bool) (*provider.ValidationResult, error) { return nil, nil }

func TestAuthCommands_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize globals and mocks")
	
	rootDir := h.TempDir
	h.SetEnv("XDG_DATA_HOME", rootDir)
	
	// Initialize store
	storePath := filepath.Join(rootDir, "caam", "profiles")
	require.NoError(t, os.MkdirAll(storePath, 0755))
	
	// Override globals
	originalProfileStore := profileStore
	originalRegistry := registry
	originalEnvLookup := envLookup
	defer func() {
		profileStore = originalProfileStore
		registry = originalRegistry
		envLookup = originalEnvLookup
	}()
	
	profileStore = profile.NewStore(storePath)
	
	// Create mock providers
	mockClaude := &MockProvider{
		id: "claude",
		mockDetection: &provider.AuthDetection{
			Provider: "claude",
			Found:    true,
			Locations: []provider.AuthLocation{
				{Path: "/home/user/.claude.json", Exists: true, IsValid: true},
			},
			Primary: &provider.AuthLocation{
				Path:   "/home/user/.claude.json",
				Exists: true,
			},
		},
		mockImportFiles: []string{"auth.json"},
	}
	
	mockCodex := &MockProvider{
		id: "codex",
		mockDetection: &provider.AuthDetection{
			Provider: "codex",
			Found:    false,
		},
	}
	
	// Mock registry
	registry = provider.NewRegistry()
	registry.Register(mockClaude)
	registry.Register(mockCodex)
	
	// Mock envLookup for HOME
	envLookup = func(key string) string {
		if key == "HOME" {
			return "/home/user"
		}
		return ""
	}
	
	h.EndStep("Setup")
	
	// 2. Test Auth Detect
	h.StartStep("Detect", "Run auth detect")
	
	// Run detect with JSON output
	authDetectCmd.Flags().Set("json", "true")
	output, err := captureStdout(t, func() error {
		return authDetectCmd.RunE(authDetectCmd, []string{})
	})
	require.NoError(t, err)
	
	var report AuthDetectReport
	err = json.Unmarshal([]byte(output), &report)
	require.NoError(t, err)
	
	// Verify report
	assert.Equal(t, 2, report.Summary.TotalProviders)
	assert.Equal(t, 1, report.Summary.FoundCount)
	assert.Equal(t, 1, report.Summary.NotFoundCount)
	
	// Verify Claude result
	var claudeRes AuthDetectResult
	for _, r := range report.Results {
		if r.Provider == "claude" {
			claudeRes = r
			break
		}
	}
	assert.True(t, claudeRes.Found)
	assert.NotNil(t, claudeRes.Primary)
	assert.Equal(t, "/home/user/.claude.json", claudeRes.Primary.Path)
	
	h.EndStep("Detect")
	
	// 3. Test Auth Import
	h.StartStep("Import", "Import detected auth")
	
	// Mock import success
	mockClaude.mockImportFiles = []string{"imported.json"}
	
	authImportCmd.Flags().Set("json", "true")
	authImportCmd.Flags().Set("name", "work")
	
	output, err = captureStdout(t, func() error {
		return authImportCmd.RunE(authImportCmd, []string{"claude"})
	})
	require.NoError(t, err)
	
	var importRes AuthImportResult
	err = json.Unmarshal([]byte(output), &importRes)
	require.NoError(t, err)
	
	assert.True(t, importRes.Success)
	assert.Equal(t, "claude", importRes.Provider)
	assert.Equal(t, "work", importRes.ProfileName)
	assert.Contains(t, importRes.SourceFile, ".claude.json")
	assert.Contains(t, importRes.CopiedFiles, "imported.json")
	
	// Verify profile creation
	prof, err := profileStore.Load("claude", "work")
	require.NoError(t, err)
	assert.Equal(t, "work", prof.Name)
	
	// Verify mock calls
	assert.True(t, mockClaude.calledPrepare)
	assert.True(t, mockClaude.calledImport)
	
	h.EndStep("Import")
	
	// 4. Test Auth Import with Source
	h.StartStep("ImportSource", "Import with explicit source")
	
	authImportCmd.Flags().Set("name", "custom")
	authImportCmd.Flags().Set("source", "/home/user/custom.json")
	// Need to mock file existence for explicit source check
	// cmd/auth.go checks os.Stat(sourcePath).
	// But /home/user doesn't exist in our test env.
	// We need to create a real temp file and pass that.
	
	tmpSource := filepath.Join(rootDir, "custom.json")
	require.NoError(t, os.WriteFile(tmpSource, []byte("{}"), 0600))
	
	authImportCmd.Flags().Set("source", tmpSource)
	
	output, err = captureStdout(t, func() error {
		return authImportCmd.RunE(authImportCmd, []string{"claude"})
	})
	require.NoError(t, err)
	
	err = json.Unmarshal([]byte(output), &importRes)
	require.NoError(t, err)
	assert.True(t, importRes.Success)
	assert.Equal(t, tmpSource, importRes.SourceFile)
	assert.Equal(t, "custom", importRes.ProfileName)
	
	h.EndStep("ImportSource")
}
