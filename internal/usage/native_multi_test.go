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
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNativeBatchRejectsIncompleteQuotaInEverySelectionHelper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == cursorPeriodUsagePath && r.Header.Get("Authorization") == "Bearer SYNTHETIC-KNOWN" {
			fmt.Fprint(w, `{"planUsage":{"limit":100,"includedSpend":25,"apiPercentUsed":40}}`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	credentials := make(map[string]string)
	for name, token := range map[string]string{"known": "SYNTHETIC-KNOWN", "incomplete": "SYNTHETIC-INCOMPLETE"} {
		dir := t.TempDir()
		path := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(path, []byte(`{"accessToken":"`+token+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		credentials[name] = "cursor-root:" + path
	}
	fetcher := &MultiProfileFetcher{cursorFetcher: &CursorFetcher{BaseURL: server.URL, ClientVersion: "synthetic"}}
	ctx := context.Background()
	rows := fetcher.FetchAllProfiles(ctx, "cursor", credentials)
	if len(rows) != 2 {
		t.Fatalf("batch dropped a profile: %+v", rows)
	}
	for _, row := range rows {
		if row.Provider != "cursor" || row.Usage.ProfileName != row.ProfileName {
			t.Errorf("profile identity changed: %+v", row)
		}
		if row.ProfileName == "incomplete" && (row.Usage.QuotaStatus != QuotaDegraded || row.Usage.Error == "" || row.Usage.NumericQuotaKnown()) {
			t.Errorf("incomplete row could appear healthy to a legacy consumer: %+v", row.Usage)
		}
	}
	encoded, err := json.Marshal(rows)
	if err != nil || strings.Contains(string(encoded), "SYNTHETIC-") {
		t.Fatalf("quota JSON serialization failed or exposed credentials: %s, %v", encoded, err)
	}
	if best := fetcher.GetBestProfile(ctx, "cursor", credentials); best == nil || best.ProfileName != "known" {
		t.Fatalf("best profile = %+v", best)
	}
	if available := fetcher.GetProfilesAboveThreshold(ctx, "cursor", credentials, 0.8); len(available) != 1 || available[0].ProfileName != "known" {
		t.Fatalf("threshold selection included unknown capacity: %+v", available)
	}
	onlyUnknown := map[string]string{"incomplete": credentials["incomplete"]}
	if best := fetcher.GetBestProfile(ctx, "cursor", onlyUnknown); best != nil {
		t.Fatalf("only incomplete profile selected: %+v", best)
	}
}

func TestNativeBatchUnavailableFetchersStayUnavailable(t *testing.T) {
	fetcher := &MultiProfileFetcher{}
	for _, provider := range []string{"grok", "cursor"} {
		credentials := map[string]string{"one": "unused", "two": "unused"}
		rows := fetcher.FetchAllProfiles(context.Background(), provider, credentials)
		if len(rows) != 2 {
			t.Fatalf("%s lost error rows: %+v", provider, rows)
		}
		for _, row := range rows {
			if row.Usage == nil || row.Usage.Provider != provider || row.Usage.QuotaStatus != QuotaUnavailable || row.Usage.NumericQuotaKnown() {
				t.Errorf("%s unavailable row = %+v", provider, row)
			}
		}
		if best := fetcher.GetBestProfile(context.Background(), provider, credentials); best != nil {
			t.Errorf("%s selected a failed fetch: %+v", provider, best)
		}
	}
}

// Preserve all failures for diagnostics, but never offer one to a caller
// asking which account should receive new work, regardless of provider.
func TestBatchSelectionRejectsFailedFetchesForEveryProvider(t *testing.T) {
	fetcher := &MultiProfileFetcher{}
	for _, provider := range []string{"claude", "codex", "grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			credentials := map[string]string{"one": "unused", "two": "unused"}
			rows := fetcher.FetchAllProfiles(context.Background(), provider, credentials)
			if len(rows) != 2 {
				t.Fatal("diagnostic rows were dropped")
			}
			for _, row := range rows {
				if row.Usage == nil || row.Usage.Error == "" {
					t.Fatal("unavailable fetcher did not report a failure")
				}
			}
			if best := fetcher.GetBestProfile(context.Background(), provider, credentials); best != nil {
				t.Fatal("best-profile selection recommended a failed fetch")
			}
			if available := fetcher.GetProfilesAboveThreshold(context.Background(), provider, credentials, 0.8); len(available) != 0 {
				t.Fatal("failed fetch was interpreted as spare capacity")
			}
		})
	}
}

func TestProfilesBelowThresholdRetainsOnlyUsableCapacity(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			row := func(name string, used float64) ProfileUsage {
				return ProfileUsage{Provider: provider, ProfileName: name, Usage: &UsageInfo{
					Provider: provider, QuotaStatus: QuotaOK,
					PrimaryWindow: &UsageWindow{Utilization: used, UsedPercent: int(used * 100)},
				}}
			}
			failed := row("failed", 0)
			failed.Usage.Error = "request failed"
			degraded := row("degraded", 0)
			degraded.Usage.QuotaStatus = QuotaDegraded
			unknown := row("unknown", 0)
			unknown.Usage.PrimaryWindow.Unmeasured = true
			secondary := row("secondary-exhausted", 0)
			secondary.Usage.SecondaryWindow = &UsageWindow{UsedPercent: 100}
			tertiary := row("tertiary-exhausted", 0)
			tertiary.Usage.TertiaryWindow = &UsageWindow{UsedPercent: 100}
			scoped := row("scoped-exhausted", 0)
			scoped.Usage.ModelWindows = map[string]*UsageWindow{"opus": {UsedPercent: 100}}
			blocked := row("blocked", 0)
			blocked.Usage.LimitStage = "HARD_BLOCK"
			rows := []ProfileUsage{
				failed, {Provider: provider, ProfileName: "nil"}, degraded, unknown, blocked,
				row("zero", 0), row("some", 0.25), row("boundary", 0.8), row("primary-exhausted", 1),
				secondary, tertiary, scoped,
			}
			before, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				threshold float64
				want      []string
			}{
				{0.8, []string{"zero", "some"}},
				{1, []string{"zero", "some", "boundary"}},
				{0.25, []string{"zero"}},
			} {
				var names []string
				for _, result := range profilesBelowThreshold(rows, tc.threshold) {
					names = append(names, result.ProfileName)
				}
				if !reflect.DeepEqual(names, tc.want) {
					t.Fatalf("threshold=%v got=%v want=%v", tc.threshold, names, tc.want)
				}
			}
			after, err := json.Marshal(rows)
			if err != nil || string(before) != string(after) {
				t.Fatal("selection changed diagnostic rows or their ordering")
			}
			if got := profilesBelowThreshold(rows[:5], 1); len(got) != 0 {
				t.Fatal("all-failed pool supplied a candidate")
			}
			if got := profilesBelowThreshold(nil, 1); len(got) != 0 {
				t.Fatal("empty pool supplied a candidate")
			}
		})
	}
}

