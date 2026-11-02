package cmd

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func syncPeerTestFrame(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var header [8]byte
	binary.BigEndian.PutUint64(header[:], uint64(len(data)))
	return append(header[:], data...)
}
func TestSyncPeerCommandUsesOnlyProtocolAndSkipsNormalHooks(t *testing.T) {
	vaultPath := filepath.Join(t.TempDir(), "vault")
	input := append(syncPeerTestFrame(t, map[string]any{"version": 1, "kind": "open", "vault": vaultPath, "provider": "claude", "profile": "empty", "metadata_only": true}), syncPeerTestFrame(t, map[string]any{"version": 1, "kind": "finish"})...)
	parent := &cobra.Command{Use: "caam", PersistentPreRunE: func(*cobra.Command, []string) error { return fmt.Errorf("normal startup must not run") }, PersistentPostRunE: func(*cobra.Command, []string) error { t.Error("normal shutdown ran"); return nil }}
	child := newSyncPeerCommand()
	parent.AddCommand(child)
	if !child.Hidden || child.Short == "" {
		t.Fatal("peer helper is not a documented hidden command")
	}
	var output bytes.Buffer
	parent.SetIn(bytes.NewReader(input))
	parent.SetOut(&output)
	parent.SetErr(io.Discard)
	parent.SetArgs([]string{"sync-peer", "--protocol-version", "1"})
	if err := parent.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"snapshot", "snapshot_end", "result"} {
		var header [8]byte
		if _, err := io.ReadFull(&output, header[:]); err != nil {
			t.Fatal(err)
		}
		n := binary.BigEndian.Uint64(header[:])
		if n > 1<<20 {
			t.Fatal("unframed output")
		}
		data := make([]byte, int(n))
		if _, err := io.ReadFull(&output, data); err != nil {
			t.Fatal(err)
		}
		var frame struct {
			Version int
			Kind    string
		}
		if json.Unmarshal(data, &frame) != nil || frame.Version != 1 || frame.Kind != want {
			t.Fatalf("unexpected protocol frame: %q", frame.Kind)
		}
	}
	if output.Len() != 0 {
		t.Fatal("helper emitted nonprotocol output")
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "claude", "empty")); !os.IsNotExist(err) {
		t.Fatal("empty exchange created routable profile")
	}
}
func TestSyncPeerCommandRequiresSupportedVersion(t *testing.T) {
	for _, args := range [][]string{nil, {"--protocol-version", "0"}, {"--protocol-version", "2"}} {
		command := newSyncPeerCommand()
		var output bytes.Buffer
		command.SetArgs(args)
		command.SetIn(bytes.NewReader([]byte("synthetic-private-input")))
		command.SetOut(&output)
		command.SetErr(io.Discard)
		if err := command.Execute(); err == nil {
			t.Fatal("unsupported protocol accepted")
		}
		if output.Len() != 0 {
			t.Fatal("unsupported protocol emitted input or output")
		}
	}
}
