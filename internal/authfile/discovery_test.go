package authfile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func discoveryFixture(t *testing.T, tool string, files map[string]string) (*Vault, AuthFileSet, string) {
	t.Helper()
	base := t.TempDir()
	live := filepath.Join(base, "live")
	if err := os.Mkdir(live, 0700); err != nil {
		t.Fatal(err)
	}
	set, ok := GetAuthFileSet(tool)
	if !ok {
		t.Fatalf("unknown fixture provider %s", tool)
	}
	for i := range set.Files {
		set.Files[i].Path = filepath.Join(live, filepath.Base(set.Files[i].Path))
	}
	for name, content := range files {
		writeFixtureFile(t, filepath.Join(live, name), content)
	}
	return NewVault(filepath.Join(base, "vault")), set, live
}

func discoveryJWT(t *testing.T, claims map[string]interface{}) string {
	t.Helper()
	data, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(data) + ".synthetic-signature"
}

func TestCaptureDiscoveryRecognizesCompleteNativeSources(t *testing.T) {
	for _, test := range []struct {
		name, tool, primary, credential, profile string
	}{
		{"Claude OAuth", "claude", ".credentials.json", `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","email":"claude@example.com","accountId":"claude-user","expiresAt":1893456000000}}`, "claude@example.com"},
		{"Claude access only", "claude", ".credentials.json", `{"claudeAiOauth":{"accessToken":"synthetic-access"}}`, "auto-"},
		{"Claude helper", "claude", "settings.json", `{"apiKeyHelper":"synthetic-helper","permissions":{"allow":["Read"]}}`, "auto-"},
		{"Codex API key", "codex", "auth.json", `{"OPENAI_API_KEY":"synthetic-api-key","tokens":null}`, "auto-"},
		{"Codex native OAuth", "codex", "auth.json", `{"OPENAI_API_KEY":null,"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"workspace-a"},"last_refresh":"2026-10-06T12:00:00Z"}`, "workspace-a"},
		{"Gemini OAuth", "gemini", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","email":"gemini@example.com","expiry_date":1893456000000}`, "gemini@example.com"},
		{"Gemini API key", "gemini", ".env", "# test\nexport GEMINI_API_KEY='synthetic-key'\n", "auto-"},
		{"Grok native key", "grok", "auth.json", `{"key":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":"2030-01-01T00:00:00Z","user_id":"grok-user","email":"grok@example.com"}`, "grok@example.com"},
		{"Grok keyed OAuth", "grok", "auth.json", `{"https://auth.x.ai::test-client":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":"2030-01-01T00:00:00Z","user_id":"grok-user","email":"grok@example.com"}}`, "grok@example.com"},
		{"OpenCode OAuth", "opencode", "auth.json", `{"anthropic":{"type":"oauth","access":"synthetic-access","refresh":"synthetic-refresh","expires":1893456000000}}`, "auto-"},
		{"OpenCode API key", "opencode", "auth.json", `{"openai":{"type":"api","key":"synthetic-key"}}`, "auto-"},
		{"Cursor access", "cursor", "auth.json", `{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh"}`, "auto-"},
		{"Cursor API key", "cursor", "auth.json", `{"apiKey":"synthetic-api-key"}`, "auto-"},
		{"Cursor API key null access", "cursor", "auth.json", `{"apiKey":"synthetic-api-key","accessToken":null}`, "auto-"},
		{"Cursor API key empty access", "cursor", "auth.json", `{"apiKey":"synthetic-api-key","accessToken":""}`, "auto-"},
		{"Cursor API key blank access", "cursor", "auth.json", `{"apiKey":"synthetic-api-key","accessToken":"  "}`, "auto-"},
		{"Cursor access null API key", "cursor", "auth.json", `{"accessToken":"synthetic-access","apiKey":null}`, "auto-"},
		{"agy opaque", "agy", "antigravity-oauth-token", "synthetic-native-agy-token", "auto-"},
		{"agy JSON", "agy", "antigravity-oauth-token", agyFakeToken, "auto-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, set, live := discoveryFixture(t, test.tool, map[string]string{test.primary: test.credential})
			result, err := v.CaptureDiscovery(set)
			if err != nil || result == nil || !result.Created || result.Updated || result.Unchanged {
				t.Fatalf("capture = %+v, %v", result, err)
			}
			if test.profile == "auto-" {
				if !strings.HasPrefix(result.Profile, test.profile) || IsSystemProfile(result.Profile) {
					t.Fatalf("anonymous login needs a normal user profile: %s", result.Profile)
				}
			} else if result.Profile != test.profile {
				t.Fatalf("profile = %q, want %q", result.Profile, test.profile)
			}
			if got := readFixtureFile(t, v.BackupPath(set.Tool, result.Profile, test.primary)); got != test.credential {
				t.Fatal("capture did not preserve the credential bytes")
			}
			if got := readFixtureFile(t, filepath.Join(live, test.primary)); got != test.credential {
				t.Fatal("discovery changed native credentials")
			}
			if err := v.ValidateProfileCredentials(set, result.Profile); err != nil {
				t.Fatalf("published profile is not restorable: %v", err)
			}
			again, err := v.CaptureDiscovery(set)
			if err != nil || !again.Unchanged || again.Profile != result.Profile {
				t.Fatalf("repeat capture = %+v, %v", again, err)
			}
			var meta map[string]interface{}
			if err := json.Unmarshal(readBytes(t, v.BackupPath(set.Tool, result.Profile, "meta.json")), &meta); err != nil || meta["type"] != "user" {
				t.Fatalf("discovery metadata must describe a user profile: %v, %v", meta, err)
			}
		})
	}
}

