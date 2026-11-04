package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/logs"
)

func TestUsageInfo_AvailabilityScore(t *testing.T) {
	tests := []struct {
		name     string
		info     *UsageInfo
		expected int
	}{
		{
			name:     "nil returns 0",
			info:     nil,
			expected: 0,
		},
		{
			name:     "error returns 0",
			info:     &UsageInfo{Error: "some error"},
			expected: 0,
		},
		{
			name:     "empty info has no measured capacity",
			info:     &UsageInfo{},
			expected: 0,
		},
		{
			name: "primary 50% used",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{Utilization: 0.5},
			},
			expected: 75, // 100 - 50*0.5 = 75
		},
		{
			name: "primary 100% used",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{Utilization: 1.0},
			},
			expected: 50, // 100 - 50*1.0 = 50
		},
		{
			name: "all windows at 50%",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{Utilization: 0.5},
				SecondaryWindow: &UsageWindow{Utilization: 0.5},
				TertiaryWindow:  &UsageWindow{Utilization: 0.5},
			},
			expected: 55, // 100 - 25 - 12.5 - 7.5 = 55
		},
		{
			name: "all at 100% with no credits",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{Utilization: 1.0},
				SecondaryWindow: &UsageWindow{Utilization: 1.0},
				TertiaryWindow:  &UsageWindow{Utilization: 1.0},
				Credits:         &CreditInfo{HasCredits: false, Unlimited: false},
			},
			expected: 0, // 100 - 50 - 25 - 15 - 10 = 0
		},
		{
			name: "uses UsedPercent when Utilization is 0",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{UsedPercent: 50},
			},
			expected: 75,
		},
		{
			name: "unlimited credits don't penalize",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{},
				Credits:       &CreditInfo{HasCredits: false, Unlimited: true},
			},
			expected: 100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score := tc.info.AvailabilityScore()
			if score != tc.expected {
				t.Errorf("AvailabilityScore() = %d, expected %d", score, tc.expected)
			}
		})
	}
}

func TestUsageInfo_IsNearLimit(t *testing.T) {
	threshold := 0.8

	tests := []struct {
		name     string
		info     *UsageInfo
		expected bool
	}{
		{
			name:     "nil returns false",
			info:     nil,
			expected: false,
		},
		{
			name:     "empty info returns false",
			info:     &UsageInfo{},
			expected: false,
		},
		{
			name: "primary below threshold",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{Utilization: 0.7},
			},
			expected: false,
		},
		{
			name: "primary at threshold",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{Utilization: 0.8},
			},
			expected: true,
		},
		{
			name: "secondary above threshold",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{Utilization: 0.5},
				SecondaryWindow: &UsageWindow{Utilization: 0.9},
			},
			expected: true,
		},
		{
			name: "tertiary above threshold",
			info: &UsageInfo{
				PrimaryWindow:  &UsageWindow{Utilization: 0.5},
				TertiaryWindow: &UsageWindow{Utilization: 0.85},
			},
			expected: true,
		},
		{
			name: "model window above threshold",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{Utilization: 0.5},
				ModelWindows: map[string]*UsageWindow{
					"claude-3-opus": {Utilization: 0.95},
				},
			},
			expected: true,
		},
		{
			name: "all below threshold",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{Utilization: 0.5},
				SecondaryWindow: &UsageWindow{Utilization: 0.6},
				TertiaryWindow:  &UsageWindow{Utilization: 0.7},
				ModelWindows: map[string]*UsageWindow{
					"claude-3-opus": {Utilization: 0.5},
				},
			},
			expected: false,
		},
		{
			name: "uses UsedPercent when Utilization is 0",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{UsedPercent: 85},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.info.IsNearLimit(threshold)
			if result != tc.expected {
				t.Errorf("IsNearLimit(%v) = %v, expected %v", threshold, result, tc.expected)
			}
		})
	}
}

