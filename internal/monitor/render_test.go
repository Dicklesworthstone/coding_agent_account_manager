package monitor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestTableRendererOutput(t *testing.T) {
	state := buildTestState(42)

	renderer := NewTableRenderer()
	renderer.Width = 60
	renderer.ShowEmoji = true

	out := renderer.Render(state)
	if !strings.Contains(out, "LIVE USAGE MONITOR") {
		t.Fatalf("table output missing header: %q", out)
	}
	if !strings.Contains(out, "CLAUDE") {
		t.Fatalf("table output missing provider: %q", out)
	}
	if !strings.Contains(out, "alice") {
		t.Fatalf("table output missing profile name: %q", out)
	}
}

func TestBriefRendererLength(t *testing.T) {
	state := &MonitorState{
		UpdatedAt: time.Now(),
		Profiles: map[string]*ProfileState{
			"claude/alice": buildProfile("claude", "alice", 42),
			"codex/bob":    buildProfile("codex", "bob", 67),
			"gemini/carl":  buildProfile("gemini", "carl", 12),
		},
	}

	renderer := NewBriefRenderer()
	out := renderer.Render(state)
	if len(out) > 80 {
		t.Fatalf("brief output too long: %d", len(out))
	}
	if !strings.Contains(out, "claude:") {
		t.Fatalf("brief output missing provider: %q", out)
	}
}

func TestJSONRendererValid(t *testing.T) {
	state := buildTestState(55)
	renderer := NewJSONRenderer(false)

	out := renderer.Render(state)
	if !json.Valid([]byte(out)) {
		t.Fatalf("json output invalid: %s", out)
	}

	var payload struct {
		Profiles []map[string]interface{} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}
	if len(payload.Profiles) != 1 {
		t.Fatalf("profiles length = %d, want 1", len(payload.Profiles))
	}
}

func TestAlertRendererDedupes(t *testing.T) {
	state := buildTestState(86)
	renderer := NewAlertRenderer(80)

	first := renderer.Render(state)
	if strings.TrimSpace(first) == "" {
		t.Fatal("expected initial alert output")
	}

	second := renderer.Render(state)
	if strings.TrimSpace(second) != "" {
		t.Fatalf("expected deduped alert output, got %q", second)
	}

	state.Profiles["claude/alice"].Usage.PrimaryWindow.UsedPercent = 96
	third := renderer.Render(state)
	if strings.TrimSpace(third) == "" {
		t.Fatal("expected alert output after escalation")
	}
}

func buildTestState(percent int) *MonitorState {
	return &MonitorState{
		UpdatedAt: time.Now(),
		Profiles: map[string]*ProfileState{
			"claude/alice": buildProfile("claude", "alice", percent),
		},
	}
}

func buildProfile(provider, name string, percent int) *ProfileState {
	return &ProfileState{
		Provider:    provider,
		ProfileName: name,
		Usage: &usage.UsageInfo{
			Provider:    provider,
			ProfileName: name,
			PrimaryWindow: &usage.UsageWindow{
				UsedPercent: percent,
			},
		},
		Health:     health.StatusHealthy,
		PoolStatus: authpool.PoolStatusReady,
	}
}

// TestUsageUnavailable verifies issue #37: a profile with an errored/empty usage
// fetch reports a human reason instead of looking like genuine 0% usage.
func TestUsageUnavailable(t *testing.T) {
	if r := usageUnavailable(nil); r != "no usage data" {
		t.Errorf("nil info: got %q, want %q", r, "no usage data")
	}

	cases := []struct {
		errMsg string
		want   string
	}{
		{"token expired or invalid", "no usage: auth expired (re-login)"},
		{"401 Unauthorized", "no usage: auth expired (re-login)"},
		{"missing access token", "no usage: not logged in"},
		{"usage not yet supported for opencode", "no usage: usage not supported"},
	}
	for _, tc := range cases {
		got := usageUnavailable(&usage.UsageInfo{Error: tc.errMsg})
		if got != tc.want {
			t.Errorf("usageUnavailable(Error=%q) = %q, want %q", tc.errMsg, got, tc.want)
		}
	}

	// Real data present -> no reason (show the number instead).
	withWindow := &usage.UsageInfo{PrimaryWindow: &usage.UsageWindow{UsedPercent: 17}}
	if r := usageUnavailable(withWindow); r != "" {
		t.Errorf("with window: got %q, want empty", r)
	}
	withCredits := &usage.UsageInfo{Credits: &usage.CreditInfo{HasCredits: true}}
	if r := usageUnavailable(withCredits); r != "" {
		t.Errorf("with credits: got %q, want empty", r)
	}
}

// TestTableRendererShowsReasonNotZeroPercent verifies that the live table shows
// the unavailability reason rather than a misleading 0% bar for a logged-in
// account whose usage fetch failed (issue #37).
func TestTableRendererShowsReasonNotZeroPercent(t *testing.T) {
	state := &MonitorState{
		UpdatedAt: time.Now(),
		Profiles: map[string]*ProfileState{
			"claude/erroracct": {
				Provider:    "claude",
				ProfileName: "erroracct",
				Usage:       &usage.UsageInfo{Provider: "claude", Error: "401 Unauthorized"},
				Health:      health.StatusHealthy,
				PoolStatus:  authpool.PoolStatusReady,
			},
		},
	}

	r := NewTableRenderer()
	r.Width = 75
	r.ShowEmoji = false
	out := r.Render(state)

	if !strings.Contains(out, "no usage: auth expired (re-login)") {
		t.Fatalf("expected unavailability reason in output, got:\n%s", out)
	}
	if strings.Contains(out, "  0%") {
		t.Fatalf("errored account must not render a 0%% bar; got:\n%s", out)
	}
}