// Claude and Codex historically omit QuotaStatus. Do not require a new
// wire format to fix error handling; keep their measured windows usable.
func TestProfilesBelowThresholdPreservesLegacyQuotaStatus(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		usable := ProfileUsage{Provider: provider, ProfileName: "usable", Usage: &UsageInfo{
			Provider: provider, PrimaryWindow: &UsageWindow{UsedPercent: 25},
		}}
		failed := ProfileUsage{Provider: provider, ProfileName: "failed", Usage: &UsageInfo{Provider: provider, Error: "request failed"}}
		spent := ProfileUsage{Provider: provider, ProfileName: "spent", Usage: &UsageInfo{
			Provider: provider, PrimaryWindow: &UsageWindow{UsedPercent: 100},
		}}
		got := profilesBelowThreshold([]ProfileUsage{failed, spent, usable}, 1)
		if len(got) != 1 || got[0].ProfileName != "usable" {
			t.Fatalf("%s selected failed/exhausted legacy quota or rejected valid quota", provider)
		}
	}
}

// A successful HTTP status is not evidence of available capacity. Missing
// percentages must never acquire Go's numeric zero value and become a seat
// that routing considers idle. Exercise the public fetchers and routing filter.
func TestSubscriptionQuotaRequiresExplicitMeasurements(t *testing.T) {
	cases := []struct{ provider, name, body string }{
		{"claude", "empty object", `{}`},
		{"claude", "null response", `null`},
		{"claude", "missing percentage", `{"five_hour":{}}`},
		{"claude", "null percentage", `{"five_hour":{"utilization":null}}`},
		{"claude", "negative percentage", `{"five_hour":{"utilization":-0.1}}`},
		{"claude", "excess percentage", `{"five_hour":{"utilization":100.1}}`},
		{"claude", "string percentage", `{"five_hour":{"utilization":"BODY-MUST-NOT-ESCAPE"}}`},
		{"claude", "unknown secondary", `{"five_hour":{"utilization":0},"seven_day":{}}`},
		{"claude", "unknown premium", `{"five_hour":{"utilization":0},"seven_day_opus":{}}`},
		{"claude", "missing limits percentage", `{"limits":[{"kind":"session"}]}`},
		{"claude", "null limits percentage", `{"limits":[{"kind":"session","percent":null}]}`},
		{"claude", "invalid scoped percentage", `{"five_hour":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":-1,"scope":{"model":{"display_name":"Opus"}}}]}`},
		{"claude", "unknown scoped percentage", `{"five_hour":{"utilization":0},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Opus"}}}]}`},
		{"claude", "null limit", `{"five_hour":{"utilization":0},"limits":[null]}`},
		{"claude", "model-only response", `{"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Opus"}}}]}`},
		{"claude", "second document", `{"five_hour":{"utilization":0}} {"error":"BODY-MUST-NOT-ESCAPE"}`},
		{"claude", "trailing garbage", `{"five_hour":{"utilization":0}} BODY-MUST-NOT-ESCAPE`},
		{"codex", "empty object", `{}`},
		{"codex", "null response", `null`},
		{"codex", "plan only", `{"plan_type":"pro"}`},
		{"codex", "credits only", `{"credits":{"has_credits":true,"unlimited":true}}`},
		{"codex", "missing percentage", `{"rate_limit":{"primary_window":{}}}`},
		{"codex", "null percentage", `{"rate_limit":{"primary_window":{"used_percent":null}}}`},
		{"codex", "negative percentage", `{"rate_limit":{"primary_window":{"used_percent":-1}}}`},
		{"codex", "excess percentage", `{"rate_limit":{"primary_window":{"used_percent":101}}}`},
		{"codex", "string percentage", `{"rate_limit":{"primary_window":{"used_percent":"BODY-MUST-NOT-ESCAPE"}}}`},
		{"codex", "unknown secondary", `{"rate_limit":{"primary_window":{"used_percent":0},"secondary_window":{}}}`},
		{"codex", "second document", `{"rate_limit":{"primary_window":{"used_percent":0}}} {"error":"BODY-MUST-NOT-ESCAPE"}`},
		{"codex", "trailing garbage", `{"rate_limit":{"primary_window":{"used_percent":0}}} BODY-MUST-NOT-ESCAPE`},
		{"codex", "invalid balance", `{"rate_limit":{"primary_window":{"used_percent":0}},"credits":{"balance":"BODY-MUST-NOT-ESCAPE"}}`},
	}
	for _, provider := range []string{"claude", "codex"} {
		valid := `{"five_hour":{"utilization":0}}`
		if provider == "codex" {
			valid = `{"rate_limit":{"primary_window":{"used_percent":0}}}`
		}
		cases = append(cases, struct{ provider, name, body string }{provider, "oversized valid prefix", valid + strings.Repeat(" ", 1<<20)})
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer SYNTHETIC-QUOTA" {
					t.Error("quota request changed method or selected credential")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			info, err := fetchSubscriptionQuotaFixture(tc.provider, server.URL)
			if err == nil || info == nil || info.Error == "" || info.NumericQuotaKnown() || info.AvailabilityScore() != 0 {
				t.Fatalf("incomplete HTTP response became capacity: info=%+v error=%v", info, err)
			}
			if info.QuotaStatus != QuotaUnavailable && info.QuotaStatus != QuotaDegraded {
				t.Fatal("failed quota has no explicit unavailable/degraded status")
			}
			if info.Provider != tc.provider || info.FetchedAt.IsZero() || info.Source != SourceAPI {
				t.Fatal("failure lost its provider or source metadata")
			}
			encoded, marshalErr := json.Marshal(info)
			if marshalErr != nil || strings.Contains(string(encoded)+err.Error(), "BODY-MUST-NOT-ESCAPE") {
				t.Fatal("quota diagnostic echoed untrusted response data")
			}
			rows := []ProfileUsage{{Provider: tc.provider, ProfileName: "incomplete", Usage: info}}
			if len(profilesBelowThreshold(rows, 1)) != 0 {
				t.Fatal("routing selected a response without a measured quota")
			}
		})
	}
}

