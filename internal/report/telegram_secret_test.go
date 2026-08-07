package report

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bot token lives in the Telegram API path, so any error that carries the
// request URL carries the secret. cmd/report logs push failures deliberately
// (fail-open: a dead DGX or a network blip must not fail the weekly run), and
// stdout goes to Loki — so a leak here is a secret at rest in the log store.
const probeToken = "8123456789:AAH-not-a-real-token-abcdefghijklmnop"

func TestSendSummaryErrorOmitsBotToken(t *testing.T) {
	// A server that is closed before use gives a genuine transport failure,
	// which is the path that produces a *url.Error carrying the full URL.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()

	tg := &Telegram{Token: probeToken, ChatID: "-100"}
	tg.apiBase = dead + "/bot" + probeToken

	err := tg.SendSummary(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected a transport error against a closed server")
	}
	if strings.Contains(err.Error(), probeToken) {
		t.Errorf("bot token leaked into the error text: %v", err)
	}
	// still has to be diagnosable
	if !strings.Contains(err.Error(), "telegram sendMessage") {
		t.Errorf("error should name the operation, got: %v", err)
	}
}

// A malformed base URL fails inside http.NewRequest rather than in the
// transport, and url.Parse errors quote the URL too.
func TestSendSummaryRequestBuildErrorOmitsBotToken(t *testing.T) {
	tg := &Telegram{Token: probeToken, ChatID: "-100"}
	tg.apiBase = "://bad-scheme/bot" + probeToken

	err := tg.SendSummary(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected a request-build error from a malformed base URL")
	}
	if strings.Contains(err.Error(), probeToken) {
		t.Errorf("bot token leaked into the error text: %v", err)
	}
}
