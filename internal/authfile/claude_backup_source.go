package authfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// readClaudeBackupSource captures a JSON credential or named cache fields.
// Nil means the source is genuinely absent (or has no selected cache fields),
// and instructs Backup to remove any obsolete copy from the selected profile.
// A dangling native link, nonregular file or malformed JSON is not absence.
// Existing native links to regular files remain supported and are never edited.
func readClaudeBackupSource(path string, fields []string) ([]byte, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("backup source must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return projectClaudeBackupSource(data, fields)
}

// projectClaudeBackupSource validates already captured bytes without rereading
// native files. Nil fields preserves a raw credential losslessly; named fields
// retain only nonblank cache strings, with no cache represented by a nil result.
func projectClaudeBackupSource(data []byte, fields []string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("backup source must contain a JSON object")
	}
	if fields == nil {
		return data, nil // The caller validates the provider's credential shape.
	}
	selected := make(map[string]json.RawMessage)
	for _, key := range fields {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var value string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("backup cache field %s must be a string", key)
		}
		if strings.TrimSpace(value) != "" {
			selected[key] = raw
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	return json.Marshal(selected)
}
