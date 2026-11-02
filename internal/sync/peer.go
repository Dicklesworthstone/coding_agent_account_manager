package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

// PeerProtocolVersion identifies the complete, ownership-preserving exchange.
const PeerProtocolVersion = 1

const (
	peerSessionTimeout = 2 * time.Minute
	peerMaxFrameBytes  = 1 << 20
	peerChunkBytes     = 256 << 10
	peerMaxFiles       = 4096
	peerMaxTotalBytes  = 1 << 30
)

var errPeerUnavailable = errors.New("safe sync requires a peer with sync protocol 1; update caam on both machines")

// Credentials travel only within bounded SSH frames, never arguments or errors.
type peerStream struct{ connection io.ReadWriteCloser }
type peerMessage struct {
	Version      int           `json:"version"`
	Kind         string        `json:"kind"`
	Vault        string        `json:"vault,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	Profile      string        `json:"profile,omitempty"`
	MetadataOnly bool          `json:"metadata_only,omitempty"`
	Expected     string        `json:"expected,omitempty"`
	Snapshot     *peerSnapshot `json:"snapshot,omitempty"`
	OK           bool          `json:"ok,omitempty"`
	Code         string        `json:"code,omitempty"`
	Recovery     string        `json:"recovery,omitempty"`
	Name         string        `json:"name,omitempty"`
	Data         []byte        `json:"data,omitempty"`
}
type peerSnapshot struct {
	Exists   bool        `json:"exists"`
	Digest   string      `json:"digest"`
	Entries  []peerEntry `json:"entries,omitempty"`
	source   *vaulttransfer.Snapshot
	metadata []byte
}
type peerEntry struct {
	Name     string `json:"name"`
	Dir      bool   `json:"dir,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Checksum string `json:"checksum,omitempty"`
}

func (p *peerStream) send(message peerMessage) error {
	message.Version = PeerProtocolVersion
	data, err := json.Marshal(message)
	if err != nil || len(data) > peerMaxFrameBytes {
		return errors.New("safe sync frame exceeds the supported size")
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	if _, err := io.Copy(p.connection, bytes.NewReader(size[:])); err != nil {
		return errors.New("safe sync transport closed")
	}
	if _, err := io.Copy(p.connection, bytes.NewReader(data)); err != nil {
		return errors.New("safe sync transport closed")
	}
	return nil
}
func (p *peerStream) receive() (peerMessage, error) {
	var message peerMessage
	var size [8]byte
	if _, err := io.ReadFull(p.connection, size[:]); err != nil {
		return message, errors.New("safe sync transport closed")
	}
	length := binary.BigEndian.Uint64(size[:])
	if length == 0 || length > peerMaxFrameBytes {
		return message, errors.New("invalid safe sync frame")
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(p.connection, data); err != nil {
		return message, errors.New("incomplete safe sync frame")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&message) != nil || message.Version != PeerProtocolVersion {
		return peerMessage{}, errors.New("unsupported safe sync protocol")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return peerMessage{}, errors.New("invalid safe sync frame")
	}
	return message, nil
}

// Cancel both setup and streaming, including callers without a deadline.
func closePeerOnCancellation(ctx context.Context, connection io.Closer) func() {
	bounded, cancel := context.WithTimeout(ctx, peerSessionTimeout)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-bounded.Done():
			select {
			case <-stopped:
				return
			default:
			}
			if aborter, ok := connection.(interface{ Abort() error }); ok {
				_ = aborter.Abort()
			} else {
				_ = connection.Close()
			}
		case <-stopped:
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stopped); cancel() }) }
}

type sshPeerConnection struct {
	reader io.Reader
	writer io.WriteCloser
	close  func() error
	abort  func() error
	once   sync.Once
}

func (c *sshPeerConnection) Read(data []byte) (int, error)  { return c.reader.Read(data) }
func (c *sshPeerConnection) Write(data []byte) (int, error) { return c.writer.Write(data) }
func (c *sshPeerConnection) Close() error {
	c.once.Do(func() { _ = c.writer.Close(); _ = c.close() })
	return nil
}
func (c *sshPeerConnection) Abort() error {
	if c.abort != nil {
		_ = c.abort()
	}
	return c.Close()
}

