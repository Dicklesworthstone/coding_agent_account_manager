package health

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

func liveExpiryJWT(expiry time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix())))
	return "e30." + payload + ".synthetic"
}

func writeLiveExpiryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSavedAPIKeySelectionMatchesLiveHealth(t *testing.T) {
	for _, tc := range []struct {
		name, tool, settings, cache string
	}{
		{"codex_expired_oauth", "codex", "", `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-selected-key","tokens":{"access_token":"synthetic-unused-oauth","expires_at":1600000000}}`},
		{"codex_malformed_unused_oauth", "codex", "", `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-selected-key","tokens":17}`},
		{"codex_key_only", "codex", "", `{"OPENAI_API_KEY":"synthetic-selected-key"}`},
		{"gemini_legacy_selector", "gemini", `{"selectedAuthType":"gemini-api-key"}`, `{"access_token":"synthetic-unused-oauth","expires_at":1600000000}`},
		{"gemini_current_selector", "gemini", `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`, `{"access_token":"synthetic-unused-oauth","expires_at":1600000000}`},
		{"gemini_malformed_unused_oauth", "gemini", `{"selectedAuthType":"gemini-api-key"}`, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, tc.tool, "work")
			t.Setenv("CAAM_KEYCHAIN", "0")
			t.Setenv("CODEX_HOME", dir)
			t.Setenv("GEMINI_HOME", dir)
			files := authfile.CodexAuthFiles()
			before := map[string]string{"auth.json": tc.cache}
			parse := func() (*ExpiryInfo, error) { return ParseCodexExpiry(filepath.Join(dir, "auth.json")) }
			if tc.tool == "gemini" {
				files = authfile.GeminiAuthFiles()
				before = map[string]string{"settings.json": tc.settings, "oauth_creds.json": tc.cache, ".env": "GEMINI_API_KEY=synthetic-selected-key\n"}
				parse = func() (*ExpiryInfo, error) { return ParseGeminiExpiry(dir) }
			}
			for name, data := range before {
				writeLiveExpiryFile(t, filepath.Join(dir, name), data)
			}
			live, err := ParseLiveExpiry(files)
			if err != nil || live == nil {
				t.Fatalf("live reference: %+v, %v", live, err)
			}
			saved, err := parse()
			if err != nil || saved == nil || !saved.ExpiresAt.IsZero() || !saved.Renewable || !saved.SelfRefreshing || saved.HasRefreshToken || saved.Fingerprint != live.Fingerprint {
				t.Fatalf("saved API-key selection differs from live: saved=%+v live=%+v err=%v", saved, live, err)
			}
			store := NewStorage(filepath.Join(root, "health.json"))
			store.SetVaultPath(root)
			if err := store.UpdateProfile(tc.tool, "work", &ProfileHealth{
				TokenExpiresAt: time.Unix(1600000000, 0), ProviderRejectedAt: time.Now(),
				ProviderRejection: "access_token_rejected", RejectedFingerprint: credentialFingerprint("synthetic-unused-oauth"),
			}); err != nil {
				t.Fatal(err)
			}
			h, err := store.GetProfile(tc.tool, "work")
			if err != nil || h == nil || !h.TokenExpiresAt.IsZero() || !h.TokenRenewable || !h.SelfRefreshing || h.CredentialFingerprint != live.Fingerprint || h.ProviderRejected() {
				t.Fatalf("stored health revived the unused OAuth grant: %+v, %v", h, err)
			}
			for name, want := range before {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != want {
					t.Fatalf("passive health changed %s: %v", name, err)
				}
			}
		})
	}
}

