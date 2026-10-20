package authfile

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func putIsolatedTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedSourceRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "credential.json")
	if _, err := readIsolatedSourceFile(path, true, 8); err == nil {
		t.Fatal("missing required source accepted")
	}
	absent, err := readIsolatedSourceFile(path, false, 8)
	if err != nil || absent.info != nil || absent.unchanged() != nil {
		t.Fatalf("optional absence: %+v, %v", absent, err)
	}
	putIsolatedTestFile(t, path, "12345678")
	if err := absent.unchanged(); err == nil {
		t.Fatal("newly created optional source was not detected")
	}
	got, err := readIsolatedSourceFile(path, true, 8)
	if err != nil || string(got.data) != "12345678" || got.unchanged() != nil {
		t.Fatalf("bounded source read: %+v, %v", got, err)
	}
	if _, err := readIsolatedSourceFile(path, true, 7); err == nil {
		t.Fatal("oversized source accepted")
	}
	if _, err := readIsolatedSourceFile(root, true, 8); err == nil {
		t.Fatal("directory accepted as a source file")
	}
	link := filepath.Join(root, "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readIsolatedSourceFile(link, true, 8); err == nil {
		t.Fatal("source file symlink accepted")
	}
	if f, err := openIsolatedSourceFile(link); err == nil {
		f.Close()
		if runtime.GOOS != "windows" {
			t.Fatal("Unix source opener followed a symlink")
		}
	}
}