type peerCloseFunc func() error

func (close peerCloseFunc) Close() error { return close() }

func openSSHPeer(ctx context.Context, client *SSHClient) (io.ReadWriteCloser, error) {
	if client == nil || ctx.Err() != nil {
		return nil, errPeerUnavailable
	}
	transport := client.Conn()
	if transport == nil {
		return nil, errPeerUnavailable
	}
	abort := func() error { return client.invalidateTransport(transport) }
	stop := closePeerOnCancellation(ctx, peerCloseFunc(abort))
	defer stop()
	session, err := transport.NewSession()
	if err != nil {
		return nil, errPeerUnavailable
	}
	input, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, errPeerUnavailable
	}
	output, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, errPeerUnavailable
	}
	session.Stderr = io.Discard
	if err := session.Start("caam sync-peer --protocol-version 1"); err != nil {
		_ = session.Close()
		return nil, errPeerUnavailable
	}
	return &sshPeerConnection{reader: output, writer: input, close: session.Close, abort: abort}, nil
}
func resolvePeerVault(vault string) (string, error) {
	if strings.ContainsRune(vault, '\x00') {
		return "", errors.New("invalid sync vault path")
	}
	if vault == "" {
		return authfile.DefaultVaultPath(), nil
	}
	if filepath.IsAbs(vault) {
		return filepath.Clean(vault), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("sync peer home is unavailable")
	}
	if vault == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(vault, "~/"))), nil
}

// ServePeer serves one exchange under the remote managed mutation lock. It
// never performs online verification or includes request contents in errors.
func ServePeer(ctx context.Context, connection io.ReadWriteCloser) error {
	stop := closePeerOnCancellation(ctx, connection)
	defer stop()
	defer connection.Close()
	stream := &peerStream{connection: connection}
	request, err := stream.receive()
	if err != nil {
		return errors.New("invalid safe sync request")
	}
	ref := ProfileRef{Provider: request.Provider, Profile: request.Profile}
	if request.Kind != "open" || vaulttransfer.ValidateProfileRef(ref.Provider, ref.Profile) != nil || (ref.Provider != "claude" && !request.MetadataOnly) {
		return errors.New("invalid safe sync request")
	}
	vault, err := resolvePeerVault(request.Vault)
	if err != nil {
		return err
	}
	if _, err := vaulttransfer.ProfilePath(vault, ref.Provider, ref.Profile); err != nil {
		return errors.New("invalid peer sync profile path")
	}
	entered := false
	err = authfile.NewVault(vault).WithMutationLock(func() error {
		entered = true
		reject := func(code string) error { return stream.send(peerMessage{Kind: "result", Code: code}) }
		path, err := vaulttransfer.ProfilePath(vault, ref.Provider, ref.Profile)
		if err != nil {
			return reject("invalid_snapshot")
		}
		captured, err := vaulttransfer.Capture(path)
		if err != nil {
			return reject("invalid_snapshot")
		}
		wire, err := snapshotForPeer(captured, ref, request.MetadataOnly)
		if err != nil {
			return reject("invalid_snapshot")
		}
		if err := stream.send(peerMessage{Kind: "snapshot", Snapshot: wire}); err != nil {
			return err
		}
		if err := sendPeerSnapshotData(stream, wire); err != nil {
			return err
		}
		decision, err := stream.receive()
		if err != nil {
			return err
		}
		if decision.Kind == "finish" {
			return stream.send(peerMessage{Kind: "result", OK: true})
		}
		checkSource := func() error {
			if _, err := vaulttransfer.ProfilePath(vault, ref.Provider, ref.Profile); err != nil {
				return err
			}
			return captured.Check()
		}
		if decision.Expected != captured.Digest() || checkSource() != nil {
			return reject("generation_changed")
		}
		switch decision.Kind {
		case "pull":
			if err := stream.send(peerMessage{Kind: "ready", OK: true}); err != nil {
				return err
			}
			for {
				message, err := stream.receive()
				if err != nil {
					return err
				}
				switch message.Kind {
				case "check_source":
					if err := stream.send(peerMessage{Kind: "source_checked", OK: checkSource() == nil}); err != nil {
						return err
					}
				case "finish":
					return stream.send(peerMessage{Kind: "result", OK: true})
				default:
					return reject("invalid_snapshot")
				}
			}
		case "push":
			if request.MetadataOnly && captured.Exists() {
				return reject("destination_exists")
			}
			if decision.Snapshot == nil || !decision.Snapshot.Exists || len(decision.Snapshot.Entries) == 0 ||
				validatePeerSnapshot(decision.Snapshot, request.MetadataOnly) != nil {
				return reject("invalid_snapshot")
			}
			if err := stream.send(peerMessage{Kind: "ready", OK: true}); err != nil {
				return err
			}
			incoming, cleanup, err := receivePeerSnapshot(stream, vault, decision.Snapshot, ref, request.MetadataOnly)
			if err != nil {
				return reject("invalid_snapshot")
			}
			defer cleanup()
			if incoming == nil {
				return reject("invalid_snapshot")
			}
			if !request.MetadataOnly && checkClaudeTransferOwner(incoming, captured) != nil {
				return reject("ownership_conflict")
			}
			provider := ref.Provider
			if request.MetadataOnly {
				provider = ""
			}
			recovery, publicationErr := vaulttransfer.Publish(incoming, captured, &vaulttransfer.Options{
				Provider: provider,
				CheckSource: func() error {
					if _, err := vaulttransfer.ProfilePath(vault, ref.Provider, ref.Profile); err != nil {
						return errors.New("peer vault path changed during publication")
					}
					if err := stream.send(peerMessage{Kind: "check_source"}); err != nil {
						return err
					}
					message, err := stream.receive()
					if err != nil || message.Kind != "source_checked" || !message.OK {
						return errors.New("client source changed during transfer")
					}
					return nil
				},
			})
			code := ""
			if publicationErr != nil {
				code = "publication_failed"
			}
			return stream.send(peerMessage{Kind: "result", OK: publicationErr == nil, Code: code, Recovery: recovery})
		default:
			return reject("invalid_snapshot")
		}
	})
	if !entered && err != nil {
		_ = stream.send(peerMessage{Kind: "result", Code: "lock_unavailable"})
	}
	return err
}

