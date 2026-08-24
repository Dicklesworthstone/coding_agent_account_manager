package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSyncMode(t *testing.T) {
	cases := []struct {
		in   string
		want SyncMode
		ok   bool
	}{
		{"replicate", ModeReplicate, true},
		{"host-local", ModeHostLocal, true},
		{" Host-Local ", ModeHostLocal, true},
		{"REPLICATE", ModeReplicate, true},
		{"handoff", "", false}, // deferred mode: not accepted yet
		{"", "", false},
		{"hostlocal", "", false},
	}
	for _, c := range cases {
		got, ok := ParseSyncMode(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseSyncMode(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDefaultModeForProvider(t *testing.T) {
	hostLocal := []string{"claude", "codex", "CLAUDE", " codex ", "some-future-tool"}
	for _, p := range hostLocal {
		if got := DefaultModeForProvider(p); got != ModeHostLocal {
			t.Errorf("DefaultModeForProvider(%q) = %q, want host-local", p, got)
		}
	}
	replicate := []string{"gemini", "agy", "grok", "opencode", "cursor"}
	for _, p := range replicate {
		if got := DefaultModeForProvider(p); got != ModeReplicate {
			t.Errorf("DefaultModeForProvider(%q) = %q, want replicate", p, got)
		}
	}
}

func TestProviderRotatesCredentials(t *testing.T) {
	if !ProviderRotatesCredentials("codex") || !ProviderRotatesCredentials("claude") {
		t.Error("codex and claude must be classified as rotating")
	}
	if ProviderRotatesCredentials("gemini") || ProviderRotatesCredentials("unknown") {
		t.Error("gemini/unknown must not be classified as rotating")
	}
}

func TestPolicyResolverPrecedence(t *testing.T) {
	r := NewPolicyResolver(
		map[string]string{"claude": "replicate", "gemini": "host-local", "codex": "bogus-mode"},
		map[string]string{"claude/work": "host-local", "Gemini/Main": "replicate"},
	)

	// Profile override beats provider override.
	if got := r.ModeFor("claude", "work"); got != ModeHostLocal {
		t.Errorf("claude/work = %q, want host-local (profile override)", got)
	}
	// Provider override beats capability default.
	if got := r.ModeFor("claude", "personal"); got != ModeReplicate {
		t.Errorf("claude/personal = %q, want replicate (provider override)", got)
	}
	if got := r.ModeFor("gemini", "other"); got != ModeHostLocal {
		t.Errorf("gemini/other = %q, want host-local (provider override)", got)
	}
	// Provider segment of a profile key is case-insensitive…
	if got := r.ModeFor("gemini", "Main"); got != ModeReplicate {
		t.Errorf("gemini/Main = %q, want replicate (provider segment case-insensitive)", got)
	}
	// …but the profile segment is case-SENSITIVE, matching vault dir names.
	if got := r.ModeFor("gemini", "main"); got != ModeHostLocal {
		t.Errorf("gemini/main = %q, want host-local (profile name case differs from override)", got)
	}
	// Invalid override value is ignored → capability default applies (fail closed).
	if got := r.ModeFor("codex", "anything"); got != ModeHostLocal {
		t.Errorf("codex with invalid override = %q, want host-local default", got)
	}
	inv := r.InvalidEntries()
	if len(inv) != 1 || !strings.Contains(inv[0], "bogus-mode") {
		t.Errorf("InvalidEntries = %v, want one entry mentioning bogus-mode", inv)
	}

	// Nil resolver falls back to defaults.
	var nilR *PolicyResolver
	if got := nilR.ModeFor("codex", "x"); got != ModeHostLocal {
		t.Errorf("nil resolver codex = %q, want host-local", got)
	}
}

func TestExplain(t *testing.T) {
	r := NewPolicyResolver(map[string]string{"claude": "replicate"}, nil)

	mode, why := r.Explain("claude")
	if mode != ModeReplicate || why != "configured override" {
		t.Errorf("Explain(claude) = (%q, %q), want (replicate, configured override)", mode, why)
	}
	mode, why = r.Explain("codex")
	if mode != ModeHostLocal || why != "rotating OAuth default" {
		t.Errorf("Explain(codex) = (%q, %q)", mode, why)
	}
	mode, why = r.Explain("gemini")
	if mode != ModeReplicate || why != "default" {
		t.Errorf("Explain(gemini) = (%q, %q)", mode, why)
	}
	mode, why = r.Explain("mystery")
	if mode != ModeHostLocal || !strings.Contains(why, "fail closed") {
		t.Errorf("Explain(mystery) = (%q, %q)", mode, why)
	}
}

func TestLoadPolicyResolverFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Legacy config without sync_policy → capability defaults.
	cfgDir := filepath.Join(dir, "caam")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"default_provider":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := LoadPolicyResolver()
	if got := r.ModeFor("codex", "x"); got != ModeHostLocal {
		t.Errorf("legacy config: codex = %q, want host-local default", got)
	}
	if got := r.ModeFor("gemini", "x"); got != ModeReplicate {
		t.Errorf("legacy config: gemini = %q, want replicate default", got)
	}

	// Config with overrides round-trips through the real JSON shape.
	raw := map[string]any{
		"sync_policy": map[string]any{
			"providers": map[string]string{"codex": "replicate"},
			"profiles":  map[string]string{"codex/pinned": "host-local"},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	r = LoadPolicyResolver()
	if got := r.ModeFor("codex", "free"); got != ModeReplicate {
		t.Errorf("override config: codex/free = %q, want replicate", got)
	}
	if got := r.ModeFor("codex", "pinned"); got != ModeHostLocal {
		t.Errorf("override config: codex/pinned = %q, want host-local", got)
	}
}

func TestFilterMetadataFiles(t *testing.T) {
	files := map[string][]byte{
		"meta.json":         []byte(`{"tool":"codex"}`),
		"auth.json":         []byte(`{"refresh_token":"secret"}`),
		".credentials.json": []byte(`{"claudeAiOauth":{}}`),
		".claude.json":      []byte(`{}`),
		"settings.json":     []byte(`{}`),
		"novel-file.bin":    []byte("opaque"), // unknown filenames are payload: fail closed
	}
	got := filterMetadataFiles(files)
	if len(got) != 1 {
		t.Fatalf("filterMetadataFiles kept %d files (%v), want exactly meta.json", len(got), got)
	}
	if _, ok := got["meta.json"]; !ok {
		t.Fatal("meta.json missing from filtered set")
	}
}

func TestApplyHostLocalDecision(t *testing.T) {
	fresh := func(exp time.Time) *TokenFreshness {
		return &TokenFreshness{Provider: "codex", Profile: "p", ExpiresAt: exp}
	}
	now := time.Now()

	newOp := func() *SyncOperation {
		return &SyncOperation{Provider: "codex", Profile: "p"}
	}

	t.Run("both absent → silent skip", func(t *testing.T) {
		op := applyHostLocalDecision(newOp(), false, false, nil, nil, false)
		if op.Direction != SyncSkip || op.Note != "" {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
		if !op.PayloadExcluded || op.Mode != ModeHostLocal {
			t.Error("mode/payload flags not set")
		}
	})

	t.Run("local only → metadata push", func(t *testing.T) {
		op := applyHostLocalDecision(newOp(), true, false, fresh(now), nil, false)
		if op.Direction != SyncPush || op.Note != noteHostLocalMetadataOnly {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
	})

	t.Run("remote only → metadata pull", func(t *testing.T) {
		op := applyHostLocalDecision(newOp(), false, true, nil, fresh(now), false)
		if op.Direction != SyncPull || op.Note != noteHostLocalMetadataOnly {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
	})

	t.Run("both, divergent freshness → skip with divergence note", func(t *testing.T) {
		op := applyHostLocalDecision(newOp(), true, true, fresh(now.Add(time.Hour)), fresh(now), false)
		if op.Direction != SyncSkip || op.Note != noteHostLocalDiverged {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
	})

	t.Run("both, equal freshness → silent skip", func(t *testing.T) {
		f := fresh(now)
		op := applyHostLocalDecision(newOp(), true, true, f, f, false)
		if op.Direction != SyncSkip || op.Note != "" {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
	})

	t.Run("both, unknown freshness → silent skip", func(t *testing.T) {
		op := applyHostLocalDecision(newOp(), true, true, nil, nil, false)
		if op.Direction != SyncSkip || op.Note != "" {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
	})

	t.Run("system profile never propagates, even one-sided", func(t *testing.T) {
		op := applyHostLocalDecision(newOp(), true, false, fresh(now), nil, true)
		if op.Direction != SyncSkip || op.Note != "" {
			t.Errorf("got (%q, %q)", op.Direction, op.Note)
		}
	})
}

func TestDowngradeIfNoMetadata(t *testing.T) {
	mk := func(dir SyncDirection, excluded bool, note string) *SyncOperation {
		return &SyncOperation{Direction: dir, PayloadExcluded: excluded, Note: note}
	}

	// Metadata-only push with nothing to offer → silent skip.
	op := downgradeIfNoMetadata(mk(SyncPush, true, noteHostLocalMetadataOnly), false)
	if op.Direction != SyncSkip || op.Note != "" {
		t.Errorf("push without metadata: got (%q, %q), want silent skip", op.Direction, op.Note)
	}
	// Metadata-only pull with metadata present → unchanged.
	op = downgradeIfNoMetadata(mk(SyncPull, true, noteHostLocalMetadataOnly), true)
	if op.Direction != SyncPull || op.Note != noteHostLocalMetadataOnly {
		t.Errorf("pull with metadata: got (%q, %q), want unchanged", op.Direction, op.Note)
	}
	// Full replicate push is never downgraded, even with no metadata.
	op = downgradeIfNoMetadata(mk(SyncPush, false, ""), false)
	if op.Direction != SyncPush {
		t.Errorf("replicate push: got %q, want push", op.Direction)
	}
	// Skip stays skip.
	op = downgradeIfNoMetadata(mk(SyncSkip, true, noteHostLocalDiverged), false)
	if op.Direction != SyncSkip || op.Note != noteHostLocalDiverged {
		t.Errorf("skip: got (%q, %q), want note preserved", op.Direction, op.Note)
	}
}

func TestLocalMetadataPresent(t *testing.T) {
	dir := t.TempDir()
	if localMetadataPresent(dir) {
		t.Error("empty dir must have no metadata")
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if localMetadataPresent(dir) {
		t.Error("payload-only dir must have no metadata")
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !localMetadataPresent(dir) {
		t.Error("meta.json must count as metadata")
	}
}

func TestHistoryAction(t *testing.T) {
	cases := []struct {
		dir      SyncDirection
		excluded bool
		want     string
	}{
		{SyncPush, true, "push-meta"},
		{SyncPull, true, "pull-meta"},
		{SyncPush, false, "push"},
		{SyncPull, false, "pull"},
		{SyncSkip, true, "skip"},
	}
	for _, c := range cases {
		op := &SyncOperation{Direction: c.dir, PayloadExcluded: c.excluded}
		if got := op.HistoryAction(); got != c.want {
			t.Errorf("HistoryAction(%q, excluded=%v) = %q, want %q", c.dir, c.excluded, got, c.want)
		}
	}
}

func TestRotatingReplicateWarning(t *testing.T) {
	w := rotatingReplicateWarning("codex")
	if !strings.Contains(w, "codex") || !strings.Contains(w, "revoke") {
		t.Errorf("warning missing substance: %q", w)
	}
}

func TestSyncedProvidersIsACopy(t *testing.T) {
	a := SyncedProviders()
	a[0] = "mutated"
	b := SyncedProviders()
	if b[0] == "mutated" {
		t.Error("SyncedProviders must return a defensive copy")
	}
	want := []string{"claude", "codex", "gemini", "opencode", "cursor"}
	if len(b) != len(want) {
		t.Fatalf("SyncedProviders = %v", b)
	}
	for i := range want {
		if b[i] != want[i] {
			t.Errorf("SyncedProviders[%d] = %q, want %q", i, b[i], want[i])
		}
	}
}
