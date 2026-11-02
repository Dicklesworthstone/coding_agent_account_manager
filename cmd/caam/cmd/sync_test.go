package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/spf13/cobra"
)

// Test helper functions that are exported

type fixtureSyncRunner struct {
	results          []*sync.SyncResult
	failure          error
	progress         sync.ProgressFunc
	closed, fullSync bool
}

func (f *fixtureSyncRunner) SyncWithMachineFiltered(_ context.Context, m *sync.Machine, _, _ string) ([]*sync.SyncResult, error) {
	for _, result := range f.results {
		result.Operation.Machine = m
		f.progress(sync.ProgressEvent{Stage: sync.StageResult, Machine: m, Result: result})
	}
	return f.results, f.failure
}
func (f *fixtureSyncRunner) RecordFullSync() { f.fullSync = true }
func (f *fixtureSyncRunner) Close() error    { f.closed = true; return nil }

func TestRunSyncReportsRecoveryAndReturnsFailure(t *testing.T) {
	for _, test := range []struct {
		name                       string
		direction                  sync.SyncDirection
		json                       bool
		profileError, machineError bool
		recovery                   bool
	}{
		{"push text", sync.SyncPush, false, false, false, true},
		{"pull json", sync.SyncPull, true, false, false, true},
		{"publication failure", sync.SyncPush, false, true, false, true},
		{"unsupported peer", sync.SyncSkip, true, true, false, false},
		{"partial machine failure", sync.SyncPull, true, false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			state := sync.NewSyncState("")
			if err := state.Pool.AddMachine(sync.NewMachine("fixture", "localhost")); err != nil {
				t.Fatal(err)
			}
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			result := &sync.SyncResult{Operation: &sync.SyncOperation{Provider: "claude", Profile: "work", Direction: test.direction}, Success: !test.profileError}
			if test.profileError {
				result.Error = errors.New("safe sync peer rejected publication")
			}
			if test.recovery {
				result.RecoveryPath = "/private/retained/profile"
			}
			runner := &fixtureSyncRunner{results: []*sync.SyncResult{result}}
			if test.machineError {
				runner.failure = errors.New("machine disconnected after partial transfer")
			}
			command := &cobra.Command{}
			command.Flags().Bool("json", test.json, "")
			command.Flags().Bool("dry-run", false, "")
			command.Flags().String("machine", "", "")
			command.Flags().String("provider", "", "")
			command.Flags().String("profile", "", "")
			var output bytes.Buffer
			command.SetOut(&output)
			err := runSyncWithRunner(command, nil, func(progress sync.ProgressFunc) (syncCommandRunner, error) {
				runner.progress = progress
				return runner, nil
			})
			wantError := test.profileError || test.machineError
			if (err != nil) != wantError {
				t.Fatalf("sync error=%v, want failure=%v", err, wantError)
			}
			if !runner.closed || runner.fullSync == wantError {
				t.Fatal("runner close/full-sync bookkeeping is incorrect")
			}
			location := "local"
			if test.direction == sync.SyncPush {
				location = "remote"
			}
			if test.json {
				var report struct {
					Results []struct {
						RecoveryPath     string `json:"recovery_path"`
						RecoveryLocation string `json:"recovery_location"`
					}
					MachineErrors []any `json:"machine_errors"`
				}
				if err := json.Unmarshal(output.Bytes(), &report); err != nil {
					t.Fatalf("stdout is not one JSON document: %v", err)
				}
				if len(report.Results) != 1 {
					t.Fatal("partial result was discarded")
				}
				if test.recovery && (report.Results[0].RecoveryPath != result.RecoveryPath || report.Results[0].RecoveryLocation != location) {
					t.Fatal("JSON lost recovery path or location")
				}
				if test.machineError && len(report.MachineErrors) != 1 {
					t.Fatal("JSON lost machine failure")
				}
			} else if test.recovery && !strings.Contains(output.String(), "Previous profile preserved ("+location+"): "+result.RecoveryPath) {
				t.Fatal("text output lost recovery")
			}
		})
	}
}

