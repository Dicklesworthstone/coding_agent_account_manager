package cmd

import (
	"fmt"
	"io"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/spf13/cobra"
)

func newSyncPeerCommand() *cobra.Command {
	var protocol int
	command := &cobra.Command{
		Use:           "sync-peer",
		Short:         "Exchange a guarded profile snapshot over SSH",
		Hidden:        true,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Protocol stdout must not contain normal migrations or CLI status.
		PersistentPreRunE:  func(*cobra.Command, []string) error { return nil },
		PersistentPostRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			if protocol != sync.PeerProtocolVersion {
				return fmt.Errorf("unsupported safe sync protocol")
			}
			return sync.ServePeer(cmd.Context(), &syncPeerStdio{reader: cmd.InOrStdin(), writer: cmd.OutOrStdout()})
		},
	}
	command.Flags().IntVar(&protocol, "protocol-version", 0, "required safe sync protocol version")
	return command
}
func init() { rootCmd.AddCommand(newSyncPeerCommand()) }

type syncPeerStdio struct {
	reader io.Reader
	writer io.Writer
}

func (s *syncPeerStdio) Read(data []byte) (int, error)  { return s.reader.Read(data) }
func (s *syncPeerStdio) Write(data []byte) (int, error) { return s.writer.Write(data) }
func (s *syncPeerStdio) Close() error {
	if closer, ok := s.reader.(io.Closer); ok {
		_ = closer.Close()
	}
	if closer, ok := s.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
