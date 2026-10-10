package keepalive

import (
	"context"
	"errors"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// SyncVaultFromLive copies a newer Grok login from its live home into saved
// snapshots of the same account, without running Grok or contacting any token
// endpoint. Grok renews ~/.grok/auth.json itself; the snapshot made by
// `caam backup grok <name>` goes stale within hours, and a stale snapshot makes
// the read-only quota probe report "grok login needs refreshing".
//
// It reuses SyncVault's proofs: same account (never compared by token),
// refresh token present in the live file, later expiry than the snapshot,
// user-snapshot metadata, atomic replacement under the native credential lock.
// Anything unproven leaves the vault untouched. Only unblocked live homes that
// own their grant are considered, so a duplicate owner cannot be the source.
func SyncVaultFromLive(ctx context.Context, vault *authfile.Vault, opts DiscoverOptions) ([]SyncResult, error) {
	if vault == nil {
		return nil, errors.New("keepalive vault is not configured")
	}
	opts.Provider = "grok"
	if opts.VaultPath == "" {
		opts.VaultPath = vault.BasePath()
	}
	grants, err := Discover(ctx, opts)
	if err != nil {
		return nil, err
	}
	var (
		results  []SyncResult
		failures []error
	)
	for _, grant := range grants {
		if grant.BlockedReason != "" || grant.Owner != "" {
			continue
		}
		observed, err := ReadCredential(grant)
		if err != nil {
			continue
		}
		synced, err := SyncVault(ctx, grant, observed, vault)
		results = append(results, synced...)
		if err != nil {
			failures = append(failures, err)
		}
	}
	return results, errors.Join(failures...)
}

// Synced counts the snapshots a SyncVaultFromLive call replaced.
func Synced(results []SyncResult) int {
	n := 0
	for _, r := range results {
		if r.Status == "synced" {
			n++
		}
	}
	return n
}