func fetchSubscriptionQuotaFixture(provider, endpoint string) (*UsageInfo, error) {
	if provider == "claude" {
		f := NewClaudeFetcher()
		f.baseURL = endpoint
		return f.Fetch(context.Background(), "SYNTHETIC-QUOTA")
	}
	f := NewCodexFetcher()
	f.baseURL = endpoint
	return f.Fetch(context.Background(), "SYNTHETIC-QUOTA")
}

func TestSubscriptionQuotaPreservesMeasuredZeroAndLegacyShapes(t *testing.T) {
	for _, tc := range []struct {
		provider, name, body string
		primary, secondary   bool
		utilization          float64
	}{
		{"claude", "zero", `{"five_hour":{"utilization":0},"seven_day":null,"seven_day_opus":null}`, true, false, 0},
		{"claude", "fractional percent", `{"five_hour":{"utilization":0.5}}`, true, false, 0.005},
		{"claude", "secondary only", `{"seven_day":{"utilization":25}}`, false, true, 0.25},
		{"claude", "limits shape", `{"limits":[{"kind":"session","percent":25},{"kind":"weekly_all","percent":0}]}`, true, true, 0.25},
		{"claude", "unknown optional metadata", `{"five_hour":{"utilization":25},"future_metadata":{"value":"ignored"}}`, true, false, 0.25},
		{"codex", "zero", `{"rate_limit":{"primary_window":{"used_percent":0},"secondary_window":null}}`, true, false, 0},
		{"codex", "primary", `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":25}},"credits":{"has_credits":true,"balance":"1,234.50"}}`, true, false, 0.25},
		{"codex", "secondary only", `{"rate_limit":{"secondary_window":{"used_percent":25}}}`, false, true, 0.25},
	} {
		t.Run(tc.provider+"/"+tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer server.Close()
			info, err := fetchSubscriptionQuotaFixture(tc.provider, server.URL)
			if err != nil || info == nil || info.Error != "" || !info.NumericQuotaKnown() {
				t.Fatalf("measured quota rejected: info=%+v error=%v", info, err)
			}
			if (info.PrimaryWindow != nil) != tc.primary || (info.SecondaryWindow != nil) != tc.secondary {
				t.Fatal("measurement windows were invented or lost")
			}
			window := info.PrimaryWindow
			if window == nil {
				window = info.SecondaryWindow
			}
			if window.Utilization != tc.utilization || window.Unmeasured {
				t.Fatalf("measured percentage changed: %+v", window)
			}
			rows := []ProfileUsage{{Provider: tc.provider, ProfileName: "measured", Usage: info}}
			if got := profilesBelowThreshold(rows, 0.8); len(got) != 1 || got[0].ProfileName != "measured" {
				t.Fatal("routing discarded genuine measured capacity")
			}
		})
	}
}

