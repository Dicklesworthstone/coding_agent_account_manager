package authfile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeCodexJWT builds an unsigned (alg=none-style) JWT whose payload carries an
// email identity claim and an iat (issued-at). Only the payload segment matters
// for our parsing; the signature is a placeholder.
func makeCodexJWT(t *testing.T, email string, iat time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := map[string]interface{}{
		"email": email,
		"iat":   iat.Unix(),
		"sub":   "user-" + email,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return header + "." + body + ".sig"
}

// writeCodexAuth writes a Codex-style auth.json with a JWT id_token and an
// optional top-level last_refresh field.
func writeCodexAuth(t *testing.T, path, email string, iat time.Time, lastRefresh *time.Time) {
	t.Helper()
	auth := map[string]interface{}{
		"tokens": map[string]interface{}{
			"id_token":      makeCodexJWT(t, email, iat),
			"access_token":  makeCodexJWT(t, email, iat),
			"refresh_token": "rt-" + iat.Format("150405"),
		},
	}
	if lastRefresh != nil {
		auth["last_refresh"] = lastRefresh.Format(time.RFC3339)
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("marshal codex auth: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
}

func codexFileSet(authPath string) AuthFileSet {
	return AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}
}

// setupCodexRestore stages a vault snapshot (the incoming profile) and a live
// auth file, then returns the vault, fileset, and live path.
func setupCodexRestore(t *testing.T, snapshot, live []byte) (*Vault, AuthFileSet, string) {
	t.Helper()
	tmp := t.TempDir()
	vaultDir := filepath.Join(tmp, "vault")
	profileDir := filepath.Join(vaultDir, "codex", "acct")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), snapshot, 0600); err != nil {
		t.Fatal(err)
	}

	livePath := filepath.Join(tmp, "home", ".codex", "auth.json")
	if live != nil {
		if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(livePath, live, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return NewVault(vaultDir), codexFileSet(livePath), livePath
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// codexAuthBytes is a convenience to build a codex auth.json blob in-memory.
func codexAuthBytes(t *testing.T, email string, iat time.Time, lastRefresh *time.Time) []byte {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "auth.json")
	writeCodexAuth(t, p, email, iat, lastRefresh)
	return readBytes(t, p)
}

func TestCodexRestoreFreshnessGuard(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	older := base
	newer := base.Add(2 * time.Hour)

	t.Run("same identity, live strictly newer -> live preserved", func(t *testing.T) {
		// Snapshot is the same account but with OLDER (rotated-out) tokens.
		snap := codexAuthBytes(t, "alice@example.com", older, &older)
		live := codexAuthBytes(t, "alice@example.com", newer, &newer)

		v, fs, livePath := setupCodexRestore(t, snap, live)
		if err := v.Restore(fs, "acct"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		got := readBytes(t, livePath)
		if string(got) != string(live) {
			t.Errorf("live file was clobbered; freshness guard failed to preserve newer live tokens")
		}
	})

	t.Run("same identity, live older -> overwritten", func(t *testing.T) {
		snap := codexAuthBytes(t, "alice@example.com", newer, &newer)
		live := codexAuthBytes(t, "alice@example.com", older, &older)

		v, fs, livePath := setupCodexRestore(t, snap, live)
		if err := v.Restore(fs, "acct"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		got := readBytes(t, livePath)
		if string(got) != string(snap) {
			t.Errorf("live file was NOT overwritten by newer snapshot; got stale live tokens")
		}
	})

	t.Run("different identity -> overwritten (real cross-account switch)", func(t *testing.T) {
		// Live is a DIFFERENT account, even if it is newer; switching must win.
		snap := codexAuthBytes(t, "alice@example.com", older, &older)
		live := codexAuthBytes(t, "bob@example.com", newer, &newer)

		v, fs, livePath := setupCodexRestore(t, snap, live)
		if err := v.Restore(fs, "acct"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		got := readBytes(t, livePath)
		if string(got) != string(snap) {
			t.Errorf("cross-account switch was blocked; different-identity live was not overwritten")
		}
	})

	t.Run("no live file -> normal copy", func(t *testing.T) {
		snap := codexAuthBytes(t, "alice@example.com", newer, &newer)

		v, fs, livePath := setupCodexRestore(t, snap, nil)
		if err := v.Restore(fs, "acct"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		got := readBytes(t, livePath)
		if string(got) != string(snap) {
			t.Errorf("first-time restore did not copy snapshot")
		}
	})

	t.Run("equal timestamps -> overwritten (not strictly newer)", func(t *testing.T) {
		snap := codexAuthBytes(t, "alice@example.com", base, &base)
		// Same identity & same freshness but DIFFERENT bytes (different refresh token).
		live := codexAuthBytes(t, "alice@example.com", base, &base)

		v, fs, livePath := setupCodexRestore(t, snap, live)
		if err := v.Restore(fs, "acct"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		got := readBytes(t, livePath)
		if string(got) != string(snap) {
			t.Errorf("equal-freshness live should be overwritten (guard requires STRICTLY newer)")
		}
	})

	t.Run("non-codex tool unaffected by guard", func(t *testing.T) {
		tmp := t.TempDir()
		vaultDir := filepath.Join(tmp, "vault")
		profileDir := filepath.Join(vaultDir, "testtool", "acct")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		snap := []byte(`{"token":"from-vault"}`)
		if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), snap, 0600); err != nil {
			t.Fatal(err)
		}
		livePath := filepath.Join(tmp, "live", "auth.json")
		if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(livePath, []byte(`{"token":"live-newer"}`), 0600); err != nil {
			t.Fatal(err)
		}
		v := NewVault(vaultDir)
		fs := AuthFileSet{Tool: "testtool", Files: []AuthFileSpec{{Tool: "testtool", Path: livePath, Required: true}}}
		if err := v.Restore(fs, "acct"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
		got := readBytes(t, livePath)
		if string(got) != string(snap) {
			t.Errorf("non-codex restore must always copy verbatim; got %q", got)
		}
	})
}

func TestCodexRestoreDoesNotAttributeUnusedOAuthFreshness(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, liveKey, targetKey string }{
		{"oauth-to-key", "", "synthetic-target-key"},
		{"key-to-oauth", "synthetic-live-key", ""},
		{"different-key", "synthetic-live-key", "synthetic-target-key"},
		{"same-key", "synthetic-same-key", "synthetic-same-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := []byte(selectedSwitchFiles(t, "codex", tc.liveKey, "shared@example.test", old.Add(time.Hour))["auth.json"])
			target := []byte(selectedSwitchFiles(t, "codex", tc.targetKey, "shared@example.test", old)["auth.json"])
			v, fs, livePath := setupCodexRestore(t, target, live)
			if CodexLiveIsNewer(livePath, v.BackupPath("codex", "acct", "auth.json")) {
				t.Fatal("unused OAuth timestamps activated the renewal/replay guard")
			}
			if err := v.Restore(fs, "acct"); err != nil {
				t.Fatal(err)
			}
			if got := readBytes(t, livePath); string(got) != string(target) {
				t.Fatal("restore did not select the requested credential")
			}
		})
	}
}

func TestCodexRestoreFreshnessRequiresConsistentWorkspace(t *testing.T) {
	type identity struct {
		accountID       string
		jwtAccountID    string
		accessAccountID string
		accessSubject   string
	}
	makeAuth := func(t *testing.T, account identity, issued time.Time) []byte {
		t.Helper()
		makeJWT := func(workspace, subject string) string {
			claims := map[string]interface{}{
				"email": "shared@example.com",
				"sub":   subject,
				"iat":   issued.Unix(),
			}
			if workspace != "" {
				claims["https://api.openai.com/auth"] = map[string]interface{}{
					"chatgpt_account_id": workspace,
				}
			}
			payload, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
		}
		accessWorkspace := account.jwtAccountID
		if account.accessAccountID != "" {
			accessWorkspace = account.accessAccountID
		}
		accessSubject := "shared-subject"
		if account.accessSubject != "" {
			accessSubject = account.accessSubject
		}
		tokens := map[string]interface{}{
			"id_token":      makeJWT(account.jwtAccountID, "shared-subject"),
			"access_token":  makeJWT(accessWorkspace, accessSubject),
			"refresh_token": "rt-" + issued.Format("150405"),
		}
		if account.accountID != "" {
			tokens["account_id"] = account.accountID
		}
		data, err := json.Marshal(map[string]interface{}{
			"tokens":       tokens,
			"last_refresh": issued.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	for _, tc := range []struct {
		name         string
		snapshot     identity
		live         identity
		wantKeepLive bool
	}{
		{
			name:         "same workspace keeps newer live tokens",
			snapshot:     identity{accountID: "workspace-a", jwtAccountID: "workspace-a"},
			live:         identity{accountID: "workspace-a", jwtAccountID: "workspace-a"},
			wantKeepLive: true,
		},
		{
			name:     "different explicit workspaces despite shared subject and email",
			snapshot: identity{accountID: "workspace-a"},
			live:     identity{accountID: "workspace-b"},
		},
		{
			name:     "different JWT workspaces despite shared subject and email",
			snapshot: identity{jwtAccountID: "workspace-a"},
			live:     identity{jwtAccountID: "workspace-b"},
		},
		{
			name:     "live workspace missing",
			snapshot: identity{accountID: "workspace-a", jwtAccountID: "workspace-a"},
		},
		{
			name: "snapshot workspace missing",
			live: identity{accountID: "workspace-a", jwtAccountID: "workspace-a"},
		},
		{
			name:     "live explicit workspace disagrees with its JWT",
			snapshot: identity{accountID: "workspace-a", jwtAccountID: "workspace-a"},
			live:     identity{accountID: "workspace-a", jwtAccountID: "workspace-b"},
		},
		{
			name:     "live ID and access tokens disagree on workspace",
			snapshot: identity{jwtAccountID: "workspace-a"},
			live:     identity{jwtAccountID: "workspace-a", accessAccountID: "workspace-b"},
		},
		{
			name:     "live ID and access tokens disagree on subject",
			snapshot: identity{jwtAccountID: "workspace-a"},
			live:     identity{jwtAccountID: "workspace-a", accessSubject: "another-subject"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			older := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
			snapshot := makeAuth(t, tc.snapshot, older)
			live := makeAuth(t, tc.live, older.Add(time.Hour))
			v, fs, livePath := setupCodexRestore(t, snapshot, live)
			snapshotPath := v.BackupPath("codex", "acct", "auth.json")

			if got := CodexLiveIsNewer(livePath, snapshotPath); got != tc.wantKeepLive {
				t.Errorf("CodexLiveIsNewer() = %v, want %v", got, tc.wantKeepLive)
			}
			if err := v.Restore(fs, "acct"); err != nil {
				t.Fatalf("Restore() error = %v", err)
			}
			want := snapshot
			if tc.wantKeepLive {
				want = live
			}
			if got := readBytes(t, livePath); string(got) != string(want) {
				t.Fatal("Restore retained the wrong account or token generation")
			}
			if got := readBytes(t, snapshotPath); string(got) != string(snapshot) {
				t.Fatal("Restore changed the saved profile")
			}
		})
	}
}

func TestCodexFreshness(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	t.Run("last_refresh wins and parses", func(t *testing.T) {
		// last_refresh newer than the JWT iat -> we take the max (last_refresh).
		jwtTime := base
		lr := base.Add(time.Hour)
		data := codexAuthBytes(t, "x@example.com", jwtTime, &lr)
		ts, ok := codexFreshness(data)
		if !ok {
			t.Fatal("expected a parseable freshness timestamp")
		}
		if !ts.Equal(lr) {
			t.Errorf("freshness = %v, want max(last_refresh)=%v", ts, lr)
		}
	})

	t.Run("falls back to jwt iat when no last_refresh", func(t *testing.T) {
		data := codexAuthBytes(t, "x@example.com", base, nil)
		ts, ok := codexFreshness(data)
		if !ok {
			t.Fatal("expected jwt iat freshness")
		}
		if !ts.Equal(base) {
			t.Errorf("freshness = %v, want jwt iat %v", ts, base)
		}
	})

	t.Run("unparseable -> not ok", func(t *testing.T) {
		if _, ok := codexFreshness([]byte(`not json`)); ok {
			t.Error("expected ok=false for non-json")
		}
		if _, ok := codexFreshness([]byte(`{"tokens":{}}`)); ok {
			t.Error("expected ok=false when no timestamp present")
		}
	})
}

func TestResnapshotOutgoing(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	newer := base.Add(time.Hour)

	setup := func(t *testing.T) (*Vault, AuthFileSet, string, string) {
		tmp := t.TempDir()
		vaultDir := filepath.Join(tmp, "vault")
		// Outgoing profile already exists in vault with OLD tokens.
		outDir := filepath.Join(vaultDir, "codex", "work")
		if err := os.MkdirAll(outDir, 0700); err != nil {
			t.Fatal(err)
		}
		writeCodexAuth(t, filepath.Join(outDir, "auth.json"), "work@example.com", base, &base)
		vaultSnap := filepath.Join(outDir, "auth.json")

		// Live file holds NEWER rotated tokens for the same account.
		livePath := filepath.Join(tmp, "home", ".codex", "auth.json")
		writeCodexAuth(t, livePath, "work@example.com", newer, &newer)

		return NewVault(vaultDir), codexFileSet(livePath), vaultSnap, livePath
	}

	t.Run("captures rotated tokens of outgoing profile", func(t *testing.T) {
		v, fs, vaultSnap, livePath := setup(t)
		before := readBytes(t, vaultSnap)
		liveBytes := readBytes(t, livePath)

		if err := v.ResnapshotOutgoing(fs, "work", "personal"); err != nil {
			t.Fatalf("ResnapshotOutgoing() error = %v", err)
		}
		after := readBytes(t, vaultSnap)
		if string(after) == string(before) {
			t.Error("vault snapshot was not refreshed with live rotated tokens")
		}
		if string(after) != string(liveBytes) {
			t.Errorf("vault snapshot != live tokens after re-snapshot")
		}
	})

	t.Run("skips system profiles", func(t *testing.T) {
		v, fs, _, _ := setup(t)
		// _backup_* is a system profile; must not be (re)written.
		if err := v.ResnapshotOutgoing(fs, "_backup_20260601", "personal"); err != nil {
			t.Fatalf("ResnapshotOutgoing() should be a silent no-op for system profiles, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(v.basePath, "codex", "_backup_20260601")); !os.IsNotExist(err) {
			t.Error("system profile should not have been created")
		}
	})

	t.Run("skips target profile", func(t *testing.T) {
		v, fs, vaultSnap, _ := setup(t)
		before := readBytes(t, vaultSnap)
		// outgoing == target -> no-op
		if err := v.ResnapshotOutgoing(fs, "work", "work"); err != nil {
			t.Fatalf("error = %v", err)
		}
		after := readBytes(t, vaultSnap)
		if string(after) != string(before) {
			t.Error("re-snapshot should be a no-op when outgoing == target")
		}
	})

	t.Run("skips empty outgoing", func(t *testing.T) {
		v, fs, _, _ := setup(t)
		if err := v.ResnapshotOutgoing(fs, "", "personal"); err != nil {
			t.Fatalf("error = %v", err)
		}
	})
}
