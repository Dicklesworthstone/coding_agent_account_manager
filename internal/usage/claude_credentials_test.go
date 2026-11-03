package usage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestReadClaudeProfileCredentialsAuthority(t *testing.T) {
	const (
		modern = `{"claudeAiOauth":{"accessToken":"synthetic-selected-A","accountId":"account-A","refreshToken":"synthetic-refresh-A","expiresAt":1900000000000}}`
		auth   = `{"access_token":"synthetic-selected-A","refresh_token":"synthetic-refresh-A","expires_at":1900000000}`
		legacy = `{"oauthToken":"synthetic-ignored-B","refresh_token":"synthetic-refresh-B","expires_at":1800000000}`
	)
	for _, tc := range []struct {
		name       string
		files      map[string]string
		path       string
		token      string
		account    string
		expiry     time.Time
		renewable  bool
		wantErr    bool
		wantAbsent bool
	}{
		{
			name:  "modern owns token and expiry",
			files: map[string]string{".credentials.json": modern, "auth.json": auth, ".claude.json": legacy},
			path:  ".credentials.json", token: "synthetic-selected-A", account: "account-A",
			expiry: time.UnixMilli(1900000000000), renewable: true,
		},
		{
			name:  "modern does not borrow sibling expiry or renewal",
			files: map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-selected-A"}}`, "auth.json": auth, ".claude.json": legacy},
			path:  ".credentials.json", token: "synthetic-selected-A",
		},
		{
			name:  "auth precedes legacy",
			files: map[string]string{"auth.json": auth, ".claude.json": legacy},
			path:  "auth.json", token: "synthetic-selected-A", expiry: time.Unix(1900000000, 0), renewable: true,
		},
		{
			name:  "auth does not borrow sibling expiry",
			files: map[string]string{"auth.json": `{"access_token":"synthetic-selected-A"}`, ".claude.json": legacy},
			path:  "auth.json", token: "synthetic-selected-A",
		},
		{
			name:  "auth selects flat grant within the same file",
			files: map[string]string{"auth.json": `{"access_token":"synthetic-selected-A","expires_at":1900000000,"claudeAiOauth":{"accessToken":"synthetic-ignored-B","refreshToken":"synthetic-refresh-B","expiresAt":1800000000000}}`},
			path:  "auth.json", token: "synthetic-selected-A", expiry: time.Unix(1900000000, 0),
		},
		{
			name:  "saved relative lifetime without issuance stays unknown",
			files: map[string]string{"auth.json": `{"access_token":"synthetic-selected-A","expires_in":3600}`},
			path:  "auth.json", token: "synthetic-selected-A",
		},
		{
			name:  "saved relative lifetime with issuance has a fixed deadline",
			files: map[string]string{"auth.json": `{"access_token":"synthetic-selected-A","expires_in":3600,"issued_at":1900000000}`},
			path:  "auth.json", token: "synthetic-selected-A", expiry: time.Unix(1900003600, 0),
		},
		{
			name:  "policy settings permit legacy string",
			files: map[string]string{"settings.json": `{"permissions":{"allow":[]},"model":"sonnet"}`, ".claude.json": `{"oauthToken":"synthetic-legacy"}`},
			path:  ".claude.json", token: "synthetic-legacy",
		},
		{
			name:  "legacy string does not borrow unrelated top level expiry",
			files: map[string]string{".claude.json": legacy},
			path:  ".claude.json", token: "synthetic-ignored-B",
		},
		{
			name:  "legacy object owns its grant metadata",
			files: map[string]string{".claude.json": `{"oauthToken":{"access_token":"synthetic-legacy","expires_at":1900000000},"refresh_token":"synthetic-ignored-B","expires_at":1800000000}`},
			path:  ".claude.json", token: "synthetic-legacy", expiry: time.Unix(1900000000, 0),
		},
		{
			name:  "legacy camel case object remains renewable",
			files: map[string]string{".claude.json": `{"oauthToken":{"accessToken":"synthetic-legacy","refreshToken":"synthetic-refresh","expiresAt":1900000000000}}`},
			path:  ".claude.json", token: "synthetic-legacy", expiry: time.UnixMilli(1900000000000), renewable: true,
		},
		{
			name:    "state only legacy is not a credential",
			files:   map[string]string{".claude.json": `{"oauthAccount":{"emailAddress":"label@example.invalid"},"cachedStatsigGates":{},"numStartups":1}`},
			wantErr: true, wantAbsent: true,
		},
		{
			name:  "malformed modern does not revive legacy",
			files: map[string]string{".credentials.json": `{"accessToken":"synthetic-secret",`, ".claude.json": legacy},
			path:  ".credentials.json", wantErr: true,
		},
		{
			name:  "empty modern does not revive legacy",
			files: map[string]string{".credentials.json": "", ".claude.json": legacy},
			path:  ".credentials.json", wantErr: true,
		},
		{
			name:  "blank modern does not revive legacy",
			files: map[string]string{".credentials.json": `{"claudeAiOauth":{"accessToken":" "}}`, ".claude.json": legacy},
			path:  ".credentials.json", wantErr: true,
		},
		{
			name:  "null modern grant cannot select embedded legacy",
			files: map[string]string{".credentials.json": `{"claudeAiOauth":null,"oauthToken":"synthetic-ignored-B"}`, ".claude.json": legacy},
			path:  ".credentials.json", wantErr: true,
		},
		{
			name:  "malformed auth does not revive legacy",
			files: map[string]string{"auth.json": `{"access_token":"synthetic-secret",`, ".claude.json": legacy},
			path:  "auth.json", wantErr: true,
		},
		{
			name:  "tokenless auth still owns source",
			files: map[string]string{"auth.json": `{"email":"account-label@example.invalid"}`, ".claude.json": legacy},
			path:  "auth.json", wantErr: true,
		},
		{
			name:  "auth API key cannot borrow subscription OAuth",
			files: map[string]string{"auth.json": `{"apiKey":"synthetic-key","access_token":"synthetic-ignored-B"}`, ".claude.json": legacy},
			path:  "auth.json", wantErr: true,
		},
		{
			name:  "legacy API key cannot borrow subscription OAuth",
			files: map[string]string{".claude.json": `{"apiKey":"synthetic-key","oauthToken":"synthetic-ignored-B"}`},
			path:  ".claude.json", wantErr: true,
		},
		{
			name:  "selected helper cannot borrow legacy OAuth",
			files: map[string]string{"settings.json": `{"apiKeyHelper":"synthetic-helper-must-not-run"}`, ".claude.json": legacy},
			path:  "settings.json", wantErr: true,
		},
		{
			name:  "settings override also blocks modern primary",
			files: map[string]string{"settings.json": `{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"synthetic-ignored-B"}}`, ".credentials.json": modern},
			path:  "settings.json", wantErr: true,
		},
		{
			name:  "cloud routing cannot borrow legacy OAuth",
			files: map[string]string{"settings.json": `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`, ".claude.json": legacy},
			path:  "settings.json", wantErr: true,
		},
		{
			name:  "gateway routing cannot borrow modern OAuth",
			files: map[string]string{"settings.json": `{"forceLoginGatewayUrl":"https://gateway.example.invalid"}`, ".credentials.json": modern},
			path:  "settings.json", wantErr: true,
		},
		{
			name:  "explicitly disabled cloud routing permits OAuth",
			files: map[string]string{"settings.json": `{"env":{"CLAUDE_CODE_USE_BEDROCK":"false"},"forceLoginMethod":"claudeai"}`, ".claude.json": `{"oauthToken":"synthetic-legacy"}`},
			path:  ".claude.json", token: "synthetic-legacy",
		},
		{
			name:  "malformed settings cannot be treated as policy only",
			files: map[string]string{"settings.json": `{"apiKeyHelper":17}`, ".claude.json": legacy},
			path:  "settings.json", wantErr: true,
		},
		{
			name:  "legacy state routing blocks its own OAuth token",
			files: map[string]string{".claude.json": `{"oauthToken":"synthetic-legacy","env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`},
			path:  ".claude.json", wantErr: true,
		},
		{
			name:  "legacy state helper also blocks modern primary",
			files: map[string]string{".claude.json": `{"oauthToken":"synthetic-ignored-B","apiKeyHelper":"synthetic-helper"}`, ".credentials.json": modern},
			path:  ".claude.json", wantErr: true,
		},
		{
			name:  "legacy state gateway also blocks modern primary",
			files: map[string]string{".claude.json": `{"env":{"ANTHROPIC_BASE_URL":"https://gateway.example.invalid"}}`, ".credentials.json": modern},
			path:  ".claude.json", wantErr: true,
		},
		{name: "no authority", wantErr: true, wantAbsent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadClaudeProfileCredentials(dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("credential error = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantAbsent && !os.IsNotExist(err) {
				t.Fatalf("absent authority error = %v, want not-exist", err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("credential contents leaked into the error")
			}
			wantPath := tc.path
			if wantPath != "" {
				wantPath = filepath.Join(dir, wantPath)
			}
			if got.Path != wantPath || got.AccessToken != tc.token || got.AccountID != tc.account {
				t.Fatalf("wrong selected credential: path=%q, token matches=%v, account=%q", got.Path, got.AccessToken == tc.token, got.AccountID)
			}
			if !got.ExpiresAt.Equal(tc.expiry) || got.Renewable != tc.renewable {
				t.Fatalf("expiry=%v renewable=%v, want %v/%v", got.ExpiresAt, got.Renewable, tc.expiry, tc.renewable)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != len(tc.files) {
				t.Fatalf("inspection changed directory contents: %v", readErr)
			}
			for name, before := range tc.files {
				after, readErr := os.ReadFile(filepath.Join(dir, name))
				if readErr != nil || string(after) != before {
					t.Fatalf("inspection changed %s: %v", name, readErr)
				}
			}
		})
	}
}

func TestReadClaudeProfileCredentialsRejectsInvalidPrimaryFiles(t *testing.T) {
	for _, kind := range []string{"directory", "dangling link", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			primary := filepath.Join(dir, ".credentials.json")
			if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthToken":"synthetic-ignored-B"}`), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(primary, 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling link":
				if err := os.Symlink(filepath.Join(dir, "missing-target"), primary); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "oversized":
				file, err := os.OpenFile(primary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate(authfile.MaxDiscoveryFileBytes + 1)
				closeErr := file.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			got, err := ReadClaudeProfileCredentials(dir)
			if err == nil || got.Path != primary || got.AccessToken != "" {
				t.Fatalf("invalid selected primary revived another login: path=%q, has token=%v, error=%v", got.Path, got.AccessToken != "", err)
			}
		})
	}
}

func TestClaudeBatchSelectionNeverQueriesIgnoredCredentials(t *testing.T) {
	vaultDir := t.TempDir()
	for name, files := range map[string]map[string]string{
		"exhausted": {"auth.json": `{"access_token":"synthetic-selected-A"}`, ".claude.json": `{"oauthToken":"synthetic-ignored-B"}`},
		"broken":    {".credentials.json": `{`, ".claude.json": `{"oauthToken":"synthetic-ignored-B"}`},
		"helper":    {"settings.json": `{"apiKeyHelper":"synthetic-helper-must-not-run"}`, ".claude.json": `{"oauthToken":"synthetic-ignored-B"}`},
		"available": {".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-selected-C"}}`, ".claude.json": `{"oauthToken":"synthetic-ignored-B"}`},
	} {
		dir := filepath.Join(vaultDir, "claude", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for filename, body := range files {
			if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var mu sync.Mutex
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		requests[token]++
		mu.Unlock()
		percent := 0
		switch token {
		case "synthetic-selected-A":
			percent = 100
		case "synthetic-selected-C":
			percent = 10
		case "synthetic-ignored-B":
			// An incorrect reader would consider this ignored login the best.
		default:
			http.Error(w, "unexpected credential", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"seven_day":{"utilization":%d}}`, percent)
	}))
	defer server.Close()
	credentials, err := LoadProfileCredentials(vaultDir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	claude := NewClaudeFetcher()
	claude.baseURL = server.URL
	fetcher := &MultiProfileFetcher{claudeFetcher: claude}
	best := fetcher.GetBestProfile(context.Background(), "claude", credentials)
	if best == nil || best.ProfileName != "available" {
		t.Fatal("account selection used ignored credentials or lost the available account")
	}
	if len(credentials) != 2 || credentials["exhausted"] != "synthetic-selected-A" || credentials["available"] != "synthetic-selected-C" {
		t.Fatal("batch loading disagreed with selected credential authority")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests["synthetic-ignored-B"] != 0 || requests["synthetic-selected-A"] != 1 || requests["synthetic-selected-C"] != 1 {
		t.Fatal("quota requests did not use exactly the selected accounts")
	}
}
