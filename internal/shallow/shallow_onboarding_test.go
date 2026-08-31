package shallow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Issue #80 regression matrix: an authenticated shallow claude profile must
// come up with hasCompletedOnboarding set (so the interactive TUI skips the
// redundant theme-setup + OAuth first-run), while intentionally empty
// credential sources keep the normal first-run flow, and a seed source's own
// onboarding state is preserved verbatim.

const validClaudeCred = `{"claudeAiOauth":{"accessToken":"fake-token","refreshToken":"fake-refresh"}}`

// onboardingEnv builds an isolated real HOME + shallow manager for one test.
// realClaudeJSON == "" means the real HOME has NO .claude.json at all.
func onboardingEnv(t *testing.T, realClaudeJSON string) (*Manager, string) {
	t.Helper()
	realHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realHome, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if realClaudeJSON != "" {
		if err := os.WriteFile(filepath.Join(realHome, ".claude.json"), []byte(realClaudeJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(t.TempDir(), "orch-homes")
	mgr, err := NewManager(base, realHome)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, realHome
}

// writeTempFile writes body to a fresh file and returns its path.
func writeTempFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// stagedClaudeJSON reads and parses <home>/.claude.json, asserting 0600 perms.
func stagedClaudeJSON(t *testing.T, home string) map[string]json.RawMessage {
	t.Helper()
	p := filepath.Join(home, ".claude.json")
	if runtime.GOOS != "windows" {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat staged .claude.json: %v", err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("staged .claude.json perms = %v, want 0600", st.Mode().Perm())
		}
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read staged .claude.json: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("staged .claude.json is not a JSON object: %v\n%s", err, raw)
	}
	return m
}

func onboardingValue(t *testing.T, m map[string]json.RawMessage) (present bool, val bool) {
	t.Helper()
	raw, ok := m["hasCompletedOnboarding"]
	if !ok {
		return false, false
	}
	if err := json.Unmarshal(raw, &val); err != nil {
		t.Fatalf("hasCompletedOnboarding not a bool: %s", raw)
	}
	return true, val
}

// Case 1: a vault-style source whose own .claude.json already carries the
// onboarding state — it must be preserved verbatim, never overwritten.
func TestOnboarding_SourceStateWithMarkerIsPreserved(t *testing.T) {
	mgr, _ := onboardingEnv(t, `{"real":"home-state"}`)
	cred := writeTempFile(t, "creds.json", validClaudeCred)
	state := writeTempFile(t, "state.json", `{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"u-1"},"theme":"dark"}`)

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude", CredentialSource: cred, SourceClaudeJSON: state})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := stagedClaudeJSON(t, home)
	if present, val := onboardingValue(t, m); !present || !val {
		t.Fatalf("marker lost: present=%v val=%v", present, val)
	}
	// Version-specific source fields must survive.
	if _, ok := m["oauthAccount"]; !ok {
		t.Fatal("oauthAccount dropped from seeded state")
	}
	if _, ok := m["theme"]; !ok {
		t.Fatal("theme dropped from seeded state")
	}
	// And since the marker was already there, the file is the source verbatim.
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	src, _ := os.ReadFile(state)
	if string(raw) != string(src) {
		t.Fatalf("source with marker should be copied verbatim; got %s", raw)
	}
}

// A source that EXPLICITLY says hasCompletedOnboarding:false keeps false —
// the merge only fills in a missing marker, it never flips one.
func TestOnboarding_SourceExplicitFalseIsKept(t *testing.T) {
	mgr, _ := onboardingEnv(t, "")
	cred := writeTempFile(t, "creds.json", validClaudeCred)
	state := writeTempFile(t, "state.json", `{"hasCompletedOnboarding":false}`)

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude", CredentialSource: cred, SourceClaudeJSON: state})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if present, val := onboardingValue(t, stagedClaudeJSON(t, home)); !present || val {
		t.Fatalf("explicit false was altered: present=%v val=%v", present, val)
	}
}

// Case 2: seeded from a real HOME whose .claude.json LACKS the marker, with
// validated credentials — the marker is merged in and other fields survive.
func TestOnboarding_RealHomeMissingMarkerGetsMerged(t *testing.T) {
	mgr, realHome := onboardingEnv(t, `{"theme":"light","numStartups":3}`)
	cred := writeTempFile(t, "creds.json", validClaudeCred)

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude", CredentialSource: cred})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := stagedClaudeJSON(t, home)
	if present, val := onboardingValue(t, m); !present || !val {
		t.Fatalf("marker not merged: present=%v val=%v", present, val)
	}
	for _, k := range []string{"theme", "numStartups"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("field %q dropped by the onboarding merge", k)
		}
	}
	// The SOURCE (real HOME) file must be untouched.
	srcRaw, err := os.ReadFile(filepath.Join(realHome, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(srcRaw) != `{"theme":"light","numStartups":3}` {
		t.Fatalf("real HOME .claude.json was modified: %s", srcRaw)
	}
}

// Case 3: no real-HOME .claude.json at all — the skeleton is written and the
// marker merged on top (authenticated source).
func TestOnboarding_NoRealHomeStateSkeletonGetsMarker(t *testing.T) {
	mgr, _ := onboardingEnv(t, "")
	cred := writeTempFile(t, "creds.json", validClaudeCred)

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude", CredentialSource: cred})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if present, val := onboardingValue(t, stagedClaudeJSON(t, home)); !present || !val {
		t.Fatalf("skeleton did not get the marker: present=%v val=%v", present, val)
	}
}

// Case 4: an explicit --from-claude-json source that lacks the marker, with
// validated credentials — the marker is merged into the STAGED copy only.
func TestOnboarding_ExplicitSourceMissingMarkerGetsMerged(t *testing.T) {
	mgr, _ := onboardingEnv(t, `{"real":"home-state"}`)
	cred := writeTempFile(t, "creds.json", validClaudeCred)
	state := writeTempFile(t, "state.json", `{"oauthAccount":{"accountUuid":"u-2"}}`)

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude", CredentialSource: cred, SourceClaudeJSON: state})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m := stagedClaudeJSON(t, home)
	if present, val := onboardingValue(t, m); !present || !val {
		t.Fatalf("marker not merged into explicit source: present=%v val=%v", present, val)
	}
	if _, ok := m["oauthAccount"]; !ok {
		t.Fatal("oauthAccount dropped by the onboarding merge")
	}
	// The explicit SOURCE file itself must be untouched.
	srcRaw, _ := os.ReadFile(state)
	if string(srcRaw) != `{"oauthAccount":{"accountUuid":"u-2"}}` {
		t.Fatalf("explicit source file was modified: %s", srcRaw)
	}
}

// Case 5: an intentionally empty credential source keeps the normal first-run
// flow — no marker is injected.
func TestOnboarding_EmptyCredentialsUntouched(t *testing.T) {
	mgr, _ := onboardingEnv(t, `{"theme":"light"}`)

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if present, _ := onboardingValue(t, stagedClaudeJSON(t, home)); present {
		t.Fatal("marker injected despite an intentionally empty credential source")
	}
}

// A credential SOURCE whose content is empty/blank is not "validated" either:
// no marker.
func TestOnboarding_BlankCredentialFileUntouched(t *testing.T) {
	mgr, _ := onboardingEnv(t, `{"theme":"light"}`)
	cred := writeTempFile(t, "creds.json", "\n")

	home, err := mgr.Create("p1", CreateOptions{Provider: "claude", CredentialSource: cred})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if present, _ := onboardingValue(t, stagedClaudeJSON(t, home)); present {
		t.Fatal("marker injected for a blank credential file")
	}
}
