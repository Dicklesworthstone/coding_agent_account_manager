package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTerminalNotifier(t *testing.T) {
	var buf bytes.Buffer
	n := NewTerminalNotifier(&buf, false)

	alert := &Alert{
		Level:   Warning,
		Title:   "Test",
		Message: "This is a test",
		Profile: "user@example.com",
		Action:  "Do something",
	}

	err := n.Notify(alert)
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "[WARN] Test: This is a test (user@example.com)") {
		t.Errorf("Output missing expected format: %q", output)
	}
	if !strings.Contains(output, "Action: Do something") {
		t.Errorf("Output missing action: %q", output)
	}
}

func TestWebhookNotifier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json")
		}
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("Expected Authorization header")
		}

		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)

		if payload["level"] != "CRITICAL" {
			t.Errorf("Expected level CRITICAL, got %v", payload["level"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := NewWebhookNotifier(server.URL)
	n.Headers["Authorization"] = "Bearer test"

	alert := &Alert{
		Level:     Critical,
		Title:     "Alert",
		Message:   "Boom",
		Timestamp: time.Now(),
	}

	err := n.Notify(alert)
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
}

type MockNotifier struct {
	Err error
}

func (m *MockNotifier) Notify(alert *Alert) error { return m.Err }
func (m *MockNotifier) Name() string              { return "mock" }
func (m *MockNotifier) Available() bool           { return true }

func TestMultiNotifier(t *testing.T) {
	n1 := &MockNotifier{}
	n2 := &MockNotifier{Err: errors.New("fail")}

	multi := NewMultiNotifier(n1, n2)
	err := multi.Notify(&Alert{})

	if err == nil {
		t.Fatal("Expected error from n2")
	}
}

type recordingNotifier struct {
	alerts []*Alert
	fail   error
}

func (r *recordingNotifier) Name() string    { return "recording" }
func (r *recordingNotifier) Available() bool { return true }
func (r *recordingNotifier) Notify(a *Alert) error {
	r.alerts = append(r.alerts, a)
	return r.fail
}

func TestThrottledSuppressesRepeats(t *testing.T) {
	rec := &recordingNotifier{}
	th := NewThrottled(rec, time.Hour)
	now := time.Unix(1_000_000, 0)
	th.now = func() time.Time { return now }

	refreshFailed := &Alert{Level: Warning, Title: "Token refresh failed", Profile: "codex/work"}
	th.Notify(refreshFailed)
	th.Notify(refreshFailed)
	th.Notify(&Alert{Level: Warning, Title: "Token refresh failed", Profile: "codex/home"})
	th.Notify(&Alert{Level: Critical, Title: "Token refresh failed", Profile: "codex/work"})
	if len(rec.alerts) != 3 {
		t.Fatalf("delivered %d alerts, want 3 (one repeat suppressed)", len(rec.alerts))
	}

	now = now.Add(61 * time.Minute)
	th.Notify(refreshFailed)
	if len(rec.alerts) != 4 {
		t.Fatalf("alert after the interval was suppressed")
	}
}

func TestThrottledRetriesAfterFailedDelivery(t *testing.T) {
	rec := &recordingNotifier{fail: errors.New("offline")}
	th := NewThrottled(rec, time.Hour)
	alert := &Alert{Level: Warning, Title: "x"}
	if err := th.Notify(alert); err == nil {
		t.Fatal("expected delivery error")
	}
	rec.fail = nil
	if err := th.Notify(alert); err != nil {
		t.Fatal(err)
	}
	if len(rec.alerts) != 2 {
		t.Fatalf("a failed delivery must not start the quiet period; delivered %d", len(rec.alerts))
	}
}

func TestNewSelectsChannels(t *testing.T) {
	multi := New(Options{Terminal: true, TerminalWriter: &bytes.Buffer{}, Desktop: true, Webhook: " https://hooks.example/x "}).(*MultiNotifier)
	var names []string
	for _, n := range multi.notifiers {
		names = append(names, n.Name())
	}
	if strings.Join(names, ",") != "terminal,desktop,webhook" {
		t.Fatalf("channels = %v", names)
	}

	empty := New(Options{})
	if empty.Available() {
		t.Fatal("a notifier without channels must not report available")
	}
	if err := empty.Notify(&Alert{Title: "dropped"}); err != nil {
		t.Fatalf("empty notifier: %v", err)
	}
}

func TestDesktopCommandPassesTextAsArguments(t *testing.T) {
	alert := &Alert{Level: Critical, Title: `Login "required"`, Message: `rm -rf / " & do shell script "x`, Profile: "claude/o'neil"}

	name, args := desktopCommand("darwin", alert)
	if name != "osascript" || len(args) != 4 {
		t.Fatalf("darwin command = %s %q", name, args)
	}
	if strings.Contains(args[1], "rm -rf") || strings.Contains(args[1], "required") {
		t.Fatalf("alert text must not be spliced into the AppleScript source: %q", args[1])
	}
	if args[2] != alert.Title || args[3] != `[claude/o'neil] `+alert.Message {
		t.Fatalf("argv = %q", args[2:])
	}

	name, args = desktopCommand("linux", alert)
	if name != "notify-send" || args[1] != "critical" || args[len(args)-2] != alert.Title {
		t.Fatalf("linux command = %s %q", name, args)
	}
	if args[len(args)-3] != "--" {
		t.Fatalf("text must follow --: %q", args)
	}

	if name, _ := desktopCommand("plan9", alert); name != "" {
		t.Fatal("unsupported platforms have no command")
	}
}

func TestWebhookPayloadIsChatCompatible(t *testing.T) {
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&payload)
	}))
	defer srv.Close()

	if err := NewWebhookNotifier(srv.URL).Notify(&Alert{Level: Warning, Title: "Token refresh failed", Message: "timeout", Profile: "codex/work"}); err != nil {
		t.Fatal(err)
	}
	want := "[caam] WARNING Token refresh failed: timeout (codex/work)"
	if payload["text"] != want || payload["content"] != want {
		t.Fatalf("text=%q content=%q, want %q", payload["text"], payload["content"], want)
	}
	if payload["timestamp"] == "" || strings.HasPrefix(payload["timestamp"], "0001") {
		t.Fatalf("timestamp = %q", payload["timestamp"])
	}
}
