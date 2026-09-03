package llm

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// env builds a getenv function over a literal map, so the parsing is testable
// without touching the process environment.
func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestConfigDefaults(t *testing.T) {
	cfg, warn := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "http://dgx:8000"}))
	if len(warn) != 0 {
		t.Errorf("warnings on a clean config: %v", warn)
	}
	if !cfg.Enabled() {
		t.Error("Enabled() = false with a base URL set")
	}
	if got := cfg.PrimaryModel(); got != DefaultModelChain[0] {
		t.Errorf("PrimaryModel() = %q, want %q", got, DefaultModelChain[0])
	}
	if cfg.EffectiveTimeout() != DefaultTimeout {
		t.Errorf("EffectiveTimeout() = %v, want DefaultTimeout", cfg.EffectiveTimeout())
	}
	if cfg.AuthHeader != DefaultAuthHeader {
		t.Errorf("AuthHeader = %q, want %q", cfg.AuthHeader, DefaultAuthHeader)
	}
	if cfg.ThinkingKwarg != DefaultThinkingKwarg {
		t.Errorf("ThinkingKwarg = %q, want %q", cfg.ThinkingKwarg, DefaultThinkingKwarg)
	}
	if cfg.Thinking != nil {
		t.Errorf("Thinking = %v, want nil so the request body stays unchanged", *cfg.Thinking)
	}
}

// TestConfigDisabledWithoutBaseURL pins rule-only mode (docs/02 §4.2).
func TestConfigDisabledWithoutBaseURL(t *testing.T) {
	cfg, _ := ConfigFromEnv(env(nil))
	if cfg.Enabled() {
		t.Error("Enabled() = true without LLM_BASE_URL")
	}
}

// TestDefaultModelChainIsCurrent guards the trap that this refactor grew out of:
// a retired id left as the in-code default. Deployments set LLM_MODELS, so a
// stale default goes unnoticed until the day something falls back to it.
func TestDefaultModelChainIsCurrent(t *testing.T) {
	for _, m := range DefaultModelChain {
		if strings.Contains(m, "deepseek-v4-flash") {
			t.Errorf("default chain still names %q, retired from the DGX on 2026-09-02", m)
		}
	}
	if len(DefaultModelChain) == 0 {
		t.Fatal("default chain is empty; PrimaryModel would panic")
	}
}

func TestConfigModelChain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  []string
		warns bool
	}{
		{"single", "qwen38-flash-next", []string{"qwen38-flash-next"}, false},
		{"chain", "custom_dgx/qwen38-flash-next, custom_m2", []string{"custom_dgx/qwen38-flash-next", "custom_m2"}, false},
		{"blanks dropped", "a, ,b,", []string{"a", "b"}, false},
		{"only separators", " , ", DefaultModelChain, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, warn := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u", "LLM_MODELS": tc.value}))
			if !reflect.DeepEqual(cfg.Models, tc.want) {
				t.Errorf("Models = %q, want %q", cfg.Models, tc.want)
			}
			if got := len(warn) > 0; got != tc.warns {
				t.Errorf("warnings %v, want any=%v", warn, tc.warns)
			}
			if got := cfg.PrimaryModel(); got != tc.want[0] {
				t.Errorf("PrimaryModel() = %q, want %q", got, tc.want[0])
			}
		})
	}
}

// TestConfigDefaultChainIsCopied stops one run's LLM_MODELS from editing the
// package-level default for the next caller.
func TestConfigDefaultChainIsCopied(t *testing.T) {
	before := append([]string(nil), DefaultModelChain...)
	cfg, _ := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u"}))
	cfg.Models[0] = "mutated"
	if !reflect.DeepEqual(DefaultModelChain, before) {
		t.Errorf("DefaultModelChain became %q, want %q", DefaultModelChain, before)
	}
}