func TestSubscriptionQuotaDecoderBoundsAndReadFailures(t *testing.T) {
	const valid = `{"five_hour":{"utilization":0}}`
	for _, extra := range []int{0, 1} {
		body := valid + strings.Repeat(" ", maxUsageResponseBytes-len(valid)+extra)
		reader := &quotaCountingReader{Reader: strings.NewReader(body)}
		var result claudeUsageResponse
		err := decodeUsageResponse(reader, &result)
		if (err != nil) != (extra == 1) {
			t.Fatalf("size-boundary result: extra=%d error=%v", extra, err)
		}
		if reader.count != len(body) {
			t.Fatal("decoder did not inspect the complete size boundary")
		}
	}
	reader := &quotaCountingReader{Reader: strings.NewReader(valid + strings.Repeat(" ", 2*maxUsageResponseBytes))}
	var result claudeUsageResponse
	if err := decodeUsageResponse(reader, &result); err == nil || reader.count != maxUsageResponseBytes+1 {
		t.Fatal("decoder consumed an unbounded response or accepted its prefix")
	}
	if err := decodeUsageResponse(quotaFailingReader{}, &result); err == nil || strings.Contains(err.Error(), "BODY-MUST-NOT-ESCAPE") {
		t.Fatal("failed response read was accepted or exposed reader details")
	}
}

