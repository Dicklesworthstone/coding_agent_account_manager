package usage

import (
	"fmt"
	"os"
	"path/filepath"
)

// NativeCredentialLocator validates the selected credential file and returns
// an opaque locator for a read-only native quota fetch. It never searches a
// different file or namespace and never returns credential contents.
func NativeCredentialLocator(provider, authPath string) (string, error) {
	switch provider {
	case "cursor":
		if !fileHasCursorAccessToken(authPath) {
			return "", fmt.Errorf("cursor access token is missing or unreadable")
		}
		return "cursor-root:" + authPath, nil
	case "grok":
		// GROK_HOME is the directory containing this exact auth.json.
		// Reject arbitrary filenames rather than silently reading a sibling.
		if filepath.Base(authPath) != "auth.json" {
			return "", fmt.Errorf("grok credential must be auth.json")
		}
		st, err := os.Stat(authPath)
		if err != nil || !st.Mode().IsRegular() {
			return "", fmt.Errorf("grok auth.json is missing or unreadable")
		}
		return "grok-home:" + filepath.Dir(authPath), nil
	default:
		return "", fmt.Errorf("native quota credential unsupported for %s", provider)
	}
}
