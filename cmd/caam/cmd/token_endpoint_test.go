package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func resetEndpointFlags(t *testing.T) {
	t.Helper()
	resetTokenFlags(t)
	t.Cleanup(func() {
		_ = tokenAddCmd.Flags().Set("endpoint", "")
		_ = tokenAddCmd.Flags().Set("base-url", "")
	})
}

func TestTokenAdd_OllamaDefaultEndpoint(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	var out bytes.Buffer
	// No stdin token: ollama endpoint profiles are auth-less.
	tokenAddCmd.SetIn(strings.NewReader(""))
	tokenAddCmd.SetOut(&out)

	if err := runTokenAdd(tokenAddCmd, []string{"ollama", "local"}); err != nil {
		t.Fatalf("runTokenAdd error: %v", err)
	}

	token, meta, err := vault.ReadTokenProfile("ollama", "local")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
	if meta.Type != authfile.ProfileTypeEndpoint {
		t.Errorf("meta.Type = %q, want endpoint", meta.Type)
	}
	if meta.Endpoint != "http://127.0.0.1:11434" {
		t.Errorf("meta.Endpoint = %q, want default ollama endpoint", meta.Endpoint)
	}
	if !strings.Contains(out.String(), "endpoint profile") {
		t.Errorf("output %q does not mention endpoint profile", out.String())
	}
}

func TestTokenAdd_OllamaExplicitEndpoint(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	_ = tokenAddCmd.Flags().Set("endpoint", "http://gpu-box:11434")
	tokenAddCmd.SetIn(strings.NewReader(""))
	tokenAddCmd.SetOut(&bytes.Buffer{})

	if err := runTokenAdd(tokenAddCmd, []string{"ollama", "gpu"}); err != nil {
		t.Fatalf("runTokenAdd error: %v", err)
	}
	_, meta, err := vault.ReadTokenProfile("ollama", "gpu")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if meta.Endpoint != "http://gpu-box:11434" {
		t.Errorf("meta.Endpoint = %q", meta.Endpoint)
	}
}

func TestTokenAdd_QuickReadsBearerFromStdin(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	tokenAddCmd.SetIn(strings.NewReader("instance-token-xyz\n"))
	tokenAddCmd.SetOut(&bytes.Buffer{})

	if err := runTokenAdd(tokenAddCmd, []string{"quick", "desktop"}); err != nil {
		t.Fatalf("runTokenAdd error: %v", err)
	}
	token, meta, err := vault.ReadTokenProfile("quick", "desktop")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "instance-token-xyz" {
		t.Errorf("token = %q", token)
	}
	if meta.Endpoint != "ws://localhost:8771" {
		t.Errorf("meta.Endpoint = %q, want default quick endpoint", meta.Endpoint)
	}
}

func TestTokenAdd_ClaudeBaseURL(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	_ = tokenAddCmd.Flags().Set("base-url", "https://api.moonshot.ai/anthropic")
	// Moonshot-issued token: NOT sk-ant-*, must pass for endpoint profiles.
	tokenAddCmd.SetIn(strings.NewReader("sk-kimi-issued-key-0123456789\n"))
	tokenAddCmd.SetOut(&bytes.Buffer{})

	if err := runTokenAdd(tokenAddCmd, []string{"claude", "kimi"}); err != nil {
		t.Fatalf("runTokenAdd error: %v", err)
	}
	token, meta, err := vault.ReadTokenProfile("claude", "kimi")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "sk-kimi-issued-key-0123456789" {
		t.Errorf("token = %q", token)
	}
	if meta.Type != authfile.ProfileTypeEndpoint || meta.Endpoint != "https://api.moonshot.ai/anthropic" {
		t.Errorf("meta = %+v", meta)
	}

	// Without --base-url the claude default stays a plain token profile with
	// the strict sk-ant- format check.
	resetEndpointFlags(t)
	_ = tokenAddCmd.Flags().Set("endpoint", "")
	_ = tokenAddCmd.Flags().Set("base-url", "")
	tokenAddCmd.SetIn(strings.NewReader("sk-kimi-issued-key-0123456789\n"))
	if err := runTokenAdd(tokenAddCmd, []string{"claude", "plain"}); err == nil {
		t.Error("expected format-check error for non-sk-ant token without --base-url")
	}
}