func TestParseLiveExpiryCredentialModes(t *testing.T) {
	expiry := time.Now().Add(-time.Hour).Truncate(time.Second)
	jwt := liveExpiryJWT(expiry)
	for _, tc := range []struct {
		name, tool, file, data string
		renewable, self        bool
		unknown                bool
	}{
		{"cursor_session", "cursor", "auth.json", fmt.Sprintf(`{"accessToken":%q,"refreshToken":%q}`, jwt, jwt), false, false, false},
		{"cursor_api_key", "cursor", "auth.json", fmt.Sprintf(`{"accessToken":%q,"apiKey":"synthetic-key"}`, jwt), true, true, false},
		{"cursor_key_without_token", "cursor", "auth.json", `{"apiKey":"synthetic-key"}`, true, true, true},
		{"cursor_opaque", "cursor", "auth.json", `{"accessToken":"synthetic-opaque"}`, false, false, true},
		{"codex_renewable", "codex", "auth.json", fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"synthetic-refresh"}}`, jwt), true, false, false},
		{"codex_opaque", "codex", "auth.json", `{"tokens":{"access_token":"synthetic-opaque"}}`, false, false, true},
		{"claude_renewable", "claude", ".credentials.json", fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","expiresAt":%d}}`, expiry.UnixMilli()), true, true, false},
		{"claude_session", "claude", ".credentials.json", fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-access","expiresAt":%d}}`, expiry.UnixMilli()), false, false, false},
		{"gemini_renewable", "gemini", "oauth_creds.json", fmt.Sprintf(`{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":%d}`, expiry.Unix()), true, false, false},
		{"gemini_opaque", "gemini", "oauth_creds.json", `{"access_token":"synthetic-opaque"}`, false, false, true},
		{"gemini_api_key", "gemini", ".env", "GEMINI_API_KEY=synthetic-key\n", true, true, true},
		{"grok_renewable", "grok", "auth.json", fmt.Sprintf(`{"https://synthetic.example::client":{"key":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":%d}}`, expiry.Unix()), true, false, false},
		{"grok_opaque", "grok", "auth.json", `{"https://synthetic.example::client":{"key":"synthetic-opaque"}}`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAAM_KEYCHAIN", "0")
			t.Setenv("CURSOR_API_KEY", "synthetic-unrelated-ambient-key")
			path := filepath.Join(t.TempDir(), tc.file)
			writeLiveExpiryFile(t, path, tc.data)
			files := authfile.AuthFileSet{Tool: tc.tool, Files: []authfile.AuthFileSpec{{Path: path}}}
			info, err := ParseLiveExpiry(files)
			if err != nil || info == nil {
				t.Fatalf("parse live credential: %v", err)
			}
			if info.Renewable != tc.renewable || info.SelfRefreshing != tc.self || info.ExpiresAt.IsZero() != tc.unknown || info.Fingerprint == "" || info.Source != path {
				t.Fatalf("incorrect live credential semantics: %+v", info)
			}
			if !tc.unknown && !info.ExpiresAt.Equal(expiry) {
				t.Fatalf("expiry = %v, want %v", info.ExpiresAt, expiry)
			}
			if tc.tool == "cursor" && !tc.renewable && info.ReloginWarningLead != CursorReloginLead {
				t.Fatal("Cursor session lost its relogin warning lead")
			}
			h := &ProfileHealth{TokenExpiresAt: info.ExpiresAt, TokenRenewable: info.Renewable, SelfRefreshing: info.SelfRefreshing, CredentialFingerprint: info.Fingerprint}
			signals := CredentialSignals(h, DefaultHealthConfig())
			if !tc.unknown && (signals.LoginRequired == nil || *signals.LoginRequired == tc.renewable) {
				t.Fatal("renewal semantics did not reach launch eligibility")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.data {
				t.Fatal("expiry read modified live credentials")
			}
		})
	}
}

func TestParseLiveExpiryUsesSuppliedNativePaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini"))
	t.Setenv("GROK_HOME", filepath.Join(root, "grok"))
	t.Setenv("CURSOR_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("CAAM_KEYCHAIN", "0")
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		files authfile.AuthFileSet
		name  string
		data  string
	}{
		{authfile.CursorAuthFiles(), "auth.json", fmt.Sprintf(`{"accessToken":%q}`, liveExpiryJWT(expiry))},
		{authfile.CodexAuthFiles(), "auth.json", fmt.Sprintf(`{"access_token":%q}`, liveExpiryJWT(expiry))},
		{authfile.GeminiAuthFiles(), "oauth_creds.json", fmt.Sprintf(`{"access_token":"synthetic","expires_at":%d}`, expiry.Unix())},
		{authfile.GrokAuthFiles(), "auth.json", fmt.Sprintf(`{"key":"synthetic","expires_at":%d}`, expiry.Unix())},
		{authfile.ClaudeAuthFiles(), ".credentials.json", fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic","expiresAt":%d}}`, expiry.UnixMilli())},
	} {
		t.Run(tc.files.Tool, func(t *testing.T) {
			var path string
			for _, spec := range tc.files.Files {
				if filepath.Base(spec.Path) == tc.name {
					path = spec.Path
				}
			}
			if path == "" {
				t.Fatal("native file set has no credential artifact")
			}
			writeLiveExpiryFile(t, path, tc.data)
			// Resolving paths again would follow this unrelated environment.
			for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "CODEX_HOME", "GEMINI_HOME", "GROK_HOME", "CURSOR_CONFIG_DIR", "CLAUDE_CONFIG_DIR"} {
				t.Setenv(name, filepath.Join(root, "unrelated"))
			}
			info, err := ParseLiveExpiry(tc.files)
			if err != nil || info == nil || info.Source != path || !info.ExpiresAt.Equal(expiry) {
				t.Fatalf("supplied native path was ignored: info=%+v err=%v", info, err)
			}
		})
	}
}

