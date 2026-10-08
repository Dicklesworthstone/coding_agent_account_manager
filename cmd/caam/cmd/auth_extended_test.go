package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/agy"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/cursor"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/grok"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/opencode"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout captures stdout from a function that prints to os.Stdout.
// It safely restores stdout even if the function panics.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}

	os.Stdout = w

	// Run function with deferred cleanup to handle panics
	var runErr error
	func() {
		defer func() {
			w.Close()
			os.Stdout = oldStdout
		}()
		runErr = f()
	}()

	// Read captured output (safe now - stdout is restored)
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	r.Close()

	return strings.TrimSpace(buf.String()), runErr
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
	profileError    error
	validationError error
	validation      *provider.ValidationResult
	nilValidation   bool
	passive         bool
	prepareHook     func(*profile.Profile) error
	importHook      func(*profile.Profile) error
}

func (m *MockProvider) ID() string          { return m.id }
func (m *MockProvider) DisplayName() string { return strings.Title(m.id) }
func (m *MockProvider) DefaultBin() string  { return m.id }
func (m *MockProvider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{provider.AuthModeOAuth}
}
func (m *MockProvider) AuthFiles() []provider.AuthFileSpec { return nil }
func (m *MockProvider) PrepareProfile(ctx context.Context, p *profile.Profile) error {
	m.calledPrepare = true
	if m.prepareHook != nil {
		return m.prepareHook(p)
	}
	return m.prepareError
}
func (m *MockProvider) Env(ctx context.Context, p *profile.Profile) (map[string]string, error) {
	return nil, nil
}
func (m *MockProvider) Login(ctx context.Context, p *profile.Profile) error  { return nil }
func (m *MockProvider) Logout(ctx context.Context, p *profile.Profile) error { return nil }
func (m *MockProvider) Status(ctx context.Context, p *profile.Profile) (*provider.ProfileStatus, error) {
	return nil, nil
}
func (m *MockProvider) ValidateProfile(ctx context.Context, p *profile.Profile) error {
	return m.profileError
}
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
		if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(fullPath, []byte("mock-content"), 0600); err != nil {
			return nil, err
		}
	}
	if m.importHook != nil {
		if err := m.importHook(targetProfile); err != nil {
			return nil, err
		}
	}
	return m.mockImportFiles, nil
}
func (m *MockProvider) ValidateToken(ctx context.Context, p *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	m.passive = passive
	if m.validationError != nil || m.nilValidation {
		return nil, m.validationError
	}
	if m.validation != nil {
		return m.validation, nil
	}
	return &provider.ValidationResult{Valid: true, Method: "passive"}, nil
}

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
		// Reset flags that may have been modified during tests
		authDetectCmd.Flags().Set("json", "false")
		authImportCmd.Flags().Set("json", "false")
		authImportCmd.Flags().Set("name", "")
		authImportCmd.Flags().Set("source", "")
	}()

	profileStore = profile.NewStore(storePath)
	nativeSource := filepath.Join(rootDir, ".claude.json")
	require.NoError(t, os.WriteFile(nativeSource, []byte("mock-native-credentials"), 0600))

	// Create mock providers
	mockClaude := &MockProvider{
		id: "claude",
		mockDetection: &provider.AuthDetection{
			Provider: "claude",
			Found:    true,
			Locations: []provider.AuthLocation{
				{Path: nativeSource, Exists: true, IsValid: true},
			},
			Primary: &provider.AuthLocation{
				Path:    nativeSource,
				Exists:  true,
				IsValid: true,
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
	assert.Equal(t, nativeSource, claudeRes.Primary.Path)

	// Mixed-case provider should resolve
	_, err = captureStdout(t, func() error {
		return authDetectCmd.RunE(authDetectCmd, []string{"ClAuDe"})
	})
	require.NoError(t, err)

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
	assert.Contains(t, importRes.CopiedFiles, filepath.Join(importRes.ProfilePath, "imported.json"))

	// Verify profile creation
	prof, err := profileStore.Load("claude", "work")
	require.NoError(t, err)
	assert.Equal(t, "work", prof.Name)

	// Verify mock calls
	assert.True(t, mockClaude.calledPrepare)
	assert.True(t, mockClaude.calledImport)
	assert.True(t, mockClaude.passive, "import must not make a live validation request")

	// Mixed-case provider should import
	authImportCmd.Flags().Set("name", "mixed")
	_, err = captureStdout(t, func() error {
		return authImportCmd.RunE(authImportCmd, []string{"ClAuDe"})
	})
	require.NoError(t, err)

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

func setupAuthImportStore(t *testing.T) *profile.Store {
	t.Helper()
	original := profileStore
	profileStore = profile.NewStore(filepath.Join(t.TempDir(), "profiles"))
	t.Cleanup(func() { profileStore = original })
	return profileStore
}

func TestClaudeNativeImportPreservesSourceAuthority(t *testing.T) {
	const keychainCredential = `{"claudeAiOauth":{"accessToken":"synthetic-personal-access","refreshToken":"synthetic-personal-refresh","expiresAt":4102444800000}}`
	const fileCredential = `{"claudeAiOauth":{"accessToken":"synthetic-work-access","refreshToken":"synthetic-work-refresh","expiresAt":4102444800000}}`
	for _, flow := range []string{"auth import", "init"} {
		for _, layout := range []string{"keychain only", "stale mirror", "explicit config directory"} {
			t.Run(flow+"/"+layout, func(t *testing.T) {
				fixtureRoot := setupInitImport(t)
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				nativeDir := filepath.Join(fixtureRoot, "home", ".claude")
				if layout == "explicit config directory" {
					nativeDir = filepath.Join(t.TempDir(), "work-account")
					t.Setenv("CLAUDE_CONFIG_DIR", nativeDir)
				}
				items := testutil.FakeKeychain(t)
				testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), keychainCredential)
				nativePath := filepath.Join(nativeDir, ".credentials.json")
				var before os.FileInfo
				if layout != "keychain only" {
					writeInitCredential(t, nativePath, fileCredential)
					var err error
					before, err = os.Stat(nativePath)
					require.NoError(t, err)
				}
				assertNativeUnchanged := func() {
					t.Helper()
					if before == nil {
						_, err := os.Stat(nativePath)
						require.True(t, os.IsNotExist(err), "read-only discovery/import created a native mirror: %v", err)
					} else {
						data, err := os.ReadFile(nativePath)
						require.NoError(t, err)
						assert.Equal(t, fileCredential, string(data))
						after, err := os.Stat(nativePath)
						require.NoError(t, err)
						assert.True(t, os.SameFile(before, after))
						assert.Equal(t, before.ModTime(), after.ModTime())
						assert.Equal(t, before.Mode(), after.Mode())
					}
					secret, exists := testutil.FakeKeychainRead(t, items, keychain.ClaudeService)
					assert.True(t, exists)
					assert.Equal(t, keychainCredential, secret)
				}
				prov := claude.New()
				registry = provider.NewRegistry()
				registry.Register(prov)
				detections := detectProviderAuth()
				require.Len(t, detections, 1)
				require.NoError(t, detections[0].Error)
				require.True(t, detections[0].Detection.Found)
				assertNativeUnchanged()

				name := "work"
				if flow == "init" {
					name = "default"
					_, err := captureStdout(t, func() error {
						count, err := importDetectedAuth(context.Background(), detections, true)
						assert.Equal(t, 1, count)
						return err
					})
					require.NoError(t, err)
				} else {
					cmd := &cobra.Command{}
					cmd.SetContext(context.Background())
					cmd.Flags().String("name", name, "")
					cmd.Flags().String("source", "", "")
					cmd.Flags().String("description", "", "")
					cmd.Flags().Bool("force", false, "")
					cmd.Flags().Bool("json", true, "")
					var output bytes.Buffer
					cmd.SetOut(&output)
					require.NoError(t, runAuthImport(cmd, []string{"claude"}))
					var result AuthImportResult
					require.NoError(t, json.Unmarshal(output.Bytes(), &result))
					require.True(t, result.Success)
					assert.NotContains(t, output.String(), "synthetic-personal")
					assert.NotContains(t, output.String(), "synthetic-work")
				}
				prof, err := profileStore.Load("claude", name)
				require.NoError(t, err)
				env, err := prov.Env(context.Background(), prof)
				require.NoError(t, err)
				imported, err := os.ReadFile(filepath.Join(env["CLAUDE_CONFIG_DIR"], ".credentials.json"))
				require.NoError(t, err)
				expected := keychainCredential
				if layout == "explicit config directory" {
					expected = fileCredential
				}
				assert.Equal(t, expected, string(imported))
				validation, err := prov.ValidateToken(context.Background(), prof, true)
				require.NoError(t, err)
				require.True(t, validation.Valid)
				assertNativeUnchanged()
			})
		}
	}
}

func TestAuthImportForcePreservesClaudeHelper(t *testing.T) {
	for _, quoted := range []bool{false, true} {
		t.Run(fmt.Sprintf("quoted_%t", quoted), func(t *testing.T) {
			store := setupAuthImportStore(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_KEYCHAIN", "0")
			old, err := store.Create("claude", "work", "oauth")
			require.NoError(t, err)
			p := claude.New()
			require.NoError(t, p.PrepareProfile(context.Background(), old))
			env, err := p.Env(context.Background(), old)
			require.NoError(t, err)
			helper := filepath.Join(old.BasePath, "helper with 'quote' $literal.sh")
			body := []byte("#!/bin/sh\nprintf '%s' \"$ANTHROPIC_API_KEY\"\n")
			require.NoError(t, os.WriteFile(helper, body, 0700))
			command := helper
			if quoted {
				command = shellQuote(helper)
			}
			source := filepath.Join(env["CLAUDE_CONFIG_DIR"], "settings.json")
			original, err := json.Marshal(map[string]string{"apiKeyHelper": command})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(source, original, 0600))

			result, err := importAuthProfile(context.Background(), p, "work", source, "", true)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Len(t, result.CopiedFiles, 2)
			current, err := store.Load("claude", "work")
			require.NoError(t, err)
			require.Equal(t, "api-key", current.AuthMode)
			currentEnv, err := p.Env(context.Background(), current)
			require.NoError(t, err)
			settingsData, err := os.ReadFile(filepath.Join(currentEnv["CLAUDE_CONFIG_DIR"], "settings.json"))
			require.NoError(t, err)
			var settings struct {
				Helper string `json:"apiKeyHelper"`
			}
			require.NoError(t, json.Unmarshal(settingsData, &settings))
			assert.Equal(t, shellQuote(result.CopiedFiles[1]), settings.Helper)
			copied, err := os.ReadFile(result.CopiedFiles[1])
			require.NoError(t, err)
			assert.Equal(t, body, copied)
			if runtime.GOOS != "windows" {
				info, err := os.Stat(result.CopiedFiles[1])
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
				changes, err := provider.ProfileEnvironment(context.Background(), p, current)
				require.NoError(t, err)
				native := exec.Command("sh", "-c", settings.Helper)
				native.Env = provider.MergeEnvironment([]string{"ANTHROPIC_API_KEY=synthetic-selected-helper-key"}, changes, nil)
				output, err := native.Output()
				require.NoError(t, err)
				assert.Equal(t, "synthetic-selected-helper-key", string(output))
			}
			backupHelper, err := os.ReadFile(filepath.Join(result.BackupPath, filepath.Base(helper)))
			require.NoError(t, err)
			assert.Equal(t, body, backupHelper)
			relativeSource, err := filepath.Rel(old.BasePath, source)
			require.NoError(t, err)
			backupSettings, err := os.ReadFile(filepath.Join(result.BackupPath, relativeSource))
			require.NoError(t, err)
			assert.Equal(t, original, backupSettings)
		})
	}
}

func TestAuthImportMissingClaudeHelperPreservesPreviousProfile(t *testing.T) {
	store := setupAuthImportStore(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CAAM_KEYCHAIN", "0")
	old, err := store.Create("claude", "work", "oauth")
	require.NoError(t, err)
	p := claude.New()
	require.NoError(t, p.PrepareProfile(context.Background(), old))
	env, err := p.Env(context.Background(), old)
	require.NoError(t, err)
	source := filepath.Join(env["CLAUDE_CONFIG_DIR"], "settings.json")
	original, err := json.Marshal(map[string]string{"apiKeyHelper": filepath.Join(old.BasePath, "missing-helper.sh")})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, original, 0600))
	metadata, err := os.ReadFile(old.MetaPath())
	require.NoError(t, err)
	result, err := importAuthProfile(context.Background(), p, "work", source, "", true)
	require.ErrorContains(t, err, "profile-owned apiKeyHelper")
	assert.False(t, result.Success)
	unchanged, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Equal(t, original, unchanged)
	unchanged, err = os.ReadFile(old.MetaPath())
	require.NoError(t, err)
	assert.Equal(t, metadata, unchanged)
	assert.False(t, old.IsLocked())
}

