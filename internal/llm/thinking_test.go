package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureBody serves a valid extraction and records the request payload.
func captureBody(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return captureBodyWithUsage(t, got, 0)
}

// captureBodyWithUsage also reports reasoningTokens in the usage block, which is
// the only evidence that a chat-template flag was ignored.
func captureBodyWithUsage(t *testing.T, got *map[string]any, reasoningTokens int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		resp := map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"languages":["go"]}`}}},
			"usage": map[string]any{
				"completion_tokens":         reasoningTokens + 10,
				"completion_tokens_details": map[string]any{"reasoning_tokens": reasoningTokens},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func boolPtr(b bool) *bool { return &b }

// templateKwargs pulls chat_template_kwargs out of a captured body.
func templateKwargs(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	kw, ok := body["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("chat_template_kwargs missing or wrong type: %#v", body["chat_template_kwargs"])
	}
	return kw
}

// TestThinkingDefaultLeavesBodyUnchanged pins the default: chat_template_kwargs is
// a vLLM/template-specific field, so a gateway or a model without that template
// could reject it. It must only appear when Thinking is set explicitly.
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

// TestDisableThinkingUsesEnableThinkingKwarg is the regression pin for the
// 2026-09-02 DGX model swap. The retired model honoured {"thinking": false};
// qwen38-flash-next honours only {"enable_thinking": false} and ignores the
// other name while still returning 200, so the wrong default costs ~7x
// throughput with nothing in the logs. Verified 2026-09-03 against the live
// endpoint: reasoning_tokens 1108 with "thinking", 0 with "enable_thinking".
func TestDisableThinkingUsesEnableThinkingKwarg(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(false)}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	kw := templateKwargs(t, body)
	if _, wrong := kw["thinking"]; wrong {
		t.Error(`sent the retired "thinking" key, which current models accept and ignore`)
	}
	// Must be the boolean false, not the string "false": the template branches on
	// truthiness and a non-empty string would read as true.
	if v, isBool := kw["enable_thinking"].(bool); !isBool || v {
		t.Errorf("enable_thinking = %#v, want boolean false", kw["enable_thinking"])
	}
}

// TestThinkingKwargIsConfigurable is the point of the exercise: a model whose
// template spells the flag differently needs an env change, not a release.
func TestThinkingKwargIsConfigurable(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(false), ThinkingKwarg: "thinking"}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	kw := templateKwargs(t, body)
	if v, isBool := kw["thinking"].(bool); !isBool || v {
		t.Errorf("thinking = %#v, want boolean false", kw["thinking"])
	}
	if _, extra := kw["enable_thinking"]; extra {
		t.Error("the default kwarg must not be sent alongside an override")
	}
}

// TestThinkingTrueIsSentExplicitly covers the mirror case: a template that
// defaults reasoning off needs the flag set to true to turn it on.
func TestThinkingTrueIsSentExplicitly(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(true)}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if v, isBool := templateKwargs(t, body)["enable_thinking"].(bool); !isBool || !v {
		t.Errorf("enable_thinking = %#v, want boolean true", templateKwargs(t, body)["enable_thinking"])
	}
}

// TestIgnoredThinkingFlagIsReported guards the failure mode that has no other
// symptom: the request succeeds, the JSON parses, and only the token accounting
// shows the model kept reasoning.
func TestIgnoredThinkingFlagIsReported(t *testing.T) {
	srv := captureBodyWithUsage(t, nil, 1108)
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(false)}
	for i := 0; i < 3; i++ {
		if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	}
	got := logs.String()
	if !strings.Contains(got, "reasoning was not disabled") {
		t.Errorf("no warning when the model ignored the flag; logs:\n%s", got)
	}
	if n := strings.Count(got, "reasoning was not disabled"); n != 1 {
		t.Errorf("warned %d times over 3 calls, want once per process", n)
	}
	if !strings.Contains(got, "LLM_THINKING_KWARG") {
		t.Error("the warning must name the knob that fixes it")
	}
}

// TestThinkingHonouredIsSilent keeps the guard from crying wolf.
func TestThinkingHonouredIsSilent(t *testing.T) {
	srv := captureBodyWithUsage(t, nil, 0)
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	// Reasoning left on must also stay silent: the tokens are expected then.
	for _, thinking := range []*bool{nil, boolPtr(false), boolPtr(true)} {
		e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: thinking}
		if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	}
	if strings.Contains(logs.String(), "reasoning was not disabled") {
		t.Errorf("unexpected warning; logs:\n%s", logs.String())
	}
}

func TestThinkingLeftOnWithReasoningIsSilent(t *testing.T) {
	srv := captureBodyWithUsage(t, nil, 800)
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(true)}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(logs.String(), "reasoning was not disabled") {
		t.Error("reasoning tokens are expected when thinking is on")
	}
}
