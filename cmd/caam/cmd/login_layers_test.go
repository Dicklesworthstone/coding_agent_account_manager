package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
)

func writeLayerFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Issue #76: after `caam login codex <p>` the vault and shallow layers keep
// the generation the login replaced. The classification must flag exactly the
// layers that still serve an older generation.
func TestFindStaleLoginLayers(t *testing.T) {
	dir := t.TempDir()
	prev := []byte(`{"tokens":{"access_token":"old"}}`)
	fresh := []byte(`{"tokens":{"access_token":"new"}}`)
	other := []byte(`{"tokens":{"access_token":"someone-else"}}`)

	vaultReplaced := filepath.Join(dir, "vault", "auth.json")
	shallowReplaced := filepath.Join(dir, "shallow-replaced", ".codex", "auth.json")
	shallowConverged := filepath.Join(dir, "shallow-converged", ".codex", "auth.json")
	shallowOtherNamed := filepath.Join(dir, "shallow-other", ".codex", "auth.json")
	shallowOtherAccount := filepath.Join(dir, "shallow-foreign", ".codex", "auth.json")
	missing := filepath.Join(dir, "missing", "auth.json")

	writeLayerFile(t, vaultReplaced, prev)
	writeLayerFile(t, shallowReplaced, prev)
	writeLayerFile(t, shallowConverged, fresh)
	writeLayerFile(t, shallowOtherNamed, other)
	writeLayerFile(t, shallowOtherAccount, other)

	candidates := []loginLayerCandidate{
		{Kind: "vault", Label: "codex/work", Path: vaultReplaced, NameMatched: true},
		{Kind: "shallow", Label: "codex-work", Path: shallowReplaced, NameMatched: true},
		{Kind: "shallow", Label: "work", Path: shallowConverged, NameMatched: true},
		{Kind: "shallow", Label: "work-alt", Path: shallowOtherNamed, NameMatched: true},
		{Kind: "shallow", Label: "codex-personal", Path: shallowOtherAccount, NameMatched: false},
		{Kind: "vault", Label: "codex/missing", Path: missing, NameMatched: true},
	}

	got := findStaleLoginLayers(prev, fresh, candidates)

	want := map[string]bool{ // label -> Replaced
		"codex/work": true,
		"codex-work": true,
		"work-alt":   false,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d stale layers, want %d: %+v", len(got), len(want), got)
	}
	for _, s := range got {
		replaced, ok := want[s.Label]
		if !ok {
			t.Errorf("unexpected stale layer %+v", s)
			continue
		}
		if s.Replaced != replaced {
			t.Errorf("%s: Replaced = %v, want %v", s.Label, s.Replaced, replaced)
		}
	}
}

func TestFindStaleLoginLayers_ForeignLayerHoldingReplacedGeneration(t *testing.T) {
	// A shallow profile with an unrelated name that holds the exact generation
	// the login replaced is still a revoked copy and must be reported.
	dir := t.TempDir()
	prev := []byte("old")
	fresh := []byte("new")
	path := filepath.Join(dir, "codex-swarm-3", ".codex", "auth.json")
	writeLayerFile(t, path, prev)

	got := findStaleLoginLayers(prev, fresh, []loginLayerCandidate{
		{Kind: "shallow", Label: "codex-swarm-3", Path: path, NameMatched: false},
	})
	if len(got) != 1 || !got[0].Replaced {
		t.Fatalf("expected the replaced generation to be flagged, got %+v", got)
	}
}

