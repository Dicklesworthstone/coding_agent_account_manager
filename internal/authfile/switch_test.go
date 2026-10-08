package authfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHasAuthFilesReadOnlyFileSources(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		files     map[string]string
		directory string
		want      bool
	}{
		{name: "Claude helper without OAuth file", tool: "claude", files: map[string]string{"settings.json": `{"apiKeyHelper":"get-test-key"}`}, want: true},
		{name: "Claude policy is not auth", tool: "claude", files: map[string]string{"settings.json": `{"model":"opus"}`}},
		{name: "Claude empty primary overrides helper", tool: "claude", files: map[string]string{claudeCredentialsFile: `{}`, "settings.json": `{"apiKeyHelper":"get-test-key"}`}},
		{name: "Claude malformed optional settings", tool: "claude", files: map[string]string{claudeCredentialsFile: keychainCreds("live"), "settings.json": `{"env":null}`}},
		{name: "Cursor config-only keychain metadata", tool: "cursor", files: map[string]string{"cli-config.json": `{"authInfo":{"email":"cursor@example.com"}}`}, want: true},
		{name: "Cursor config-only policy", tool: "cursor", files: map[string]string{"cli-config.json": `{"model":"default"}`}},
		{name: "Cursor empty metadata", tool: "cursor", files: map[string]string{"cli-config.json": `{"authInfo":{}}`}},
		{name: "Cursor empty primary blocks metadata", tool: "cursor", files: map[string]string{"auth.json": `{}`, "cli-config.json": `{"authInfo":{"email":"old@example.com"}}`}},
		{name: "Cursor malformed primary blocks metadata", tool: "cursor", files: map[string]string{"auth.json": `{`, "cli-config.json": `{"authInfo":{"email":"old@example.com"}}`}},
		{name: "Cursor malformed access token", tool: "cursor", files: map[string]string{"auth.json": `{"accessToken":3}`}},
		{name: "Cursor refresh token alone", tool: "cursor", files: map[string]string{"auth.json": `{"refreshToken":"refresh"}`}},
		{name: "Cursor API key with null access slot", tool: "cursor", files: map[string]string{"auth.json": `{"apiKey":"synthetic-key","accessToken":null}`}, want: true},
		{name: "Cursor nonregular primary blocks metadata", tool: "cursor", directory: "auth.json", files: map[string]string{"cli-config.json": `{"authInfo":{"email":"old@example.com"}}`}},
		{name: "Codex credential", tool: "codex", files: map[string]string{"auth.json": `{"access_token":"synthetic-access"}`}, want: true},
		{name: "Codex empty credential", tool: "codex", files: map[string]string{"auth.json": "  \n"}},
		{name: "Codex nonregular source", tool: "codex", directory: "auth.json"},
		{name: "Grok credential", tool: "grok", files: map[string]string{"auth.json": `{"access_token":"synthetic-access"}`}, want: true},
		{name: "Grok config alone", tool: "grok", files: map[string]string{"config.toml": "model = 'grok'"}},
		{name: "OpenCode credential", tool: "opencode", files: map[string]string{"auth.json": `{"provider":{"key":"synthetic-key"}}`}, want: true},
		{name: "Gemini OAuth without settings", tool: "gemini", files: map[string]string{"oauth_creds.json": `{"access_token":"synthetic-access"}`}, want: true},
		{name: "Gemini empty sources", tool: "gemini", files: map[string]string{"settings.json": "", "oauth_creds.json": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CAAM_KEYCHAIN", "0")
			fs := AuthFileSet{Tool: tc.tool}
			add := func(name string, required bool) {
				fs.Files = append(fs.Files, AuthFileSpec{Tool: tc.tool, Path: filepath.Join(root, name), Required: required})
			}
			switch tc.tool {
			case "claude":
				add(claudeCredentialsFile, true)
				add("settings.json", false)
				fs.AllowOptionalOnly = true
			case "cursor":
				add("cli-config.json", false)
				add("auth.json", false)
				fs.AllowOptionalOnly = true
			case "gemini":
				add("settings.json", true)
				add("oauth_creds.json", false)
				fs.AllowOptionalOnly = true
			default:
				add("auth.json", true)
				if tc.tool == "grok" {
					add("config.toml", false)
				}
			}
			for name, data := range tc.files {
				writeFixtureFile(t, filepath.Join(root, name), data)
			}
			if tc.directory != "" {
				if err := os.Mkdir(filepath.Join(root, tc.directory), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if got := HasAuthFilesReadOnly(fs); got != tc.want {
				t.Fatalf("read-only presence = %v, want %v", got, tc.want)
			}
			for name, data := range tc.files {
				if got := readFixtureFile(t, filepath.Join(root, name)); got != data {
					t.Fatalf("read-only presence changed %s", name)
				}
			}
		})
	}
}

func TestSwitchPreservesRotatedCodexCredential(t *testing.T) {
	old := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	target := codexAuthBytes(t, "bob@example.com", old, &old)
	live := codexAuthBytes(t, "alice@example.com", newer, &newer)
	v, fs, livePath := setupCodexRestore(t, target, live)
	writeCodexAuth(t, v.BackupPath("codex", "alice", "auth.json"), "alice@example.com", old, &old)
	metadata := []byte(`{"description":"keep this description","tags":["work"]}`)
	metadataPath := v.BackupPath("codex", "alice", "meta.json")
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		t.Fatal(err)
	}

	result, err := v.Switch(fs, "acct", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousProfile != "alice" || result.ResnapshottedProfile != "alice" || result.AutoBackup != "" || !result.RestoreStarted {
		t.Fatalf("unexpected preservation result: %+v", result)
	}
	if !bytes.Equal(readBytes(t, v.BackupPath("codex", "alice", "auth.json")), live) {
		t.Fatal("outgoing account lost its rotated credential")
	}
	if !bytes.Equal(readBytes(t, metadataPath), metadata) {
		t.Fatal("saving the rotated credential changed user metadata")
	}
	if !bytes.Equal(readBytes(t, livePath), target) {
		t.Fatal("target account was not activated")
	}
}

// Both modes deliberately retain the same OAuth identity. Native selection,
// not the mere presence of that old cache, determines which grant is active.
func selectedSwitchFiles(t *testing.T, tool, key, email string, stamp time.Time) map[string]string {
	t.Helper()
	grant := map[string]interface{}{
		"access_token":  makeCodexJWT(t, email, stamp),
		"id_token":      makeCodexJWT(t, email, stamp),
		"refresh_token": "synthetic-shared-refresh",
	}
	files := make(map[string]string)
	if tool == "codex" {
		auth := map[string]interface{}{"auth_mode": "chatgpt", "tokens": grant, "last_refresh": stamp.Format(time.RFC3339)}
		if key != "" {
			auth["auth_mode"], auth["OPENAI_API_KEY"] = "apikey", key
		}
		data, err := json.Marshal(auth)
		if err != nil {
			t.Fatal(err)
		}
		files["auth.json"] = string(data)
	} else {
		grant["expiry_date"] = stamp.UnixMilli()
		data, err := json.Marshal(grant)
		if err != nil {
			t.Fatal(err)
		}
		files["oauth_creds.json"] = string(data)
		files["settings.json"] = `{"security":{"auth":{"selectedType":"oauth-personal"}}}`
		if key != "" {
			files["settings.json"] = `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`
			files[".env"] = "export GEMINI_API_KEY='" + key + "' # selected grant\n"
		}
	}
	return files
}

func TestSwitchDoesNotOverwriteAnotherSelectedMethod(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tool := range []string{"codex", "gemini"} {
		for _, selectedLive := range []bool{false, true} {
			name := "oauth-live"
			liveKey, savedKey := "", "synthetic-saved-key"
			if selectedLive {
				name, liveKey, savedKey = "key-live", "synthetic-live-key", ""
			}
			t.Run(tool+"/"+name, func(t *testing.T) {
				live := selectedSwitchFiles(t, tool, liveKey, "shared@example.test", old.Add(time.Hour))
				saved := selectedSwitchFiles(t, tool, savedKey, "shared@example.test", old)
				target := selectedSwitchFiles(t, tool, "", "other@example.test", old)
				v, fs, liveDir := discoveryFixture(t, tool, live)
				writeSwitchProfile(t, v, tool, "saved", saved)
				writeSwitchProfile(t, v, tool, "target", target)
				result, err := v.Switch(fs, "target", SwitchOptions{})
				if err != nil || result.ResnapshottedProfile != "" || result.PreviousProfile != "" || result.AutoBackup == "" || !result.RestoreStarted {
					t.Fatalf("unused OAuth claimed ownership of another method: %+v, %v", result, err)
				}
				for file, want := range saved {
					if got := readFixtureFile(t, v.BackupPath(tool, "saved", file)); got != want {
						t.Fatalf("outgoing preservation overwrote saved %s", file)
					}
				}
				for file, want := range live {
					if got := readFixtureFile(t, v.BackupPath(tool, result.AutoBackup, file)); got != want {
						t.Fatalf("outgoing %s was not retained in recovery", file)
					}
				}
				for file, want := range target {
					if got := readFixtureFile(t, filepath.Join(liveDir, file)); got != want {
						t.Fatalf("requested %s was not activated", file)
					}
				}
			})
		}
	}
}

func TestSwitchActivatesSelectedMethodDespiteUnusedOAuthFreshness(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tool := range []string{"codex", "gemini"} {
		for _, tc := range []struct{ name, liveKey, targetKey string }{
			{"oauth-to-key", "", "synthetic-target-key"},
			{"key-to-oauth", "synthetic-live-key", ""},
			{"different-key", "synthetic-live-key", "synthetic-target-key"},
			{"same-key", "synthetic-same-key", "synthetic-same-key"},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				live := selectedSwitchFiles(t, tool, tc.liveKey, "shared@example.test", old.Add(time.Hour))
				target := selectedSwitchFiles(t, tool, tc.targetKey, "shared@example.test", old)
				v, fs, liveDir := discoveryFixture(t, tool, live)
				writeSwitchProfile(t, v, tool, "target", target)
				result, err := v.Switch(fs, "target", SwitchOptions{})
				if err != nil || result.KeptLive || !result.RestoreStarted {
					t.Fatalf("unused OAuth suppressed requested method: %+v, %v", result, err)
				}
				for file, want := range target {
					if got := readFixtureFile(t, filepath.Join(liveDir, file)); got != want {
						t.Fatalf("selected target %s was not restored", file)
					}
				}
			})
		}
	}
}