func TestConfigNumericKnobs(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kv          map[string]string
		timeout     time.Duration
		concurrency int
		desc        int
		warns       int
	}{
		{"valid", map[string]string{"LLM_TIMEOUT": "120", "LLM_CONCURRENCY": "8", "LLM_MAX_DESC_CHARS": "2000"},
			120 * time.Second, 8, 2000, 0},
		{"not a number", map[string]string{"LLM_TIMEOUT": "2m", "LLM_CONCURRENCY": "many", "LLM_MAX_DESC_CHARS": "lots"},
			DefaultTimeout, 0, 0, 3},
		{"non-positive", map[string]string{"LLM_TIMEOUT": "0", "LLM_CONCURRENCY": "-1", "LLM_MAX_DESC_CHARS": "0"},
			DefaultTimeout, 0, 0, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.kv["LLM_BASE_URL"] = "u"
			cfg, warn := ConfigFromEnv(env(tc.kv))
			if cfg.EffectiveTimeout() != tc.timeout {
				t.Errorf("EffectiveTimeout() = %v, want %v", cfg.EffectiveTimeout(), tc.timeout)
			}
			if cfg.Concurrency != tc.concurrency {
				t.Errorf("Concurrency = %d, want %d", cfg.Concurrency, tc.concurrency)
			}
			if cfg.MaxDescChars != tc.desc {
				t.Errorf("MaxDescChars = %d, want %d", cfg.MaxDescChars, tc.desc)
			}
			if len(warn) != tc.warns {
				t.Errorf("got %d warnings %v, want %d — a dropped knob must be reported", len(warn), warn, tc.warns)
			}
		})
	}
}

func TestConfigThinking(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kv      map[string]string
		want    *bool
		summary string
		warns   int
	}{
		{"unset", nil, nil, "model default (no chat_template_kwargs sent)", 0},
		{"false", map[string]string{"LLM_THINKING": "false"}, boolPtr(false), "enable_thinking=false", 0},
		{"true", map[string]string{"LLM_THINKING": "true"}, boolPtr(true), "enable_thinking=true", 0},
		{"0 is false", map[string]string{"LLM_THINKING": "0"}, boolPtr(false), "enable_thinking=false", 0},
		{"garbage ignored", map[string]string{"LLM_THINKING": "off"}, nil, "model default (no chat_template_kwargs sent)", 1},
		{"kwarg override", map[string]string{"LLM_THINKING": "false", "LLM_THINKING_KWARG": "thinking"},
			boolPtr(false), "thinking=false", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kv := map[string]string{"LLM_BASE_URL": "u"}
			for k, v := range tc.kv {
				kv[k] = v
			}
			cfg, warn := ConfigFromEnv(env(kv))
			switch {
			case tc.want == nil && cfg.Thinking != nil:
				t.Errorf("Thinking = %v, want nil", *cfg.Thinking)
			case tc.want != nil && cfg.Thinking == nil:
				t.Errorf("Thinking = nil, want %v", *tc.want)
			case tc.want != nil && *cfg.Thinking != *tc.want:
				t.Errorf("Thinking = %v, want %v", *cfg.Thinking, *tc.want)
			}
			if got := cfg.ThinkingSummary(); got != tc.summary {
				t.Errorf("ThinkingSummary() = %q, want %q", got, tc.summary)
			}
			if len(warn) != tc.warns {
				t.Errorf("got %d warnings %v, want %d", len(warn), warn, tc.warns)
			}
		})
	}
}

func TestConfigExtraBody(t *testing.T) {
	t.Run("parsed", func(t *testing.T) {
		cfg, warn := ConfigFromEnv(env(map[string]string{
			"LLM_BASE_URL":   "u",
			"LLM_EXTRA_BODY": `{"top_p":0.8,"chat_template_kwargs":{"foo":true}}`,
		}))
		if len(warn) != 0 {
			t.Errorf("warnings: %v", warn)
		}
		if cfg.ExtraBody["top_p"] != 0.8 {
			t.Errorf("top_p = %#v", cfg.ExtraBody["top_p"])
		}
		if _, ok := cfg.ExtraBody["chat_template_kwargs"].(map[string]any); !ok {
			t.Errorf("chat_template_kwargs = %#v, want a nested object", cfg.ExtraBody["chat_template_kwargs"])
		}
	})

	t.Run("invalid JSON is dropped and reported", func(t *testing.T) {
		cfg, warn := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u", "LLM_EXTRA_BODY": "top_p=0.8"}))
		if cfg.ExtraBody != nil {
			t.Errorf("ExtraBody = %#v, want nil", cfg.ExtraBody)
		}
		if len(warn) != 1 || !strings.Contains(warn[0], "LLM_EXTRA_BODY") {
			t.Errorf("warnings = %v, want one naming LLM_EXTRA_BODY", warn)
		}
	})

	t.Run("reserved keys stripped", func(t *testing.T) {
		cfg, warn := ConfigFromEnv(env(map[string]string{
			"LLM_BASE_URL":   "u",
			"LLM_EXTRA_BODY": `{"model":"hijack","messages":[],"top_p":0.5}`,
		}))
		if _, present := cfg.ExtraBody["model"]; present {
			t.Error("model must not be settable through LLM_EXTRA_BODY")
		}
		if _, present := cfg.ExtraBody["messages"]; present {
			t.Error("messages must not be settable through LLM_EXTRA_BODY")
		}
		if cfg.ExtraBody["top_p"] != 0.5 {
			t.Errorf("top_p = %#v, want the rest of the object kept", cfg.ExtraBody["top_p"])
		}
		if len(warn) != 2 {
			t.Errorf("warnings = %v, want one per stripped key", warn)
		}
	})
}

