package report

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSendSummaryThreadIDIsNumber pins message_thread_id as a JSON number.
//
// The Bot API types it as Integer. Sent as a quoted string it may be rejected or,
// worse, ignored — and being ignored posts the weekly report into the group's
// General topic instead of the content topic. The alert topic and the content
// topic live in the same chat and differ only by this field, so getting it wrong
// is exactly the mix-up docs/02 §4.3 forbids.
func TestSendSummaryThreadIDIsNumber(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = readAll(r)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := &Telegram{Token: "t", ChatID: "-1003981213530", ThreadID: "7", Client: srv.Client()}
	tg.apiBase = srv.URL
	if err := tg.SendSummary(context.Background(), "hello"); err != nil {
		t.Fatalf("SendSummary: %v", err)
	}

	// Unquoted 7, not "7".
	if !strings.Contains(string(raw), `"message_thread_id":7`) {
		t.Errorf("payload must carry message_thread_id as a number, got: %s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, isString := decoded["message_thread_id"].(string); isString {
		t.Error("message_thread_id decoded as a string; Telegram documents Integer")
	}
}

func TestSendSummaryRejectsNonNumericThreadID(t *testing.T) {
	tg := &Telegram{Token: "t", ChatID: "-100", ThreadID: "general"}
	err := tg.SendSummary(context.Background(), "hello")
	if err == nil {
		t.Fatal("a non-numeric thread id must be reported, not silently dropped into General")
	}
	if !strings.Contains(err.Error(), "not an integer") {
		t.Errorf("error should name the cause, got: %v", err)
	}
}

// TestSendSummaryOmitsThreadIDWhenEmpty covers the documented default: an empty
// ThreadID means the group's General topic, so the field must be absent.
func TestSendSummaryOmitsThreadIDWhenEmpty(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = readAll(r)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := &Telegram{Token: "t", ChatID: "-100", Client: srv.Client()}
	tg.apiBase = srv.URL
	if err := tg.SendSummary(context.Background(), "hello"); err != nil {
		t.Fatalf("SendSummary: %v", err)
	}
	if strings.Contains(string(raw), "message_thread_id") {
		t.Errorf("empty ThreadID must omit the field, got: %s", raw)
	}
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