func TestTokenAdd_EndpointUnsupportedProvider(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	_ = tokenAddCmd.Flags().Set("endpoint", "https://api.deepseek.com")
	tokenAddCmd.SetIn(strings.NewReader("sk-0123456789abcdef01234567\n"))
	tokenAddCmd.SetOut(&bytes.Buffer{})

	if err := runTokenAdd(tokenAddCmd, []string{"deepseek", "x"}); err == nil {
		t.Error("expected error: deepseek does not support endpoint profiles")
	}
}

func TestTokenAdd_DeepSeekKey(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	tokenAddCmd.SetIn(strings.NewReader("sk-0123456789abcdef01234567\n"))
	tokenAddCmd.SetOut(&bytes.Buffer{})
	if err := runTokenAdd(tokenAddCmd, []string{"deepseek", "main"}); err != nil {
		t.Fatalf("runTokenAdd error: %v", err)
	}
	if !vault.IsTokenProfile("deepseek", "main") {
		t.Error("deepseek profile not stored")
	}

	// Malformed keys are rejected by the passive format check.
	tokenAddCmd.SetIn(strings.NewReader("not-a-deepseek-key\n"))
	if err := runTokenAdd(tokenAddCmd, []string{"deepseek", "bad"}); err == nil {
		t.Error("expected format-check error for malformed deepseek key")
	}
}

func TestTokenAdd_InvalidEndpointURL(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	_ = tokenAddCmd.Flags().Set("endpoint", "not a url")
	tokenAddCmd.SetIn(strings.NewReader(""))
	tokenAddCmd.SetOut(&bytes.Buffer{})

	if err := runTokenAdd(tokenAddCmd, []string{"ollama", "bad"}); err == nil {
		t.Error("expected error for invalid endpoint URL")
	}
	if vault.IsTokenProfile("ollama", "bad") {
		t.Error("invalid endpoint profile was stored")
	}
}

func TestProbeProfile_OllamaReachable(t *testing.T) {
	setupTokenTest(t)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	meta := &authfile.TokenMeta{Type: authfile.ProfileTypeEndpoint, Endpoint: srv.URL}
	up, err := probeProfile(context.Background(), "ollama", "", meta)
	if err != nil {
		t.Fatalf("probeProfile error: %v", err)
	}
	if !up {
		t.Error("probeProfile = down, want reachable")
	}
	if gotPath != "/api/tags" {
		t.Errorf("probe hit %q, want /api/tags", gotPath)
	}
}

func TestProbeProfile_OllamaUnreachable(t *testing.T) {
	setupTokenTest(t)

	// Reserve a port, then close it so the probe hits a dead endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close()

	meta := &authfile.TokenMeta{Type: authfile.ProfileTypeEndpoint, Endpoint: dead}
	up, err := probeProfile(context.Background(), "ollama", "", meta)
	if err != nil {
		t.Fatalf("probeProfile error (want conclusive down): %v", err)
	}
	if up {
		t.Error("probeProfile = reachable, want down")
	}
}

func TestProbeProfile_QuickWSEndpoint(t *testing.T) {
	setupTokenTest(t)

	// The Quick agent answers plain HTTP GETs with a non-2xx (it wants a WS
	// upgrade); ANY response must count as reachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()

	wsURL := "ws://" + strings.TrimPrefix(srv.URL, "http://")
	meta := &authfile.TokenMeta{Type: authfile.ProfileTypeEndpoint, Endpoint: wsURL}
	up, err := probeProfile(context.Background(), "quick", "tok", meta)
	if err != nil {
		t.Fatalf("probeProfile error: %v", err)
	}
	if !up {
		t.Error("probeProfile = down, want reachable (426 is still a response)")
	}
}

func TestProbeProfile_ClaudeEndpointInconclusive(t *testing.T) {
	setupTokenTest(t)

	// Anthropic-compatible endpoints have no quota-free probe; the active
	// check must be inconclusive (error), never a hard verdict.
	meta := &authfile.TokenMeta{Type: authfile.ProfileTypeEndpoint, Endpoint: "https://api.z.ai/api/anthropic"}
	if _, err := probeProfile(context.Background(), "claude", "tok", meta); err == nil {
		t.Error("expected inconclusive (error) for claude endpoint probe")
	}
}

