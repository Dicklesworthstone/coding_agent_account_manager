package authfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

func publicationWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func publicationRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := claudesettings.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func publicationNoArtifacts(t *testing.T, dir string) {
	t.Helper()
	for _, pattern := range []string{"settings.json.stage.*", "settings.json.rollback.*", "credentials.json.rollback.*", "keychain.rollback.*"} {
		paths, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil || len(paths) != 0 {
			t.Fatalf("unreleased recovery artifacts: %v, %v", paths, err)
		}
	}
}

func TestClaudePublicationFailureRecoversWholeFileBatch(t *testing.T) {
	for _, targetKind := range []string{"oauth", "helper"} {
		for _, failureMode := range []string{"before-write", "after-write", "native-keychain", "uncertain", "recovery-failed", "native-file"} {
			t.Run(targetKind+"/"+failureMode, func(t *testing.T) {
				dir := t.TempDir()
				primary := filepath.Join(dir, ".credentials.json")
				secondary := filepath.Join(dir, "auth.json")
				settings := filepath.Join(dir, "settings.json")
				snapshot := filepath.Join(dir, "snapshot", ".credentials.json")
				const old = `{"accessToken":"synthetic-outgoing"}`
				const incoming = `{"accessToken":"synthetic-incoming"}`
				const native = `{"accessToken":"synthetic-native"}`
				const oldSettings = `{"permissions":{"allow":["Read"]},"apiKeyHelper":"old-helper"}`
				publicationWrite(t, primary, old)
				publicationWrite(t, secondary, old)
				publicationWrite(t, settings, oldSettings)
				item := []byte(old)
				var readErr error
				writes := 0
				failure := errors.New("injected publication error")
				authority, err := prepareClaudeRestoreAuthority(primary,
					func() ([]byte, error) { return bytes.Clone(item), readErr },
					func(data []byte) error {
						writes++
						if writes > 1 {
							if failureMode == "recovery-failed" {
								return errors.New("injected recovery error")
							}
							item = bytes.Clone(data)
							return nil
						}
						switch failureMode {
						case "before-write":
						case "native-keychain":
							item = []byte(native)
						case "native-file":
							publicationWrite(t, primary, native)
						case "uncertain":
							item = bytes.Clone(data)
							readErr = errors.New("keychain unavailable")
						default:
							item = bytes.Clone(data)
						}
						return failure
					})
				if err != nil {
					t.Fatal(err)
				}
				batch := &claudeRestoreBatch{authority: authority}
				if targetKind == "oauth" {
					publicationWrite(t, snapshot, incoming)
					credential, err := prepareClaudeCredentialRestore(snapshot, primary, nil, authority.read, nil)
					if err != nil {
						t.Fatal(err)
					}
					batch.credentials = append(batch.credentials, credential)
				} else {
					retirement, err := prepareClaudeCredentialRetirement(snapshot, primary, authority.read)
					if err != nil {
						t.Fatal(err)
					}
					batch.retirements = append(batch.retirements, retirement)
				}
				retirement, err := prepareClaudeCredentialRetirement(filepath.Join(dir, "snapshot", "auth.json"), secondary, nil)
				if err != nil {
					t.Fatal(err)
				}
				batch.retirements = append(batch.retirements, retirement)
				update, err := claudesettings.PrepareAPIKeyHelper(settings, "incoming-helper")
				if err != nil {
					t.Fatal(err)
				}
				batch.updates = append(batch.updates, update)
				err = batch.Apply()
				if !errors.Is(err, failure) || strings.Contains(err.Error(), "synthetic-") {
					t.Fatalf("publication failure was lost or disclosed credentials: %v", err)
				}
				if string(publicationRead(t, secondary)) != old || string(publicationRead(t, settings)) != oldSettings {
					t.Fatal("publication failure left partially installed settings or retired auth")
				}
				wantPrimary := old
				if failureMode == "native-file" {
					wantPrimary = native
					if !strings.Contains(err.Error(), "preserved at") {
						t.Fatalf("missing retained file recovery reference: %v", err)
					}
				}
				if string(publicationRead(t, primary)) != wantPrimary {
					t.Fatal("primary credential was not recovered or native login was overwritten")
				}
				switch failureMode {
				case "native-keychain":
					if string(item) != native || writes != 1 {
						t.Fatal("native keychain change was overwritten")
					}
				case "uncertain", "recovery-failed":
				default:
					if string(item) != old {
						t.Fatal("outgoing keychain item not recovered")
					}
				}
				if failureMode == "native-keychain" || failureMode == "uncertain" || failureMode == "recovery-failed" {
					if authority.backup == "" || !strings.Contains(err.Error(), authority.backup) || string(publicationRead(t, authority.backup)) != old {
						t.Fatalf("uncertain keychain outcome lost its original recovery copy: %v", err)
					}
					info, statErr := os.Stat(authority.backup)
					if statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
						t.Fatal("keychain recovery copy is not private")
					}
				} else if failureMode != "native-file" {
					publicationNoArtifacts(t, dir)
				}
				beforeRetry := writes
				if nextErr := batch.Apply(); nextErr != err || writes != beforeRetry {
					t.Fatal("repeated per-file visit retried a partially failed publication")
				}
			})
		}
	}
}