func TestAuthImportFailuresPreserveProfiles(t *testing.T) {
	failure := errors.New("synthetic import failure")
	cases := []struct {
		name      string
		configure func(*MockProvider, context.CancelFunc)
	}{
		{"prepare", func(m *MockProvider, _ context.CancelFunc) { m.prepareError = failure }},
		{"copy", func(m *MockProvider, _ context.CancelFunc) { m.importError = failure }},
		{"partial copy", func(m *MockProvider, _ context.CancelFunc) {
			m.importHook = func(*profile.Profile) error { return failure }
		}},
		{"profile validation", func(m *MockProvider, _ context.CancelFunc) { m.profileError = failure }},
		{"token validation error", func(m *MockProvider, _ context.CancelFunc) { m.validationError = failure }},
		{"invalid token", func(m *MockProvider, _ context.CancelFunc) {
			m.validation = &provider.ValidationResult{Valid: false, Error: "incomplete credential"}
		}},
		{"nil token result", func(m *MockProvider, _ context.CancelFunc) { m.nilValidation = true }},
		{"no copied credentials", func(m *MockProvider, _ context.CancelFunc) { m.mockImportFiles = nil }},
		{"canceled before preparation", func(_ *MockProvider, cancel context.CancelFunc) { cancel() }},
		{"canceled after copy", func(m *MockProvider, cancel context.CancelFunc) {
			m.importHook = func(*profile.Profile) error { cancel(); return nil }
		}},
	}
	for _, tc := range cases {
		for _, replace := range []bool{false, true} {
			name := tc.name + "/new"
			if replace {
				name = tc.name + "/replace"
			}
			t.Run(name, func(t *testing.T) {
				store := setupAuthImportStore(t)
				source := filepath.Join(t.TempDir(), "auth.json")
				require.NoError(t, os.WriteFile(source, []byte("synthetic-source"), 0600))
				var oldMetadata []byte
				if replace {
					old, err := store.Create("test", "work", "oauth")
					require.NoError(t, err)
					old.Description = "keep this account and its history"
					old.Metadata["custom"] = "preserve"
					require.NoError(t, old.Save())
					oldMetadata, err = os.ReadFile(old.MetaPath())
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(old.HomePath(), "history.txt"), []byte("old history"), 0600))
					require.NoError(t, os.WriteFile(filepath.Join(old.BasePath, "auth.json"), []byte("old credential"), 0600))
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				mock := &MockProvider{id: "test", mockImportFiles: []string{"auth.json"}}
				tc.configure(mock, cancel)
				result, err := importAuthProfile(ctx, mock, "work", source, "new description", replace)
				require.Error(t, err)
				assert.False(t, result.Success)
				assert.NotEmpty(t, result.Error)
				assert.Empty(t, result.ProfilePath)
				assert.Empty(t, result.CopiedFiles)
				profiles, err := store.List("test")
				require.NoError(t, err)
				if replace {
					require.Len(t, profiles, 1)
					metadata, err := os.ReadFile(profiles[0].MetaPath())
					require.NoError(t, err)
					assert.Equal(t, oldMetadata, metadata)
					auth, err := os.ReadFile(filepath.Join(profiles[0].BasePath, "auth.json"))
					require.NoError(t, err)
					assert.Equal(t, "old credential", string(auth))
					history, err := os.ReadFile(filepath.Join(profiles[0].HomePath(), "history.txt"))
					require.NoError(t, err)
					assert.Equal(t, "old history", string(history))
					assert.False(t, profiles[0].IsLocked())
				} else {
					assert.Empty(t, profiles)
					_, err := os.Stat(store.ProfilePath("test", "work"))
					assert.True(t, os.IsNotExist(err), "failed new import left a destination: %v", err)
				}
			})
		}
	}
}

