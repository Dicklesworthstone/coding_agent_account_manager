package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/gemini"
)

const initCodexCredential = `{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-codex-access","refresh_token":"synthetic-codex-refresh"}}`
const initGeminiCredential = `{"access_token":"synthetic-gemini-access","refresh_token":"synthetic-gemini-refresh"}`

func setupInitImport(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for key, value := range map[string]string{
		"HOME": filepath.Join(root, "home"), "CAAM_HOME": filepath.Join(root, "caam"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"),
		"XDG_STATE_HOME": filepath.Join(root, "state"), "CODEX_HOME": filepath.Join(root, "native-codex"),
		"GEMINI_HOME": filepath.Join(root, "native-gemini"), "GEMINI_CLI_HOME": "",
		"CLOUDSDK_CONFIG": filepath.Join(root, "native-gcloud"), "GOOGLE_APPLICATION_CREDENTIALS": "",
		"GEMINI_API_KEY": "", "GOOGLE_API_KEY": "", "OPENAI_API_KEY": "",
	} {
		t.Setenv(key, value)
	}
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_GENAI_USE_GCA", "GOOGLE_GENAI_USE_VERTEXAI"} {
		t.Setenv(key, "") // Let native dotenv credentials load as they do in a clean shell.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(os.Getenv("HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	oldStore, oldRegistry := profileStore, registry
	t.Cleanup(func() { profileStore, registry = oldStore, oldRegistry })
	profileStore = profile.NewStore(profile.DefaultStorePath())
	registry = provider.NewRegistry()
	registry.Register(codex.New())
	registry.Register(gemini.New())
	return root
}

func writeInitCredential(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func seedInitCredentials(t *testing.T) []ProviderAuthDetection {
	t.Helper()
	writeInitCredential(t, filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), initCodexCredential)
	writeInitCredential(t, filepath.Join(os.Getenv("GEMINI_HOME"), "oauth_creds.json"), initGeminiCredential)
	detections := detectProviderAuth()
	if len(detections) != 2 {
		t.Fatalf("provider detection count = %d, want 2", len(detections))
	}
	for _, d := range detections {
		if d.Error != nil || d.Detection == nil || !d.Detection.Found || d.Detection.Primary == nil {
			t.Fatalf("real native fixture was not detected: %+v", d)
		}
	}
	return detections
}

func TestInitImportContinuesAfterAnotherProviderFails(t *testing.T) {
	for _, failed := range []string{"codex", "gemini"} {
		t.Run(failed, func(t *testing.T) {
			setupInitImport(t)
			detections := seedInitCredentials(t)
			if detections[0].ProviderID != failed {
				detections[0], detections[1] = detections[1], detections[0]
			}
			// A native CLI can change a credential between detection and import.
			writeInitCredential(t, detections[0].Detection.Primary.Path, `{"access_token":null}`)
			count, err := importDetectedAuth(context.Background(), detections, true)
			if count != 1 || err == nil {
				t.Fatalf("partial import = %d, %v; want one committed profile and an error", count, err)
			}
			if _, err := os.Stat(profileStore.ProfilePath(failed, "default")); !os.IsNotExist(err) {
				t.Fatalf("failed %s import left a partial profile: %v", failed, err)
			}
			other := detections[1].ProviderID
			prof, err := profileStore.Load(other, "default")
			if err != nil {
				t.Fatalf("valid %s import was skipped: %v", other, err)
			}
			prov, _ := registry.Get(other)
			validation, err := prov.ValidateToken(context.Background(), prof, true)
			if err != nil || validation == nil || !validation.Valid {
				t.Fatalf("published %s profile is unusable: %+v, %v", other, validation, err)
			}
		})
	}
}

func TestInitImportPersistsActualGeminiMode(t *testing.T) {
	for _, mode := range []provider.AuthMode{provider.AuthModeAPIKey, provider.AuthModeVertexADC} {
		t.Run(string(mode), func(t *testing.T) {
			setupInitImport(t)
			writeInitCredential(t, filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), initCodexCredential)
			if mode == provider.AuthModeAPIKey {
				writeInitCredential(t, filepath.Join(os.Getenv("GEMINI_HOME"), ".env"), "GEMINI_API_KEY=synthetic-init-key\n")
				writeInitCredential(t, filepath.Join(os.Getenv("GEMINI_HOME"), "settings.json"), `{"security":{"auth":{"selectedType":"gemini-api-key"}},"theme":"private"}`)
			} else {
				writeInitCredential(t, filepath.Join(os.Getenv("GEMINI_HOME"), "settings.json"), `{"security":{"auth":{"selectedType":"vertex-ai"}},"theme":"private"}`)
				writeInitCredential(t, filepath.Join(os.Getenv("CLOUDSDK_CONFIG"), "application_default_credentials.json"), `{"type":"authorized_user","client_id":"synthetic-client","client_secret":"synthetic-secret","refresh_token":"synthetic-refresh"}`)
			}
			count, err := importDetectedAuth(context.Background(), detectProviderAuth(), true)
			if err != nil || count != 2 {
				t.Fatalf("mixed provider import = %d, %v", count, err)
			}
			prof, err := profileStore.Load("gemini", "default")
			if err != nil || prof.AuthMode != string(mode) || prof.BasePath != profileStore.ProfilePath("gemini", "default") {
				t.Fatalf("saved Gemini metadata = %+v, %v", prof, err)
			}
			env, err := gemini.New().Env(context.Background(), prof)
			if err != nil {
				t.Fatal(err)
			}
			if mode == provider.AuthModeAPIKey && env["GEMINI_API_KEY"] != "synthetic-init-key" {
				t.Fatal("init imported API key is not selected at runtime")
			}
			if mode == provider.AuthModeVertexADC && env["GOOGLE_APPLICATION_CREDENTIALS"] != filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json") {
				t.Fatal("init imported ADC is not selected at runtime")
			}
		})
	}
}

