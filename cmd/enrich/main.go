// Command enrich runs rule-layer (always) + LLM-layer (optional) technology
// extraction. It fails open: LLM errors keep rule results and exit 0
// (docs/02 §4.2); JobsSgEnrichBacklog surfaces the backlog.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/llm"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

func main() {
	dataDir := flag.String("data-dir", "/data", "directory holding jobs.db and raw/")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	db, err := store.Open(*dataDir+"/jobs.db", false)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	rows, err := db.LoadTechTaxonomy(context.Background())
	if err != nil {
		slog.Error("load taxonomy", "err", err)
		os.Exit(1)
	}
	tax := tech.LoadTaxonomy(rows)

	// Model ids are backend-specific, so the chain must be configurable:
	// Bifrost routes on provider-prefixed names (custom_dgx/deepseek-v4-flash),
	// while a raw vLLM server serves the bare id (deepseek-v4-flash) and 404s the
	// prefixed form. LLM_MODELS (comma-separated, first = preferred) overrides the
	// default Bifrost chain, so pointing LLM_BASE_URL straight at a vLLM endpoint
	// needs no code change:
	//
	//   LLM_BASE_URL=http://<dgx>:8000  LLM_MODELS=deepseek-v4-flash
	//
	// The default is unchanged, so existing Bifrost deployments are unaffected.
	modelChain := []string{"custom_dgx/deepseek-v4-flash", "custom_m2"}
	if v := os.Getenv("LLM_MODELS"); strings.TrimSpace(v) != "" {
		var models []string
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				models = append(models, m)
			}
		}
		if len(models) > 0 {
			modelChain = models
		}
	}

	var extractor llm.Extractor
	if baseURL := os.Getenv("LLM_BASE_URL"); baseURL != "" {
		// VirtualKey rides in the x-bf-vk header. Bifrost requires it (the
		// governance PreHook applies even in-cluster); a plain vLLM server ignores
		// the unknown header, so leaving it empty for a direct backend is fine.
		vk := os.Getenv("BIFROST_VK")
		var chain []llm.Extractor
		for _, m := range modelChain {
			chain = append(chain, &llm.OpenAIExtractor{BaseURL: baseURL, VirtualKey: vk, Model: m})
		}
		extractor = llm.Chain{Extractors: chain}
		slog.Info("llm enabled", "base_url", baseURL, "models", modelChain)
	} else {
		slog.Info("LLM_BASE_URL unset -> rule-only mode")
	}

	// LLM_CONCURRENCY tunes the bounded fan-out (Run() defaults to 3 when 0).
	// The first run after a baseline scan faces a backlog of thousands, and at
	// ~15s per call concurrency 3 cannot drain it inside a sane activeDeadline;
	// steady-state daily volume is a fraction of that. Keep it modest — the DGX
	// backend is a shared cross-tailnet box, not dedicated capacity.
	concurrency := 0
	if v := strings.TrimSpace(os.Getenv("LLM_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			concurrency = n
		} else {
			slog.Warn("ignoring invalid LLM_CONCURRENCY", "value", v)
		}
	}

	en := &llm.Enricher{
		DB:          db,
		DataDir:     *dataDir,
		Taxonomy:    tax,
		LLM:         extractor,
		Concurrency: concurrency,
		// Provenance recorded on job_tech rows — must name the model that actually
		// ran, so it tracks the chain's first entry rather than a hardcoded id.
		Model:         modelChain[0],
		PromptVersion: "v1",
	}
	res, err := en.Run(context.Background())
	if err != nil {
		slog.Error("enrich failed", "err", err)
		os.Exit(1)
	}
	// fail-open: partial is normal, exit 0
	if res.Status == store.StatusFailed {
		os.Exit(1)
	}
}
