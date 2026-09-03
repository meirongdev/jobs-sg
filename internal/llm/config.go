package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Everything in this file exists so that swapping the model — or the endpoint in
// front of it — is a manifest edit rather than a code change.
//
// The 2026-09-02 DGX swap (deepseek-v4-flash -> qwen38-flash-next) is the worked
// example. Only two things actually differed between the two stacks: the model
// id, and the chat-template flag that turns reasoning off. The model id was
// already an env var; the flag name was not, so a value that used to work became
// a silent no-op — the request still returned 200, the model still reasoned, and
// throughput quietly fell by ~7x. DefaultThinkingKwarg and Config.ExtraBody make
// that class of difference configurable, and OpenAIExtractor reports it in the
// logs when the model ignores the flag anyway.
const (
	// DefaultThinkingKwarg is the chat_template_kwargs key that switches
	// reasoning on and off.
	//
	// "enable_thinking" is what Qwen3-family templates read, and it is the only
	// name that works on the current DGX model: measured 2026-09-03 against
	// qwen38-flash-next on the same endpoint enrich uses, one real posting —
	// no kwargs 792 reasoning tokens / 22.0s, {"thinking":false} 1108 / 28.1s,
	// {"enable_thinking":false} 0 / 3.6s. All three returned 200, which is why
	// the wrong name is so easy to miss. Override with LLM_THINKING_KWARG for a
	// template that spells it differently.
	DefaultThinkingKwarg = "enable_thinking"

	// DefaultAuthHeader carries the API key. "Authorization" is the
	// OpenAI-compatible default and gets a "Bearer " prefix; any other header
	// name sends the key verbatim (e.g. LLM_AUTH_HEADER=x-bf-vk).
	DefaultAuthHeader = "Authorization"

	// DefaultMaxDescChars bounds the description sent to the model. Well under
	// any current context window; exists so a smaller-window model can be
	// accommodated with LLM_MAX_DESC_CHARS instead of a code change.
	DefaultMaxDescChars = 4000

	// DefaultPromptVersion is the version recorded alongside cached extractions.
	// It is part of the enrich cache key, so it must change whenever the prompt
	// changes — ConfigFromEnv derives one automatically for a custom prompt.
	DefaultPromptVersion = "v1"

	// DefaultAttempts is how many times one posting is sent before the LLM layer
	// gives up on it and fails open. Two means the documented "retry once".
	//
	// Retrying pays for a transient fault, but a call that exhausted the timeout
	// will almost always exhaust it again: the same posting and the same model
	// produce the same runaway generation. Set LLM_RETRIES=0 to stop paying
	// twice for those; the posting stays in the backlog either way and is picked
	// up by the next night's run.
	DefaultAttempts = 2

	// DefaultConcurrency bounds the enrich fan-out. Deliberately modest: this
	// fans out onto a shared GPU box, so the ceiling is the backend's spare
	// capacity rather than the local CPU.
	DefaultConcurrency = 3

	// DefaultMaxTokens is zero, meaning the request carries no output cap.
	//
	// A cap looks like the obvious cure for a reasoning model that runs away on
	// one posting, and it is not: measured 2026-09-03 over 24 real postings with
	// reasoning on, legitimate extractions reach 4359 completion tokens
	// (p50 475, p90 2631), while a call only exhausts the 300s budget somewhere
	// north of ~7000 tokens under concurrency 8. A cap has to sit inside that
	// narrow band to help at all, and a cap set too low turns a slow success
	// into a failure — the model emits its reasoning first, so a truncated
	// response has no JSON in it whatsoever.
	//
	// So this stays off by default and exists for the case it genuinely fits: a
	// model with a smaller output window. LLM_RETRIES=0 is the cheaper lever
	// against runaway cost.
	DefaultMaxTokens = 0
)

// DefaultModelChain is the fallback when LLM_MODELS is unset. Deployments should
// set LLM_MODELS explicitly — a retired id left in code here is exactly what
// broke after the DGX swap — but a wrong-but-current default fails loudly (HTTP
// 404 from /v1/chat/completions) whereas an empty one fails open and silently.
var DefaultModelChain = []string{"qwen38-flash-next"}

// reservedBodyKeys are derived from the posting and the model chain, so
// LLM_EXTRA_BODY may not set them.
var reservedBodyKeys = []string{"model", "messages"}

