package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureRequest records headers alongside the decoded body.
func captureRequest(t *testing.T, body *map[string]any, hdr *http.Header) *httptest.Server {
	t.Helper()
	inner := captureBody(t, body)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hdr = r.Header.Clone()
		inner.Config.Handler.ServeHTTP(w, r)
	}))
}

// TestExtraBodyReachesTheRequest is the general escape hatch: a parameter the
// next model needs and this struct has no field for must still be settable.
func TestExtraBodyReachesTheRequest(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", ExtraBody: map[string]any{
		"top_p":            0.8,
		"max_tokens":       float64(256),
		"reasoning_effort": "low",
	}}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for k, want := range map[string]any{"top_p": 0.8, "max_tokens": float64(256), "reasoning_effort": "low"} {
		if got := body[k]; got != want {
			t.Errorf("body[%q] = %#v, want %#v", k, got, want)
		}
	}
}

// TestExtraBodyCannotOverrideDerivedFields keeps the escape hatch from breaking
// the extraction contract: model comes from the chain, messages from the posting.
func TestExtraBodyCannotOverrideDerivedFields(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "real-model", ExtraBody: map[string]any{
		"model":    "hijacked",
		"messages": []any{},
	}}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if body["model"] != "real-model" {
		t.Errorf("model = %#v, want the configured model", body["model"])
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Errorf("messages = %#v, want the system+user pair", body["messages"])
	}
}

// TestExtraBodyOverridesTemperature allows the one derived field that is a
// genuine tuning knob rather than part of the contract.
func TestExtraBodyOverridesTemperature(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", ExtraBody: map[string]any{"temperature": 0.2}}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if body["temperature"] != 0.2 {
		t.Errorf("temperature = %#v, want the override 0.2", body["temperature"])
	}
}

// TestExtraBodyTemplateKwargsMergeWithThinking: the two ways of setting template
// kwargs must compose, with the specific setting winning.
func TestExtraBodyTemplateKwargsMergeWithThinking(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(false),
		ExtraBody: map[string]any{"chat_template_kwargs": map[string]any{
			"enable_thinking": true, // must lose to the explicit setting
			"other_flag":      "keep",
		}}}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	kw := templateKwargs(t, body)
	if v, isBool := kw["enable_thinking"].(bool); !isBool || v {
		t.Errorf("enable_thinking = %#v, want the explicit false", kw["enable_thinking"])
	}
	if kw["other_flag"] != "keep" {
		t.Errorf("other_flag = %#v, want the extra-body value preserved", kw["other_flag"])
	}
}

// TestExtraBodyIsNotMutated matters because Config.Extractors shares one map
// across every model in the chain.
func TestExtraBodyIsNotMutated(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	kwargs := map[string]any{"other_flag": "keep"}
	extra := map[string]any{"chat_template_kwargs": kwargs}
	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Thinking: boolPtr(false), ExtraBody: extra}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, leaked := kwargs["enable_thinking"]; leaked {
		t.Error("Extract wrote the thinking flag back into the caller's ExtraBody map")
	}
	if len(extra) != 1 {
		t.Errorf("ExtraBody grew to %#v", extra)
	}
}

func TestAuthHeaderDefaultsToBearer(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := captureRequest(t, &body, &hdr)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", APIKey: "sk-secret"}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := hdr.Get("Authorization"); got != "Bearer sk-secret" {
		t.Errorf("Authorization = %q, want the Bearer form", got)
	}
}

// TestAuthHeaderCustomNameIsVerbatim covers gateways with their own key header;
// a "Bearer " prefix there would be sent as part of the key.
func TestAuthHeaderCustomNameIsVerbatim(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := captureRequest(t, &body, &hdr)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", APIKey: "vk-1", AuthHeader: "x-bf-vk"}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := hdr.Get("x-bf-vk"); got != "vk-1" {
		t.Errorf("x-bf-vk = %q, want the raw key", got)
	}
	if hdr.Get("Authorization") != "" {
		t.Error("must not also send Authorization")
	}
}

