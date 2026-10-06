package provider

import (
	"context"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// ProfileRunPreparer applies required policy before an isolated CLI launch.
// Unlike the best-effort ProfileRefresher for convenience assets, a failure
// must abort the launch: running stale permissions or account settings is not
// an acceptable fallback. Env remains read-only for inspection and dry runs.
type ProfileRunPreparer interface {
	PrepareRun(ctx context.Context, p *profile.Profile) error
}
