package notify

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// desktopTimeout bounds a notification command so a hung notification
// service never stalls the caller (for example an account handoff).
const desktopTimeout = 5 * time.Second

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

	name, args := desktopCommand(runtime.GOOS, alert)
	if name == "" {
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(context.Background(), desktopTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// desktopCommand builds the notification command for goos. Alert text is
// passed as arguments, never interpreted: AppleScript receives it through
// "on run argv" rather than spliced into the script source.
func desktopCommand(goos string, alert *Alert) (string, []string) {
	title := alert.Title
	message := alert.Message
	if alert.Profile != "" {
		message = fmt.Sprintf("[%s] %s", alert.Profile, message)
	}

	switch goos {
	case "linux":
		urgency := "normal"
		if alert.Level == Critical {
			urgency = "critical"
		}
		return "notify-send", []string{"-u", urgency, "-a", "caam", "--", title, message}
	case "darwin":
		script := "on run argv\ndisplay notification (item 2 of argv) with title (item 1 of argv)\nend run"
		return "osascript", []string{"-e", script, title, message}
	default:
		return "", nil
	}
}
