package tui

import (
	"os"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/watcher"
)

type profilesChangedMsg struct {
	event watcher.Event
}

type watcherReadyMsg struct {
	watcher *watcher.Watcher
	err     error
}

type badgeExpiredMsg struct {
	key string
}

type badgeFadeMsg struct {
	key   string
	level int
}

type projectContextLoadedMsg struct {
	cwd      string
	resolved *project.Resolved
	err      error
}

type usageStatsLoadedMsg struct {
	stats []ProfileUsage
	err   error
}

type signalsReadyMsg struct {
	handler *signals.Handler
	err     error
}

type reloadRequestedMsg struct{}

type dumpStatsMsg struct{}

type shutdownRequestedMsg struct {
	sig os.Signal
}

func eventTypeVerb(t watcher.EventType) string {
	switch t {
	case watcher.EventProfileAdded:
		return "added"
	case watcher.EventProfileDeleted:
		return "deleted"
	default:
		return "updated"
	}
}