func TestConfigAuth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kv     map[string]string
		key    string
		header string
		warns  int
	}{
		{"none", nil, "", DefaultAuthHeader, 0},
		{"api key", map[string]string{"LLM_API_KEY": "sk-1"}, "sk-1", DefaultAuthHeader, 0},
		{"custom header", map[string]string{"LLM_API_KEY": "vk-1", "LLM_AUTH_HEADER": "x-api-key"}, "vk-1", "x-api-key", 0},
		{"bifrost alias", map[string]string{"BIFROST_VK": "vk-2"}, "vk-2", "x-bf-vk", 1},
		{"api key wins", map[string]string{"LLM_API_KEY": "sk-1", "BIFROST_VK": "vk-2"}, "sk-1", DefaultAuthHeader, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kv := map[string]string{"LLM_BASE_URL": "u"}
			for k, v := range tc.kv {
				kv[k] = v
			}
			cfg, warn := ConfigFromEnv(env(kv))
			if cfg.APIKey != tc.key {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tc.key)
			}
			if cfg.AuthHeader != tc.header {
				t.Errorf("AuthHeader = %q, want %q", cfg.AuthHeader, tc.header)
			}
			if len(warn) != tc.warns {
				t.Errorf("got %d warnings %v, want %d", len(warn), warn, tc.warns)
			}
		})
	}
}

// TestConfigExtractorsCarryEverything is the seam that matters: a knob parsed
// from the environment but not passed to the extractor is a silent no-op, which
// is the whole failure class this package now guards against.
func TestConfigExtractorsCarryEverything(t *testing.T) {
	cfg, warn := ConfigFromEnv(env(map[string]string{
		"LLM_BASE_URL":       "http://dgx:8000",
		"LLM_MODELS":         "primary,fallback",
		"LLM_API_KEY":        "sk-1",
		"LLM_AUTH_HEADER":    "x-api-key",
		"LLM_TIMEOUT":        "120",
		"LLM_MAX_DESC_CHARS": "1500",
		"LLM_THINKING":       "false",
		"LLM_THINKING_KWARG": "thinking",
		"LLM_EXTRA_BODY":     `{"top_p":0.9}`,
	}))
	if len(warn) != 0 {
		t.Fatalf("warnings: %v", warn)
	}
	got := cfg.Extractors()
	if len(got) != 2 {
		t.Fatalf("built %d extractors, want one per model", len(got))
	}
	for i, want := range []string{"primary", "fallback"} {
		e, ok := got[i].(*OpenAIExtractor)
		if !ok {
			t.Fatalf("extractor %d is %T", i, got[i])
		}
		if e.Model != want {
			t.Errorf("extractor %d model = %q, want %q", i, e.Model, want)
		}
		if e.BaseURL != "http://dgx:8000" || e.APIKey != "sk-1" || e.AuthHeader != "x-api-key" {
			t.Errorf("extractor %d endpoint/auth not carried: %+v", i, e)
		}
		if e.Timeout != 120*time.Second || e.MaxDescChars != 1500 {
			t.Errorf("extractor %d budgets not carried: timeout=%v desc=%d", i, e.Timeout, e.MaxDescChars)
		}
		if e.Thinking == nil || *e.Thinking || e.ThinkingKwarg != "thinking" {
			t.Errorf("extractor %d thinking not carried: %v %q", i, e.Thinking, e.ThinkingKwarg)
		}
		if e.ExtraBody["top_p"] != 0.9 {
			t.Errorf("extractor %d extra body not carried: %#v", i, e.ExtraBody)
		}
	}
}

func TestConfigAttemptsFromRetries(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		warns int
	}{
		{"", DefaultAttempts, 0},
		{"0", 1, 0},
		{"2", 3, 0},
		{"-1", DefaultAttempts, 1},
		{"twice", DefaultAttempts, 1},
	} {
		t.Run("LLM_RETRIES="+tc.value, func(t *testing.T) {
			cfg, warn := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u", "LLM_RETRIES": tc.value}))
			if got := cfg.EffectiveAttempts(); got != tc.want {
				t.Errorf("EffectiveAttempts() = %d, want %d", got, tc.want)
			}
			if len(warn) != tc.warns {
				t.Errorf("warnings = %v, want %d", warn, tc.warns)
			}
		})
	}
}

