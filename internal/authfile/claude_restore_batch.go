package authfile

import (
	"sync"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
)

// Claude's per-file Restore loop references one shared batch. It is applied
// once, regardless of which document occurs first in the file set. Settings,
// legacy identity, Desktop caches and missing OAuth sources are staged before
// any is changed. Present OAuth snapshots still use Restore's freshness guard;
// the final keychain write remains outside this file batch.
type claudeRestoreBatch struct {
	updates     []*claudesettings.Update
	retirements []*claudeCredentialRetirement
	once        sync.Once
	err         error
}

func (batch *claudeRestoreBatch) Apply() error {
	batch.once.Do(func() {
		removals := make([]*claudesettings.Removal, 0, len(batch.retirements))
		for _, retirement := range batch.retirements {
			removal, err := retirement.prepareRemoval()
			if err != nil {
				batch.err = err
				return
			}
			removals = append(removals, removal)
		}
		batch.err = claudesettings.ApplyUpdatesWithRemovals(batch.updates, removals)
	})
	return batch.err
}