func TestUsageInfo_TimeUntilReset(t *testing.T) {
	now := time.Now()
	future1h := now.Add(time.Hour)
	future2h := now.Add(2 * time.Hour)
	future30m := now.Add(30 * time.Minute)
	past := now.Add(-time.Hour)

	tests := []struct {
		name        string
		info        *UsageInfo
		expectZero  bool
		expectRange [2]time.Duration // min, max expected
	}{
		{
			name:       "nil returns 0",
			info:       nil,
			expectZero: true,
		},
		{
			name:       "empty info returns 0",
			info:       &UsageInfo{},
			expectZero: true,
		},
		{
			name: "primary window only",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{ResetsAt: future1h},
			},
			expectRange: [2]time.Duration{55 * time.Minute, 65 * time.Minute},
		},
		{
			name: "picks earliest window",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{ResetsAt: future2h},
				SecondaryWindow: &UsageWindow{ResetsAt: future1h},
			},
			expectRange: [2]time.Duration{55 * time.Minute, 65 * time.Minute},
		},
		{
			name: "tertiary is earliest",
			info: &UsageInfo{
				PrimaryWindow:  &UsageWindow{ResetsAt: future2h},
				TertiaryWindow: &UsageWindow{ResetsAt: future30m},
			},
			expectRange: [2]time.Duration{25 * time.Minute, 35 * time.Minute},
		},
		{
			name: "model window is earliest",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{ResetsAt: future2h},
				ModelWindows: map[string]*UsageWindow{
					"opus": {ResetsAt: future30m},
				},
			},
			expectRange: [2]time.Duration{25 * time.Minute, 35 * time.Minute},
		},
		{
			name: "past reset returns 0",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{ResetsAt: past},
			},
			expectZero: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.info.TimeUntilReset()
			if tc.expectZero {
				if result != 0 {
					t.Errorf("TimeUntilReset() = %v, expected 0", result)
				}
			} else {
				if result < tc.expectRange[0] || result > tc.expectRange[1] {
					t.Errorf("TimeUntilReset() = %v, expected in range [%v, %v]",
						result, tc.expectRange[0], tc.expectRange[1])
				}
			}
		})
	}
}

func TestMultiProfileFetcher_NilFetcherDoesNotPanic(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var m *MultiProfileFetcher
		results := m.FetchAllProfiles(context.Background(), "claude", map[string]string{
			"work": "token",
		})
		if len(results) != 1 {
			t.Fatalf("results len = %d, want 1", len(results))
		}
		if results[0].Usage == nil {
			t.Fatal("results[0].Usage is nil")
		}
		if results[0].Usage.Error == "" {
			t.Fatal("results[0].Usage.Error is empty, want error")
		}
	})

	t.Run("claude nil fetcher", func(t *testing.T) {
		m := &MultiProfileFetcher{}
		results := m.FetchAllProfiles(context.Background(), "claude", map[string]string{
			"work": "token",
		})
		if len(results) != 1 {
			t.Fatalf("results len = %d, want 1", len(results))
		}
		if results[0].Usage == nil {
			t.Fatal("results[0].Usage is nil")
		}
		if results[0].Usage.Error == "" {
			t.Fatal("results[0].Usage.Error is empty, want error")
		}
	})

	t.Run("codex nil fetcher", func(t *testing.T) {
		m := &MultiProfileFetcher{}
		results := m.FetchAllProfiles(context.Background(), "codex", map[string]string{
			"work": "token",
		})
		if len(results) != 1 {
			t.Fatalf("results len = %d, want 1", len(results))
		}
		if results[0].Usage == nil {
			t.Fatal("results[0].Usage is nil")
		}
		if results[0].Usage.Error == "" {
			t.Fatal("results[0].Usage.Error is empty, want error")
		}
	})
}

type countingLogScanner struct {
	mu    sync.Mutex
	scans int
}

func (s *countingLogScanner) Scan(ctx context.Context, logDir string, since time.Time) (*logs.ScanResult, error) {
	s.mu.Lock()
	s.scans++
	s.mu.Unlock()
	now := time.Now()
	return &logs.ScanResult{Provider: "claude", Since: since, Until: now, Entries: []*logs.LogEntry{
		{Timestamp: now.Add(-3 * time.Hour), Model: "m", InputTokens: 600, OutputTokens: 400, TotalTokens: 1000},
		{Timestamp: now.Add(-2 * time.Hour), Model: "m", InputTokens: 600, OutputTokens: 400, TotalTokens: 1000},
		{Timestamp: now.Add(-time.Hour), Model: "m", InputTokens: 600, OutputTokens: 400, TotalTokens: 1000},
	}}, nil
}

func (s *countingLogScanner) LogDir() string { return "" }

// Local session logs carry no account identity, so they are read once per
// call and shared by every profile instead of once per profile.
func TestMultiProfileFetcherScansLogsOncePerCall(t *testing.T) {
	scanner := &countingLogScanner{}
	multi := logs.NewMultiScanner()
	multi.Register("claude", scanner)
	m := &MultiProfileFetcher{logScanner: multi}
	results := m.FetchAllProfiles(context.Background(), "claude", map[string]string{"a": "t", "b": "t", "c": "t", "d": "t"})
	if len(results) != 4 {
		t.Fatalf("results = %d, want 4", len(results))
	}
	for _, r := range results {
		if r.Usage == nil || r.Usage.BurnRate == nil {
			t.Fatalf("%s has no burn rate: %+v", r.ProfileName, r.Usage)
		}
	}
	if scanner.scans != 1 {
		t.Fatalf("logs scanned %d times for 4 profiles, want once", scanner.scans)
	}
	if rows := (&MultiProfileFetcher{logScanner: multi}).FetchAllProfiles(context.Background(), "codex", map[string]string{"x": "t"}); rows[0].Usage.BurnRate != nil {
		t.Fatal("a provider without a scanner got another provider's burn rate")
	}
}