// Config is the whole model-facing configuration of the LLM layer. Zero value
// means "LLM disabled": Enabled() reports false without a BaseURL.
type Config struct {
	// BaseURL of an OpenAI-compatible server; empty disables the LLM layer.
	BaseURL string
	// APIKey is sent on AuthHeader; empty sends no auth header at all, which is
	// what a raw in-tailnet vLLM server wants.
	APIKey     string
	AuthHeader string
	// Models is the degradation chain, first entry preferred. Ids are
	// backend-specific: a gateway routes on provider-prefixed names
	// (custom_dgx/qwen38-flash-next) while a raw vLLM server serves the bare id
	// and 404s the prefixed form.
	Models []string
	// Timeout per call; zero means DefaultTimeout.
	Timeout time.Duration
	// Concurrency of the enrich fan-out; zero means DefaultConcurrency.
	Concurrency int
	// MaxDescChars bounds the description; zero means DefaultMaxDescChars.
	MaxDescChars int
	// MaxTokens caps the model's output; zero sends no cap. See DefaultMaxTokens
	// before setting one.
	MaxTokens int
	// Prompt overrides the extraction instructions; empty means ExtractPrompt.
	// The output contract is parsed by json.Unmarshal into Result, so a
	// replacement still has to ask for that JSON object.
	Prompt string
	// PromptVersion is recorded with each cached extraction and forms part of the
	// cache key, so changing the prompt without changing this would serve results
	// produced by the old one. ConfigFromEnv keeps the two in step.
	PromptVersion string
	// Attempts per posting before failing open; zero means DefaultAttempts.
	Attempts int
	// Thinking nil leaves the request body alone, which is the safe default: a
	// backend whose template lacks the flag can reject an unknown kwarg. Non-nil
	// sends chat_template_kwargs {ThinkingKwarg: *Thinking} — false to turn
	// reasoning off, true to turn it on for a model whose template defaults off.
	Thinking      *bool
	ThinkingKwarg string
	// ExtraBody is merged into the request body beneath the fields above. The
	// escape hatch for anything a future model needs that has no field here:
	// top_p, reasoning_effort, a differently-shaped template kwarg.
	ExtraBody map[string]any
}

// ConfigFromEnv reads the LLM_* environment. getenv is injected so the parsing
// is testable; pass os.Getenv.
//
// It never fails: an unparseable value is dropped and described in the returned
// warnings, so one typo degrades a single knob instead of the nightly run. The
// caller must log the warnings — a dropped knob is invisible otherwise, and
// invisible config is the failure this file exists to prevent.
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	var warn []string
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }

	c := Config{
		BaseURL:       get("LLM_BASE_URL"),
		AuthHeader:    DefaultAuthHeader,
		Models:        append([]string(nil), DefaultModelChain...),
		ThinkingKwarg: DefaultThinkingKwarg,
	}

	if v := get("LLM_MODELS"); v != "" {
		var models []string
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				models = append(models, m)
			}
		}
		if len(models) > 0 {
			c.Models = models
		} else {
			warn = append(warn, "LLM_MODELS lists no model; keeping the default chain")
		}
	}

	c.APIKey = get("LLM_API_KEY")
	if v := get("BIFROST_VK"); v != "" {
		// Bifrost was retired 2026-09; kept as an alias so a stale manifest loses
		// throughput rather than authentication.
		if c.APIKey == "" {
			c.APIKey, c.AuthHeader = v, "x-bf-vk"
			warn = append(warn, "BIFROST_VK is deprecated; set LLM_API_KEY plus LLM_AUTH_HEADER=x-bf-vk")
		} else {
			warn = append(warn, "BIFROST_VK ignored because LLM_API_KEY is set")
		}
	}
	if v := get("LLM_AUTH_HEADER"); v != "" {
		c.AuthHeader = v
	}

	if v := get("LLM_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Timeout = time.Duration(n) * time.Second
		} else {
			warn = append(warn, fmt.Sprintf("ignoring invalid LLM_TIMEOUT %q (want a positive whole number of seconds)", v))
		}
	}
	if v := get("LLM_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Concurrency = n
		} else {
			warn = append(warn, fmt.Sprintf("ignoring invalid LLM_CONCURRENCY %q (want a positive whole number)", v))
		}
	}
	if v := get("LLM_MAX_DESC_CHARS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxDescChars = n
		} else {
			warn = append(warn, fmt.Sprintf("ignoring invalid LLM_MAX_DESC_CHARS %q (want a positive whole number)", v))
		}
	}
	// Zero is meaningful here and means "no cap", so unlike the knobs above it is
	// accepted rather than rejected.
	if v := get("LLM_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.MaxTokens = n
		} else {
			warn = append(warn, fmt.Sprintf("ignoring invalid LLM_MAX_TOKENS %q (want a whole number, 0 for no cap)", v))
		}
	}
	// Expressed as retries because that is how the design doc words it; carried
	// as attempts so that the zero value can still mean "use the default".
	if v := get("LLM_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Attempts = n + 1
		} else {
			warn = append(warn, fmt.Sprintf("ignoring invalid LLM_RETRIES %q (want a whole number, 0 for no retry)", v))
		}
	}

	if v := get("LLM_THINKING"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Thinking = &b
		} else {
			warn = append(warn, fmt.Sprintf("ignoring invalid LLM_THINKING %q (want true/false)", v))
		}
	}
	if v := get("LLM_THINKING_KWARG"); v != "" {
		c.ThinkingKwarg = v
	}

	// A changed prompt must not be served results the previous prompt produced,
	// so the version travels with it: an explicit LLM_PROMPT_VERSION wins,
	// otherwise a custom prompt gets a version derived from its own text. That
	// makes the cache miss for every posting, which is the correct and expensive
	// consequence of changing the instructions.
	c.Prompt = get("LLM_PROMPT")
	c.PromptVersion = get("LLM_PROMPT_VERSION")
	if c.PromptVersion == "" {
		if c.Prompt == "" || c.Prompt == ExtractPrompt {
			c.PromptVersion = DefaultPromptVersion
		} else {
			sum := sha256.Sum256([]byte(c.Prompt))
			c.PromptVersion = "custom-" + hex.EncodeToString(sum[:])[:8]
		}
	}

	if v := get("LLM_EXTRA_BODY"); v != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(v), &extra); err != nil {
			warn = append(warn, fmt.Sprintf("ignoring LLM_EXTRA_BODY, want a JSON object: %v", err))
		} else {
			for _, k := range reservedBodyKeys {
				if _, clash := extra[k]; clash {
					delete(extra, k)
					warn = append(warn, fmt.Sprintf("LLM_EXTRA_BODY key %q ignored: it is derived from the posting and LLM_MODELS", k))
				}
			}
			if len(extra) > 0 {
				c.ExtraBody = extra
			}
		}
	}

	return c, warn
}

