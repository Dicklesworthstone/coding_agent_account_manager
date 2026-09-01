package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// quotaTestNow is the reference clock for quota rendering tests.
var quotaTestNow = time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)

func quotaTestTime(t *testing.T, value string) *time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return &ts
}

// quotaTestRows returns one row of each kind the table has to render: a live
// active profile, a frozen vault snapshot, a shallow profile, and a profile
// that has never run a session.
func quotaTestRows(t *testing.T) []quotaRow {
	t.Helper()
	fetched := quotaTestNow.Add(-3 * time.Minute)
	stale := quotaTestNow.Add(-50 * time.Hour)

	return []quotaRow{
		{
			Profile:     "work",
			Email:       "n/a",
			Plan:        "Pro",
			Source:      quotaSourceLive,
			Active:      true,
			AccountUUID: "0552fa96-40f9-4b38-a33a-0d5ac585167d",
			FetchedAt:   &fetched,
			Windows: []usage.CachedWindow{
				{Kind: usage.CachedKindSession, Label: "5h", Percent: 53, ResetsAt: quotaTestTime(t, "2026-09-01T22:50:00Z")},
				{Kind: usage.CachedKindWeeklyAll, Label: "weekly", Percent: 11, ResetsAt: quotaTestTime(t, "2026-09-04T06:00:00Z")},
				{Kind: usage.CachedKindWeeklyScoped, Label: "Fable", Percent: 19, ResetsAt: quotaTestTime(t, "2026-09-04T06:00:00Z")},
			},
		},
		{
			Profile:     "personal",
			Email:       "n/a",
			Plan:        "Pro",
			Source:      quotaSourceSnapshot,
			AccountUUID: "9fc5c17d-a950-4bd0-b3c6-c1c9e7a9b452",
			FetchedAt:   &stale,
			Windows: []usage.CachedWindow{
				{Kind: usage.CachedKindSession, Label: "5h", Percent: 0, Rolled: true, ResetsAt: quotaTestTime(t, "2026-09-01T10:00:00Z")},
				{Kind: usage.CachedKindWeeklyAll, Label: "weekly", Percent: 88, ResetsAt: quotaTestTime(t, "2026-09-02T18:59:59Z")},
				{Kind: usage.CachedKindWeeklyScoped, Label: "Fable", Percent: 96},
			},
		},
		{
			Profile: "spare",
			Email:   "unknown",
			Plan:    "unknown",
			Source:  quotaSourceShallow,
			Windows: []usage.CachedWindow{},
		},
	}
}

func TestQuotaTableRendersEveryRowKind(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderQuotaTable(&buf, quotaTestRows(t), quotaTestNow, false))
	out := buf.String()

	assert.Contains(t, out, "PROFILE")
	assert.Contains(t, out, "EMAIL")
	assert.Contains(t, out, "PLAN")
	assert.Contains(t, out, "5H")
	assert.Contains(t, out, "WEEKLY")
	assert.Contains(t, out, "FABLE", "the model column is titled from the weekly_scoped scope")
	assert.Contains(t, out, "RESETS")
	assert.Contains(t, out, "AS OF")

	assert.Contains(t, out, "● work", "the active profile is marked")
	assert.Contains(t, out, "spare (shallow)", "shallow rows are tagged")
	assert.Contains(t, out, "no usage cache yet (run a session)")

	assert.Contains(t, out, "█", "percentages render as bars")
	assert.Contains(t, out, "░")
	assert.Contains(t, out, " 53%")
	assert.Contains(t, out, "~ 88%", "a snapshot row marks its percentages as second-hand")
	assert.NotContains(t, out, "~ 53%", "the live row is not marked as a snapshot")

	assert.Contains(t, out, "3m ago")
	assert.Contains(t, out, "2d ago")
	assert.Contains(t, out, "usage as cached by Claude Code")
	assert.Contains(t, out, "No network.")
}

func TestQuotaTableIsPlainWithoutColor(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderQuotaTable(&buf, quotaTestRows(t), quotaTestNow, false))
	assert.NotContains(t, buf.String(), "\x1b[", "no ANSI escapes when stdout is not a TTY")
}

func TestQuotaTableColorsBarsWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderQuotaTable(&buf, quotaTestRows(t), quotaTestNow, true))
	out := buf.String()
	assert.Contains(t, out, "█", "bars survive colorization")
	assert.Contains(t, out, "no usage cache yet (run a session)")
}

func TestQuotaTableColumnsStayAligned(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderQuotaTable(&buf, quotaTestRows(t), quotaTestNow, false))

	var dataLines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "● work") || strings.HasPrefix(line, "  personal") {
			dataLines = append(dataLines, line)
		}
	}
	require.Len(t, dataLines, 2)

	// Both rows carry full bar cells, so the "AS OF" column must start in the
	// same display column on each. Compare rune offsets: the active marker and
	// the bar blocks are multi-byte.
	first := utf8.RuneCountInString(dataLines[0][:strings.Index(dataLines[0], "3m ago")])
	second := utf8.RuneCountInString(dataLines[1][:strings.Index(dataLines[1], "2d ago")])
	assert.Equal(t, first, second, "bar columns must keep a fixed width")
}

func TestQuotaTableEmptyVault(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderQuotaTable(&buf, nil, quotaTestNow, false))
	assert.Contains(t, buf.String(), "No Claude profiles found.")
}

func TestQuotaJSONShape(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderQuotaJSON(&buf, quotaTestRows(t)))

	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.Len(t, decoded, 3)

	live := decoded[0]
	assert.Equal(t, "work", live["profile"])
	assert.Equal(t, "n/a", live["email"])
	assert.Equal(t, "Pro", live["plan"])
	assert.Equal(t, "live", live["source"])
	assert.Equal(t, true, live["active"])
	assert.Equal(t, "0552fa96-40f9-4b38-a33a-0d5ac585167d", live["account_uuid"])
	assert.NotNil(t, live["fetched_at"])

	windows, ok := live["windows"].([]any)
	require.True(t, ok)
	require.Len(t, windows, 3)
	session := windows[0].(map[string]any)
	assert.Equal(t, "session", session["kind"])
	assert.Equal(t, "5h", session["label"])
	assert.Equal(t, float64(53), session["percent"])
	assert.Equal(t, false, session["rolled"])
	assert.NotNil(t, session["resets_at"])

	snapshot := decoded[1]
	assert.Equal(t, "snapshot", snapshot["source"])
	assert.Equal(t, false, snapshot["active"])
	rolled := snapshot["windows"].([]any)[0].(map[string]any)
	assert.Equal(t, true, rolled["rolled"])
	assert.Equal(t, float64(0), rolled["percent"])

	missing := decoded[2]
	assert.Equal(t, "shallow", missing["source"])
	assert.Nil(t, missing["fetched_at"])
	assert.Empty(t, missing["windows"])
}

func TestQuotaUnknownProviderFails(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runQuota(cmd, []string{"codex"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no local usage cache for codex")
	assert.Equal(t, 1, ExitCode(err))
}

func TestCollectQuotaRowsReadsLiveSnapshotAndShallow(t *testing.T) {
	vaultDir := t.TempDir()
	homeDir := t.TempDir()
	shallowBase := filepath.Join(t.TempDir(), "orch-homes")

	writeQuotaCache(t, filepath.Join(homeDir, ".claude.json"), 1788292800000, "live-uuid", 53)
	writeQuotaCache(t, filepath.Join(vaultDir, "claude", "work", ".claude.json"), 1788292800000, "live-uuid", 99)
	writeQuotaCache(t, filepath.Join(vaultDir, "claude", "personal", ".claude.json"), 1788200000000, "snap-uuid", 12)
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "fresh"), 0o755))

	shallowHome := filepath.Join(shallowBase, "spare")
	writeQuotaCache(t, filepath.Join(shallowHome, ".claude.json"), 1788200000000, "shallow-uuid", 7)
	writeShallowMeta(t, shallowHome, "spare", "claude")
	mgr, err := shallow.NewManager(shallowBase, homeDir)
	require.NoError(t, err)

	rows, err := collectQuotaRows(quotaScan{
		vault:          authfile.NewVault(vaultDir),
		liveClaudeJSON: filepath.Join(homeDir, ".claude.json"),
		active:         "work",
		shallowMgr:     mgr,
		now:            quotaTestNow,
	})
	require.NoError(t, err)
	require.Len(t, rows, 4)

	byName := map[string]quotaRow{}
	for _, r := range rows {
		byName[r.Profile] = r
	}

	work := byName["work"]
	assert.Equal(t, quotaSourceLive, work.Source)
	assert.True(t, work.Active)
	assert.Equal(t, "live-uuid", work.AccountUUID)
	assert.Equal(t, 53, work.Windows[0].Percent, "the active profile reads the live file, not the frozen snapshot")

	personal := byName["personal"]
	assert.Equal(t, quotaSourceSnapshot, personal.Source)
	assert.False(t, personal.Active)
	assert.Equal(t, 12, personal.Windows[0].Percent)

	fresh := byName["fresh"]
	assert.Equal(t, quotaSourceSnapshot, fresh.Source)
	assert.Empty(t, fresh.Windows, "a profile with no cache yet has no windows")
	assert.Nil(t, fresh.FetchedAt)

	spare := byName["spare"]
	assert.Equal(t, quotaSourceShallow, spare.Source)
	assert.Equal(t, 7, spare.Windows[0].Percent)
}

