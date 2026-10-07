package authfile

import (
	"sync"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

// All raw credentials, mixed settings, legacy identity and Desktop caches share
// one recovery batch. Publication to the native keychain happens last, while
// the file rollback copies are still available. Repeated Restore visits reuse
// the same result. This is returned-error recovery, not crash atomicity.
type claudeRestoreBatch struct {
	updates     []*claudesettings.Update
	credentials []*claudeCredentialRestore
	retirements []*claudeCredentialRetirement
	authority   *claudeRestoreAuthority
	once        sync.Once
	err         error
}

func (batch *claudeRestoreBatch) Apply() error {
	batch.once.Do(func() {
		// Raw freshness decisions use the captured outgoing identity, before
		// the mixed-state documents are replaced. Their guards must therefore
		// run first, independent of the caller's file-set ordering.
		updates := make([]*claudesettings.Update, 0, len(batch.credentials)+len(batch.updates))
		for _, credential := range batch.credentials {
			update, after, err := credential.prepareUpdate()
			if err != nil {
				batch.err = err
				return
			}
			updates = append(updates, update)
			if batch.authority != nil && credential.live == batch.authority.path {
				batch.authority.want = after
			}
		}
		updates = append(updates, batch.updates...)
		removals := make([]*claudesettings.Removal, 0, len(batch.retirements))
		for _, retirement := range batch.retirements {
			removal, err := retirement.prepareRemoval()
			if err != nil {
				batch.err = err
				return
			}
			removals = append(removals, removal)
		}
		var finalize func() error
		if batch.authority != nil {
			defer batch.authority.cleanup()
			if err := batch.authority.stage(); err != nil {
				batch.err = err
				return
			}
			finalize = batch.authority.publish
		}
		batch.err = claudesettings.ApplyUpdatesWithFinalizer(updates, removals, finalize)
	})
	return batch.err
}
