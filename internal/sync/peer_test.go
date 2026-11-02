package sync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
	"golang.org/x/crypto/ssh"
)

// A separate process exercises the OS lock as well as the process-wide mutex.
func TestSyncPeerHelperProcess(t *testing.T) {
	if os.Getenv("CAAM_TEST_SYNC_PEER") != "1" {
		return
	}
	connection := &sshPeerConnection{reader: os.Stdin, writer: os.Stdout, close: os.Stdin.Close}
	_ = ServePeer(context.Background(), connection)
}

type peerObserver func(*peerMessage) error
type observedPeer struct {
	connection              io.ReadWriteCloser
	beforeSend, afterRead   peerObserver
	readBuffer, writeBuffer bytes.Buffer
}

func (p *observedPeer) Read(data []byte) (int, error) {
	if p.readBuffer.Len() == 0 {
		message, err := (&peerStream{connection: p.connection}).receive()
		if err != nil {
			return 0, err
		}
		if p.afterRead != nil {
			if err := p.afterRead(&message); err != nil {
				_ = p.connection.Close()
				return 0, err
			}
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return 0, err
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(encoded)))
		p.readBuffer.Write(size[:])
		p.readBuffer.Write(encoded)
	}
	return p.readBuffer.Read(data)
}
func (p *observedPeer) Write(data []byte) (int, error) {
	p.writeBuffer.Write(data)
	for p.writeBuffer.Len() >= 8 {
		length := binary.BigEndian.Uint64(p.writeBuffer.Bytes()[:8])
		if length > peerMaxFrameBytes {
			return 0, errors.New("test received oversized frame")
		}
		if uint64(p.writeBuffer.Len()-8) < length {
			break
		}
		p.writeBuffer.Next(8)
		var message peerMessage
		if err := json.Unmarshal(p.writeBuffer.Next(int(length)), &message); err != nil {
			return 0, err
		}
		if p.beforeSend != nil {
			if err := p.beforeSend(&message); err != nil {
				_ = p.connection.Close()
				return 0, err
			}
		}
		if err := (&peerStream{connection: p.connection}).send(message); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}
func (p *observedPeer) Close() error { return p.connection.Close() }
func peerProcessDial(t *testing.T, beforeSend, afterRead peerObserver) func(context.Context, *SSHClient) (io.ReadWriteCloser, error) {
	t.Helper()
	return func(parent context.Context, _ *SSHClient) (io.ReadWriteCloser, error) {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		command := exec.CommandContext(ctx, executable, "-test.run=^TestSyncPeerHelperProcess$")
		command.Env = append(os.Environ(), "CAAM_TEST_SYNC_PEER=1")
		command.Stderr = io.Discard
		input, err := command.StdinPipe()
		if err != nil {
			cancel()
			return nil, err
		}
		output, err := command.StdoutPipe()
		if err != nil {
			cancel()
			return nil, err
		}
		if err := command.Start(); err != nil {
			cancel()
			return nil, err
		}
		connection := &sshPeerConnection{reader: output, writer: input, close: func() error { err := command.Wait(); cancel(); return err }}
		t.Cleanup(func() { _ = connection.Close() })
		return &observedPeer{connection: connection, beforeSend: beforeSend, afterRead: afterRead}, nil
	}
}
func peerGrant(label string, expiry time.Time) []byte {
	if expiry.IsZero() {
		return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-%s-access","refreshToken":"synthetic-%s-refresh"}}`, label, label))
	}
	return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-%s-access","refreshToken":"synthetic-%s-refresh","expiresAt":%d}}`, label, label, expiry.UnixMilli()))
}
func writePeerProfile(t *testing.T, vault, profile string, credentials []byte, org, proofFile string) string {
	t.Helper()
	dir := filepath.Join(vault, "claude", profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), credentials, 0600); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"tool": "claude", "profile": profile, "description": "operator label", "tags": []string{"work"}, "identity": "stale@example.invalid", "identity_keys": []string{"uuid:stale", "org:stale"}}
	if proofFile != "" {
		fingerprint, err := identity.ClaudeCredentialFingerprint(credentials)
		if err != nil {
			t.Fatal(err)
		}
		binding := &identity.ClaudeIdentityBinding{Identity: identity.Identity{Provider: "claude", AccountID: "shared-account", OrganizationID: org, Email: "shared@example.invalid", Organization: org, Verified: true}, CredentialFingerprint: fingerprint, VerifiedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
		if proofFile == "meta.json" {
			meta["claude_identity"] = binding
		} else {
			data, _ := json.Marshal(map[string]any{"claude_identity": binding})
			if err := os.WriteFile(filepath.Join(dir, proofFile), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"theme":"dark","projects":{"/project":{"trusted":true}},"oauthAccount":{"accountUuid":"shared-account","organizationUuid":"wrong-org","emailAddress":"shared@example.invalid"}}`)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func runPeerSync(t *testing.T, local, remote string, mode SyncMode, beforeSend, afterRead peerObserver) (*SyncResult, error) {
	t.Helper()
	syncer := &Syncer{vaultPath: local, remoteVaultPath: remote, policy: NewPolicyResolver(map[string]string{"claude": string(mode)}, nil), peerDial: peerProcessDial(t, beforeSend, afterRead)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := syncer.syncProfileOperation(ctx, nil, NewMachine("peer", "localhost"), ProfileRef{Provider: "claude", Profile: "work"})
	if err == nil && result != nil {
		err = result.Error
	}
	return result, err
}
func capturePeerProfile(t *testing.T, path string) *vaulttransfer.Snapshot {
	t.Helper()
	snapshot, err := vaulttransfer.Capture(path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestGuardedSyncRejectsSharedEmailOrganizationsBeforeFreshness(t *testing.T) {
	base := time.Now().Add(time.Hour)
	for _, timing := range []string{"push", "pull", "equal", "unknown"} {
		t.Run(timing, func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			localExpiry, remoteExpiry := base, base
			switch timing {
			case "push":
				localExpiry = base.Add(time.Hour)
			case "pull":
				remoteExpiry = base.Add(time.Hour)
			case "unknown":
				localExpiry = time.Time{}
			}
			grant := peerGrant("personal", localExpiry)
			personal := writePeerProfile(t, local, "work", grant, "personal", "meta.json")
			team := writePeerProfile(t, remote, "work", peerGrant("team", remoteExpiry), "team", "meta.json")
			beforeLocal, beforeRemote := capturePeerProfile(t, personal), capturePeerProfile(t, team)
			if timing == "unknown" {
				if err := authfile.ValidateCredentialData("claude", ".credentials.json", grant); err != nil {
					t.Fatalf("unknown expiry fixture invalid: %v", err)
				}
				fresh, err := capturedClaudeFreshness(beforeLocal, "work")
				if err != nil || fresh == nil || !fresh.ExpiresAt.IsZero() {
					t.Fatalf("fixture expiry is not unknown: %v", err)
				}
			}
			_, err := runPeerSync(t, local, remote, ModeReplicate, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("expected ownership error: %v", err)
			}
			if beforeLocal.Check() != nil || beforeRemote.Check() != nil {
				t.Fatal("conflicting organizations changed a profile")
			}
		})
	}
}
func TestGuardedSyncPreservesExpiredVerifiedOwnerAndRestartsOffline(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		t.Run(string(direction), func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			sourceVault, destVault := local, remote
			if direction == SyncPull {
				sourceVault, destVault = remote, local
			}
			newer := peerGrant("new", time.Now().Add(-2*time.Hour))
			source := writePeerProfile(t, sourceVault, "work", newer, "team", identity.ClaudeIdentityCacheFile)
			dest := writePeerProfile(t, destVault, "work", peerGrant("old", time.Now().Add(-3*time.Hour)), "team", "meta.json")
			beforeSource, beforeDest := capturePeerProfile(t, source), capturePeerProfile(t, dest)
			result, err := runPeerSync(t, local, remote, ModeReplicate, nil, nil)
			if err != nil || result == nil || !result.Success || result.Operation.Direction != direction {
				t.Fatalf("offline transfer failed: %v", err)
			}
			if beforeSource.Check() != nil {
				t.Fatal("source changed")
			}
			if result.RecoveryPath == "" || beforeDest.CheckAt(result.RecoveryPath) != nil {
				t.Fatal("complete previous generation not reported/preserved")
			}
			restarted, err := vaulttransfer.InspectClaude(capturePeerProfile(t, dest))
			if err != nil || restarted.Binding == nil || restarted.Binding.Identity.OrganizationID != "team" {
				t.Fatalf("restart lost verified owner: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dest, ".credentials.json"))
			if err != nil || !bytes.Equal(data, newer) {
				t.Fatal("wrong credential generation installed")
			}
			if _, err := os.Stat(filepath.Join(dest, identity.ClaudeIdentityCacheFile)); !os.IsNotExist(err) {
				t.Fatal("redundant native proof was retained")
			}
			var settings map[string]any
			data, _ = os.ReadFile(filepath.Join(dest, ".claude.json"))
			_ = json.Unmarshal(data, &settings)
			if settings["theme"] != "dark" || settings["projects"] == nil {
				t.Fatal("unrelated state was lost")
			}
			account, _ := settings["oauthAccount"].(map[string]any)
			if account["organizationUuid"] != "team" {
				t.Fatal("stale account label was copied")
			}
		})
	}
}
func TestGuardedSyncRejectsUnprovenOrMismatchedCredentials(t *testing.T) {
	for _, kind := range []string{"unknown-pairs", "mismatched-access", "malformed-proof", "discard-proof"} {
		t.Run(kind, func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			old := peerGrant("old", time.Now().Add(time.Hour))
			newer := peerGrant("new", time.Now().Add(2*time.Hour))
			lp, rp := "meta.json", "meta.json"
			switch kind {
			case "unknown-pairs":
				lp = ""
				rp = ""
			case "discard-proof":
				lp = ""
			}
			if kind == "discard-proof" {
				var doc map[string]any
				_ = json.Unmarshal(old, &doc)
				doc["claudeAiOauth"].(map[string]any)["expiresAt"] = time.Now().Add(2 * time.Hour).UnixMilli()
				newer, _ = json.Marshal(doc)
			}
			ld := writePeerProfile(t, local, "work", newer, "team", lp)
			rd := writePeerProfile(t, remote, "work", old, "team", rp)
			if kind == "mismatched-access" {
				data := bytes.Replace(newer, []byte("synthetic-new-access"), []byte("synthetic-other-access"), 1)
				if err := os.WriteFile(filepath.Join(ld, ".credentials.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "malformed-proof" {
				if err := os.WriteFile(filepath.Join(ld, "meta.json"), []byte(`{"claude_identity":{"token":"synthetic-secret"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			beforeL, beforeR := capturePeerProfile(t, ld), capturePeerProfile(t, rd)
			_, err := runPeerSync(t, local, remote, ModeReplicate, nil, nil)
			if err == nil || strings.Contains(err.Error(), "synthetic-") {
				t.Fatalf("unsafe profile not rejected confidentially: %v", err)
			}
			if beforeL.Check() != nil || beforeR.Check() != nil {
				t.Fatal("rejected profile transfer changed destination")
			}
		})
	}
}
func TestGuardedSyncExactUnverifiedPairRemainsOffline(t *testing.T) {
	local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
	old := peerGrant("same", time.Now().Add(time.Hour))
	newer := peerGrant("same", time.Now().Add(2*time.Hour))
	writePeerProfile(t, local, "work", newer, "ignored", "")
	dest := writePeerProfile(t, remote, "work", old, "ignored", "")
	result, err := runPeerSync(t, local, remote, ModeReplicate, nil, nil)
	if err != nil || !result.Success {
		t.Fatalf("exact pair rejected: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dest, ".claude.json"))
	var settings map[string]any
	_ = json.Unmarshal(data, &settings)
	if _, present := settings["oauthAccount"]; present {
		t.Fatal("unverified owner labels survived normalization")
	}
}

func TestGuardedSyncSourceChangesRollBackPublication(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		for _, when := range []int{1, 3} {
			t.Run(fmt.Sprintf("%s/check-%d", direction, when), func(t *testing.T) {
				local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
				srcVault, dstVault := local, remote
				if direction == SyncPull {
					srcVault, dstVault = remote, local
				}
				writePeerProfile(t, srcVault, "work", peerGrant("new", time.Now().Add(2*time.Hour)), "team", "meta.json")
				dest := writePeerProfile(t, dstVault, "work", peerGrant("old", time.Now().Add(time.Hour)), "team", "meta.json")
				before := capturePeerProfile(t, dest)
				checks := 0
				change := func(message *peerMessage) error {
					if message.Kind == "check_source" {
						checks++
						if checks == when {
							writePeerProfile(t, srcVault, "work", peerGrant("independent", time.Now().Add(3*time.Hour)), "team", "meta.json")
						}
					}
					return nil
				}
				var send, read peerObserver
				if direction == SyncPush {
					read = change
				} else {
					send = change
				}
				_, err := runPeerSync(t, local, remote, ModeReplicate, send, read)
				if err == nil || checks < when {
					t.Fatalf("source change did not interrupt publication: %v", err)
				}
				if before.Check() != nil {
					t.Fatal("source race did not restore complete destination")
				}
			})
		}
	}
}
func TestGuardedSyncReportsRecoveryWhenIndependentLoginPreventsRollback(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		t.Run(string(direction), func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			srcVault, dstVault := local, remote
			if direction == SyncPull {
				srcVault, dstVault = remote, local
			}
			writePeerProfile(t, srcVault, "work", peerGrant("new", time.Now().Add(2*time.Hour)), "team", "meta.json")
			dest := writePeerProfile(t, dstVault, "work", peerGrant("old", time.Now().Add(time.Hour)), "team", "meta.json")
			before := capturePeerProfile(t, dest)
			checks := 0
			independent := peerGrant("independent", time.Now().Add(3*time.Hour))
			change := func(message *peerMessage) error {
				if message.Kind == "check_source" {
					checks++
					if checks == 3 {
						writePeerProfile(t, srcVault, "work", independent, "team", "meta.json")
						writePeerProfile(t, dstVault, "work", independent, "personal", "meta.json")
					}
				}
				return nil
			}
			var send, read peerObserver
			if direction == SyncPush {
				read = change
			} else {
				send = change
			}
			result, err := runPeerSync(t, local, remote, ModeReplicate, send, read)
			if err == nil || result == nil || result.RecoveryPath == "" {
				t.Fatal("failed publication did not report recovery")
			}
			if before.CheckAt(result.RecoveryPath) != nil {
				t.Fatal("previous login lost")
			}
			data, _ := os.ReadFile(filepath.Join(dest, ".credentials.json"))
			if !bytes.Equal(data, independent) {
				t.Fatal("rollback overwrote independent login")
			}
		})
	}
}

