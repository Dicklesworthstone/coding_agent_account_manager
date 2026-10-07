package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/spf13/cobra"
)

func TestConfiguredNotifierFollowsAlertConfig(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()

	cfg := config.DefaultSPMConfig()
	cfg.Alerts.Notifications.Desktop = false
	cfg.Alerts.Notifications.Webhook = srv.URL

	var term bytes.Buffer
	n := configuredNotifier(cfg, notifierChannels{Terminal: &term, External: true})
	if err := n.Notify(&notify.Alert{Level: notify.Info, Title: "Profile switched", Message: "now on b"}); err != nil {
		t.Fatal(err)
	}
	if hits != 1 || !strings.Contains(term.String(), "Profile switched") {
		t.Fatalf("webhook hits=%d terminal=%q", hits, term.String())
	}

	// alerts.enabled=false silences external channels but keeps the
	// command's own terminal output.
	cfg.Alerts.Enabled = false
	term.Reset()
	n = configuredNotifier(cfg, notifierChannels{Terminal: &term, External: true})
	n.Notify(&notify.Alert{Title: "again"})
	if hits != 1 || !strings.Contains(term.String(), "again") {
		t.Fatalf("disabled alerts: webhook hits=%d terminal=%q", hits, term.String())
	}

	// --quiet: no terminal writer and no external channels means a no-op.
	n = configuredNotifier(cfg, notifierChannels{})
	if err := n.Notify(&notify.Alert{Title: "dropped"}); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyTestWebhook(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &got)
	}))
	defer srv.Close()

	notifyTestChannels, notifyTestWebhook, notifyTestJSON = []string{"webhook"}, srv.URL, true
	t.Cleanup(func() { notifyTestChannels, notifyTestWebhook, notifyTestJSON = nil, "", false })

	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	c.SetErr(io.Discard)
	c.SetContext(context.Background())
	if err := runNotifyTest(c, nil); err != nil {
		t.Fatalf("notify test: %v\n%s", err, out.String())
	}
	var results []notifyTestResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	if len(results) != 1 || results[0].Channel != "webhook" || !results[0].Delivered {
		t.Fatalf("results = %+v", results)
	}
	if got["title"] != "caam test alert" {
		t.Fatalf("webhook payload = %v", got)
	}
}

func TestNotifyTestReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	notifyTestChannels, notifyTestWebhook, notifyTestJSON = []string{"webhook"}, srv.URL, false
	t.Cleanup(func() { notifyTestChannels, notifyTestWebhook, notifyTestJSON = nil, "", false })

	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	c.SetErr(io.Discard)
	c.SetContext(context.Background())
	if err := runNotifyTest(c, nil); err == nil {
		t.Fatal("a rejected webhook must fail the command")
	}
	if !strings.Contains(out.String(), "✗ webhook") {
		t.Fatalf("output = %q", out.String())
	}

	notifyTestChannels = []string{"pager"}
	if err := runNotifyTest(c, nil); err == nil || !strings.Contains(err.Error(), "unknown channel") {
		t.Fatalf("unknown channel error = %v", err)
	}
}