func peerFileLimit(name string) int64 {
	if name == ".claude.json" || name == "settings.json" {
		return 256 << 20
	}
	return 16 << 20
}
func snapshotForPeer(snapshot *vaulttransfer.Snapshot, ref ProfileRef, metadataOnly bool) (*peerSnapshot, error) {
	if err := snapshot.Check(); err != nil {
		return nil, errors.New("sync profile changed")
	}
	wire := &peerSnapshot{Exists: snapshot.Exists(), Digest: snapshot.Digest(), source: snapshot}
	var total int64
	entries := snapshot.Entries()
	if !metadataOnly && len(entries) > peerMaxFiles+1 {
		return nil, errors.New("sync profile has too many entries")
	}
	// Bound transferred files before parsers allocate their contents. Excluded
	// host-local payloads remain part of generation checks, never wire limits.
	for _, entry := range entries {
		if entry.Dir || metadataOnly && entry.Name != "meta.json" {
			continue
		}
		if entry.Size < 0 || entry.Size > peerFileLimit(filepath.ToSlash(entry.Name)) || total > peerMaxTotalBytes-entry.Size {
			return nil, errors.New("sync profile exceeds supported size")
		}
		total += entry.Size
	}
	if !wire.Exists {
		return wire, nil
	}
	if metadataOnly {
		data, exists, err := snapshot.ReadFile("meta.json")
		if err != nil {
			return nil, errors.New("cannot read sync metadata")
		}
		if !exists {
			return wire, nil
		}
		wire.metadata, err = sanitizePeerMetadata(data, ref)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(wire.metadata)
		wire.Entries = []peerEntry{{Name: "meta.json", Size: int64(len(wire.metadata)), Checksum: hex.EncodeToString(sum[:])}}
	} else {
		if _, err := vaulttransfer.InspectClaude(snapshot); err != nil {
			return nil, errors.New("invalid sync claude identity")
		}
		for _, entry := range entries {
			if entry.Name == "." {
				continue
			}
			next := peerEntry{Name: filepath.ToSlash(entry.Name), Dir: entry.Dir}
			if !entry.Dir {
				next.Size = entry.Size
				next.Checksum = entry.Checksum
			}
			wire.Entries = append(wire.Entries, next)
		}
	}
	if err := validatePeerSnapshot(wire, metadataOnly); err != nil {
		return nil, err
	}
	return wire, nil
}
func sanitizePeerMetadata(data []byte, ref ProfileRef) ([]byte, error) {
	var source map[string]json.RawMessage
	if json.Unmarshal(data, &source) != nil || source == nil {
		return nil, errors.New("invalid sync metadata")
	}
	value := struct {
		Tool        string   `json:"tool"`
		Profile     string   `json:"profile"`
		Type        string   `json:"type"`
		Description string   `json:"description,omitempty"`
		Tags        []string `json:"tags,omitempty"`
	}{Tool: ref.Provider, Profile: ref.Profile, Type: "user"}
	if raw, exists := source["description"]; exists && json.Unmarshal(raw, &value.Description) != nil {
		return nil, errors.New("invalid sync description")
	}
	if raw, exists := source["tags"]; exists && json.Unmarshal(raw, &value.Tags) != nil {
		return nil, errors.New("invalid sync tags")
	}
	return json.Marshal(value)
}
func validatePeerSnapshot(wire *peerSnapshot, metadataOnly bool) error {
	if wire == nil || len(wire.Entries) > peerMaxFiles {
		return errors.New("invalid sync snapshot")
	}
	digest, err := hex.DecodeString(wire.Digest)
	if err != nil || len(digest) != sha256.Size || (!wire.Exists && len(wire.Entries) != 0) {
		return errors.New("invalid sync snapshot")
	}
	if metadataOnly && (len(wire.Entries) > 1 || len(wire.Entries) == 1 && (wire.Entries[0].Name != "meta.json" || wire.Entries[0].Dir)) {
		return errors.New("invalid metadata snapshot")
	}
	seen := map[string]bool{}
	prefixes := map[string]string{}
	var total int64
	for _, entry := range wire.Entries {
		if !utf8.ValidString(entry.Name) || vaulttransfer.ValidateRelativePath(entry.Name) != nil {
			return errors.New("invalid sync profile path")
		}
		key := strings.ToLower(entry.Name)
		if _, exists := seen[key]; exists {
			return errors.New("duplicate sync profile path")
		}
		seen[key] = entry.Dir
		parts := strings.Split(entry.Name, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			lower := strings.ToLower(prefix)
			if previous, exists := prefixes[lower]; exists && previous != prefix {
				return errors.New("case-conflicting sync profile paths")
			}
			prefixes[lower] = prefix
		}
		if entry.Dir {
			if entry.Size != 0 || entry.Checksum != "" {
				return errors.New("invalid sync directory")
			}
			continue
		}
		sum, err := hex.DecodeString(entry.Checksum)
		if err != nil || len(sum) != sha256.Size || entry.Size < 0 || entry.Size > peerFileLimit(entry.Name) || total > peerMaxTotalBytes-entry.Size {
			return errors.New("invalid sync profile file")
		}
		total += entry.Size
	}
	for name := range seen {
		parts := strings.Split(name, "/")
		for i := 1; i < len(parts); i++ {
			if isDir, exists := seen[strings.Join(parts[:i], "/")]; exists && !isDir {
				return errors.New("sync file is also a directory")
			}
		}
	}
	return nil
}