func TestCurrentProfileMatchesSelectedKeyAcrossUnusedOAuthChurn(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tool := range []string{"codex", "gemini"} {
		t.Run(tool, func(t *testing.T) {
			live := selectedSwitchFiles(t, tool, "synthetic-selected-key", "new-cache@example.test", old.Add(time.Hour))
			v, fs, _ := discoveryFixture(t, tool, live)
			writeSwitchProfile(t, v, tool, "selected", selectedSwitchFiles(t, tool, "synthetic-selected-key", "old-cache@example.test", old))
			writeSwitchProfile(t, v, tool, "other-key", selectedSwitchFiles(t, tool, "synthetic-other-key", "new-cache@example.test", old.Add(time.Hour)))
			writeSwitchProfile(t, v, tool, "oauth", selectedSwitchFiles(t, tool, "", "new-cache@example.test", old.Add(time.Hour)))
			owner, err := v.CurrentProfile(fs)
			if err != nil || owner != "selected" {
				t.Fatalf("current profile follows unused cache rather than selected key: %q, %v", owner, err)
			}
		})
	}
}

func TestSwitchGeminiSelectedKeyIgnoresMalformedUnusedCache(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	live := selectedSwitchFiles(t, "gemini", "", "live@example.test", old.Add(time.Hour))
	target := selectedSwitchFiles(t, "gemini", "synthetic-selected-key", "unused@example.test", old)
	target["oauth_creds.json"] = "{"
	v, fs, liveDir := discoveryFixture(t, "gemini", live)
	writeSwitchProfile(t, v, "gemini", "target", target)
	result, err := v.Switch(fs, "target", SwitchOptions{})
	if err != nil || result.KeptLive || !result.RestoreStarted {
		t.Fatalf("unused malformed cache blocked a complete selected key: %+v, %v", result, err)
	}
	for file, want := range target {
		if got := readFixtureFile(t, filepath.Join(liveDir, file)); got != want {
			t.Fatalf("selected target %s was not restored", file)
		}
	}
}

