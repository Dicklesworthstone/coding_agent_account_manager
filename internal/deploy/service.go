package deploy

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// AgentServiceLabel names the local auth-agent service (launchd label and
// systemd unit name).
const AgentServiceLabel = "com.dicklesworthstone.caam.auth-agent"

const agentSystemdUnitName = "caam-auth-agent.service"

// CommandRunner runs a local command and returns its combined output.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runLocal(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// AgentService installs the local auth-agent as a per-user service that
// starts at login and restarts on failure: a launchd agent on macOS and a
// systemd user unit on Linux.
type AgentService struct {
	// GOOS selects the service manager ("darwin" or "linux").
	GOOS string
	// Home is the user's home directory.
	Home string
	// Executable is the caam binary the service runs.
	Executable string
	// ConfigPath is the agent config passed with --config.
	ConfigPath string
	// Run executes service-manager commands; defaults to running them locally.
	Run CommandRunner
	// UID is used for launchd domain targets; defaults to the current user.
	UID int
}

// NewAgentService returns an AgentService for the current platform and user.
func NewAgentService(configPath string) (*AgentService, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	exe, err := stableExecutable()
	if err != nil {
		return nil, err
	}
	return &AgentService{
		GOOS:       runtime.GOOS,
		Home:       home,
		Executable: exe,
		ConfigPath: configPath,
		Run:        runLocal,
		UID:        os.Getuid(),
	}, nil
}

// stableExecutable prefers the caam found on PATH when it is the running
// binary, so a package-manager symlink (which survives upgrades) is used
// instead of a versioned install path.
func stableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve caam executable: %w", err)
	}
	if onPath, err := exec.LookPath("caam"); err == nil {
		if abs, err := filepath.Abs(onPath); err == nil && sameFile(abs, exe) {
			return abs, nil
		}
	}
	return exe, nil
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// UnitPath returns where the service definition is written.
func (s *AgentService) UnitPath() (string, error) {
	switch s.GOOS {
	case "darwin":
		return filepath.Join(s.Home, "Library", "LaunchAgents", AgentServiceLabel+".plist"), nil
	case "linux":
		return filepath.Join(s.Home, ".config", "systemd", "user", agentSystemdUnitName), nil
	default:
		return "", fmt.Errorf("auth-agent service is not supported on %s; run 'caam auth-agent --config %s' instead", s.GOOS, s.ConfigPath)
	}
}

// LogPath returns where the service's output goes (launchd only; systemd
// output goes to the journal).
func (s *AgentService) LogPath() string {
	if s.GOOS == "darwin" {
		return filepath.Join(s.Home, "Library", "Logs", "caam-auth-agent.log")
	}
	return ""
}

func (s *AgentService) args() []string {
	return []string{s.Executable, "auth-agent", "--config", s.ConfigPath}
}

// Definition renders the service definition.
func (s *AgentService) Definition() (string, error) {
	if !filepath.IsAbs(s.Executable) || !filepath.IsAbs(s.ConfigPath) {
		return "", fmt.Errorf("service paths must be absolute: executable=%q config=%q", s.Executable, s.ConfigPath)
	}
	switch s.GOOS {
	case "darwin":
		return launchdPlist(AgentServiceLabel, s.args(), s.LogPath())
	case "linux":
		quoted := make([]string, 0, len(s.args()))
		for _, a := range s.args() {
			quoted = append(quoted, systemdQuote(a))
		}
		return GenerateSystemdUnit(SystemdUnitConfig{
			Type:      "Auth Agent",
			ExecStart: strings.Join(quoted, " "),
		})
	default:
		_, err := s.UnitPath()
		return "", err
	}
}