func TestFindStaleLoginLayers_NoPreviousCredential(t *testing.T) {
	// First login into an empty profile: only same-named layers that differ
	// from the fresh credential are reported, and nothing is marked replaced.
	dir := t.TempDir()
	fresh := []byte("new")
	named := filepath.Join(dir, "vault", "auth.json")
	foreign := filepath.Join(dir, "foreign", "auth.json")
	writeLayerFile(t, named, []byte("older"))
	writeLayerFile(t, foreign, []byte("older"))

	got := findStaleLoginLayers(nil, fresh, []loginLayerCandidate{
		{Kind: "vault", Label: "codex/work", Path: named, NameMatched: true},
		{Kind: "shallow", Label: "codex-other", Path: foreign, NameMatched: false},
	})
	if len(got) != 1 || got[0].Label != "codex/work" || got[0].Replaced {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestFindStaleLoginLayers_EmptyFreshIsNoop(t *testing.T) {
	if got := findStaleLoginLayers([]byte("old"), nil, []loginLayerCandidate{{Path: "/nonexistent"}}); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestCollectLoginLayerCandidates(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "home")
	base := filepath.Join(root, "orch-homes")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}

	v := authfile.NewVault(filepath.Join(root, "vault"))
	writeLayerFile(t, v.BackupPath("codex", "work", "auth.json"), []byte("vault-gen"))

	mgr, err := shallow.NewManager(base, realHome)
	if err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(root, "seed.json")
	writeLayerFile(t, cred, []byte("shallow-gen"))
	for _, spec := range []struct{ name, provider string }{
		{"codex-work", "codex"},
		{"work", "codex"},
		{"codex-other", "codex"},
		{"claude-work", "claude"},
	} {
		if _, err := mgr.Create(spec.name, shallow.CreateOptions{Provider: spec.provider, CredentialSource: cred}); err != nil {
			t.Fatalf("create %s: %v", spec.name, err)
		}
	}

	got := collectLoginLayerCandidates("codex", "work", v, mgr)

	byLabel := map[string]loginLayerCandidate{}
	for _, c := range got {
		byLabel[c.Label] = c
	}
	if len(got) != 4 {
		t.Fatalf("expected vault + 3 codex shallow candidates, got %d: %+v", len(got), got)
	}
	if c := byLabel["codex/work"]; c.Kind != "vault" || !c.NameMatched || c.Path != v.BackupPath("codex", "work", "auth.json") {
		t.Errorf("vault candidate wrong: %+v", c)
	}
	for _, name := range []string{"codex-work", "work"} {
		c, ok := byLabel[name]
		if !ok || c.Kind != "shallow" || !c.NameMatched {
			t.Errorf("shallow %s should be name-matched: %+v (present=%v)", name, c, ok)
		}
		if !strings.HasSuffix(c.Path, filepath.Join(name, ".codex", "auth.json")) {
			t.Errorf("shallow %s path wrong: %s", name, c.Path)
		}
		if data, err := os.ReadFile(c.Path); err != nil || !bytes.Equal(data, []byte("shallow-gen")) {
			t.Errorf("shallow %s credential not readable at %s: %v", name, c.Path, err)
		}
	}
	if c := byLabel["codex-other"]; c.Kind != "shallow" || c.NameMatched {
		t.Errorf("codex-other must be present but not name-matched: %+v", c)
	}
	if _, ok := byLabel["claude-work"]; ok {
		t.Error("claude shallow profile must not be a candidate for a codex login")
	}
}

func TestCollectLoginLayerCandidates_NilVaultAndEmptyBase(t *testing.T) {
	root := t.TempDir()
	mgr, err := shallow.NewManager(filepath.Join(root, "orch-homes"), filepath.Join(root, "home"))
	if err != nil {
		t.Fatal(err)
	}
	if got := collectLoginLayerCandidates("codex", "work", nil, mgr); len(got) != 0 {
		t.Fatalf("expected no candidates, got %+v", got)
	}
	// A tool without a shallow layout still yields its vault candidate only.
	v := authfile.NewVault(filepath.Join(root, "vault"))
	got := collectLoginLayerCandidates("gemini", "work", v, mgr)
	if len(got) != 1 || got[0].Kind != "vault" {
		t.Fatalf("expected the vault candidate only, got %+v", got)
	}
}

func TestPrintStaleLoginLayers(t *testing.T) {
	prof := &profile.Profile{Name: "work", Provider: "codex", BasePath: "/data/caam/profiles/codex/work"}
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	stale := []staleLoginLayer{
		{Kind: "vault", Label: "codex/work", Path: "/data/caam/vault/codex/work/auth.json", Replaced: true},
		{Kind: "shallow", Label: "codex-work", Path: "/home/u/orch-homes/codex-work/.codex/auth.json", Replaced: false},
	}

	var out bytes.Buffer
	printStaleLoginLayers(&out, "codex", "work", prof, authPath, stale)
	text := out.String()

	for _, want := range []string{
		"Warning: this login updated only the isolated profile credential",
		authPath,
		"/data/caam/vault/codex/work/auth.json",
		"holds the generation this login replaced",
		"/home/u/orch-homes/codex-work/.codex/auth.json",
		"differs from this login",
		"CODEX_HOME=/data/caam/profiles/codex/work/codex_home caam backup codex work",
		"caam shallow-profile create codex-work --tool codex --from-file " + authPath + " --force",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	printStaleLoginLayers(&out, "codex", "work", prof, authPath, nil)
	if out.Len() != 0 {
		t.Errorf("converged layers must print nothing, got %q", out.String())
	}
}
