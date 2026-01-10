package notify

import (
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
