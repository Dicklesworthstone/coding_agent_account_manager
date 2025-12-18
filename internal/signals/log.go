package signals

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func DefaultLogFilePath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "caam.log")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".caam", "caam.log")
	}
	return filepath.Join(homeDir, ".caam", "caam.log")
}

func AppendLogLine(path, line string) error {
	if path == "" {
		path = DefaultLogFilePath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(f, "%s %s\n", ts, line); err != nil {
		return fmt.Errorf("write log line: %w", err)
	}
	return nil
}
