package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestExtractorTimeoutIsConfigurable guards the regression that made LLM
// enrichment barely work: the per-call budget was hardcoded to 60s while real
// postings measured 66.5s against DGX deepseek-v4-flash, so almost every call
// timed out just short of completing.
func TestExtractorTimeoutIsConfigurable(t *testing.T) {
	if DefaultTimeout <= 66*time.Second {
		t.Fatalf("DefaultTimeout = %v, must exceed the ~66s measured per-call latency "+
			"with headroom, or enrich fails open on nearly every job", DefaultTimeout)
	}

	// A server slower than the configured timeout must surface a timeout error…
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer slow.Close()

	e := &OpenAIExtractor{BaseURL: slow.URL, Model: "m", Timeout: 50 * time.Millisecond}
	if _, err := e.Extract(context.Background(), "title", "desc"); err == nil {
		t.Error("expected a timeout error when Timeout is shorter than the response")
	}

	// …and a budget above the response time must succeed, proving the field is
	// honoured rather than ignored in favour of a constant.
	e = &OpenAIExtractor{BaseURL: slow.URL, Model: "m", Timeout: 5 * time.Second}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Errorf("expected success with a generous Timeout, got %v", err)
	}
}
