package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WebhookNotifier delivers alerts via HTTP POST.
type WebhookNotifier struct {
	URL     string
	Timeout time.Duration
	Headers map[string]string
}

func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		URL:     url,
		Timeout: 5 * time.Second,
		Headers: make(map[string]string),
	}
}

func (n *WebhookNotifier) Name() string {
	return "webhook"
}

func (n *WebhookNotifier) Available() bool {
	return n.URL != ""
}

func (n *WebhookNotifier) Notify(alert *Alert) error {
	if !n.Available() {
		return nil
	}

	payload := map[string]interface{}{
		"level":     alert.Level.String(),
		"title":     alert.Title,
		"message":   alert.Message,
		"profile":   alert.Profile,
		"timestamp": alert.Timestamp.Format(time.RFC3339),
		"action":    alert.Action,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", n.URL, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{
		Timeout: n.Timeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook failed with status: %s", resp.Status)
	}

	return nil
}