// TestConfigMaxTokensZeroMeansNoCap: zero is a meaningful value here, unlike the
// other numeric knobs where it is rejected as nonsense.
func TestConfigMaxTokensZeroMeansNoCap(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		warns int
	}{
		{"", DefaultMaxTokens, 0},
		{"0", 0, 0},
		{"2048", 2048, 0},
		{"-1", DefaultMaxTokens, 1},
		{"lots", DefaultMaxTokens, 1},
	} {
		t.Run("LLM_MAX_TOKENS="+tc.value, func(t *testing.T) {
			cfg, warn := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u", "LLM_MAX_TOKENS": tc.value}))
			if cfg.MaxTokens != tc.want {
				t.Errorf("MaxTokens = %d, want %d", cfg.MaxTokens, tc.want)
			}
			if len(warn) != tc.warns {
				t.Errorf("warnings = %v, want %d", warn, tc.warns)
			}
		})
	}
}

// TestPromptVersionTracksPrompt is the invariant that keeps a prompt change from
// being served stale results: PromptVersion is part of the enrich cache key, so
// a custom prompt must never share a version with the built-in one.
func TestPromptVersionTracksPrompt(t *testing.T) {
	version := func(kv map[string]string) string {
		kv["LLM_BASE_URL"] = "u"
		cfg, warn := ConfigFromEnv(env(kv))
		if len(warn) != 0 {
			t.Fatalf("warnings: %v", warn)
		}
		return cfg.EffectivePromptVersion()
	}

	if got := version(map[string]string{}); got != DefaultPromptVersion {
		t.Errorf("no prompt override: version = %q, want %q", got, DefaultPromptVersion)
	}
	// Supplying the built-in prompt verbatim is not a change, so the cache stays.
	if got := version(map[string]string{"LLM_PROMPT": ExtractPrompt}); got != DefaultPromptVersion {
		t.Errorf("built-in prompt passed explicitly: version = %q, want %q", got, DefaultPromptVersion)
	}

	a := version(map[string]string{"LLM_PROMPT": "Return JSON only."})
	b := version(map[string]string{"LLM_PROMPT": "Return JSON only, please."})
	if a == DefaultPromptVersion {
		t.Error("a custom prompt must not reuse the built-in version — the cache would serve results from the old prompt")
	}
	if a == b {
		t.Errorf("two different prompts derived the same version %q", a)
	}
	if again := version(map[string]string{"LLM_PROMPT": "Return JSON only."}); again != a {
		t.Errorf("derivation is not stable: %q then %q", a, again)
	}

	// An explicit version is the operator's call and wins either way.
	if got := version(map[string]string{"LLM_PROMPT": "Return JSON only.", "LLM_PROMPT_VERSION": "v2"}); got != "v2" {
		t.Errorf("explicit version = %q, want v2", got)
	}
}

func TestConfigCarriesPromptAndCap(t *testing.T) {
	cfg, warn := ConfigFromEnv(env(map[string]string{
		"LLM_BASE_URL":   "u",
		"LLM_MODELS":     "m1,m2",
		"LLM_PROMPT":     "Return JSON only.",
		"LLM_MAX_TOKENS": "4096",
	}))
	if len(warn) != 0 {
		t.Fatalf("warnings: %v", warn)
	}
	if got := cfg.PromptSummary(); got != "custom (17 chars)" {
		t.Errorf("PromptSummary() = %q", got)
	}
	for i, ex := range cfg.Extractors() {
		e := ex.(*OpenAIExtractor)
		if e.Prompt != "Return JSON only." {
			t.Errorf("extractor %d prompt = %q", i, e.Prompt)
		}
		if e.MaxTokens != 4096 {
			t.Errorf("extractor %d max tokens = %d", i, e.MaxTokens)
		}
	}
}

func TestConfigEffectiveConcurrency(t *testing.T) {
	cfg, _ := ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u"}))
	if got := cfg.EffectiveConcurrency(); got != DefaultConcurrency {
		t.Errorf("EffectiveConcurrency() = %d, want %d", got, DefaultConcurrency)
	}
	cfg, _ = ConfigFromEnv(env(map[string]string{"LLM_BASE_URL": "u", "LLM_CONCURRENCY": "8"}))
	if got := cfg.EffectiveConcurrency(); got != 8 {
		t.Errorf("EffectiveConcurrency() = %d, want 8", got)
	}
}
