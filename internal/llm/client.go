// Package llm provides the technology-stack extraction clients: a pure-rule
// fallback, an OpenAI-compatible extractor, and a degradation chain over
// several models. Failures are fail-open by design (docs/02 §4.2).
//
// Everything that differs between one model and the next is configuration, not
// code: see Config and config.go.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Result is the structured tech-stack output the LLM is asked to produce.
type Result struct {
	Languages  []string `json:"languages"`
	Frameworks []string `json:"frameworks"`
	Cloud      []string `json:"cloud"`
	Databases  []string `json:"databases"`
	Tools      []string `json:"tools"`
	AI         []string `json:"ai"`
}

// All flattens every category.
func (r Result) All() []string {
	var out []string
	out = append(out, r.Languages...)
	out = append(out, r.Frameworks...)
	out = append(out, r.Cloud...)
	out = append(out, r.Databases...)
	out = append(out, r.Tools...)
	out = append(out, r.AI...)
	return out
}

// Extractor extracts a structured tech stack from title + plain description.
type Extractor interface {
	Extract(ctx context.Context, title, desc string) (Result, error)
}

// ExtractPrompt instructs the model to return strict JSON only; the caller
// parses with json.Unmarshal, so anything else fails safely.
const ExtractPrompt = `You extract software technology keywords from a job posting.
Return ONLY a JSON object with exactly these array keys:
{"languages":[],"frameworks":[],"cloud":[],"databases":[],"tools":[],"ai":[]}
Use lowercase canonical names (e.g. "go", "kubernetes", "aws", "postgresql").
No prose, no markdown.`

// OpenAIExtractor talks to an OpenAI-compatible chat completions endpoint: the
// DGX vLLM server directly, or a gateway in front of it.
//
// Build one with Config.Extractors rather than by hand, so a model swap stays a
// configuration change.
type OpenAIExtractor struct {
	BaseURL string
	// APIKey is sent on AuthHeader ("Authorization" gets a "Bearer " prefix,
	// any other header name gets the value verbatim). Empty sends no auth
	// header, which is what a raw in-tailnet vLLM server wants.
	APIKey     string
	AuthHeader string
	Model      string
	Client     *http.Client
	// Timeout for one call. Zero means DefaultTimeout.
	Timeout time.Duration
	// MaxDescChars bounds the description. Zero means DefaultMaxDescChars.
	MaxDescChars int
	// MaxTokens caps the model's output. Zero sends no cap; read
	// DefaultMaxTokens before setting one, because a cap set below what a
	// reasoning model needs turns a slow success into a hard failure.
	MaxTokens int
	// Prompt replaces ExtractPrompt. Empty uses the built-in one.
	Prompt string
	// Thinking, when non-nil, sends chat_template_kwargs {ThinkingKwarg: value}.
	// Nil leaves the request body alone.
	//
	// Turning reasoning off is the backlog-burn lever: measured 2026-09-03 on
	// qwen38-flash-next over 16 real postings at concurrency 8, per-call mean
	// 18.7s with reasoning against 2.8s without, so a batch that took 64s took
	// 6.2s — reasoning is ~90% of the output tokens.
	//
	// The trade is small but real: raw output gets looser, but
	// Enricher.writeResult maps every term through the tech_taxonomy allowlist,
	// so junk lands in unmapped_tech rather than job_tech.
	//
	// Default nil: meant for burning down a large backlog, not for the
	// steady-state daily volume where the extra precision is affordable.
	Thinking *bool
	// ThinkingKwarg names the chat-template flag. Empty means
	// DefaultThinkingKwarg.
	ThinkingKwarg string
	// ExtraBody is merged into the request body beneath the derived fields; the
	// escape hatch for a parameter no field here covers.
	ExtraBody map[string]any

	// thinkingIgnored fires the "flag had no effect" warning at most once per
	// process, so a whole nightly run costs one log line rather than thousands.
	thinkingIgnored sync.Once
}

// DefaultTimeout is the per-call budget when none is set.
//
// Deliberately far above observed latency. An earlier hardcoded 60s sat just
// *below* it, so nearly every call was marginally over budget: enrich burned
// 2x60s per job, failed open, and left the job in the backlog.
//
// Measured 2026-09-03 against qwen38-flash-next over 16 real postings: per-call
// mean 18.7s, worst case 58.7s at concurrency 8. A 60s budget would therefore
// still be marginal today.
//
// But the tail is much heavier than a 16-posting sample can show, and 300s is
// NOT comfortable headroom: production logs put ~1.3% of calls over this budget
// on both this model and the retired one (3 of 242 and 3 of 204 on consecutive
// nights, different postings each time). Nothing suggests a slow endpoint — a
// reasoning model can simply run away on one posting.
//
// An output cap is the tempting cure and mostly is not one: legitimate
// extractions reach 4359 completion tokens (measured over 24 real postings)
// while a call only exhausts this budget somewhere past ~7000, so a cap has to
// land inside a narrow band to help, and one set too low converts a slow
// success into a failure. See DefaultMaxTokens.
//
// The failures are fail-open, so those postings stay in the backlog and are
// retried by the next night's run regardless. That makes the immediate retry
// the cheapest thing to give up: LLM_RETRIES=0 halves what a runaway costs
// without risking anything. LLM_THINKING=false removes the runaways outright,
// at some precision.
//
// The endpoint is non-streaming: no headers arrive until generation finishes, so
// this budget covers the whole generation ("awaiting headers" in the timeout
// error). Giving up early also does not stop the server generating, which wastes
// capacity on a shared GPU box.
const DefaultTimeout = 300 * time.Second

