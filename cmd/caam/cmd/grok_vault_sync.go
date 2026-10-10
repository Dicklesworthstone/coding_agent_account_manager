package cmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keepalive"
)

// grokVaultSyncTimeout bounds the local file work of a live-to-vault sync so a
// busy native credential lock cannot stall a limits sweep.
const grokVaultSyncTimeout = 10 * time.Second

// syncGrokVaultFromLive keeps saved Grok snapshots usable without operator
// action: Grok renews its own ~/.grok/auth.json, and this copies that newer
// login into saved snapshots of the same account (see keepalive.SyncVaultFromLive).
// It reads local files only, never runs Grok, and every failure is non-fatal:
// the caller's own freshness check still decides what the snapshot can do.
// It returns the number of snapshots replaced.
func syncGrokVaultFromLive(ctx context.Context, vault *authfile.Vault, warn io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, grokVaultSyncTimeout)
	defer cancel()
	results, err := keepalive.SyncVaultFromLive(ctx, vault, keepalive.DiscoverOptions{})
	if err != nil && warn != nil {
		fmt.Fprintf(warn, "warning: grok vault sync: %v\n", err)
	}
	return keepalive.Synced(results)
}
