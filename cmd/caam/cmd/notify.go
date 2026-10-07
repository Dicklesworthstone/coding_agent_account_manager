package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/spf13/cobra"
)

// notifierChannels selects the delivery channels for a command.
type notifierChannels struct {
	// Terminal writes alerts to this writer when non-nil.
	Terminal io.Writer
	// External enables the configured desktop and webhook channels.
	External bool
}

// configuredNotifier builds the notifier described by the alerts section of
// the SPM config. Disabled alerts still reach the terminal channel, which is
// the command's own output; desktop and webhook channels follow the config.
func configuredNotifier(cfg *config.SPMConfig, ch notifierChannels) notify.Notifier {
	opts := notify.Options{}
	if ch.Terminal != nil && (cfg == nil || cfg.Alerts.Notifications.Terminal) {
		opts.Terminal = true
		opts.TerminalWriter = ch.Terminal
		opts.TerminalColor = true
	}
	if ch.External && cfg != nil && cfg.Alerts.Enabled {
		opts.Desktop = cfg.Alerts.Notifications.Desktop
		opts.Webhook = cfg.Alerts.Notifications.Webhook
	}
	return notify.New(opts)
}

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Inspect and test alert delivery",
	Long: `caam sends alerts when it switches accounts during 'caam run' and when the
daemon cannot refresh a credential or a login needs to be renewed.

Channels come from the alerts section of the config:
  alerts.enabled                  master switch for desktop and webhook alerts
  alerts.notifications.terminal   print alerts in the running command
  alerts.notifications.desktop    notify-send (Linux) / Notification Center (macOS)
  alerts.notifications.webhook    POST JSON (Slack/Discord compatible) to this URL

Configure with 'caam config set alerts.notifications.webhook https://...'.`,
}

var (
	notifyTestChannels []string
	notifyTestWebhook  string
	notifyTestJSON     bool
)

var notifyTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Send a test alert through each configured channel",
	Args:  cobra.NoArgs,
	// A failed delivery is a result, not a usage mistake.
	SilenceUsage: true,
	RunE:         runNotifyTest,
}

func init() {
	rootCmd.AddCommand(notifyCmd)
	notifyCmd.AddCommand(notifyTestCmd)
	notifyTestCmd.Flags().StringSliceVar(&notifyTestChannels, "channel", nil,
		"channels to test (terminal, desktop, webhook); default: the configured ones")
	notifyTestCmd.Flags().StringVar(&notifyTestWebhook, "webhook", "", "webhook URL to test instead of the configured one")
	notifyTestCmd.Flags().BoolVar(&notifyTestJSON, "json", false, "print results as JSON")
}

// notifyTestResult is one channel's outcome.
type notifyTestResult struct {
	Channel   string `json:"channel"`
	Available bool   `json:"available"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

func runNotifyTest(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadSPMConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	webhook := cfg.Alerts.Notifications.Webhook
	if notifyTestWebhook != "" {
		webhook = notifyTestWebhook
	}

	wanted := map[string]bool{}
	for _, c := range notifyTestChannels {
		c = strings.ToLower(strings.TrimSpace(c))
		switch c {
		case "terminal", "desktop", "webhook":
			wanted[c] = true
		case "":
		default:
			return fmt.Errorf("unknown channel %q (use terminal, desktop, or webhook)", c)
		}
	}
	if len(wanted) == 0 {
		wanted["terminal"] = cfg.Alerts.Notifications.Terminal
		wanted["desktop"] = cfg.Alerts.Enabled && cfg.Alerts.Notifications.Desktop
		wanted["webhook"] = (cfg.Alerts.Enabled && webhook != "") || notifyTestWebhook != ""
	}

	alert := &notify.Alert{
		Level:     notify.Info,
		Title:     "caam test alert",
		Message:   "Alerts from caam reach this channel.",
		Timestamp: time.Now(),
	}

	var results []notifyTestResult
	for _, name := range []string{"terminal", "desktop", "webhook"} {
		if !wanted[name] {
			continue
		}
		var n notify.Notifier
		switch name {
		case "terminal":
			n = notify.NewTerminalNotifier(cmd.ErrOrStderr(), true)
		case "desktop":
			n = notify.NewDesktopNotifier()
		case "webhook":
			if webhook == "" {
				results = append(results, notifyTestResult{Channel: name, Error: "no webhook URL configured"})
				continue
			}
			n = notify.NewWebhookNotifier(webhook)
		}
		r := notifyTestResult{Channel: name, Available: n.Available()}
		if err := n.Notify(alert); err != nil {
			r.Error = err.Error()
		} else {
			r.Delivered = r.Available
		}
		results = append(results, r)
	}

	out := cmd.OutOrStdout()
	if notifyTestJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		if len(results) == 0 {
			fmt.Fprintln(out, "No alert channels are enabled; see 'caam notify --help'.")
		}
		for _, r := range results {
			switch {
			case r.Delivered:
				fmt.Fprintf(out, "  ✓ %s: delivered\n", r.Channel)
			case r.Error != "":
				fmt.Fprintf(out, "  ✗ %s: %s\n", r.Channel, r.Error)
			default:
				fmt.Fprintf(out, "  ✗ %s: unavailable on this system\n", r.Channel)
			}
		}
	}

	for _, r := range results {
		if !r.Delivered {
			return fmt.Errorf("%s alert was not delivered", r.Channel)
		}
	}
	return nil
}