func TestCollectQuotaRowsUnreadableVault(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-vault")
	rows, err := collectQuotaRows(quotaScan{
		vault:          authfile.NewVault(missing),
		liveClaudeJSON: filepath.Join(t.TempDir(), ".claude.json"),
		now:            quotaTestNow,
	})
	// An absent vault is empty, not broken: no profiles, no error.
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestFormatQuotaAge(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want string
	}{
		{0, "just now"},
		{45 * time.Second, "just now"},
		{3 * time.Minute, "3m ago"},
		{5 * time.Hour, "5h ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, formatQuotaAge(tc.age), "age %v", tc.age)
	}
}

func TestQuotaBarWidthAndFill(t *testing.T) {
	assert.Equal(t, "░░░░░░░░░░", quotaBar(0))
	assert.Equal(t, "██████████", quotaBar(100))
	assert.Equal(t, "█████░░░░░", quotaBar(53))
	assert.Equal(t, "██████████", quotaBar(150), "percentages are clamped")
}

func writeQuotaCache(t *testing.T, path string, fetchedAtMs int64, accountUUID string, sessionPercent int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	payload := map[string]any{
		"cachedUsageUtilization": map[string]any{
			"fetchedAtMs": fetchedAtMs,
			"accountUuid": accountUUID,
			"utilization": map[string]any{
				"limits": []map[string]any{
					{"kind": "session", "percent": sessionPercent, "resets_at": "2026-09-01T22:50:00+00:00"},
					{"kind": "weekly_all", "percent": 11, "resets_at": "2026-09-04T06:00:00+00:00"},
					{"kind": "weekly_scoped", "percent": 19, "resets_at": "2026-09-04T06:00:00+00:00",
						"scope": map[string]any{"model": map[string]any{"display_name": "Fable"}}},
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func writeShallowMeta(t *testing.T, home, name, provider string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(home, 0o755))
	meta := shallow.Meta{Name: name, Provider: provider, RealHome: filepath.Dir(home), Version: 1}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, shallow.ProfileMetaFilename), data, 0o600))
}

// TestColorizeQuotaLine_IdenticalCellsPaintedIndependently pins the bug where a
// second identical bar on the same row was matched inside the first, already
// painted, cell.
func TestColorizeQuotaLine_IdenticalCellsPaintedIndependently(t *testing.T) {
	zero := quotaBarCell{Text: "░░░░░░░░░░ ~  0%", Percent: 0}
	high := quotaBarCell{Text: "█████████░ ~ 90%", Percent: 90}
	line := "  acct  n/a  Pro  " + zero.Text + "  " + zero.Text + "  " + high.Text + "  Mon Jan 1 00:00  3h ago"

	got := colorizeQuotaLine(line, []quotaBarCell{zero, zero, high}, func(c quotaBarCell) string {
		return "<" + c.Text + ">"
	})

	want := "  acct  n/a  Pro  <" + zero.Text + ">  <" + zero.Text + ">  <" + high.Text + ">  Mon Jan 1 00:00  3h ago"
	if got != want {
		t.Fatalf("colorizeQuotaLine() =\n%s\nwant\n%s", got, want)
	}
}