func TestCaptureDiscoveryRejectsIncompleteAndMalformedSources(t *testing.T) {
	for _, test := range []struct {
		name, tool string
		files      map[string]string
		want       error
	}{
		{"absent", "codex", nil, ErrNoCredentials},
		{"Claude settings only", "claude", map[string]string{"settings.json": `{"model":"sonnet","permissions":{"allow":["Read"]}}`, ".claude.json": `{"oauthAccount":{"emailAddress":"label@example.com"}}`}, ErrNoCredentials},
		{"Gemini settings only", "gemini", map[string]string{"settings.json": `{"email":"label@example.com","project_id":"test-project"}`}, ErrNoCredentials},
		{"Cursor label only", "cursor", map[string]string{"cli-config.json": `{"authInfo":{"email":"label@example.com"}}`}, ErrNoCredentials},
		{"agy Google cache only", "agy", map[string]string{"oauth_creds.json": agyFakeCreds, "google_accounts.json": agyFakeAccounts}, ErrNoCredentials},
		{"empty", "codex", map[string]string{"auth.json": ""}, ErrInvalidCredentials},
		{"null", "codex", map[string]string{"auth.json": `null`}, ErrInvalidCredentials},
		{"array", "codex", map[string]string{"auth.json": `[]`}, ErrInvalidCredentials},
		{"truncated", "codex", map[string]string{"auth.json": `{"tokens":`}, ErrInvalidCredentials},
		{"empty object", "codex", map[string]string{"auth.json": `{}`}, ErrInvalidCredentials},
		{"null tokens", "codex", map[string]string{"auth.json": `{"tokens":null}`}, ErrInvalidCredentials},
		{"wrong tokens type", "codex", map[string]string{"auth.json": `{"tokens":[]}`}, ErrInvalidCredentials},
		{"refresh without access", "codex", map[string]string{"auth.json": `{"tokens":{"refresh_token":"synthetic-refresh"}}`}, ErrInvalidCredentials},
		{"ID token without access", "codex", map[string]string{"auth.json": `{"id_token":"synthetic-id-token"}`}, ErrInvalidCredentials},
		{"wrong access type", "codex", map[string]string{"auth.json": `{"tokens":{"access_token":5}}`}, ErrInvalidCredentials},
		{"null refresh", "codex", map[string]string{"auth.json": `{"tokens":{"access_token":"synthetic-access","refresh_token":null}}`}, ErrInvalidCredentials},
		{"empty refresh", "claude", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":""}}`}, ErrInvalidCredentials},
		{"Claude legacy null refresh", "claude", map[string]string{"auth.json": `{"access_token":"synthetic-access","refresh_token":null}`}, ErrInvalidCredentials},
		{"Claude legacy malformed expiry", "claude", map[string]string{"auth.json": `{"access_token":"synthetic-access","expires_at":"broken"}`}, ErrInvalidCredentials},
		{"null OAuth", "claude", map[string]string{".credentials.json": `{"claudeAiOauth":null}`}, ErrInvalidCredentials},
		{"null expiry", "claude", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","expiresAt":null}}`}, ErrInvalidCredentials},
		{"fractional expiry", "claude", map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","expiresAt":123.5}}`}, ErrInvalidCredentials},
		{"Gemini identity without access", "gemini", map[string]string{"oauth_creds.json": `{"email":"label@example.com"}`}, ErrInvalidCredentials},
		{"Gemini blank API key", "gemini", map[string]string{".env": "GEMINI_API_KEY=\n"}, ErrInvalidCredentials},
		{"Gemini unfinished API key", "gemini", map[string]string{".env": "GEMINI_API_KEY='synthetic"}, ErrInvalidCredentials},
		{"Grok null entry", "grok", map[string]string{"auth.json": `{"issuer::client":null}`}, ErrInvalidCredentials},
		{"Grok incomplete key", "grok", map[string]string{"auth.json": `{"key":"","refresh_token":"synthetic-refresh"}`}, ErrInvalidCredentials},
		{"Grok malformed expiry", "grok", map[string]string{"auth.json": `{"key":"synthetic-key","expires_at":"invalid"}`}, ErrInvalidCredentials},
		{"Grok conflicting access aliases", "grok", map[string]string{"auth.json": `{"key":"synthetic-key-one","access_token":"synthetic-key-two"}`}, ErrInvalidCredentials},
		{"Cursor conflicting refresh aliases", "cursor", map[string]string{"auth.json": `{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh-one","refresh_token":"synthetic-refresh-two"}`}, ErrInvalidCredentials},
		{"OpenCode incomplete entry", "opencode", map[string]string{"auth.json": `{"anthropic":{"type":"oauth","refresh":"synthetic-refresh"}}`}, ErrInvalidCredentials},
		{"Cursor empty auth", "cursor", map[string]string{"auth.json": `{}`, "cli-config.json": `{"authInfo":{"email":"label@example.com"}}`}, ErrInvalidCredentials},
		{"Cursor null slots", "cursor", map[string]string{"auth.json": `{"accessToken":null,"apiKey":null}`, "cli-config.json": `{"authInfo":{"email":"label@example.com"}}`}, ErrInvalidCredentials},
		{"Cursor refresh alias only", "cursor", map[string]string{"auth.json": `{"refreshToken":"synthetic-session-alias"}`, "cli-config.json": `{"authInfo":{"email":"label@example.com"}}`}, ErrInvalidCredentials},
		{"Cursor malformed access with API key", "cursor", map[string]string{"auth.json": `{"accessToken":{},"apiKey":"synthetic-api-key"}`, "cli-config.json": `{"authInfo":{"email":"label@example.com"}}`}, ErrInvalidCredentials},
		{"Cursor malformed API key with access", "cursor", map[string]string{"auth.json": `{"accessToken":"synthetic-access","apiKey":false}`, "cli-config.json": `{"authInfo":{"email":"label@example.com"}}`}, ErrInvalidCredentials},
		{"agy empty token", "agy", map[string]string{"antigravity-oauth-token": "  "}, ErrInvalidCredentials},
		{"agy incomplete JSON", "agy", map[string]string{"antigravity-oauth-token": `{"token":`}, ErrInvalidCredentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			v, set, _ := discoveryFixture(t, test.tool, test.files)
			writeSwitchProfile(t, v, set.Tool, "existing", map[string]string{"auth.json": `{"access_token":"do-not-replace"}`, "meta.json": `{"description":"keep this"}`})
			result, err := v.CaptureDiscovery(set)
			if !errors.Is(err, test.want) || result != nil {
				t.Fatalf("capture = %+v, %v, want %v", result, err, test.want)
			}
			profiles, err := v.List(set.Tool)
			if err != nil || len(profiles) != 1 || profiles[0] != "existing" {
				t.Fatalf("invalid capture published a profile: %v, %v", profiles, err)
			}
			if got := readFixtureFile(t, v.BackupPath(set.Tool, "existing", "auth.json")); got != `{"access_token":"do-not-replace"}` {
				t.Fatal("invalid capture replaced a saved credential")
			}
		})
	}
}

func TestCaptureDiscoveryRejectsMalformedOwnershipAndFreshness(t *testing.T) {
	for _, malformed := range []string{`null`, `123`, `{}`, `[]`} {
		for _, tool := range []string{"grok", "codex", "gemini"} {
			t.Run(tool+"/"+malformed, func(t *testing.T) {
				primary := "auth.json"
				var saved, changed string
				switch tool {
				case "grok":
					saved = `{"key":"synthetic-old","refresh_token":"synthetic-refresh-old","expires_at":"2030-01-01T00:00:00Z","user_id":"account-a","email":"shared@example.com"}`
					changed = fmt.Sprintf(`{"key":"synthetic-new","refresh_token":"synthetic-refresh-new","expires_at":"2031-01-01T00:00:00Z","user_id":%s,"email":"shared@example.com"}`, malformed)
				case "codex":
					id := discoveryJWT(t, map[string]interface{}{"email": "shared@example.com", "sub": "user", "iat": 1893459600})
					saved = fmt.Sprintf(`{"tokens":{"access_token":"synthetic-old","refresh_token":"synthetic-refresh-old","id_token":%q,"account_id":"account-a"},"last_refresh":"2030-01-01T00:00:00Z"}`, id)
					changed = fmt.Sprintf(`{"tokens":{"access_token":"synthetic-new","refresh_token":"synthetic-refresh-new","id_token":%q,"account_id":%s},"last_refresh":"2030-01-01T01:00:00Z"}`, id, malformed)
				case "gemini":
					primary = "oauth_creds.json"
					id := discoveryJWT(t, map[string]interface{}{"email": "shared@example.com", "sub": "user"})
					saved = fmt.Sprintf(`{"access_token":"synthetic-old","refresh_token":"synthetic-refresh-old","id_token":%q,"email":"shared@example.com","expiry_date":1000}`, id)
					changed = fmt.Sprintf(`{"access_token":"synthetic-new","refresh_token":"synthetic-refresh-new","id_token":%q,"email":%s,"expiry_date":2000}`, id, malformed)
				}
				v, set, _ := discoveryFixture(t, tool, map[string]string{primary: changed})
				writeSwitchProfile(t, v, tool, "shared@example.com", map[string]string{primary: saved})
				result, err := v.CaptureDiscovery(set)
				if !errors.Is(err, ErrInvalidCredentials) || result != nil {
					t.Fatalf("malformed ownership = %+v, %v", result, err)
				}
				if got := readFixtureFile(t, v.BackupPath(tool, "shared@example.com", primary)); got != saved {
					t.Fatal("malformed account metadata authorized an overwrite")
				}
			})
		}
	}
	for _, value := range []string{`null`, `123`, `"broken"`, `""`} {
		t.Run("Codex last_refresh/"+value, func(t *testing.T) {
			id := discoveryJWT(t, map[string]interface{}{"email": "shared@example.com", "sub": "user", "iat": 1893459600})
			credential := fmt.Sprintf(`{"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":%q},"last_refresh":%s}`, id, value)
			v, set, _ := discoveryFixture(t, "codex", map[string]string{"auth.json": credential})
			if _, err := v.CaptureDiscovery(set); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("malformed last_refresh = %v", err)
			}
		})
	}
	for _, claim := range []string{"sub", "email", "account_id", "iat", "exp"} {
		t.Run("JWT/"+claim, func(t *testing.T) {
			claims := map[string]interface{}{"email": "shared@example.com", "sub": "user", "iat": 1893459600}
			claims[claim] = nil
			credential := fmt.Sprintf(`{"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":%q}}`, discoveryJWT(t, claims))
			v, set, _ := discoveryFixture(t, "codex", map[string]string{"auth.json": credential})
			if _, err := v.CaptureDiscovery(set); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("malformed JWT claim = %v", err)
			}
		})
	}
}