func TestIsolatedSourceDetectsGenerationChanges(t *testing.T) {
	for _, change := range []string{"bytes", "replacement", "removal", "symlink", "permissions"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.json")
			putIsolatedTestFile(t, path, "before")
			source, err := readIsolatedSourceFile(path, true, 1024)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "bytes":
				// Same size, inode and restored timestamp: compare bytes too.
				putIsolatedTestFile(t, path, "after!")
				if err := os.Chtimes(path, source.info.ModTime(), source.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				putIsolatedTestFile(t, path+".new", "before")
				if err := os.Rename(path+".new", path); err != nil {
					t.Fatal(err)
				}
			case "removal":
				if err := os.Rename(path, path+".retired"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".retired", path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "permissions":
				if runtime.GOOS == "windows" {
					t.Skip("Unix file modes")
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := source.unchanged(); err == nil {
				t.Fatal("source generation change was accepted")
			}
		})
	}
}

func TestIsolatedSourceDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := inspectIsolatedSourceDir(path)
	if err != nil || dir.unchanged() != nil {
		t.Fatalf("inspect source directory: %v", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := dir.unchanged(); err == nil {
		t.Fatal("directory replacement accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := inspectIsolatedSourceDir(link); err == nil {
		t.Fatal("directory symlink accepted")
	}
}

func TestIsolatedClaudeSelectsLegacyWithoutMixingNamespaces(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "home-state"
		if nested {
			name = "nested-state"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			xdg := filepath.Join(root, "xdg_config", "claude-code")
			legacy := filepath.Join(root, "home", ".claude")
			state := filepath.Join(root, "home", ".claude.json")
			if nested {
				state = filepath.Join(legacy, ".claude.json")
				putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"stale-home"}}`)
			}
			putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"legacy"}}`)
			putIsolatedTestFile(t, state, `{"oauthAccount":{"accountUuid":"legacy-id"}}`)
			putIsolatedTestFile(t, filepath.Join(legacy, "settings.json"), `{"model":"legacy-model"}`)
			// Policy and identity alone do not select XDG in the provider.
			putIsolatedTestFile(t, filepath.Join(xdg, "settings.json"), `{"model":"xdg-model"}`)
			putIsolatedTestFile(t, filepath.Join(xdg, ".claude.json"), `{"oauthAccount":{"accountUuid":"stale-xdg"}}`)
			selected, captures, err := selectIsolatedClaudeLayout(root, 4096, 4096)
			if err != nil || selected.configDir != legacy || len(captures) != 2 {
				t.Fatalf("legacy source not selected: %v", err)
			}
			data := selected.data()
			if string(data[".claude.json"]) != `{"oauthAccount":{"accountUuid":"legacy-id"}}` || string(data["settings.json"]) != `{"model":"legacy-model"}` {
				t.Fatal("import mixed identity or settings from another namespace")
			}
			if selected.files[1].path != state {
				t.Fatalf("wrong state provenance: %s", selected.files[1].path)
			}
			for _, capture := range captures {
				if err := capture.unchanged(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIsolatedClaudeXDGPrimaryPresenceNeverFallsBack(t *testing.T) {
	for _, name := range []string{".credentials.json", "auth.json"} {
		for _, data := range []string{"", "{", "{}", `{"claudeAiOauth":{"accessToken":"expired","expiresAt":1}}`} {
			t.Run(name+"/"+data, func(t *testing.T) {
				root := t.TempDir()
				xdg := filepath.Join(root, "xdg_config", "claude-code")
				legacy := filepath.Join(root, "home", ".claude")
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"legacy-access","refreshToken":"legacy-refresh"}}`)
				putIsolatedTestFile(t, filepath.Join(xdg, name), data)
				selected, captures, err := selectIsolatedClaudeLayout(root, 4096, 4096)
				if err != nil || selected.configDir != xdg || len(captures) != 2 {
					t.Fatalf("incomplete XDG login fell back to legacy: %v", err)
				}
				if string(selected.data()[name]) != data {
					t.Fatal("selector modified captured credential")
				}
			})
		}
	}
}

func TestIsolatedClaudeLayoutRejectsUnsafeSelectorsAndStateFallback(t *testing.T) {
	for _, data := range []string{"{", "null", `{"apiKeyHelper":"do-not-execute"}`, `{"env":{"ANTHROPIC_API_KEY":"do-not-print"}}`} {
		t.Run(data, func(t *testing.T) {
			root := t.TempDir()
			putIsolatedTestFile(t, filepath.Join(root, "home", ".claude", ".credentials.json"), `{}`)
			putIsolatedTestFile(t, filepath.Join(root, "xdg_config", "claude-code", "settings.json"), data)
			if _, _, err := selectIsolatedClaudeLayout(root, 4096, 4096); err == nil {
				t.Fatal("unsafe XDG settings fell back to legacy")
			}
		})
	}
	root := t.TempDir()
	putIsolatedTestFile(t, filepath.Join(root, "home", ".claude", ".credentials.json"), `{}`)
	putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"home"}}`)
	putIsolatedTestFile(t, filepath.Join(root, "home", ".claude", ".claude.json"), "{")
	if _, _, err := selectIsolatedClaudeLayout(root, 4096, 4096); err == nil {
		t.Fatal("malformed nested legacy state fell back to home state")
	}
}

func TestIsolatedClaudeLayoutDetectsSelectionChanges(t *testing.T) {
	for _, change := range []string{"xdg-parent", "xdg-directory", "xdg-token", "xdg-auth", "xdg-settings", "legacy-state", "legacy-token", "legacy-parent"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			xdg := filepath.Join(root, "xdg_config", "claude-code")
			legacy := filepath.Join(root, "home", ".claude")
			putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"generation":"one"}`)
			putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"legacy"}}`)
			if change != "xdg-parent" && change != "xdg-directory" {
				if err := os.MkdirAll(xdg, 0700); err != nil {
					t.Fatal(err)
				}
			}
			selected, captures, err := selectIsolatedClaudeLayout(root, 4096, 4096)
			if err != nil || selected.configDir != legacy {
				t.Fatalf("capture legacy: %v", err)
			}
			switch change {
			case "xdg-parent":
				err = os.Mkdir(filepath.Dir(xdg), 0700)
			case "xdg-directory":
				err = os.MkdirAll(xdg, 0700)
			case "xdg-token":
				putIsolatedTestFile(t, filepath.Join(xdg, ".credentials.json"), `{"generation":"new"}`)
			case "xdg-auth":
				putIsolatedTestFile(t, filepath.Join(xdg, "auth.json"), `{}`)
			case "xdg-settings":
				putIsolatedTestFile(t, filepath.Join(xdg, "settings.json"), `{"apiKeyHelper":"new-helper"}`)
			case "legacy-state":
				putIsolatedTestFile(t, filepath.Join(legacy, ".claude.json"), `{"oauthAccount":{"accountUuid":"new"}}`)
			case "legacy-token":
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"generation":"two"}`)
			case "legacy-parent":
				err = os.Rename(filepath.Dir(legacy), filepath.Dir(legacy)+".moved")
			}
			if err != nil {
				t.Fatal(err)
			}
			detected := false
			for _, capture := range captures {
				detected = capture.unchanged() != nil || detected
			}
			if !detected {
				t.Fatal("changed source selection was not detected before publication")
			}
		})
	}
}

func TestIsolatedClaudeLayoutRejectsLinksAndOversizedFiles(t *testing.T) {
	for _, part := range []string{"xdg_config", "home", "home/.claude", "home/.claude.json", "home/.claude/.credentials.json"} {
		t.Run(part, func(t *testing.T) {
			root := t.TempDir()
			putIsolatedTestFile(t, filepath.Join(root, "home", ".claude", ".credentials.json"), `{}`)
			putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{}`)
			path := filepath.Join(root, filepath.FromSlash(part))
			target := path + ".original"
			if _, err := os.Lstat(path); err == nil {
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, _, err := selectIsolatedClaudeLayout(root, 4096, 4096); err == nil {
				t.Fatal("linked source accepted")
			}
		})
	}
	root := t.TempDir()
	path := filepath.Join(root, "home", ".claude", ".credentials.json")
	putIsolatedTestFile(t, path, "12345")
	if _, _, err := selectIsolatedClaudeLayout(root, 4, 4096); err == nil {
		t.Fatal("oversized legacy credential accepted")
	}
}

func TestOptionalIsolatedDirectoryPresenceIsGuarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	dir, err := inspectOptionalIsolatedSourceDir(path)
	if err != nil || dir.info != nil || dir.unchanged() != nil {
		t.Fatalf("absent directory: %v", err)
	}
	if _, err := inspectIsolatedSourceDir(path); err == nil {
		t.Fatal("required directory accepted absence")
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := dir.unchanged(); err == nil {
		t.Fatal("new directory was ignored")
	}
	present, err := inspectIsolatedSourceDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := present.unchanged(); err == nil {
		t.Fatal("removed directory was ignored")
	}
}

func TestIsolatedClaudeXDGRejectsConflictingLegacyAccount(t *testing.T) {
	for _, source := range []string{"credential", "nested-state", "home-state", "secondary-auth", "helper"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			xdg := filepath.Join(root, "xdg_config", "claude-code")
			legacy := filepath.Join(root, "home", ".claude")
			putIsolatedTestFile(t, filepath.Join(xdg, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"selected-access","refreshToken":"selected-refresh","accountId":"selected-account"}}`)
			putIsolatedTestFile(t, filepath.Join(xdg, ".claude.json"), `{"oauthAccount":{"accountUuid":"selected-account"}}`)
			state := filepath.Join(legacy, ".claude.json")
			switch source {
			case "credential":
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"ignored-access","refreshToken":"ignored-refresh","accountId":"other-account"}}`)
			case "nested-state", "home-state":
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"ignored-access","refreshToken":"ignored-refresh"}}`)
				if source == "home-state" {
					state = filepath.Join(root, "home", ".claude.json")
				}
			case "secondary-auth":
				putIsolatedTestFile(t, filepath.Join(legacy, "auth.json"), `{"accessToken":"ignored-access"}`)
			case "helper":
				putIsolatedTestFile(t, filepath.Join(legacy, "settings.json"), `{"apiKeyHelper":"must-not-execute"}`)
			}
			putIsolatedTestFile(t, state, `{"oauthAccount":{"accountUuid":"other-account"}}`)
			if _, _, err := selectIsolatedClaudeLayout(root, 4096, 4096); err == nil {
				t.Fatal("export accepted accounts that the Claude provider refuses to launch")
			}
		})
	}
}

func TestIsolatedClaudeXDGDoesNotExportLegacyGuard(t *testing.T) {
	for _, scenario := range []string{"same-account", "identity-only", "unknown-account", "embedded-id", "nested-state"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			xdg := filepath.Join(root, "xdg_config", "claude-code")
			legacy := filepath.Join(root, "home", ".claude")
			selectedBytes := `{"claudeAiOauth":{"accessToken":"selected-access","refreshToken":"selected-refresh"}}`
			putIsolatedTestFile(t, filepath.Join(xdg, ".credentials.json"), selectedBytes)
			putIsolatedTestFile(t, filepath.Join(xdg, ".claude.json"), `{"oauthAccount":{"accountUuid":"same-id"}}`)
			putIsolatedTestFile(t, filepath.Join(xdg, "settings.json"), `{"model":"selected-model"}`)
			putIsolatedTestFile(t, filepath.Join(legacy, "settings.json"), `{"model":"ignored-model"}`)
			state := `{"oauthAccount":{"accountUuid":"same-id"}}`
			if scenario != "identity-only" {
				credential := `{"claudeAiOauth":{"accessToken":"ignored-access","refreshToken":"ignored-refresh"}}`
				if scenario == "embedded-id" {
					credential = `{"claudeAiOauth":{"accessToken":"ignored-access","refreshToken":"ignored-refresh","accountId":" same-id "}}`
					state = `{"oauthAccount":{"accountUuid":"stale-paired-id"}}`
				}
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), credential)
			}
			if scenario == "identity-only" {
				state = `{"oauthAccount":{"accountUuid":"stale-id-without-login"}}`
			} else if scenario == "unknown-account" {
				state = `{}`
			}
			putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), state)
			if scenario == "nested-state" {
				putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"ignored-home-id"}}`)
				putIsolatedTestFile(t, filepath.Join(legacy, ".claude.json"), `{"oauthAccount":{"accountUuid":"same-id"}}`)
			}
			selected, captures, err := selectIsolatedClaudeLayout(root, 4096, 4096)
			if err != nil || selected.configDir != xdg || len(captures) != 2 {
				t.Fatalf("valid XDG source rejected: %v", err)
			}
			if !bytes.Equal(selected.data()[".credentials.json"], []byte(selectedBytes)) || strings.Contains(string(selected.data()["settings.json"]), "ignored") {
				t.Fatal("legacy guard bytes were exported or changed the selected credential")
			}
			for _, capture := range captures {
				if err := capture.unchanged(); err != nil {
					t.Fatalf("read-only selection changed the source: %v", err)
				}
			}
		})
	}
}

