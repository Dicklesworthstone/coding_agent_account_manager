package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastDelivery retries quickly so tests exercise several attempts.
var fastDelivery = deliveryPolicy{Timeout: 2 * time.Second, InitialDelay: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond}

// fakeOAuth completes OAuth without a browser.
type fakeOAuth struct {
	code, account string
	err           error
	calls         atomic.Int32
}

func (f *fakeOAuth) CompleteOAuth(ctx context.Context, oauthURL string, accounts []string) (string, string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", "", f.err
	}
	return f.code, f.account, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDeliverCompletionRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(data))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch attempts.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte(`{"status":"accepted","request_id":"r1"}`))
		}
	}))
	defer srv.Close()

	err := deliverCompletion(context.Background(), srv.Client(), srv.URL, "tok",
		completion{RequestID: "r1", Code: "CODE", Account: "a@example.com"}, fastDelivery, discardLogger())
	if err != nil {
		t.Fatalf("deliverCompletion: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	// Every attempt carries the identical payload the coordinator deduplicates on.
	for _, b := range bodies {
		if b != bodies[0] {
			t.Fatalf("payload changed between attempts: %q vs %q", b, bodies[0])
		}
	}
	if !strings.Contains(bodies[0], `"code":"CODE"`) || strings.Contains(bodies[0], `"error"`) {
		t.Fatalf("payload = %s", bodies[0])
	}
}

func TestDeliverCompletionStopsOnFinalRejection(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusGone} {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			w.Write([]byte(`{"status":"error","error":"nope"}`))
		}))

		err := deliverCompletion(context.Background(), srv.Client(), srv.URL, "",
			completion{RequestID: "r1", Code: "CODE"}, fastDelivery, discardLogger())
		srv.Close()

		var rejected *DeliveryRejectedError
		if !errors.As(err, &rejected) || rejected.StatusCode != status || rejected.Message != "nope" {
			t.Errorf("status %d: err = %v, want DeliveryRejectedError", status, err)
		}
		if attempts.Load() != 1 {
			t.Errorf("status %d: attempts = %d, want 1 (final statuses are not retried)", status, attempts.Load())
		}
	}
}

func TestDeliverCompletionGivesUpAfterTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	policy := fastDelivery
	policy.Timeout = 60 * time.Millisecond
	start := time.Now()
	err := deliverCompletion(context.Background(), srv.Client(), srv.URL, "",
		completion{RequestID: "r1", Code: "CODE"}, policy, discardLogger())
	if err == nil {
		t.Fatal("expected failure when the coordinator never acknowledges")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("delivery ran %v, past its timeout", elapsed)
	}
}