// TestAuthHeaderKeepsSuppliedScheme lets an operator pass a non-Bearer scheme.
func TestAuthHeaderKeepsSuppliedScheme(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := captureRequest(t, &body, &hdr)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", APIKey: "Basic abc123"}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := hdr.Get("Authorization"); got != "Basic abc123" {
		t.Errorf("Authorization = %q, want the supplied scheme untouched", got)
	}
}

// TestNoAuthHeaderWithoutKey: the live setup talks to a raw in-tailnet vLLM that
// wants no credential at all.
func TestNoAuthHeaderWithoutKey(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	srv := captureRequest(t, &body, &hdr)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m"}
	if _, err := e.Extract(context.Background(), "title", "desc"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := hdr.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want no auth header", got)
	}
}

func TestMaxDescCharsIsConfigurable(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	long := strings.Repeat("x", 6000)
	userContent := func() string {
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 2 {
			t.Fatalf("messages = %#v", body["messages"])
		}
		m, _ := msgs[1].(map[string]any)
		s, _ := m["content"].(string)
		return s
	}

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m"}
	if _, err := e.Extract(context.Background(), "t", long); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := strings.Count(userContent(), "x"); got != DefaultMaxDescChars {
		t.Errorf("default truncation kept %d chars, want %d", got, DefaultMaxDescChars)
	}

	e = &OpenAIExtractor{BaseURL: srv.URL, Model: "m", MaxDescChars: 100}
	if _, err := e.Extract(context.Background(), "t", long); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := strings.Count(userContent(), "x"); got != 100 {
		t.Errorf("configured truncation kept %d chars, want 100", got)
	}
}

func TestPromptIsConfigurable(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	systemContent := func() string {
		msgs, ok := body["messages"].([]any)
		if !ok || len(msgs) != 2 {
			t.Fatalf("messages = %#v", body["messages"])
		}
		m, _ := msgs[0].(map[string]any)
		s, _ := m["content"].(string)
		return s
	}

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m"}
	if _, err := e.Extract(context.Background(), "t", "d"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if systemContent() != ExtractPrompt {
		t.Error("default must send the built-in prompt")
	}

	e = &OpenAIExtractor{BaseURL: srv.URL, Model: "m", Prompt: "Return JSON only."}
	if _, err := e.Extract(context.Background(), "t", "d"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := systemContent(); got != "Return JSON only." {
		t.Errorf("system prompt = %q, want the override", got)
	}
}

// TestMaxTokensAbsentByDefault pins DefaultMaxTokens: an output cap set below
// what a reasoning model needs turns a slow success into a hard failure, so the
// request carries none unless asked.
func TestMaxTokensAbsentByDefault(t *testing.T) {
	var body map[string]any
	srv := captureBody(t, &body)
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m"}
	if _, err := e.Extract(context.Background(), "t", "d"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if _, present := body["max_tokens"]; present {
		t.Errorf("max_tokens = %#v, want absent by default", body["max_tokens"])
	}

	e = &OpenAIExtractor{BaseURL: srv.URL, Model: "m", MaxTokens: 2048}
	if _, err := e.Extract(context.Background(), "t", "d"); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if body["max_tokens"] != float64(2048) {
		t.Errorf("max_tokens = %#v, want 2048", body["max_tokens"])
	}
}

// TestTruncatedResponseIsNamed guards a confusing failure: when the cap is hit,
// the endpoint returns finish_reason "length" and — with reasoning on — a null
// content, which would otherwise be reported as unexplained bad JSON.
func TestTruncatedResponseIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":null},"finish_reason":"length"}],
			"usage":{"completion_tokens":64,"completion_tokens_details":{"reasoning_tokens":64}}}`))
	}))
	defer srv.Close()

	e := &OpenAIExtractor{BaseURL: srv.URL, Model: "m", MaxTokens: 64}
	_, err := e.Extract(context.Background(), "t", "d")
	if err == nil {
		t.Fatal("expected an error for a truncated response")
	}
	if !strings.Contains(err.Error(), "LLM_MAX_TOKENS") {
		t.Errorf("error = %v, want it to name the knob that fixes it", err)
	}
	if strings.Contains(err.Error(), "bad JSON") {
		t.Errorf("error = %v, want the truncation named rather than blamed on the payload", err)
	}
}