func TestSwitchRetainsGenuineOAuthFreshnessProtection(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tool := range []string{"codex", "gemini"} {
		t.Run(tool, func(t *testing.T) {
			live := selectedSwitchFiles(t, tool, "", "shared@example.test", old.Add(time.Hour))
			v, fs, liveDir := discoveryFixture(t, tool, live)
			writeSwitchProfile(t, v, tool, "target", selectedSwitchFiles(t, tool, "", "shared@example.test", old))
			result, err := v.Switch(fs, "target", SwitchOptions{})
			if err != nil || !result.KeptLive || result.RestoreStarted {
				t.Fatalf("same-OAuth freshness protection was lost: %+v, %v", result, err)
			}
			for file, want := range live {
				if got := readFixtureFile(t, filepath.Join(liveDir, file)); got != want {
					t.Fatalf("newer native %s was replaced", file)
				}
			}
		})
	}
}

func TestSwitchRejectsSelectedKeyMissingItsCredential(t *testing.T) {
	old := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, tool := range []string{"codex", "gemini"} {
		t.Run(tool, func(t *testing.T) {
			live := selectedSwitchFiles(t, tool, "", "live@example.test", old)
			target := selectedSwitchFiles(t, tool, "synthetic-key", "live@example.test", old)
			if tool == "codex" {
				var auth map[string]interface{}
				if err := json.Unmarshal([]byte(target["auth.json"]), &auth); err != nil {
					t.Fatal(err)
				}
				delete(auth, "OPENAI_API_KEY")
				data, err := json.Marshal(auth)
				if err != nil {
					t.Fatal(err)
				}
				target["auth.json"] = string(data)
			} else {
				delete(target, ".env")
			}
			v, fs, liveDir := discoveryFixture(t, tool, live)
			writeSwitchProfile(t, v, tool, "target", target)
			result, err := v.Switch(fs, "target", SwitchOptions{BackupMode: "always", PreserveOriginal: true})
			if err == nil || result.KeptLive || result.RestoreStarted || result.AutoBackup != "" || result.OriginalBackup {
				t.Fatalf("missing selected key borrowed unused OAuth: %+v, %v", result, err)
			}
			for file, want := range live {
				if got := readFixtureFile(t, filepath.Join(liveDir, file)); got != want {
					t.Fatalf("rejected target changed native %s", file)
				}
			}
		})
	}
}