func TestClaudePublicationRepeatedAuthenticationTransitions(t *testing.T) {
	dir := t.TempDir()
	primary, settings := filepath.Join(dir, ".credentials.json"), filepath.Join(dir, "settings.json")
	const policy = `"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["tests"]},"mcpServers":{"current":{}},"hooks":{"Stop":[]},"model":"opus","effortLevel":"high"`
	publicationWrite(t, settings, `{`+policy+`,"apiKeyHelper":"outgoing"}`)
	item := []byte(`{"accessToken":"outgoing"}`)
	publicationWrite(t, primary, string(item))
	for _, name := range []string{"oauth-a", "helper-b", "env-c", "oauth-a"} {
		t.Run(name, func(t *testing.T) {
			profileDir := filepath.Join(dir, name)
			auth := ""
			switch name {
			case "helper-b":
				auth = `,"apiKeyHelper":"helper-b"`
			case "env-c":
				auth = `,"env":{"ANTHROPIC_AUTH_TOKEN":"env-c"}`
			}
			publicationWrite(t, filepath.Join(profileDir, "settings.json"), `{"model":"stale"`+auth+`}`)
			authority, err := prepareClaudeRestoreAuthority(primary, func() ([]byte, error) { return bytes.Clone(item), nil }, func(data []byte) error { item = bytes.Clone(data); return nil })
			if err != nil {
				t.Fatal(err)
			}
			batch := &claudeRestoreBatch{authority: authority}
			snapshot := filepath.Join(profileDir, ".credentials.json")
			if name == "oauth-a" {
				publicationWrite(t, snapshot, `{"accessToken":"oauth-a"}`)
				credential, err := prepareClaudeCredentialRestore(snapshot, primary, nil, authority.read, nil)
				if err != nil {
					t.Fatal(err)
				}
				batch.credentials = append(batch.credentials, credential)
			} else {
				retirement, err := prepareClaudeCredentialRetirement(snapshot, primary, authority.read)
				if err != nil {
					t.Fatal(err)
				}
				batch.retirements = append(batch.retirements, retirement)
			}
			update, err := claudesettings.PrepareRestore(filepath.Join(profileDir, "settings.json"), settings, claudesettings.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			batch.updates = append(batch.updates, update)
			if err := batch.Apply(); err != nil {
				t.Fatal(err)
			}
			want, err := claudesettings.Merge([]byte(`{`+policy+`}`), []byte(`{`+strings.TrimPrefix(auth, ",")+`}`), claudesettings.Policy{})
			if err != nil || !bytes.Equal(publicationRead(t, settings), want) {
				t.Fatal("transition reverted shared workflow or retained outgoing settings auth")
			}
			if name == "oauth-a" {
				if string(item) != `{"accessToken":"oauth-a"}` || !bytes.Equal(item, publicationRead(t, primary)) {
					t.Fatal("OAuth publication did not install selected account")
				}
			} else if item != nil || publicationRead(t, primary) != nil {
				t.Fatal("helper/environment publication retained OAuth fallback")
			}
			publicationNoArtifacts(t, dir)
		})
	}
}

func TestClaudePublicationPreservesFreshLiveChoiceAndExactBytes(t *testing.T) {
	dir := t.TempDir()
	live, snapshot, identity := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot"), filepath.Join(dir, "identity")
	const newer = "{ \"accessToken\" : \"newer\", \"counter\":9007199254740993 }\n"
	publicationWrite(t, live, newer)
	publicationWrite(t, snapshot, `{"accessToken":"older"}`)
	publicationWrite(t, identity, `{"account":"same"}`)
	calls := 0
	credential, err := prepareClaudeCredentialRestore(snapshot, live, map[string][]byte{identity: publicationRead(t, identity)}, nil, func() bool { calls++; return true })
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}
	batch := &claudeRestoreBatch{credentials: []*claudeCredentialRestore{credential}}
	if err := batch.Apply(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(live)
	if err != nil || !os.SameFile(info, after) || string(publicationRead(t, live)) != newer || calls != 1 {
		t.Fatal("fresh live choice was copied/reformatted or decided repeatedly")
	}
}

func TestClaudePublicationRejectsCapturedSourceChanges(t *testing.T) {
	for _, changed := range []string{"snapshot", "identity", "live", "keychain"} {
		t.Run(changed, func(t *testing.T) {
			dir := t.TempDir()
			live, snapshot, identity := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot"), filepath.Join(dir, "identity")
			const old = `{"accessToken":"old"}`
			publicationWrite(t, live, old)
			publicationWrite(t, snapshot, `{"accessToken":"target"}`)
			publicationWrite(t, identity, `{"account":"old"}`)
			item := []byte(old)
			credential, err := prepareClaudeCredentialRestore(snapshot, live, map[string][]byte{identity: publicationRead(t, identity)}, func() ([]byte, error) { return item, nil }, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := old
			switch changed {
			case "snapshot":
				publicationWrite(t, snapshot, `{"accessToken":"changed"}`)
			case "identity":
				publicationWrite(t, identity, `{"account":"changed"}`)
			case "live":
				want = `{"accessToken":"changed"}`
				publicationWrite(t, live, want)
			case "keychain":
				item = []byte(`{"accessToken":"changed"}`)
			}
			if err := (&claudeRestoreBatch{credentials: []*claudeCredentialRestore{credential}}).Apply(); err == nil {
				t.Fatal("changed captured credential/identity was accepted")
			}
			if string(publicationRead(t, live)) != want {
				t.Fatal("failed preflight changed live credentials")
			}
			publicationNoArtifacts(t, dir)
		})
	}
}

func TestClaudePublicationStagingFailureNeverChangesAuthority(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires deterministic unwritable procfs destination")
	}
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
	publicationWrite(t, live, `{"accessToken":"old"}`)
	publicationWrite(t, snapshot, `{"accessToken":"new"}`)
	credential, err := prepareClaudeCredentialRestore(snapshot, live, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := claudesettings.PrepareRestore(snapshot, filepath.Join("/proc/self", "caam-publication-test"), claudesettings.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	authority, err := prepareClaudeRestoreAuthority(live, func() ([]byte, error) { return []byte(`{"accessToken":"old"}`), nil }, func([]byte) error { writes++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	batch := &claudeRestoreBatch{credentials: []*claudeCredentialRestore{credential}, updates: []*claudesettings.Update{blocked}, authority: authority}
	if err := batch.Apply(); err == nil || writes != 0 || string(publicationRead(t, live)) != `{"accessToken":"old"}` {
		t.Fatal("staging failure published or overwrote part of the incoming account")
	}
	publicationNoArtifacts(t, dir)
}

func TestClaudePublicationAcceptsOnlyCapturedKeychainMirror(t *testing.T) {
	for _, initial := range []string{"missing", "stale-disk"} {
		for _, fail := range []bool{false, true} {
			t.Run(initial+map[bool]string{false: "/success", true: "/rollback"}[fail], func(t *testing.T) {
				dir := t.TempDir()
				live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
				const old = `{"accessToken":"effective-keychain"}`
				const incoming = `{"accessToken":"selected"}`
				if initial == "stale-disk" {
					publicationWrite(t, live, `{"accessToken":"obsolete-disk"}`)
				}
				publicationWrite(t, snapshot, incoming)
				item := []byte(old)
				authority, err := prepareClaudeRestoreAuthority(live, func() ([]byte, error) { return bytes.Clone(item), nil }, func(data []byte) error {
					if fail {
						return errors.New("publication denied")
					}
					item = bytes.Clone(data)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				credential, err := prepareClaudeCredentialRestore(snapshot, live, nil, authority.read, nil)
				if err != nil {
					t.Fatal(err)
				}
				// Restore's pull is the one accepted change between preflight
				// and the batch: it installs the captured authoritative grant.
				publicationWrite(t, live, old+"\n")
				err = (&claudeRestoreBatch{credentials: []*claudeCredentialRestore{credential}, authority: authority}).Apply()
				if (err != nil) != fail {
					t.Fatalf("unexpected publication result: %v", err)
				}
				want := incoming
				if fail {
					want = old
				}
				if !sameClaudeAuthority(publicationRead(t, live), []byte(want)) || string(item) != want {
					t.Fatal("restore lost the authoritative keychain generation")
				}
				publicationNoArtifacts(t, dir)
			})
		}
	}
}