func TestInitImportPreservesExistingProfilesOnRepeat(t *testing.T) {
	setupInitImport(t)
	detections := seedInitCredentials(t)
	if count, err := importDetectedAuth(context.Background(), detections, true); err != nil || count != 2 {
		t.Fatalf("first import = %d, %v", count, err)
	}
	snapshots := make(map[string][]byte)
	for _, name := range []string{"codex", "gemini"} {
		prof, err := profileStore.Load(name, "default")
		if err != nil {
			t.Fatal(err)
		}
		prof.Description = "existing private notes"
		prof.Tags = []string{"work"}
		if err := prof.Save(); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{prof.MetaPath(), filepath.Join(prof.HomePath(), ".gemini", "oauth_creds.json"), filepath.Join(prof.CodexHomePath(), "auth.json")} {
			if data, err := os.ReadFile(path); err == nil {
				snapshots[path] = data
			}
		}
	}
	for _, d := range detections {
		writeInitCredential(t, d.Detection.Primary.Path, `{"access_token":"different-new-native-account"}`)
	}
	if count, err := importDetectedAuth(context.Background(), detectProviderAuth(), true); err != nil || count != 0 {
		t.Fatalf("repeated import = %d, %v", count, err)
	}
	for path, want := range snapshots {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("repeated init changed existing account file %s: %v", path, err)
		}
	}
}

func TestInitImportRejectsInvalidDetectionWithoutBlockingOtherProvider(t *testing.T) {
	for _, kind := range []string{"nil detection", "nil primary", "invalid primary", "missing primary", "wrong provider", "detection error"} {
		t.Run(kind, func(t *testing.T) {
			setupInitImport(t)
			detections := seedInitCredentials(t)
			if detections[0].ProviderID != "codex" {
				detections[0], detections[1] = detections[1], detections[0]
			}
			switch kind {
			case "nil detection":
				detections[0].Detection = nil
			case "nil primary":
				detections[0].Detection.Primary = nil
			case "invalid primary":
				detections[0].Detection.Primary.IsValid = false
			case "missing primary":
				detections[0].Detection.Primary.Exists = false
			case "wrong provider":
				detections[0].Detection.Provider = "gemini"
			case "detection error":
				detections[0].Error = errors.New("synthetic detector failure")
			}
			// Both reporting paths must also tolerate incomplete adapter results.
			printProviderDetectionResults(detections)
			printSetupSummaryV2(detections, 0, false)
			if count, err := importDetectedAuth(context.Background(), detections, true); err == nil || count != 1 {
				t.Fatalf("invalid detection handling = %d, %v", count, err)
			}
			if profileStore.Exists("codex", "default") || !profileStore.Exists("gemini", "default") {
				t.Fatal("invalid detection was imported or blocked the independent Gemini account")
			}
		})
	}
}

func TestInitWizardDoesNotFallbackToUnvalidatedLegacyBackup(t *testing.T) {
	setupInitImport(t)
	writeInitCredential(t, filepath.Join(os.Getenv("GEMINI_HOME"), "settings.json"), `{"email":"policy-only@example.com","theme":"private"}`)
	cmd := initImportCommand(context.Background())
	if _, err := captureStdout(t, func() error { return runInitWizard(cmd, nil) }); err != nil {
		t.Fatal(err)
	}
	profiles, err := profileStore.ListAll()
	if err != nil || len(profiles) != 0 {
		t.Fatalf("policy-only auth became an isolated profile: %+v, %v", profiles, err)
	}
	saved, err := authfile.NewVault(authfile.DefaultVaultPath()).List("gemini")
	if err != nil || len(saved) != 0 {
		t.Fatalf("rejected settings were rescued by legacy backup: %v, %v", saved, err)
	}
}

func TestInitImportCancellationLeavesNoPublishedProfiles(t *testing.T) {
	for _, duringPrepare := range []bool{false, true} {
		t.Run(map[bool]string{false: "before import", true: "during preparation"}[duringPrepare], func(t *testing.T) {
			setupInitImport(t)
			detections := seedInitCredentials(t)
			if detections[0].ProviderID != "codex" {
				detections[0], detections[1] = detections[1], detections[0]
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if duringPrepare {
				registry.Register(&initCancelProvider{Provider: codex.New(), cancel: cancel})
			} else {
				cancel()
			}
			if count, err := importDetectedAuth(ctx, detections, true); count != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled import = %d, %v", count, err)
			}
			if profileStore.Exists("codex", "default") || profileStore.Exists("gemini", "default") {
				t.Fatal("canceled init published a profile")
			}
			if err := runInitWizard(initImportCommand(ctx), nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("wizard ignored its command context: %v", err)
			}
		})
	}
}

type initCancelProvider struct {
	provider.Provider
	cancel context.CancelFunc
}

func (p *initCancelProvider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	if err := p.Provider.PrepareProfile(ctx, prof); err != nil {
		return err
	}
	p.cancel()
	return nil
}

func initImportCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{Use: "init"}
	cmd.SetContext(ctx)
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("quick", true, "")
	cmd.Flags().Bool("no-shell", true, "")
	return cmd
}