func TestSwitchPreservesRotatedClaudeCredential(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("bob", bobGen1, bobSettings(1))
	f.writeProfile("_backup_exact", aliceGen2, aliceSettings(9))
	f.writeLive(aliceGen2, aliceSettings(9))
	settingsBefore := readFixtureFile(t, f.profileFile("alice", claudeSettingsFile))

	result, err := f.vault.Switch(f.fileSet, "bob", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousProfile != "alice" || result.ResnapshottedProfile != "alice" || result.AutoBackup == "" {
		t.Fatalf("named owner should receive rotated auth despite a matching system backup: %+v", result)
	}
	if got := readFixtureFile(t, f.profileFile("alice", claudeCredentialsFile)); got != aliceGen2 {
		t.Fatal("outgoing Claude credential was not saved")
	}
	if got := readFixtureFile(t, f.profileFile("alice", claudeSettingsFile)); got != settingsBefore {
		t.Fatal("credential preservation changed saved Claude settings")
	}
	if got := readFixtureFile(t, f.profileFile(result.AutoBackup, claudeSettingsFile)); got != aliceSettings(9) {
		t.Fatal("changed auxiliary files were not preserved in the recovery snapshot")
	}
	if got := readFixtureFile(t, f.liveCreds); got != bobGen1 {
		t.Fatal("target Claude credential was not activated")
	}
}

func TestSwitchPreservesNativeGrokCredential(t *testing.T) {
	old := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	makeAuth := func(id, access string, expiry time.Time) []byte {
		data, err := json.Marshal(map[string]interface{}{
			"https://auth.x.ai::client": map[string]interface{}{
				"user_id": id, "email": id + "@example.com", "access_token": access,
				"refresh_token": "synthetic-refresh-" + access, "expires_at": expiry.Format(time.RFC3339),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	v, fs, livePath := setupCodexRestore(t, makeAuth("bob", "bob-old", old), makeAuth("alice", "alice-new", old.Add(time.Hour)))
	// Reuse the isolated fileset fixture with the native Grok parser.
	fs.Tool = "grok"
	for i := range fs.Files {
		fs.Files[i].Tool = "grok"
	}
	for name, data := range map[string][]byte{"acct": makeAuth("bob", "bob-old", old), "alice": makeAuth("alice", "alice-old", old)} {
		path := v.BackupPath("grok", name, "auth.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	live := readBytes(t, livePath)
	result, err := v.Switch(fs, "acct", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResnapshottedProfile != "alice" || !bytes.Equal(readBytes(t, v.BackupPath("grok", "alice", "auth.json")), live) {
		t.Fatalf("native Grok rotation was not preserved: %+v", result)
	}
}

func TestSwitchCreatesUniqueImmutableRecoverySnapshots(t *testing.T) {
	target := []byte(`{"access_token":"synthetic-target"}`)
	live := []byte(`{"access_token":"synthetic-unsaved-one"}`)
	v, fs, livePath := setupCodexRestore(t, target, live)
	first, err := v.Switch(fs, "acct", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.AutoBackup, "_backup_") || first.PreviousProfile != "" {
		t.Fatalf("unknown live login needs a recovery snapshot: %+v", first)
	}
	firstPath := v.BackupPath("codex", first.AutoBackup, "auth.json")
	if !bytes.Equal(readBytes(t, firstPath), live) {
		t.Fatal("backup did not preserve exact outgoing credential bytes")
	}
	if err := os.WriteFile(livePath, []byte(`{"access_token":"synthetic-unsaved-two"}`), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := v.Switch(fs, "acct", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.AutoBackup == first.AutoBackup || second.AutoBackup == "" {
		t.Fatal("successive switches reused a system snapshot name")
	}
	if !bytes.Equal(readBytes(t, firstPath), live) {
		t.Fatal("a later switch overwrote immutable recovery material")
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(readBytes(t, v.BackupPath("codex", first.AutoBackup, "meta.json")), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["type"] != "system" || meta["created_by"] != "auto" {
		t.Fatalf("recovery metadata is not a system backup: %v", meta)
	}
	if err := v.Backup(fs, first.AutoBackup); err == nil {
		t.Fatal("recovery snapshot can be overwritten through Backup")
	}
}

func TestSwitchRejectsIncomingBeforePreservation(t *testing.T) {
	for _, malformed := range []string{"{", "null", `[]`, `"not an auth object"`} {
		t.Run(malformed, func(t *testing.T) {
			live := []byte(`{"access_token":"synthetic-current"}`)
			v, fs, livePath := setupCodexRestore(t, []byte(malformed), live)
			result, err := v.Switch(fs, "acct", SwitchOptions{BackupMode: "always", PreserveOriginal: true})
			if err == nil || result.RestoreStarted || result.AutoBackup != "" || result.OriginalBackup {
				t.Fatalf("invalid incoming data must fail before any preservation: result=%+v, err=%v", result, err)
			}
			if !bytes.Equal(readBytes(t, livePath), live) {
				t.Fatal("invalid target modified the live login")
			}
			profiles, err := v.List("codex")
			if err != nil || len(profiles) != 1 || profiles[0] != "acct" {
				t.Fatalf("invalid target changed the vault: %v, %v", profiles, err)
			}
		})
	}

	t.Run("malformed Claude settings", func(t *testing.T) {
		f := newClaudeRotationFixture(t)
		f.writeProfile("bob", bobGen1, `{"env":null}`)
		f.writeLive(aliceGen2, aliceSettings(9))
		if _, err := f.vault.Switch(f.fileSet, "bob", SwitchOptions{BackupMode: "always", PreserveOriginal: true}); err == nil {
			t.Fatal("malformed target settings should fail")
		}
		if got := readFixtureFile(t, f.liveCreds); got != aliceGen2 {
			t.Fatal("malformed settings changed the live credential")
		}
		profiles, err := f.vault.List("claude")
		if err != nil || len(profiles) != 1 {
			t.Fatalf("malformed settings caused backup side effects: %v, %v", profiles, err)
		}
	})
}

func TestSwitchKeepsNewerSameAccountBeforeAnyWrites(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))
	result, err := f.vault.Switch(f.fileSet, "alice", SwitchOptions{BackupMode: "always", PreserveOriginal: true})
	if err != nil || !result.KeptLive || result.RestoreStarted || result.AutoBackup != "" || result.OriginalBackup {
		t.Fatalf("same-account stale target should be an untouched success: %+v, %v", result, err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen2 {
		t.Fatal("stale activation rolled back native Claude credentials")
	}
	if got := readFixtureFile(t, f.liveSettings); got != aliceSettings(9) {
		t.Fatal("stale activation changed live settings before the freshness guard")
	}
	if got := readFixtureFile(t, f.profileFile("alice", claudeCredentialsFile)); got != aliceGen1 {
		t.Fatal("stale activation rewrote the incoming saved profile")
	}
}

func TestSwitchAccountIDOverridesSharedEmail(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("bob", bobGen1, bobSettings(1))
	// The email matches Alice but the account ID explicitly does not.
	f.writeLive(aliceGen2, claudeRotationSettings("other-account", rotAliceEmail, "", 9))
	result, err := f.vault.Switch(f.fileSet, "bob", SwitchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousProfile != "" || result.ResnapshottedProfile != "" || result.AutoBackup == "" {
		t.Fatalf("conflicting account IDs must produce a separate recovery snapshot: %+v", result)
	}
	if got := readFixtureFile(t, f.profileFile("alice", claudeCredentialsFile)); got != aliceGen1 {
		t.Fatal("shared email overwrote a different account's saved credentials")
	}
}

func TestSwitchDoesNotWeakenNamedCredentials(t *testing.T) {
	for _, test := range []struct {
		name string
		live string
	}{
		{"missing refresh token", claudeRotationCreds("synthetic-incomplete", "", 3000)},
		{"older live token", claudeRotationCreds("synthetic-older", "synthetic-refresh", 500)},
		{"unknown freshness", `{"claudeAiOauth":{"accessToken":"synthetic-unknown","refreshToken":"synthetic-refresh"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("alice", aliceGen1, aliceSettings(1))
			f.writeProfile("bob", bobGen1, bobSettings(1))
			f.writeLive(test.live, aliceSettings(9))
			result, err := f.vault.Switch(f.fileSet, "bob", SwitchOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result.ResnapshottedProfile != "" || result.AutoBackup == "" {
				t.Fatalf("unproven live freshness must not update named credentials: %+v", result)
			}
			if got := readFixtureFile(t, f.profileFile("alice", claudeCredentialsFile)); got != aliceGen1 {
				t.Fatal("unproven outgoing credential weakened the named snapshot")
			}
			if got := readFixtureFile(t, f.profileFile(result.AutoBackup, claudeCredentialsFile)); got != test.live {
				t.Fatal("unproven outgoing material was not preserved separately")
			}
		})
	}
}

func TestSwitchBackupModesAndOriginal(t *testing.T) {
	for _, test := range []struct {
		name     string
		opts     SwitchOptions
		wantAuto bool
		wantOrig bool
	}{
		{"default smart", SwitchOptions{}, true, false},
		{"always", SwitchOptions{BackupMode: "always"}, true, false},
		{"never", SwitchOptions{BackupMode: "never"}, false, false},
		{"first activation", SwitchOptions{PreserveOriginal: true}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			live := []byte(`{"access_token":"synthetic-unsaved"}`)
			v, fs, _ := setupCodexRestore(t, []byte(`{"access_token":"synthetic-target"}`), live)
			result, err := v.Switch(fs, "acct", test.opts)
			if err != nil || (result.AutoBackup != "") != test.wantAuto || result.OriginalBackup != test.wantOrig {
				t.Fatalf("unexpected backup policy outcome: %+v, %v", result, err)
			}
			if test.wantOrig && !bytes.Equal(readBytes(t, v.BackupPath("codex", "_original", "auth.json")), live) {
				t.Fatal("first activation did not preserve the original login")
			}
		})
	}
}

func TestSwitchPreservationFailureLeavesLiveUntouched(t *testing.T) {
	live := []byte(`{"access_token":"synthetic-unsaved"}`)
	v, fs, path := setupCodexRestore(t, []byte(`{"access_token":"synthetic-target"}`), live)
	// A file where the immutable original directory belongs is a deterministic
	// preservation failure, including when the test suite runs as root.
	if err := os.WriteFile(v.ProfilePath("codex", "_original"), []byte("do not replace"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := v.Switch(fs, "acct", SwitchOptions{PreserveOriginal: true})
	if err == nil || result.RestoreStarted {
		t.Fatalf("failed required preservation must stop activation: %+v, %v", result, err)
	}
	if !bytes.Equal(readBytes(t, path), live) {
		t.Fatal("failed preservation still overwrote the live login")
	}
}

func writeSwitchProfile(t *testing.T, v *Vault, tool, name string, files map[string]string) {
	t.Helper()
	dir := v.ProfilePath(tool, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for filename, data := range files {
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSwitchPreservesEveryCredentialSource(t *testing.T) {
	for _, test := range []struct {
		name     string
		tool     string
		required string
		live     map[string]string
		saved    map[string]string
		target   map[string]string
	}{
		{
			name: "agy authoritative token", tool: "agy", required: "antigravity-oauth-token",
			live:   map[string]string{"antigravity-oauth-token": "new-native-token", "oauth_creds.json": `{"access_token":"shared-cache"}`},
			saved:  map[string]string{"antigravity-oauth-token": "old-native-token", "oauth_creds.json": `{"access_token":"shared-cache"}`},
			target: map[string]string{"antigravity-oauth-token": "target-native-token", "oauth_creds.json": `{"access_token":"target-cache"}`},
		},
		{
			name: "gemini optional API key only", tool: "gemini", required: "settings.json",
			live:   map[string]string{".env": "GEMINI_API_KEY=synthetic-unsaved"},
			target: map[string]string{".env": "GEMINI_API_KEY=synthetic-target"},
		},
		{
			name: "gemini independently changed secondary credential", tool: "gemini", required: "settings.json",
			live:   map[string]string{"settings.json": `{}`, "oauth_creds.json": `{"access_token":"unchanged-oauth"}`, ".env": "GEMINI_API_KEY=synthetic-new"},
			saved:  map[string]string{"settings.json": `{}`, "oauth_creds.json": `{"access_token":"unchanged-oauth"}`, ".env": "GEMINI_API_KEY=synthetic-old"},
			target: map[string]string{"settings.json": `{}`, "oauth_creds.json": `{"access_token":"target-oauth"}`, ".env": "GEMINI_API_KEY=synthetic-target"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			v := NewVault(filepath.Join(base, "vault"))
			liveDir := filepath.Join(base, "live")
			if err := os.Mkdir(liveDir, 0700); err != nil {
				t.Fatal(err)
			}
			fs := AuthFileSet{Tool: test.tool, AllowOptionalOnly: true}
			names := map[string]bool{test.required: true}
			for name := range test.target {
				names[name] = true
			}
			for name := range test.live {
				names[name] = true
			}
			for name := range names {
				fs.Files = append(fs.Files, AuthFileSpec{Tool: test.tool, Path: filepath.Join(liveDir, name), Required: name == test.required})
			}
			for name, data := range test.live {
				if err := os.WriteFile(filepath.Join(liveDir, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeSwitchProfile(t, v, test.tool, "target", test.target)
			if test.saved != nil {
				writeSwitchProfile(t, v, test.tool, "saved", test.saved)
			}
			result, err := v.Switch(fs, "target", SwitchOptions{})
			if err != nil || result.AutoBackup == "" || result.KeptLive {
				t.Fatalf("unsaved credentials require a recovery snapshot: %+v, %v", result, err)
			}
			for name, want := range test.live {
				if got := readFixtureFile(t, v.BackupPath(test.tool, result.AutoBackup, name)); got != want {
					t.Fatalf("live %s was not preserved", name)
				}
			}
			for name, want := range test.target {
				if got := readFixtureFile(t, filepath.Join(liveDir, name)); got != want {
					t.Fatalf("target %s was not activated", name)
				}
			}
		})
	}
}

func TestSwitchRestoresSettingsWithIdenticalCredential(t *testing.T) {
	base := t.TempDir()
	v := NewVault(filepath.Join(base, "vault"))
	liveDir := filepath.Join(base, "live")
	if err := os.Mkdir(liveDir, 0700); err != nil {
		t.Fatal(err)
	}
	fs := AuthFileSet{Tool: "gemini", Files: []AuthFileSpec{
		{Path: filepath.Join(liveDir, "settings.json"), Required: true},
		{Path: filepath.Join(liveDir, "oauth_creds.json")},
	}}
	writeFixtureFile(t, fs.Files[0].Path, `{"model":"live-model"}`)
	writeFixtureFile(t, fs.Files[1].Path, `{"access_token":"same-token"}`)
	writeSwitchProfile(t, v, "gemini", "target", map[string]string{
		"settings.json": `{"model":"target-model"}`, "oauth_creds.json": `{"access_token":"same-token"}`,
	})
	result, err := v.Switch(fs, "target", SwitchOptions{})
	if err != nil || result.KeptLive || !result.RestoreStarted {
		t.Fatalf("identical credentials must not suppress a settings switch: %+v, %v", result, err)
	}
	if got := readFixtureFile(t, fs.Files[0].Path); got != `{"model":"target-model"}` {
		t.Fatal("requested settings were not restored")
	}
}

func TestSwitchRetentionKeepsCurrentRecoveryAfterClockRollback(t *testing.T) {
	live := []byte(`{"access_token":"synthetic-unsaved"}`)
	v, fs, _ := setupCodexRestore(t, []byte(`{"access_token":"synthetic-target"}`), live)
	writeSwitchProfile(t, v, "codex", "_backup_99991231_235959", map[string]string{"auth.json": `{"access_token":"future-backup"}`})
	result, err := v.Switch(fs, "acct", SwitchOptions{MaxAutoBackups: 1})
	if err != nil || result.AutoBackup == "" {
		t.Fatalf("switch failed: %+v, %v", result, err)
	}
	if !bytes.Equal(readBytes(t, v.BackupPath("codex", result.AutoBackup, "auth.json")), live) {
		t.Fatal("retention deleted the recovery material needed by this switch")
	}
	profiles, err := v.List("codex")
	if err != nil || len(profiles) != 2 {
		t.Fatalf("retention should keep the target and current recovery: %v, %v", profiles, err)
	}
}

func TestSwitchReadsKeychainWithoutMirroringBeforeStaleGuard(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(aliceGen2)
	writeFixtureFile(t, f.statePath, aliceSettings(9))
	writeSwitchProfile(t, f.vault, "claude", "alice", map[string]string{
		claudeCredentialsFile: aliceGen1, claudeSettingsFile: aliceSettings(1),
	})
	owner, err := f.vault.CurrentProfile(f.fileSet)
	if err != nil || owner != "alice" {
		t.Fatalf("read-only owner = %q, %v", owner, err)
	}
	result, err := f.vault.Switch(f.fileSet, "alice", SwitchOptions{BackupMode: "always", PreserveOriginal: true})
	if err != nil || !result.KeptLive || result.AutoBackup != "" {
		t.Fatalf("newer keychain credential must be retained: %+v, %v", result, err)
	}
	if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
		t.Fatalf("read-only owner/freshness check created a keychain mirror: %v", err)
	}
	if got, ok := f.storedToken(); !ok || got != aliceGen2 {
		t.Fatal("same-account activation changed the authoritative keychain")
	}
}

func TestSwitchRestoreFailureNamesRecoveryCopy(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(aliceGen2)
	writeFixtureFile(t, f.statePath, aliceSettings(9))
	writeSwitchProfile(t, f.vault, "claude", "bob", map[string]string{
		claudeCredentialsFile: bobGen1, claudeSettingsFile: bobSettings(1),
	})
	// Delegate reads to the normal fake keychain, but fail the native write
	// after Restore has already applied files. No real keychain is involved.
	t.Setenv("CAAM_TEST_SECURITY_READER", os.Getenv("CAAM_KEYCHAIN_BIN"))
	wrapper := filepath.Join(t.TempDir(), "security-fail-write")
	script := "#!/bin/sh\nif [ \"$1\" = add-generic-password ]; then\n  echo 'security: write refused' >&2\n  exit 1\nfi\nexec \"$CAAM_TEST_SECURITY_READER\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAAM_KEYCHAIN_BIN", wrapper)
	result, err := f.vault.Switch(f.fileSet, "bob", SwitchOptions{})
	if err == nil || !result.RestoreStarted || result.AutoBackup == "" || !strings.Contains(err.Error(), "claude/"+result.AutoBackup) {
		t.Fatalf("restore failure omitted its recovery reference: %+v, %v", result, err)
	}
	if got := readFixtureFile(t, f.vault.BackupPath("claude", result.AutoBackup, claudeCredentialsFile)); got != aliceGen2 {
		t.Fatal("restore failure lost the outgoing keychain credential")
	}
}
