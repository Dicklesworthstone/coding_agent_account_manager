//go:build windows

package signals

import (
	"os"
	"os/signal"
	"sync"
)

func newHandler() (*Handler, error) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	h := &Handler{
		reload:   make(chan struct{}, 1),
		shutdown: make(chan os.Signal, 1),
		dump:     make(chan struct{}, 1),
	}

	done := make(chan struct{})
	var once sync.Once

	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-sigChan:
				select {
				case h.shutdown <- sig:
				default:
				}
			}
		}
	}()

	h.stop = func() {
		once.Do(func() {
			signal.Stop(sigChan)
			close(done)
		})
	}

	return h, nil
}
