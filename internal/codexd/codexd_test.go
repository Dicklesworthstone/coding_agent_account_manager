package codexd

import (
	"path/filepath"
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

// TestEffectiveCodexHome covers the CODEX_HOME attribution used to scope a
// shallow-spawn reload (issue #47), including the HOME/.codex fallback and the
// fail-safe "unknown environment" case.
func TestEffectiveCodexHome(t *testing.T) {
	cases := []struct {
		name    string
		proc    Process
		wantDir string
		wantOK  bool
	}{
		{"explicit codex_home", Process{EnvKnown: true, CodexHome: "/orch/cod-a/.codex"}, "/orch/cod-a/.codex", true},
		{"home fallback", Process{EnvKnown: true, Home: "/orch/cod-b"}, filepath.Clean("/orch/cod-b/.codex"), true},
		{"codex_home wins over home", Process{EnvKnown: true, CodexHome: "/x/.codex", Home: "/y"}, "/x/.codex", true},
		{"uncleaned path is cleaned", Process{EnvKnown: true, CodexHome: "/x/./sub/../.codex"}, filepath.Clean("/x/.codex"), true},
		{"env unknown fails closed", Process{EnvKnown: false, CodexHome: "/x/.codex"}, "", false},
		{"env known but empty", Process{EnvKnown: true}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.proc.EffectiveCodexHome()
			if ok != tc.wantOK || got != tc.wantDir {
				t.Fatalf("EffectiveCodexHome() = (%q, %v), want (%q, %v)", got, ok, tc.wantDir, tc.wantOK)
			}
		})
	}
}

// TestFilterByCodexHome is the core #47 property: reloading one shallow codex
// profile must NOT match the daemons of a concurrent profile, and daemons whose
// environment cannot be inspected are left alone (fail-safe).
func TestFilterByCodexHome(t *testing.T) {
	codA := "/orch/cod-a/.codex"
	codB := "/orch/cod-b/.codex"
	procs := []Process{
		{PID: 10, Subcommand: "app-server", EnvKnown: true, CodexHome: codA, ShallowProfile: "cod-a"},
		{PID: 20, Subcommand: "mcp-server", EnvKnown: true, CodexHome: codB, ShallowProfile: "cod-b"},
		{PID: 30, Subcommand: "app-server", EnvKnown: true, Home: "/orch/cod-a"},       // HOME/.codex fallback -> cod-a
		{PID: 40, Subcommand: "proto", EnvKnown: false, CodexHome: codA},               // unknown env -> excluded even though it looks like cod-a
		{PID: 50, Subcommand: "app-server", EnvKnown: true, CodexHome: "/real/.codex"}, // unrelated global daemon
	}

	gotA := FilterByCodexHome(procs, codA)
	if pids := pidsOf(gotA); !reflect.DeepEqual(pids, []int{10, 30}) {
		t.Fatalf("cod-a reload matched pids %v, want [10 30] (must not touch cod-b or unattributable daemons)", pids)
	}

	gotB := FilterByCodexHome(procs, codB)
	if pids := pidsOf(gotB); !reflect.DeepEqual(pids, []int{20}) {
		t.Fatalf("cod-b reload matched pids %v, want [20]", pids)
	}

	// Empty scope == host-wide (activate/next): everything passes through.
	if got := FilterByCodexHome(procs, ""); len(got) != len(procs) {
		t.Fatalf("empty scope must be host-wide: got %d, want %d", len(got), len(procs))
	}
}

func pidsOf(procs []Process) []int {
	out := make([]int, 0, len(procs))
	for _, p := range procs {
		out = append(out, p.PID)
	}
	return out
}

// TestDetect_PopulatesEnviron verifies Detect wires the per-platform environ
// reader into the classified daemon's attribution fields (issue #47).
func TestDetect_PopulatesEnviron(t *testing.T) {
	origScan, origEnv := scanProcesses, readProcEnviron
	t.Cleanup(func() { scanProcesses, readProcEnviron = origScan, origEnv })

	scanProcesses = func() ([]rawProc, bool) {
		return []rawProc{{pid: 77, cmdline: "codex app-server"}}, true
	}
	var inspected []int
	readProcEnviron = func(pid int) (map[string]string, bool) {
		inspected = append(inspected, pid)
		return map[string]string{
			"CODEX_HOME":      "/orch/cod-a/.codex",
			"HOME":            "/orch/cod-a",
			"SHALLOW_PROFILE": "cod-a",
		}, true
	}

	procs, supported := Detect()
	if !supported || len(procs) != 1 {
		t.Fatalf("Detect() = (%+v, %v), want one daemon", procs, supported)
	}
	// The environ must be read ONLY for the classified daemon.
	if !reflect.DeepEqual(inspected, []int{77}) {
		t.Fatalf("environ inspected for %v, want only [77]", inspected)
	}
	p := procs[0]
	if !p.EnvKnown || p.CodexHome != "/orch/cod-a/.codex" || p.Home != "/orch/cod-a" || p.ShallowProfile != "cod-a" {
		t.Fatalf("attribution not populated: %+v", p)
	}
}

// TestDetect_EnvironOnlyForDaemons proves the environ is NOT read for processes
// that fail Codex-daemon classification (issue #47: inspect only after match).
func TestDetect_EnvironOnlyForDaemons(t *testing.T) {
	origScan, origEnv := scanProcesses, readProcEnviron
	t.Cleanup(func() { scanProcesses, readProcEnviron = origScan, origEnv })

	scanProcesses = func() ([]rawProc, bool) {
		return []rawProc{
			{pid: 1, cmdline: "codex app-server"}, // daemon -> inspected
			{pid: 2, cmdline: "codex"},            // interactive -> ignored
			{pid: 3, cmdline: "vim notes.txt"},    // unrelated -> ignored
		}, true
	}
	var inspected []int
	readProcEnviron = func(pid int) (map[string]string, bool) {
		inspected = append(inspected, pid)
		return nil, false
	}

	if _, supported := Detect(); !supported {
		t.Fatal("expected supported=true")
	}
	if !reflect.DeepEqual(inspected, []int{1}) {
		t.Fatalf("environ inspected for %v, want only the daemon [1]", inspected)
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