func TestGuardedSyncHostLocalSeedsOnlyLogicalMetadata(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		t.Run(string(direction), func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			srcVault, dstVault := local, remote
			if direction == SyncPull {
				srcVault, dstVault = remote, local
			}
			source := writePeerProfile(t, srcVault, "work", peerGrant("private", time.Now().Add(time.Hour)), "team", "meta.json")
			before := capturePeerProfile(t, source)
			check := func(message *peerMessage) error {
				if message.Kind == "chunk" && (message.Name != "meta.json" || bytes.Contains(message.Data, []byte("synthetic-")) || bytes.Contains(message.Data, []byte("claude_identity")) || bytes.Contains(message.Data, []byte("identity_keys"))) {
					return errors.New("host-local wire included private data")
				}
				return nil
			}
			result, err := runPeerSync(t, local, remote, ModeHostLocal, check, check)
			if err != nil || !result.Success || result.Operation.Direction != direction {
				t.Fatalf("metadata seed failed: %v", err)
			}
			dest := filepath.Join(dstVault, "claude", "work")
			entries, err := os.ReadDir(dest)
			if err != nil || len(entries) != 1 || entries[0].Name() != "meta.json" {
				t.Fatal("metadata seed copied credentials")
			}
			data, _ := os.ReadFile(filepath.Join(dest, "meta.json"))
			var meta map[string]any
			_ = json.Unmarshal(data, &meta)
			if meta["description"] != "operator label" || meta["profile"] != "work" || meta["tool"] != "claude" || meta["identity"] != nil {
				t.Fatal("logical metadata was not sanitized")
			}
			if before.Check() != nil {
				t.Fatal("metadata seed changed source")
			}
		})
	}
}
func TestGuardedSyncHostLocalSeedsWithOversizedExcludedHistory(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		t.Run(string(direction), func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			sourceVault, destinationVault := local, remote
			if direction == SyncPull {
				sourceVault, destinationVault = remote, local
			}
			source := writePeerProfile(t, sourceVault, "work", peerGrant("private", time.Now().Add(time.Hour)), "team", "meta.json")
			history, err := os.OpenFile(filepath.Join(source, "private.history"), os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if err := history.Truncate(peerFileLimit("private.history") + 1); err != nil {
				history.Close()
				t.Fatal(err)
			}
			if err := history.Close(); err != nil {
				t.Fatal(err)
			}
			before := capturePeerProfile(t, source)
			inspect := func(message *peerMessage) error {
				if message.Snapshot != nil {
					for _, entry := range message.Snapshot.Entries {
						if entry.Name != "meta.json" {
							return errors.New("excluded private history entered manifest")
						}
					}
				}
				if message.Kind == "chunk" && (message.Name != "meta.json" || bytes.Contains(message.Data, []byte("synthetic-")) || bytes.Contains(message.Data, []byte("claude_identity"))) {
					return errors.New("excluded private payload entered wire")
				}
				return nil
			}
			result, err := runPeerSync(t, local, remote, ModeHostLocal, inspect, inspect)
			if err != nil || result == nil || !result.Success || result.Operation.Direction != direction {
				t.Fatalf("excluded history prevented metadata seed: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(destinationVault, "claude", "work"))
			if err != nil || len(entries) != 1 || entries[0].Name() != "meta.json" {
				t.Fatal("metadata seed published an excluded payload")
			}
			if err := before.Check(); err != nil {
				t.Fatalf("metadata seed changed the complete source generation: %v", err)
			}
		})
	}

}
func TestGuardedSyncHostLocalRefusesConcurrentDestinationCreation(t *testing.T) {
	local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
	writePeerProfile(t, local, "work", peerGrant("source", time.Now().Add(time.Hour)), "team", "meta.json")
	var concurrent *vaulttransfer.Snapshot
	change := func(message *peerMessage) error {
		if message.Kind == "push" {
			dest := writePeerProfile(t, remote, "work", peerGrant("login", time.Now().Add(time.Hour)), "personal", "meta.json")
			concurrent = capturePeerProfile(t, dest)
		}
		return nil
	}
	_, err := runPeerSync(t, local, remote, ModeHostLocal, change, nil)
	if err == nil || concurrent == nil || concurrent.Check() != nil {
		t.Fatal("metadata seed replaced a concurrent login")
	}
}
func TestGuardedSyncStreamsChunksAndRejectsCorruption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
			source := writePeerProfile(t, local, "work", peerGrant("new", time.Now().Add(2*time.Hour)), "team", "meta.json")
			dest := writePeerProfile(t, remote, "work", peerGrant("old", time.Now().Add(time.Hour)), "team", "meta.json")
			policy, _ := json.Marshal(map[string]string{"policy": strings.Repeat("x", 3*peerChunkBytes+11)})
			if err := os.WriteFile(filepath.Join(source, "settings.json"), policy, 0600); err != nil {
				t.Fatal(err)
			}
			before := capturePeerProfile(t, dest)
			chunks := 0
			observe := func(message *peerMessage) error {
				if message.Kind == "chunk" && message.Name == "settings.json" {
					chunks++
					if len(message.Data) > peerChunkBytes {
						return errors.New("oversized chunk")
					}
					if corrupt && chunks == 2 {
						message.Data[0] ^= 1
					}
				}
				return nil
			}
			result, err := runPeerSync(t, local, remote, ModeReplicate, observe, nil)
			if chunks < 4 {
				t.Fatal("policy was not streamed in bounded chunks")
			}
			if corrupt {
				if err == nil || before.Check() != nil {
					t.Fatal("corrupt stream published")
				}
				return
			}
			if err != nil || !result.Success {
				t.Fatalf("chunked transfer failed: %v", err)
			}
			data, _ := os.ReadFile(filepath.Join(dest, "settings.json"))
			if !bytes.Equal(data, policy) {
				t.Fatal("streamed policy changed")
			}
		})
	}
}