// Install writes the service definition and (re)starts the service. It is
// idempotent: an installed service is replaced and restarted.
func (s *AgentService) Install(ctx context.Context) (string, error) {
	path, err := s.UnitPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(s.ConfigPath); err != nil {
		return "", fmt.Errorf("agent config %s: %w (run 'caam setup distributed' first)", s.ConfigPath, err)
	}
	def, err := s.Definition()
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(def), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if logPath := s.LogPath(); logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
			return "", fmt.Errorf("create log dir: %w", err)
		}
	}

	switch s.GOOS {
	case "darwin":
		domain := "gui/" + strconv.Itoa(s.UID)
		// Unload a previous definition; failure just means it was not loaded.
		s.Run(ctx, "launchctl", "bootout", domain+"/"+AgentServiceLabel)
		if out, err := s.Run(ctx, "launchctl", "bootstrap", domain, path); err != nil {
			return path, fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(out)))
		}
		if out, err := s.Run(ctx, "launchctl", "enable", domain+"/"+AgentServiceLabel); err != nil {
			return path, fmt.Errorf("launchctl enable: %w: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		for _, args := range [][]string{
			{"--user", "daemon-reload"},
			{"--user", "enable", agentSystemdUnitName},
			{"--user", "restart", agentSystemdUnitName},
		} {
			if out, err := s.Run(ctx, "systemctl", args...); err != nil {
				return path, fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	return path, nil
}

// Uninstall stops the service and removes its definition. It succeeds when
// the service is not installed.
func (s *AgentService) Uninstall(ctx context.Context) (string, error) {
	path, err := s.UnitPath()
	if err != nil {
		return "", err
	}
	switch s.GOOS {
	case "darwin":
		s.Run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(s.UID)+"/"+AgentServiceLabel)
	case "linux":
		s.Run(ctx, "systemctl", "--user", "disable", "--now", agentSystemdUnitName)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return path, fmt.Errorf("remove %s: %w", path, err)
	}
	if s.GOOS == "linux" {
		s.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return path, nil
}

// AgentServiceStatus describes the installed service.
type AgentServiceStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	UnitPath  string `json:"unit_path"`
	LogPath   string `json:"log_path,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Status reports whether the service is installed and running.
func (s *AgentService) Status(ctx context.Context) (AgentServiceStatus, error) {
	path, err := s.UnitPath()
	if err != nil {
		return AgentServiceStatus{}, err
	}
	st := AgentServiceStatus{UnitPath: path, LogPath: s.LogPath()}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	}

	switch s.GOOS {
	case "darwin":
		out, err := s.Run(ctx, "launchctl", "print", "gui/"+strconv.Itoa(s.UID)+"/"+AgentServiceLabel)
		if err == nil {
			st.Running = strings.Contains(string(out), "state = running")
			for _, line := range strings.Split(string(out), "\n") {
				if t := strings.TrimSpace(line); strings.HasPrefix(t, "state = ") || strings.HasPrefix(t, "last exit code = ") {
					st.Detail = strings.TrimSpace(st.Detail + "; " + t)
				}
			}
			st.Detail = strings.TrimPrefix(st.Detail, "; ")
		}
	case "linux":
		out, _ := s.Run(ctx, "systemctl", "--user", "is-active", agentSystemdUnitName)
		state := strings.TrimSpace(string(out))
		st.Running = state == "active"
		st.Detail = state
	}
	return st, nil
}

// launchdPlist renders a launchd agent that runs args at login and keeps it
// alive, logging to logPath.
func launchdPlist(label string, args []string, logPath string) (string, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	writeKeyString := func(key, value string) error {
		b.WriteString("\t<key>" + key + "</key>\n\t<string>")
		if err := xml.EscapeText(&b, []byte(value)); err != nil {
			return err
		}
		b.WriteString("</string>\n")
		return nil
	}
	if err := writeKeyString("Label", label); err != nil {
		return "", err
	}
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args {
		b.WriteString("\t\t<string>")
		if err := xml.EscapeText(&b, []byte(a)); err != nil {
			return "", err
		}
		b.WriteString("</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	// Restart after crashes, but not after a clean exit.
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n")
	// The agent drives a visible Chrome window, so it must run in the GUI session.
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	if logPath != "" {
		if err := writeKeyString("StandardOutPath", logPath); err != nil {
			return "", err
		}
		if err := writeKeyString("StandardErrorPath", logPath); err != nil {
			return "", err
		}
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String(), nil
}

// writeFileAtomic writes data to path via a temp file and rename.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