type peerChunkWriter struct {
	stream *peerStream
	name   string
}

func (w *peerChunkWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		size := min(len(data), peerChunkBytes)
		if err := w.stream.send(peerMessage{Kind: "chunk", Name: w.name, Data: data[:size]}); err != nil {
			return written, err
		}
		written += size
		data = data[size:]
	}
	return written, nil
}
func sendPeerSnapshotData(stream *peerStream, wire *peerSnapshot) error {
	for _, entry := range wire.Entries {
		if entry.Dir {
			continue
		}
		writer := &peerChunkWriter{stream: stream, name: entry.Name}
		if wire.metadata != nil {
			if _, err := writer.Write(wire.metadata); err != nil {
				return err
			}
		} else {
			exists, err := wire.source.WriteFile(filepath.FromSlash(entry.Name), writer)
			if err != nil || !exists {
				return errors.New("sync source changed during streaming")
			}
		}
		if err := stream.send(peerMessage{Kind: "file_end", Name: entry.Name}); err != nil {
			return err
		}
	}
	if wire.source != nil && wire.source.Check() != nil {
		return errors.New("sync source changed during streaming")
	}
	return stream.send(peerMessage{Kind: "snapshot_end"})
}

// Anchor cleanup to the opened vault and exact reserved stage. A rebound vault
// or independently reused stage name is never followed or removed.
func newPeerIncomingStage(vault string) (string, func(), error) {
	info, err := os.Lstat(vault)
	if err != nil || !info.IsDir() {
		return "", func() {}, errors.New("invalid sync staging vault")
	}
	root, err := os.OpenRoot(vault)
	if err != nil {
		return "", func() {}, errors.New("cannot open sync staging vault")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return "", func() {}, errors.New("sync staging vault changed")
	}
	stage, err := os.MkdirTemp(vault, "_caam-vault-backup~sync-incoming-*")
	if err != nil {
		root.Close()
		return "", func() {}, errors.New("cannot create sync staging directory")
	}
	name := filepath.Base(stage)
	created, err := os.Lstat(stage)
	owned, ownedErr := root.Lstat(name)
	if err != nil || ownedErr != nil || !created.IsDir() || !os.SameFile(created, owned) {
		root.Close()
		return "", func() {}, errors.New("sync staging vault changed")
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			defer root.Close()
			current, err := os.Lstat(vault)
			if err != nil || !current.IsDir() || !os.SameFile(info, current) {
				return
			}
			current, err = root.Lstat(name)
			if err == nil && current.IsDir() && os.SameFile(owned, current) {
				_ = root.RemoveAll(name)
			}
		})
	}
	return stage, cleanup, nil
}

