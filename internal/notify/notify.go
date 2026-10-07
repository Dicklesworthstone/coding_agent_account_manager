package notify

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// AlertLevel represents the severity of an alert.
type AlertLevel int

const (
	Info AlertLevel = iota
	Warning
	Critical
)

func (l AlertLevel) String() string {
	switch l {
	case Info:
		return "INFO"
	case Warning:
		return "WARNING"
	case Critical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// Alert represents a notification to be delivered.
type Alert struct {
	Level     AlertLevel
	Title     string
	Message   string
	Profile   string
	Timestamp time.Time
	Action    string // Suggested action for the user
}

// Notifier defines the interface for delivering notifications.
type Notifier interface {
	// Notify delivers an alert.
	Notify(alert *Alert) error

	// Name returns the name of the notifier (e.g., "terminal", "desktop").
	Name() string

	// Available checks if the notifier can be used in the current environment.
	Available() bool
}

// Summary renders an alert as one line: "[caam] WARNING Title: message
// (profile) — action".
func Summary(alert *Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[caam] %s %s", alert.Level, alert.Title)
	if alert.Message != "" {
		b.WriteString(": " + alert.Message)
	}
	if alert.Profile != "" {
		b.WriteString(" (" + alert.Profile + ")")
	}
	if alert.Action != "" {
		b.WriteString(" — " + alert.Action)
	}
	return b.String()
}

// Options selects the channels a notifier delivers to.
type Options struct {
	// Terminal writes alerts to TerminalWriter (stderr when nil).
	Terminal       bool
	TerminalWriter io.Writer
	TerminalColor  bool
	// Desktop shows OS notifications where a notification command exists.
	Desktop bool
	// Webhook posts alerts as JSON to this URL when non-empty.
	Webhook string
}

// New returns a notifier delivering to every selected channel. With no
// channel selected it returns a notifier that drops alerts, so callers never
// need a nil check.
func New(opts Options) Notifier {
	var notifiers []Notifier
	if opts.Terminal {
		notifiers = append(notifiers, NewTerminalNotifier(opts.TerminalWriter, opts.TerminalColor))
	}
	if opts.Desktop {
		notifiers = append(notifiers, NewDesktopNotifier())
	}
	if strings.TrimSpace(opts.Webhook) != "" {
		notifiers = append(notifiers, NewWebhookNotifier(strings.TrimSpace(opts.Webhook)))
	}
	return NewMultiNotifier(notifiers...)
}

// Nop returns a notifier that drops every alert.
func Nop() Notifier {
	return NewMultiNotifier()
}

// Throttled suppresses repeats of the same alert (level, title, profile)
// within an interval, so a condition re-detected on every daemon check
// notifies once rather than every few minutes.
type Throttled struct {
	next     Notifier
	interval time.Duration
	now      func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// NewThrottled wraps next with repeat suppression.
func NewThrottled(next Notifier, interval time.Duration) *Throttled {
	return &Throttled{next: next, interval: interval, now: time.Now, last: make(map[string]time.Time)}
}

func (t *Throttled) Name() string    { return t.next.Name() }
func (t *Throttled) Available() bool { return t.next.Available() }

// Notify delivers the alert unless an identical one went out within the
// interval. A failed delivery does not start the quiet period.
func (t *Throttled) Notify(alert *Alert) error {
	key := alert.Level.String() + "\x00" + alert.Title + "\x00" + alert.Profile
	now := t.now()

	t.mu.Lock()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		t.mu.Unlock()
		return nil
	}
	t.last[key] = now
	t.mu.Unlock()

	if err := t.next.Notify(alert); err != nil {
		t.mu.Lock()
		delete(t.last, key)
		t.mu.Unlock()
		return err
	}
	return nil
}
