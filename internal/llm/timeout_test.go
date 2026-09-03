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
// postings ran longer, so almost every call timed out just short of completing.
//
// The floor is re-anchored per model swap. On qwen38-flash-next, 16 real
// postings at concurrency 8 on 2026-09-03 measured a 18.7s mean and a 58.7s
// worst case, so a 60s budget would still be marginal; 2x the sampled worst
// case is the minimum that leaves the fail-open path for genuine faults.
//
// This is a floor, not a claim that the budget is generous. Production sees
// ~1.3% of calls exceed even 300s — see DefaultTimeout for why raising it is
// the wrong lever.
func TestExtractorTimeoutIsConfigurable(t *testing.T) {
	const measuredWorstCase = 59 * time.Second
	if DefaultTimeout < 2*measuredWorstCase {
		t.Fatalf("DefaultTimeout = %v, want at least %v (2x the measured %v worst case), "+
			"or enrich fails open on the slow tail and leaves those jobs in the backlog",
			DefaultTimeout, 2*measuredWorstCase, measuredWorstCase)
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