func TestParseLiveExpiryCodexSelectedAPIKey(t *testing.T) {
	expired := liveExpiryJWT(time.Now().Add(-time.Hour))
	oauth := fmt.Sprintf(`{"tokens":{"access_token":%q}}`, expired)
	oldFingerprint := CodexCredentialFingerprint([]byte(oauth))
	if oldFingerprint == "" {
		t.Fatal("synthetic OAuth grant has no fingerprint")
	}
	var keyFingerprint string
	for _, tc := range []struct {
		name, data string
		apiKey     bool
	}{
		{"explicit key with expired OAuth", fmt.Sprintf(`{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-selected-key","tokens":{"access_token":%q}}`, expired), true},
		{"key alias without OAuth", `{"auth_mode":"api-key","apiKey":"synthetic-selected-key"}`, true},
		{"implicit key with null OAuth", `{"OPENAI_API_KEY":"synthetic-selected-key","tokens":null}`, true},
		{"implicit key alias", `{"api_key":"synthetic-selected-key"}`, true},
		{"explicit OAuth ignores key", fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":"synthetic-selected-key","tokens":{"access_token":%q}}`, expired), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			writeLiveExpiryFile(t, path, tc.data)
			info, err := ParseLiveExpiry(authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{{Path: path}}})
			if err != nil || info == nil {
				t.Fatalf("read selected Codex credential: %v", err)
			}
			if !tc.apiKey && info.Fingerprint != CodexCredentialFingerprint([]byte(tc.data)) {
				t.Fatal("OAuth live health and OAuth verification fingerprint different credentials")
			}
			if tc.apiKey && info.Fingerprint == CodexCredentialFingerprint([]byte(tc.data)) {
				t.Fatal("an OAuth-only probe can attribute its verdict to the selected API key")
			}
			h := &ProfileHealth{TokenExpiresAt: info.ExpiresAt, TokenRenewable: info.Renewable, SelfRefreshing: info.SelfRefreshing,
				CredentialFingerprint: info.Fingerprint, ProviderRejectedAt: time.Now(), RejectedFingerprint: oldFingerprint}
			if tc.apiKey {
				if !info.ExpiresAt.IsZero() || !info.Renewable || h.ProviderRejected() {
					t.Fatal("selected API key inherited old OAuth expiry or rejection")
				}
				if keyFingerprint != "" && info.Fingerprint != keyFingerprint {
					t.Fatal("unused OAuth fields or key alias changed the selected key fingerprint")
				}
				keyFingerprint = info.Fingerprint
			} else if info.ExpiresAt.IsZero() || info.Renewable || !h.ProviderRejected() {
				t.Fatal("explicit OAuth mode borrowed an unrelated API key")
			}
			h.RejectedFingerprint = info.Fingerprint
			if signals := CredentialSignals(h, DefaultHealthConfig()); signals.LoginRequired == nil || !*signals.LoginRequired {
				t.Fatal("rejection of the selected credential did not block launch")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	writeLiveExpiryFile(t, path, `{"OPENAI_API_KEY":"synthetic-replacement-key"}`)
	replacement, err := ParseLiveExpiry(authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{{Path: path}}})
	if err != nil || replacement == nil || replacement.Fingerprint == "" || replacement.Fingerprint == keyFingerprint {
		t.Fatal("replacing the API key did not replace its fingerprint")
	}
}

func TestParseLiveExpiryUnknownAndMalformed(t *testing.T) {
	for _, tc := range []struct {
		name, tool, file, data string
		want                   error
		malformed              bool
	}{
		{"cursor_metadata", "cursor", "cli-config.json", `{"authInfo":{"email":"synthetic@example.com"}}`, ErrNoExpiry, false},
		{"invalid_cursor_json", "cursor", "auth.json", "{", nil, true},
		{"invalid_cursor_slot", "cursor", "auth.json", `{"accessToken":17}`, nil, true},
		{"invalid_claude_json", "claude", ".credentials.json", "{", nil, true},
		{"invalid_claude_slot", "claude", ".credentials.json", `{"claudeAiOauth":{"accessToken":17}}`, nil, true},
		{"invalid_codex_slot", "codex", "auth.json", `{"tokens":{"access_token":17}}`, nil, true},
		{"invalid_gemini_json", "gemini", "oauth_creds.json", "null", nil, true},
		{"invalid_grok_json", "grok", "auth.json", "[]", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAAM_KEYCHAIN", "0")
			path := filepath.Join(t.TempDir(), tc.file)
			writeLiveExpiryFile(t, path, tc.data)
			files := authfile.AuthFileSet{Tool: tc.tool, Files: []authfile.AuthFileSpec{{Path: path}}}
			_, err := ParseLiveExpiry(files)
			if tc.malformed {
				if err == nil || errors.Is(err, ErrNoExpiry) || errors.Is(err, ErrNoAuthFile) {
					t.Fatalf("malformed live credential accepted as unknown: %v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	files := authfile.AuthFileSet{Tool: "cursor", Files: []authfile.AuthFileSpec{{Path: filepath.Join(t.TempDir(), "auth.json")}}}
	if _, err := ParseLiveExpiry(files); !errors.Is(err, ErrNoAuthFile) {
		t.Fatalf("missing credential: %v", err)
	}
}

func TestParseLiveExpiryGeminiSelectedAPIKey(t *testing.T) {
	for _, settings := range []string{`{"selectedAuthType":"gemini-api-key"}`, `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`} {
		for _, cache := range []string{`{"access_token":"synthetic-old-oauth","expires_at":1600000000}`, `null`, `{`} {
			t.Run(fmt.Sprintf("selector_%d/cache_%d", len(settings), len(cache)), func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("GEMINI_HOME", root)
				t.Setenv("GEMINI_API_KEY", "synthetic-unrelated-ambient")
				const key = "synthetic-selected-key"
				const env = "# captured native credential\nexport GEMINI_API_KEY='" + key + "' # selected account\nEDITOR=vim\n"
				before := map[string]string{"settings.json": settings, "oauth_creds.json": cache, ".env": env}
				for name, data := range before {
					writeLiveExpiryFile(t, filepath.Join(root, name), data)
				}
				files := authfile.GeminiAuthFiles()
				name, data, err := authfile.ReadLiveCredential(files)
				if err != nil || name != ".env" || string(data) != env {
					t.Fatalf("API selector retained OAuth cache: name=%q err=%v", name, err)
				}
				info, err := ParseLiveExpiry(files)
				if err != nil || info == nil || !info.Renewable || !info.SelfRefreshing || !info.ExpiresAt.IsZero() || info.Source != filepath.Join(root, ".env") || info.Fingerprint != credentialFingerprint(key) {
					t.Fatalf("selected API key health: info=%+v err=%v", info, err)
				}
				h := &ProfileHealth{TokenRenewable: info.Renewable, SelfRefreshing: info.SelfRefreshing, CredentialFingerprint: info.Fingerprint,
					ProviderRejectedAt: time.Now(), RejectedFingerprint: credentialFingerprint("synthetic-old-oauth"), ProviderRejection: "access_token_rejected"}
				if h.ProviderRejected() {
					t.Fatal("old OAuth rejection blocked the selected stored key")
				}
				h.RejectedFingerprint = credentialFingerprint(key)
				if signals := CredentialSignals(h, DefaultHealthConfig()); signals.LoginRequired == nil || !*signals.LoginRequired {
					t.Fatal("rejection of the selected key was ignored")
				}
				for name, want := range before {
					after, err := os.ReadFile(filepath.Join(root, name))
					if err != nil || string(after) != want {
						t.Fatalf("passive API-key selection changed %s", name)
					}
				}
			})
		}
	}
}

func TestParseLiveExpiryGeminiVertexDoesNotBorrowOAuth(t *testing.T) {
	for _, settings := range []string{`{"selectedAuthType":"vertex-ai"}`, `{"security":{"auth":{"selectedType":"vertex-ai"}}}`} {
		t.Run(settings, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("GEMINI_HOME", root)
			writeLiveExpiryFile(t, filepath.Join(root, "settings.json"), settings)
			writeLiveExpiryFile(t, filepath.Join(root, "oauth_creds.json"), `{"access_token":"synthetic-unused","expires_at":1600000000}`)
			info, err := ParseLiveExpiry(authfile.GeminiAuthFiles())
			if !errors.Is(err, ErrNoExpiry) || info != nil {
				t.Fatalf("Vertex selection borrowed OAuth expiry: info=%+v err=%v", info, err)
			}
		})
	}
}

func TestParseLiveExpiryGeminiSelectedModeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, settings, env string
	}{
		{"missing_selected_key", `{"selectedAuthType":"gemini-api-key"}`, ""},
		{"empty_selected_key", `{"selectedAuthType":"gemini-api-key"}`, "GEMINI_API_KEY=\n"},
		{"malformed_key", `{"selectedAuthType":"gemini-api-key"}`, "GEMINI_API_KEY='unterminated\n"},
		{"conflicting_env_mode", `{"selectedAuthType":"gemini-api-key"}`, "GEMINI_API_KEY=synthetic\nGOOGLE_GENAI_USE_VERTEXAI=true\n"},
		{"conflicting_selectors", `{"selectedAuthType":"oauth-personal","security":{"auth":{"selectedType":"gemini-api-key"}}}`, "GEMINI_API_KEY=synthetic\n"},
		{"null_selector", `{"security":{"auth":{"selectedType":null}}}`, "GEMINI_API_KEY=synthetic\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("GEMINI_HOME", root)
			writeLiveExpiryFile(t, filepath.Join(root, "settings.json"), tc.settings)
			writeLiveExpiryFile(t, filepath.Join(root, "oauth_creds.json"), `{"access_token":"synthetic-unrelated-valid","refresh_token":"synthetic-refresh"}`)
			if tc.env != "" {
				writeLiveExpiryFile(t, filepath.Join(root, ".env"), tc.env)
			}
			if _, err := ParseLiveExpiry(authfile.GeminiAuthFiles()); !errors.Is(err, authfile.ErrInvalidCredentials) {
				t.Fatalf("invalid selected mode borrowed another login: %v", err)
			}
		})
	}
}

func TestParseLiveExpiryClaudeKeychainIsReadOnly(t *testing.T) {
	for _, mode := range []string{"keychain_only", "stale_mirror", "explicit_config", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			items := testutil.FakeKeychain(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
			t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "")
			path := filepath.Join(home, ".claude", ".credentials.json")
			expiry := time.Now().Add(-time.Hour).Truncate(time.Second)
			live := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-live","expiresAt":%d}}`, expiry.UnixMilli())
			stale := `{"claudeAiOauth":{"accessToken":"synthetic-file","refreshToken":"synthetic-file-refresh","expiresAt":1893456000000}}`
			testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), live)
			if mode != "keychain_only" {
				writeLiveExpiryFile(t, path, stale)
			}
			switch mode {
			case "explicit_config":
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(path))
			case "denied":
				t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")
			case "malformed":
				testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), "synthetic-not-json")
			}
			info, err := ParseLiveExpiry(authfile.ClaudeAuthFiles())
			if mode == "denied" || mode == "malformed" {
				if err == nil || (mode == "denied" && !errors.Is(err, keychain.ErrDenied)) {
					t.Fatalf("unreadable keychain accepted stale mirror: %v", err)
				}
			} else if err != nil || info == nil {
				t.Fatalf("read Claude live expiry: %v", err)
			} else if mode == "explicit_config" {
				if !info.Renewable || info.Fingerprint != credentialFingerprint("synthetic-file-refresh") {
					t.Fatal("explicit config borrowed the default keychain credential")
				}
			} else if !info.ExpiresAt.Equal(expiry) || info.Renewable || info.Fingerprint != credentialFingerprint("synthetic-live") {
				t.Fatal("expiry did not come from authoritative keychain bytes")
			}
			after, readErr := os.ReadFile(path)
			if mode == "keychain_only" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("expiry reader created a mirror: %v", readErr)
				}
			} else if readErr != nil || !bytes.Equal(after, []byte(stale)) {
				t.Fatal("expiry reader modified the existing mirror")
			}
		})
	}
}