func receivePeerSnapshot(stream *peerStream, vault string, wire *peerSnapshot, ref ProfileRef, metadataOnly bool) (*vaulttransfer.Snapshot, func(), error) {
	noop := func() {}
	if err := validatePeerSnapshot(wire, metadataOnly); err != nil {
		return nil, noop, err
	}
	if !wire.Exists || len(wire.Entries) == 0 {
		message, err := stream.receive()
		if err != nil || message.Kind != "snapshot_end" {
			return nil, noop, errors.New("incomplete sync snapshot")
		}
		return nil, noop, nil
	}
	stage, cleanup, err := newPeerIncomingStage(vault)
	if err != nil {
		return nil, noop, err
	}
	failed := true
	defer func() {
		if failed {
			cleanup()
		}
	}()
	for _, entry := range wire.Entries {
		target := filepath.Join(stage, filepath.FromSlash(entry.Name))
		if entry.Dir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return nil, noop, errors.New("cannot stage sync directory")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, noop, errors.New("cannot stage sync directory")
		}
		if err := receivePeerFile(stream, target, entry); err != nil {
			return nil, noop, err
		}
	}
	message, err := stream.receive()
	if err != nil || message.Kind != "snapshot_end" {
		return nil, noop, errors.New("incomplete sync snapshot")
	}
	if metadataOnly {
		name := filepath.Join(stage, "meta.json")
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, noop, errors.New("cannot read staged sync metadata")
		}
		data, err = sanitizePeerMetadata(data, ref)
		if err != nil {
			return nil, noop, err
		}
		if err := os.WriteFile(name, data, 0600); err != nil {
			return nil, noop, errors.New("cannot normalize staged sync metadata")
		}
	}
	snapshot, err := vaulttransfer.Capture(stage)
	if err != nil {
		return nil, noop, errors.New("cannot capture staged sync profile")
	}
	failed = false
	return snapshot, cleanup, nil
}
func receivePeerFile(stream *peerStream, target string, entry peerEntry) error {
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot stage sync profile file")
	}
	defer file.Close()
	hash := sha256.New()
	var received int64
	for {
		message, err := stream.receive()
		if err != nil || message.Name != entry.Name {
			return errors.New("incomplete sync profile file")
		}
		if message.Kind == "file_end" {
			if received != entry.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), entry.Checksum) {
				return errors.New("sync profile checksum mismatch")
			}
			if file.Sync() != nil {
				return errors.New("cannot sync staged profile file")
			}
			if file.Close() != nil {
				return errors.New("cannot close staged profile file")
			}
			return nil
		}
		if message.Kind != "chunk" || len(message.Data) == 0 || len(message.Data) > peerChunkBytes || int64(len(message.Data)) > entry.Size-received {
			return errors.New("invalid sync profile chunk")
		}
		if _, err := io.Copy(io.MultiWriter(file, hash), bytes.NewReader(message.Data)); err != nil {
			return errors.New("cannot stage sync profile file")
		}
		received += int64(len(message.Data))
	}
}
func checkClaudeTransferOwner(source, destination *vaulttransfer.Snapshot) error {
	incoming, err := vaulttransfer.InspectClaude(source)
	if err != nil {
		return errors.New("invalid incoming claude identity")
	}
	if !destination.Exists() {
		return nil
	}
	previous, err := vaulttransfer.InspectClaude(destination)
	if err != nil {
		return errors.New("invalid destination claude identity")
	}
	return vaulttransfer.CheckClaudeReplacement(previous, incoming)
}
func peerResultError(message peerMessage) error {
	switch message.Code {
	case "ownership_conflict":
		return errors.New("peer rejected different or unverified claude ownership; existing profile preserved")
	case "generation_changed", "destination_exists":
		return errors.New("peer profile changed during sync; retry")
	case "lock_unavailable":
		return errors.New("peer vault is busy; retry")
	case "publication_failed":
		return errors.New("peer could not publish the guarded snapshot; any previous snapshot was retained for recovery")
	case "invalid_snapshot":
		return errors.New("peer profile has invalid credentials, identity metadata, or file structure")
	default:
		return errors.New("peer rejected the safe sync request")
	}
}
func finishPeer(stream *peerStream) error {
	if err := stream.send(peerMessage{Kind: "finish"}); err != nil {
		return err
	}
	message, err := stream.receive()
	if err != nil || message.Kind != "result" {
		return errors.New("safe sync peer did not complete the exchange")
	}
	if !message.OK {
		return peerResultError(message)
	}
	return nil
}