func TestGetStatusIcon(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{sync.StatusOnline, "🟢"},
		{sync.StatusOffline, "🔴"},
		{sync.StatusSyncing, "🔄"},
		{sync.StatusError, "⚠️"},
		{sync.StatusUnknown, "⚪"},
		{"other", "⚪"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := getStatusIcon(tt.status)
			if got != tt.expected {
				t.Errorf("getStatusIcon(%q) = %q, want %q", tt.status, got, tt.expected)
			}
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	tests := []struct {
		name     string
		ago      time.Duration
		expected string
	}{
		{"just now", 30 * time.Second, "just now"},
		{"1 minute", 1 * time.Minute, "1 min ago"},
		{"5 minutes", 5 * time.Minute, "5 mins ago"},
		{"1 hour", 1 * time.Hour, "1 hour ago"},
		{"3 hours", 3 * time.Hour, "3 hours ago"},
		{"1 day", 25 * time.Hour, "1 day ago"},
		{"3 days", 72 * time.Hour, "3 days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create time that was tt.ago in the past
			tm := time.Now().Add(-tt.ago)
			got := formatTimeAgo(tm)
			if got != tt.expected {
				t.Errorf("formatTimeAgo() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestSyncCommands tests that all sync subcommands are registered.
func TestSyncCommands(t *testing.T) {
	subcommands := []string{
		"init",
		"status",
		"add",
		"remove",
		"test",
		"enable",
		"disable",
		"log",
		"discover",
		"queue",
		"edit",
	}

	for _, name := range subcommands {
		t.Run(name, func(t *testing.T) {
			found := false
			for _, cmd := range syncCmd.Commands() {
				if cmd.Name() == name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("subcommand %q not found", name)
			}
		})
	}
}

// TestSyncCmdFlags tests that all expected flags are present.
func TestSyncCmdFlags(t *testing.T) {
	flags := []string{
		"machine",
		"provider",
		"profile",
		"dry-run",
		"force",
		"json",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			if syncCmd.Flags().Lookup(flag) == nil {
				t.Errorf("flag --%s not found", flag)
			}
		})
	}
}

// TestSyncInitCmdFlags tests sync init command flags.
func TestSyncInitCmdFlags(t *testing.T) {
	flags := []string{
		"discover",
		"csv",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			if syncInitCmd.Flags().Lookup(flag) == nil {
				t.Errorf("flag --%s not found", flag)
			}
		})
	}
}

// TestSyncAddCmdFlags tests sync add command flags.
func TestSyncAddCmdFlags(t *testing.T) {
	flags := []string{
		"key",
		"user",
		"remote-path",
		"test",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			if syncAddCmd.Flags().Lookup(flag) == nil {
				t.Errorf("flag --%s not found", flag)
			}
		})
	}
}

// TestSyncLogCmdFlags tests sync log command flags.
func TestSyncLogCmdFlags(t *testing.T) {
	flags := []string{
		"limit",
		"machine",
		"provider",
		"errors",
		"json",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			if syncLogCmd.Flags().Lookup(flag) == nil {
				t.Errorf("flag --%s not found", flag)
			}
		})
	}
}

// TestSyncQueueCmdFlags tests sync queue command flags.
func TestSyncQueueCmdFlags(t *testing.T) {
	flags := []string{
		"clear",
		"process",
		"json",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			if syncQueueCmd.Flags().Lookup(flag) == nil {
				t.Errorf("flag --%s not found", flag)
			}
		})
	}
}

// TestSyncStatusJSONOutput tests the JSON output helper.
func TestSyncStatusJSONOutput(t *testing.T) {
	state := sync.NewSyncState(t.TempDir())

	var buf bytes.Buffer
	err := runSyncStatusJSON(state, &buf)
	if err != nil {
		t.Fatalf("runSyncStatusJSON: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "auto_sync") {
		t.Errorf("JSON output missing auto_sync field")
	}
	if !strings.Contains(output, "machines") {
		t.Errorf("JSON output missing machines field")
	}
}

func TestRemoteVaultPath(t *testing.T) {
	defaultPath := sync.DefaultSyncerConfig().RemoteVaultPath

	if got := remoteVaultPath(nil); got != defaultPath {
		t.Fatalf("remoteVaultPath(nil) = %q, want %q", got, defaultPath)
	}

	if got := remoteVaultPath(&sync.Machine{}); got != defaultPath {
		t.Fatalf("remoteVaultPath(empty) = %q, want %q", got, defaultPath)
	}

	custom := &sync.Machine{RemotePath: "/data/caam"}
	if got := remoteVaultPath(custom); got != "/data/caam/vault" {
		t.Fatalf("remoteVaultPath(custom) = %q, want %q", got, "/data/caam/vault")
	}
}
