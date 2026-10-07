package authfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func backupSourceWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeBackupSourcePreservesCompleteCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	const credential = "{\n  \"access_token\": \"synthetic-access\", \"refresh_token\": \"synthetic-refresh\", \"future\": 9007199254740993\n}\n"
	backupSourceWrite(t, path, credential)
	got, err := readClaudeBackupSource(path, nil)
	if err != nil || string(got) != credential {
		t.Fatalf("raw credential not captured losslessly: %q, %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, got) {
		t.Fatalf("source was modified: %q, %v", after, err)
	}
}

func TestClaudeBackupSourceRecordsAbsentCaches(t *testing.T) {
	for name, document := range map[string]string{
		"missing file": "",
		"policy only":  `{"theme":"live","windowBounds":{"x":9007199254740993}}`,
		"empty cache":  `{"oauth:tokenCache":"","oauth:tokenCacheV2":"  "}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if document != "" {
				backupSourceWrite(t, path, document)
			}
			got, err := readClaudeBackupSource(path, []string{"oauth:tokenCache", "oauth:tokenCacheV2"})
			if err != nil || got != nil {
				t.Fatalf("missing auth must retire the old snapshot: %q, %v", got, err)
			}
			if document != "" {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != document {
					t.Fatal("capturing absence changed Desktop preferences")
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if got, err := readClaudeBackupSource(path, nil); err != nil || got != nil {
		t.Fatalf("absent raw credential not recorded: %q, %v", got, err)
	}
}

func TestClaudeBackupSourceSelectsOnlyNamedAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	const document = `{"oauth:tokenCache":"synthetic-v1","oauth:tokenCacheV2":"synthetic-v2","theme":"private-theme","oauth:other":"not-a-supported-cache","futureCounter":9007199254740993}`
	backupSourceWrite(t, path, document)
	got, err := readClaudeBackupSource(path, []string{"oauth:tokenCache", "oauth:tokenCacheV2"})
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]string
	if err := json.Unmarshal(got, &obj); err != nil || len(obj) != 2 || obj["oauth:tokenCache"] != "synthetic-v1" || obj["oauth:tokenCacheV2"] != "synthetic-v2" {
		t.Fatalf("Desktop auth projection copied policy or lost a token: %q, %v", got, err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != document {
		t.Fatal("Desktop source was changed")
	}
}

func TestClaudeBackupSourceRejectsCorruptionRatherThanInferringAbsence(t *testing.T) {
	for _, document := range []string{"", "null", "[]", "{broken", `{"oauth:tokenCache":null}`, `{"oauth:tokenCacheV2":{"secret":"synthetic"}}`} {
		t.Run(document, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			backupSourceWrite(t, path, document)
			_, err := readClaudeBackupSource(path, []string{"oauth:tokenCache", "oauth:tokenCacheV2"})
			if err == nil {
				t.Fatal("corruption was mistaken for a missing credential")
			}
			if strings.Contains(err.Error(), "synthetic") {
				t.Fatal("error exposed a credential value")
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != document {
				t.Fatal("invalid source was modified")
			}
		})
	}
}

func TestClaudeBackupSourceDistinguishesNativeLinksFromMissingSources(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated Windows privileges")
	}
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeBackupSource(link, nil); err == nil {
		t.Fatal("dangling native link was treated as absence")
	}
	const credential = `{"access_token":"synthetic"}`
	backupSourceWrite(t, target, credential)
	if got, err := readClaudeBackupSource(link, nil); err != nil || string(got) != credential {
		t.Fatalf("valid native link was not captured: %q, %v", got, err)
	}
	if _, err := readClaudeBackupSource(dir, nil); err == nil {
		t.Fatal("directory was treated as a credential source")
	}
}
