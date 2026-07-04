package workflows

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDaemonSignals(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize environment")
	rootDir := h.TempDir
	pidFile := filepath.Join(rootDir, "caam-daemon.pid")

	// Create config file with initial settings
	configDir := filepath.Join(rootDir, "caam")
	require.NoError(t, os.MkdirAll(configDir, 0755))
	configPath := filepath.Join(configDir, "config.yaml")

	initialConfig := fmt.Sprintf(`
runtime:
  reload_on_sighup: true
  pid_file: true
  pid_file_path: %s
daemon:
  verbose: false
`, pidFile)
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0600))

	env := os.Environ()
	env = append(env, "GO_WANT_DAEMON_HELPER=1")
	env = append(env, fmt.Sprintf("XDG_CONFIG_HOME=%s", rootDir))
	// Critical: Set CAAM_HOME so LoadSPMConfig finds the isolated config.yaml
	env = append(env, fmt.Sprintf("CAAM_HOME=%s", configDir))
	// We need to capture logs to verify reload
	logPath := filepath.Join(rootDir, "daemon.log")
	// Daemon helper doesn't use config for log path, it uses args or default.
	// We passed --verbose in helper.

	// But we want to test reload. If we change config, does daemon pick it up?
	// Daemon loads global config in New().
	// Reload logic should re-load config.

	h.EndStep("Setup")

	// 2. Start Daemon
	h.StartStep("Start", "Start daemon process")

	exe, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.Command(exe, "-test.run=^TestDaemonHelper$")
	cmd.Env = env
	// Run the daemon as the leader of its OWN process group so teardown can
	// signal the ENTIRE tree — the daemon AND any grandchild it (or a tool it
	// drives, e.g. a Codex plugin-clone helper) may spawn — not just the direct
	// child. Reaping only the direct child left grandchildren writing files
	// under the temp home, racing t.TempDir() RemoveAll and failing cleanup with
	// "directory not empty" (issue #48).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Redirect stdout/stderr to a file we can read
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Start()
	require.NoError(t, err)

	// stopDaemon deterministically tears the daemon's whole process group down.
	// It is idempotent (sync.Once) and registered as a t.Cleanup so it runs even
	// if the test fails early (e.g. the PID file never appears) — and, crucially,
	// BEFORE the harness's t.TempDir() RemoveAll (registered first, so it runs
	// last). After this returns, no process rooted under the temp home is alive
	// to keep writing during cleanup.
	var stopOnce sync.Once
	pgid := cmd.Process.Pid // == process-group id, since the child is the group leader
	stopDaemon := func() {
		stopOnce.Do(func() {
			// Graceful first: SIGTERM the whole group.
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
			// Brief grace for a clean shutdown, then hard-kill the group as a
			// backstop. SIGKILL is sent while the leader is still unreaped so the
			// group id cannot have been recycled onto an unrelated process.
			time.Sleep(200 * time.Millisecond)
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			// Reap the direct child so it does not linger as a zombie.
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stopDaemon)

	// Wait for PID file
	pidFound := false
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(pidFile); err == nil {
			pidFound = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !pidFound {
		logs, _ := os.ReadFile(logPath)
		h.LogInfo("Daemon startup failed", "logs", string(logs))
		t.FailNow()
	}

	content, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(content)))

	// Relaxed check: verify process exists
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	// Check if process exists by sending signal 0
	require.NoError(t, proc.Signal(syscall.Signal(0)), "PID from file should be running")

	h.LogInfo("PID check", "cmd_pid", cmd.Process.Pid, "file_pid", pid)

	h.EndStep("Start")

	// 3. Reload (SIGHUP)
	h.StartStep("Reload", "Send SIGHUP and verify")

	// Change config
	newConfig := `
runtime:
  reload_on_sighup: true
daemon:
  verbose: true
`
	require.NoError(t, os.WriteFile(configPath, []byte(newConfig), 0600))

	// Send SIGHUP
	err = cmd.Process.Signal(syscall.SIGHUP)
	require.NoError(t, err)

	// Wait for reload (check log)
	// If SIGHUP is not handled, process might exit (default action) or ignore.
	// If handled, it should log "Reloading config..." or similar.

	time.Sleep(1 * time.Second)

	// Check if process is still running
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("Daemon died after SIGHUP (exit code?): %v", err)
	}

	// Read log
	logs, err := os.ReadFile(logPath)
	require.NoError(t, err)
	h.LogInfo("Daemon logs", "content", string(logs))

	// Assertions on log content would depend on implementation.
	// For now just verify it didn't die.

	h.EndStep("Reload")

	// 4. Stop
	h.StartStep("Stop", "Send SIGTERM")
	// Tear down the daemon and its ENTIRE process group, waiting for exit, before
	// the harness/TempDir cleanup runs (issue #48). Idempotent with the t.Cleanup.
	stopDaemon()
	logFile.Close()
	h.EndStep("Stop")
}