func TestIsolatedClaudeXDGGuardsLegacyChanges(t *testing.T) {
	for _, change := range []string{"new-login", "credential-id", "home-state", "nested-state", "helper", "directory"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			xdg := filepath.Join(root, "xdg_config", "claude-code")
			legacy := filepath.Join(root, "home", ".claude")
			putIsolatedTestFile(t, filepath.Join(xdg, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"selected","accountId":"one"}}`)
			putIsolatedTestFile(t, filepath.Join(xdg, ".claude.json"), `{"oauthAccount":{"accountUuid":"one"}}`)
			if change != "new-login" {
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"ignored"}}`)
				putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"one"}}`)
			}
			selected, captures, err := selectIsolatedClaudeLayout(root, 4096, 4096)
			if err != nil || selected.configDir != xdg {
				t.Fatalf("capture XDG: %v", err)
			}
			switch change {
			case "new-login", "credential-id":
				putIsolatedTestFile(t, filepath.Join(legacy, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"changed","accountId":"two"}}`)
			case "home-state":
				putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"two"}}`)
			case "nested-state":
				putIsolatedTestFile(t, filepath.Join(legacy, ".claude.json"), `{"oauthAccount":{"accountUuid":"two"}}`)
			case "helper":
				putIsolatedTestFile(t, filepath.Join(legacy, "settings.json"), `{"apiKeyHelper":"not-executed"}`)
			case "directory":
				if err := os.Rename(legacy, legacy+".moved"); err != nil {
					t.Fatal(err)
				}
			}
			// The chosen XDG files never changed. Only the non-selected guard
			// can prevent publication from an account now rejected by exec.
			if err := selected.unchanged(); err != nil {
				t.Fatalf("test unexpectedly modified XDG: %v", err)
			}
			detected := false
			for _, capture := range captures {
				detected = capture.unchanged() != nil || detected
			}
			if !detected {
				t.Fatal("legacy change escaped the final publication guard")
			}
		})
	}
}

func TestIsolatedClaudeLegacySettingsParticipateInConflicts(t *testing.T) {
	for _, settings := range []string{
		`{"env":{"ANTHROPIC_API_KEY":"unprinted-key"}}`,
		`{"env":{"ANTHROPIC_AUTH_TOKEN":"unprinted-token"}}`,
		`{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"unprinted-token"}}`,
		`{"env":null}`, `{"env":{"ANTHROPIC_API_KEY":42}}`,
		`{"apiKeyHelper":null}`, `null`, `{`,
	} {
		root := t.TempDir()
		putIsolatedTestFile(t, filepath.Join(root, "xdg_config", "claude-code", ".credentials.json"), `{"claudeAiOauth":{"accountId":"one"}}`)
		putIsolatedTestFile(t, filepath.Join(root, "home", ".claude", "settings.json"), settings)
		putIsolatedTestFile(t, filepath.Join(root, "home", ".claude.json"), `{"oauthAccount":{"accountUuid":"two"}}`)
		_, _, err := selectIsolatedClaudeLayout(root, 4096, 4096)
		if err == nil || !strings.Contains(err.Error(), "conflicting accounts") {
			t.Fatalf("legacy auth settings hid the conflict: %v", err)
		}
		if strings.Contains(err.Error(), "unprinted") || strings.Contains(err.Error(), "one") || strings.Contains(err.Error(), "two") {
			t.Fatal("conflict diagnostic included source identity or credentials")
		}
	}
}
