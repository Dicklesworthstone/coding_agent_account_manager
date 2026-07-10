package shallow

import (
	"os"
	"path/filepath"
	"testing"
)

// skillsHome lays down a real HOME with a codex identity and, optionally,
// user-installed codex skills (the layout jsm produces: one directory per
// skill containing SKILL.md).
func skillsHome(t *testing.T, withSkills bool) string {
	t.Helper()
	home := t.TempDir()

	mustWrite := func(rel, body string) {
		full := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	mustWrite(".codex/auth.json", `{"real":"codex-identity"}`)
	mustWrite(".codex/config.toml", "model = \"gpt\"\n")
	mustWrite(".codex/sessions/marker", "codex-sessions")
	if withSkills {
		mustWrite(".codex/skills/example-skill/SKILL.md", "# example skill\n")
		mustWrite(".codex/skills/another-skill/SKILL.md", "# another skill\n")
	}
	return home
}

func newSkillsManager(t *testing.T, home string) *Manager {
	t.Helper()
	mgr, err := NewManager(filepath.Join(t.TempDir(), "orch-homes"), home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

// TestCreateCodexProfileSharesUserInstalledSkills is the create-time half of
// issue #56: a codex shallow profile made while user skills already exist must
// expose them (via the skills passthrough) while keeping auth.json and
// config.toml real and private.
func TestCreateCodexProfileSharesUserInstalledSkills(t *testing.T) {
	home := skillsHome(t, true)
	mgr := newSkillsManager(t, home)

	prof, err := mgr.Create("codex-skills", CreateOptions{
		Provider:         "codex",
		CredentialSource: filepath.Join(home, ".codex", "auth.json"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Identity/config stay real and private.
	mustNotSymlink(t, filepath.Join(prof, ".codex"), "shallow .codex dir")
	mustNotSymlink(t, filepath.Join(prof, ".codex", "auth.json"), "shallow auth.json")
	mustNotSymlink(t, filepath.Join(prof, ".codex", "config.toml"), "shallow config.toml")

	// The user skill resolves to the SAME content as in the real HOME.
	got, err := os.ReadFile(filepath.Join(prof, ".codex", "skills", "example-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("shallow profile does not expose user-installed skill: %v", err)
	}
	if string(got) != "# example skill\n" {
		t.Fatalf("skill content mismatch: %q", got)
	}

	// A fresh, healthy profile needs no repair.
	created, err := mgr.RepairSkillShare("codex-skills")
	if err != nil {
		t.Fatalf("RepairSkillShare: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("repair on a healthy profile should be a no-op, created %v", created)
	}
}

// TestRepairCodexProfileBackfillsSkillSymlinks reproduces the exact drift from
// issue #56: the profile was created BEFORE any user skills existed, codex then
// materialized a REAL <shallow>/.codex/skills containing only its bundled
// .system entries, and jsm later installed user skills into the real HOME.
// Repair must symlink each user skill into the shallow dir without touching
// .system, profile-local entries, or the private auth files.
func TestRepairCodexProfileBackfillsSkillSymlinks(t *testing.T) {
	home := skillsHome(t, false) // no user skills yet
	mgr := newSkillsManager(t, home)

	prof, err := mgr.Create("codex-drift", CreateOptions{
		Provider:         "codex",
		CredentialSource: filepath.Join(home, ".codex", "auth.json"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate codex materializing bundled system skills inside the shallow
	// HOME, plus one genuinely profile-local skill.
	shallowSkills := filepath.Join(prof, ".codex", "skills")
	for _, rel := range []string{".system/builtin/SKILL.md", "local-only/SKILL.md"} {
		full := filepath.Join(shallowSkills, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("# "+rel+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// jsm installs user skills into the REAL home afterwards.
	for _, name := range []string{"example-skill", "another-skill", ".system"} {
		full := filepath.Join(home, ".codex", "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("# real "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	created, err := mgr.RepairSkillShare("codex-drift")
	if err != nil {
		t.Fatalf("RepairSkillShare: %v", err)
	}
	want := map[string]bool{
		".codex/skills/example-skill": true,
		".codex/skills/another-skill": true,
	}
	if len(created) != len(want) {
		t.Fatalf("created = %v, want exactly %v", created, want)
	}
	for _, c := range created {
		if !want[c] {
			t.Fatalf("unexpected created link %q (all: %v)", c, created)
		}
	}

	// Backfilled skills are symlinks resolving to the real HOME content.
	target := mustSymlink(t, filepath.Join(shallowSkills, "example-skill"), "backfilled skill")
	if target != filepath.Join(home, ".codex", "skills", "example-skill") {
		t.Fatalf("backfilled skill points at %q", target)
	}
	got, err := os.ReadFile(filepath.Join(shallowSkills, "example-skill", "SKILL.md"))
	if err != nil || string(got) != "# real example-skill\n" {
		t.Fatalf("backfilled skill content = %q, err=%v", got, err)
	}

	// .system stays the PROFILE's own (never replaced by the real one).
	mustNotSymlink(t, filepath.Join(shallowSkills, ".system"), "bundled .system dir")
	sys, err := os.ReadFile(filepath.Join(shallowSkills, ".system", "builtin", "SKILL.md"))
	if err != nil || string(sys) != "# .system/builtin/SKILL.md\n" {
		t.Fatalf(".system was disturbed: %q, err=%v", sys, err)
	}

	// Profile-local skills are untouched.
	mustNotSymlink(t, filepath.Join(shallowSkills, "local-only"), "profile-local skill")

	// Auth files remain real and private.
	mustNotSymlink(t, filepath.Join(prof, ".codex", "auth.json"), "shallow auth.json")
	mustNotSymlink(t, filepath.Join(prof, ".codex", "config.toml"), "shallow config.toml")

	// Repair is idempotent.
	again, err := mgr.RepairSkillShare("codex-drift")
	if err != nil {
		t.Fatalf("second RepairSkillShare: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second repair should be a no-op, created %v", again)
	}
}

// TestRepairSkillShareLinksWholeDirWhenMissing covers the other drift shape:
// the profile predates the real skills dir and codex never ran, so the shallow
// .codex has NO skills entry at all. Repair lays down the same wholesale
// passthrough symlink that Create would have.
func TestRepairSkillShareLinksWholeDirWhenMissing(t *testing.T) {
	home := skillsHome(t, false)
	mgr := newSkillsManager(t, home)

	prof, err := mgr.Create("codex-late", CreateOptions{
		Provider:         "codex",
		CredentialSource: filepath.Join(home, ".codex", "auth.json"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Skills arrive in the real HOME only after the profile exists.
	skill := filepath.Join(home, ".codex", "skills", "late-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("# late\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	created, err := mgr.RepairSkillShare("codex-late")
	if err != nil {
		t.Fatalf("RepairSkillShare: %v", err)
	}
	if len(created) != 1 || created[0] != ".codex/skills" {
		t.Fatalf("created = %v, want [.codex/skills]", created)
	}
	target := mustSymlink(t, filepath.Join(prof, ".codex", "skills"), "skills passthrough")
	if target != filepath.Join(home, ".codex", "skills") {
		t.Fatalf("skills passthrough points at %q", target)
	}
	if _, err := os.ReadFile(filepath.Join(prof, ".codex", "skills", "late-skill", "SKILL.md")); err != nil {
		t.Fatalf("late skill not visible through passthrough: %v", err)
	}
}

// TestRepairSkillShareNoRealSkillsIsNoop: nothing to share, nothing created.
func TestRepairSkillShareNoRealSkillsIsNoop(t *testing.T) {
	home := skillsHome(t, false)
	mgr := newSkillsManager(t, home)
	if _, err := mgr.Create("codex-none", CreateOptions{
		Provider:         "codex",
		CredentialSource: filepath.Join(home, ".codex", "auth.json"),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	created, err := mgr.RepairSkillShare("codex-none")
	if err != nil {
		t.Fatalf("RepairSkillShare: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("created = %v, want none", created)
	}
}

// TestRepairSkillShareClaudeAndAgy verifies the repair covers every provider's
// skills dir, not just codex.
func TestRepairSkillShareClaudeAndAgy(t *testing.T) {
	cases := []struct {
		provider string
		credRel  string
		skillRel string
	}{
		{"claude", ".claude/.credentials.json", ".claude/skills"},
		{"agy", ".gemini/antigravity-cli/antigravity-oauth-token", ".gemini/skills"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			home := multiHome(t)
			mgr := newSkillsManager(t, home)
			if _, err := mgr.Create("p-"+tc.provider, CreateOptions{
				Provider:         tc.provider,
				CredentialSource: filepath.Join(home, filepath.FromSlash(tc.credRel)),
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}

			// Skills appear in the real HOME after creation.
			skill := filepath.Join(home, filepath.FromSlash(tc.skillRel), "post-skill", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(skill, []byte("# post\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			created, err := mgr.RepairSkillShare("p-" + tc.provider)
			if err != nil {
				t.Fatalf("RepairSkillShare: %v", err)
			}
			if len(created) != 1 || created[0] != tc.skillRel {
				t.Fatalf("created = %v, want [%s]", created, tc.skillRel)
			}
			prof, _ := mgr.HomeFor("p-" + tc.provider)
			if _, err := os.ReadFile(filepath.Join(prof, filepath.FromSlash(tc.skillRel), "post-skill", "SKILL.md")); err != nil {
				t.Fatalf("skill not visible in shallow profile: %v", err)
			}
		})
	}
}