func TestCaptureDiscoveryTracksNativeNestedGrokRotation(t *testing.T) {
	rotated := strings.NewReplacer("SYNTHETIC-GROK-TOKEN-NOT-REAL", "SYNTHETIC-ROTATED-ACCESS", "SYNTHETIC-REFRESH-NOT-REAL", "SYNTHETIC-ROTATED-REFRESH", "2099-01-01", "2099-02-01").Replace(grokFakeAuth)
	v, set, live := discoveryFixture(t, "grok", map[string]string{"auth.json": rotated, "config.toml": "native policy"})
	writeSwitchProfile(t, v, "grok", "renamed-grok", map[string]string{"auth.json": grokFakeAuth, "config.toml": grokFakeConfig})
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.Updated || result.Profile != "renamed-grok" {
		t.Fatalf("native Grok rotation = %+v, %v", result, err)
	}
	if got := readFixtureFile(t, v.BackupPath("grok", "renamed-grok", "auth.json")); got != rotated {
		t.Fatal("native key grant rotation was not saved")
	}
	if got := readFixtureFile(t, v.BackupPath("grok", "renamed-grok", "config.toml")); got != grokFakeConfig {
		t.Fatal("native token rotation changed saved Grok settings")
	}
	writeFixtureFile(t, filepath.Join(live, "auth.json"), grokFakeAuth)
	stale, err := v.CaptureDiscovery(set)
	if err != nil || !stale.KeptNewer || stale.Profile != "renamed-grok" {
		t.Fatalf("native Grok stale grant = %+v, %v", stale, err)
	}
}

