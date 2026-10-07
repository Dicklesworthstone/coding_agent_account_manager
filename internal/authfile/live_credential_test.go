package authfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

func TestReadLiveCredentialPrimaryAndBounds(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "auth.json")
	metadata := filepath.Join(root, "cli-config.json")
	files := AuthFileSet{Tool: "cursor", Files: []AuthFileSpec{{Path: metadata}, {Path: primary}}}
	meta := []byte(`{"authInfo":{"email":"synthetic@example.com"}}`)
	if err := os.WriteFile(metadata, meta, 0600); err != nil {
		t.Fatal(err)
	}
	name, data, err := ReadLiveCredential(files)
	if err != nil || name != "cli-config.json" || !bytes.Equal(data, meta) {
		t.Fatalf("metadata fallback: name=%q err=%v", name, err)
	}
	// An explicitly empty primary must not borrow the metadata-only login.
	if err := os.WriteFile(primary, nil, 0600); err != nil {
		t.Fatal(err)
	}
	name, data, err = ReadLiveCredential(files)
	if err != nil || name != "auth.json" || len(data) != 0 {
		t.Fatalf("empty primary was bypassed: name=%q err=%v", name, err)
	}
	if err := os.Truncate(primary, MaxDiscoveryFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLiveCredential(files); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("oversized live source: %v", err)
	}
	after, err := os.ReadFile(metadata)
	if err != nil || !bytes.Equal(after, meta) {
		t.Fatal("read changed native metadata")
	}
}

func TestReadLiveCredentialRejectsNonregularSource(t *testing.T) {
	root := t.TempDir()
	files := AuthFileSet{Tool: "codex", Files: []AuthFileSpec{{Path: filepath.Join(root, "auth.json")}}}
	if _, _, err := ReadLiveCredential(files); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("absent source: %v", err)
	}
	if err := os.Mkdir(files.Files[0].Path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLiveCredential(files); err == nil {
		t.Fatal("directory was accepted as a live credential")
	}
}

func TestReadLiveCredentialClaudeAuthorityWithoutMirror(t *testing.T) {
	for _, mode := range []string{"missing_mirror", "stale_mirror", "explicit_config", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f := newKeychainFixture(t)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "")
			live := keychainCreds("synthetic-keychain")
			stale := keychainCreds("synthetic-file")
			f.storeToken(live)
			if mode != "missing_mirror" {
				writeFixtureFile(t, f.credPath, stale)
			}
			switch mode {
			case "explicit_config":
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(f.credPath))
			case "denied":
				t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")
			case "malformed":
				f.storeToken("synthetic-not-json")
			}
			name, data, err := ReadLiveCredential(f.fileSet)
			if mode == "denied" || mode == "malformed" {
				if err == nil || (mode == "denied" && !errors.Is(err, keychain.ErrDenied)) {
					t.Fatalf("unreadable keychain borrowed stale file: %v", err)
				}
			} else {
				want := live
				if mode == "explicit_config" {
					want = stale
				}
				if err != nil || name != ".credentials.json" || string(data) != want {
					t.Fatalf("wrong captured authority: name=%q err=%v", name, err)
				}
			}
			after, readErr := os.ReadFile(f.credPath)
			if mode == "missing_mirror" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("read created a keychain mirror: %v", readErr)
				}
			} else if readErr != nil || string(after) != stale {
				t.Fatal("read rewrote the credentials mirror")
			}
		})
	}
}