func TestCodexFetcher_Fetch_BalanceParsing(t *testing.T) {
	tests := []struct {
		name     string
		balance  string
		expected float64
		absent   bool
		invalid  bool
	}{
		{name: "numeric balance", balance: "12.34", expected: 12.34},
		{name: "string balance", balance: `"45.67"`, expected: 45.67},
		{name: "comma balance", balance: `"1,234.50"`, expected: 1234.50},
		{name: "zero balance", balance: "0", expected: 0},
		{name: "finite negative balance", balance: "-12.34", expected: -12.34},
		{name: "null balance", balance: "null", absent: true},
		{name: "empty string balance", balance: `""`, absent: true},
		{name: "blank string balance", balance: `"   "`, absent: true},
		{name: "NaN balance", balance: `"NaN"`, invalid: true},
		{name: "positive infinite balance", balance: `"+Inf"`, invalid: true},
		{name: "negative infinite balance", balance: `"-Inf"`, invalid: true},
		{name: "infinity balance", balance: `"Infinity"`, invalid: true},
		{name: "negative infinity balance", balance: `"-Infinity"`, invalid: true},
		{name: "numeric overflow", balance: "1e999", invalid: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A response without a measured general window is degraded and
			// returns an error (see the quota-response tests), so carry one
			// here to test balance parsing on a successful fetch.
			payload := fmt.Sprintf(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":10,"reset_at":1900000000,"limit_window_seconds":18000}},"credits":{"has_credits":true,"unlimited":false,"balance":%s}}`, tc.balance)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != CodexUsagePath {
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()

			fetcher := NewCodexFetcher()
			fetcher.baseURL = server.URL

			info, err := fetcher.Fetch(context.Background(), "token")
			if tc.invalid {
				if err == nil || info == nil || info.Error == "" || info.QuotaStatus != QuotaUnavailable || info.NumericQuotaKnown() {
					t.Fatalf("invalid balance accepted as usable quota: info=%+v err=%v", info, err)
				}
				if data, marshalErr := json.Marshal(info); marshalErr != nil || !json.Valid(data) {
					t.Fatalf("invalid balance escaped into usage JSON: %q, %v", data, marshalErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if info.Credits == nil || !info.NumericQuotaKnown() {
				t.Fatalf("valid balance lost credit metadata or measured quota: %+v", info)
			}
			if tc.absent {
				if info.Credits.Balance != nil {
					t.Fatalf("absent balance manufactured a number: %v", *info.Credits.Balance)
				}
				return
			}
			if info.Credits.Balance == nil {
				t.Fatalf("expected credits balance, got nil")
			}
			if got := *info.Credits.Balance; got != tc.expected {
				t.Fatalf("balance = %v, expected %v", got, tc.expected)
			}
		})
	}
}

func TestCodexBalanceNonFiniteErrorsDoNotRepeatInput(t *testing.T) {
	for _, raw := range []string{`"NaN"`, `"+Inf"`, `"-Infinity"`} {
		var balance codexBalance
		if err := json.Unmarshal([]byte(raw), &balance); err == nil || err.Error() != "credit balance must be finite" {
			t.Errorf("non-finite balance error must be bounded and omit provider input: %v", err)
		}
		if balance.Value != nil {
			t.Error("non-finite balance retained a numeric value")
		}
	}
}

// TestClaudeFetcher_Fetch_PercentScaling is a regression test for issue #52.
// The Claude OAuth usage API reports utilization on a 0-100 percent scale
// (1.0 == 1%, 4.0 == 4%, 100.0 == 100%). Earlier code treated any value
// <= 1.0 as a 0-1 fraction and multiplied by 100, so a fresh subscription at
// 1.0% weekly usage was mis-reported as 100%. Values must be taken as percents
// directly: UsedPercent == floor(apiValue) and Utilization == apiValue/100.
func TestClaudeFetcher_Fetch_PercentScaling(t *testing.T) {
	tests := []struct {
		name            string
		apiValue        float64
		wantUsedPercent int
		wantUtilization float64
	}{
		{name: "half percent", apiValue: 0.5, wantUsedPercent: 0, wantUtilization: 0.005},
		{name: "one percent boundary", apiValue: 1.0, wantUsedPercent: 1, wantUtilization: 0.01},
		{name: "four percent", apiValue: 4.0, wantUsedPercent: 4, wantUtilization: 0.04},
		{name: "full", apiValue: 100.0, wantUsedPercent: 100, wantUtilization: 1.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := fmt.Sprintf(
				`{"five_hour":{"utilization":%g,"resets_at":""},"seven_day":{"utilization":%g,"resets_at":""},"opus":{"utilization":%g,"resets_at":""}}`,
				tc.apiValue, tc.apiValue, tc.apiValue,
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()

			fetcher := NewClaudeFetcher()
			fetcher.baseURL = server.URL

			info, err := fetcher.Fetch(context.Background(), "token")
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}

			for _, w := range []struct {
				label  string
				window *UsageWindow
			}{
				{"primary", info.PrimaryWindow},
				{"secondary", info.SecondaryWindow},
				{"tertiary", info.TertiaryWindow},
			} {
				if w.window == nil {
					t.Fatalf("%s window is nil", w.label)
				}
				if w.window.UsedPercent != tc.wantUsedPercent {
					t.Errorf("%s UsedPercent = %d, want %d (apiValue=%g)",
						w.label, w.window.UsedPercent, tc.wantUsedPercent, tc.apiValue)
				}
				if diff := w.window.Utilization - tc.wantUtilization; diff > 1e-9 || diff < -1e-9 {
					t.Errorf("%s Utilization = %g, want %g (apiValue=%g)",
						w.label, w.window.Utilization, tc.wantUtilization, tc.apiValue)
				}
			}
		})
	}
}

func TestUsageInfo_MostConstrainedWindow(t *testing.T) {
	tests := []struct {
		name     string
		info     *UsageInfo
		expected float64 // expected utilization of most constrained
	}{
		{
			name:     "nil returns nil",
			info:     nil,
			expected: -1, // signal for nil
		},
		{
			name:     "empty info returns nil",
			info:     &UsageInfo{},
			expected: -1,
		},
		{
			name: "primary is most constrained",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{Utilization: 0.9},
				SecondaryWindow: &UsageWindow{Utilization: 0.5},
			},
			expected: 0.9,
		},
		{
			name: "tertiary is most constrained",
			info: &UsageInfo{
				PrimaryWindow:  &UsageWindow{Utilization: 0.5},
				TertiaryWindow: &UsageWindow{Utilization: 0.95},
			},
			expected: 0.95,
		},
		{
			name: "model window is most constrained",
			info: &UsageInfo{
				PrimaryWindow: &UsageWindow{Utilization: 0.5},
				ModelWindows: map[string]*UsageWindow{
					"opus": {Utilization: 1.0},
				},
			},
			expected: 1.0,
		},
		{
			name: "uses UsedPercent when Utilization is 0",
			info: &UsageInfo{
				PrimaryWindow:   &UsageWindow{UsedPercent: 80},
				SecondaryWindow: &UsageWindow{UsedPercent: 50},
			},
			expected: 0.8,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.info.MostConstrainedWindow()
			if tc.expected == -1 {
				if result != nil {
					t.Error("expected nil window")
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil window")
			}

			util := result.Utilization
			if util == 0 && result.UsedPercent > 0 {
				util = float64(result.UsedPercent) / 100.0
			}

			if util != tc.expected {
				t.Errorf("MostConstrainedWindow().Utilization = %v, expected %v", util, tc.expected)
			}
		})
	}
}

func TestUsageInfoMostConstrainedWindowForModel(t *testing.T) {
	weekly := &UsageWindow{UsedPercent: 20, WindowDuration: 7 * 24 * time.Hour}
	fable := &UsageWindow{Label: "Fable", UsedPercent: 100} // Unknown duration still constrains usage.
	sonnetSession := &UsageWindow{Label: "Sonnet", Utilization: 0.4, UsedPercent: 10}
	sonnetWeekly := &UsageWindow{Label: "Sonnet", Utilization: 0.1, UsedPercent: 75}
	info := &UsageInfo{
		SecondaryWindow: weekly,
		ModelWindows:    map[string]*UsageWindow{"fable": fable, "sonnet": sonnetSession},
		ScopedWindows:   []*UsageWindow{sonnetSession, sonnetWeekly},
	}
	for _, tc := range []struct {
		model string
		want  *UsageWindow
	}{
		{"", fable},
		{"fable", fable},
		{"sonnet", sonnetWeekly},
		{"opus", weekly},
	} {
		if got := info.MostConstrainedWindowForModel(tc.model); got != tc.want {
			t.Errorf("model %q lost a general or overlapping restriction: got %+v, want %+v", tc.model, got, tc.want)
		}
	}

	weekly.UsedPercent = 90
	if got := info.MostConstrainedWindowForModel("sonnet"); got != weekly {
		t.Errorf("scoped selection hid the general weekly quota: %+v", got)
	}
	if got := (&UsageInfo{SecondaryWindow: weekly}).MostConstrainedWindowForModel("sonnet"); got != weekly {
		t.Errorf("weekly-only account lost its only general measurement: %+v", got)
	}
	var missing *UsageInfo
	if missing.MostConstrainedWindowForModel("sonnet") != nil {
		t.Error("missing quota manufactured a window")
	}
}

func TestReadClaudeCredentials(t *testing.T) {
	tmpDir := t.TempDir()

	credentialsPath := filepath.Join(tmpDir, "credentials.json")
	credentialsContent := `{"claudeAiOauth":{"accessToken":"tok-1","accountId":"acct-1"}}`
	if err := os.WriteFile(credentialsPath, []byte(credentialsContent), 0600); err != nil {
		t.Fatalf("write credentials.json: %v", err)
	}

	token, account, err := ReadClaudeCredentials(credentialsPath)
	if err != nil {
		t.Fatalf("ReadClaudeCredentials error: %v", err)
	}
	if token != "tok-1" || account != "acct-1" {
		t.Fatalf("unexpected token/account: %q/%q", token, account)
	}

	legacyPath := filepath.Join(tmpDir, "legacy.json")
	legacyContent := `{"oauthToken":"tok-legacy"}`
	if err := os.WriteFile(legacyPath, []byte(legacyContent), 0600); err != nil {
		t.Fatalf("write legacy.json: %v", err)
	}

	objectPath := filepath.Join(tmpDir, "legacy_object.json")
	objectContent := `{"oauthToken":{"access_token":"tok-object"}}`
	if err := os.WriteFile(objectPath, []byte(objectContent), 0600); err != nil {
		t.Fatalf("write legacy_object.json: %v", err)
	}

	token, account, err = ReadClaudeCredentials(objectPath)
	if err != nil {
		t.Fatalf("ReadClaudeCredentials legacy object error: %v", err)
	}
	if token != "tok-object" || account != "" {
		t.Fatalf("unexpected legacy object token/account: %q/%q", token, account)
	}

	token, account, err = ReadClaudeCredentials(legacyPath)
	if err != nil {
		t.Fatalf("ReadClaudeCredentials legacy error: %v", err)
	}
	if token != "tok-legacy" || account != "" {
		t.Fatalf("unexpected legacy token/account: %q/%q", token, account)
	}
}

func TestReadCodexCredentials(t *testing.T) {
	tmpDir := t.TempDir()

	rootPath := filepath.Join(tmpDir, "auth_root.json")
	rootContent := `{"access_token":"root-token","account_id":"acct"}`
	if err := os.WriteFile(rootPath, []byte(rootContent), 0600); err != nil {
		t.Fatalf("write auth_root.json: %v", err)
	}

	token, account, err := ReadCodexCredentials(rootPath)
	if err != nil {
		t.Fatalf("ReadCodexCredentials root error: %v", err)
	}
	if token != "root-token" || account != "acct" {
		t.Fatalf("unexpected root token/account: %q/%q", token, account)
	}

	tokensPath := filepath.Join(tmpDir, "auth_tokens.json")
	tokensContent := `{"tokens":{"access_token":"nested-token","account_id":"acct-2"}}`
	if err := os.WriteFile(tokensPath, []byte(tokensContent), 0600); err != nil {
		t.Fatalf("write auth_tokens.json: %v", err)
	}

	token, account, err = ReadCodexCredentials(tokensPath)
	if err != nil {
		t.Fatalf("ReadCodexCredentials tokens error: %v", err)
	}
	if token != "nested-token" || account != "acct-2" {
		t.Fatalf("unexpected tokens token/account: %q/%q", token, account)
	}

	apiKeyPath := filepath.Join(tmpDir, "auth_api_key.json")
	apiKeyContent := `{"OPENAI_API_KEY":"sk-test"}`
	if err := os.WriteFile(apiKeyPath, []byte(apiKeyContent), 0600); err != nil {
		t.Fatalf("write auth_api_key.json: %v", err)
	}

	token, account, err = ReadCodexCredentials(apiKeyPath)
	if err == nil {
		t.Fatal("ReadCodexCredentials api key expected error, got nil")
	}
	if token != "" || account != "" {
		t.Fatalf("unexpected api key token/account: %q/%q", token, account)
	}
}

func TestUsageInfo_WindowForModel(t *testing.T) {
	tertiaryWindow := &UsageWindow{Utilization: 0.7}
	opusWindow := &UsageWindow{Utilization: 0.9}

	tests := []struct {
		name       string
		info       *UsageInfo
		model      string
		expectNil  bool
		expectUtil float64
	}{
		{
			name:      "nil returns nil",
			info:      nil,
			model:     "opus",
			expectNil: true,
		},
		{
			name:      "empty info returns nil",
			info:      &UsageInfo{},
			model:     "opus",
			expectNil: true,
		},
		{
			name: "finds model-specific window",
			info: &UsageInfo{
				TertiaryWindow: tertiaryWindow,
				ModelWindows: map[string]*UsageWindow{
					"claude-3-opus": opusWindow,
				},
			},
			model:      "claude-3-opus",
			expectUtil: 0.9,
		},
		{
			name: "falls back to tertiary",
			info: &UsageInfo{
				TertiaryWindow: tertiaryWindow,
				ModelWindows: map[string]*UsageWindow{
					"claude-3-opus": opusWindow,
				},
			},
			model:      "claude-3-sonnet",
			expectUtil: 0.7,
		},
		{
			name: "returns nil if no match and no tertiary",
			info: &UsageInfo{
				ModelWindows: map[string]*UsageWindow{
					"claude-3-opus": opusWindow,
				},
			},
			model:     "claude-3-sonnet",
			expectNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.info.WindowForModel(tc.model)
			if tc.expectNil {
				if result != nil {
					t.Error("expected nil window")
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil window")
			}

			if result.Utilization != tc.expectUtil {
				t.Errorf("WindowForModel(%s).Utilization = %v, expected %v",
					tc.model, result.Utilization, tc.expectUtil)
			}
		})
	}
}

func TestUsageInfoScopedWindowsPreserveModelResetHorizons(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	short := &UsageWindow{
		Label: "Reasoning model", ModelID: "claude-fable-5", Kind: LimitKindSession,
		UsedPercent: 10, Utilization: 0.1, WindowDuration: 5 * time.Hour, ResetsAt: now.Add(2 * time.Hour),
	}
	long := &UsageWindow{
		Label: "Fable", Kind: LimitKindWeeklyScoped, UsedPercent: 80, Utilization: 0.8,
		WindowDuration: 7 * 24 * time.Hour, ResetsAt: now.Add(120 * time.Hour),
	}
	opus := &UsageWindow{Label: "Opus", UsedPercent: 30, ResetsAt: now.Add(5 * time.Minute)}
	for _, windows := range [][]*UsageWindow{{short, long}, {long, short}} {
		info := &UsageInfo{
			Provider: "claude", SecondaryWindow: &UsageWindow{UsedPercent: 20, ResetsAt: now.Add(24 * time.Hour)},
			TertiaryWindow: opus, ModelWindows: map[string]*UsageWindow{"Fable": long}, ScopedWindows: windows,
		}
		before, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{"fable", "claude-fable-5", "CLAUDE-FABLE-5-20261103"} {
			if got := info.EarliestResetForModel(model); !got.Equal(short.ResetsAt) {
				t.Fatalf("%s lost the less-used model session reset: %s", model, got)
			}
			if got := info.ScopedLimit(model); got != long || info.WindowForModel(model) != long {
				t.Fatalf("%s did not retain the binding weekly quota: %+v", model, got)
			}
			if got := GoverningWindow(info, model); got != long {
				t.Fatalf("%s did not retain the longest allowance: %+v", model, got)
			}
		}
		if got := info.EarliestResetForModel("sonnet"); !got.Equal(info.SecondaryWindow.ResetsAt) {
			t.Fatalf("Sonnet borrowed another model's reset: %s", got)
		}
		if info.ScopedLimit("sonnet") != nil || info.WindowForModel("sonnet") != nil {
			t.Fatal("Sonnet inherited an unrelated tertiary or scoped restriction")
		}
		if !info.EarliestReset().Equal(opus.ResetsAt) || !info.EarliestResetForModel("").Equal(opus.ResetsAt) {
			t.Fatal("unknown-model work omitted a reported restriction")
		}
		after, err := json.Marshal(info)
		if err != nil || string(before) != string(after) {
			t.Fatal("quota inspection mutated the provider's window order or measurements")
		}
		var restored UsageInfo
		if err := json.Unmarshal(before, &restored); err != nil {
			t.Fatal(err)
		}
		if len(restored.ScopedWindows) != 2 || restored.ModelWindows["Fable"].UsedPercent != 80 ||
			!restored.EarliestResetForModel("fable").Equal(short.ResetsAt) {
			t.Fatalf("normalized JSON lost the complete windows or existing projection: %+v", restored)
		}
	}
	if !(*UsageInfo)(nil).EarliestResetForModel("fable").IsZero() {
		t.Fatal("nil usage invented a model reset")
	}
}

func TestScopedWindowProjectionIsStableAndPreservesExhaustion(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		want, other *UsageWindow
	}{
		{
			name:  "integer exhaustion beats a larger fractional field",
			want:  &UsageWindow{Label: "Fable", UsedPercent: 100, Utilization: 0.2},
			other: &UsageWindow{Label: "Fable", UsedPercent: 95, Utilization: 0.95},
		},
		{
			name:  "fractional precision remains binding",
			want:  &UsageWindow{Label: "Fable", UsedPercent: 50, Utilization: 0.509},
			other: &UsageWindow{Label: "Fable", UsedPercent: 50, Utilization: 0.505},
		},
		{
			name:  "equal usage prefers a known longer allowance",
			want:  &UsageWindow{Label: "Fable", UsedPercent: 80, WindowDuration: 7 * 24 * time.Hour},
			other: &UsageWindow{Label: "Fable", UsedPercent: 80, WindowDuration: 5 * time.Hour},
		},
		{
			name:  "equal duration prefers later reset",
			want:  &UsageWindow{Label: "Fable", UsedPercent: 80, ResetsAt: now.Add(48 * time.Hour)},
			other: &UsageWindow{Label: "Fable", UsedPercent: 80, ResetsAt: now.Add(24 * time.Hour)},
		},
		{
			name:  "equal usage and horizons order by provider kind",
			want:  &UsageWindow{Label: "Fable", Kind: "a-scoped", UsedPercent: 80},
			other: &UsageWindow{Label: "Fable", Kind: "z-scoped", UsedPercent: 80},
		},
		{
			name:  "equal kinds order by provider model identifier",
			want:  &UsageWindow{Label: "Fable", ModelID: "model-a", UsedPercent: 80},
			other: &UsageWindow{Label: "Fable", ModelID: "model-z", UsedPercent: 80},
		},
		{
			name:  "equal identifiers order by display label",
			want:  &UsageWindow{Label: "Fable A", ModelID: "claude-fable-5", UsedPercent: 80},
			other: &UsageWindow{Label: "Fable Z", ModelID: "claude-fable-5", UsedPercent: 80},
		},
		{
			name:  "display-only scope cannot suppress a measurement",
			want:  &UsageWindow{Label: "Fable", UsedPercent: 20},
			other: &UsageWindow{Label: "Fable", Utilization: 1, Unmeasured: true},
		},
		{
			name:  "rolled scope cannot suppress a measured zero",
			want:  &UsageWindow{Label: "Fable"},
			other: &UsageWindow{Label: "Fable", Rolled: true, WindowDuration: 7 * 24 * time.Hour},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, windows := range [][]*UsageWindow{{tc.want, tc.other}, {tc.other, tc.want}} {
				info := &UsageInfo{PrimaryWindow: &UsageWindow{}, ScopedWindows: windows}
				if got := info.ScopedLimit("fable"); got != tc.want || info.WindowForModel("fable") != tc.want {
					t.Fatalf("scope projection depended on input order: got=%+v want=%+v", got, tc.want)
				}
				if tc.want.UsedPercent == 100 {
					if !info.IsNearLimitForModel(1, "fable") || info.MostConstrainedWindow() != tc.want || info.AvailabilityScoreForModel("fable") != 85 {
						t.Fatal("the stronger integer measurement disappeared from quota consumers")
					}
				}
			}
		})
	}
}