func TestValidateTokenProfile_EndpointActive(t *testing.T) {
	setupTokenTest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := vault.SaveEndpointProfile("ollama", "local", srv.URL, "", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}

	out := validateTokenProfile(context.Background(), "ollama", "local", true)
	if !out.Valid {
		t.Errorf("validateTokenProfile = invalid: %s", out.Error)
	}
	if out.Method != "active" {
		t.Errorf("Method = %q, want active", out.Method)
	}

	srv.Close()
	out = validateTokenProfile(context.Background(), "ollama", "local", true)
	if out.Valid {
		t.Error("validateTokenProfile = valid for dead endpoint, want invalid")
	}
	if !strings.Contains(out.Error, "not reachable") {
		t.Errorf("Error = %q, want endpoint-not-reachable", out.Error)
	}
}

func TestValidateTokenProfile_EndpointPassive(t *testing.T) {
	setupTokenTest(t)

	// GLM/Moonshot tokens are not sk-ant-*; passive validation of a claude
	// ENDPOINT profile must still pass.
	if err := vault.SaveEndpointProfile("claude", "glm", "https://api.z.ai/api/anthropic", "glm-issued.jwt", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}
	out := validateTokenProfile(context.Background(), "claude", "glm", false)
	if !out.Valid {
		t.Errorf("passive validation failed for claude endpoint profile: %s", out.Error)
	}
}

func TestTokenLs_ShowsEndpoint(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	if err := vault.SaveEndpointProfile("ollama", "local", "http://127.0.0.1:11434", "", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}
	if err := vault.SaveTokenProfile("deepseek", "main", "sk-0123456789abcdef01234567", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	var out bytes.Buffer
	tokenLsCmd.SetOut(&out)
	if err := runTokenLs(tokenLsCmd, nil); err != nil {
		t.Fatalf("runTokenLs error: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "http://127.0.0.1:11434") {
		t.Errorf("token ls output missing endpoint URL:\n%s", s)
	}
	if !strings.Contains(s, "endpoint") || !strings.Contains(s, "deepseek") {
		t.Errorf("token ls output missing type/provider:\n%s", s)
	}
}

func TestLs_EndpointProfileTypeSuffix(t *testing.T) {
	setupTokenTest(t)
	resetEndpointFlags(t)

	if err := vault.SaveEndpointProfile("ollama", "local", "http://127.0.0.1:11434", "", ""); err != nil {
		t.Fatalf("SaveEndpointProfile error: %v", err)
	}
	if err := vault.SaveTokenProfile("deepseek", "main", "sk-0123456789abcdef01234567", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	// Text mode: the suffix names the true profile type, mirroring status.
	t.Cleanup(func() { _ = lsCmd.Flags().Set("json", "false") })
	for _, tt := range []struct {
		tool string
		want string
	}{
		{"ollama", "local [endpoint]"},
		{"deepseek", "main [token]"},
	} {
		_ = lsCmd.Flags().Set("json", "false")
		oldStdout := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdout = w
		lsErr := runLs(lsCmd, []string{tt.tool})
		w.Close()
		os.Stdout = oldStdout
		if lsErr != nil {
			t.Fatalf("runLs(%s) error: %v", tt.tool, lsErr)
		}
		data, _ := io.ReadAll(r)
		if !strings.Contains(string(data), tt.want) {
			t.Errorf("ls %s output missing %q:\n%s", tt.tool, tt.want, data)
		}
	}

	// JSON mode: the type field carries the true profile type.
	_ = lsCmd.Flags().Set("json", "true")
	var out bytes.Buffer
	lsCmd.SetOut(&out)
	if err := runLs(lsCmd, []string{"ollama"}); err != nil {
		t.Fatalf("runLs --json error: %v", err)
	}
	var parsed lsOutput
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("parse ls JSON: %v\n%s", err, out.String())
	}
	if len(parsed.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(parsed.Profiles))
	}
	if parsed.Profiles[0].Type != authfile.ProfileTypeEndpoint {
		t.Errorf("type = %q, want %q", parsed.Profiles[0].Type, authfile.ProfileTypeEndpoint)
	}
}

func TestWsToHTTP(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ws://localhost:8771", "http://localhost:8771"},
		{"wss://host:1", "https://host:1"},
		{"http://host:2", "http://host:2"},
	}
	for _, tt := range tests {
		if got := wsToHTTP(tt.in); got != tt.want {
			t.Errorf("wsToHTTP(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
