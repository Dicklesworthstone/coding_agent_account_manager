package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

// robotHistoryResponse mirrors the RobotOutput wrapper for the history command
// so tests can assert on the decoded JSON payload.
type robotHistoryResponse struct {
	Success bool             `json:"success"`
	Command string           `json:"command"`
	Data    RobotHistoryData `json:"data"`
	Error   *RobotError      `json:"error"`
}

func newRobotHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Int("days", 7, "")
	cmd.Flags().Int("limit", 50, "")
	cmd.Flags().String("provider", "", "")
	return cmd
}

// TestRobotHistory_ReturnsLoggedEvents is a regression test for issue #51:
// `caam robot history` returned count:0/events:[] even when activity_log had
// rows, because its query referenced a nonexistent `notes` column. The fix
// mirrors the working `caam history` column set (details, not notes).
func TestRobotHistory_ReturnsLoggedEvents(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}

	// Insert several activity_log rows across providers/profiles.
	events := []caamdb.Event{
		{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"},
		{Type: caamdb.EventRefresh, Provider: "claude", ProfileName: "personal"},
		{Type: caamdb.EventActivate, Provider: "claude", ProfileName: "personal",
			Details: map[string]any{"reason": "rotation"}},
	}
	for _, ev := range events {
		if err := db.LogEvent(ev); err != nil {
			t.Fatalf("LogEvent(%+v) error = %v", ev, err)
		}
	}
	db.Close()

	cmd := newRobotHistoryCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runRobotHistory(cmd, []string{}); err != nil {
		t.Fatalf("runRobotHistory() error = %v", err)
	}

	var resp robotHistoryResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode robot history output: %v\noutput: %s", err, buf.String())
	}

	if resp.Error != nil {
		t.Fatalf("robot history returned error: %+v", resp.Error)
	}
	if !resp.Success {
		t.Fatalf("robot history success=false, output: %s", buf.String())
	}

	// The core regression assertion: the real events are returned, not empty.
	if resp.Data.Count != len(events) {
		t.Fatalf("robot history count = %d, want %d (events: %+v)",
			resp.Data.Count, len(events), resp.Data.Events)
	}
	if len(resp.Data.Events) != len(events) {
		t.Fatalf("robot history returned %d events, want %d",
			len(resp.Data.Events), len(events))
	}

	// The activity-log detail context surfaces in the Notes field (sourced
	// from the `details` column, since there is no `notes` column).
	var foundRotation bool
	for _, e := range resp.Data.Events {
		if e.Provider == "" || e.Profile == "" || e.Event == "" {
			t.Errorf("event missing required fields: %+v", e)
		}
		if e.Notes != "" && strings.Contains(e.Notes, "rotation") {
			foundRotation = true
		}
	}
	if !foundRotation {
		t.Errorf("expected the details/notes context to surface in output, events: %+v", resp.Data.Events)
	}
}

// TestRobotHistory_ConsistentWithNonRobotHistory asserts the robot and
// non-robot history paths agree on event counts for the same DB, so a future
// schema drift that breaks one but not the other is caught.
func TestRobotHistory_ConsistentWithNonRobotHistory(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	inserted := []caamdb.Event{
		{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "work"},
		{Type: caamdb.EventActivate, Provider: "gemini", ProfileName: "main"},
		{Type: caamdb.EventError, Provider: "claude", ProfileName: "personal"},
	}
	for _, ev := range inserted {
		if err := db.LogEvent(ev); err != nil {
			t.Fatalf("LogEvent error = %v", err)
		}
	}
	db.Close()

	// Robot path count.
	robotCmd := newRobotHistoryCmd()
	var robotBuf bytes.Buffer
	robotCmd.SetOut(&robotBuf)
	if err := runRobotHistory(robotCmd, []string{}); err != nil {
		t.Fatalf("runRobotHistory() error = %v", err)
	}
	var robotResp robotHistoryResponse
	if err := json.Unmarshal(robotBuf.Bytes(), &robotResp); err != nil {
		t.Fatalf("decode robot history: %v", err)
	}

	// Non-robot path count (JSON mode).
	nonRobotCmd := &cobra.Command{}
	nonRobotCmd.Flags().IntP("limit", "n", 100, "")
	nonRobotCmd.Flags().String("provider", "", "")
	nonRobotCmd.Flags().String("profile", "", "")
	nonRobotCmd.Flags().String("type", "", "")
	nonRobotCmd.Flags().String("since", "", "")
	nonRobotCmd.Flags().Bool("json", true, "")
	_ = nonRobotCmd.Flags().Set("json", "true")
	var nonRobotBuf bytes.Buffer
	nonRobotCmd.SetOut(&nonRobotBuf)
	if err := runHistory(nonRobotCmd, []string{}); err != nil {
		t.Fatalf("runHistory() error = %v", err)
	}
	var nonRobotResp historyOutput
	if err := json.Unmarshal(nonRobotBuf.Bytes(), &nonRobotResp); err != nil {
		t.Fatalf("decode non-robot history: %v\noutput: %s", err, nonRobotBuf.String())
	}

	if robotResp.Data.Count != len(inserted) {
		t.Fatalf("robot count = %d, want %d", robotResp.Data.Count, len(inserted))
	}
	if robotResp.Data.Count != nonRobotResp.Count {
		t.Fatalf("robot count (%d) != non-robot count (%d) for identical DB",
			robotResp.Data.Count, nonRobotResp.Count)
	}
}