func TestCreditInfo(t *testing.T) {
	t.Run("has credits", func(t *testing.T) {
		info := &UsageInfo{
			PrimaryWindow: &UsageWindow{},
			Credits:       &CreditInfo{HasCredits: true},
		}
		// Should not penalize score
		score := info.AvailabilityScore()
		if score != 100 {
			t.Errorf("score with HasCredits=true = %d, expected 100", score)
		}
	})

	t.Run("no credits penalizes", func(t *testing.T) {
		info := &UsageInfo{
			PrimaryWindow: &UsageWindow{},
			Credits:       &CreditInfo{HasCredits: false, Unlimited: false},
		}
		score := info.AvailabilityScore()
		if score != 90 {
			t.Errorf("score with no credits = %d, expected 90", score)
		}
	})
}

func TestPredictDepletion(t *testing.T) {
	now := time.Now()
	future1h := now.Add(time.Hour)

	tests := []struct {
		name           string
		currentPercent float64
		burnRate       *BurnRateInfo
		window         *UsageWindow
		expectZero     bool
		expectRange    [2]time.Duration // min, max from now
	}{
		{
			name:           "nil burn rate returns zero",
			currentPercent: 50,
			burnRate:       nil,
			expectZero:     true,
		},
		{
			name:           "zero burn rate returns zero",
			currentPercent: 50,
			burnRate:       &BurnRateInfo{PercentPerHour: 0},
			expectZero:     true,
		},
		{
			name:           "already at 100% returns now",
			currentPercent: 100,
			burnRate:       &BurnRateInfo{PercentPerHour: 10},
			expectRange:    [2]time.Duration{-time.Second, time.Second}, // Allow slight timing variance
		},
		{
			name:           "50% at 10%/hr = 5 hours",
			currentPercent: 50,
			burnRate:       &BurnRateInfo{PercentPerHour: 10},
			expectRange:    [2]time.Duration{4*time.Hour + 55*time.Minute, 5*time.Hour + 5*time.Minute},
		},
		{
			name:           "80% at 20%/hr = 1 hour",
			currentPercent: 80,
			burnRate:       &BurnRateInfo{PercentPerHour: 20},
			expectRange:    [2]time.Duration{55 * time.Minute, 65 * time.Minute},
		},
		{
			name:           "caps at window reset",
			currentPercent: 10,
			burnRate:       &BurnRateInfo{PercentPerHour: 5}, // Would take 18 hours
			window:         &UsageWindow{ResetsAt: future1h}, // But resets in 1 hour
			expectRange:    [2]time.Duration{55 * time.Minute, 65 * time.Minute},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PredictDepletion(tc.currentPercent, tc.burnRate, tc.window)

			if tc.expectZero {
				if !result.IsZero() {
					t.Errorf("expected zero time, got %v", result)
				}
				return
			}

			duration := time.Until(result)
			if duration < tc.expectRange[0] || duration > tc.expectRange[1] {
				t.Errorf("PredictDepletion() = %v from now, expected in range [%v, %v]",
					duration, tc.expectRange[0], tc.expectRange[1])
			}
		})
	}
}

