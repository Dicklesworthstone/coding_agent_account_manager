package notify

import (
	"fmt"
	"os/exec"
	"runtime"
)

// DesktopNotifier delivers alerts via desktop notifications.
type DesktopNotifier struct{}

func NewDesktopNotifier() *DesktopNotifier {
	return &DesktopNotifier{}
}

func (n *DesktopNotifier) Name() string {
	return "desktop"
}

func (n *DesktopNotifier) Available() bool {
	switch runtime.GOOS {
	case "linux":
		_, err := exec.LookPath("notify-send")
		return err == nil
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	default:
		return false
	}
}

func (n *DesktopNotifier) Notify(alert *Alert) error {
	if !n.Available() {
		return fmt.Errorf("desktop notifications not available; install notify-send (Linux) or ensure osascript is available (macOS)")
	}

	title := alert.Title
	message := alert.Message
	if alert.Profile != "" {
		message = fmt.Sprintf("[%s] %s", alert.Profile, message)
	}

	switch runtime.GOOS {
	case "linux":
		urgency := "normal"
		if alert.Level == Critical {
			urgency = "critical"
		}
		return exec.Command("notify-send", "-u", urgency, title, message).Run()
	case "darwin":
		// osascript -e 'display notification "message" with title "title"'
		script := fmt.Sprintf(`display notification "%s" with title "%s"`, message, title)
		return exec.Command("osascript", "-e", script).Run()
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
