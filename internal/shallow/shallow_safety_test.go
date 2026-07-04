package shallow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- #38: never RemoveAll (or replace) the user's real HOME ------------------

// TestCreateForceRefusesRealHomeCollision reproduces the exact scenario in
// issue #38: a --base that is the PARENT of the real HOME, with a profile name
// that resolves the profile home straight onto the real HOME. Create --force
// must refuse and leave the real HOME untouched.
func TestCreateForceRefusesRealHomeCollision(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "home", "alice")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	// A precious file in the real HOME that must survive.
	precious := filepath.Join(realHome, ".bashrc")
	if err := os.WriteFile(precious, []byte("# precious\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// base is the PARENT of realHome; NewManager's only check (base != realHome)
	// passes because /root/home != /root/home/alice.
	base := filepath.Join(root, "home")
	mgr, err := NewManager(base, realHome)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// HomeFor("alice") == base/alice == realHome.
	if h, _ := mgr.HomeFor("alice"); h != realHome {
		t.Fatalf("test precondition: HomeFor(alice)=%q, want realHome %q", h, realHome)
	}

	// Both with and without --force must error, never delete.
	if _, err := mgr.Create("alice", CreateOptions{Force: true}); err == nil {
		t.Fatalf("Create --force must refuse to overwrite the real HOME")
	}
	if _, err := mgr.Create("alice", CreateOptions{}); err == nil {
		t.Fatalf("Create must refuse to use the real HOME as a profile home")
	}
	// Real HOME and its contents survive.
	if body, err := os.ReadFile(precious); err != nil || string(body) != "# precious\n" {
		t.Fatalf("real HOME was damaged: body=%q err=%v", body, err)
	}
}

// TestCreateRefusesAncestorOfRealHome covers the "one level worse" variant from
// #38: a profile home that is an ANCESTOR of the real HOME (which RemoveAll
// would delete transitively).
func TestCreateRefusesAncestorOfRealHome(t *testing.T) {
	root := t.TempDir()
	// realHome is /root/parent/home/alice; base is /root/parent; name "home"
	// resolves the profile home to /root/parent/home — an ancestor of realHome.
	realHome := filepath.Join(root, "parent", "home", "alice")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(realHome, "keep")
	if err := os.WriteFile(guard, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "parent")
	mgr, err := NewManager(base, realHome)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("home", CreateOptions{Force: true}); err == nil {
		t.Fatalf("Create --force must refuse an ancestor of the real HOME")
	}
	if _, err := os.Stat(guard); err != nil {
		t.Fatalf("real HOME subtree damaged: %v", err)
	}
}

// TestCreateForceRefusesNonProfileDirectory ensures --force will not wipe a
// directory that isn't a caam shallow profile (no metadata sidecar) — e.g. a
// --base that accidentally points at an unrelated real directory.
func TestCreateForceRefusesNonProfileDirectory(t *testing.T) {
	home := fakeHome(t)
	base := filepath.Join(t.TempDir(), "homes")
	mgr, err := NewManager(base, home)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-create base/notaprofile with important content but NO sidecar.
	notProfile := filepath.Join(base, "notaprofile")
	if err := os.MkdirAll(notProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(notProfile, "important.txt")
	if err := os.WriteFile(keep, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("notaprofile", CreateOptions{Force: true}); err == nil {
		t.Fatalf("Create --force must refuse a directory without the shallow sidecar")
	}
	if body, err := os.ReadFile(keep); err != nil || string(body) != "keep me" {
		t.Fatalf("non-profile directory was damaged: body=%q err=%v", body, err)
	}
}

// --- #42: --force is non-destructive until the rebuild succeeds --------------

// TestCreateForceRollsBackOnBuildFailure proves that when a --force rebuild
// fails partway, the original profile survives completely intact (its live
// credential is not destroyed before a working replacement exists).
func TestCreateForceRollsBackOnBuildFailure(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}

	// Build a valid profile whose credential is the ONLY copy of an identity.
	credSrc := credSource(t, `{"identity":"ORIGINAL-LIVE-TOKEN"}`)
	if _, err := mgr.Create("alice", CreateOptions{CredentialSource: credSrc, CredentialFromLabel: "vault:claude/alice"}); err != nil {
		t.Fatal(err)
	}
	aliceCred, _ := mgr.CredentialPath("alice")

	// Now attempt a --force rebuild that is guaranteed to fail AFTER the point
	// where the old code would already have RemoveAll'd the profile: a
	// nonexistent --from-claude-json source.
	_, err = mgr.Create("alice", CreateOptions{
		Force:            true,
		CredentialSource: credSource(t, `{"identity":"REPLACEMENT"}`),
		SourceClaudeJSON: filepath.Join(t.TempDir(), "does-not-exist.json"),
	})
	if err == nil {
		t.Fatalf("expected the forced rebuild to fail on a missing source")
	}

	// The original profile must still exist and be intact.
	if _, gerr := mgr.Get("alice"); gerr != nil {
		t.Fatalf("original profile destroyed by a failed --force rebuild: %v", gerr)
	}
	if body, rerr := os.ReadFile(aliceCred); rerr != nil {
		t.Fatalf("original credential destroyed: %v", rerr)
	} else if string(body) != `{"identity":"ORIGINAL-LIVE-TOKEN"}` {
		t.Fatalf("original credential drifted to %q — rollback failed", body)
	}
	// No leftover staging/backup scaffolding should leak into List.
	profiles, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "alice" {
		t.Fatalf("expected exactly the intact alice profile, got %+v", profiles)
	}
}

// TestCreateForceSucceedsAndSwaps confirms a successful --force actually
// replaces the credential (the happy path through build-then-swap).
func TestCreateForceSucceedsAndSwaps(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{CredentialSource: credSource(t, `{"v":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("alice", CreateOptions{Force: true, CredentialSource: credSource(t, `{"v":2}`)}); err != nil {
		t.Fatalf("forced rebuild: %v", err)
	}
	cred, _ := mgr.CredentialPath("alice")
	if body, _ := os.ReadFile(cred); string(body) != `{"v":2}` {
		t.Fatalf("credential not swapped: %q", body)
	}
	// Base dir must contain only the profile — no leftover scaffolding.
	entries, _ := os.ReadDir(mgr.BaseDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), reservedDirPrefix) {
			t.Fatalf("leftover scaffolding after successful swap: %s", e.Name())
		}
	}
}

// TestCreateRejectsReservedPrefixName ensures user profiles can't collide with
// caam's internal staging/backup names.
func TestCreateRejectsReservedPrefixName(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(reservedDirPrefix+"evil", CreateOptions{}); err == nil {
		t.Fatalf("expected reserved-prefix name to be rejected")
	}
}

// TestListHidesReservedScaffolding ensures a crashed swap's leftover staging or
// backup dir is not surfaced as a profile.
func TestListHidesReservedScaffolding(t *testing.T) {
	home := fakeHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create("real", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed swap leaving scaffolding behind.
	if err := os.MkdirAll(filepath.Join(mgr.BaseDir(), stagingDirPrefix+"real-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mgr.BaseDir(), backupDirPrefix+"real-456"), 0o700); err != nil {
		t.Fatal(err)
	}
	profiles, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "real" {
		t.Fatalf("List surfaced scaffolding: %+v", profiles)
	}
}

// --- #43: provider resolution fails closed, never silently claude -----------

// TestResolveProviderInfersNonClaudeWhenMetaCorrupt is the core #43 security
// property: a codex/agy profile whose metadata is unreadable must NOT resolve to
// claude (which would skip CODEX_HOME/GEMINI_HOME pinning and leak the real
// identity). The provider is recovered from the on-disk layout instead.
func TestResolveProviderInfersNonClaudeWhenMetaCorrupt(t *testing.T) {
	home := multiHome(t)
	for _, provider := range []string{"claude", "codex", "agy"} {
		t.Run(provider, func(t *testing.T) {
			mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Create("p", CreateOptions{Provider: provider, CredentialSource: credSource(t, "tok")}); err != nil {
				t.Fatal(err)
			}
			profHome, _ := mgr.HomeFor("p")

			// Corrupt the metadata sidecar so readMeta fails.
			if err := os.WriteFile(filepath.Join(profHome, ProfileMetaFilename), []byte("}{ not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := mgr.ResolveProvider("p")
			if err != nil {
				t.Fatalf("ResolveProvider with corrupt meta: %v", err)
			}
			if got != provider {
				t.Fatalf("provider mis-resolved to %q, want %q (would leak the real identity)", got, provider)
			}

			// Also with the metadata removed entirely.
			if err := os.Remove(filepath.Join(profHome, ProfileMetaFilename)); err != nil {
				t.Fatal(err)
			}
			got, err = mgr.ResolveProvider("p")
			if err != nil {
				t.Fatalf("ResolveProvider with missing meta: %v", err)
			}
			if got != provider {
				t.Fatalf("provider mis-inferred to %q, want %q", got, provider)
			}
		})
	}
}

// --- #50: infer provider from legacy credential metadata --------------------

// TestProviderFromLegacyLabel unit-tests the strict label parser: it accepts
// ONLY vault:<supported-provider>/<name> and fails closed on everything else.
func TestProviderFromLegacyLabel(t *testing.T) {
	cases := []struct {
		label  string
		want   string
		wantOK bool
	}{
		// valid, well-formed labels
		{"vault:claude/legacy-profile", "claude", true},
		{"vault:codex/bob", "codex", true},
		{"vault:agy/carol", "agy", true},
		{"vault:CODEX/bob", "codex", true},                 // provider is normalized
		{"  vault:claude/alice  ", "claude", true},         // surrounding space tolerated
		{"vault:claude/alice@example.com", "claude", true}, // name may contain @
		// blank / missing
		{"", "", false},
		{"   ", "", false},
		// non-vault schemes and arbitrary strings
		{"file:/home/u/.codex/auth.json", "", false},
		{"alice@example.com", "", false},
		{"/home/u/.codex", "", false},
		{"claude", "", false},
		// unsupported / unknown providers
		{"vault:openai/x", "", false},
		{"vault:gpt/x", "", false},
		{"vault:gemini/x", "", false}, // gemini is not a shallow provider id (agy is)
		// malformed vault labels
		{"vault:", "", false},
		{"vault:codex", "", false},  // no profile name
		{"vault:codex/", "", false}, // empty profile name
		{"vault:/x", "", false},     // empty provider
		{"vault: /x", "", false},    // whitespace-only provider
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, ok := providerFromLegacyLabel(tc.label)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("providerFromLegacyLabel(%q) = (%q, %v), want (%q, %v)", tc.label, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestResolveProviderInfersFromLegacyMetadata drives the full ResolveProvider
// path against hand-written legacy metadata (blank explicit provider), proving
// shallow-spawn/CredentialPath recover the provider from a well-formed vault
// label, keep the claude default for a genuinely blank label, and fail closed on
// malformed/unsupported labels (issue #50).
func TestResolveProviderInfersFromLegacyMetadata(t *testing.T) {
	home := multiHome(t)
	mgr, err := NewManager(filepath.Join(t.TempDir(), "homes"), home)
	if err != nil {
		t.Fatal(err)
	}
	// A real claude profile on disk; we overwrite ONLY its metadata per-case.
	if _, err := mgr.Create("p", CreateOptions{Provider: "claude", CredentialSource: credSource(t, "x")}); err != nil {
		t.Fatal(err)
	}
	profHome, _ := mgr.HomeFor("p")
	metaPath := filepath.Join(profHome, ProfileMetaFilename)

	// writeMeta lets a case omit the provider and/or credential_from keys entirely.
	writeMetaFields := func(t *testing.T, fields map[string]any) {
		t.Helper()
		fields["name"] = "p"
		fields["real_home"] = home
		fields["version"] = 2
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metaPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("valid legacy labels", func(t *testing.T) {
		for label, want := range map[string]string{
			"vault:claude/legacy-profile": "claude",
			"vault:codex/legacy-profile":  "codex",
			"vault:agy/legacy-profile":    "agy",
		} {
			writeMetaFields(t, map[string]any{"credential_from": label})
			got, err := mgr.ResolveProvider("p")
			if err != nil {
				t.Fatalf("label %q: unexpected error: %v", label, err)
			}
			if got != want {
				t.Fatalf("label %q resolved to %q, want %q", label, got, want)
			}
		}
	})

	t.Run("blank label defaults to claude", func(t *testing.T) {
		// credential_from present but empty.
		writeMetaFields(t, map[string]any{"credential_from": ""})
		if got, err := mgr.ResolveProvider("p"); err != nil || got != "claude" {
			t.Fatalf("blank label = (%q, %v), want (claude, nil)", got, err)
		}
		// credential_from key absent entirely.
		writeMetaFields(t, map[string]any{})
		if got, err := mgr.ResolveProvider("p"); err != nil || got != "claude" {
			t.Fatalf("absent label = (%q, %v), want (claude, nil)", got, err)
		}
	})

	t.Run("malformed and unsupported labels fail closed", func(t *testing.T) {
		for _, label := range []string{
			"file:/home/u/.codex/auth.json", // non-vault scheme
			"alice@example.com",             // email-like
			"/home/u/.codex",                // path
			"codex",                         // bare child-command-like string
			"vault:openai/x",                // unsupported provider
			"vault:codex",                   // missing profile name
			"vault:/x",                      // empty provider
		} {
			writeMetaFields(t, map[string]any{"credential_from": label})
			if got, err := mgr.ResolveProvider("p"); err == nil {
				t.Fatalf("label %q must fail closed, but resolved to %q", label, got)
			}
		}
	})

	t.Run("explicit provider still wins over label", func(t *testing.T) {
		writeMetaFields(t, map[string]any{"provider": "codex", "credential_from": "vault:claude/x"})
		if got, err := mgr.ResolveProvider("p"); err != nil || got != "codex" {
			t.Fatalf("explicit provider = (%q, %v), want (codex, nil)", got, err)
		}
	})
}

// TestResolveProviderFailsClosedWhenAmbiguous ensures that when the provider
// cannot be read from metadata OR unambiguously inferred from disk, resolution
// errors rather than defaulting to claude.
func TestResolveProviderFailsClosedWhenAmbiguous(t *testing.T) {
	home := fakeHome(t)
	base := filepath.Join(t.TempDir(), "homes")
	mgr, err := NewManager(base, home)
	if err != nil {
		t.Fatal(err)
	}
	// A directory that looks like a profile slot but has no metadata and no
	// real provider layout on disk.
	empty := filepath.Join(base, "mystery")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ResolveProvider("mystery"); err == nil {
		t.Fatalf("ResolveProvider must fail closed for an indeterminate profile")
	}
}