func TestUsageInfo_UpdateDepletion(t *testing.T) {
	t.Run("nil info does nothing", func(t *testing.T) {
		var info *UsageInfo
		info.UpdateDepletion() // Should not panic
	})

	t.Run("no burn rate does nothing", func(t *testing.T) {
		info := &UsageInfo{
			PrimaryWindow: &UsageWindow{Utilization: 0.5},
		}
		info.UpdateDepletion()
		if !info.EstimatedDepletion.IsZero() {
			t.Error("expected zero depletion without burn rate")
		}
	})

	t.Run("calculates depletion correctly", func(t *testing.T) {
		info := &UsageInfo{
			PrimaryWindow: &UsageWindow{Utilization: 0.5}, // 50%
			BurnRate:      &BurnRateInfo{PercentPerHour: 10, Confidence: 0.8},
		}
		info.UpdateDepletion()

		// 50% remaining at 10%/hr = 5 hours
		ttd := info.TimeToDepletion()
		if ttd < 4*time.Hour+50*time.Minute || ttd > 5*time.Hour+10*time.Minute {
			t.Errorf("TimeToDepletion() = %v, expected ~5h", ttd)
		}

		if info.DepletionConfidence != 0.8 {
			t.Errorf("DepletionConfidence = %v, expected 0.8", info.DepletionConfidence)
		}
	})
}

