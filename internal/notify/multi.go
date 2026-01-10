package notify

import (
	"errors"
	"sync"
)

// MultiNotifier delivers alerts to multiple notifiers.
type MultiNotifier struct {
	notifiers []Notifier
}

func NewMultiNotifier(notifiers ...Notifier) *MultiNotifier {
	return &MultiNotifier{
		notifiers: notifiers,
	}
}

func (m *MultiNotifier) Name() string {
	return "multi"
}

func (m *MultiNotifier) Available() bool {
	for _, n := range m.notifiers {
		if n.Available() {
			return true
		}
	}
	return false
}

func (m *MultiNotifier) Notify(alert *Alert) error {
	var errs []error
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, n := range m.notifiers {
		if n.Available() {
			wg.Add(1)
			go func(notifier Notifier) {
				defer wg.Done()
				if err := notifier.Notify(alert); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}(n)
		}
	}
	wg.Wait()

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
