package codexd

import (
	"reflect"
	"testing"
)

func TestClassifyCodexDaemon(t *testing.T) {
	cases := []struct {
		name     string
		cmdline  string
		wantSub  string
		wantMtch bool
	}{
		// --- should match (persistent daemons) ---
		{"app-server bare", "codex app-server", "app-server", true},
		{"app-server abs path", "/usr/local/bin/codex app-server", "app-server", true},
		{"app-server NUL argv", "codex\x00app-server\x00", "app-server", true},
		{"app-server with global flag before sub", "codex --cd /work app-server", "app-server", true},
		{"app-server underscore variant", "codex app_server", "app-server", true},
		{"app-server with trailing flags", "codex app-server --port 1234", "app-server", true},
		{"mcp-server hyphen", "codex mcp-server", "mcp-server", true},
		{"mcp serve two-word", "codex mcp serve", "mcp-server", true},
		{"mcp server two-word", "codex mcp server", "mcp-server", true},
		{"proto stream", "codex proto", "proto", true},
		{"windows exe basename", `C:\tools\codex.exe app-server`, "app-server", true},

		// --- should NOT match ---
		{"interactive codex tui", "codex", "", false},
		{"codex resume session", "codex resume 1234", "", false},
		{"codex exec one-shot", "codex exec 'do a thing'", "", false},
		{"not codex at all", "node app-server.js", "", false},
		{"codex-ish other binary", "codexd app-server", "", false},
		{"empty", "", "", false},
		{"only flags", "codex --help", "", false},
		{"mcp without serve", "codex mcp list", "", false},
		{"app-server as an arg to another tool", "grep app-server file", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub, match := classifyCodexDaemon(tc.cmdline)
			if match != tc.wantMtch {
				t.Fatalf("match = %v, want %v (cmdline=%q)", match, tc.wantMtch, tc.cmdline)
			}
			if match && sub != tc.wantSub {
				t.Fatalf("subcommand = %q, want %q (cmdline=%q)", sub, tc.wantSub, tc.cmdline)
			}
		})
	}
}

func TestExecIsCodex(t *testing.T) {
	cases := map[string]bool{
		"codex":                   true,
		"/usr/local/bin/codex":    true,
		"CODEX":                   true,
		`C:\tools\codex.exe`:      true,
		"./codex":                 true,
		"codexd":                  false,
		"my-codex":                false,
		"node":                    false,
		"":                        false,
		"/opt/homebrew/bin/codex": true,
	}
	for in, want := range cases {
		if got := execIsCodex(in); got != want {
			t.Errorf("execIsCodex(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestDetect_DeduplicatesAndClassifies drives Detect() through an injected
// scanner so the platform-independent aggregation logic is covered everywhere.
func TestDetect_DeduplicatesAndClassifies(t *testing.T) {
	orig := scanProcesses
	t.Cleanup(func() { scanProcesses = orig })

	scanProcesses = func() ([]rawProc, bool) {
		return []rawProc{
			{pid: 200, cmdline: "codex app-server"},
			{pid: 100, cmdline: "codex mcp serve"},
			{pid: 100, cmdline: "codex mcp serve"}, // duplicate pid -> collapsed
			{pid: 300, cmdline: "codex"},           // interactive -> ignored
			{pid: 400, cmdline: "vim notes.txt"},   // unrelated -> ignored
		}, true
	}

	procs, supported := Detect()
	if !supported {
		t.Fatal("expected supported=true with injected scanner")
	}
	if len(procs) != 2 {
		t.Fatalf("expected 2 daemons, got %d: %+v", len(procs), procs)
	}
	// Sorted by PID ascending.
	gotPIDs := []int{procs[0].PID, procs[1].PID}
	if !reflect.DeepEqual(gotPIDs, []int{100, 200}) {
		t.Fatalf("pids = %v, want [100 200]", gotPIDs)
	}
	if procs[0].Subcommand != "mcp-server" || procs[1].Subcommand != "app-server" {
		t.Fatalf("subcommands = %q,%q", procs[0].Subcommand, procs[1].Subcommand)
	}
}

func TestDetect_Unsupported(t *testing.T) {
	orig := scanProcesses
	t.Cleanup(func() { scanProcesses = orig })

	scanProcesses = nil
	if procs, supported := Detect(); supported || procs != nil {
		t.Fatalf("expected (nil, false) when scanner is nil, got (%v, %v)", procs, supported)
	}

	scanProcesses = func() ([]rawProc, bool) { return nil, false }
	if procs, supported := Detect(); supported || procs != nil {
		t.Fatalf("expected (nil, false) when scan reports unsupported, got (%v, %v)", procs, supported)
	}
}