func TestCaptureDiscoveryUpdatesRenamedAccountWithoutChangingMetadata(t *testing.T) {
	old := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	before := string(codexAuthBytes(t, "alice@example.com", old, &old))
	after := string(codexAuthBytes(t, "alice@example.com", newer, &newer))
	v, set, _ := discoveryFixture(t, "codex", map[string]string{"auth.json": after})
	metadata := `{"description":"important account","tags":["work"],"unknown":{"preserve":true}}`
	writeSwitchProfile(t, v, "codex", "renamed-work", map[string]string{"auth.json": before, "meta.json": metadata})
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.Updated || result.Profile != "renamed-work" {
		t.Fatalf("rotation = %+v, %v", result, err)
	}
	if got := readFixtureFile(t, v.BackupPath("codex", result.Profile, "auth.json")); got != after {
		t.Fatal("named account lost its newer rotated credential")
	}
	if got := readFixtureFile(t, v.BackupPath("codex", result.Profile, "meta.json")); got != metadata {
		t.Fatal("rotation changed unrelated profile metadata")
	}
}

func TestCaptureDiscoveryNeverOverwritesAnotherCodexWorkspace(t *testing.T) {
	for _, test := range []struct{ name, oldWorkspace, newWorkspace string }{
		{"different workspace", "workspace-old", "workspace-new"},
		{"missing workspace", "workspace-old", ""},
		{"new scoped workspace", "", "workspace-new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			credential := func(workspace, access, lastRefresh string) string {
				idToken := discoveryJWT(t, map[string]interface{}{"email": "shared@example.com", "sub": "same-person"})
				return fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"same-synthetic-refresh","account_id":%q,"id_token":%q},"last_refresh":%q}`, access, workspace, idToken, lastRefresh)
			}
			before := credential(test.oldWorkspace, "old-access", "2026-10-06T12:00:00Z")
			after := credential(test.newWorkspace, "new-access", "2026-10-06T13:00:00Z")
			v, set, _ := discoveryFixture(t, "codex", map[string]string{"auth.json": after})
			writeSwitchProfile(t, v, "codex", "shared@example.com", map[string]string{"auth.json": before})
			result, err := v.CaptureDiscovery(set)
			if err != nil || !result.Created || !strings.HasPrefix(result.Profile, "auto-") {
				t.Fatalf("workspace collision = %+v, %v", result, err)
			}
			if got := readFixtureFile(t, v.BackupPath("codex", "shared@example.com", "auth.json")); got != before {
				t.Fatal("email collision overwrote another workspace")
			}
		})
	}
}

func TestCaptureDiscoveryKeepsNewerSavedGrant(t *testing.T) {
	old := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	before := string(codexAuthBytes(t, "alice@example.com", newer, &newer))
	stale := string(codexAuthBytes(t, "alice@example.com", old, &old))
	v, set, _ := discoveryFixture(t, "codex", map[string]string{"auth.json": stale})
	writeSwitchProfile(t, v, "codex", "alice", map[string]string{"auth.json": before})
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.KeptNewer || !result.Unchanged || result.Profile != "alice" {
		t.Fatalf("stale capture = %+v, %v", result, err)
	}
	if got := readFixtureFile(t, v.BackupPath("codex", "alice", "auth.json")); got != before {
		t.Fatal("stale native credential downgraded saved grant")
	}
}

func TestCaptureDiscoveryPreservesOpaqueRotationAndNewLogin(t *testing.T) {
	initial := claudeRotationCreds("synthetic-access-one", "synthetic-stable-refresh", 1000)
	rotated := claudeRotationCreds("synthetic-access-two", "synthetic-stable-refresh", 2000)
	unproven := claudeRotationCreds("synthetic-other-access", "synthetic-other-refresh", 3000)
	v, set, live := discoveryFixture(t, "claude", map[string]string{claudeCredentialsFile: initial})
	first, err := v.CaptureDiscovery(set)
	if err != nil || !first.Created || !strings.HasPrefix(first.Profile, "auto-") {
		t.Fatalf("anonymous capture = %+v, %v", first, err)
	}
	writeFixtureFile(t, filepath.Join(live, claudeCredentialsFile), rotated)
	second, err := v.CaptureDiscovery(set)
	if err != nil || !second.Updated || second.Profile != first.Profile {
		t.Fatalf("opaque refresh continuity = %+v, %v", second, err)
	}
	writeFixtureFile(t, filepath.Join(live, claudeCredentialsFile), unproven)
	third, err := v.CaptureDiscovery(set)
	if err != nil || !third.Created || third.Profile == first.Profile {
		t.Fatalf("unproven opaque login = %+v, %v", third, err)
	}
	if got := readFixtureFile(t, v.BackupPath("claude", first.Profile, claudeCredentialsFile)); got != rotated {
		t.Fatal("new anonymous login replaced the previous account")
	}
}

func TestCaptureDiscoverySecondaryCredentialIsNeverLostToStalePrimary(t *testing.T) {
	id := discoveryJWT(t, map[string]interface{}{"email": "gemini@example.com", "sub": "google-user"})
	makeAuth := func(access string, expiry int64) string {
		return fmt.Sprintf(`{"access_token":%q,"refresh_token":"stable-synthetic-refresh","id_token":%q,"expiry_date":%d}`, access, id, expiry)
	}
	old := makeAuth("old-access", 1000)
	newer := makeAuth("newer-access", 2000)
	v, set, _ := discoveryFixture(t, "gemini", map[string]string{"oauth_creds.json": old, ".env": "GEMINI_API_KEY=changed-synthetic-key"})
	writeSwitchProfile(t, v, "gemini", "gemini@example.com", map[string]string{"oauth_creds.json": newer, ".env": "GEMINI_API_KEY=old-synthetic-key"})
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.Created || result.KeptNewer {
		t.Fatalf("independent API key change = %+v, %v", result, err)
	}
	if got := readFixtureFile(t, v.BackupPath("gemini", result.Profile, ".env")); got != "GEMINI_API_KEY=changed-synthetic-key" {
		t.Fatal("older OAuth suppressed a newly discovered API key")
	}
	if got := readFixtureFile(t, v.BackupPath("gemini", "gemini@example.com", "oauth_creds.json")); got != newer {
		t.Fatal("secondary credential capture downgraded the named OAuth profile")
	}
}

func TestCaptureDiscoveryRejectsConflictingClaudePair(t *testing.T) {
	credential := `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","accountId":"new-account","email":"new@example.com","expiresAt":2000}}`
	settings := `{"oauthAccount":{"accountUuid":"old-account","emailAddress":"old@example.com"}}`
	v, set, live := discoveryFixture(t, "claude", map[string]string{claudeCredentialsFile: credential, claudeSettingsFile: settings})
	if _, err := v.CaptureDiscovery(set); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("conflicting pair = %v", err)
	}
	profiles, _ := v.List("claude")
	if len(profiles) != 0 {
		t.Fatal("discovery published an unusable conflicting snapshot")
	}
	writeFixtureFile(t, filepath.Join(live, claudeSettingsFile), `{"oauthAccount":{"accountUuid":"new-account","emailAddress":"new@example.com"}}`)
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.Created || result.Profile != "new@example.com" {
		t.Fatalf("completed pair = %+v, %v", result, err)
	}
}

