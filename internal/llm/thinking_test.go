package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureBody serves a valid extraction and records the request payload.
func captureBody(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"languages\":[\"go\"]}"}}]}`))
	}))
}

// TestThinkingDefaultLeavesBodyUnchanged pins the default: chat_template_kwargs is
// a vLLM/template-specific field, so a gateway or a model without that template
// could reject it. It must only appear when thinking is explicitly disabled.
func TestThinkingDefaultLeavesBodyUnchanged(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m"}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, present := body["chat_template_kwargs"]; present {
		t.Error("chat_template_kwargs must be absent by default — some backends reject it")
	}
}

func TestDisableThinkingSetsTemplateKwarg(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", DisableThinking: true}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	kw, ok := body["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("chat_template_kwargs missing or wrong type: %#v", body["chat_template_kwargs"])
	}
	// Must be the boolean false, not the string "false": the template branches on
	// truthiness and a non-empty string would read as true.
	if v, isBool := kw["thinking"].(bool); !isBool || v {
		t.Errorf("thinking = %#v, want boolean false", kw["thinking"])
	}
}
