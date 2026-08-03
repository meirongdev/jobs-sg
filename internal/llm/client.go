// Package llm provides the technology-stack extraction clients: a pure-rule
// fallback, an OpenAI-compatible extractor for Bifrost, and a degradation
// chain (custom_dgx -> custom_m2 -> rule-only). Failures are fail-open by
// design (docs/02 §4.2).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// OpenAIExtractor talks to an OpenAI-compatible chat completions endpoint
// (Bifrost at bifrost.bifrost.svc:8080 with x-bf-vk virtual key).
type OpenAIExtractor struct {
	BaseURL    string
	VirtualKey string
	Model      string
	Client     *http.Client
	// Timeout for one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// DefaultTimeout is the per-call budget when none is set.
//
// Deliberately far above observed latency. The previous hardcoded 60s sat just
// *below* it: one real posting (2.3k chars of description, 497 prompt tokens)
// against DGX deepseek-v4-flash measured 66.5s end to end, ~937 completion
// tokens of which most were reasoning. Nearly every call was therefore
// marginally over budget, so enrich burned 2×60s per job, failed open, left the
// job in the backlog, and drained at 2.1 jobs/min instead of ~7.
//
// The endpoint is non-streaming: no headers arrive until generation finishes, so
// this budget covers the whole generation ("awaiting headers" in the timeout
// error). Giving up early also does not stop the server generating, which wastes
// capacity on a shared GPU box.
const DefaultTimeout = 300 * time.Second

// Extract calls the chat completions API once.
func (e *OpenAIExtractor) Extract(ctx context.Context, title, desc string) (Result, error) {
	desc = truncate(desc, 4000)
	payload := map[string]any{
		"model": e.Model,
		"messages": []map[string]string{
			{"role": "system", "content": ExtractPrompt},
			{"role": "user", "content": "TITLE: " + title + "\n\nDESCRIPTION:\n" + desc},
		},
		"temperature": 0,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	url := strings.TrimRight(e.BaseURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-bf-vk", e.VirtualKey)
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
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		return Result{}, err
	}
	if len(chat.Choices) == 0 {
		return Result{}, fmt.Errorf("llm: no choices in response")
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
