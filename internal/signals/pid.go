package signals

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func DefaultPIDFilePath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "caam.pid")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".caam", "caam.pid")
	}
	return filepath.Join(homeDir, ".caam", "caam.pid")
}

func WritePIDFile(path string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid: %d", pid)
	}
	if path == "" {
		path = DefaultPIDFilePath()
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create pid dir: %w", err)
	}

	if existingPID, err := ReadPIDFile(path); err == nil {
		if existingPID != pid && isProcessAlive(existingPID) {
			return fmt.Errorf("pid file already points to running process (pid=%d)", existingPID)
		}
		_ = os.Remove(path) // stale or same pid; best-effort
	}

	data := []byte(strconv.Itoa(pid) + "\n")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp pid file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp pid file: %w", err)
	}

	return nil
}

func ReadPIDFile(path string) (int, error) {
	if path == "" {
		path = DefaultPIDFilePath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid: %d", pid)
	}
	return pid, nil
}

func RemovePIDFile(path string) error {
	if path == "" {
		path = DefaultPIDFilePath()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