func TestUsageInfo_TimeToDepletion(t *testing.T) {
	t.Run("nil returns 0", func(t *testing.T) {
		var info *UsageInfo
		if info.TimeToDepletion() != 0 {
			t.Error("expected 0 for nil")
		}
	})

	t.Run("zero depletion returns 0", func(t *testing.T) {
		info := &UsageInfo{}
		if info.TimeToDepletion() != 0 {
			t.Error("expected 0 for zero depletion")
		}
	})

	t.Run("past depletion returns 0", func(t *testing.T) {
		info := &UsageInfo{
			EstimatedDepletion: time.Now().Add(-time.Hour),
		}
		if info.TimeToDepletion() != 0 {
			t.Error("expected 0 for past depletion")
		}
	})

	t.Run("future depletion returns positive", func(t *testing.T) {
		info := &UsageInfo{
			EstimatedDepletion: time.Now().Add(time.Hour),
		}
		ttd := info.TimeToDepletion()
		if ttd < 55*time.Minute || ttd > 65*time.Minute {
			t.Errorf("TimeToDepletion() = %v, expected ~1h", ttd)
		}
	})
}

func TestUsageInfo_IsDepletionImminent(t *testing.T) {
	t.Run("nil returns false", func(t *testing.T) {
		var info *UsageInfo
		if info.IsDepletionImminent(10 * time.Minute) {
			t.Error("expected false for nil")
		}
	})

	t.Run("zero depletion returns false", func(t *testing.T) {
		info := &UsageInfo{}
		if info.IsDepletionImminent(10 * time.Minute) {
			t.Error("expected false for zero depletion")
		}
	})

	t.Run("imminent when within threshold", func(t *testing.T) {
		info := &UsageInfo{
			EstimatedDepletion: time.Now().Add(5 * time.Minute),
		}
		if !info.IsDepletionImminent(10 * time.Minute) {
			t.Error("expected true for 5min depletion with 10min threshold")
		}
	})

	t.Run("not imminent when beyond threshold", func(t *testing.T) {
		info := &UsageInfo{
			EstimatedDepletion: time.Now().Add(30 * time.Minute),
		}
		if info.IsDepletionImminent(10 * time.Minute) {
			t.Error("expected false for 30min depletion with 10min threshold")
		}
	})
}

func TestUsageInfo_DepletionWarningLevel(t *testing.T) {
	tests := []struct {
		name     string
		info     *UsageInfo
		expected int
	}{
		{
			name:     "nil returns 0",
			info:     nil,
			expected: 0,
		},
		{
			name:     "zero depletion returns 0",
			info:     &UsageInfo{},
			expected: 0,
		},
		{
			name: "imminent (<10min) returns 2",
			info: &UsageInfo{
				EstimatedDepletion: time.Now().Add(5 * time.Minute),
			},
			expected: 2,
		},
		{
			name: "approaching (<30min) returns 1",
			info: &UsageInfo{
				EstimatedDepletion: time.Now().Add(20 * time.Minute),
			},
			expected: 1,
		},
		{
			name: "none (>=30min) returns 0",
			info: &UsageInfo{
				EstimatedDepletion: time.Now().Add(time.Hour),
			},
			expected: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.info.DepletionWarningLevel()
			if result != tc.expected {
				t.Errorf("DepletionWarningLevel() = %d, expected %d", result, tc.expected)
			}
		})
	}
}
