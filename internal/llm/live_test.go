package llm

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveEndpointHonoursThinkingKwarg is the acceptance check to run after a
// model swap. It is skipped unless an endpoint is supplied, so it never runs in
// CI:
//
//	LLM_LIVE_URL=http://<endpoint> LLM_LIVE_MODEL=<id> go test ./internal/llm -run Live -v
//
// It asserts the thing that cannot be seen from a status code: that the
// configured chat-template flag actually stops the model reasoning. A model that
// does not recognise the flag returns 200 and reasons anyway, which is how the
// 2026-09-02 DGX swap cost ~7x throughput without a single error in the logs.
//
// Set LLM_LIVE_KWARG to try a different flag name when this fails; whatever
// makes it pass is the value for LLM_THINKING_KWARG in the manifest.
func TestLiveEndpointHonoursThinkingKwarg(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("LLM_LIVE_URL"))
	if base == "" {
		t.Skip("set LLM_LIVE_URL (and LLM_LIVE_MODEL) to check a real endpoint")
	}
	model := strings.TrimSpace(os.Getenv("LLM_LIVE_MODEL"))
	if model == "" {
		model = DefaultModelChain[0]
	}

	// A description with enough substance that a reasoning model would think
	// about it; a trivial prompt can finish without reasoning either way.
	const desc = `We are hiring a backend engineer to own our order pipeline.
You will design Go services on Kubernetes, move a legacy Java monolith onto
gRPC, tune PostgreSQL and Redis under load, run Kafka consumers, and ship
through Terraform and GitHub Actions on AWS. Familiarity with OpenTelemetry
and with running PyTorch inference behind an API is a plus.`

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	cfg, warn := ConfigFromEnv(func(k string) string {
		switch k {
		case "LLM_BASE_URL":
			return base
		case "LLM_MODELS":
			return model
		case "LLM_THINKING":
			return "false"
		case "LLM_THINKING_KWARG":
			return os.Getenv("LLM_LIVE_KWARG")
		case "LLM_API_KEY":
			return os.Getenv("LLM_LIVE_KEY")
		case "LLM_AUTH_HEADER":
			return os.Getenv("LLM_LIVE_AUTH_HEADER")
		}
		return ""
	})
	if len(warn) != 0 {
		t.Fatalf("config warnings: %v", warn)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.EffectiveTimeout())
	defer cancel()

	start := time.Now()
	res, err := Chain{Extractors: cfg.Extractors()}.Extract(ctx, "Senior Backend Engineer", desc)
	if err != nil {
		t.Fatalf("Extract against %s (%s): %v", base, cfg.PrimaryModel(), err)
	}
	elapsed := time.Since(start)

	if got := logs.String(); strings.Contains(got, "reasoning was not disabled") {
		t.Errorf("%s ignored %s — try another flag name via LLM_LIVE_KWARG and set "+
			"LLM_THINKING_KWARG to whatever works:\n%s", cfg.PrimaryModel(), cfg.ThinkingSummary(), got)
	}
	if len(res.All()) == 0 {
		t.Errorf("no technologies extracted; the prompt or the model is broken: %+v", res)
	}
	// Not an assertion: latency varies with what else shares the GPU. Printed so
	// a swap can be recalibrated against docs/09 §3.2 and DefaultTimeout.
	t.Logf("%s: %v with %s, extracted %d terms", cfg.PrimaryModel(), elapsed.Round(time.Millisecond),
		cfg.ThinkingSummary(), len(res.All()))
}