func TestAuthImportCommandPublishesValidatedCodex(t *testing.T) {
	store := setupAuthImportStore(t)
	originalRegistry := registry
	registry = provider.NewRegistry()
	registry.Register(codex.New())
	t.Cleanup(func() { registry = originalRegistry })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "native-codex"))
	old, err := store.Create("codex", "work", "oauth")
	require.NoError(t, err)
	old.Description = "previous working account"
	require.NoError(t, old.Save())
	oldMetadata, err := os.ReadFile(old.MetaPath())
	require.NoError(t, err)
	oldAuth := []byte(`{"tokens":{"access_token":"old-access","refresh_token":"old-refresh"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(old.CodexHomePath(), "auth.json"), oldAuth, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(old.HomePath(), "history.txt"), []byte("preserve history"), 0600))

	source := filepath.Join(t.TempDir(), "exported-credential.json")
	credential := []byte(`{"OPENAI_API_KEY":"fixture-api-key","tokens":null}`)
	require.NoError(t, os.WriteFile(source, credential, 0600))
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().String("name", "work", "")
	cmd.Flags().String("source", source, "")
	cmd.Flags().String("description", "imported account", "")
	cmd.Flags().Bool("force", true, "")
	cmd.Flags().Bool("json", true, "")
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.NoError(t, runAuthImport(cmd, []string{"CoDeX"}))
	var result AuthImportResult
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.True(t, result.Success)
	assert.Equal(t, store.ProfilePath("codex", "work"), result.ProfilePath)
	assert.Equal(t, []string{filepath.Join(result.ProfilePath, "codex_home", "auth.json")}, result.CopiedFiles)
	require.NotEmpty(t, result.BackupPath)
	prof, err := store.Load("codex", "work")
	require.NoError(t, err)
	assert.Equal(t, string(provider.AuthModeAPIKey), prof.AuthMode)
	assert.Equal(t, "imported account", prof.Description)
	assert.Equal(t, result.ProfilePath, prof.BasePath)
	assert.False(t, prof.IsLocked())
	actual, err := os.ReadFile(result.CopiedFiles[0])
	require.NoError(t, err)
	assert.Equal(t, credential, actual)
	for path, expected := range map[string][]byte{
		"profile.json":         oldMetadata,
		"codex_home/auth.json": oldAuth,
		"home/history.txt":     []byte("preserve history"),
	} {
		actual, err := os.ReadFile(filepath.Join(result.BackupPath, filepath.FromSlash(path)))
		require.NoError(t, err)
		assert.Equal(t, expected, actual)
	}
	env, err := codex.New().Env(context.Background(), prof)
	require.NoError(t, err)
	assert.Equal(t, prof.CodexHomePath(), env["CODEX_HOME"])
	validation, err := codex.New().ValidateToken(context.Background(), prof, true)
	require.NoError(t, err)
	require.True(t, validation.Valid)
	all, err := store.ListAll()
	require.NoError(t, err)
	require.Len(t, all["codex"], 1)
	assert.Empty(t, all[".imports"])

	// A later failed forced replacement must preserve the newly working login.
	require.NoError(t, os.WriteFile(source, []byte(`{"tokens":null}`), 0600))
	output.Reset()
	require.Error(t, runAuthImport(cmd, []string{"codex"}))
	var rejected AuthImportResult
	require.NoError(t, json.Unmarshal(output.Bytes(), &rejected))
	assert.False(t, rejected.Success)
	assert.NotEmpty(t, rejected.Error)
	actual, err = os.ReadFile(result.CopiedFiles[0])
	require.NoError(t, err)
	assert.Equal(t, credential, actual)

	// Importing from the profile being replaced must capture it before moving it.
	require.NoError(t, cmd.Flags().Set("source", result.CopiedFiles[0]))
	output.Reset()
	require.NoError(t, runAuthImport(cmd, []string{"codex"}))
	var repeated AuthImportResult
	require.NoError(t, json.Unmarshal(output.Bytes(), &repeated))
	assert.True(t, repeated.Success)
	actual, err = os.ReadFile(repeated.CopiedFiles[0])
	require.NoError(t, err)
	assert.Equal(t, credential, actual)
}

func TestAuthImportOtherNativeProviders(t *testing.T) {
	expiredAt := time.Now().Add(-time.Hour).UnixMilli()
	cases := []struct {
		name     string
		prov     provider.Provider
		filename string
		data     string
		valid    bool
	}{
		{"Grok native", grok.New(), "auth.json", `{"key":"fixture-access","refreshToken":"fixture-refresh"}`, true},
		{"Grok empty object", grok.New(), "auth.json", `{}`, false},
		{"Grok null", grok.New(), "auth.json", `null`, false},
		{"Grok null token", grok.New(), "auth.json", `{"key":null}`, false},
		{"Grok malformed token", grok.New(), "auth.json", `{"key":42}`, false},
		{"OpenCode native", opencode.New(), "auth.json", `{"anthropic":{"type":"oauth","access":"fixture-access","refresh":"fixture-refresh"}}`, true},
		{"OpenCode API key", opencode.New(), "auth.json", `{"openai":{"type":"api","key":"fixture-key"}}`, true},
		{"OpenCode empty object", opencode.New(), "auth.json", `{}`, false},
		{"OpenCode incomplete", opencode.New(), "auth.json", `{"anthropic":{"refresh":"fixture-refresh"}}`, false},
		{"Antigravity native", agy.New(), "antigravity-oauth-token", "fixture-access", true},
		{"Antigravity whitespace", agy.New(), "antigravity-oauth-token", " \n\t", false},
		{"Antigravity null", agy.New(), "antigravity-oauth-token", "null", false},
		{"Antigravity empty object", agy.New(), "antigravity-oauth-token", `{}`, false},
		{"Cursor native", cursor.New(), "auth.json", `{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}`, true},
		{"Cursor empty object", cursor.New(), "auth.json", `{}`, false},
		{"Claude native", claude.New(), ".credentials.json", `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":4102444800000}}`, true},
		{"Claude renewable expired access", claude.New(), ".credentials.json", fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":%d}}`, expiredAt), true},
		{"Claude expired without refresh", claude.New(), ".credentials.json", fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"fixture-access","expiresAt":%d}}`, expiredAt), false},
		{"Claude empty object", claude.New(), ".credentials.json", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := setupAuthImportStore(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			source := filepath.Join(t.TempDir(), tc.filename)
			require.NoError(t, os.WriteFile(source, []byte(tc.data), 0600))
			before, err := os.Stat(source)
			require.NoError(t, err)
			result, err := importAuthProfile(context.Background(), tc.prov, "native", source, "", false)
			if !tc.valid {
				require.Error(t, err)
				assert.False(t, result.Success)
				assert.False(t, store.Exists(tc.prov.ID(), "native"))
				return
			}
			require.NoError(t, err)
			require.True(t, result.Success)
			prof, err := store.Load(tc.prov.ID(), "native")
			require.NoError(t, err)
			validation, err := tc.prov.ValidateToken(context.Background(), prof, true)
			require.NoError(t, err)
			require.NotNil(t, validation)
			assert.True(t, validation.Valid, validation.Error)
			assert.Equal(t, "passive", validation.Method)
			for _, path := range result.CopiedFiles {
				info, err := os.Stat(path)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			}
			after, err := os.Stat(source)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after))
			assert.WithinDuration(t, before.ModTime(), after.ModTime(), time.Nanosecond)
			actual, err := os.ReadFile(source)
			require.NoError(t, err)
			assert.Equal(t, tc.data, string(actual))
		})
	}
}