func TestAgentReportsSuccessOnlyAfterAcknowledgement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		w.Write([]byte(`{"status":"error","error":"auth request is closed"}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.CoordinatorURL = srv.URL
	cfg.Logger = discardLogger()
	a := New(cfg)
	a.delivery = fastDelivery
	a.oauth = &fakeOAuth{code: "CODE", account: "a@example.com"}

	var completed, failed atomic.Int32
	a.OnAuthComplete = func(account, code string) { completed.Add(1) }
	a.OnAuthFailed = func(account string, err error) {
		var rejected *DeliveryRejectedError
		if !errors.As(err, &rejected) || rejected.StatusCode != http.StatusGone {
			t.Errorf("failure error = %v", err)
		}
		failed.Add(1)
	}

	a.processAuthRequest(context.Background(), "r1", "https://claude.ai/oauth/authorize?x=1")
	if completed.Load() != 0 || failed.Load() != 1 {
		t.Fatalf("completed=%d failed=%d; an unacknowledged code is a failure", completed.Load(), failed.Load())
	}
	a.mu.RLock()
	result := a.accountUsage["a@example.com"].LastResult
	a.mu.RUnlock()
	if result != "undelivered" {
		t.Fatalf("usage result = %q, want undelivered", result)
	}
}

func TestAgentReportsOAuthFailureToCoordinator(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		got.Store(string(data))
		w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.CoordinatorURL = srv.URL
	cfg.Logger = discardLogger()
	a := New(cfg)
	a.delivery = fastDelivery
	a.oauth = &fakeOAuth{err: errors.New("consent page changed")}

	a.processAuthRequest(context.Background(), "r1", "https://claude.ai/oauth/authorize?x=1")
	body, _ := got.Load().(string)
	if !strings.Contains(body, `"error":"consent page changed"`) || strings.Contains(body, `"code"`) {
		t.Fatalf("reported body = %q", body)
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Port != 7891 {
		t.Errorf("expected port 7891, got %d", config.Port)
	}
	if config.CoordinatorURL != "http://localhost:7890" {
		t.Errorf("unexpected coordinator URL: %s", config.CoordinatorURL)
	}
	if config.PollInterval != 2*time.Second {
		t.Errorf("unexpected poll interval: %v", config.PollInterval)
	}
	if config.Headless != false {
		t.Error("headless should default to false")
	}
	if config.AccountStrategy != StrategyLRU {
		t.Errorf("expected LRU strategy, got %v", config.AccountStrategy)
	}
}

func TestAgentSelectLRU(t *testing.T) {
	agent := &Agent{
		config: Config{
			Accounts:        []string{"a@test.com", "b@test.com", "c@test.com"},
			AccountStrategy: StrategyLRU,
		},
		accountUsage: make(map[string]*AccountUsage),
	}

	// First select should return first account (never used)
	selected := agent.selectLRU(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", selected)
	}

	// Mark a@test.com as used
	agent.accountUsage["a@test.com"] = &AccountUsage{
		Email:    "a@test.com",
		LastUsed: time.Now(),
	}

	// Next select should return b@test.com (never used)
	selected = agent.selectLRU(agent.config.Accounts)
	if selected != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", selected)
	}

	// Mark all as used with different times
	now := time.Now()
	agent.accountUsage["a@test.com"] = &AccountUsage{
		Email:    "a@test.com",
		LastUsed: now.Add(-1 * time.Hour), // oldest
	}
	agent.accountUsage["b@test.com"] = &AccountUsage{
		Email:    "b@test.com",
		LastUsed: now.Add(-30 * time.Minute),
	}
	agent.accountUsage["c@test.com"] = &AccountUsage{
		Email:    "c@test.com",
		LastUsed: now.Add(-10 * time.Minute), // most recent
	}

	// LRU should return a@test.com (oldest)
	selected = agent.selectLRU(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com (oldest), got %s", selected)
	}
}

func TestAgentSelectRoundRobin(t *testing.T) {
	agent := &Agent{
		config: Config{
			Accounts:        []string{"a@test.com", "b@test.com", "c@test.com"},
			AccountStrategy: StrategyRoundRobin,
		},
		accountUsage: make(map[string]*AccountUsage),
	}

	// First select with no usage should return first account
	selected := agent.selectRoundRobin(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", selected)
	}

	// Mark a@test.com as most recent
	agent.accountUsage["a@test.com"] = &AccountUsage{
		Email:    "a@test.com",
		LastUsed: time.Now(),
	}

	// Round robin should return b@test.com (next after a)
	selected = agent.selectRoundRobin(agent.config.Accounts)
	if selected != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", selected)
	}

	// Mark b@test.com as most recent
	agent.accountUsage["b@test.com"] = &AccountUsage{
		Email:    "b@test.com",
		LastUsed: time.Now(),
	}

	// Round robin should return c@test.com (next after b)
	selected = agent.selectRoundRobin(agent.config.Accounts)
	if selected != "c@test.com" {
		t.Errorf("expected c@test.com, got %s", selected)
	}

	// Mark c@test.com as most recent
	agent.accountUsage["c@test.com"] = &AccountUsage{
		Email:    "c@test.com",
		LastUsed: time.Now(),
	}

	// Round robin should wrap back to a@test.com
	selected = agent.selectRoundRobin(agent.config.Accounts)
	if selected != "a@test.com" {
		t.Errorf("expected a@test.com (wrap around), got %s", selected)
	}
}

func TestAgentSelectAccountNoAccounts(t *testing.T) {
	agent := &Agent{
		config: Config{
			Accounts:        nil, // no accounts configured
			AccountStrategy: StrategyLRU,
		},
		accountUsage: make(map[string]*AccountUsage),
	}

	// With no accounts, should return empty string
	selected := agent.selectAccount()
	if selected != "" {
		t.Errorf("expected empty string with no accounts, got %s", selected)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		limit    int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.limit)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.limit, result, tt.expected)
		}
	}
}

func TestAccountUsage(t *testing.T) {
	usage := &AccountUsage{
		Email:      "test@example.com",
		LastUsed:   time.Now(),
		UseCount:   5,
		LastResult: "success",
	}

	if usage.Email != "test@example.com" {
		t.Errorf("unexpected email: %s", usage.Email)
	}
	if usage.UseCount != 5 {
		t.Errorf("unexpected use count: %d", usage.UseCount)
	}
	if usage.LastResult != "success" {
		t.Errorf("unexpected last result: %s", usage.LastResult)
	}
}

func TestRandomStrategySpreadsAcrossAccounts(t *testing.T) {
	accounts := []string{"a@example.com", "b@example.com", "c@example.com"}

	single := New(Config{AccountStrategy: StrategyRandom, Accounts: accounts, Logger: discardLogger()})
	multi := NewMulti(MultiConfig{AccountStrategy: StrategyRandom, Accounts: accounts, Logger: discardLogger()})

	for name, pick := range map[string]func() string{"single": single.selectAccount, "multi": multi.selectAccount} {
		seen := map[string]int{}
		for i := 0; i < 300; i++ {
			seen[pick()]++
		}
		if len(seen) != len(accounts) {
			t.Errorf("%s: random strategy picked %v, want all of %v", name, seen, accounts)
		}
		for acc := range seen {
			if acc != accounts[0] && acc != accounts[1] && acc != accounts[2] {
				t.Errorf("%s: picked unknown account %q", name, acc)
			}
		}
	}
}

func TestAccountOrder(t *testing.T) {
	if got := accountOrder("", []string{"a@x", "b@x"}); got != nil {
		t.Fatalf("no selection = %q, want any account (nil)", got)
	}
	got := accountOrder("b@x", []string{"a@x", "B@x", "c@x"})
	if strings.Join(got, ",") != "b@x,a@x,c@x" {
		t.Fatalf("accountOrder = %q, want the selection first, then the others once", got)
	}
}

func TestLimitResetTime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, ny)
	for _, tc := range []struct {
		reset string
		now   time.Time
		want  time.Time
	}{
		{"3pm (America/New_York)", now, time.Date(2026, 10, 7, 15, 0, 0, 0, ny)},
		// A time already past today is tomorrow's.
		{"9am (America/New_York)", now, time.Date(2026, 10, 8, 9, 0, 0, 0, ny)},
		{"12:30am (America/New_York)", now, time.Date(2026, 10, 8, 0, 30, 0, 0, ny)},
		{"Oct 9, 3:30pm (Europe/Paris)", now, time.Date(2026, 10, 9, 15, 30, 0, 0, paris)},
		{"Oct 9 (America/New_York)", now, time.Date(2026, 10, 9, 0, 0, 0, 0, ny)},
		// "Jan 2" read at the end of December is next year's.
		{"Jan 2, 3pm (America/New_York)", time.Date(2026, 12, 30, 12, 0, 0, 0, ny), time.Date(2027, 1, 2, 15, 0, 0, 0, ny)},
		{"in 2h 13m", now, now.Add(2*time.Hour + 13*time.Minute)},
		{"in 45m", now, now.Add(45 * time.Minute)},
		// Unreadable: the session window.
		{"", now, now.Add(defaultLimitHold)},
		{"soon", now, now.Add(defaultLimitHold)},
		// A reset already past holds nothing; one far off is capped.
		{"Oct 6, 3pm (America/New_York)", now, now},
		{"Dec 1, 3pm (America/New_York)", now, now.Add(maxLimitHold)},
	} {
		if got := limitResetTime(tc.reset, tc.now); !got.Equal(tc.want) {
			t.Errorf("limitResetTime(%q) = %v, want %v", tc.reset, got, tc.want)
		}
	}
}

func TestValidateAuthorizeURL(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st", true},
		{"https://claude.com/cai/oauth/authorize?code=true&state=st", true},
		{"https://CLAUDE.AI/oauth/authorize", true},
		{"http://claude.ai/oauth/authorize", false},
		{"https://evil.example/oauth/authorize", false},
		{"https://claude.ai.evil.example/oauth/authorize", false},
		{"https://sub.claude.ai/oauth/authorize", false},
		{"https://claude.ai:8443/oauth/authorize", false},
		{"https://user@claude.ai/oauth/authorize", false},
		{"https://claude.ai/login", false},
		{"https://claude.ai/oauth/authorize/../../logout", false},
		{"javascript:alert(1)", false},
	} {
		if err := validateAuthorizeURL(tc.url); (err == nil) != tc.ok {
			t.Errorf("validateAuthorizeURL(%q) = %v, want ok=%v", tc.url, err, tc.ok)
		}
	}
}

func TestAuthEndpointsRejectNonClaudeAuthorizeURL(t *testing.T) {
	for name, handle := range map[string]func(http.ResponseWriter, *http.Request){
		"single": (&Agent{}).handleAuth,
		"multi":  (&MultiAgent{}).handleAuth,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(`{"url":"https://evil.example/oauth/authorize"}`))
		handle(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: POST /auth with a foreign URL = %d, want 400", name, rec.Code)
		}
	}
}

func TestLoopbackOnlyRejectsForeignHostHeader(t *testing.T) {
	h := loopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for host, want := range map[string]int{
		"127.0.0.1:7891":          http.StatusNoContent,
		"localhost:7891":          http.StatusNoContent,
		"[::1]:7891":              http.StatusNoContent,
		"127.0.0.1":               http.StatusNoContent,
		"evil.example:7891":       http.StatusForbidden,
		"127.0.0.1.nip.io:7891":   http.StatusForbidden,
		"attacker.localhost.test": http.StatusForbidden,
		"":                        http.StatusForbidden,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/status", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q = %d, want %d", host, rec.Code, want)
		}
	}
}