func TestCaptureDiscoveryIgnoresPolicyChurnButChecksCredentialAndIdentity(t *testing.T) {
	credential := `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","accountId":"account-a","email":"a@example.com","expiresAt":2000}}`
	settings := `{"oauthAccount":{"accountUuid":"account-a","emailAddress":"a@example.com"},"numStartups":1}`
	v, set, live := discoveryFixture(t, "claude", map[string]string{claudeCredentialsFile: credential, claudeSettingsFile: settings, "settings.json": `{"model":"old"}`})
	first, err := v.CaptureDiscovery(set)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := readSwitchState(set, "")
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(live, claudeSettingsFile), strings.Replace(settings, `"numStartups":1`, `"numStartups":42`, 1))
	writeFixtureFile(t, filepath.Join(live, "settings.json"), `{"model":"new"}`)
	if err := checkDiscoveryUnchanged(set, captured); err != nil {
		t.Fatalf("unrelated policy churn blocked a stable login: %v", err)
	}
	again, err := v.CaptureDiscovery(set)
	if err != nil || !again.Unchanged || again.Profile != first.Profile {
		t.Fatalf("policy-only capture = %+v, %v", again, err)
	}
	if got := readFixtureFile(t, v.BackupPath("claude", first.Profile, "settings.json")); got != `{"model":"old"}` {
		t.Fatal("policy churn overwrote saved settings")
	}
	writeFixtureFile(t, filepath.Join(live, claudeCredentialsFile), strings.Replace(credential, "synthetic-access", "synthetic-different-access", 1))
	if err := checkDiscoveryUnchanged(set, captured); err == nil {
		t.Fatal("credential race was not detected")
	}
}