func openPeerFixture(t *testing.T, remote string, metadataOnly bool) (*peerStream, *peerSnapshot) {
	t.Helper()
	connection, err := peerProcessDial(t, nil, nil)(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &peerStream{connection: connection}
	ref := ProfileRef{Provider: "claude", Profile: "work"}
	if err := stream.send(peerMessage{Kind: "open", Vault: remote, Provider: ref.Provider, Profile: ref.Profile, MetadataOnly: metadataOnly}); err != nil {
		t.Fatal(err)
	}
	message, err := stream.receive()
	if err != nil || message.Kind != "snapshot" {
		t.Fatalf("peer did not capture snapshot: %v", err)
	}
	_, cleanup, err := receivePeerSnapshot(stream, t.TempDir(), message.Snapshot, ref, metadataOnly)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	return stream, message.Snapshot
}
func TestSyncPeerIndependentlyRejectsWrongOwner(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	source := writePeerProfile(t, local, "work", peerGrant("personal", time.Now().Add(2*time.Hour)), "personal", "meta.json")
	dest := writePeerProfile(t, remote, "work", peerGrant("team", time.Now().Add(time.Hour)), "team", "meta.json")
	before := capturePeerProfile(t, dest)
	stream, remoteWire := openPeerFixture(t, remote, false)
	wire, err := snapshotForPeer(capturePeerProfile(t, source), ProfileRef{Provider: "claude", Profile: "work"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.send(peerMessage{Kind: "push", Expected: remoteWire.Digest, Snapshot: wire}); err != nil {
		t.Fatal(err)
	}
	ready, err := stream.receive()
	if err != nil || ready.Kind != "ready" {
		t.Fatalf("peer did not accept stream: %v", err)
	}
	if err := sendPeerSnapshotData(stream, wire); err != nil {
		t.Fatal(err)
	}
	result, err := stream.receive()
	if err != nil || result.OK || result.Code != "ownership_conflict" || before.Check() != nil {
		t.Fatalf("remote owner boundary failed: %v", err)
	}
}
func TestSyncPeerRejectsChangedDigestAndUnsafePaths(t *testing.T) {
	for _, kind := range []string{"digest", "traversal", "device", "trailing-dot", "case-alias", "directory-case"} {
		t.Run(kind, func(t *testing.T) {
			local, remote := t.TempDir(), t.TempDir()
			source := writePeerProfile(t, local, "work", peerGrant("new", time.Now().Add(2*time.Hour)), "team", "meta.json")
			dest := writePeerProfile(t, remote, "work", peerGrant("old", time.Now().Add(time.Hour)), "team", "meta.json")
			before := capturePeerProfile(t, dest)
			stream, remoteWire := openPeerFixture(t, remote, false)
			wire, err := snapshotForPeer(capturePeerProfile(t, source), ProfileRef{Provider: "claude", Profile: "work"}, false)
			if err != nil {
				t.Fatal(err)
			}
			expected := remoteWire.Digest
			switch kind {
			case "digest":
				expected = strings.Repeat("0", 64)
			case "traversal":
				wire.Entries[0].Name = "../outside"
			case "device":
				wire.Entries[0].Name = "NUL.json"
			case "trailing-dot":
				wire.Entries[0].Name = "auth.json."
			case "case-alias":
				other := wire.Entries[0]
				other.Name = strings.ToUpper(other.Name)
				wire.Entries = append(wire.Entries, other)
			case "directory-case":
				wire.Entries = append(wire.Entries, peerEntry{Name: "Parent", Dir: true}, peerEntry{Name: "parent/sub", Dir: true})
			}
			if err := stream.send(peerMessage{Kind: "push", Expected: expected, Snapshot: wire}); err != nil {
				t.Fatal(err)
			}
			result, err := stream.receive()
			if err != nil || result.Kind != "result" || result.OK || before.Check() != nil {
				t.Fatalf("unsafe request accepted: %v", err)
			}
		})
	}
}

func TestGuardedSyncUsesConfiguredRemoteDataRoot(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		t.Run(string(direction), func(t *testing.T) {
			base := t.TempDir()
			local := filepath.Join(base, "local")
			fallback := filepath.Join(base, "default-vault")
			dataRoot := filepath.Join(base, "custom caam")
			remote := filepath.Join(dataRoot, "vault")
			sourceVault, destVault := local, remote
			if direction == SyncPull {
				sourceVault, destVault = remote, local
			}
			newer := peerGrant("custom-new", time.Now().Add(2*time.Hour))
			source := writePeerProfile(t, sourceVault, "work", newer, "team", "meta.json")
			dest := writePeerProfile(t, destVault, "work", peerGrant("custom-old", time.Now().Add(time.Hour)), "team", "meta.json")
			decoy := writePeerProfile(t, fallback, "work", peerGrant("decoy", time.Now().Add(3*time.Hour)), "personal", "meta.json")
			beforeSource, beforeDecoy := capturePeerProfile(t, source), capturePeerProfile(t, decoy)
			machine := NewMachine("custom-root-peer", "localhost")
			machine.RemotePath = filepath.ToSlash(dataRoot)
			opened := false
			syncer := &Syncer{vaultPath: local, remoteVaultPath: fallback, policy: NewPolicyResolver(map[string]string{"claude": string(ModeReplicate)}, nil), peerDial: peerProcessDial(t, func(message *peerMessage) error {
				if message.Kind == "open" {
					opened = true
					if message.Vault != filepath.ToSlash(remote) {
						return errors.New("wrong remote namespace")
					}
				}
				return nil
			}, nil)}
			result, err := syncer.syncProfileOperation(context.Background(), nil, machine, ProfileRef{Provider: "claude", Profile: "work"})
			if err != nil || result == nil || result.Error != nil || !result.Success || result.Operation.Direction != direction || !opened {
				t.Fatalf("custom-root sync failed: %v", err)
			}
			data, _ := os.ReadFile(filepath.Join(dest, ".credentials.json"))
			if !bytes.Equal(data, newer) {
				t.Fatal("wrong custom-root grant")
			}
			if beforeSource.Check() != nil || beforeDecoy.Check() != nil {
				t.Fatal("custom-root sync changed unselected default vault or source")
			}
		})
	}
}
func TestGuardedSyncRejectsVaultSymlinkBeforeLockCreation(t *testing.T) {
	for _, side := range []string{"local", "peer"} {
		t.Run(side, func(t *testing.T) {
			base := t.TempDir()
			foreign := filepath.Join(base, "foreign")
			if err := os.Mkdir(foreign, 0700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(base, "alias")
			if err := os.Symlink(foreign, alias); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if side == "local" {
				_, err := runPeerSync(t, alias, filepath.Join(base, "remote"), ModeReplicate, nil, nil)
				if err == nil {
					t.Fatal("symlinked local vault accepted")
				}
			} else {
				connection, err := peerProcessDial(t, nil, nil)(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				stream := &peerStream{connection: connection}
				if err := stream.send(peerMessage{Kind: "open", Vault: alias, Provider: "claude", Profile: "work"}); err != nil {
					t.Fatal(err)
				}
				response, err := stream.receive()
				if err == nil && response.OK {
					t.Fatal("symlinked remote vault accepted")
				}
			}
			entries, err := os.ReadDir(foreign)
			if err != nil || len(entries) != 0 {
				t.Fatal("rejected vault alias created an external lock")
			}
		})
	}
}
func TestGuardedSyncIncomingCleanupPreservesReboundVault(t *testing.T) {
	for _, direction := range []SyncDirection{SyncPush, SyncPull} {
		t.Run(string(direction), func(t *testing.T) {
			base := t.TempDir()
			local, remote := filepath.Join(base, "local"), filepath.Join(base, "remote")
			sourceVault, receiverVault := local, remote
			if direction == SyncPull {
				sourceVault, receiverVault = remote, local
			}
			newer := peerGrant("new", time.Now().Add(2*time.Hour))
			source := capturePeerProfile(t, writePeerProfile(t, sourceVault, "work", newer, "team", "meta.json"))
			previous := capturePeerProfile(t, writePeerProfile(t, receiverVault, "work", peerGrant("old", time.Now().Add(time.Hour)), "team", "meta.json"))
			retained, foreign := filepath.Join(base, "retained"), filepath.Join(base, "foreign")
			if err := os.Mkdir(foreign, 0700); err != nil {
				t.Fatal(err)
			}
			stageName := ""
			rebind := func(message *peerMessage) error {
				if message.Kind != "check_source" || stageName != "" {
					return nil
				}
				entries, err := os.ReadDir(receiverVault)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "_caam-vault-backup~sync-incoming-") {
						stageName = entry.Name()
						break
					}
				}
				if stageName == "" {
					return errors.New("missing incoming stage")
				}
				if err := os.Rename(receiverVault, retained); err != nil {
					return err
				}
				if err := os.Symlink(foreign, receiverVault); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				if err := os.Mkdir(filepath.Join(foreign, stageName), 0700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(foreign, stageName, "private.json"), []byte("independent"), 0600)
			}
			var send, read peerObserver
			if direction == SyncPush {
				read = rebind
			} else {
				send = rebind
			}
			_, err := runPeerSync(t, local, remote, ModeReplicate, send, read)
			if err == nil || stageName == "" {
				t.Fatal("vault rebind did not interrupt publication")
			}
			data, err := os.ReadFile(filepath.Join(foreign, stageName, "private.json"))
			if err != nil || string(data) != "independent" {
				t.Fatal("cleanup touched foreign stage")
			}
			data, err = os.ReadFile(filepath.Join(retained, stageName, ".credentials.json"))
			if err != nil || !bytes.Equal(data, newer) {
				t.Fatal("uncertain incoming stage was deleted")
			}
			if source.Check() != nil || capturePeerProfile(t, filepath.Join(retained, "claude", "work")).Digest() != previous.Digest() {
				t.Fatal("vault rebind changed profile data")
			}
			if _, err := os.Stat(filepath.Join(foreign, "claude", "work")); !os.IsNotExist(err) {
				t.Fatal("publication escaped to rebound vault")
			}
		})
	}
}
func TestPeerIncomingStageCleanupPreservesReusedName(t *testing.T) {
	vault := t.TempDir()
	stage, cleanup, err := newPeerIncomingStage(vault)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(stage, "auth.json"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(vault, "retained-stage")
	if err := os.Rename(stage, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "auth.json"), []byte("independent"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	for path, want := range map[string]string{retained: "original", stage: "independent"} {
		data, err := os.ReadFile(filepath.Join(path, "auth.json"))
		if err != nil || string(data) != want {
			t.Fatal("cleanup removed an unowned stage")
		}
	}
}

type inertPeer struct{ reader *bytes.Reader }

func (p *inertPeer) Read(data []byte) (int, error) { return p.reader.Read(data) }
func (*inertPeer) Write(data []byte) (int, error)  { return len(data), nil }
func (*inertPeer) Close() error                    { return nil }
func TestGuardedSyncUnsupportedPeerNeverFallsBackOrExposesOutput(t *testing.T) {
	local, remote := filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "remote")
	before := capturePeerProfile(t, writePeerProfile(t, local, "work", peerGrant("new", time.Now().Add(time.Hour)), "team", "meta.json"))
	syncer := &Syncer{vaultPath: local, remoteVaultPath: remote, policy: NewPolicyResolver(map[string]string{"claude": "replicate"}, nil), peerDial: func(context.Context, *SSHClient) (io.ReadWriteCloser, error) {
		return &inertPeer{reader: bytes.NewReader([]byte("unknown command synthetic-private-access"))}, nil
	}}
	_, err := syncer.syncProfileOperation(context.Background(), nil, nil, ProfileRef{Provider: "claude", Profile: "work"})
	if !errors.Is(err, errPeerUnavailable) || strings.Contains(err.Error(), "synthetic-") || before.Check() != nil {
		t.Fatalf("unsupported peer did not fail confidentially: %v", err)
	}
	if _, err := os.Stat(remote); !os.IsNotExist(err) {
		t.Fatal("unsupported peer caused fallback writes")
	}
}

func TestSSHPeerSetupCancellationBoundsUnresponsivePeer(t *testing.T) {
	for _, phase := range []string{"session", "start", "stream"} {
		t.Run(phase, func(t *testing.T) {
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := ssh.NewSignerFromKey(key)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				defer connection.Close()
				config := &ssh.ServerConfig{NoClientAuth: true}
				config.AddHostKey(signer)
				server, channels, requests, err := ssh.NewServerConn(connection, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					if phase == "session" {
						continue
					}
					accepted, requests, err := channel.Accept()
					if err != nil {
						return
					}
					defer accepted.Close()
					for request := range requests {
						if phase == "stream" && request.Type == "exec" {
							_ = request.Reply(true, nil)
						}
					}
				}
			}()
			raw, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			client := &SSHClient{}
			client.client.Store(raw)
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			connection, err := openSSHPeer(ctx, client)
			if phase == "stream" {
				if err != nil {
					t.Fatalf("stream setup failed: %v", err)
				}
				stop := closePeerOnCancellation(ctx, connection)
				_, err = connection.Write(bytes.Repeat([]byte("x"), 4<<20))
				stop()
				_ = connection.Close()
				if err == nil {
					t.Fatal("blocked write was not interrupted")
				}
			} else if err == nil {
				t.Fatal("unresponsive setup succeeded")
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("cancellation did not bound SSH")
			}
			if client.IsConnected() || client.Conn() != nil {
				t.Fatal("canceled SSH transport remains reusable")
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("SSH fixture did not close")
			}
		})
	}
}
func TestSSHPeerCancellationReconnectsForLaterProfiles(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyData, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "peer_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyData}), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverContext, stopServer := context.WithCancel(context.Background())
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	missingDigest := capturePeerProfile(t, filepath.Join(t.TempDir(), "absent")).Digest()
	firstExec := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for generation := 1; ; generation++ {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func(generation int) {
				defer workers.Done()
				defer connection.Close()
				stop := context.AfterFunc(serverContext, func() { _ = connection.Close() })
				defer stop()
				server, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					accepted, requests, err := channel.Accept()
					if err != nil {
						return
					}
					func() {
						defer accepted.Close()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							if generation == 1 {
								close(firstExec)
								continue
							}
							if request.Reply(true, nil) != nil {
								return
							}
							stream := &peerStream{connection: accepted}
							message, err := stream.receive()
							if err != nil || message.Kind != "open" {
								return
							}
							if stream.send(peerMessage{Kind: "snapshot", Snapshot: &peerSnapshot{Digest: missingDigest}}) != nil {
								return
							}
							if stream.send(peerMessage{Kind: "snapshot_end"}) != nil {
								return
							}
							message, err = stream.receive()
							if err == nil && message.Kind == "finish" {
								_ = stream.send(peerMessage{Kind: "result", OK: true})
							}
							return
						}
					}()
				}
			}(generation)
		}
	}()
	machine := NewMachine("retry-peer", "127.0.0.1")
	machine.Port = listener.Addr().(*net.TCPAddr).Port
	machine.SSHUser = "test"
	machine.SSHKeyPath = keyPath
	pool := NewConnectionPool(ConnectOptions{Timeout: time.Second, SkipHostKeyCheck: true})
	t.Cleanup(func() {
		stopServer()
		_ = listener.Close()
		pool.CloseAll()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("SSH reconnect fixture did not stop")
		}
	})
	retired, err := pool.Get(machine)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, err = openSSHPeer(ctx, retired)
	cancel()
	if err == nil || retired.IsConnected() || pool.IsConnected(machine.ID) {
		t.Fatal("canceled transport remains reusable")
	}
	select {
	case <-firstExec:
	default:
		t.Fatal("fixture did not block the exec request")
	}
	syncer := &Syncer{pool: pool, vaultPath: filepath.Join(t.TempDir(), "local"), remoteVaultPath: "unused-vault", policy: NewPolicyResolver(map[string]string{"claude": string(ModeReplicate)}, nil)}
	retryContext, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	// The per-machine loop still holds this retired local client variable.
	result, err := syncer.syncProfileOperation(retryContext, retired, machine, ProfileRef{Provider: "claude", Profile: "next"})
	if err != nil || result == nil || result.Error != nil || !result.Success {
		t.Fatalf("next profile did not reconnect: %v", err)
	}
	reconnected, err := pool.Get(machine)
	if err != nil || reconnected == retired || !reconnected.IsConnected() {
		t.Fatal("pool did not retain replacement")
	}
	previous := reconnected.Conn()
	if err := pool.Refresh(); err != nil {
		t.Fatal(err)
	}
	replacement := reconnected.Conn()
	if replacement == nil || replacement == previous {
		t.Fatal("fixture did not refresh transport")
	}
	_ = reconnected.invalidateTransport(previous)
	if reconnected.Conn() != replacement || !reconnected.IsConnected() {
		t.Fatal("late old abort retired new transport")
	}
	result, err = syncer.syncProfileOperation(retryContext, reconnected, machine, ProfileRef{Provider: "claude", Profile: "after-refresh"})
	if err != nil || result == nil || result.Error != nil || !result.Success {
		t.Fatalf("new generation unusable after stale abort: %v", err)
	}
}
func TestPeerStreamCancellationReleasesManagedLock(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote")
	writePeerProfile(t, remote, "work", peerGrant("team", time.Now().Add(time.Hour)), "team", "meta.json")
	ctx, cancel := context.WithCancel(context.Background())
	connection, err := peerProcessDial(t, nil, nil)(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := &peerStream{connection: connection}
	if err := stream.send(peerMessage{Kind: "open", Vault: remote, Provider: "claude", Profile: "work", MetadataOnly: true}); err != nil {
		t.Fatal(err)
	}
	message, err := stream.receive()
	if err != nil || message.Kind != "snapshot" {
		t.Fatalf("peer did not hold lease: %v", err)
	}
	cancel()
	_ = connection.Close()
	if err := authfile.NewVault(remote).WithMutationLock(func() error { return nil }); err != nil {
		t.Fatalf("canceled helper kept its lock: %v", err)
	}
}
