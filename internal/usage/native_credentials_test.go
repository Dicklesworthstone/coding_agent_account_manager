package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeCredentialLocatorsStayInSelectedFile(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "auth.json")
	stale := filepath.Join(dir, "xdg-auth.json")
	if err := os.WriteFile(stale, []byte(`{"accessToken":"SYNTHETIC-STALE"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NativeCredentialLocator("cursor", canonical); err == nil {
		t.Fatal("missing canonical credential fell back to another file")
	}
	if err := os.WriteFile(canonical, []byte(`{"accessToken":"SYNTHETIC-CURRENT"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := NativeCredentialLocator("cursor", canonical); err != nil || got != "cursor-root:"+canonical {
		t.Fatalf("cursor locator=%q err=%v", got, err)
	}
	if got, err := NativeCredentialLocator("grok", canonical); err != nil || got != "grok-home:"+dir {
		t.Fatalf("grok locator=%q err=%v", got, err)
	}
	if _, err := NativeCredentialLocator("grok", stale); err == nil {
		t.Fatal("Grok locator silently substituted a sibling auth.json")
	}
	if _, err := NativeCredentialLocator("grok", dir); err == nil {
		t.Fatal("directory accepted as a credential file")
	}
}

func TestLoadNativeCredentialsUsesCanonicalVaultLayout(t *testing.T) {
	root := t.TempDir()
	for _, provider := range []string{"cursor", "grok"} {
		for _, name := range []string{"selected", "stale-only"} {
			dir := filepath.Join(root, provider, name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := "auth.json"
			if name == "stale-only" {
				file = "xdg-auth.json"
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte(`{"accessToken":"SYNTHETIC","key":"SYNTHETIC"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := LoadProfileCredentials(root, provider)
		if err != nil || len(got) != 1 || got["selected"] == "" {
			t.Errorf("%s credential lookup = %v, %v", provider, got, err)
		}
	}
}

func nativeLiveAuth(t *testing.T, provider, account, email, marker string) []byte {
	t.Helper()
	var value any
	if provider == "cursor" {
		claims := map[string]string{"marker": marker}
		if account != "" {
			claims["sub"] = account
		}
		if email != "" {
			claims["email"] = email
		}
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		value = map[string]string{"accessToken": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".SYNTHETIC"}
	} else {
		entry := map[string]string{"access_token": "SYNTHETIC-" + marker, "refresh_token": "SYNTHETIC-RENEWAL-" + marker}
		entry["expires_at"] = "2099-01-01T00:00:00Z"
		if account != "" {
			entry["user_id"] = account
		}
		if email != "" {
			entry["email"] = email
		}
		value = map[string]any{"https://auth.example.test::client": entry}
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeNativeLiveFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeLiveCredentialRequiresSameAccount(t *testing.T) {
	for _, provider := range []string{"grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range []struct {
				name, savedID, liveID, savedEmail, liveEmail string
				wantOK                                       bool
			}{
				{"same ID after rotation", "seat-A", "seat-A", "old@example.test", "new@example.test", true},
				{"different IDs override same email", "seat-A", "seat-B", "same@example.test", "same@example.test", false},
				{"IDs are case sensitive", "seat-A", "seat-a", "same@example.test", "same@example.test", false},
				{"email fallback", "", "", "User@example.test", "user@example.test", true},
				{"one ID absent", "seat-A", "", "same@example.test", "same@example.test", true},
				{"different emails", "", "", "a@example.test", "b@example.test", false},
				{"unknown saved identity", "", "seat-A", "", "a@example.test", false},
				{"unknown live identity", "seat-A", "", "a@example.test", "", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := t.TempDir()
					savedPath := filepath.Join(dir, "vault", "auth.json")
					livePath := filepath.Join(dir, "live", "auth.json")
					saved := nativeLiveAuth(t, provider, tc.savedID, tc.savedEmail, "OLD")
					live := nativeLiveAuth(t, provider, tc.liveID, tc.liveEmail, "NEW")
					writeNativeLiveFixture(t, savedPath, saved)
					writeNativeLiveFixture(t, livePath, live)
					locator, err := NativeLiveCredentialLocator(provider, livePath, savedPath)
					if (err == nil) != tc.wantOK {
						t.Fatalf("locator accepted=%t, want %t: %v", err == nil, tc.wantOK, err)
					}
					if err != nil && strings.Contains(err.Error(), "SYNTHETIC") {
						t.Fatal("credential contents escaped through error")
					}
					if err == nil {
						path, read, bound, err := readNativeLiveCredential(provider, locator)
						if err != nil || !bound || path != livePath || string(read) != string(live) {
							t.Fatalf("bound read: path=%s bound=%t err=%v", path, bound, err)
						}
					}
					for path, want := range map[string][]byte{savedPath: saved, livePath: live} {
						got, err := os.ReadFile(path)
						if err != nil || string(got) != string(want) {
							t.Fatal("source selection changed a credential")
						}
					}
				})
			}
		})
	}
}

func TestNativeLiveCredentialRejectsMalformedAndAmbiguousIdentity(t *testing.T) {
	for _, tc := range []struct{ name, provider, body string }{
		{"grok malformed", "grok", `{"user_id":`},
		{"grok null", "grok", `{"user_id":null,"email":"a@example.test"}`},
		{"grok non-string", "grok", `{"user_id":42,"email":"a@example.test"}`},
		{"grok conflicting entries", "grok", `{"one":{"user_id":"a","email":"same@example.test"},"two":{"user_id":"b","email":"same@example.test"}}`},
		{"grok unknown credential alongside known", "grok", `{"one":{"user_id":"a"},"two":{"access_token":"SYNTHETIC"}}`},
		{"grok root conflicts with entry", "grok", `{"user_id":"a","other":{"user_id":"b"}}`},
		{"cursor opaque token", "cursor", `{"accessToken":"SYNTHETIC"}`},
		{"cursor malformed JWT", "cursor", `{"accessToken":"e30.INVALID.SYNTHETIC"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			savedPath := filepath.Join(dir, "saved", "auth.json")
			livePath := filepath.Join(dir, "live", "auth.json")
			writeNativeLiveFixture(t, savedPath, nativeLiveAuth(t, tc.provider, "a", "a@example.test", "SAVED"))
			writeNativeLiveFixture(t, livePath, []byte(tc.body))
			// Config metadata must not supply a missing credential identity.
			writeNativeLiveFixture(t, filepath.Join(filepath.Dir(livePath), "cli-config.json"), []byte(`{"authInfo":{"email":"a@example.test"}}`))
			if _, err := NativeLiveCredentialLocator(tc.provider, livePath, savedPath); err == nil {
				t.Fatal("unverifiable live credential accepted")
			}
		})
	}
}

func TestCursorLiveFetchFollowsRotationAndRejectsAccountSwitch(t *testing.T) {
	dir := t.TempDir()
	savedPath := filepath.Join(dir, "saved", "auth.json")
	livePath := filepath.Join(dir, "live", "auth.json")
	saved := nativeLiveAuth(t, "cursor", "seat-A", "", "SAVED")
	writeNativeLiveFixture(t, savedPath, saved)
	writeNativeLiveFixture(t, livePath, nativeLiveAuth(t, "cursor", "seat-A", "", "INITIAL"))
	locator, err := NativeLiveCredentialLocator("cursor", livePath, savedPath)
	if err != nil {
		t.Fatal(err)
	}
	rotated := nativeLiveAuth(t, "cursor", "seat-A", "", "ROTATED")
	writeNativeLiveFixture(t, livePath, rotated)
	wantToken, err := cursorAccessToken(rotated)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount := nativeLiveAuth(t, "cursor", "seat-B", "", "OTHER")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+wantToken {
			t.Error("request used a stale or switched credential")
		}
		// All three read RPCs must use the same validated snapshot even if
		// the native CLI switches accounts during the first response.
		if count == 1 {
			if err := os.WriteFile(livePath, otherAccount, 0600); err != nil {
				t.Error(err)
			}
		}
		if r.URL.Path == cursorPeriodUsagePath {
			fmt.Fprint(w, `{"planUsage":{"includedSpend":10,"limit":100,"autoPercentUsed":10}}`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	fetcher := &CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}
	info, err := fetcher.Fetch(context.Background(), locator)
	if err != nil || !info.NumericQuotaKnown() || info.AccountID != "seat-A" || requests.Load() != 3 {
		t.Fatalf("live usage = %+v requests=%d err=%v", info, requests.Load(), err)
	}
	info, err = fetcher.Fetch(context.Background(), locator)
	if err != nil || info.NumericQuotaKnown() || info.QuotaStatus != QuotaUnavailable || !strings.Contains(info.Error, "does not match") || requests.Load() != 3 {
		t.Fatalf("switched live login was queried: info=%+v requests=%d err=%v", info, requests.Load(), err)
	}
	got, err := os.ReadFile(savedPath)
	if err != nil || string(got) != string(saved) {
		t.Fatal("live quota fetch overwrote the vault snapshot")
	}
	got, err = os.ReadFile(livePath)
	if err != nil || string(got) != string(otherAccount) {
		t.Fatal("live quota fetch overwrote the native login")
	}
}

func TestGrokLiveFetchUsesValidatedSnapshotWithoutWritingSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	dir := t.TempDir()
	savedPath := filepath.Join(dir, "saved", "auth.json")
	livePath := filepath.Join(dir, "live", "auth.json")
	saved := nativeLiveAuth(t, "grok", "seat-A", "", "SAVED")
	writeNativeLiveFixture(t, savedPath, saved)
	writeNativeLiveFixture(t, livePath, nativeLiveAuth(t, "grok", "seat-A", "", "INITIAL"))
	locator, err := NativeLiveCredentialLocator("grok", livePath, savedPath)
	if err != nil {
		t.Fatal(err)
	}
	rotated := nativeLiveAuth(t, "grok", "seat-A", "", "ROTATED")
	writeNativeLiveFixture(t, livePath, rotated)
	captured := filepath.Join(dir, "captured-auth")
	staged, err := grokAccessOnlyAuth(rotated)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAAM_TEST_GROK_CAPTURE", captured)
	for _, key := range []string{"GROK_AUTH", "GROK_AUTH_PATH", "GROK_API_KEY", "XAI_API_KEY"} {
		t.Setenv(key, "SYNTHETIC-UNSELECTED")
	}
	bin := filepath.Join(dir, "grok")
	script := `#!/bin/sh
test -z "$GROK_AUTH$GROK_AUTH_PATH$GROK_API_KEY$XAI_API_KEY" || exit 1
cat "$GROK_HOME/auth.json" > "$CAAM_TEST_GROK_CAPTURE"
case "$(cat "$GROK_HOME/auth.json")" in
  *refresh_token*|*SYNTHETIC-RENEWAL*) exit 1 ;;
  *SYNTHETIC-ROTATED*) ;;
  *) exit 1 ;;
esac
printf '{}' > "$GROK_HOME/auth.json"
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"authMethods":[{"id":"cached_token"}]}}\n' "$id" ;;
    *'"_x.ai/billing"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"config":{"creditUsagePercent":12}}}\n' "$id"; exit 0 ;;
    *) printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"synthetic"}}\n' "$id" ;;
  esac
done
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	fetcher := &GrokFetcher{Bin: bin}
	info, err := fetcher.Fetch(context.Background(), locator)
	if err != nil || !info.NumericQuotaKnown() || info.AccountID != "seat-A" {
		t.Fatalf("live usage=%+v err=%v", info, err)
	}
	for path, want := range map[string][]byte{savedPath: saved, livePath: rotated, captured: staged} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("validated credential snapshot was not isolated: %s, %v", path, err)
		}
	}
	writeNativeLiveFixture(t, livePath, nativeLiveAuth(t, "grok", "seat-B", "", "SWITCHED"))
	info, err = fetcher.Fetch(context.Background(), locator)
	if err != nil || info.NumericQuotaKnown() || !strings.Contains(info.Error, "does not match") {
		t.Fatalf("account switch was accepted: %+v %v", info, err)
	}
	got, err := os.ReadFile(captured)
	if err != nil || string(got) != string(staged) {
		t.Fatal("Grok process ran after live account identity changed")
	}
}

func TestGrokStagingRemovesRenewalSecretsFromEveryEntry(t *testing.T) {
	for _, auth := range []string{
		`{"access_token":"SYNTHETIC-ACCESS","user_id":"seat-A","refresh_token":"SYNTHETIC-RENEWAL","expires_at":999999999999999999}`,
		`{"issuer::client":{"key":"SYNTHETIC-ACCESS","user_id":"seat-A","refreshToken":"SYNTHETIC-RENEWAL","client_secret":"SYNTHETIC-RENEWAL"}}`,
		`{"one":{"key":"SYNTHETIC-ACCESS","refresh_token":"SYNTHETIC-RENEWAL"},"two":{"accessToken":"SYNTHETIC-ACCESS","renewal_token":"SYNTHETIC-RENEWAL"}}`,
		`{"access_token":"SYNTHETIC-ACCESS","nested":[{"refresh":{"token":"SYNTHETIC-RENEWAL"}}]}`,
	} {
		stage, err := stageGrokHome([]byte(auth))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(stage) })
		got, err := os.ReadFile(filepath.Join(stage, "auth.json"))
		if err != nil || strings.Contains(string(got), "SYNTHETIC-RENEWAL") || !strings.Contains(string(got), "SYNTHETIC-ACCESS") {
			t.Fatalf("access-only staging failed: %v", err)
		}
		if strings.Contains(auth, "999999999999999999") && !strings.Contains(string(got), "999999999999999999") {
			t.Fatal("staging changed numeric credential metadata")
		}
	}
	for _, auth := range []string{"null", "[]", `{"key":"SYNTHETIC"} {"refresh_token":"SYNTHETIC-RENEWAL"}`} {
		if _, err := grokAccessOnlyAuth([]byte(auth)); err == nil {
			t.Fatalf("malformed credential accepted: %s", auth)
		}
	}
}

// A quota read must use the method selected in auth.json, not a leftover
// subscription token that happens to survive an API-key login.
func TestCodexUsageReaderHonorsSelectedAuthMethod(t *testing.T) {
	for _, tc := range []struct {
		name, auth, token, account string
		wantError                  string
	}{
		{"explicit key with nested OAuth", `{"auth_mode":"apikey","OPENAI_API_KEY":"SYNTHETIC-KEY","tokens":{"access_token":"SYNTHETIC-OLD","account_id":"old-seat"}}`, "", "", "API key auth"},
		{"hyphenated key with root OAuth", `{"auth_mode":"api-key","OPENAI_API_KEY":"SYNTHETIC-KEY","access_token":"SYNTHETIC-OLD","account_id":"old-seat"}`, "", "", "API key auth"},
		{"snake key alias", `{"auth_mode":"apikey","api_key":"SYNTHETIC-KEY","tokens":{"access_token":"SYNTHETIC-OLD"}}`, "", "", "API key auth"},
		{"camel key alias", `{"auth_mode":"APIKEY","apiKey":"SYNTHETIC-KEY","access_token":"SYNTHETIC-OLD"}`, "", "", "API key auth"},
		{"key ignores malformed unused tokens", `{"auth_mode":"apikey","OPENAI_API_KEY":"SYNTHETIC-KEY","tokens":17}`, "", "", "API key auth"},
		{"key-only legacy", `{"OPENAI_API_KEY":"SYNTHETIC-KEY"}`, "", "", "API key auth"},
		{"key mode without key", `{"auth_mode":"apikey","tokens":{"access_token":"SYNTHETIC-OLD"}}`, "", "", "invalid Codex credential selection"},
		{"key aliases conflict", `{"auth_mode":"apikey","OPENAI_API_KEY":"SYNTHETIC-A","apiKey":"SYNTHETIC-B","tokens":{"access_token":"SYNTHETIC-OLD"}}`, "", "", "invalid Codex credential selection"},
		{"malformed mode", `{"auth_mode":42,"tokens":{"access_token":"SYNTHETIC-OLD"}}`, "", "", "invalid Codex credential selection"},
		{"malformed key", `{"OPENAI_API_KEY":42,"tokens":{"access_token":"SYNTHETIC-OLD"}}`, "", "", "invalid Codex credential selection"},
		{"explicit OAuth with unused key", `{"auth_mode":"chatgpt","OPENAI_API_KEY":"SYNTHETIC-UNUSED","tokens":{"access_token":"SYNTHETIC-SELECTED","account_id":"selected-seat"}}`, "SYNTHETIC-SELECTED", "selected-seat", ""},
		{"legacy nested OAuth", `{"tokens":{"access_token":"SYNTHETIC-SELECTED","account_id":"selected-seat"}}`, "SYNTHETIC-SELECTED", "selected-seat", ""},
		{"legacy root OAuth", `{"access_token":"SYNTHETIC-SELECTED","account_id":"selected-seat"}`, "SYNTHETIC-SELECTED", "selected-seat", ""},
		{"implicit mixed preserves native OAuth precedence", `{"OPENAI_API_KEY":"SYNTHETIC-UNUSED","tokens":{"access_token":"SYNTHETIC-SELECTED","account_id":"selected-seat"}}`, "SYNTHETIC-SELECTED", "selected-seat", ""},
		{"nested OAuth precedes old root", `{"tokens":{"access_token":"SYNTHETIC-SELECTED","account_id":"selected-seat"},"access_token":"SYNTHETIC-OLD","account_id":"old-seat"}`, "SYNTHETIC-SELECTED", "selected-seat", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, []byte(tc.auth), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			token, account, err := ReadCodexCredentials(path)
			if token != tc.token || account != tc.account {
				t.Fatal("usage reader returned the wrong selected credential or account")
			}
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("selected OAuth was rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wrong selection refusal: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "SYNTHETIC") {
				t.Fatal("selection diagnostic exposed credential material")
			}
			data, readErr := os.ReadFile(path)
			after, statErr := os.Stat(path)
			if readErr != nil || statErr != nil || string(data) != tc.auth || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("quota selection changed the saved credential")
			}
		})
	}
}

func TestCodexUsagePoolExcludesSelectedKeysWithLeftoverOAuth(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"key":     `{"auth_mode":"apikey","OPENAI_API_KEY":"SYNTHETIC-KEY","tokens":{"access_token":"SYNTHETIC-OLD"}}`,
		"invalid": `{"auth_mode":"apikey","tokens":{"access_token":"SYNTHETIC-OLD"}}`,
		"oauth":   `{"auth_mode":"chatgpt","OPENAI_API_KEY":"SYNTHETIC-UNUSED","tokens":{"access_token":"SYNTHETIC-SELECTED"}}`,
	}
	for name, data := range fixtures {
		writeNativeLiveFixture(t, filepath.Join(root, "codex", name, "auth.json"), []byte(data))
	}
	credentials, err := LoadProfileCredentials(root, "codex")
	if err != nil || len(credentials) != 1 || credentials["oauth"] != "SYNTHETIC-SELECTED" {
		t.Fatalf("quota pool included an unselected account: count=%d, error=%v", len(credentials), err)
	}
	for name, want := range fixtures {
		data, err := os.ReadFile(filepath.Join(root, "codex", name, "auth.json"))
		if err != nil || string(data) != want {
			t.Fatal("quota pool loading changed saved credentials")
		}
	}
}
