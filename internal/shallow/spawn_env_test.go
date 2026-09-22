package shallow

import (
	"path/filepath"
	"testing"
)

// Every shallow session must drop every provider-home override it inherits,
// whichever provider it is for (issue #106): a claude shallow spawn started
// from inside a codex shallow session would otherwise carry the outer
// CODEX_HOME into nested tools. The session's own provider re-pins its
// variable via `set`, which callers apply after the scrub.
func TestSpawnEnvScrubsEveryProviderHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "orch-homes", "p")
	cases := []struct {
		provider string
		pinned   map[string]string
	}{
		{"claude", map[string]string{}},
		{"codex", map[string]string{"CODEX_HOME": filepath.Join(home, ".codex")}},
		{"agy", map[string]string{"GEMINI_HOME": filepath.Join(home, ".gemini")}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			set, scrub := SpawnEnv(tc.provider, home, "p", false, false)
			scrubbed := make(map[string]bool, len(scrub))
			for _, k := range scrub {
				scrubbed[k] = true
			}
			for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_HOME", "CAAM_HOME", "XDG_DATA_HOME"} {
				if !scrubbed[k] {
					t.Errorf("%s: %s missing from scrub list %v", tc.provider, k, scrub)
				}
			}
			for k, want := range tc.pinned {
				if got := set[k]; got != want {
					t.Errorf("%s: set[%s] = %q, want %q", tc.provider, k, got, want)
				}
			}
			// Foreign provider homes are never pinned, only scrubbed.
			for _, k := range providerHomeEnv {
				if _, pinned := tc.pinned[k]; pinned {
					continue
				}
				if v, ok := set[k]; ok {
					t.Errorf("%s: foreign %s=%q must not be set", tc.provider, k, v)
				}
			}
			if set["HOME"] != home {
				t.Errorf("%s: HOME = %q, want %q", tc.provider, set["HOME"], home)
			}
		})
	}
}
