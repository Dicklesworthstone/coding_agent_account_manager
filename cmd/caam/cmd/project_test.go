package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
)

func TestProjectSetRemoveClear(t *testing.T) {
	tmpDir := t.TempDir()
	cwd := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	oldWD, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	oldStore := projectStore
	projectStore = project.NewStore(filepath.Join(tmpDir, "projects.json"))
	t.Cleanup(func() { projectStore = oldStore })

	if err := projectSetCmd.RunE(projectSetCmd, []string{"claude", "work@company.com"}); err != nil {
		t.Fatalf("project set error = %v", err)
	}

	data, err := projectStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// The command keys the association by os.Getwd(), which resolves
	// symlinks (macOS temp dirs live under /var -> /private/var).
	abs, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if got := data.Associations[abs]["claude"]; got != "work@company.com" {
		t.Fatalf("associations[%s][claude] = %q, want %q", abs, got, "work@company.com")
	}

	if err := projectRemoveCmd.RunE(projectRemoveCmd, []string{"claude"}); err != nil {
		t.Fatalf("project remove error = %v", err)
	}
	data, err = projectStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := data.Associations[abs]; ok {
		t.Fatalf("expected associations[%s] to be removed after provider delete", abs)
	}

	if err := projectSetCmd.RunE(projectSetCmd, []string{"codex", "work"}); err != nil {
		t.Fatalf("project set error = %v", err)
	}
	if err := projectClearCmd.RunE(projectClearCmd, nil); err != nil {
		t.Fatalf("project clear error = %v", err)
	}
	data, err = projectStore.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(data.Associations) != 0 {
		t.Fatalf("associations size = %d, want 0", len(data.Associations))
	}
}

func TestActivate_UsesProjectAssociationWhenProfileOmitted(t *testing.T) {
	tmpDir := t.TempDir()

	// Ensure provider auth files and SPM config paths stay within tmpDir.
	oldCodexHome := os.Getenv("CODEX_HOME")
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() {
		_ = os.Setenv("CODEX_HOME", oldCodexHome)
		_ = os.Setenv("CAAM_HOME", oldCaamHome)
	})

	codexHome := filepath.Join(tmpDir, "codex-home")
	_ = os.Setenv("CODEX_HOME", codexHome)
	_ = os.Setenv("CAAM_HOME", tmpDir)

	// Set up vault with a codex/work profile.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("codex", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(profileDir) error = %v", err)
	}
	wantAuth := `{"access_token":"token-for-work"}`
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(wantAuth), 0600); err != nil {
		t.Fatalf("WriteFile(auth.json) error = %v", err)
	}

	// Associate CWD with codex/work.
	oldProjectStore := projectStore
	projectStore = project.NewStore(filepath.Join(tmpDir, "projects.json"))
	t.Cleanup(func() { projectStore = oldProjectStore })

	// Initialize health store for refresh checks
	oldHealthStore := healthStore
	healthStore = health.NewStorage(filepath.Join(tmpDir, "health.json"))
	t.Cleanup(func() { healthStore = oldHealthStore })

	cwd := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatalf("MkdirAll(cwd) error = %v", err)
	}
	oldWD, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// Key by the symlink-resolved path, which is what os.Getwd() reports
	// inside runActivate (macOS temp dirs live under /var -> /private/var).
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if err := projectStore.SetAssociation(resolved, "codex", "work"); err != nil {
		t.Fatalf("SetAssociation() error = %v", err)
	}

	// Ensure cfg exists for default fallback path (not used here).
	oldCfg := cfg
	cfg = config.DefaultConfig()
	t.Cleanup(func() { cfg = oldCfg })

	// Run activate without profile name; should use project association.
	if err := runActivate(activateCmd, []string{"codex"}); err != nil {
		t.Fatalf("activate error = %v", err)
	}

	gotAuth, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile(auth.json) error = %v", err)
	}
	if string(gotAuth) != wantAuth {
		t.Fatalf("auth.json = %q, want %q", string(gotAuth), wantAuth)
	}
}