// Both managed locks remain held through publication and source rechecks.
// Opposite concurrent syncs fail boundedly if they cannot acquire both leases.
func (s *Syncer) syncGuardedProfile(ctx context.Context, client *SSHClient, machine *Machine, ref ProfileRef) (*SyncResult, error) {
	started := time.Now()
	op := &SyncOperation{Provider: ref.Provider, Profile: ref.Profile, Machine: machine, Mode: s.policy.ModeFor(ref.Provider, ref.Profile), Direction: SyncSkip}
	op.PayloadExcluded = op.Mode == ModeHostLocal
	result := &SyncResult{Operation: op}
	if authfile.IsSystemProfile(ref.Profile) {
		result.Success = true
		return result, nil
	}
	if _, err := vaulttransfer.ProfilePath(s.vaultPath, ref.Provider, ref.Profile); err != nil {
		return nil, errors.New("invalid local sync profile path")
	}
	planned := false
	err := authfile.NewVault(s.vaultPath).WithMutationLock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		localPath, err := vaulttransfer.ProfilePath(s.vaultPath, ref.Provider, ref.Profile)
		if err != nil {
			return errors.New("invalid local sync profile path")
		}
		local, err := vaulttransfer.Capture(localPath)
		if err != nil {
			return errors.New("cannot capture local sync profile")
		}
		dial := s.peerDial
		if dial == nil {
			dial = openSSHPeer
		}
		connection, err := dial(ctx, client)
		if err != nil {
			return errPeerUnavailable
		}
		defer connection.Close()
		stop := closePeerOnCancellation(ctx, connection)
		defer stop()
		stream := &peerStream{connection: connection}
		if err := stream.send(peerMessage{Kind: "open", Vault: s.remoteVaultForMachine(machine), Provider: ref.Provider, Profile: ref.Profile, MetadataOnly: op.PayloadExcluded}); err != nil {
			return errPeerUnavailable
		}
		response, err := stream.receive()
		if err != nil {
			return errPeerUnavailable
		}
		if response.Kind == "result" {
			return peerResultError(response)
		}
		if response.Kind != "snapshot" || response.Snapshot == nil {
			return errPeerUnavailable
		}
		remoteWire := response.Snapshot
		remote, cleanup, err := receivePeerSnapshot(stream, s.vaultPath, remoteWire, ref, op.PayloadExcluded)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := planGuardedSync(op, local, remote, remoteWire); err != nil {
			return err
		}
		planned = true
		if op.Direction == SyncSkip {
			return finishPeer(stream)
		}
		if local.Check() != nil {
			return errors.New("local sync profile changed; retry")
		}
		if op.Direction == SyncPull {
			if op.PayloadExcluded && local.Exists() {
				return errors.New("host-local metadata cannot replace an existing profile")
			}
			if !op.PayloadExcluded {
				if err := checkClaudeTransferOwner(remote, local); err != nil {
					return err
				}
			}
			if err := stream.send(peerMessage{Kind: "pull", Expected: remoteWire.Digest}); err != nil {
				return err
			}
			ready, err := stream.receive()
			if err != nil || ready.Kind != "ready" || !ready.OK {
				return errors.New("peer source changed before transfer; retry")
			}
			provider := ref.Provider
			if op.PayloadExcluded {
				provider = ""
			}
			recovery, publicationErr := vaulttransfer.Publish(remote, local, &vaulttransfer.Options{
				Provider: provider,
				CheckSource: func() error {
					if _, err := vaulttransfer.ProfilePath(s.vaultPath, ref.Provider, ref.Profile); err != nil {
						return errors.New("local vault path changed during publication")
					}
					if err := stream.send(peerMessage{Kind: "check_source"}); err != nil {
						return err
					}
					checked, err := stream.receive()
					if err != nil || checked.Kind != "source_checked" || !checked.OK {
						return errors.New("peer source changed during transfer; retry")
					}
					return nil
				},
			})
			result.RecoveryPath = recovery
			finishErr := finishPeer(stream)
			if publicationErr != nil {
				return errors.New("local guarded publication failed; any previous snapshot was retained for recovery")
			}
			return finishErr
		}
		outgoing, err := snapshotForPeer(local, ref, op.PayloadExcluded)
		if err != nil {
			return err
		}
		if err := stream.send(peerMessage{Kind: "push", Expected: remoteWire.Digest, Snapshot: outgoing}); err != nil {
			return err
		}
		ready, err := stream.receive()
		if err != nil {
			return err
		}
		if ready.Kind == "result" {
			result.RecoveryPath = ready.Recovery
			return peerResultError(ready)
		}
		if ready.Kind != "ready" || !ready.OK {
			return errors.New("peer rejected sync snapshot")
		}
		if err := sendPeerSnapshotData(stream, outgoing); err != nil {
			return err
		}
		for {
			message, err := stream.receive()
			if err != nil {
				return err
			}
			switch message.Kind {
			case "check_source":
				_, pathErr := vaulttransfer.ProfilePath(s.vaultPath, ref.Provider, ref.Profile)
				if err := stream.send(peerMessage{Kind: "source_checked", OK: pathErr == nil && local.Check() == nil}); err != nil {
					return err
				}
			case "result":
				result.RecoveryPath = message.Recovery
				if !message.OK {
					return peerResultError(message)
				}
				return nil
			default:
				return errors.New("peer returned an invalid sync response")
			}
		}
	})
	result.Duration = time.Since(started)
	result.Error = err
	result.Success = err == nil
	if !planned && err != nil {
		return nil, err
	}
	return result, nil
}
func planGuardedSync(op *SyncOperation, local, remote *vaulttransfer.Snapshot, wire *peerSnapshot) error {
	if op.PayloadExcluded {
		applyHostLocalDecision(op, local.Exists(), wire.Exists, nil, nil, false)
		switch op.Direction {
		case SyncPush:
			_, exists, err := local.ReadFile("meta.json")
			if err != nil {
				return errors.New("cannot read local sync metadata")
			}
			downgradeIfNoMetadata(op, exists)
		case SyncPull:
			downgradeIfNoMetadata(op, remote != nil)
		}
		return nil
	}
	if !local.Exists() && !wire.Exists {
		return nil
	}
	if wire.Exists && remote == nil {
		return errors.New("peer profile has no transferable credentials")
	}
	if !local.Exists() {
		if _, err := vaulttransfer.InspectClaude(remote); err != nil {
			return errors.New("invalid peer claude identity")
		}
		op.Direction = SyncPull
		return nil
	}
	if !wire.Exists {
		if _, err := vaulttransfer.InspectClaude(local); err != nil {
			return errors.New("invalid local claude identity")
		}
		op.Direction = SyncPush
		return nil
	}
	localIdentity, err := vaulttransfer.InspectClaude(local)
	if err != nil {
		return errors.New("invalid local claude identity")
	}
	remoteIdentity, err := vaulttransfer.InspectClaude(remote)
	if err != nil {
		return errors.New("invalid peer claude identity")
	}
	// Conflict takes priority over equal or unavailable expiry. One-direction
	// proof promotion remains possible; actual transfer cannot discard proof.
	if vaulttransfer.CheckClaudeReplacement(localIdentity, remoteIdentity) != nil && vaulttransfer.CheckClaudeReplacement(remoteIdentity, localIdentity) != nil {
		return errors.New("claude ownership differs or is unverified; existing profiles preserved")
	}
	localFresh, localErr := capturedClaudeFreshness(local, op.Profile)
	remoteFresh, remoteErr := capturedClaudeFreshness(remote, op.Profile)
	op.LocalFreshness = localFresh
	op.RemoteFreshness = remoteFresh
	if localErr != nil || remoteErr != nil || localFresh == nil || remoteFresh == nil || localFresh.ExpiresAt.IsZero() || remoteFresh.ExpiresAt.IsZero() {
		op.Note = "selected Claude credential expiry is unavailable; existing profiles preserved"
		return nil
	}
	switch {
	case localFresh.ExpiresAt.After(remoteFresh.ExpiresAt):
		if vaulttransfer.CheckClaudeReplacement(remoteIdentity, localIdentity) != nil {
			return errors.New("claude ownership differs or is unverified; existing profiles preserved")
		}
		op.Direction = SyncPush
	case remoteFresh.ExpiresAt.After(localFresh.ExpiresAt):
		if vaulttransfer.CheckClaudeReplacement(localIdentity, remoteIdentity) != nil {
			return errors.New("claude ownership differs or is unverified; existing profiles preserved")
		}
		op.Direction = SyncPull
	default:
		return nil
	}
	warnIfRotatingReplicate(op)
	return nil
}
func capturedClaudeFreshness(snapshot *vaulttransfer.Snapshot, profile string) (*TokenFreshness, error) {
	for _, name := range []string{".credentials.json", "auth.json", ".claude.json"} {
		data, exists, err := snapshot.ReadFile(name)
		if err != nil {
			return nil, errors.New("cannot read selected claude credentials")
		}
		if exists {
			return ExtractFreshnessFromBytes("claude", profile, map[string][]byte{name: data})
		}
	}
	return nil, errors.New("no selected claude credentials")
}