// Extract calls the chat completions API once.
func (e *OpenAIExtractor) Extract(ctx context.Context, title, desc string) (Result, error) {
	body, err := json.Marshal(e.payload(title, desc))
	if err != nil {
		return Result{}, err
	}
	url := strings.TrimRight(e.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if name, value := e.authHeader(); name != "" {
		req.Header.Set(name, value)
	}
	client := e.Client
	if client == nil {
		t := e.Timeout
		if t <= 0 {
			t = DefaultTimeout
		}
		client = &http.Client{Timeout: t}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("llm http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var chat struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens        int `json:"completion_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		return Result{}, err
	}
	e.checkThinkingHonoured(chat.Usage.CompletionTokensDetails.ReasoningTokens)
	if len(chat.Choices) == 0 {
		return Result{}, fmt.Errorf("llm: no choices in response")
	}
	// A capped response carries finish_reason "length" and, with reasoning on,
	// a null content: everything generated went into the reasoning channel. That
	// would otherwise surface as an unexplained "bad JSON".
	if chat.Choices[0].FinishReason == "length" {
		return Result{}, fmt.Errorf("llm: response truncated at the output cap "+
			"(%d tokens, finish_reason=length) — raise or clear LLM_MAX_TOKENS", e.MaxTokens)
	}
	var res Result
	content := strings.TrimSpace(chat.Choices[0].Message.Content)
	// tolerate ```json fences
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	if err := json.Unmarshal([]byte(content), &res); err != nil {
		return Result{}, fmt.Errorf("llm: bad JSON: %w", err)
	}
	return res, nil
}

// payload assembles the request body: ExtraBody first so an operator can set
// anything this struct has no field for, then the derived fields on top so
// model and messages cannot be overridden into nonsense.
func (e *OpenAIExtractor) payload(title, desc string) map[string]any {
	max := e.MaxDescChars
	if max <= 0 {
		max = DefaultMaxDescChars
	}
	p := make(map[string]any, len(e.ExtraBody)+4)
	for k, v := range e.ExtraBody {
		p[k] = v
	}
	if _, set := p["temperature"]; !set {
		p["temperature"] = 0
	}
	if e.Thinking != nil {
		// Merge rather than replace: LLM_EXTRA_BODY may carry other template
		// kwargs, and the explicit thinking setting is the more specific one.
		kwargs := map[string]any{}
		if prev, ok := p["chat_template_kwargs"].(map[string]any); ok {
			for k, v := range prev {
				kwargs[k] = v
			}
		}
		kwargs[e.thinkingKwarg()] = *e.Thinking
		p["chat_template_kwargs"] = kwargs
	}
	if e.MaxTokens > 0 {
		p["max_tokens"] = e.MaxTokens
	}
	prompt := e.Prompt
	if prompt == "" {
		prompt = ExtractPrompt
	}
	p["model"] = e.Model
	p["messages"] = []map[string]string{
		{"role": "system", "content": prompt},
		{"role": "user", "content": "TITLE: " + title + "\n\nDESCRIPTION:\n" + truncate(desc, max)},
	}
	return p
}

func (e *OpenAIExtractor) thinkingKwarg() string {
	if e.ThinkingKwarg != "" {
		return e.ThinkingKwarg
	}
	return DefaultThinkingKwarg
}

// authHeader returns the header to send, or an empty name for no auth.
func (e *OpenAIExtractor) authHeader() (name, value string) {
	if e.APIKey == "" {
		return "", ""
	}
	name = e.AuthHeader
	if name == "" {
		name = DefaultAuthHeader
	}
	value = e.APIKey
	// Only Authorization carries a scheme, and only if the operator did not
	// already supply one ("Bearer x", "Basic y").
	if strings.EqualFold(name, "Authorization") && !strings.Contains(value, " ") {
		value = "Bearer " + value
	}
	return name, value
}

// checkThinkingHonoured turns the swap failure this package was bitten by into a
// log line. A chat template that does not recognise the flag ignores it and
// still returns 200, so the only evidence is that the model kept reasoning.
func (e *OpenAIExtractor) checkThinkingHonoured(reasoningTokens int) {
	if e.Thinking == nil || *e.Thinking || reasoningTokens <= 0 {
		return
	}
	e.thinkingIgnored.Do(func() {
		slog.Warn("llm: reasoning was not disabled — the model ignored the chat-template flag; "+
			"this model's template probably spells it differently (set LLM_THINKING_KWARG)",
			"model", e.Model, "kwarg", e.thinkingKwarg(), "reasoning_tokens", reasoningTokens)
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Chain tries extractors in order; the first success wins. If all fail, it
// returns the last error (caller treats as fail-open).
type Chain struct {
	Extractors []Extractor
}

func (c Chain) Extract(ctx context.Context, title, desc string) (Result, error) {
	var lastErr error
	for _, e := range c.Extractors {
		res, err := e.Extract(ctx, title, desc)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return Result{}, lastErr
}