type quotaCountingReader struct {
	io.Reader
	count int
}

func (r *quotaCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count += n
	return n, err
}

type quotaFailingReader struct{}

func (quotaFailingReader) Read(p []byte) (int, error) {
	n := copy(p, `{"five_hour":{"utilization":0}}`)
	return n, fmt.Errorf("BODY-MUST-NOT-ESCAPE")
}

func TestSubscriptionQuotaExhaustionAndHTTPFailuresRemainExcluded(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, status := range []int{200, 401, 403, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
					if provider == "claude" {
						fmt.Fprint(w, `{"five_hour":{"utilization":100}}`)
					} else {
						fmt.Fprint(w, `{"rate_limit":{"primary_window":{"used_percent":100}}}`)
					}
				}))
				defer server.Close()
				info, err := fetchSubscriptionQuotaFixture(provider, server.URL)
				if info == nil || (err == nil) != (status == 200) {
					t.Fatalf("HTTP failure or measured exhaustion misclassified: %v", err)
				}
				if info.NumericQuotaKnown() != (status == 200) {
					t.Fatal("exhausted measurement was confused with missing data")
				}
				rows := []ProfileUsage{{Provider: provider, ProfileName: "unusable", Usage: info}}
				if len(profilesBelowThreshold(rows, 1)) != 0 {
					t.Fatal("exhausted or failed account remained routable")
				}
			})
		}
	}
}

func TestCachedSubscriptionQuotaCannotInventZero(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, percent := range []string{"", `,"percent":null`, `,"percent":-1`, `,"percent":101`} {
		data := []byte(`{"cachedUsageUtilization":{"utilization":{"limits":[{"kind":"session"` + percent + `}]}}}`)
		info, err := ParseCachedClaudeUsage(data, now)
		if err == nil || info != nil {
			t.Fatal("cache parser manufactured zero usage from an invalid measurement")
		}
	}
	data := []byte(`{"cachedUsageUtilization":{"fetchedAtMs":1792580400000,"accountUuid":"seat","utilization":{"limits":[{"kind":"session","percent":0,"resets_at":"2099-01-01T00:00:00Z"},{"kind":"weekly_all","percent":100,"resets_at":"2020-01-01T00:00:00Z"}]}}}`)
	info, err := ParseCachedClaudeUsage(data, now)
	if err != nil || !info.NumericQuotaKnown() || info.PrimaryWindow.UsedPercent != 0 || info.PrimaryWindow.Rolled || !info.SecondaryWindow.Rolled || info.SecondaryWindow.UsedPercent != 0 {
		t.Fatalf("valid cached zero or recorded rollover changed: %+v %v", info, err)
	}
	if info.Source != SourceCache || info.AccountID != "seat" || info.FetchedAt.IsZero() {
		t.Fatal("cache identity, timestamp or provenance changed")
	}
}
