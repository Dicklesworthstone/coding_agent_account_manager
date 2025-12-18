package signals

import "os"

// Handler listens for OS signals and exposes normalized channels.
//
// Reload:    SIGHUP (Unix)
// Shutdown:  SIGINT/SIGTERM (Unix), os.Interrupt (Windows)
// DumpStats: SIGUSR1 (Unix)
type Handler struct {
	reload   chan struct{}
	shutdown chan os.Signal
	dump     chan struct{}

	stop func()
}

// New starts a signal handler for the current process.
func New() (*Handler, error) {
	return newHandler()
}

func (h *Handler) Reload() <-chan struct{} {
	if h == nil {
		return nil
	}
	return h.reload
}

func (h *Handler) Shutdown() <-chan os.Signal {
	if h == nil {
		return nil
	}
	return h.shutdown
}

func (h *Handler) DumpStats() <-chan struct{} {
	if h == nil {
		return nil
	}
	return h.dump
}

func (h *Handler) Close() error {
	if h == nil || h.stop == nil {
		return nil
	}
	h.stop()
	return nil
}
