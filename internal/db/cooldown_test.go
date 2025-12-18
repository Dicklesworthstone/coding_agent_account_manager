package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCooldown_SetAndQueryActive(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	hitAt := now.Add(-5 * time.Minute)

	created, err := d.SetCooldown("claude", "work", hitAt, 60*time.Minute, "rate limit")
	if err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}
	if created.Provider != "claude" || created.ProfileName != "work" {
		t.Fatalf("created provider/profile = %s/%s, want claude/work", created.Provider, created.ProfileName)
	}
	if !created.HitAt.Equal(hitAt) {
		t.Fatalf("created hitAt = %s, want %s", created.HitAt, hitAt)
	}
	if !created.CooldownUntil.Equal(hitAt.Add(60 * time.Minute)) {
		t.Fatalf("created cooldownUntil = %s, want %s", created.CooldownUntil, hitAt.Add(60*time.Minute))
	}

	active, err := d.ActiveCooldown("claude", "work", now)
	if err != nil {
		t.Fatalf("ActiveCooldown() error = %v", err)
	}
	if active == nil {
		t.Fatalf("ActiveCooldown() = nil, want event")
	}
	if !active.HitAt.Equal(hitAt) {
		t.Fatalf("active hitAt = %s, want %s", active.HitAt, hitAt)
	}
	if !active.CooldownUntil.Equal(hitAt.Add(60 * time.Minute)) {
		t.Fatalf("active cooldownUntil = %s, want %s", active.CooldownUntil, hitAt.Add(60*time.Minute))
	}
	if active.Notes != "rate limit" {
		t.Fatalf("active notes = %q, want %q", active.Notes, "rate limit")
	}

	expired, err := d.ActiveCooldown("claude", "work", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ActiveCooldown(expired) error = %v", err)
	}
	if expired != nil {
		t.Fatalf("ActiveCooldown(expired) = %+v, want nil", expired)
	}
}

func TestCooldown_ListActive_ReturnsLatestPerProfile(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	now := time.Now().UTC().Truncate(time.Second)

	// Older hit, shorter cooldown.
	_, err = d.SetCooldown("codex", "main", now.Add(-30*time.Minute), 45*time.Minute, "")
	if err != nil {
		t.Fatalf("SetCooldown(older) error = %v", err)
	}
	// Newer hit, longer cooldown (should win).
	_, err = d.SetCooldown("codex", "main", now.Add(-10*time.Minute), 90*time.Minute, "extended")
	if err != nil {
		t.Fatalf("SetCooldown(newer) error = %v", err)
	}
	// Another provider/profile.
	_, err = d.SetCooldown("claude", "work", now.Add(-5*time.Minute), 60*time.Minute, "")
	if err != nil {
		t.Fatalf("SetCooldown(claude) error = %v", err)
	}

	list, err := d.ListActiveCooldowns(now)
	if err != nil {
		t.Fatalf("ListActiveCooldowns() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListActiveCooldowns() len = %d, want 2", len(list))
	}

	var gotCodex, gotClaude *CooldownEvent
	for i := range list {
		ev := &list[i]
		switch ev.Provider + "/" + ev.ProfileName {
		case "codex/main":
			gotCodex = ev
		case "claude/work":
			gotClaude = ev
		}
	}
	if gotCodex == nil {
		t.Fatalf("ListActiveCooldowns missing codex/main: %+v", list)
	}
	if gotCodex.Notes != "extended" {
		t.Fatalf("codex/main notes = %q, want %q", gotCodex.Notes, "extended")
	}
	if !gotCodex.CooldownUntil.Equal(now.Add(-10 * time.Minute).Add(90 * time.Minute)) {
		t.Fatalf("codex/main cooldownUntil = %s, want %s", gotCodex.CooldownUntil, now.Add(-10*time.Minute).Add(90*time.Minute))
	}
	if gotClaude == nil {
		t.Fatalf("ListActiveCooldowns missing claude/work: %+v", list)
	}
}

func TestCooldown_Clear(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	_, err = d.SetCooldown("claude", "work", now, 60*time.Minute, "")
	if err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}
	_, err = d.SetCooldown("codex", "main", now, 60*time.Minute, "")
	if err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	deleted, err := d.ClearCooldown("claude", "work")
	if err != nil {
		t.Fatalf("ClearCooldown() error = %v", err)
	}
	if deleted == 0 {
		t.Fatalf("ClearCooldown() deleted = %d, want > 0", deleted)
	}

	// codex/main should remain.
	list, err := d.ListActiveCooldowns(now)
	if err != nil {
		t.Fatalf("ListActiveCooldowns() error = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListActiveCooldowns() len = %d, want 1", len(list))
	}
	if list[0].Provider != "codex" || list[0].ProfileName != "main" {
		t.Fatalf("remaining cooldown = %s/%s, want codex/main", list[0].Provider, list[0].ProfileName)
	}

	allDeleted, err := d.ClearAllCooldowns()
	if err != nil {
		t.Fatalf("ClearAllCooldowns() error = %v", err)
	}
	if allDeleted == 0 {
		t.Fatalf("ClearAllCooldowns() deleted = %d, want > 0", allDeleted)
	}
}