func TestCaptureDiscoveryReadsAuthoritativeKeychainWithoutMirroring(t *testing.T) {
	f := newKeychainFixture(t)
	credential := keychainCreds("synthetic-authoritative-access")
	f.storeToken(credential)
	result, err := f.vault.CaptureDiscovery(f.fileSet)
	if err != nil || !result.Created {
		t.Fatalf("keychain capture = %+v, %v", result, err)
	}
	if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
		t.Fatal("discovery created a live keychain mirror")
	}
	if got := readFixtureFile(t, f.vault.BackupPath("claude", result.Profile, claudeCredentialsFile)); got != credential {
		t.Fatal("capture did not save authoritative keychain bytes")
	}
}

func TestCaptureDiscoveryExplicitClaudeConfigRemainsIsolatedAndRestorable(t *testing.T) {
	for _, legacyLocation := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy-location=%t", legacyLocation), func(t *testing.T) {
			f := newKeychainFixture(t)
			hostToken := keychainCreds("synthetic-host-keychain")
			f.storeToken(hostToken)
			configDir := filepath.Join(t.TempDir(), "selected-config")
			if legacyLocation {
				configDir = filepath.Dir(f.credPath)
			}
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDE_CONFIG_DIR", configDir)
			set := ClaudeAuthFiles()
			credential := `{"claudeAiOauth":{"accessToken":"synthetic-selected-access","refreshToken":"synthetic-selected-refresh","accountId":"selected-account","email":"selected@example.com","expiresAt":2000}}`
			session := `{"oauthAccount":{"accountUuid":"selected-account","emailAddress":"selected@example.com"},"projects":{"/synthetic-repo":{"history":["selected-history"],"lastSessionId":"selected-session","allowedTools":["Read"]}}}`
			settings := `{"apiKeyHelper":"selected-helper","env":{"PRIVATE_GATEWAY":"selected-gateway"},"model":"old-shared-model"}`
			for name, data := range map[string]string{claudeCredentialsFile: credential, claudeSettingsFile: session, "settings.json": settings} {
				writeFixtureFile(t, filepath.Join(configDir, name), data)
			}
			result, err := f.vault.CaptureDiscovery(set)
			if err != nil || !result.Created || result.Profile != "selected@example.com" {
				t.Fatalf("selected native store = %+v, %v", result, err)
			}
			if got := readFixtureFile(t, f.vault.BackupPath("claude", result.Profile, claudeCredentialsFile)); got != credential {
				t.Fatal("explicit config discovery borrowed the default keychain")
			}
			if _, err := os.Stat(f.vault.BackupPath("claude", result.Profile, "config.json")); !os.IsNotExist(err) {
				t.Fatal("explicit config discovery captured the host Desktop cache")
			}
			writeFixtureFile(t, filepath.Join(configDir, claudeCredentialsFile), `{"claudeAiOauth":{"accessToken":"synthetic-other-access","refreshToken":"synthetic-other-refresh","accountId":"other-account","email":"other@example.com","expiresAt":3000}}`)
			writeFixtureFile(t, filepath.Join(configDir, claudeSettingsFile), `{"oauthAccount":{"accountUuid":"other-account","emailAddress":"other@example.com"},"projects":{"/synthetic-repo":{"history":["other-history"],"lastSessionId":"other-session","allowedTools":["Bash"]}}}`)
			writeFixtureFile(t, filepath.Join(configDir, "settings.json"), `{"apiKeyHelper":"other-helper","env":{"PRIVATE_GATEWAY":"other-gateway"},"model":"new-shared-model"}`)
			if err := f.vault.Restore(set, result.Profile); err != nil {
				t.Fatalf("restore discovered native store: %v", err)
			}
			if got := readFixtureFile(t, filepath.Join(configDir, claudeCredentialsFile)); got != credential {
				t.Fatal("discovered profile did not restore the selected credential")
			}
			var restoredSettings struct {
				Helper string            `json:"apiKeyHelper"`
				Model  string            `json:"model"`
				Env    map[string]string `json:"env"`
			}
			if err := json.Unmarshal(readBytes(t, filepath.Join(configDir, "settings.json")), &restoredSettings); err != nil {
				t.Fatal(err)
			}
			if restoredSettings.Helper != "selected-helper" || restoredSettings.Env["PRIVATE_GATEWAY"] != "selected-gateway" || restoredSettings.Model != "new-shared-model" {
				t.Fatalf("account helper/private env or shared policy was lost: %+v", restoredSettings)
			}
			var restoredSession struct {
				Projects map[string]struct {
					History      []string `json:"history"`
					LastSession  string   `json:"lastSessionId"`
					AllowedTools []string `json:"allowedTools"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(readBytes(t, filepath.Join(configDir, claudeSettingsFile)), &restoredSession); err != nil {
				t.Fatal(err)
			}
			project := restoredSession.Projects["/synthetic-repo"]
			if len(project.History) != 1 || project.History[0] != "selected-history" || project.LastSession != "selected-session" || len(project.AllowedTools) != 1 || project.AllowedTools[0] != "Bash" {
				t.Fatalf("restored project crossed account sessions or lost shared permissions: %+v", project)
			}
			if got, ok := f.storedToken(); !ok || got != hostToken {
				t.Fatal("explicit config capture/restore changed the default keychain")
			}
		})
	}
}

func TestCaptureDiscoveryTracksClaudePrivateSettingsUsingConfiguredPolicy(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "caam"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(configHome, "caam", "config.json"), `{"claude_settings":{"shared_env_keys":["ANTHROPIC_MODEL"]}}`)
	credential := `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","email":"claude@example.com","expiresAt":2000}}`
	settings := `{"apiKeyHelper":"same-helper","env":{"PRIVATE_GATEWAY":"gateway-one","ANTHROPIC_BASE_URL":"https://first.invalid","ANTHROPIC_MODEL":"model-one"}}`
	v, set, live := discoveryFixture(t, "claude", map[string]string{claudeCredentialsFile: credential, "settings.json": settings})
	first, err := v.CaptureDiscovery(set)
	if err != nil {
		t.Fatal(err)
	}
	sharedChange := strings.Replace(settings, "model-one", "model-two", 1)
	writeFixtureFile(t, filepath.Join(live, "settings.json"), sharedChange)
	if DiscoveryFileFingerprint("claude", "settings.json", []byte(settings)) != DiscoveryFileFingerprint("claude", "settings.json", []byte(sharedChange)) {
		t.Fatal("explicitly shared environment policy changed the credential fingerprint")
	}
	again, err := v.CaptureDiscovery(set)
	if err != nil || !again.Unchanged || again.Profile != first.Profile {
		t.Fatalf("shared environment policy churn = %+v, %v", again, err)
	}
	privateChange := strings.Replace(sharedChange, "gateway-one", "gateway-two", 1)
	writeFixtureFile(t, filepath.Join(live, "settings.json"), privateChange)
	if DiscoveryFileFingerprint("claude", "settings.json", []byte(sharedChange)) == DiscoveryFileFingerprint("claude", "settings.json", []byte(privateChange)) {
		t.Fatal("private helper environment change was not observed")
	}
	changed, err := v.CaptureDiscovery(set)
	if err != nil || !changed.Created || changed.Profile == first.Profile {
		t.Fatalf("private helper environment change = %+v, %v", changed, err)
	}
	if got := readFixtureFile(t, v.BackupPath("claude", first.Profile, "settings.json")); got != settings {
		t.Fatal("private settings change overwrote the previous captured account state")
	}
	if got := readFixtureFile(t, v.BackupPath("claude", changed.Profile, "settings.json")); got != privateChange {
		t.Fatal("new private helper configuration was not preserved")
	}
}

func TestCaptureDiscoveryRejectsUnrestorableClaudeProjectState(t *testing.T) {
	for _, state := range []string{`{"projects":[]}`, `{"projects":"invalid"}`, `{"projects":{"/synthetic-repo":42}}`} {
		t.Run(state, func(t *testing.T) {
			v, set, _ := discoveryFixture(t, "claude", map[string]string{claudeCredentialsFile: keychainCreds("synthetic-access"), claudeSettingsFile: state})
			if _, err := v.CaptureDiscovery(set); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("unrestorable project state = %v", err)
			}
			profiles, _ := v.List("claude")
			if len(profiles) != 0 {
				t.Fatal("capture published a profile that Restore would reject")
			}
		})
	}
}

func TestCaptureDiscoveryRestoresClaudeNullProjectRevocation(t *testing.T) {
	credential := keychainCreds("synthetic-selected-access")
	state := `{"projects":null}`
	v, set, live := discoveryFixture(t, "claude", map[string]string{claudeCredentialsFile: credential, claudeSettingsFile: state})
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.Created {
		t.Fatalf("capture null-project revocation = %+v, %v", result, err)
	}
	if got := readFixtureFile(t, v.BackupPath("claude", result.Profile, claudeSettingsFile)); got != state {
		t.Fatal("discovery did not preserve the captured project revocation")
	}
	writeFixtureFile(t, filepath.Join(live, claudeCredentialsFile), keychainCreds("synthetic-other-access"))
	if err := v.Restore(set, result.Profile); err != nil {
		t.Fatalf("restore null-project revocation: %v", err)
	}
	if got := readFixtureFile(t, filepath.Join(live, claudeCredentialsFile)); got != credential {
		t.Fatal("null-project state prevented credential restoration")
	}
	var restored map[string]json.RawMessage
	if err := json.Unmarshal(readBytes(t, filepath.Join(live, claudeSettingsFile)), &restored); err != nil {
		t.Fatal(err)
	}
	if raw, exists := restored["projects"]; exists && string(raw) != "null" && string(raw) != "{}" {
		t.Fatal("restoring captured revocation resurrected project policy")
	}
}

func TestCaptureDiscoveryPromotesSafetySnapshotToUserProfile(t *testing.T) {
	credential := `{"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"workspace"}}`
	v, set, _ := discoveryFixture(t, "codex", map[string]string{"auth.json": credential})
	writeSwitchProfile(t, v, "codex", "_backup_saved", map[string]string{"auth.json": credential})
	result, err := v.CaptureDiscovery(set)
	if err != nil || !result.Created || IsSystemProfile(result.Profile) {
		t.Fatalf("discovered account only exists as a non-routable safety snapshot: %+v, %v", result, err)
	}
}

func TestCaptureDiscoveryConcurrentCallsPublishOneCompleteProfile(t *testing.T) {
	credential := `{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh"}`
	v, set, _ := discoveryFixture(t, "cursor", map[string]string{"auth.json": credential})
	var wg sync.WaitGroup
	results := make(chan *DiscoveryResult, 8)
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := v.CaptureDiscovery(set)
			results <- result
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	created := 0
	for result := range results {
		if result.Created {
			created++
		}
	}
	profiles, _ := v.List("cursor")
	if created != 1 || len(profiles) != 1 {
		t.Fatalf("concurrent discovery = %d creations, %v profiles", created, profiles)
	}
	if !bytes.Equal(readBytes(t, v.BackupPath("cursor", profiles[0], "auth.json")), []byte(credential)) {
		t.Fatal("concurrent capture published partial credential data")
	}
}

func TestDiscoveryFileFingerprintIgnoresPolicyButTracksAuth(t *testing.T) {
	before := []byte(`{"apiKeyHelper":"synthetic-helper","model":"a"}`)
	after := []byte(`{"apiKeyHelper":"synthetic-helper","model":"b"}`)
	if DiscoveryFileFingerprint("claude", "settings.json", before) != DiscoveryFileFingerprint("claude", "settings.json", after) {
		t.Fatal("model setting changes affected the authentication fingerprint")
	}
	if DiscoveryFileFingerprint("claude", "settings.json", before) == DiscoveryFileFingerprint("claude", "settings.json", []byte(`{"apiKeyHelper":"different-helper"}`)) {
		t.Fatal("changed authentication helper did not affect the fingerprint")
	}
	if DiscoveryFileFingerprint("codex", "auth.json", []byte(`null`)) == DiscoveryFileFingerprint("codex", "auth.json", []byte(`[]`)) {
		t.Fatal("different malformed states cannot be observed independently")
	}
}

func TestCaptureDiscoveryBoundsNativeCredentialReads(t *testing.T) {
	v, set, live := discoveryFixture(t, "codex", map[string]string{"auth.json": `{"access_token":"synthetic-small"}`})
	path := filepath.Join(live, "auth.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxDiscoveryFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.CaptureDiscovery(set); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("oversized native credential = %v", err)
	}
	// Simulate the file growing after Lstat: the opened-file read must still
	// enforce the limit rather than trusting the earlier small size.
	if _, err := readSwitchSource(path, before, MaxDiscoveryFileBytes); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("credential grew after size check: %v", err)
	}
	profiles, _ := v.List("codex")
	if len(profiles) != 0 {
		t.Fatal("oversized credential published a profile")
	}
}
