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