// Enabled reports whether the LLM layer should run at all. Without a BaseURL
// enrich degrades to rule-only mode (docs/02 §4.2).
func (c Config) Enabled() bool { return c.BaseURL != "" }

// EffectiveTimeout resolves the per-call budget actually used.
func (c Config) EffectiveTimeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// Extractors builds the degradation chain, one extractor per model, in
// preference order.
func (c Config) Extractors() []Extractor {
	out := make([]Extractor, 0, len(c.Models))
	for _, m := range c.Models {
		out = append(out, &OpenAIExtractor{
			BaseURL:       c.BaseURL,
			APIKey:        c.APIKey,
			AuthHeader:    c.AuthHeader,
			Model:         m,
			Timeout:       c.Timeout,
			MaxDescChars:  c.MaxDescChars,
			MaxTokens:     c.MaxTokens,
			Prompt:        c.Prompt,
			Thinking:      c.Thinking,
			ThinkingKwarg: c.ThinkingKwarg,
			ExtraBody:     c.ExtraBody,
		})
	}
	return out
}

// EffectiveConcurrency resolves the fan-out actually used.
func (c Config) EffectiveConcurrency() int {
	if c.Concurrency > 0 {
		return c.Concurrency
	}
	return DefaultConcurrency
}

// EffectiveAttempts resolves how many times one posting is sent.
func (c Config) EffectiveAttempts() int {
	if c.Attempts > 0 {
		return c.Attempts
	}
	return DefaultAttempts
}

// EffectivePromptVersion resolves the cache-key version.
func (c Config) EffectivePromptVersion() string {
	if c.PromptVersion != "" {
		return c.PromptVersion
	}
	return DefaultPromptVersion
}

// PromptSummary describes the prompt for the startup log without printing it.
func (c Config) PromptSummary() string {
	if c.Prompt == "" || c.Prompt == ExtractPrompt {
		return "builtin"
	}
	return fmt.Sprintf("custom (%d chars)", len(c.Prompt))
}

// PrimaryModel is the model the chain prefers, and therefore the one recorded as
// provenance on job_tech rows and as part of the LLM cache key.
func (c Config) PrimaryModel() string {
	if len(c.Models) == 0 {
		return DefaultModelChain[0]
	}
	return c.Models[0]
}

// ThinkingSummary renders the reasoning setting for the startup log, naming the
// kwarg so a swap that needs a different one is visible without reading code.
func (c Config) ThinkingSummary() string {
	if c.Thinking == nil {
		return "model default (no chat_template_kwargs sent)"
	}
	kwarg := c.ThinkingKwarg
	if kwarg == "" {
		kwarg = DefaultThinkingKwarg
	}
	return fmt.Sprintf("%s=%t", kwarg, *c.Thinking)
}
